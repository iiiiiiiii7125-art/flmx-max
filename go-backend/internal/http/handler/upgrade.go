package handler

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"go-backend/internal/http/response"
	"go-backend/internal/ws"
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

	// maxBatchUpgradeNodes 是单次批量升级的节点数上限（B05-002 服务
	// 端收口，1.7.7 由 25 抬到 50）：上限只防超大请求在小内存面板上
	// 让 goroutine 与待回执命令无界增长（L-001 内存 spike 前科），
	// 无正确性隐患。派发为一波全下发（1.7.7 用户口径：批量命令本
	// 就应同时下发到各机并行执行，不再按 batchWorkers=5 分波等待），
	// 后端界 = 版本发现前奏（≤120s，空 version 时）+ 单节点
	// upgradeTimeout(300s) ≈ 420s，远小于服务端写截止
	// nodeBatchUpgradeWriteDeadline(1800s)；超限请求明确报错并提示
	// 分批（前端超时公式仍按旧波次口径放大，为安全余量，见登记表
	// T031 注）。
	maxBatchUpgradeNodes = 50

	releaseChannelStable = "stable"
	releaseChannelDev    = "dev"
)

var (
	stableVersionPattern = regexp.MustCompile(`^\d+(?:\.\d+)+$`)
	testKeywordPattern   = regexp.MustCompile(`(?i)(alpha|beta|rc)`)
)

const nodeOnlineRedeployCooldown = 30 * time.Second

// --- 面板源分发二期：下发前预热 + 期望哈希 ---

// agentReleaseArches 是本产品 release 的全部 agent 架构。预热两
// 架构都做（免去逐节点辨认与竞态，多花的只是一次 ~9.4MB 回源，
// 且同版本全网只发生一次）。
var agentReleaseArches = []string{"amd64", "arm64"}

// upgradePreheatHashWait 是下发为取期望哈希最多等待预热落地的
// 时间。等待只覆盖「哈希已缓存或回源很快」的常态；超时、失败
// 一律不带哈希下发（agent 回落同源 .sha256 比对），绝不阻塞、绝
// 不取消下发。登记表 T005/T031 的后端界已把这 15s 计入（界
// 420s→435s，仍小于 480s 写截止）。var 以便测试收缩。
var upgradePreheatHashWait = 15 * time.Second

// preheatWG 跟踪全部后台预热 goroutine（warmAgentReleaseAssets 与
// 期望哈希收集者）。生产侧只 Add/Done、从不等待（预热不阻塞下发
// 的口径不变）；测试侧在用例结束前以 drainPreheatForTest 排空，
// 消除预热 goroutine 的跨用例泄漏——泄漏的预热曾在本包全量跑时
// 写进他人用例的 TempDir 缓存、与 t.TempDir 的 RemoveAll 清理相
// 撞（GATE F3），也曾在 ghMirrorBases 被恢复后继续敲真实
// github.com，给整包添时序扰动。
var preheatWG sync.WaitGroup

// drainPreheatForTest 阻塞到全部在飞预热 goroutine 落定。仅测试
// 调用（handler 包内同包测试）。
func drainPreheatForTest() {
	preheatWG.Wait()
}

// warmAgentReleaseAssets 后台预热目标版本的双架构 agent 资产
// （二进制 + .sha256）到面板镜像缓存；逐个文件失败只记日志。
func warmAgentReleaseAssets(version string) {
	for _, arch := range agentReleaseArches {
		for _, file := range []string{"gost-" + arch, "gost-" + arch + ".sha256"} {
			if err := ghMirrorWarmReleaseAsset(version, file); err != nil {
				log.Printf("gh_mirror: 预热 %s %s 失败（不影响下发）: %v", version, file, err)
			}
		}
	}
}

// preheatAgentReleaseHashes 启动后台预热，并在一个共享的
// upgradePreheatHashWait 窗口内收集各架构的期望哈希（取自面板
// 镜像缓存的官方 .sha256）。窗口内没落地的架构不在结果里——对
// 应节点的命令不带 expectedSha256，agent 按一期口径校验。
func preheatAgentReleaseHashes(version string) map[string]string {
	preheatWG.Add(1)
	go func() {
		defer preheatWG.Done()
		warmAgentReleaseAssets(version)
	}()

	type hashResult struct {
		arch string
		sum  string
	}
	ch := make(chan hashResult, len(agentReleaseArches))
	for _, arch := range agentReleaseArches {
		preheatWG.Add(1)
		go func(arch string) {
			defer preheatWG.Done()
			ch <- hashResult{arch: arch, sum: ghMirrorReleaseSHA256(version, arch)}
		}(arch)
	}
	hashes := make(map[string]string, len(agentReleaseArches))
	deadline := time.After(upgradePreheatHashWait)
	for range agentReleaseArches {
		select {
		case r := <-ch:
			if r.sum != "" {
				hashes[r.arch] = r.sum
			}
		case <-deadline:
			log.Printf("gh_mirror: 等待 %s 期望哈希超时（不影响下发）", version)
			return hashes
		}
	}
	return hashes
}

