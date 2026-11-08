package handler

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"go-backend/internal/clientip"
)

// 面板的 GitHub 镜像。/api/v1/open_api/gh/<GitHub 地址> 用法与公开的
// "GitHub 加速"代理（<proxy>/https://github.com/...）相同，把加速地址设为
// <panel>/api/v1/open_api/gh 后，能连面板但连不上 GitHub 的节点（国内）
// 就可以经面板安装、升级和拉内核。旧 agent 无需更新：它们本来就会在 URL
// 前拼接代理前缀。
//
// 只服务面板安装用到的仓库。tag 下的 release 文件永不变化，永久缓存在
// 磁盘；"latest" 文件、raw 文件和 API 响应按 TTL 刷新；GitHub 不可达时
// 返回旧副本。

const (
	ghMirrorPath     = "/api/v1/open_api/gh/"
	ghMirrorMaxFile  = 300 << 20
	ghMirrorMaxCache = 2 << 30
	ghMirrorFreshAPI = 10 * time.Minute
	ghMirrorFreshRaw = 6 * time.Hour
	ghMirrorLatest   = 10 * time.Minute
	ghMirrorServeFor = 30 * time.Minute
	// F-137：下载失败时 stale 回退的年龄上限。tag 下的 release 文件是
	// 不可变的（fresh==0，永不重下），不受此限；此限只约束 API/raw/
	// latest 这类可变内容的旧副本最多能旧到什么程度。
	ghMirrorMaxStaleAge = 24 * time.Hour
)

// J-002：镜像代理是无鉴权的开放代理，文件可达 300MB、总缓存 2GB，
// 不限流会被刷带宽/磁盘。按源 IP 做固定窗口限流（60 次/分钟，
// 节点正常安装升级远用不满）；超限返回 429。
var (
	ghMirrorRateMu       sync.Mutex
	ghMirrorRate         = make(map[string]*ghMirrorBucket)
	ghMirrorRateMax      = 60
	ghMirrorRateWindow   = time.Minute
	ghMirrorRateMaxHosts = 10000
)

type ghMirrorBucket struct {
	count     int
	windowEnd time.Time
}

// ghMirrorEvictLocked 清理限流桶（D6-05）：先删全部过期桶；
// 仍达上限时按 windowEnd 最早者逐个淘汰（与 login_rate_limit 的
// evictIfNeededLocked 同型）。旧实现从不清理：累计 10000 个不同源 IP
// 后所有新 IP 被 fail-closed 永久拒绝至进程重启（该端点无鉴权开放，
// 公网扫描即可填满）。
// 调用方须持 ghMirrorRateMu。
func ghMirrorEvictLocked(now time.Time) {
	for ip, b := range ghMirrorRate {
		if now.After(b.windowEnd) {
			delete(ghMirrorRate, ip)
		}
	}
	for len(ghMirrorRate) >= ghMirrorRateMaxHosts {
		var oldestIP string
		var oldestEnd time.Time
		first := true
		for ip, b := range ghMirrorRate {
			if first || b.windowEnd.Before(oldestEnd) {
				oldestIP, oldestEnd, first = ip, b.windowEnd, false
			}
		}
		if oldestIP == "" {
			return
		}
		log.Printf("warning: gh_mirror rate limiter full (%d hosts), evicting oldest bucket", len(ghMirrorRate))
		delete(ghMirrorRate, oldestIP)
	}
}

func ghMirrorAllow(ip string) bool {
	now := time.Now()
	ghMirrorRateMu.Lock()
	defer ghMirrorRateMu.Unlock()
	b, ok := ghMirrorRate[ip]
	if !ok || now.After(b.windowEnd) {
		if !ok && len(ghMirrorRate) >= ghMirrorRateMaxHosts {
			// 满额时先清理过期/最旧桶再收新 IP；清理后仍满才 fail-closed。
			ghMirrorEvictLocked(now)
			if len(ghMirrorRate) >= ghMirrorRateMaxHosts {
				return false
			}
		}
		ghMirrorRate[ip] = &ghMirrorBucket{count: 1, windowEnd: now.Add(ghMirrorRateWindow)}
		return true
	}
	if b.count >= ghMirrorRateMax {
		return false
	}
	b.count++
	return true
}

