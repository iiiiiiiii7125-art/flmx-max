package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"go-backend/internal/http/response"
)

// failedForward tracks a forward that failed redeployment, for retry.
type failedForward struct {
	id      int64
	forward *forwardRecord
	err     error
}

// githubRepo is where releases (node installer, agent binaries, panel
// compose files) are fetched from. Release builds set it to the repository
// that built them via -ldflags "-X go-backend/internal/http/handler.githubRepo=owner/repo".
var githubRepo = "iiiiiiiii7125-art/flmx-max"

// githubAPIBase / githubHTMLBase 是 var 以便测试指向本地 stub 服务器。
var (
	githubAPIBase  = "https://api.github.com"
	githubHTMLBase = "https://github.com"
)

const (
	upgradeTimeout = 5 * time.Minute
	batchWorkers   = 5

	releaseChannelStable = "stable"
	releaseChannelDev    = "dev"

	defaultGithubProxyEnabled = true
	defaultGithubProxyURL     = "https://gcode.hostcentral.cc"
)

var (
	stableVersionPattern = regexp.MustCompile(`^\d+(?:\.\d+)+$`)
	testKeywordPattern   = regexp.MustCompile(`(?i)(alpha|beta|rc)`)
)

const nodeOnlineRedeployCooldown = 30 * time.Second

type githubRelease struct {
	TagName     string `json:"tag_name"`
	Name        string `json:"name"`
	PublishedAt string `json:"published_at"`
	Prerelease  bool   `json:"prerelease"`
	Draft       bool   `json:"draft"`
}

func normalizeReleaseChannel(channel string) string {
	switch strings.ToLower(strings.TrimSpace(channel)) {
	case releaseChannelDev:
		return releaseChannelDev
	default:
		return releaseChannelStable
	}
}

func releaseChannelFromTag(tag string) string {
	normalized := strings.ToLower(strings.TrimSpace(tag))
	if normalized == "" {
		return releaseChannelDev
	}
	if testKeywordPattern.MatchString(normalized) {
		return releaseChannelDev
	}
	if stableVersionPattern.MatchString(normalized) {
		return releaseChannelStable
	}

	return releaseChannelDev
}

func releaseChannelLabel(channel string) string {
	if normalizeReleaseChannel(channel) == releaseChannelDev {
		return "测试版"
	}

	return "正式版"
}

func (h *Handler) getGithubProxyConfig() (enabled bool, proxyURL string) {
	enabled = defaultGithubProxyEnabled
	proxyURL = defaultGithubProxyURL

	if h == nil || h.repo == nil {
		return
	}

	if enabledCfg, err := h.repo.GetConfigByName("github_proxy_enabled"); err == nil && enabledCfg != nil {
		enabled = enabledCfg.Value != "false"
	}

	if urlCfg, err := h.repo.GetConfigByName("github_proxy_url"); err == nil && urlCfg != nil && urlCfg.Value != "" {
		proxyURL = strings.TrimSpace(urlCfg.Value)
		if !strings.HasPrefix(proxyURL, "http://") && !strings.HasPrefix(proxyURL, "https://") {
			proxyURL = "https://" + proxyURL
		}
		// U2-P3-4：去全部尾斜杠（旧逻辑 TrimSuffix 只去一个，双尾斜杠会拼出双斜杠路径）
		proxyURL = strings.TrimRight(proxyURL, "/")
	}

	return
}

