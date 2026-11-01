package handler

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// ---- 内置 GitHub 候选与面板侧测速直选（1.7.7，KU5）----
//
// 面板设置里的「开启 GitHub 加速」（github_proxy_enabled /
// github_proxy_url）已整体移除：单点配置的代理一挂，节点升级、
// 版本发现、内核版本列表全链路陪葬（ghfast 多次实测失效即全灭），
// 且设置值与实际下载路径脱节。改为全产品统一的内置候选清单：
// 9 个公共代理前缀 + 直连（""），与 agent 侧、install.sh /
// panel_install.sh 三面同表同序（改一处须同步其余两处）。
//
// 路线选择口径（面板侧版本发现与脚本测速同此）：
//   1. 先单独探测直连（真实目标、3s 超时）：通 → 直接用直连，
//      不再测任何代理——海内外一律先测直连，直连可用时零代理
//      依赖、零额外开销。
//   2. 直连不通（或已选直连在真实请求中失败）→ 才并行探测
//      9 个代理，按延迟选最快；真实请求失败按排名续切；已选
//      路线中途失败时同样按此顺序补测/续切。
//   3. 判活只认真实目标内容校验（全产品统一口径，非裸
//      HTTP 200）：资产面以真实 .sha256 拉取成功且可解析为
//      准入（agent 升级/内核下载、面板生成安装命令首跳）；
//      发现面（本文件）以重定向 tag 合法解析为准入——探测
//      目标为本仓 releases/latest 重定向端点，须经既有校验
//      链解析出合法 tag 才算活，任意 HTTP 200 落地页不算活
//      （防镜像站假活）；真实请求阶段每名候选的成败以该请
//      求自身的内容校验为准（API JSON 可解、tag 可解析、
//      资产 sha256 同源校验通过）。
//   4. 探测只排序不删除：并行探测中未判活的代理按清单原序
//      殿后，真实请求仍逐个尝试（探测口径与某类请求口径不
//      一致时，如代理不转发 api.github.com，靠尝试阶段的逐个
//      失败自然落到下一名）；直连被探测判死时排在最末作兜底
//      尝试。探测结果缓存 5 分钟，不进每次请求的热路径。
//
// 每段（API 段 / 重定向段）尝试有总预算：逐个候选尝试、首个
// 成功即用，单次尝试超时取客户端固有界（30s）与段预算剩余量
// 的较小者。整链最坏耗时 = 直连探测 3s + 代理探测 ≤5s（仅升级
// 时发生）+ API 段 50s + 重定向段 50s ≈ 108s，仍在登记的后端
// 界 120s 内（doc/audit/timeout-budgets.tsv 的 C059/C080；内核
// 版本列表只有一段，≈58s，在其 60s 界内，T007）。

// githubFetchProxyCandidates 是内置代理前缀全集（不含直连），
// 为 var 以便测试把候选替换为本地 stub 前缀。
var githubFetchProxyCandidates = []string{
	"https://ghfast.top",
	"https://gcode.hostcentral.cc",
	"https://gh-proxy.com",
	"https://ghproxy.net",
	"https://hub.gitmirror.com",
	"https://ghps.cc",
	"https://gh.llkk.cc",
	"https://ghp.ci",
	"https://ghproxy.cc",
}

const (
	// githubDirectProbeTimeout 是直连单独探测的超时（首步短路）。
	githubDirectProbeTimeout = 3 * time.Second
	// githubRouteProbeTimeout 是单个代理候选的探测超时；9 个候选
	// 并行探测，一轮总耗时即此值（有界）。
	githubRouteProbeTimeout = 5 * time.Second
	// githubRouteRankTTL 是探测结果的缓存时长：版本发现本身已有
	// 10 分钟结果缓存，5 分钟重测一次足够跟随路线质量变化。
	githubRouteRankTTL = 5 * time.Minute
	// githubFetchStageBudget 是版本发现每一段（API / 重定向）的
	// 尝试总预算（不含探测，探测在段外单独计界，见文件头）。
	githubFetchStageBudget = 50 * time.Second
	// githubAssetDownloadBudget 是面板自升级资产下载（资产 + 同源
	// .sha256 为一腿）的全候选总预算，对齐 T004 的下载腿界。
	githubAssetDownloadBudget = 115 * time.Second
)

var githubRouteCache = struct {
	sync.Mutex
	at            time.Time
	directProbed  bool
	directAlive   bool
	proxiesProbed bool
	proxyOrder    []string
}{}

// githubRouteCacheStaleLocked 报告探测缓存是否已过期（调用方持锁）。
func githubRouteCacheStaleLocked() bool {
	if !githubRouteCache.directProbed && !githubRouteCache.proxiesProbed {
		return true
	}
	return time.Since(githubRouteCache.at) >= githubRouteRankTTL
}

func githubRouteCacheResetLocked() {
	githubRouteCache.directProbed = false
	githubRouteCache.directAlive = false
	githubRouteCache.proxiesProbed = false
	githubRouteCache.proxyOrder = nil
}

// probeGitHubDirectLocked 单独探测直连（调用方持锁）：以本仓
// releases/latest 重定向端点为真实目标，复用重定向解析的校验链
// 判定存活（不跟随重定向 + Location 主机校验 + tag 字符集）。
func probeGitHubDirectLocked() {
	target := githubHTMLBase + "/" + githubRepo + "/releases/latest"
	_, err := fetchLatestTagByRedirectWithTimeout(target, githubDirectProbeTimeout)
	githubRouteCache.directProbed = true
	githubRouteCache.directAlive = err == nil
	githubRouteCache.at = time.Now()
}