// ghMirrorRepos 是面板安装用到的仓库：自身 release（agent、install.sh）、
// 内核，以及内核读取的数据文件。
func ghMirrorRepos() map[string]bool {
	return map[string]bool{
		strings.ToLower(githubRepo):    true,
		"xtls/xray-core":               true,
		"sagernet/sing-box":            true,
		"ihciah/shadow-tls":            true,
		"v2fly/geoip":                  true,
		"v2fly/domain-list-community":  true,
		"sagernet/sing-geosite":        true,
		"sagernet/sing-geoip":          true,
		"metacubex/meta-rules-dat":     true,
		"blackmatrix7/ios_rule_script": true,
	}
}

var ghMirrorSegment = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// ghMirrorUpstream 描述请求要取的 GitHub 资源。
type ghMirrorUpstream struct {
	url   string        // 实际抓取的地址
	fresh time.Duration // 缓存副本的有效期；0 表示永久有效
	// latest 用于 …/releases/latest/download/<file>：文件从 "latest" 当前
	// 指向的 release 取。
	repo, file string
	latest     bool
	// tagPage 用于 …/releases/latest 和 …/releases/tag/<tag>，
	// install.sh 只跟随它们拿到 tag。
	tagPage bool
}

// ghMirrorPinnedTransport 是镜像上游抓取的钉死拨号传输层
// （L8-F-04 / R5-FC）：与版本发现的加固客户端（C-02）同口径——
// 拨号时重解析并逐地址复判（硬拒层恒拒、非公网须显式 allowlist
// 放行），解析结果钉死后拨号，消除「仅校验 host 名、拨号时被
// DNS 污染/重绑定引向内网」的残留面；不走环境代理（与
// githubVersionFetchClient 一致）。上游恒为固定 GitHub 主机，
// 正常解析必为公网，allowlist 仅为本机 stub/特殊部署的逃生口。
func ghMirrorPinnedTransport() *http.Transport {
	return &http.Transport{
		DialContext: secureFetchDialContext,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	}
}

var (
	ghMirrorDir   = "" // set from DB_PATH; tests point it elsewhere
	ghMirrorHTTP  = &http.Client{Timeout: 10 * time.Minute, CheckRedirect: ghMirrorCheckRedirect, Transport: ghMirrorPinnedTransport()}
	ghMirrorBases = struct{ github, api, raw string }{"https://github.com", "https://api.github.com", "https://raw.githubusercontent.com"}

	ghMirrorFlightMu sync.Mutex
	ghMirrorFlights  = map[string]*ghMirrorFlight{}

	// R6X-012（R6-C-01）：stale 回退的上次下载尝试时间（节流时钟），
	// 与内容年龄基准解耦。旧实现靠回退时 os.Chtimes 把缓存文件 mtime
	// 续到当前时刻来节流，而新鲜期与 stale 年龄判定也只读 mtime——
	// 续期把内容真实年龄一并抹除，连续失败 + 周期请求下年龄永不
	// 超过 24h 上限。改后：mtime 只由成功下载写入（即内容真实年龄），
	// 节流只记在此表、以 fresh 为窗口，进程重启至多多打一次上游，
	// 年龄上限不受影响。
	ghMirrorAttemptMu sync.Mutex
	ghMirrorAttempts  = map[string]time.Time{}

	ghMirrorTagMu sync.Mutex
	ghMirrorTags  = map[string]ghMirrorTag{}
)

type ghMirrorFlight struct {
	done chan struct{}
	err  error
}

type ghMirrorTag struct {
	tag string
	at  time.Time
}

func ghMirrorLastAttempt(path string) (time.Time, bool) {
	ghMirrorAttemptMu.Lock()
	defer ghMirrorAttemptMu.Unlock()
	at, ok := ghMirrorAttempts[path]
	return at, ok
}

// ghMirrorAttemptsMax 是 stale 节流表的容量上限（L2-22）。白名单内
// 可造的互异 URL 无界（raw 任意路径、API tag 变体），旧实现条目随
// 进程生命单调增长——与同仓限流桶/缓存目录「上限＋淘汰」口径对齐：
// 达上限写入新键时先清扫超过 stale 年龄上限的条目（节流窗口最长
// 6h，远小于 24h，删除不改变节流语义），仍满则按时间淘汰最早者。
const ghMirrorAttemptsMax = 10000

