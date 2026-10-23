package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
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

var (
	ghMirrorDir   = "" // set from DB_PATH; tests point it elsewhere
	ghMirrorHTTP  = &http.Client{Timeout: 10 * time.Minute, CheckRedirect: ghMirrorCheckRedirect}
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

func ghMirrorRecordAttempt(path string) {
	ghMirrorAttemptMu.Lock()
	ghMirrorAttempts[path] = time.Now()
	ghMirrorAttemptMu.Unlock()
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
	if host == "github.com" || strings.HasSuffix(host, ".githubusercontent.com") {
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
	// J-002：开放代理限流，防带宽/磁盘被刷。
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		if !ghMirrorAllow(host) {
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
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

	client := &http.Client{
		Timeout:       15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	lookup := func(path, marker string) (string, error) {
		resp, err := client.Head(ghMirrorBases.github + "/" + repo + path)
		if err != nil {
			return "", err
		}
		resp.Body.Close()
		_, rest, found := strings.Cut(resp.Header.Get("Location"), marker)
		tag, _, _ := strings.Cut(rest, "/")
		if !found || !ghMirrorSegment.MatchString(tag) {
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
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	ghMirrorPrune(dir, path)
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