// S-02: Agent 版本号严格校验，防止路径穿越
// 只允许 ^v?[0-9]+\.[0-9]+\.[0-9]+$ 格式
var agentVersionRe = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+$`)

func isValidAgentVersion(v string) bool {
	return agentVersionRe.MatchString(v)
}

// ---- GitHub 版本列表缓存 ----
// 未认证 GitHub API 限流 60 次/小时/IP（面板出口常为共享 NAT，更易耗尽）。
// 版本列表不需要实时：成功结果缓存 10 分钟，节点升级下拉、系统检查更新、
// 页脚探测共用同一份配额预算。
const releasesCacheTTL = 10 * time.Minute

var releasesCache = struct {
	sync.Mutex
	entries map[string]releasesCacheEntry
}{entries: map[string]releasesCacheEntry{}}

type releasesCacheEntry struct {
	at       time.Time
	releases []githubRelease
}

func cachedReleases(key string) ([]githubRelease, bool) {
	releasesCache.Lock()
	defer releasesCache.Unlock()
	e, ok := releasesCache.entries[key]
	if !ok || time.Since(e.at) >= releasesCacheTTL {
		return nil, false
	}
	return e.releases, true
}

func storeReleasesCache(key string, releases []githubRelease) {
	releasesCache.Lock()
	defer releasesCache.Unlock()
	releasesCache.entries[key] = releasesCacheEntry{at: time.Now(), releases: releases}
}

func sliceReleases(releases []githubRelease, perPage int) []githubRelease {
	if perPage > 0 && len(releases) > perPage {
		return releases[:perPage]
	}
	return releases
}

// githubProxyURLForFetch 返回版本发现类请求应使用的代理前缀；
// 代理未启用时返回 ""（调用方只直连）。
func (h *Handler) githubProxyURLForFetch() string {
	if enabled, proxyURL := h.getGithubProxyConfig(); enabled {
		return proxyURL
	}
	return ""
}

// maxGitHubAPIResponseBytes 是版本列表解码的体积兜底（防异常端点无限
// 灌数据），正常 releases 负载远低于此，与内核版本列表的 256MiB 同口径。
const maxGitHubAPIResponseBytes = 256 << 20

func fetchGitHubReleases(perPage int) ([]githubRelease, error) {
	return fetchGitHubReleasesWithProxy(perPage, "")
}

// F-029：节点侧版本列表拉取此前只有直连一条路，与面板侧
// fetchSystemUpgradeReleases（U1-001 已有代理→直连降级）口径不一致：
// gh-proxy 类服务不转发 api.github.com 时节点升级下拉整体失效；纯直连
// 又在代理必经的网络里连不上。现在同口径：传了 proxyURL 先走代理
// （proxyURL + "/" + apiURL），失败降级直连；无代理时与旧行为等价。
func fetchGitHubReleasesWithProxy(perPage int, proxyURL string) ([]githubRelease, error) {
	if perPage <= 0 {
		perPage = 20
	}
	cacheKey := "api:" + githubAPIBase
	if releases, ok := cachedReleases(cacheKey); ok {
		return sliceReleases(releases, perPage), nil
	}

	directURL := fmt.Sprintf("%s/repos/%s/releases?per_page=%d", githubAPIBase, githubRepo, 50)
	if proxyURL == "" {
		releases, err := fetchGitHubReleasesPage(directURL)
		if err != nil {
			return nil, err
		}
		storeReleasesCache(cacheKey, releases)
		return sliceReleases(releases, perPage), nil
	}

	releases, proxyErr := fetchGitHubReleasesPage(proxyURL + "/" + directURL)
	if proxyErr == nil {
		storeReleasesCache(cacheKey, releases)
		return sliceReleases(releases, perPage), nil
	}
	releases, directErr := fetchGitHubReleasesPage(directURL)
	if directErr != nil {
		return nil, fmt.Errorf("代理与直连均失败（代理: %v；直连: %v）", proxyErr, directErr)
	}
	storeReleasesCache(cacheKey, releases)
	return sliceReleases(releases, perPage), nil
}

func fetchGitHubReleasesPage(url string) ([]githubRelease, error) {
	// X1-403：超时 15s → 30s（经代理多一跳 + 大响应，口径同 U1-006）。
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("请求GitHub API失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		if resp.StatusCode == http.StatusForbidden && strings.Contains(string(body), "rate limit") {
			return nil, fmt.Errorf("GitHub API 已限流（未认证额度 60 次/小时/IP），请稍后再试")
		}
		return nil, fmt.Errorf("GitHub API返回 %d: %s", resp.StatusCode, string(body))
	}

	var releases []githubRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxGitHubAPIResponseBytes)).Decode(&releases); err != nil {
		return nil, fmt.Errorf("解析GitHub API响应失败: %v", err)
	}
	return releases, nil
}

// resolveLatestTagByRedirect 通过 github.com 的 releases/latest 重定向
// 解析最新稳定版 tag。该端点不走 API、不消耗限流配额，用作 API 不可用
// （限流等）时"最新版"解析的兜底。
func resolveLatestTagByRedirect() (string, error) {
	return resolveLatestTagByRedirectWithProxy("")
}

// F-029：重定向解析同样补代理先行/直连降级——stable 渠道的 API 兜底
// 若只直连，代理必经网络里 API 与重定向会一起失效，兜底形同虚设。
func resolveLatestTagByRedirectWithProxy(proxyURL string) (string, error) {
	directURL := githubHTMLBase + "/" + githubRepo + "/releases/latest"
	if proxyURL == "" {
		return fetchLatestTagByRedirect(directURL)
	}
	tag, proxyErr := fetchLatestTagByRedirect(proxyURL + "/" + directURL)
	if proxyErr == nil {
		return tag, nil
	}
	tag, directErr := fetchLatestTagByRedirect(directURL)
	if directErr != nil {
		return "", fmt.Errorf("代理与直连均失败（代理: %v；直连: %v）", proxyErr, directErr)
	}
	return tag, nil
}

func fetchLatestTagByRedirect(url string) (string, error) {
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	loc := resp.Header.Get("Location")
	const marker = "/releases/tag/"
	i := strings.Index(loc, marker)
	if i < 0 {
		return "", fmt.Errorf("无法从重定向解析最新版本: %q", loc)
	}
	tag := strings.TrimSpace(loc[i+len(marker):])
	if q := strings.IndexAny(tag, "?#"); q >= 0 {
		tag = tag[:q]
	}
	if tag == "" {
		return "", fmt.Errorf("无法从重定向解析最新版本: %q", loc)
	}
	return tag, nil
}

func resolveLatestReleaseByChannel(channel string) (string, error) {
	return resolveLatestReleaseByChannelWithProxy(channel, "")
}

// F-029：渠道最新版解析的代理感知版（机制同 fetchGitHubReleasesWithProxy）。
func resolveLatestReleaseByChannelWithProxy(channel string, proxyURL string) (string, error) {
	normalizedChannel := normalizeReleaseChannel(channel)
	releases, err := fetchGitHubReleasesWithProxy(50, proxyURL)
	if err != nil {
		// API 不可用（限流等）时稳定版回退到重定向解析，不依赖 API 配额。
		if normalizedChannel == releaseChannelStable {
			if tag, ferr := resolveLatestTagByRedirectWithProxy(proxyURL); ferr == nil {
				return tag, nil
			}
		}
		return "", err
	}

	for _, r := range releases {
		if r.Draft {
			continue
		}
		tag := strings.TrimSpace(r.TagName)
		if tag == "" {
			continue
		}
		if releaseChannelFromTag(tag) == normalizedChannel {
			return tag, nil
		}
	}

	return "", fmt.Errorf("未找到%s版本号", releaseChannelLabel(normalizedChannel))
}

func (h *Handler) nodeUpgrade(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}

	var req struct {
		ID      int64  `json:"id"`
		Version string `json:"version"`
		Channel string `json:"channel"`
	}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	if req.ID <= 0 {
		response.WriteJSON(w, response.ErrDefault("节点ID无效"))
		return
	}

	channel := normalizeReleaseChannel(req.Channel)
	version := strings.TrimSpace(req.Version)
	if version == "" {
		var err error
		version, err = resolveLatestReleaseByChannelWithProxy(channel, h.githubProxyURLForFetch())
		if err != nil {
			response.WriteJSON(w, response.Err(-2, fmt.Sprintf("获取最新%s失败: %v", releaseChannelLabel(channel), err)))
			return
		}
	}

	// S-02: 严格校验版本号格式，防止路径穿越
	if !isValidAgentVersion(version) {
		response.WriteJSON(w, response.ErrDefault("版本号格式无效"))
		return
	}

	// O9-005：目标版本低于节点当前版本时拒绝升级（防误降级；有意降级请用独立的
	// nodeRollback 接口）。节点版本未知（未上报）时不拦截。
	if node, err := h.repo.GetNodeByID(req.ID); err == nil && node != nil && node.Version.Valid {
		if cur := strings.TrimSpace(node.Version.String); cur != "" {
			// A11-132-06：不可比版本 fail-closed（与面板自升级口径一致）。
			cmp, cmpErr := compareVersions(strings.TrimPrefix(version, "v"), strings.TrimPrefix(cur, "v"))
			if cmpErr != nil {
				response.WriteJSON(w, response.ErrDefault(fmt.Sprintf("版本号 %s 或节点当前版本 %s 无法比较，已拒绝: %v", version, cur, cmpErr)))
				return
			}
			if cmp < 0 {
				response.WriteJSON(w, response.ErrDefault(fmt.Sprintf("目标版本 %s 低于节点当前版本 %s，已拒绝。如确需降级请使用节点回滚功能。", version, cur)))
				return
			}
		}
	}

	// S-02: 面板不再发送 downloadUrl/checksumURL，只发送 version
	// Agent 侧用编译期常量仓库 + 严格校验的版本号本地拼 URL
	// 代理支持：系统设置开启 GitHub 代理时下发 proxyUrl 前缀（与内核下载同模式），
	// agent 仅作前缀拼接，路径仍由其本地构造。
	upgradeData := map[string]interface{}{
		"version": version,
	}
	if enabled, proxyURL := h.getGithubProxyConfig(); enabled {
		upgradeData["proxyUrl"] = proxyURL
	}
	result, err := h.wsServer.SendCommand(req.ID, "UpgradeAgent", upgradeData, upgradeTimeout)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, fmt.Sprintf("升级失败: %v", err)))
		return
	}
	h.markNodePendingUpgradeRedeploy(req.ID)

	response.WriteJSON(w, response.OK(map[string]interface{}{
		"version": version,
		"message": result.Message,
	}))
}

func resolveLatestRelease() (string, error) {
	return resolveLatestReleaseByChannel(releaseChannelStable)
}

func resolveLatestReleaseAPI() (string, error) {
	return resolveLatestReleaseByChannel(releaseChannelStable)
}

func (h *Handler) nodeBatchUpgrade(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}

	var req struct {
		IDs     []int64 `json:"ids"`
		Version string  `json:"version"`
		Channel string  `json:"channel"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	if len(req.IDs) == 0 {
		response.WriteJSON(w, response.ErrDefault("ids不能为空"))
		return
	}

	channel := normalizeReleaseChannel(req.Channel)
	version := strings.TrimSpace(req.Version)
	if version == "" {
		var err error
		version, err = resolveLatestReleaseByChannelWithProxy(channel, h.githubProxyURLForFetch())
		if err != nil {
			response.WriteJSON(w, response.Err(-2, fmt.Sprintf("获取最新%s失败: %v", releaseChannelLabel(channel), err)))
			return
		}
	}

	// S-02: 严格校验版本号格式，防止路径穿越
	if !isValidAgentVersion(version) {
		response.WriteJSON(w, response.ErrDefault("版本号格式无效"))
		return
	}

	// S-02: 面板不再发送 downloadUrl/checksumURL，只发送 version

	// 代理支持：系统设置开启 GitHub 代理时下发 proxyUrl 前缀（与单节点升级同模式）。
	// 在循环外计算一次，避免每个 goroutine 重复读配置。
	batchUpgradeData := map[string]interface{}{
		"version": version,
	}
	if enabled, proxyURL := h.getGithubProxyConfig(); enabled {
		batchUpgradeData["proxyUrl"] = proxyURL
	}

	type upgradeResult struct {
		ID      int64  `json:"id"`
		Success bool   `json:"success"`
		Message string `json:"message"`
	}

	results := make([]upgradeResult, len(req.IDs))
	sem := make(chan struct{}, batchWorkers)
	var wg sync.WaitGroup

	for i, id := range req.IDs {
		// L-001：先获取信号量再启动 goroutine，避免大量 IDs 时 goroutine 数 = IDs 数导致内存 spike
		// 旧逻辑：先启动所有 goroutine，信号量在内部限制并发，1000 个 IDs 就创建 1000 个 goroutine
		sem <- struct{}{}
		wg.Add(1)
		go func(index int, nodeID int64) {
			defer wg.Done()
			defer func() { <-sem }()

			// O9-005：目标版本低于该节点当前版本时拒绝（防误降级；有意降级请用 nodeRollback）
			// A11-132-06：不可比版本同样 fail-closed 拒绝。
			if node, err := h.repo.GetNodeByID(nodeID); err == nil && node != nil && node.Version.Valid {
				if cur := strings.TrimSpace(node.Version.String); cur != "" {
					cmp, cmpErr := compareVersions(strings.TrimPrefix(version, "v"), strings.TrimPrefix(cur, "v"))
					if cmpErr != nil {
						results[index] = upgradeResult{ID: nodeID, Success: false, Message: fmt.Sprintf("版本号 %s 或节点当前版本 %s 无法比较，已拒绝: %v", version, cur, cmpErr)}
						return
					}
					if cmp < 0 {
						results[index] = upgradeResult{ID: nodeID, Success: false, Message: fmt.Sprintf("目标版本 %s 低于节点当前版本 %s，已拒绝（降级请用回滚功能）", version, cur)}
						return
					}
				}
			}

			result, err := h.wsServer.SendCommand(nodeID, "UpgradeAgent", batchUpgradeData, upgradeTimeout)
			if err != nil {
				results[index] = upgradeResult{ID: nodeID, Success: false, Message: err.Error()}
				return
			}
			h.markNodePendingUpgradeRedeploy(nodeID)
			results[index] = upgradeResult{ID: nodeID, Success: true, Message: result.Message}
		}(i, id)
	}
	wg.Wait()

	response.WriteJSON(w, response.OK(map[string]interface{}{
		"version": version,
		"results": results,
	}))
}