func ghMirrorRecordAttempt(path string) {
	ghMirrorAttemptMu.Lock()
	defer ghMirrorAttemptMu.Unlock()
	if _, ok := ghMirrorAttempts[path]; !ok && len(ghMirrorAttempts) >= ghMirrorAttemptsMax {
		now := time.Now()
		for p, at := range ghMirrorAttempts {
			if now.Sub(at) > ghMirrorMaxStaleAge {
				delete(ghMirrorAttempts, p)
			}
		}
		for len(ghMirrorAttempts) >= ghMirrorAttemptsMax {
			var oldestPath string
			var oldestAt time.Time
			first := true
			for p, at := range ghMirrorAttempts {
				if first || at.Before(oldestAt) {
					oldestPath, oldestAt, first = p, at, false
				}
			}
			if oldestPath == "" {
				break
			}
			log.Printf("warning: gh_mirror attempt table full (%d entries), evicting oldest entry", len(ghMirrorAttempts))
			delete(ghMirrorAttempts, oldestPath)
		}
	}
	ghMirrorAttempts[path] = time.Now()
}

func ghMirrorClearAttempt(path string) {
	ghMirrorAttemptMu.Lock()
	delete(ghMirrorAttempts, path)
	ghMirrorAttemptMu.Unlock()
}

func ghMirrorCheckRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return fmt.Errorf("too many redirects")
	}
	host := strings.ToLower(req.URL.Hostname())
	// L2-20（后端面）：GitHub 自家主机全集——镜像本体直取的三个
	// 基址主机与 github.com 之间存在合法互跳（仓库改名、raw 内容
	// 跳转），旧白名单只有 github.com＋*.githubusercontent.com，
	// 跳到 api/raw/codeload 会被误拒（可用性死角）。放行的仍只是
	// GitHub 自家主机名，且拨号层另有钉死复判（ghMirrorPinnedTransport），
	// 主机名放行不等于地址放行。
	if host == "github.com" || host == "api.github.com" || host == "raw.githubusercontent.com" || host == "codeload.github.com" || strings.HasSuffix(host, ".githubusercontent.com") {
		return nil
	}
	return fmt.Errorf("redirect to %s refused", host)
}

// parseGHMirrorPath 把 /gh/ 后面的部分解析为上游地址，白名单仓库之外
// 的一律拒绝。
func parseGHMirrorPath(rest, rawQuery string) (*ghMirrorUpstream, error) {
	// 路径中的连续斜杠可能在传输中被合并（"https:/github.com/..."）。
	rest = strings.TrimPrefix(rest, "/")
	for _, p := range []string{"https://", "https:/", "http://", "http:/"} {
		if strings.HasPrefix(rest, p) {
			rest = rest[len(p):]
			break
		}
	}
	host, path, _ := strings.Cut(rest, "/")
	parts := strings.Split(path, "/")
	for _, p := range parts {
		if !ghMirrorSegment.MatchString(p) || p == "." || p == ".." {
			return nil, fmt.Errorf("不支持的地址")
		}
	}
	repos := ghMirrorRepos()
	allowed := func(owner, repo string) bool { return repos[strings.ToLower(owner+"/"+repo)] }

	switch strings.ToLower(host) {
	case "github.com":
		// owner/repo/releases/download/<tag>/<file>
		// owner/repo/releases/latest/download/<file>
		// owner/repo/releases/latest, owner/repo/releases/tag/<tag>
		if len(parts) < 4 || parts[2] != "releases" || !allowed(parts[0], parts[1]) {
			break
		}
		repo := parts[0] + "/" + parts[1]
		switch {
		case len(parts) == 6 && parts[3] == "download":
			return &ghMirrorUpstream{url: ghMirrorBases.github + "/" + path}, nil
		case len(parts) == 6 && parts[3] == "latest" && parts[4] == "download":
			return &ghMirrorUpstream{repo: repo, file: parts[5], latest: true}, nil
		case len(parts) == 4 && parts[3] == "latest":
			return &ghMirrorUpstream{repo: repo, tagPage: true}, nil
		case len(parts) == 5 && parts[3] == "tag":
			return &ghMirrorUpstream{repo: repo, file: parts[4], tagPage: true}, nil
		}
	case "api.github.com":
		// repos/owner/repo/releases[/latest | /tags/<tag>]
		if len(parts) < 4 || parts[0] != "repos" || parts[3] != "releases" || !allowed(parts[1], parts[2]) {
			break
		}
		if len(parts) == 4 || (len(parts) == 5 && parts[4] == "latest") || (len(parts) == 6 && parts[4] == "tags") {
			u := ghMirrorBases.api + "/" + path
			if rawQuery != "" {
				q, err := url.ParseQuery(rawQuery)
				if err != nil {
					break
				}
				u += "?" + q.Encode()
			}
			return &ghMirrorUpstream{url: u, fresh: ghMirrorFreshAPI}, nil
		}
	case "raw.githubusercontent.com":
		if len(parts) >= 4 && allowed(parts[0], parts[1]) {
			return &ghMirrorUpstream{url: ghMirrorBases.raw + "/" + path, fresh: ghMirrorFreshRaw}, nil
		}
	}
	return nil, fmt.Errorf("只能下载面板用到的 GitHub 仓库文件")
}

