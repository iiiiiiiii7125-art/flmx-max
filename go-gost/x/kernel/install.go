package kernel

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/go-gost/x/bypass"
	"github.com/go-gost/x/internal/util/download"
)

const (
	maxDownloadBytes = 256 << 20
	maxEntryBytes    = 256 << 20
	downloadTimeout  = 4 * time.Minute
)

// fetchStallWindow is the no-progress watchdog window applied to
// every fetch attempt (R3-WDG-01): a peer that answers and then
// stalls or trickles the body fails the attempt after this long
// with zero byte progress instead of burning the whole-operation
// budget (downloadTimeout / geoTimeout), so the mirror/direct
// fallback leg runs with budget to spare. The value is the shared
// download.StallWindow (45s), registered in
// doc/audit/timeout-budgets.tsv rows A001/A002. A var so tests can
// shrink it.
var fetchStallWindow = download.StallWindow

var sha256HexPattern = regexp.MustCompile(`\b[0-9a-fA-F]{64}\b`)

// parseChecksum extracts the first SHA-256 digest from a checksum file. It
// accepts `sha256sum` output as well as Xray's .dgst files, where the 64-hex
// token is the only one of that length (MD5/SHA1/SHA512 differ).
func parseChecksum(body string) (string, bool) {
	for _, line := range strings.Split(body, "\n") {
		upper := strings.ToUpper(line)
		if strings.Contains(upper, "SHA2-512") || strings.Contains(upper, "SHA512") {
			continue
		}
		if m := sha256HexPattern.FindString(line); m != "" {
			return strings.ToLower(m), true
		}
	}
	return "", false
}

func resolveArch(d *Download) (string, error) {
	if len(d.ArchMap) == 0 {
		return runtime.GOARCH, nil
	}
	if v, ok := d.ArchMap[runtime.GOARCH]; ok && v != "" {
		return v, nil
	}
	return "", fmt.Errorf("内核不支持当前架构 %s", runtime.GOARCH)
}

// --- R7-C-03: fetch() outbound policy ---
//
// fetch is the shared download primitive for kernel installs and geo
// data, but it rode http.DefaultClient: redirects were followed
// without any scheme or target policy, so the geo face's F-090
// "https-only" entry check could be undone by a single 302 to an
// http / internal address — while the agent self-upgrade path has
// had a redirect whitelist since S-02. fetch now uses an explicit
// client, layered like the verdict's adopted scheme:
//
//   - Entry URL: scheme http/https, host present, every resolved IP
//     clear of the hard-deny layer (bypass.IsHardDenyIP, the agent
//     mirror of netguard's hard set). The entry itself is built by
//     the panel or the GitHub API and its consumers add their own
//     gates on top (geo validate() requires https, F-090; content
//     is sha256-verified before use), so the primitive does not ban
//     non-public entries outright.
//   - Redirect hops (max 3, same cap as the S-02 self-upgrade path):
//     https is preserved — a hop may never downgrade below a scheme
//     already seen in the chain — and the hop target is fully
//     judged: hard-deny and every non-public range rejected
//     (bypass.IsNonPublicIP). GitHub's official 302s to
//     *.githubusercontent.com are https + public and unaffected.
//   - The dial is pinned and re-judges the hard-deny layer on the
//     IPs it actually dials, so rebinding cannot swap in a metadata
//     target between the hop check and the connect.

var fetchLookupIP = func(ctx context.Context, host string) ([]net.IP, error) {
	return net.DefaultResolver.LookupIP(ctx, "ip", host)
}