func (h *Handler) listReleases(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}

	var req struct {
		Channel string `json:"channel"`
	}
	if err := decodeJSON(r.Body, &req); err != nil && err != io.EOF {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}

	channel := normalizeReleaseChannel(req.Channel)

	releases, err := fetchGitHubReleasesWithProxy(50, h.githubProxyURLForFetch())
	if err != nil {
		response.WriteJSON(w, response.Err(-2, fmt.Sprintf("获取版本列表失败: %v", err)))
		return
	}

	type releaseItem struct {
		Version     string `json:"version"`
		Name        string `json:"name"`
		PublishedAt string `json:"publishedAt"`
		Prerelease  bool   `json:"prerelease"`
		Channel     string `json:"channel"`
	}

	items := make([]releaseItem, 0, len(releases))
	for _, r := range releases {
		if r.Draft {
			continue
		}
		tag := strings.TrimSpace(r.TagName)
		if tag == "" {
			continue
		}
		itemChannel := releaseChannelFromTag(tag)
		if itemChannel != channel {
			continue
		}
		items = append(items, releaseItem{
			Version:     tag,
			Name:        r.Name,
			PublishedAt: r.PublishedAt,
			Prerelease:  itemChannel == releaseChannelDev,
			Channel:     itemChannel,
		})
	}

	response.WriteJSON(w, response.OK(items))
}