func (h *Handler) githubMirror(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// J-002：开放代理限流，防带宽/磁盘被刷。键走可信代理链解析
	// （clientip，与登录限流同口径，R5-FC / L2-09）：标准反代部署
	// 下 RemoteAddr 恒为代理机地址，裸键等于全站共用一桶——50 台
	// 批量升级走面板源（每台探针+二进制+校验 ≥3 次）会自触发
	// 429，公网访客抢占亦可封死全体节点。解析不出地址时记入共享
	// 桶 fail-closed 并告警，不再像旧实现那样 SplitHostPort 失败
	// 即整段跳过限流（fail-open）。
	rateKey, resolved := clientip.RateLimitKey(r)
	if !resolved {
		log.Printf("gh_mirror: 无法从 RemoteAddr %q 解析客户端地址，限流记入共享桶（fail-closed）", r.RemoteAddr)
		rateKey = clientip.UnresolvedKey
	}
	if !ghMirrorAllow(rateKey) {
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}
	up, err := parseGHMirrorPath(strings.TrimPrefix(r.URL.Path, ghMirrorPath), r.URL.RawQuery)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	if up.tagPage {
		tag := up.file
		if tag == "" {
			if tag, err = ghMirrorLatestTag(up.repo, ""); err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			// 像 GitHub 一样指向 tag 页面，跟随重定向的客户端最终落在
			// …/releases/tag/<tag>。
			http.Redirect(w, r, ghMirrorPath+"https://github.com/"+up.repo+"/releases/tag/"+tag, http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, tag+"\n")
		return
	}
	if up.latest {
		tag, err := ghMirrorLatestTag(up.repo, up.file)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		up.url = ghMirrorBases.github + "/" + up.repo + "/releases/download/" + tag + "/" + up.file
	}

	path, err := ghMirrorFetch(up)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		http.Error(w, "cache read failed", http.StatusInternalServerError)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.Error(w, "cache read failed", http.StatusInternalServerError)
		return
	}
	// 慢链路上传内核需要比服务器写超时更长的时间。
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(ghMirrorServeFor))
	if strings.HasPrefix(up.url, ghMirrorBases.api) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	http.ServeContent(w, r, "", st.ModTime(), f)
}

// ghMirrorValidateTagLocation 校验 releases/latest 重定向 Location
// 的主机：绝对地址必须与 ghMirrorBases.github 同主机（相对地址
// 无 host、按基址继承，放行），与版本发现面的
// validateRedirectLocationHost（C-01）同口径、基址换成本镜像的
// github 基址（测试可指向本地 stub）。
func ghMirrorValidateTagLocation(loc string) error {
	u, err := url.Parse(loc)
	if err != nil {
		return fmt.Errorf("重定向地址解析失败: %v", err)
	}
	if u.Host == "" {
		return nil
	}
	base, err := url.Parse(ghMirrorBases.github)
	if err != nil {
		return fmt.Errorf("GitHub 基址配置无效: %v", err)
	}
	if !strings.EqualFold(u.Hostname(), base.Hostname()) {
		return fmt.Errorf("重定向地址主机不受信任: %q", u.Host)
	}
	return nil
}