// judgeFetchHost resolves host and judges every answer. hardOnly
// applies just the hard-deny layer (entry / dial); otherwise the
// full non-public judgment applies (redirect hops).
func judgeFetchHost(ctx context.Context, host string, hardOnly bool) ([]net.IP, error) {
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		var err error
		ips, err = fetchLookupIP(ctx, host)
		if err != nil || len(ips) == 0 {
			return nil, fmt.Errorf("下载地址无法解析，已拒绝: %s", host)
		}
	}
	for _, ip := range ips {
		if bypass.IsHardDenyIP(ip) {
			return nil, fmt.Errorf("下载地址指向云元数据服务，已拒绝: %s", host)
		}
		if !hardOnly && bypass.IsNonPublicIP(ip) {
			return nil, fmt.Errorf("下载地址指向非公网地址，已拒绝: %s", host)
		}
	}
	return ips, nil
}

func validateFetchEntryURL(ctx context.Context, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("下载地址无效，已拒绝")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("下载地址 scheme 不支持，已拒绝: %s", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("下载地址主机为空，已拒绝")
	}
	_, err = judgeFetchHost(ctx, host, true)
	return err
}

// fetchDialContext pins the dial: re-resolve, re-judge the hard-deny
// layer, dial a validated IP.
func fetchDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := judgeFetchHost(ctx, host, true)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: 30 * time.Second}
	var lastErr error
	for _, ip := range ips {
		dialIP := ip
		if ip4 := ip.To4(); ip4 != nil {
			dialIP = ip4
		}
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(dialIP.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func fetchHTTPClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil // pinned dialing is meaningless via a proxy
	tr.DialContext = fetchDialContext
	return &http.Client{
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return fmt.Errorf("下载重定向次数过多，已拒绝")
			}
			// https preservation: once any hop in the chain was
			// https, no later hop may drop to http.
			for _, v := range via {
				if v.URL.Scheme == "https" && req.URL.Scheme != "https" {
					return fmt.Errorf("下载重定向降级到 http，已拒绝: %s", req.URL.Host)
				}
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("下载重定向 scheme 不支持，已拒绝: %s", req.URL.Scheme)
			}
			host := req.URL.Hostname()
			if host == "" {
				return fmt.Errorf("下载重定向主机为空，已拒绝")
			}
			_, err := judgeFetchHost(req.Context(), host, false)
			return err
		},
	}
}

func fetch(ctx context.Context, url string, limit int64, w io.Writer) error {
	if err := validateFetchEntryURL(ctx, url); err != nil {
		return err
	}
	// R3-WDG-01: no-progress watchdog for this attempt. The watch
	// starts now, so a peer that never sends headers trips it too;
	// body bytes count as progress via the wrapped writer. On a
	// stall the derived context is cancelled and the attempt fails
	// with download.ErrNoProgress, leaving the caller's fallback
	// sequence (mirror strip / next file) its remaining budget.
	watcher := download.NewWatcher(fetchStallWindow)
	ctx, stopWatch := watcher.Watch(ctx)
	defer stopWatch()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := fetchHTTPClient().Do(req)
	if err != nil {
		if watcher.Stalled() {
			return fmt.Errorf("下载失败: %w", download.ErrNoProgress)
		}
		return fmt.Errorf("下载失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载失败: HTTP %d (%s)", resp.StatusCode, url)
	}
	n, err := io.Copy(watcher.Writer(w), io.LimitReader(resp.Body, limit+1))
	if err != nil {
		if watcher.Stalled() {
			return fmt.Errorf("下载中断: %w", download.ErrNoProgress)
		}
		return fmt.Errorf("下载中断: %v", err)
	}
	if n > limit {
		return fmt.Errorf("下载文件过大")
	}
	return nil
}

// fetchFileResume performs ONE attempt of a resumable file
// download (download.FileTransfer) with fetch's entry-URL SSRF
// judging and no-progress watchdog caliber: the attempt's bytes
// stay in the transfer when the leg fails, and the caller's next
// route resumes after them (1.7.12 cross-route resume, shared by
// the kernel binary leg in install and the geodata body leg in
// stageGeoFile). The watchdog is created per attempt, exactly as
// in fetch, and a stall surfaces from the transfer as
// download.ErrNoProgress.
func fetchFileResume(ctx context.Context, transfer *download.FileTransfer, url string) error {
	if err := validateFetchEntryURL(ctx, url); err != nil {
		return err
	}
	watcher := download.NewWatcher(fetchStallWindow)
	return transfer.Attempt(ctx, watcher, fetchHTTPClient().Do, url, nil)
}