func (h *Handler) nodeRollback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}

	var req struct {
		ID int64 `json:"id"`
	}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	if req.ID <= 0 {
		response.WriteJSON(w, response.ErrDefault("节点ID无效"))
		return
	}

	result, err := h.wsServer.SendCommand(req.ID, "RollbackAgent", map[string]interface{}{}, 30*time.Second)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, fmt.Sprintf("回退失败: %v", err)))
		return
	}

	response.WriteJSON(w, response.OK(map[string]interface{}{
		"message": result.Message,
	}))
}

func (h *Handler) markNodePendingUpgradeRedeploy(nodeID int64) {
	if h == nil || nodeID <= 0 {
		return
	}
	h.upgradeMu.Lock()
	h.pendingUpgradeRedeploy[nodeID] = struct{}{}
	h.upgradeMu.Unlock()
}

func (h *Handler) consumeNodePendingUpgradeRedeploy(nodeID int64) bool {
	if h == nil || nodeID <= 0 {
		return false
	}
	h.upgradeMu.Lock()
	_, ok := h.pendingUpgradeRedeploy[nodeID]
	if ok {
		delete(h.pendingUpgradeRedeploy, nodeID)
	}
	h.upgradeMu.Unlock()
	return ok
}

func (h *Handler) onNodeOnline(nodeID int64) {
	if h == nil || h.repo == nil || h.wsServer == nil {
		return
	}
	h.notifyNodeOnline(nodeID)
	if node, err := h.getNodeRecord(nodeID); err == nil && (node == nil || node.Status != 1) {
		return // The next connection will resume any pending reconciliation.
	}
	go h.reconcileNodeKernels(nodeID)
	if !h.startNodeOnlineRedeploy(nodeID, time.Now()) {
		return
	}
	defer h.finishNodeOnlineRedeploy(nodeID)

	// A fresh agent needs a full restore. Shared failures remain persisted and
	// are retried by maintenance without restarting acknowledged services.
	h.reconcileSharedNodeRuntime(nodeID)
	if !h.redeployLocalNodeRuntime(nodeID) {
		h.scheduleNodeLocalRuntimeRetry(nodeID)
	} else {
		h.clearNodeLocalRuntimeRetry(nodeID)
	}
}