// ghMirrorLatestTag 从 GitHub 对 …/releases/latest（或给定文件时
// …/releases/latest/download/<file>）的重定向中读出最新 release 的 tag；
// 这两处不像 API 那样限流。结果短暂缓存。
func ghMirrorLatestTag(repo, file string) (string, error) {
	key := strings.ToLower(repo)
	ghMirrorTagMu.Lock()
	cached, ok := ghMirrorTags[key]
	ghMirrorTagMu.Unlock()
	if ok && time.Since(cached.at) < ghMirrorLatest {
		return cached.tag, nil
	}

	// L8-F-04：tag 查询客户端同走钉死拨号（ghMirrorPinnedTransport），
	// 不再裸 http.Client 只看 Location 字符串。
	client := &http.Client{
		Timeout:       15 * time.Second,
		Transport:     ghMirrorPinnedTransport(),
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	lookup := func(path, marker string) (string, error) {
		resp, err := client.Head(ghMirrorBases.github + "/" + repo + path)
		if err != nil {
			return "", err
		}
		resp.Body.Close()
		loc := resp.Header.Get("Location")
		// L2-19：Location 主机必须与 github 基址同源（相对地址无
		// host 时按基址主机继承，放行）——旧实现只按 marker 切串
		// 取 tag，上游/中间人把 Location 指向站外时照单全收；与
		// 版本发现面（validateRedirectLocationHost，C-01）同口径。
		if err := ghMirrorValidateTagLocation(loc); err != nil {
			return "", err
		}
		_, rest, found := strings.Cut(loc, marker)
		tag, _, _ := strings.Cut(rest, "/")
		if !found || !validRedirectTag(tag) {
			return "", fmt.Errorf("GitHub 返回 %d", resp.StatusCode)
		}
		return tag, nil
	}
	tag, err := lookup("/releases/latest", "/releases/tag/")
	if err != nil && file != "" {
		tag, err = lookup("/releases/latest/download/"+file, "/releases/download/")
	}
	if err != nil {
		if ok {
			// R5X-011（R5-C-02）：latest 标签 stale 回退与文件面
			// （ghMirrorFetch，F-137）共用 ghMirrorMaxStaleAge 上限
			// + 双分支日志——旧实现对任意年龄的缓存静默回退，
			// GitHub 长期不可达时旧版被无限期当作 latest 且无日志
			// 可见。超限返回查询错误，不得无上限静默回退。
			if age := time.Since(cached.at); age <= ghMirrorMaxStaleAge {
				log.Printf("gh_mirror: 查询 %s 最新版本失败（%v），回退缓存标签 %s（年龄 %s，上限 %s）", repo, err, cached.tag, age.Round(time.Second), ghMirrorMaxStaleAge)
				return cached.tag, nil
			} else {
				log.Printf("gh_mirror: 查询 %s 最新版本失败（%v），缓存标签 %s 年龄 %s 超过上限 %s，不回退", repo, err, cached.tag, age.Round(time.Second), ghMirrorMaxStaleAge)
			}
		}
		return "", fmt.Errorf("查询 %s 最新版本失败: %v", repo, err)
	}
	ghMirrorTagMu.Lock()
	ghMirrorTags[key] = ghMirrorTag{tag: tag, at: time.Now()}
	ghMirrorTagMu.Unlock()
	return tag, nil
}

func ghMirrorCacheDir() string {
	if ghMirrorDir != "" {
		return ghMirrorDir
	}
	if p := strings.TrimSpace(os.Getenv("DB_PATH")); p != "" {
		return filepath.Join(filepath.Dir(p), "github-cache")
	}
	return filepath.Join(os.TempDir(), "flmx-github-cache")
}

// ghMirrorFetch 返回 up 对应的缓存文件；缺失或过期时下载。
// 对同一文件的并发请求共享一次下载。
func ghMirrorFetch(up *ghMirrorUpstream) (string, error) {
	sum := sha256.Sum256([]byte(up.url))
	path := filepath.Join(ghMirrorCacheDir(), hex.EncodeToString(sum[:]))
	if st, err := os.Stat(path); err == nil {
		age := time.Since(st.ModTime())
		if up.fresh == 0 || age < up.fresh {
			return path, nil
		}
		// R6X-012：节流窗口内（上次下载失败尝试距今 < fresh）
		// 直接回退旧副本，不必每个请求都去等 GitHub——但仅限年龄
		// 未超上限；已超限的副本不得借节流续命，落到下载尝试，
		// 失败后由回退段按真实年龄报错（上游恢复则正常更新）。
		if age <= ghMirrorMaxStaleAge {
			if last, ok := ghMirrorLastAttempt(path); ok && time.Since(last) < up.fresh {
				return path, nil
			}
		}
	}

	ghMirrorFlightMu.Lock()
	if f, ok := ghMirrorFlights[path]; ok {
		ghMirrorFlightMu.Unlock()
		<-f.done
		if f.err != nil {
			return "", f.err
		}
		return path, nil
	}
	f := &ghMirrorFlight{done: make(chan struct{})}
	ghMirrorFlights[path] = f
	ghMirrorFlightMu.Unlock()

	f.err = ghMirrorDownload(up.url, path)
	if f.err == nil {
		ghMirrorClearAttempt(path)
	}
	if f.err != nil {
		// 本次尝试计入节流时钟（无论最终是否回退），见 ghMirrorAttempts。
		ghMirrorRecordAttempt(path)
		if st, err := os.Stat(path); err == nil {
			// F-137：stale 回退必须可见且有年龄上限。此前静默返回任意
			// 年龄的旧副本（并 Chtimes 续期），故障可被长期掩盖。
			// 超过上限不再回退、保留下载错误；上限内回退时打警告日志。
			//（本层只做可用性缓存：内容完整性由下游各自的 sha256 校验
			// 负责——节点安装命令已在 F-060 补齐校验，不再裸奔。）
			// R6X-012：年龄取内容真实年龄——mtime 自成功下载后从未被
			// 回退/节流改写（续期已删，节流走 ghMirrorAttempts），故
			// 连续失败 + 周期请求下年龄照常累积、越限即止。
			if age := time.Since(st.ModTime()); age <= ghMirrorMaxStaleAge {
				log.Printf("gh_mirror: 下载失败（%v），回退 %s 的缓存副本（年龄 %s，上限 %s）", f.err, up.url, age.Round(time.Second), ghMirrorMaxStaleAge)
				f.err = nil
			} else {
				log.Printf("gh_mirror: 下载失败（%v），%s 的缓存副本年龄 %s 超过上限 %s，不回退", f.err, up.url, age.Round(time.Second), ghMirrorMaxStaleAge)
			}
		}
	}
	ghMirrorFlightMu.Lock()
	delete(ghMirrorFlights, path)
	ghMirrorFlightMu.Unlock()
	close(f.done)
	if f.err != nil {
		return "", f.err
	}
	return path, nil
}

func ghMirrorDownload(rawURL, path string) error {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	if strings.HasPrefix(rawURL, ghMirrorBases.api) {
		req.Header.Set("Accept", "application/vnd.github+json")
		if token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}
	resp, err := ghMirrorHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("面板下载 GitHub 文件失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub 返回 %d", resp.StatusCode)
	}
	if resp.ContentLength > ghMirrorMaxFile {
		return fmt.Errorf("文件过大")
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".download-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	n, err := io.Copy(tmp, io.LimitReader(resp.Body, ghMirrorMaxFile+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("面板下载 GitHub 文件中断: %v", err)
	}
	if n > ghMirrorMaxFile {
		return fmt.Errorf("文件过大")
	}
	if resp.ContentLength >= 0 && n != resp.ContentLength {
		return fmt.Errorf("面板下载 GitHub 文件不完整")
	}
	// L8-F-04：API 面的响应在入缓存前做基本内容校验——镜像的
	// API JSON 是版本元数据，下游（内核版本列表等）按 JSON 解码
	// 消费；上游被污染/DNS 被劫持时返回的落地页 HTML 若原样入
	// 缓存，会在 fresh 期内持续毒化消费方。仅 API 基址的下载
	// 走本校验（二进制/raw 不受影响），且体积另有 API 专用上限，
	// 防把 300MB 级内容当 JSON 读入。
	if strings.HasPrefix(rawURL, ghMirrorBases.api) {
		if err := ghMirrorValidateAPIContent(tmp.Name(), n); err != nil {
			return err
		}
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	ghMirrorPrune(dir, path)
	return nil
}

// ghMirrorMaxAPIContent 是镜像 API 面单响应的内容校验上限：
// GitHub releases API 单页（per_page≤100）正常在数 MB 内，32MB
// 留足余量；超过即按异常内容拒绝入缓存（与文件面 300MB 上限
// 分开，防大文件被当 JSON 全量读入）。
const ghMirrorMaxAPIContent = 32 << 20

// ghMirrorValidateAPIContent 校验已下载到 path 的 API 响应是合法
// JSON 且顶层为对象/数组（releases API 的两种形态）。校验失败
// 返回错误，调用方不落缓存。（按路径重开：下载侧的临时文件在
// 校验点已 Close，不可复用其句柄做 Seek。）
func ghMirrorValidateAPIContent(path string, size int64) error {
	if size > ghMirrorMaxAPIContent {
		return fmt.Errorf("GitHub API 响应过大（%d 字节），已拒绝入缓存", size)
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("校验 GitHub API 响应失败: %v", err)
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, ghMirrorMaxAPIContent+1))
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return fmt.Errorf("GitHub API 响应不是合法 JSON，已拒绝入缓存: %v", err)
	}
	trimmed := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
		return fmt.Errorf("GitHub API 响应顶层不是对象/数组，已拒绝入缓存")
	}
	// 尾随非空白内容同样判非法（防 JSON+落地页拼接的污染形态）。
	if dec.More() {
		return fmt.Errorf("GitHub API 响应含尾随内容，已拒绝入缓存")
	}
	return nil
}