// preheatInstallReleaseAssets 后台预热节点安装要用的全套资产
// （install.sh 本体与其 .sha256、双架构二进制与其 .sha256），
// 供 nodeInstall 在生成安装命令前调用：纯异步、零等待——安装
// 命令在节点机上真正执行下载是数秒到数分钟之后，预热届时已
// 落地；失败只记日志，命令的候选回退与 F-060 校验不依赖它。
func preheatInstallReleaseAssets(version string) {
	preheatWG.Add(1)
	go func() {
		defer preheatWG.Done()
		files := []string{"install.sh", "install.sh.sha256"}
		for _, arch := range agentReleaseArches {
			files = append(files, "gost-"+arch, "gost-"+arch+".sha256")
		}
		for _, file := range files {
			if err := ghMirrorWarmReleaseAsset(version, file); err != nil {
				log.Printf("gh_mirror: 预热 %s %s 失败（不影响安装命令生成）: %v", version, file, err)
			}
		}
	}()
}

// agentArchFromReportedVersion 从节点上报的 fullVersion
// （"1.8.0 (debian/amd64)" 形态：版本 + 空格 + 括号内发行版/架构，
// go-gost/main.go 口径）提取 release 架构；后缀缺失或不是已知
// 架构时返回空串（调用方据此不带期望哈希）。
func agentArchFromReportedVersion(v string) string {
	v = strings.TrimSpace(v)
	for _, arch := range agentReleaseArches {
		if strings.HasSuffix(v, "/"+arch+")") {
			return arch
		}
	}
	return ""
}

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