// Local retries are separate from reconnect reconciliation: a failed local
// forward must not cause every shared listener to be replayed every 30 seconds.
func (h *Handler) scheduleNodeLocalRuntimeRetry(nodeID int64) {
	h.upgradeMu.Lock()
	defer h.upgradeMu.Unlock()
	if h.nodeLocalRuntimeRetryQueued == nil {
		h.nodeLocalRuntimeRetryQueued = make(map[int64]struct{})
	}
	if _, queued := h.nodeLocalRuntimeRetryQueued[nodeID]; queued {
		return
	}
	h.nodeLocalRuntimeRetryQueued[nodeID] = struct{}{}
	time.AfterFunc(nodeOnlineRedeployCooldown, func() {
		h.upgradeMu.Lock()
		_, queued := h.nodeLocalRuntimeRetryQueued[nodeID]
		delete(h.nodeLocalRuntimeRetryQueued, nodeID)
		h.upgradeMu.Unlock()
		if !queued {
			return
		}
		h.retryNodeLocalRuntime(nodeID)
	})
}

func (h *Handler) clearNodeLocalRuntimeRetry(nodeID int64) {
	h.upgradeMu.Lock()
	delete(h.nodeLocalRuntimeRetryQueued, nodeID)
	h.upgradeMu.Unlock()
}