// ghMirrorPrune 把缓存控制在 ghMirrorMaxCache 以内，按抓取时间从旧到新
// 删除（刚写好的 keep 文件永不删除）。
func ghMirrorPrune(dir, keep string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type file struct {
		path string
		size int64
		at   time.Time
	}
	var files []file
	var total int64
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		files = append(files, file{filepath.Join(dir, e.Name()), info.Size(), info.ModTime()})
		total += info.Size()
	}
	sort.Slice(files, func(i, j int) bool { return files[i].at.Before(files[j].at) })
	for _, f := range files {
		if total <= ghMirrorMaxCache {
			return
		}
		if f.path == keep {
			continue
		}
		if os.Remove(f.path) == nil {
			total -= f.size
		}
	}
}

// isPanelGithubMirror 判断一个加速地址是否为面板自身的镜像。
// 是的话面板直接从 GitHub 下载，不再经过自己。
func isPanelGithubMirror(proxyURL string) bool {
	return strings.HasSuffix(strings.TrimRight(proxyURL, "/"), strings.TrimRight(ghMirrorPath, "/"))
}

// --- 面板源分发二期：升级/安装下发前的缓存预热与期望哈希 ---
//
// agent 侧已把本端点作为一条下载线路纳入测速排名（面板源一期），
// 缓存冷时首台节点要等本端点回源 GitHub。下发侧在派发前先行
// 预热：同一版本同一架构全网只回源一次（singleflight + 永久
// 缓存），预热失败只记日志、绝不阻塞下发（agent 侧有懒加载与
// 全套线路回退兜底）。期望哈希取自本端点缓存的官方 .sha256，
// 经 WSS 控制通道随升级命令下发，成为校验的信任根。