// stripMirrorPrefix 若 url 是镜像前缀形式（<前缀>/https://目标地址），
// 返回其中的直连地址；否则返回空串。KU4 (1.7.7) 起它只作 URL 归一化：
// 面板下发的地址可能带某个镜像前缀，下载路由改由本机的测速计划
// （download.Plan）决定，先剥到直连形式再由计划按候选重建前缀。
func stripMirrorPrefix(url string) string {
	for _, scheme := range []string{"/https://", "/http://"} {
		if i := strings.Index(url, scheme); i > 0 {
			return url[i+1:]
		}
	}
	return ""
}

// directForm returns url in direct form (any mirror prefix the
// panel attached stripped); already-direct URLs pass through.
func directForm(url string) string {
	if d := stripMirrorPrefix(url); d != "" {
		return d
	}
	return url
}

// --- KU4 (1.7.7): probe-selected routes for kernel downloads ---
//
// Kernel downloads used to derive their route from the panel: the
// download URLs could arrive mirror-prefixed and GitHubRelease
// named ONE mirror; a failed attempt then stripped the prefix and
// retried direct (the deleted fetchWithMirrorFallback and its
// twins). The route is now decided where the download happens,
// by the shared direct-first probe plan (download.Plan): direct
// is probed alone against the operation's real small target;
// only when direct is not admitted are the built-in mirrors
// probed and ranked by latency. Panel-supplied routing (a
// prefixed URL, g.Proxy) no longer decides anything.

// checksumProbe admits a route only when it delivers the real
// checksum file with a parseable SHA-256 — never a bare 200 (a
// mirror's landing page is fake-alive and stays out).
func checksumProbe(ctx context.Context, url string) error {
	var buf bytes.Buffer
	if err := fetch(ctx, url, 64<<10, &buf); err != nil {
		return err
	}
	if _, ok := parseChecksum(buf.String()); !ok {
		return fmt.Errorf("校验文件内容无可解析的 SHA256")
	}
	return nil
}

// apiProbe admits a route only when it delivers a parseable
// GitHub release JSON with a tag — mirrors that answer their
// landing page (or refuse API paths, like ghfast) stay out.
func apiProbe(ctx context.Context, url string) error {
	var buf bytes.Buffer
	if err := fetch(ctx, url, 4<<20, &buf); err != nil {
		return err
	}
	var rel githubReleaseJSON
	if err := json.Unmarshal(buf.Bytes(), &rel); err != nil {
		return fmt.Errorf("发布信息不可解析: %v", err)
	}
	if rel.TagName == "" {
		return fmt.Errorf("发布信息缺少 tag_name")
	}
	return nil
}

// assetProbe is the light admission probe for operations with no
// small companion file: the route must answer the asset URL
// itself with 200; only the first bytes are read, so the cost
// stays bounded by the probe context the plan wraps it in.
func assetProbe(ctx context.Context, url string) error {
	if err := validateFetchEntryURL(ctx, url); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := fetchHTTPClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载失败: HTTP %d (%s)", resp.StatusCode, url)
	}
	_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return err
}

// fetchRanked fetches a small file (checksum / API response) over
// the route plan: each attempt's content lands in a buffer first
// and is written to w only on success, so a failed attempt never
// leaves partial content behind (the deleted
// fetchWithMirrorFallback contract, generalized to the plan).
func fetchRanked(ctx context.Context, plan *download.Plan, directURL string, limit int64, w io.Writer) error {
	_, err := download.TryPlan(plan, directURL, func(route download.Route, url string) error {
		var buf bytes.Buffer
		if err := fetch(ctx, url, limit, &buf); err != nil {
			return err
		}
		_, err := io.Copy(w, &buf)
		return err
	})
	return err
}