func (h *Handler) retryNodeLocalRuntime(nodeID int64) {
	if h == nil || h.repo == nil || h.wsServer == nil {
		return
	}
	if node, err := h.getNodeRecord(nodeID); err == nil && (node == nil || node.Status != 1) {
		return
	}
	h.upgradeMu.Lock()
	if _, inFlight := h.nodeOnlineRedeploying[nodeID]; inFlight {
		h.upgradeMu.Unlock()
		h.scheduleNodeLocalRuntimeRetry(nodeID)
		return
	}
	if h.nodeOnlineRedeploying == nil {
		h.nodeOnlineRedeploying = make(map[int64]struct{})
	}
	h.nodeOnlineRedeploying[nodeID] = struct{}{}
	h.upgradeMu.Unlock()
	defer h.finishNodeOnlineRedeploy(nodeID)
	if !h.redeployLocalNodeRuntime(nodeID) {
		h.scheduleNodeLocalRuntimeRetry(nodeID)
	} else {
		h.clearNodeLocalRuntimeRetry(nodeID)
	}
}

func (h *Handler) startNodeOnlineRedeploy(nodeID int64, now time.Time) bool {
	if h == nil || nodeID <= 0 {
		return false
	}
	if now.IsZero() {
		now = time.Now()
	}

	h.upgradeMu.Lock()
	defer h.upgradeMu.Unlock()
	if h.pendingUpgradeRedeploy == nil {
		h.pendingUpgradeRedeploy = make(map[int64]struct{})
	}
	if h.nodeOnlineRedeployAt == nil {
		h.nodeOnlineRedeployAt = make(map[int64]time.Time)
	}
	if h.nodeOnlineRedeployQueued == nil {
		h.nodeOnlineRedeployQueued = make(map[int64]struct{})
	}
	if h.nodeOnlineRedeploying == nil {
		h.nodeOnlineRedeploying = make(map[int64]struct{})
	}

	_, pendingUpgrade := h.pendingUpgradeRedeploy[nodeID]
	lastRedeployAt := h.nodeOnlineRedeployAt[nodeID]
	_, inFlight := h.nodeOnlineRedeploying[nodeID]
	if fireAt, start := nextNodeOnlineRedeployFireAt(lastRedeployAt, now, pendingUpgrade, inFlight); !start {
		h.queueNodeOnlineRedeployLocked(nodeID, fireAt)
		return false
	}

	delete(h.pendingUpgradeRedeploy, nodeID)
	h.nodeOnlineRedeployAt[nodeID] = now
	h.nodeOnlineRedeploying[nodeID] = struct{}{}
	return true
}