var ghMirrorSHA256Hex = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// ghMirrorReleaseAssetUpstream 组装本仓库某个 release 资产的镜像
// 上游描述（tag 资产，永久缓存形态）。
func ghMirrorReleaseAssetUpstream(version, file string) (*ghMirrorUpstream, error) {
	return parseGHMirrorPath("https://github.com/"+githubRepo+"/releases/download/"+version+"/"+file, "")
}

// ghMirrorWarmReleaseAsset 确保一个 release 资产已在镜像缓存中
// （缺失/过期时回源抓取）。供下发前预热调用。
func ghMirrorWarmReleaseAsset(version, file string) error {
	up, err := ghMirrorReleaseAssetUpstream(version, file)
	if err != nil {
		return err
	}
	_, err = ghMirrorFetch(up)
	return err
}

// ghMirrorReleaseSHA256 返回版本 version 的 gost-<arch> 期望
// SHA256：取镜像缓存中官方 .sha256 的首字段（缓存缺失时先回源
// 抓取）。取不到、内容不合法时返回空串——调用方据此不下发期望
// 哈希，agent 回落同源 .sha256 比对（一期口径）。
func ghMirrorReleaseSHA256(version, arch string) string {
	up, err := ghMirrorReleaseAssetUpstream(version, "gost-"+arch+".sha256")
	if err != nil {
		return ""
	}
	path, err := ghMirrorFetch(up)
	if err != nil {
		return ""
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(body))
	if len(fields) == 0 || !ghMirrorSHA256Hex.MatchString(fields[0]) {
		return ""
	}
	return strings.ToLower(fields[0])
}