// githubAPIBase is a variable so tests can point it at a local server.
var githubAPIBase = "https://api.github.com"

type githubReleaseJSON struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		Digest             string `json:"digest"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

// resolveGitHub fills in the download URL and digest of a release asset from
// the GitHub API (the asset "digest" field is "sha256:<hex>").
func resolveGitHub(ctx context.Context, g *GitHubRelease, arch string) (url, digest string, err error) {
	path := "latest"
	if g.Tag != "" {
		path = "tags/" + g.Tag
	}
	apiURL := fmt.Sprintf("%s/repos/%s/releases/%s", githubAPIBase, g.Repo, path)
	// F-061 口径、KU4 (1.7.7) 形态：API 请求与下载/校验走同一路由
	// 策略——测速计划。计划以 API 地址本身为探针目标：某条线路
	// 只有真回一份可解析的发布 JSON 才准入，不转发 API 的镜像
	// （ghfast 回 403/落地页）被自动排除。g.Proxy 不再参与路由
	// （面板 GitHub 加速设置已退役，路由由本机内置候选测速决定）；
	// 返回的资产地址是直连形式，由 install 的计划负责选线。
	plan := download.NewPlan(ctx, apiURL, apiProbe, panelExtraRoutes()...)
	var body strings.Builder
	if err := fetchRanked(ctx, plan, apiURL, 4<<20, &body); err != nil {
		return "", "", fmt.Errorf("查询 GitHub 发布信息失败: %v", err)
	}
	var rel githubReleaseJSON
	if err := json.Unmarshal([]byte(body.String()), &rel); err != nil {
		return "", "", fmt.Errorf("解析 GitHub 发布信息失败: %v", err)
	}
	version := strings.TrimPrefix(rel.TagName, "v")
	name := strings.NewReplacer("{VERSION}", version, "{ARCH}", arch).Replace(g.Asset)
	for _, a := range rel.Assets {
		if a.Name != name {
			continue
		}
		digest = strings.ToLower(strings.TrimPrefix(a.Digest, "sha256:"))
		if !sha256HexPattern.MatchString(digest) || len(digest) != 64 {
			return "", "", fmt.Errorf("GitHub 未提供 %s 的校验值", name)
		}
		url = a.BrowserDownloadURL
		return url, digest, nil
	}
	return "", "", fmt.Errorf("发布 %s 中没有文件 %s", rel.TagName, name)
}

// install downloads, verifies and unpacks a binary into binDir. It refuses to
// install anything whose SHA-256 it cannot establish.
func install(d *Download, binDir string) error {
	arch, err := resolveArch(d)
	if err != nil {
		return err
	}
	url := strings.ReplaceAll(d.URL, "{ARCH}", arch)
	sumURL := strings.ReplaceAll(d.ChecksumURL, "{ARCH}", arch)
	want := strings.ToLower(d.SHA256[arch])

	ctx, cancel := context.WithTimeout(context.Background(), downloadTimeout)
	defer cancel()

	if d.GitHub != nil && (strings.Contains(url, "{VERSION}") || url == "" || (want == "" && sumURL == "")) {
		ghURL, digest, err := resolveGitHub(ctx, d.GitHub, arch)
		if err != nil {
			return err
		}
		url, want = ghURL, digest
	}
	if strings.Contains(url, "{VERSION}") || url == "" {
		return fmt.Errorf("下载地址不完整")
	}
	if want == "" && sumURL == "" {
		return fmt.Errorf("没有可用的 SHA256 校验值，拒绝安装")
	}

	if err := os.MkdirAll(filepath.Dir(binDir), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(binDir), ".download-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	// KU4 (1.7.7): 面板下发的地址可能带镜像前缀——先归一化为直连
	// 形式，路由由本次安装的测速计划决定（二进制与校验两腿共用
	// 一次探测，不逐文件重测）。探针目标优先校验文件（小且内容
	// 可验），没有校验文件时以资产地址本身作轻量探针。
	url = directForm(url)
	sumURL = directForm(sumURL)
	var plan *download.Plan
	if sumURL != "" {
		plan = download.NewPlan(ctx, sumURL, checksumProbe, panelExtraRoutes()...)
	} else {
		plan = download.NewPlan(ctx, url, assetProbe, panelExtraRoutes()...)
	}

	// 本体下载走可续传的共享 transfer（download.FileTransfer，
	// 1.7.12）：一条线路中途失败时已落盘字节保留，下一线路以
	// Range 续传，不再每腿从 0 重下（慢节点在整程预算内被首腿
	// 涓流饿死的修复口径，与 agent 自升级同源）。哈希由 transfer
	// 持续累计，成功后与 want 的比对口径不变。
	transfer := download.NewFileTransfer(tmp.Name(), maxDownloadBytes, 0o600)
	if _, err := download.TryPlan(plan, url, func(route download.Route, u string) error {
		return fetchFileResume(ctx, transfer, u)
	}); err != nil {
		return err
	}
	if want == "" {
		var sb strings.Builder
		if err := fetchRanked(ctx, plan, sumURL, 64<<10, &sb); err != nil {
			return fmt.Errorf("获取校验文件失败: %v", err)
		}
		var ok bool
		if want, ok = parseChecksum(sb.String()); !ok {
			return fmt.Errorf("校验文件中没有 SHA256")
		}
	}
	if got := transfer.Sum(); got != want {
		return fmt.Errorf("SHA256 校验失败: %s != %s", got, want)
	}

	staging := binDir + ".staging"
	_ = os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(staging)

	switch d.Archive {
	case "zip":
		err = extractZip(tmp.Name(), staging)
	case "tar.gz":
		err = extractTarGz(tmp.Name(), staging)
	default:
		err = copyFile(tmp.Name(), filepath.Join(staging, d.BinName), 0o755)
	}
	if err != nil {
		return err
	}
	bin := filepath.Join(staging, d.BinName)
	if st, err := os.Stat(bin); err != nil || st.IsDir() {
		return fmt.Errorf("压缩包中没有找到 %s", d.BinName)
	}
	if err := os.Chmod(bin, 0o755); err != nil {
		return err
	}
	_ = os.RemoveAll(binDir)
	return os.Rename(staging, binDir)
}

// entryName flattens archive paths ("sing-box-1.2-linux-amd64/sing-box") to
// their base name and rejects anything unsafe.
func entryName(name string) (string, bool) {
	base := filepath.Base(filepath.Clean("/" + name))
	if base == "/" || base == "." || !safeName(base) {
		return "", false
	}
	return base, true
}

func extractZip(src, dst string) error {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return fmt.Errorf("解压失败: %v", err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		if !f.Mode().IsRegular() {
			continue
		}
		name, ok := entryName(f.Name)
		if !ok {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		err = writeLimited(filepath.Join(dst, name), rc, f.Mode().Perm()|0o600)
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func extractTarGz(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("解压失败: %v", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("解压失败: %v", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		name, ok := entryName(hdr.Name)
		if !ok {
			continue
		}
		if err := writeLimited(filepath.Join(dst, name), tr, os.FileMode(hdr.Mode).Perm()|0o600); err != nil {
			return err
		}
	}
}

func writeLimited(path string, r io.Reader, mode os.FileMode) error {
	out, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, io.LimitReader(r, maxEntryBytes+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > maxEntryBytes {
		err = fmt.Errorf("压缩包内文件过大")
	}
	return err
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	return writeLimited(dst, in, mode)
}

// writeFileAtomic replaces path without leaving a half-written config behind.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