func nextNodeOnlineRedeployFireAt(lastRedeployAt, now time.Time, pendingUpgrade bool, inFlight bool) (time.Time, bool) {
	if now.IsZero() {
		now = time.Now()
	}
	if inFlight {
		fireAt := now.Add(nodeOnlineRedeployCooldown)
		if !lastRedeployAt.IsZero() {
			cooldownAt := lastRedeployAt.Add(nodeOnlineRedeployCooldown)
			if cooldownAt.After(now) {
				fireAt = cooldownAt
			}
		}
		return fireAt, false
	}
	if !pendingUpgrade && !lastRedeployAt.IsZero() && now.Sub(lastRedeployAt) < nodeOnlineRedeployCooldown {
		return lastRedeployAt.Add(nodeOnlineRedeployCooldown), false
	}
	return time.Time{}, true
}

func (h *Handler) queueNodeOnlineRedeployLocked(nodeID int64, fireAt time.Time) {
	if h == nil || nodeID <= 0 {
		return
	}
	if h.nodeOnlineRedeployQueued == nil {
		h.nodeOnlineRedeployQueued = make(map[int64]struct{})
	}
	if _, queued := h.nodeOnlineRedeployQueued[nodeID]; queued {
		return
	}
	if fireAt.IsZero() {
		fireAt = time.Now().Add(nodeOnlineRedeployCooldown)
	}
	delay := time.Until(fireAt)
	if delay < 0 {
		delay = 0
	}
	h.nodeOnlineRedeployQueued[nodeID] = struct{}{}
	time.AfterFunc(delay, func() {
		h.upgradeMu.Lock()
		delete(h.nodeOnlineRedeployQueued, nodeID)
		h.upgradeMu.Unlock()
		h.onNodeOnline(nodeID)
	})
}

func (h *Handler) finishNodeOnlineRedeploy(nodeID int64) {
	if h == nil || nodeID <= 0 {
		return
	}
	h.upgradeMu.Lock()
	delete(h.nodeOnlineRedeploying, nodeID)
	h.upgradeMu.Unlock()
}

func (h *Handler) redeployNodeRuntimeAfterUpgrade(nodeID int64) bool {
	sharedOK := h.reconcileSharedNodeRuntime(nodeID)
	localOK := h.redeployLocalNodeRuntime(nodeID)
	return sharedOK && localOK
}

func (h *Handler) reconcileSharedNodeRuntime(nodeID int64) bool {
	succeeded := true
	if err := h.reconcilePeerShareResourcesOnNode(nodeID); err != nil {
		fmt.Printf("reconnect shared resource reconciliation failed on node %d: %v\n", nodeID, err)
		succeeded = false
	}
	if err := h.reconcilePeerShareRoleRuntimesOnNode(nodeID); err != nil {
		fmt.Printf("reconnect shared role reconciliation failed on node %d: %v\n", nodeID, err)
		succeeded = false
	}
	return succeeded
}