// probeGitHubProxiesLocked 并行探测全部代理候选（调用方持锁）：
// 同一直连探测的目标与判活标准，活候选按延迟升序，未判活的按
// 清单原序殿后。
func probeGitHubProxiesLocked() {
	candidates := githubFetchProxyCandidates
	type probeResult struct {
		prefix  string
		alive   bool
		latency time.Duration
	}
	results := make([]probeResult, len(candidates))
	var wg sync.WaitGroup
	for i, prefix := range candidates {
		wg.Add(1)
		go func(i int, prefix string) {
			defer wg.Done()
			target := prefixURL(prefix, githubHTMLBase+"/"+githubRepo+"/releases/latest")
			start := time.Now()
			_, err := fetchLatestTagByRedirectWithTimeout(target, githubRouteProbeTimeout)
			results[i] = probeResult{prefix: prefix, alive: err == nil, latency: time.Since(start)}
		}(i, prefix)
	}
	wg.Wait()

	alive := make([]probeResult, 0, len(results))
	for _, r := range results {
		if r.alive {
			alive = append(alive, r)
		}
	}
	sort.SliceStable(alive, func(a, b int) bool { return alive[a].latency < alive[b].latency })
	seen := make(map[string]bool, len(candidates))
	order := make([]string, 0, len(candidates))
	for _, r := range alive {
		order = append(order, r.prefix)
		seen[r.prefix] = true
	}
	for _, prefix := range candidates {
		if !seen[prefix] {
			order = append(order, prefix)
		}
	}
	githubRouteCache.proxiesProbed = true
	githubRouteCache.proxyOrder = order
	githubRouteCache.at = time.Now()
}

// gitHubDirectAlive 返回直连探测结论（必要时先探测，结果缓存）。
func gitHubDirectAlive() bool {
	githubRouteCache.Lock()
	defer githubRouteCache.Unlock()
	if githubRouteCacheStaleLocked() {
		githubRouteCacheResetLocked()
	}
	if !githubRouteCache.directProbed {
		probeGitHubDirectLocked()
	}
	return githubRouteCache.directAlive
}

// gitHubProxyOrder 返回代理排名（必要时先并行探测，结果缓存）。
func gitHubProxyOrder() []string {
	githubRouteCache.Lock()
	defer githubRouteCache.Unlock()
	if githubRouteCacheStaleLocked() {
		githubRouteCacheResetLocked()
	}
	if !githubRouteCache.proxiesProbed {
		probeGitHubProxiesLocked()
	}
	return append([]string(nil), githubRouteCache.proxyOrder...)
}

// gitHubRouteOrder 是需要静态路线表的调用方（面板自升级资产下载
// 的候选 URL 列表）的全序：直连判活时直连居首（代理未探测时以
// 清单原序殿后作兜底，已探测时用排名）；直连判死时为代理排名
// + 直连殿后兜底。
func gitHubRouteOrder() []string {
	githubRouteCache.Lock()
	defer githubRouteCache.Unlock()
	if githubRouteCacheStaleLocked() {
		githubRouteCacheResetLocked()
	}
	if !githubRouteCache.directProbed {
		probeGitHubDirectLocked()
	}
	proxies := githubRouteCache.proxyOrder
	if !githubRouteCache.proxiesProbed {
		proxies = githubFetchProxyCandidates
	}
	order := make([]string, 0, len(proxies)+1)
	if githubRouteCache.directAlive {
		order = append(order, "")
		order = append(order, proxies...)
		return order
	}
	order = append(order, proxies...)
	return append(order, "")
}

// prefixURL 把候选前缀拼到目标 URL 前（"" 为直连，原样返回）。
func prefixURL(prefix, rawURL string) string {
	if prefix == "" {
		return rawURL
	}
	return prefix + "/" + rawURL
}

// gitHubRouteLabel 是候选在日志/错误中的称呼。
func gitHubRouteLabel(prefix string) string {
	if prefix == "" {
		return "直连"
	}
	return prefix
}

// forEachGitHubRoute 按「直连优先、失败升级代理排名」的顺序逐个
// 执行 attempt（prefix "" 为直连），首个成功即返回 nil：
//   - 直连探测判活：先尝试直连；直连尝试失败才触发代理探测
//     （若尚未探测）并按排名续切；
//   - 直连探测判死：直接按代理排名尝试，全部失败后最后兜底
//     尝试一次直连（探测口径与请求口径可能不一致）。
//
// stageBudget 是本段尝试总预算（探测耗时不计入段预算，各探测
// 自有界，见文件头）：预算耗尽后不再发起新尝试；attemptTimeout
// 是本次尝试应采用的超时（min(30s, 段预算剩余)），各抓取器据此
// 收紧自己的客户端/ctx。全部失败返回最后一次错误。
func forEachGitHubRoute(stageBudget time.Duration, attempt func(prefix string, attemptTimeout time.Duration) error) error {
	deadline := time.Now().Add(stageBudget)
	var lastErr error
	succeeded := false
	tryPrefix := func(prefix string) {
		if succeeded {
			return
		}
		remaining := time.Until(deadline)
		if lastErr != nil && remaining <= 0 {
			return
		}
		timeout := 30 * time.Second
		if remaining > 0 && remaining < timeout {
			timeout = remaining
		}
		if timeout <= 0 {
			timeout = time.Second
		}
		if err := attempt(prefix, timeout); err != nil {
			lastErr = err
			return
		}
		succeeded = true
	}

	directAlive := gitHubDirectAlive()
	if directAlive {
		tryPrefix("")
		if succeeded {
			return nil
		}
	}
	for _, prefix := range gitHubProxyOrder() {
		tryPrefix(prefix)
		if succeeded {
			return nil
		}
	}
	if !directAlive {
		tryPrefix("")
		if succeeded {
			return nil
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("无可用下载路线")
	}
	return lastErr
}