// S-02: Agent 版本号严格校验，防止路径穿越
// 只允许 ^v?[0-9]+\.[0-9]+\.[0-9]+$ 格式
var agentVersionRe = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+$`)

func isValidAgentVersion(v string) bool {
	return agentVersionRe.MatchString(v)
}

// C-01：重定向解析出的 tag 会进入 nodeInstall 的 shell 命令
// （VERSION=%s 无引号拼接），生产处必须校验字符集：只允
// [0-9A-Za-z.-]，拒一切 shell 元字符（; $ 空格 | 等）。
var redirectTagPattern = regexp.MustCompile(`^[0-9A-Za-z.-]+$`)

// ---- GitHub 版本列表缓存 ----
// 未认证 GitHub API 限流 60 次/小时/IP（面板出口常为共享 NAT，更易耗尽）。
// 版本列表不需要实时：成功结果缓存 10 分钟，节点升级下拉、系统检查更新、
// 页脚探测共用同一份配额预算。
const releasesCacheTTL = 10 * time.Minute

// releasesFetchPerPage 是版本发现两路（节点侧 fetchGitHubReleases
// 与面板侧 fetchSystemUpgradeReleases）共同的全量拉取条数：一次拉满
// 存缓存，读取方各自 slice 到所需条数（B05-004 统一键的前提）。
const releasesFetchPerPage = 50

// releasesCacheKey 是版本列表缓存的统一键（B05-004）：以仓库 + 实际
// 使用的 API base 为键。旧实现两路同表异键（"api:"/"sysapi:"）永不
// 互相命中，同一份列表在冷缓存时被请求两遍、配额双倍消耗；现两路
// 共用一条目（生产中 githubAPIBase 与 systemUpgradeAPIBaseURL 同值，
// 测试中各自换 base 时键随实际 base 走，不串扰）。
func releasesCacheKey(apiBase string) string {
	return "releases:" + githubRepo + "@" + strings.TrimRight(apiBase, "/")
}

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

// maxGitHubAPIResponseBytes 是版本列表解码的体积兜底（防异常端点无限
// 灌数据），正常 releases 负载远低于此，与内核版本列表的 256MiB 同口径。
const maxGitHubAPIResponseBytes = 256 << 20

// fetchGitHubReleases 拉取版本列表（10 分钟缓存口径不变）。
//
// F-029 的「代理先行/直连降级」在 1.7.7 升级为内置候选测速直选
// （KU5，github_routes.go）：面板设置里的 GitHub 加速已整体移除，
// 路线按「直连优先短路 → 代理探测排名续切」逐个尝试、首个成功即
// 用——gh-proxy 类服务不转发 api.github.com 时该候选的 API 请求
// 自行失败、自然落到下一名，不再依赖单点配置。任一候选回限流
// 错误时优先报限流人话（其余候选也全失败才轮到它）。
func fetchGitHubReleases(perPage int) ([]githubRelease, error) {
	if perPage <= 0 {
		perPage = 20
	}
	cacheKey := releasesCacheKey(githubAPIBase)
	if releases, ok := cachedReleases(cacheKey); ok {
		return sliceReleases(releases, perPage), nil
	}

	directURL := fmt.Sprintf("%s/repos/%s/releases?per_page=%d", githubAPIBase, githubRepo, releasesFetchPerPage)
	var releases []githubRelease
	var rateLimitedErr error
	err := forEachGitHubRoute(githubFetchStageBudget, func(prefix string, attemptTimeout time.Duration) error {
		ctx, cancel := context.WithTimeout(context.Background(), attemptTimeout)
		defer cancel()
		rs, err := fetchGitHubReleasesPageCtx(ctx, prefixURL(prefix, directURL), attemptTimeout)
		if err != nil {
			if strings.Contains(err.Error(), "已限流") && rateLimitedErr == nil {
				rateLimitedErr = err
			}
			return err
		}
		releases = rs
		return nil
	})
	if err != nil {
		if rateLimitedErr != nil {
			return nil, rateLimitedErr
		}
		return nil, err
	}
	storeReleasesCache(cacheKey, releases)
	return sliceReleases(releases, perPage), nil
}

func fetchGitHubReleasesPage(url string) ([]githubRelease, error) {
	return fetchGitHubReleasesPageCtx(context.Background(), url, 30*time.Second)
}

func fetchGitHubReleasesPageCtx(ctx context.Context, url string, clientTimeout time.Duration) ([]githubRelease, error) {
	// X1-403：超时 15s → 30s（经代理多一跳 + 大响应，口径同 U1-006）；
	// 直选尝试时由 clientTimeout 按段预算剩余量收紧。
	// C-02：走共用加固助手（钉死拨号 + 逐跳重定向复检），原始客户端
	// 会被代理 302 引向内网地址。
	client := githubVersionFetchClientWithTimeout(clientTimeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("请求GitHub API失败: %v", err)
	}
	resp, err := client.Do(req)
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
//
// F-029 的代理感知在 1.7.7 升级为内置候选测速直选（KU5，
// github_routes.go）：stable 渠道的 API 兜底与 API 段同路线口径，
// 代理必经网络里不再形同虚设；路线探测本身也复用本函数的校验链
// （见 probeGitHubDirectLocked / probeGitHubProxiesLocked）。
func resolveLatestTagByRedirect() (string, error) {
	directURL := githubHTMLBase + "/" + githubRepo + "/releases/latest"
	var tag string
	err := forEachGitHubRoute(githubFetchStageBudget, func(prefix string, attemptTimeout time.Duration) error {
		t, err := fetchLatestTagByRedirectWithTimeout(prefixURL(prefix, directURL), attemptTimeout)
		if err != nil {
			return err
		}
		tag = t
		return nil
	})
	if err != nil {
		return "", err
	}
	return tag, nil
}

func fetchLatestTagByRedirect(url string) (string, error) {
	return fetchLatestTagByRedirectWithTimeout(url, 30*time.Second)
}

func fetchLatestTagByRedirectWithTimeout(url string, timeout time.Duration) (string, error) {
	// L8-F-04 / R5-FC：不再用裸 http.Client——旧实现只校验重定向
	// Location 的 host，实际请求（含候选代理前缀的 URL）的拨号
	// 未钉死、解析结果未过任何 IP 判定，DNS 污染可把版本发现
	// 引向内网地址。与版本列表面（C-02）同口径：钉死拨号 +
	// 拨号期逐地址复判（secureFetchDialContext）；本函数语义
	// 只取首个响应（ErrUseLastResponse），不跟随重定向。
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext:     secureFetchDialContext,
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	// 逐尝试超时只能比默认 30s 更紧（段预算剩余量），不得放宽。
	if timeout > 0 && timeout < client.Timeout {
		client.Timeout = timeout
	}
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	loc := resp.Header.Get("Location")
	// C-01：Location host 须为 githubHTMLBase 的 host（相对 Location
	// 无 host 时跳过本项）；恶意代理不得把 tag 解析引向站外地址。
	if err := validateRedirectLocationHost(loc); err != nil {
		return "", err
	}
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
	// C-01：tag 字符集校验（纵深第一道；第二道在 nodeInstall）。
	if !redirectTagPattern.MatchString(tag) {
		return "", fmt.Errorf("重定向解析的版本号格式无效: %q", tag)
	}
	return tag, nil
}

// validateRedirectLocationHost 校验重定向 Location 的 host：绝对地址
// 必须指向 githubHTMLBase 的 host；相对地址（无 host）放行。
func validateRedirectLocationHost(loc string) error {
	u, err := url.Parse(loc)
	if err != nil {
		return fmt.Errorf("重定向地址解析失败: %v", err)
	}
	if u.Host == "" {
		return nil
	}
	expected, err := url.Parse(githubHTMLBase)
	if err != nil {
		return fmt.Errorf("GitHub 地址配置无效: %v", err)
	}
	if !strings.EqualFold(u.Hostname(), expected.Hostname()) {
		return fmt.Errorf("重定向地址主机不受信任: %q", u.Host)
	}
	return nil
}

// resolveLatestReleaseByChannel 解析渠道最新版：API 段与重定向兜底
// 段均走内置候选测速直选（1.7.7 KU5；F-029 的代理感知后继）。
func resolveLatestReleaseByChannel(channel string) (string, error) {
	normalizedChannel := normalizeReleaseChannel(channel)
	releases, err := fetchGitHubReleases(50)
	if err != nil {
		// API 不可用（限流等）时稳定版回退到重定向解析，不依赖 API 配额。
		// J-03：兜底亦须过渠道过滤——稳定渠道只接受纯数字 tag
		// （releaseChannelFromTag(tag)==stable），命中 beta 时继续报
		// API 错误，不得把测试版当成最新正式版返回。
		if normalizedChannel == releaseChannelStable {
			if tag, ferr := resolveLatestTagByRedirect(); ferr == nil && releaseChannelFromTag(tag) == normalizedChannel {
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
	// R8-T-04: upgradeTimeout is 300s; the global 120s write cap
	// truncated every slow upgrade's response.
	extendWriteDeadline(w, nodeUpgradeWriteDeadline)
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
		version, err = resolveLatestReleaseByChannel(channel)
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
	nodeArch := ""
	if node, err := h.repo.GetNodeByID(req.ID); err == nil && node != nil && node.Version.Valid {
		nodeArch = agentArchFromReportedVersion(node.Version.String)
		if cur := strings.TrimSpace(node.Version.String); cur != "" {
			// A11-132-06：不可比版本 fail-closed（与面板自升级口径一致）。
			// R4-N1：cur 是 fullVersion（带 " (debian/amd64)" 后缀），
			// 经 compareWithReportedVersion 提取首段语义版本再比。
			cmp, cmpErr := compareWithReportedVersion(strings.TrimPrefix(version, "v"), cur)
			if cmpErr != nil {
				response.WriteJSON(w, response.ErrDefault(fmt.Sprintf("版本号 %s 或节点当前版本 %s 无法比较，已拒绝: %v", version, cur, cmpErr)))
				return
			}
			if cmp < 0 {
				response.WriteJSON(w, response.ErrDefault(fmt.Sprintf("目标版本 %s 低于节点当前版本 %s，已拒绝。如确需降级请使用节点回滚功能。", version, cur)))
				return
			}
			if cmp == 0 {
				// X-P3-29（L-11-4）：同版本短路——已最新，不下发
				// UpgradeAgent、不重启 agent。（面板自升级同版本
				// 重装为有意口径，差异见 doc/guide/upgrade.md。）
				response.WriteJSON(w, response.OK(map[string]interface{}{
					"version":       version,
					"alreadyLatest": true,
					"message":       fmt.Sprintf("节点已是最新版本 %s，无需升级", version),
				}))
				return
			}
		}
	}

	// L2-10：同节点已有升级/回退在途时下发前即拒绝——不让第二条
	// 命令进 agent 串行队列被首条的重启切断、回执误报失败。
	// （同版本短路在上方已返回，不占登记。）
	if existing, ok := h.tryAcquireNodeUpgradeOp(req.ID, "升级"); !ok {
		response.WriteJSON(w, response.Err(-2, fmt.Sprintf("该节点已有%s操作正在进行，请等待其完成后再试", existing)))
		return
	}
	defer h.releaseNodeUpgradeOp(req.ID)

	// S-02: 面板不再发送 downloadUrl/checksumURL，只发送 version
	// Agent 侧用编译期常量仓库 + 严格校验的版本号本地拼 URL。
	// 1.7.7（KU5）：不再下发面板配置的 proxyUrl——「开启 GitHub 加
	// 速」设置已整体移除，1.7.7 agent 内置候选并在下载处自行测速
	// 直选；老 agent 收不到 proxyUrl 时走其本地持久化配置/直连
	// （已知过渡行为，见 1.7.7 发版说明）。
	upgradeData := map[string]interface{}{
		"version": version,
	}
	// 面板源二期：下发前预热镜像缓存（后台进行、失败不阻塞），并
	// 在窗口内拿到本节点架构的期望哈希时随命令下发——哈希是值、
	// 经已鉴权的 WSS 控制通道走，在 agent 侧是校验信任根（老
	// agent 忽略未知字段；架构辨不出或哈希没落地则不带，agent
	// 回落一期口径）。
	if hashes := preheatAgentReleaseHashes(version); nodeArch != "" {
		if sum := hashes[nodeArch]; sum != "" {
			upgradeData["expectedSha256"] = sum
		}
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
	// R8-T-04 口径更新（1.7.7）：一波全下发后批量界 = 版本发现前奏
	// ≤120s + 单节点 upgradeTimeout 300s ≈ 420s，不再按波次叠加。
	extendWriteDeadline(w, nodeBatchUpgradeWriteDeadline)
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}

	var req struct {
		IDs     []int64 `json:"ids"`
		Version string  `json:"version"`
		Channel string  `json:"channel"`
	}
	// B05-001（R3-K9）：与升级家族其余端点同口径走 decodeJSON
	// （MaxBytesReader 10MB + DisallowUnknownFields）；旧实现裸
	// NewDecoder 无大小上限、无严格字段，超大请求体可无界读入。
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	if len(req.IDs) == 0 {
		response.WriteJSON(w, response.ErrDefault("ids不能为空"))
		return
	}
	if len(req.IDs) > maxBatchUpgradeNodes {
		response.WriteJSON(w, response.ErrDefault(fmt.Sprintf("单次批量升级最多 %d 台节点，请分批提交", maxBatchUpgradeNodes)))
		return
	}
	// R4B03-004：上限检查后去重保序——同批同节点只下发一次。重复
	// 命令在 agent 串行队列里会撞上首条升级的进程重启而永不回执，
	// 整批回执便出现同节点一成一败的自相矛盾结果。
	req.IDs = uniqueInt64s(req.IDs)

	channel := normalizeReleaseChannel(req.Channel)
	version := strings.TrimSpace(req.Version)
	if version == "" {
		var err error
		version, err = resolveLatestReleaseByChannel(channel)
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

	// S-02: 面板不再发送 downloadUrl/checksumURL，只发送 version。
	// 1.7.7（KU5）：与单节点升级同口径停发 proxyUrl（agent 自行测速
	// 直选；老 agent 走本地持久化/直连，为已知过渡行为）。
	batchUpgradeData := map[string]interface{}{
		"version": version,
	}
	// 面板源二期：整批只预热一次（后台进行、失败不阻塞下发），
	// 期望哈希按各节点架构在派发时逐个附加（见循环内 data 组装）。
	batchExpectedHashes := preheatAgentReleaseHashes(version)

	type upgradeResult struct {
		ID      int64  `json:"id"`
		Success bool   `json:"success"`
		Message string `json:"message"`
		// R8-015：同版本短路（未下发命令）单独标记——Success=true 但
		// 未下发，前端汇总不得计入「命令已发送到 N 个节点」。
		AlreadyLatest bool `json:"alreadyLatest,omitempty"`
	}

	results := make([]upgradeResult, len(req.IDs))
	var wg sync.WaitGroup

	// B05-002（R2-K2）：已派发命令的等待与请求 ctx 解绑——沿备份导入
	// 写入相位的既有范式（context.WithoutCancel，handler.go）。X-P3-26
	// 的「断开即停派发」只作用于派发前检查（下方仍读 r.Context()）：
	// 客户端断开后不再派发新节点；但命令一旦已发给 agent，前端超时/
	// 关页不得把在途升级半途掐断、连带丢掉整批回执与待重部署标记。
	// 单条命令仍受 upgradeTimeout 约束，解绑 ctx 以整批写截止为总界。
	dispatchCtx, cancelDispatch := context.WithTimeout(context.WithoutCancel(r.Context()), nodeBatchUpgradeWriteDeadline)
	defer cancelDispatch()

	// 1.7.7 一波全下发（用户口径）：单批内全部节点同时派发、各机
	// 并行执行、回执整批汇总——删除原 batchWorkers=5 信号量（>5 台
	// 被分波串行等待，尾波节点迟迟不动、前端长等待下整批回执易被
	// 误判「根本没执行」）。goroutine 数由 maxBatchUpgradeNodes(50)
	// 上限收口，L-001 的无界 spike 隐患不成立。
	for i, id := range req.IDs {
		// X-P3-26：派发前检查请求上下文——客户端断开/取消后不再派发剩余节点。
		if ctxErr := r.Context().Err(); ctxErr != nil {
			results[i] = upgradeResult{ID: id, Success: false, Message: fmt.Sprintf("请求已取消: %v", ctxErr)}
			continue
		}
		wg.Add(1)
		go func(index int, nodeID int64) {
			defer wg.Done()

			// L2-10：同节点互斥（与单节点升级/回退共用同一登记）：
			// 在途节点逐条明确失败并说明原因，其余节点不受影响、
			// 照常一波派发；不再让命令进 agent 队列被先行升级的
			// 重启切断后误报成升级失败。
			if existing, ok := h.tryAcquireNodeUpgradeOp(nodeID, "升级"); !ok {
				results[index] = upgradeResult{ID: nodeID, Success: false, Message: fmt.Sprintf("该节点已有%s操作正在进行，已跳过", existing)}
				return
			}
			defer h.releaseNodeUpgradeOp(nodeID)

			// O9-005：目标版本低于该节点当前版本时拒绝（防误降级；有意降级请用 nodeRollback）
			// A11-132-06：不可比版本同样 fail-closed 拒绝。
			nodeArch := ""
			if node, err := h.repo.GetNodeByID(nodeID); err == nil && node != nil && node.Version.Valid {
				nodeArch = agentArchFromReportedVersion(node.Version.String)
				if cur := strings.TrimSpace(node.Version.String); cur != "" {
					// R4-N1：同单节点路径，cur 经上报型比较提取首段版本。
					cmp, cmpErr := compareWithReportedVersion(strings.TrimPrefix(version, "v"), cur)
					if cmpErr != nil {
						results[index] = upgradeResult{ID: nodeID, Success: false, Message: fmt.Sprintf("版本号 %s 或节点当前版本 %s 无法比较，已拒绝: %v", version, cur, cmpErr)}
						return
					}
					if cmp < 0 {
						results[index] = upgradeResult{ID: nodeID, Success: false, Message: fmt.Sprintf("目标版本 %s 低于节点当前版本 %s，已拒绝（降级请用回滚功能）", version, cur)}
						return
					}
					if cmp == 0 {
						// X-P3-29（L-11-4）：同版本短路，不下发不重启。
						// R8-015：带 AlreadyLatest 标记供前端汇总分开计数。
						results[index] = upgradeResult{ID: nodeID, Success: true, AlreadyLatest: true, Message: fmt.Sprintf("已是最新版本 %s，无需升级", version)}
						return
					}
				}
			}

			// 面板源二期：本节点架构的期望哈希落地了才附加（逐节点
			// 克隆命令数据，不动整批共享的 batchUpgradeData）。
			data := batchUpgradeData
			if sum := batchExpectedHashes[nodeArch]; nodeArch != "" && sum != "" {
				data = map[string]interface{}{
					"version":        version,
					"expectedSha256": sum,
				}
			}
			result, err := h.wsServer.SendCommandContext(dispatchCtx, nodeID, "UpgradeAgent", data, upgradeTimeout)
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

	releases, err := fetchGitHubReleases(50)
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

	// L2-10：与升级共用同节点在途登记——回退排在升级之后会被
	// 升级的重启切断，回退本身也会误动刚替换的二进制，下发前
	// 即拒绝并告知在途操作。
	if existing, ok := h.tryAcquireNodeUpgradeOp(req.ID, "回退"); !ok {
		response.WriteJSON(w, response.Err(-2, fmt.Sprintf("该节点已有%s操作正在进行，请等待其完成后再试", existing)))
		return
	}
	defer h.releaseNodeUpgradeOp(req.ID)

	result, err := h.wsServer.SendCommand(req.ID, "RollbackAgent", map[string]interface{}{}, 30*time.Second)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, fmt.Sprintf("回退失败: %v", err)))
		return
	}

	response.WriteJSON(w, response.OK(rollbackReceiptData(result)))
}

// rollbackReceiptData builds the node rollback receipt (R6-05).
// Agents that carry the backup-pairing pre-flight report the
// landing version in the command data; the receipt must name that
// version — a rollback whose target the operator cannot see is how
// the cross-path generation skip (panel upgrade A->B, script
// update B->C, rollback silently landing on A) stayed invisible.
// The backupGeneration field is passed through verbatim; a
// "legacy-unknown" backup (written before pairing records existed)
// is disclosed as unverified in the message. Older agents report
// no data at all (R7-13): their rollback ran under the pre-pairing
// semantics with no generation check at all, so the receipt
// carries the same unverified disclosure instead of forwarding
// the agent's bare message — in a mixed-version fleet every
// unverified rollback shows the same signal.
func rollbackReceiptData(result ws.CommandResult) map[string]interface{} {
	data := map[string]interface{}{"message": result.Message}
	toVersion, _ := result.Data["toVersion"].(string)
	if toVersion == "" {
		data["message"] = "回退命令已发送，节点将自动重启（该备份无代际配对记录，未经紧邻上一代校验）"
		return data
	}
	data["toVersion"] = toVersion
	if fromVersion, _ := result.Data["fromVersion"].(string); fromVersion != "" {
		data["fromVersion"] = fromVersion
	}
	message := fmt.Sprintf("回退命令已发送，节点将回退到版本 %s 并自动重启", toVersion)
	if generation, _ := result.Data["backupGeneration"].(string); generation != "" {
		data["backupGeneration"] = generation
		if generation == "legacy-unknown" {
			message += "（该备份无代际配对记录，未经紧邻上一代校验）"
		}
	}
	data["message"] = message
	return data
}

// --- 同节点升级类操作互斥（L2-10 / R5-FC）---

// tryAcquireNodeUpgradeOp 登记一条同节点升级类操作（op 为
// "升级"/"回退"展示名）；该节点已有操作在途时返回在途操作名与
// false。三个下发入口（单节点升级、批量升级、节点回退）共用；
// R9-06 起删节点漏斗（checkNodeDeleteGuards）亦接入：占用时拒绝
// 删除并点名在途操作，通过后以 op "删除" 占用至级联段结束：
// agent 侧 mutation 命令走单工串行队列，第二条命令不会被拒而是
// 排队，首条升级的重启会把排队/执行中的第二条连进程一起杀掉、
// 面板按 failPendingForNode 结算成失败——节点实际已被首条升级
// 成功，整批回执却误报失败（回退排在升级后还会按 .old 语义回退
// 刚替换的二进制）。改为下发前互斥：后到者明确被拒并告知在途
// 操作，根本不进 agent 队列；前端守卫只是页内状态（多标签页/
// 多管理员绕得过），后端登记才是收口。登记只覆盖命令在途期
// （下发到回执/超时/断连结算），随面板进程重启清空（与
// pendingUpgradeRedeploy 等记账同为内存态）。
func (h *Handler) tryAcquireNodeUpgradeOp(nodeID int64, op string) (string, bool) {
	if h == nil || nodeID <= 0 {
		return "", true
	}
	h.upgradeMu.Lock()
	defer h.upgradeMu.Unlock()
	if h.nodeUpgradeOps == nil {
		h.nodeUpgradeOps = make(map[int64]string)
	}
	if existing, ok := h.nodeUpgradeOps[nodeID]; ok {
		return existing, false
	}
	h.nodeUpgradeOps[nodeID] = op
	return "", true
}

func (h *Handler) releaseNodeUpgradeOp(nodeID int64) {
	if h == nil || nodeID <= 0 {
		return
	}
	h.upgradeMu.Lock()
	delete(h.nodeUpgradeOps, nodeID)
	h.upgradeMu.Unlock()
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
	// FIX2-A（V2-04）：离线期间被切走 realm 的节点，先同步结算
	// realm 计数再放行下方异步对账与 redeploy。此刻 agent 仍持
	// realm 计数（尚未收到任何新下发），轮询可完整回收离线增量；
	// 顺序反过来（redeploy/对账先行让 agent 弃 realm 计数）则增
	// 量永久漏计。节点状态未启用而上方已返回时标记不消费，下次
	// 连接再试。
	if h.consumePendingRealmSettle(nodeID) {
		h.refreshNodeKernelStatus(nodeID)
	}
	// DB-3：内核对账完成后排一次受影响面重测（走定向探测路径，
	// 不计告警 streak）；排队本身只入队，静默合并窗在 worker 侧。
	go func() {
		h.reconcileNodeKernels(nodeID)
		h.scheduleNodeReconnectProbe(nodeID)
	}()
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
		} else {
			// R21-X-02：重推成功后清理离线更新产生的失势 limiter（下
			// 面 helper 内说明判定口径）。
			h.deleteRedeployStaleRuleConnLimiter(forward, nodeID)
		}
	}

	// Retry failed items with exponential backoff (max 3 attempts)
	return h.retryFailedRedeploys(nodeID, tunnelFailed, failedForwards)
}

// deleteRedeployStaleRuleConnLimitersForTunnel 是 R21-X-02 的隧道重试补删：
// retryFailedRedeploys 里隧道失败后其下转发首轮既不同步（forward 循环
// 跳过）、亦不进 failedForwards；隧道重试成功时转发在
// redeployTunnelAndForwards 内重推成功，但该路径不带失势清理，故在此
// 逐条按“当前记录已无限流”补删（与首轮 forward 循环同口径）。仅清本
// 节点上有入口端口的转发（多节点转发由各节点自己的重连重推各清各的）；
// pending-delete 行不同步、不同删（删除漏斗接管）；列举/取端口失败只
// 记日志、不挡重推回执（与 deleteForwardConnLimiterOnNode 同口径）。
func (h *Handler) deleteRedeployStaleRuleConnLimitersForTunnel(tunnelID, nodeID int64) {
	forwards, err := h.listForwardsByTunnel(tunnelID)
	if err != nil {
		fmt.Printf("post-upgrade redeploy: list forwards for tunnel %d failed: %v\n", tunnelID, err)
		return
	}
	for i := range forwards {
		forward := &forwards[i]
		if isForwardPendingDelete(forward) {
			continue
		}
		ports, err := h.listForwardPorts(forward.ID)
		if err != nil {
			fmt.Printf("post-upgrade redeploy: list ports for forward %d failed: %v\n", forward.ID, err)
			continue
		}
		onNode := false
		for _, fp := range ports {
			if fp.NodeID == nodeID {
				onNode = true
				break
			}
		}
		if !onNode {
			continue
		}
		h.deleteRedeployStaleRuleConnLimiter(forward, nodeID)
	}
}

// deleteRedeployStaleRuleConnLimiter 是 R21-X-02 的重连重推清理：离线期间
// 更新去限流只改了 DB、未推节点；重连重推把已去引用的新服务配置下发后，
// 节点上 rule_conn_limit_<id> 成无引用孤儿。重推时 DB 已是新态、无更新
// 前快照可传，sync 内的失势清理（R20-X-01 ①）因 oldForward == nil 直接
// 返回，故在此按“当前记录已无限流”判定失势：名只由转发 ID 决定，agent
// 侧删不存在者为 noop（与 R20-X-01 ② 同口径）；服务 climiter 字段只由
// 当前记录的限流值赋值（control_plane.go buildForwardServiceConfigs 内），
// 当前无限流时节点上同名 limiter 若仍存在必为孤儿。仅在重推成功后调用
// （先推服务后删 limiter）。
// user_conn_limit_* 为同用户共享，永不在此删除（按 ID 取名只取 rule_ 名）。
func (h *Handler) deleteRedeployStaleRuleConnLimiter(forward *forwardRecord, nodeID int64) {
	if forward == nil || forwardRuleConnLimiterName(forward) != "" {
		return
	}
	h.deleteForwardConnLimiterOnNode(forward, nodeID)
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
				// R21-X-02：隧道重试成功时其下转发已在 redeployTunnelAndForwards
				// 内重推成功，但该路径不带失势清理；逐条补删（与首轮 forward
				// 循环同口径；失败-重试成功的隧道下转发不得留孤儿）。
				h.deleteRedeployStaleRuleConnLimitersForTunnel(tunnelID, nodeID)
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
				// R21-X-02：重试重推成功后同样清理失势 limiter（与首轮
				// 重推同口径；失败-重试成功的转发不得留孤儿）。
				h.deleteRedeployStaleRuleConnLimiter(ff.forward, nodeID)
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