func (h *Handler) redeployLocalNodeRuntime(nodeID int64) bool {
	tunnelIDs, err := h.repo.ListActiveTunnelIDsByNode(nodeID)
	if err != nil {
		fmt.Printf("post-upgrade redeploy: list tunnels for node %d failed: %v\n", nodeID, err)
		return false
	}
	forwardIDs, err := h.repo.ListForwardIDsByNode(nodeID)
	if err != nil {
		fmt.Printf("post-upgrade redeploy: list forwards for node %d failed: %v\n", nodeID, err)
		return false
	}

	// First pass: deploy everything
	tunnelFailed := make(map[int64]struct{})
	for _, tunnelID := range tunnelIDs {
		if err := h.redeployTunnelAndForwards(tunnelID); err != nil {
			tunnelFailed[tunnelID] = struct{}{}
			fmt.Printf("post-upgrade redeploy: tunnel %d failed on node %d: %v\n", tunnelID, nodeID, err)
		}
	}

	// Collect forwards that failed independently (not skipped due to tunnel failure)
	var failedForwards []failedForward

	for _, forwardID := range forwardIDs {
		forward, getErr := h.getForwardRecord(forwardID)
		if getErr != nil || forward == nil {
			continue
		}
		if _, skipped := tunnelFailed[forward.TunnelID]; skipped {
			continue
		}
		if err := h.syncForwardServices(forward, "UpdateService", true); err != nil {
			failedForwards = append(failedForwards, failedForward{id: forwardID, forward: forward, err: err})
			fmt.Printf("post-upgrade redeploy: forward %d failed on node %d: %v\n", forwardID, nodeID, err)
		}
	}

	// Retry failed items with exponential backoff (max 3 attempts)
	return h.retryFailedRedeploys(nodeID, tunnelFailed, failedForwards)
}

// isRetryableError returns true if the error looks transient and worth retrying.
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	// Skip non-retryable errors: not-found, already-exists, validation errors
	if strings.Contains(msg, "not found") || strings.Contains(msg, "不存在") {
		return false
	}
	if strings.Contains(msg, "already exists") || strings.Contains(msg, "已存在") {
		return false
	}
	// Everything else (timeout, connection lost, port in use, etc.) is retryable
	return true
}

// retryFailedRedeploys retries failed tunnels and forwards with exponential backoff.
func (h *Handler) retryFailedRedeploys(nodeID int64, tunnelFailed map[int64]struct{}, failedForwards []failedForward) bool {
	if len(tunnelFailed) == 0 && len(failedForwards) == 0 {
		return true
	}

	permanentFailure := false
	const maxRetries = 3
	baseDelay := time.Second

	for attempt := 1; attempt <= maxRetries; attempt++ {
		delay := baseDelay * time.Duration(1<<uint(attempt-1)) // 1s, 2s, 4s
		time.Sleep(delay)

		// Retry failed tunnels
		for tunnelID := range tunnelFailed {
			if err := h.redeployTunnelAndForwards(tunnelID); err == nil {
				delete(tunnelFailed, tunnelID)
				fmt.Printf("post-upgrade redeploy retry: tunnel %d succeeded on node %d (attempt %d)\n", tunnelID, nodeID, attempt)
			} else if !isRetryableError(err) {
				permanentFailure = true
				delete(tunnelFailed, tunnelID) // Preserve failure while avoiding immediate retries.
			} else {
				fmt.Printf("post-upgrade redeploy retry: tunnel %d still failing on node %d (attempt %d): %v\n", tunnelID, nodeID, attempt, err)
			}
		}

		// Retry failed forwards
		var stillFailed []failedForward
		for _, ff := range failedForwards {
			if _, skipped := tunnelFailed[ff.forward.TunnelID]; skipped {
				stillFailed = append(stillFailed, ff) // Tunnel still failed, skip forward
				continue
			}
			if err := h.syncForwardServices(ff.forward, "UpdateService", true); err == nil {
				fmt.Printf("post-upgrade redeploy retry: forward %d succeeded on node %d (attempt %d)\n", ff.id, nodeID, attempt)
			} else if !isRetryableError(err) {
				permanentFailure = true // Keep reconciliation pending for a later reconnect.
			} else {
				stillFailed = append(stillFailed, ff)
				fmt.Printf("post-upgrade redeploy retry: forward %d still failing on node %d (attempt %d): %v\n", ff.id, nodeID, attempt, err)
			}
		}
		failedForwards = stillFailed

		if len(tunnelFailed) == 0 && len(failedForwards) == 0 {
			fmt.Printf("post-upgrade redeploy retry: all items recovered on node %d\n", nodeID)
			return !permanentFailure
		}
	}

	// Final summary
	for tunnelID := range tunnelFailed {
		fmt.Printf("post-upgrade redeploy: tunnel %d permanently failed on node %d after retries\n", tunnelID, nodeID)
	}
	for _, ff := range failedForwards {
		fmt.Printf("post-upgrade redeploy: forward %d permanently failed on node %d after retries\n", ff.id, nodeID)
	}
	return false
}
