package handler

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/scrypt"

	"go-backend/internal/http/response"
	"go-backend/internal/netguard"
	"go-backend/internal/store/model"
)

// PORT-3342/B1：备份上传 GitHub Gist（移植自上游 kure29/flyx 3.3.42
// go-backend/internal/http/handler/gist_backup.go，仅上传链）。
// PORT-3342/G-backend（本批，在 B1 基础上）：
//   - 新增 versions 端点（列 Gist history 最近 10 版本）；restore 端点
//     本批不接（归 G-恢复链批），但恢复侧复用的取版本内容 helper
//     gistVersionContent 与解密 gistOpen 已落到位，不触导入链。
//   - T-4（用户钉死，比上游收紧）：未设加密密码时上传一律拒绝，
//     不得明文离境；见 gistUploadLocked 开头的门禁。前端警示文案
//     的改动归 G-前端批，本批只改后端语义 + 单测。
//   - 加密信封解密侧双认 flyx/flmx 标记（写侧仍写 flyx，与上游
//     线格式互通；见 gistOpen）。
//
// 与上游的偏差（均有意，见报告）：
//   - 不含 versions/restore：从 Gist 恢复会走备份导入链（含导入重推
//     谓词），本轮红线禁止触碰导入代码，故只移植上传侧；恢复仍走
//     现有手动导入流程。
//   - 导出用 ExportAllContext（绑定 ctx，X-P3-25 口径），而非上游的
//     ExportAll()；内容与手动全量导出一致（空 types = 全量）。
//   - 文件名与描述用 FLMX 品牌（flmx-backup.json / FLMX 面板备份）。
//   - API 基址环境变量为 FLMX_GIST_API_BASE（FLVX_GIST_API_BASE 仅
//     作兼容回落），默认 https://api.github.com。
//   - 三个端点仅管理员可用（isAdminRequest）：配置页对任意已登录用户
//     开放，而 Gist 令牌是管理员凭据；上游未设此限。
//   - 令牌/密码绝不进日志、不进响应：status 只给 tokenHint；
//     config/list 即使对管理员也删 token/password（与上游一致）。
//
// 默认关闭：未填令牌时 gistDue 恒 false，定时循环不动作；手动上传
// 无令牌时报错；现有备份导出/导入行为不受影响。

const (
	gistConfigToken    = "gist_token"
	gistConfigPassword = "gist_password"
	gistConfigID       = "gist_id"
	gistConfigInterval = "gist_interval_hours"
	// gistConfigLastAt 是最近一次上传尝试（unix ms，含失败），
	// gistConfigLastUpload 是最近一次产生新版本的时间，
	// gistConfigLastHash 标识其内容，内容不变时跳过上传。
	gistConfigLastAt     = "gist_last_at"
	gistConfigLastUpload = "gist_last_upload"
	gistConfigLastError  = "gist_last_error"
	gistConfigLastHash   = "gist_last_hash"

	gistFileName       = "flmx-backup.json"
	gistMaxBackupBytes = 20 << 20
	notifyEventBackup  = "backup_failed"
)

// notifyGistFailurePendingConfigKey holds the pending Gist backup-failure
// notice (R2-I-02). Runtime ledger, not admin config: excluded from
// backups by backupRuntimeExcludedConfigKeys, like the I-01 quota
// recovery pending key.
const notifyGistFailurePendingConfigKey = "notify_gist_failure_pending"

// gistIntervals 是自动上传的可选项（小时；0 为关闭）。
var gistIntervals = map[int]bool{0: true, 6: true, 12: true, 24: true, 168: true}

var (
	gistCheckInterval = 5 * time.Minute
	gistMu            sync.Mutex
)

// gistAPIBase 返回 GitHub API 基址；测试用环境变量指向 mock 服务。
func gistAPIBase() string {
	if v := strings.TrimRight(strings.TrimSpace(os.Getenv("FLMX_GIST_API_BASE")), "/"); v != "" {
		return v
	}
	if v := strings.TrimRight(strings.TrimSpace(os.Getenv("FLVX_GIST_API_BASE")), "/"); v != "" {
		return v
	}
	return "https://api.github.com"
}

func (h *Handler) gistConfig() map[string]string {
	cfg, err := h.repo.ListConfigs()
	if err != nil || cfg == nil {
		return map[string]string{}
	}
	return cfg
}

func gistInt64(cfg map[string]string, key string) int64 {
	v, _ := strconv.ParseInt(strings.TrimSpace(cfg[key]), 10, 64)
	return v
}

// --- encryption ---
//
// 信封格式与上游逐字节一致（scrypt N=32768/r=8/p=1 + AES-256-GCM），
// 以便上游面板产出的加密 Gist 与本实现互通。注意 JSON 键名 "flyx"
// 是上游的原始写法（字段名 Format 的 tag），属线格式、不得「修正」。

type gistEnvelope struct {
	Format string `json:"flyx"`
	V      int    `json:"v"`
	KDF    string `json:"kdf"`
	N      int    `json:"n"`
	R      int    `json:"r"`
	P      int    `json:"p"`
	Salt   string `json:"salt"`
	Nonce  string `json:"nonce"`
	Data   string `json:"data"`
}

const gistEnvelopeFormat = "encrypted-backup"

func gistKey(password string, salt []byte, n, r, p int) ([]byte, error) {
	return scrypt.Key([]byte(password), salt, n, r, p, 32)
}

func gistEncrypt(plain []byte, password string) ([]byte, error) {
	env := gistEnvelope{Format: gistEnvelopeFormat, V: 1, KDF: "scrypt", N: 1 << 15, R: 8, P: 1}
	salt, nonce := make([]byte, 16), make([]byte, 12)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	key, err := gistKey(password, salt, env.N, env.R, env.P)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	env.Salt = base64.StdEncoding.EncodeToString(salt)
	env.Nonce = base64.StdEncoding.EncodeToString(nonce)
	env.Data = base64.StdEncoding.EncodeToString(gcm.Seal(nil, nonce, plain, nil))
	return json.MarshalIndent(env, "", "  ")
}

// gistOpen 还原 Gist 文件内容：加密信封需密码解密，明文原样返回。
// 信封标记双认：上游写法 "flyx" 与我方品牌写法 "flmx" 均认作加密
// 信封（写侧仍写 "flyx"，与上游线格式互通；其余信封字段两写法
// 相同，故可共用同一解析）。本轮上传侧不调用它（仅单测验证加密
// 往返与未来恢复侧复用）。
func gistOpen(content []byte, password string) ([]byte, bool, error) {
	var env gistEnvelope
	if json.Unmarshal(content, &env) != nil {
		return content, false, nil
	}
	// 双认：任一标记等于信封格式即为加密信封；否则按明文原样返回。
	// 直接用 map 判定，避免 struct 只有一个标记字段导致另一写法漏认。
	var raw map[string]interface{}
	if json.Unmarshal(content, &raw) != nil {
		return content, false, nil
	}
	isEnvelope := raw["flyx"] == gistEnvelopeFormat || raw["flmx"] == gistEnvelopeFormat
	if !isEnvelope {
		return content, false, nil
	}
	if password == "" {
		return nil, true, errText("这个版本是加密的，请输入上传时用的密码")
	}
	if env.KDF != "scrypt" || env.N <= 1 || env.N > 1<<20 || env.R <= 0 || env.R > 32 || env.P <= 0 || env.P > 16 {
		return nil, true, errText("不认识的加密格式")
	}
	salt, err1 := base64.StdEncoding.DecodeString(env.Salt)
	nonce, err2 := base64.StdEncoding.DecodeString(env.Nonce)
	data, err3 := base64.StdEncoding.DecodeString(env.Data)
	if errors.Join(err1, err2, err3) != nil || len(nonce) != 12 {
		return nil, true, errText("加密数据已损坏")
	}
	key, err := gistKey(password, salt, env.N, env.R, env.P)
	if err != nil {
		return nil, true, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, true, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, true, err
	}
	plain, err := gcm.Open(nil, nonce, data, nil)
	if err != nil {
		return nil, true, errText("密码不对，或者数据已损坏")
	}
	return plain, true, nil
}

// --- GitHub API ---

type gistFile struct {
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
	RawURL    string `json:"raw_url"`
	Size      int64  `json:"size"`
}

type gistDoc struct {
	ID      string              `json:"id"`
	HTMLURL string              `json:"html_url"`
	Files   map[string]gistFile `json:"files"`
	History []gistHistoryEntry  `json:"history"`
}

type gistHistoryEntry struct {
	Version     string `json:"version"`
	CommittedAt string `json:"committed_at"`
}

var errGistNotFound = errors.New("gist not found")

// --- R7-C-01: controlled Gist outbound client ---
//
// gistRequest used to ride http.DefaultClient with no SSRF guard at
// all, and attached the GitHub token to every URL it was handed —
// including the raw_url taken from an API response (C-02 family
// recurrence). All Gist outbound now goes through gistHTTPClient:
//
//   - Hard-deny layer always on (netguard PolicyHardDenyOnly, the
//     notify-face policy: a self-hosted API base on a private address
//     stays legal, metadata / link-local segment / unspecified never
//     are), judged on the initial URL and again at every redirect hop
//     (max 5), and re-judged at dial time on the pinned IPs so DNS
//     rebinding cannot swap the target between check and dial.
//   - The Authorization Bearer token is attached only when the
//     request host is the configured API base's host or the official
//     API host (api.github.com). The raw_url download leg uses
//     gistRawRequest, which never carries the token, whatever host
//     the response named.

var gistLookupIP = net.LookupIP

// validateGistURL judges one Gist outbound URL (initial or redirect
// hop): http/https only, host present, every resolved IP clear of
// the hard-deny layer. Hostname() is the real host, so userinfo
// masking (https://example.com@169.254.169.254/) is judged on the
// address actually dialed.
func validateGistURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return errors.New("Gist 地址无效，已拒绝")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("Gist 地址 scheme 不支持，已拒绝: %s", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return errors.New("Gist 地址主机为空，已拒绝")
	}
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		ips, err = gistLookupIP(host)
		if err != nil || len(ips) == 0 {
			return fmt.Errorf("Gist 地址无法解析，已拒绝: %s", host)
		}
	}
	for _, ip := range ips {
		if d := netguard.Judge(ip, netguard.PolicyHardDenyOnly, false); !d.Allowed {
			return errors.New("Gist 地址指向云元数据服务，已拒绝")
		}
	}
	return nil
}

// gistDialContext is the pinned dial for Gist outbound: resolve,
// re-judge every IP against the hard-deny layer, dial a validated IP
// (same shape as notifyDialContext, R2X-044).
func gistDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		ips, err = gistLookupIP(host)
		if err != nil || len(ips) == 0 {
			return nil, fmt.Errorf("Gist 地址无法解析: %s", host)
		}
	}
	for _, ip := range ips {
		if d := netguard.Judge(ip, netguard.PolicyHardDenyOnly, false); !d.Allowed {
			return nil, errors.New("Gist 地址指向云元数据服务，已拒绝")
		}
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

// gistTokenAllowedForURL reports whether the Gist token may be sent
// to this URL: only the configured API base's own host (same-origin,
// which covers self-hosted bases) or the official API host.
func gistTokenAllowedForURL(u *url.URL) bool {
	if u == nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return false
	}
	if base, err := url.Parse(gistAPIBase()); err == nil && strings.EqualFold(host, base.Hostname()) {
		return true
	}
	return host == "api.github.com"
}

func gistHTTPClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	// Pinned dialing only works direct: via a proxy the proxy would
	// resolve the target and the pin would be meaningless (same
	// rationale as notifyTransport / federation transport).
	tr.Proxy = nil
	tr.DialContext = gistDialContext
	return &http.Client{
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("Gist 地址重定向次数过多，已拒绝")
			}
			if err := validateGistURL(req.URL.String()); err != nil {
				return err
			}
			if !gistTokenAllowedForURL(req.URL) {
				req.Header.Del("Authorization")
			}
			return nil
		},
	}
}

func gistRequest(ctx context.Context, method, url, token string, body interface{}, out interface{}) error {
	return gistDo(ctx, method, url, token, true, body, out)
}

// gistRawRequest is the raw_url download leg: same controlled client
// and hard-deny policy, but the token is never attached — the URL
// comes from an API response, not from the panel's own config.
func gistRawRequest(ctx context.Context, method, rawURL string, body interface{}, out interface{}) error {
	return gistDo(ctx, method, rawURL, "", false, body, out)
}

func gistDo(ctx context.Context, method, rawURL, token string, attachToken bool, body interface{}, out interface{}) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	// R7-C-01: the initial URL is judged before any request is built;
	// redirect hops are re-judged by the client's CheckRedirect and
	// the dial is pinned (see gistHTTPClient).
	if err := validateGistURL(rawURL); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return err
	}
	// 注意：此处 Bearer 是 GitHub API 的要求，与面板自身的 raw JWT
	//（无 Bearer 前缀）无关，不得混用。R7-C-01：令牌只发往 API
	// 基址同源或官方 API 主机（gistTokenAllowedForURL）；raw_url
	// 下载腿走 gistRawRequest，永不带令牌。
	if attachToken && token != "" {
		if parsed, perr := url.Parse(rawURL); perr == nil && gistTokenAllowedForURL(parsed) {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "flmx-panel")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := gistHTTPClient().Do(req)
	if err != nil {
		return fmt.Errorf("连接 GitHub 失败：%v", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, gistMaxBackupBytes*2))
	if err != nil {
		return err
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return errText("GitHub 令牌无效或已过期")
	case resp.StatusCode == http.StatusForbidden:
		return errText("GitHub 拒绝了请求：令牌需要 gist 权限，或者请求太频繁")
	case resp.StatusCode == http.StatusNotFound:
		return errGistNotFound
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &e)
		return fmt.Errorf("GitHub 返回 %d：%s", resp.StatusCode, truncate(e.Message, 200))
	}
	if out == nil {
		return nil
	}
	if raw, ok := out.(*[]byte); ok {
		*raw = data
		return nil
	}
	return json.Unmarshal(data, out)
}

// --- upload ---

type gistUploadResult struct {
	Uploaded bool   `json:"uploaded"`
	Message  string `json:"message"`
}

// gistBackupContent 返回待上传文件内容与内容哈希：哈希以去掉导出
// 时间的备份 + 密码计算，内容不变即不产生新版本。
func (h *Handler) gistBackupContent(ctx context.Context, password string) ([]byte, string, error) {
	backup, err := h.repo.ExportAllContext(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("导出备份失败：%v", err)
	}
	exportedAt := backup.ExportedAt
	backup.ExportedAt = 0
	stable, err := json.Marshal(backup)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(append(append(stable, 0), []byte(password)...))
	backup.ExportedAt = exportedAt
	plain, err := json.MarshalIndent(backup, "", "  ")
	if err != nil {
		return nil, "", err
	}
	if len(plain) > gistMaxBackupBytes {
		return nil, "", errText("备份超过 20MB，无法上传到 Gist")
	}
	content := plain
	if password != "" {
		if content, err = gistEncrypt(plain, password); err != nil {
			return nil, "", err
		}
	}
	return content, hex.EncodeToString(sum[:]), nil
}

// gistUpload 上传备份：首次新建 gist，之后复用同一 gist 产生新版本
// （gist 在 GitHub 侧被删则重建）；内容不变且非强制时跳过。
// 无论成败都记录最近一次尝试时间与错误（失败时 lastError 非空），
// 成功时刷新 lastUpload/lastHash/gist_id。
func (h *Handler) gistUpload(ctx context.Context, force bool) (gistUploadResult, error) {
	gistMu.Lock()
	defer gistMu.Unlock()
	cfg := h.gistConfig()
	token := strings.TrimSpace(cfg[gistConfigToken])
	if token == "" {
		return gistUploadResult{}, errText("请先填写 GitHub 令牌")
	}
	now := time.Now().UnixMilli()
	result, err := h.gistUploadLocked(ctx, cfg, token, force, now)
	fields := map[string]string{gistConfigLastAt: strconv.FormatInt(now, 10), gistConfigLastError: ""}
	if err != nil {
		fields[gistConfigLastError] = truncate(err.Error(), 255)
	}
	_ = h.repo.UpsertConfigs(fields, now)
	if err == nil {
		// R2-I-02：上传成功即 supersede 此前的失败——与 lastError
		// 被清空同一时刻清掉待发失败通知，下一轮不再补发已过时的失败。
		h.clearGistFailurePending()
	}
	return result, err
}

func (h *Handler) gistUploadLocked(ctx context.Context, cfg map[string]string, token string, force bool, now int64) (gistUploadResult, error) {
	password := cfg[gistConfigPassword]
	// T-4（用户钉死，比上游放行+警告收紧）：未设加密密码时上传
	// 必须拒绝。门禁放在内容导出与跳过判断之前：即使内容未变也
	// 不得放行，且不得产生任何 GitHub 请求。自动循环同样经此门
	// 禁，失败会经 gistUpload 写 lastAt/lastError，status 可见。
	if password == "" {
		return gistUploadResult{}, errText("未设置加密密码，禁止明文上传到 Gist：请先在 Gist 设置中填写加密密码后再上传")
	}
	content, hash, err := h.gistBackupContent(ctx, password)
	if err != nil {
		return gistUploadResult{}, err
	}
	id := strings.TrimSpace(cfg[gistConfigID])
	if !force && id != "" && hash == cfg[gistConfigLastHash] {
		return gistUploadResult{Message: "内容没有变化，没有产生新版本"}, nil
	}
	files := map[string]interface{}{gistFileName: map[string]string{"content": string(content)}}
	var doc gistDoc
	if id != "" {
		err = gistRequest(ctx, http.MethodPatch, gistAPIBase()+"/gists/"+id, token, map[string]interface{}{"files": files}, &doc)
		if errors.Is(err, errGistNotFound) {
			id = "" // 在 GitHub 侧被删：重建一个
		} else if err != nil {
			return gistUploadResult{}, err
		}
	}
	if id == "" {
		body := map[string]interface{}{"description": "FLMX 面板备份", "public": false, "files": files}
		if err := gistRequest(ctx, http.MethodPost, gistAPIBase()+"/gists", token, body, &doc); err != nil {
			if errors.Is(err, errGistNotFound) {
				return gistUploadResult{}, errText("GitHub 找不到接口，请检查令牌权限")
			}
			return gistUploadResult{}, err
		}
		if doc.ID == "" {
			return gistUploadResult{}, errText("GitHub 没有返回 Gist 编号")
		}
	}
	if err := h.repo.UpsertConfigs(map[string]string{
		gistConfigID: doc.ID, gistConfigLastHash: hash, gistConfigLastUpload: strconv.FormatInt(now, 10),
	}, now); err != nil {
		return gistUploadResult{}, err
	}
	return gistUploadResult{Uploaded: true, Message: "已上传"}, nil
}

// gistDue 报告自动上传是否到期：未配置间隔（0=关闭）或未填令牌时
// 恒 false；lastAt 按每次尝试刷新，失败不会导致 5 分钟循环热重试。
func gistDue(cfg map[string]string, now time.Time) bool {
	hours := gistInt64(cfg, gistConfigInterval)
	if hours <= 0 || strings.TrimSpace(cfg[gistConfigToken]) == "" {
		return false
	}
	return now.UnixMilli()-gistInt64(cfg, gistConfigLastAt) >= hours*int64(time.Hour/time.Millisecond)
}

func (h *Handler) runGistBackupLoop(ctx context.Context) {
	defer h.jobsWG.Done()
	ticker := time.NewTicker(gistCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if !gistDue(h.gistConfig(), time.Now()) {
			continue
		}
		if _, err := h.gistUpload(ctx, false); err != nil {
			// 令牌与错误详情不进日志：lastError 只落库、经 status 端点
			// 给管理员看；通知文案同样不含令牌（err 来自 GitHub 侧
			// 通用信息，见 gistRequest 的状态码翻译）。
			log.Printf("gist backup failed: %v", err)
			// R2-I-02：失败通知不再走裸 h.notify（全后端最后一处），
			// 改走 notifyTracked + 待发标记，发送失败由后续检查轮次补发。
			h.notifyGistBackupFailed(err)
		}
	}
}

// pendingGistFailure is one undelivered Gist backup-failure notice,
// persisted in vite_config under notifyGistFailurePendingConfigKey.
// Single record (one gist, one current failure); a newer failure
// overwrites the message, a later successful upload clears it.
//
// Gen is the generation of the mark (R6-I-01): every mark advances it
// monotonically, clears never move it. A Failed callback carries the
// generation its dispatch was sent under, so a stale callback can be
// told apart from the current mark — the whole class of "old callback
// overwrites newer pending value" is closed at this helper, not per
// call site. Records written before generations existed unmarshal with
// Gen 0 and are advanced past by the next mark.
type pendingGistFailure struct {
	Message string `json:"message"`
	At      int64  `json:"at"`
	Gen     int64  `json:"gen"`
}

// notifyGistFailurePendingGenConfigKey holds the last issued generation
// for the pending Gist failure mark (R6-I-01). Runtime ledger, excluded
// from backups like the pending record itself: it is advanced only by
// markGistFailurePending, never by clear, so that after a newer
// generation's dispatch succeeds and clears the record, an older
// generation's late failure cannot resurrect the stale message.
const notifyGistFailurePendingGenConfigKey = "notify_gist_failure_pending_gen"

func (h *Handler) loadPendingGistFailure() *pendingGistFailure {
	if h == nil || h.repo == nil {
		return nil
	}
	h.gistFailureMu.Lock()
	defer h.gistFailureMu.Unlock()
	cfg, err := h.repo.GetConfigByName(notifyGistFailurePendingConfigKey)
	if err != nil || cfg == nil || strings.TrimSpace(cfg.Value) == "" {
		return nil
	}
	var p pendingGistFailure
	if err := json.Unmarshal([]byte(cfg.Value), &p); err != nil {
		// 损坏的待发记录无法解析：记日志后丢弃，避免每轮重试都
		// 卡在同一行上（与 I-01 的待发列表同口径）。
		log.Printf("gist failure pending record corrupt, dropping: %v", err)
		_ = h.repo.UpsertConfig(notifyGistFailurePendingConfigKey, "", time.Now().UnixMilli())
		return nil
	}
	return &p
}

func (h *Handler) gistFailureGenLocked() int64 {
	cfg, err := h.repo.GetConfigByName(notifyGistFailurePendingGenConfigKey)
	if err != nil || cfg == nil {
		return 0
	}
	gen, _ := strconv.ParseInt(strings.TrimSpace(cfg.Value), 10, 64)
	return gen
}

// markGistFailurePending arms (or re-arms) the pending mark with a fresh
// generation and returns it. The caller passes the generation to the
// dispatch callback so remarkGistFailurePendingIfCurrent can reject
// stale callbacks. The watermark is written before the record: a
// failed record write after a watermark advance is harmless (the next
// mark advances past it), while a record written ahead of the
// watermark could let a stale callback resurrect after a success-clear.
func (h *Handler) markGistFailurePending(message string) int64 {
	if h == nil || h.repo == nil || strings.TrimSpace(message) == "" {
		return 0
	}
	h.gistFailureMu.Lock()
	defer h.gistFailureMu.Unlock()
	gen := h.gistFailureGenLocked() + 1
	if cfg, err := h.repo.GetConfigByName(notifyGistFailurePendingConfigKey); err == nil && cfg != nil && strings.TrimSpace(cfg.Value) != "" {
		var cur pendingGistFailure
		if err := json.Unmarshal([]byte(cfg.Value), &cur); err == nil && cur.Gen >= gen {
			gen = cur.Gen + 1
		}
	}
	if err := h.repo.UpsertConfig(notifyGistFailurePendingGenConfigKey, strconv.FormatInt(gen, 10), time.Now().UnixMilli()); err != nil {
		log.Printf("gist failure pending generation save failed: %v", err)
	}
	raw, err := json.Marshal(pendingGistFailure{Message: message, At: time.Now().UnixMilli(), Gen: gen})
	if err != nil {
		log.Printf("gist failure pending marshal failed: %v", err)
		return gen
	}
	if err := h.repo.UpsertConfig(notifyGistFailurePendingConfigKey, string(raw), time.Now().UnixMilli()); err != nil {
		log.Printf("gist failure pending save failed: %v", err)
	}
	return gen
}

// remarkGistFailurePendingIfCurrent re-arms the pending mark for a
// Failed callback, but only if the callback's generation is still
// current (R6-I-01). Two cases re-arm: the record still carries gen
// (the failure is still the newest), or the record is gone and no newer
// mark has been issued since gen (a same-generation outcome cleared
// it — e.g. a concurrent Suppressed outcome — and the failure must
// survive for the next retry round). If a newer mark exists, or a
// newer generation was issued and has since cleared, the callback is
// stale and must not overwrite the newer message or resurrect a
// cleared one. Clears deliberately do not touch the watermark, so the
// second case stays distinguishable from a newer generation's
// success-clear.
func (h *Handler) remarkGistFailurePendingIfCurrent(gen int64, message string) {
	if h == nil || h.repo == nil || gen <= 0 || strings.TrimSpace(message) == "" {
		return
	}
	h.gistFailureMu.Lock()
	defer h.gistFailureMu.Unlock()
	if cfg, err := h.repo.GetConfigByName(notifyGistFailurePendingConfigKey); err == nil && cfg != nil && strings.TrimSpace(cfg.Value) != "" {
		var cur pendingGistFailure
		if err := json.Unmarshal([]byte(cfg.Value), &cur); err == nil {
			if cur.Gen != gen {
				return
			}
			raw, err := json.Marshal(pendingGistFailure{Message: message, At: time.Now().UnixMilli(), Gen: gen})
			if err != nil {
				log.Printf("gist failure pending marshal failed: %v", err)
				return
			}
			if err := h.repo.UpsertConfig(notifyGistFailurePendingConfigKey, string(raw), time.Now().UnixMilli()); err != nil {
				log.Printf("gist failure pending save failed: %v", err)
			}
			return
		}
		// Corrupt record: loadPendingGistFailure drops it, so treat
		// the record as absent and fall through to the watermark.
	}
	if h.gistFailureGenLocked() != gen {
		return
	}
	raw, err := json.Marshal(pendingGistFailure{Message: message, At: time.Now().UnixMilli(), Gen: gen})
	if err != nil {
		log.Printf("gist failure pending marshal failed: %v", err)
		return
	}
	if err := h.repo.UpsertConfig(notifyGistFailurePendingConfigKey, string(raw), time.Now().UnixMilli()); err != nil {
		log.Printf("gist failure pending save failed: %v", err)
	}
}

func (h *Handler) clearGistFailurePending() {
	if h == nil || h.repo == nil {
		return
	}
	h.gistFailureMu.Lock()
	defer h.gistFailureMu.Unlock()
	if err := h.repo.UpsertConfig(notifyGistFailurePendingConfigKey, "", time.Now().UnixMilli()); err != nil {
		log.Printf("gist failure pending clear failed: %v", err)
	}
}

// notifyGistBackupFailed sends the backup-failure notice with I-01 rearm
// semantics (R2-I-02): mark pending before dispatch; the notifyTracked
// callback re-marks on failure and clears on sent / suppressed /
// disabled. The mark is armed only while the failure is still current
// (gist_last_error non-empty): a later successful upload clears
// lastError, which supersedes the failure — a stale notice must neither
// be armed nor sent.
//
// R6-I-01: the failure re-mark is generation-guarded. The lastError
// check alone is identity-blind (a newer failure also leaves lastError
// non-empty), so a late old callback used to overwrite the newer
// pending message; remarkGistFailurePendingIfCurrent rejects it.
func (h *Handler) notifyGistBackupFailed(err error) {
	if h == nil || h.repo == nil || err == nil {
		return
	}
	msg := err.Error()
	if strings.TrimSpace(h.gistConfig()[gistConfigLastError]) == "" {
		return
	}
	gen := h.markGistFailurePending(msg)
	h.notifyTracked(notifyEventBackup, "Gist 备份失败", msg,
		func(outcome notifyOutcome) {
			if outcome == notifyOutcomeFailed {
				// 失败重记，但仅当失败仍为当前状态；其间若上传已成功
				// （lastError 已清空），不复活已过时的失败通知。
				// 世代守卫见 remarkGistFailurePendingIfCurrent。
				if strings.TrimSpace(h.gistConfig()[gistConfigLastError]) != "" {
					h.remarkGistFailurePendingIfCurrent(gen, msg)
				}
				return
			}
			h.clearGistFailurePending()
		})
}

// retryPendingGistFailureNotice resends a Gist backup-failure notice
// whose send failed (R2-I-02). Called from the 10-minute
// checkUserReminders round, the same loop that retries failed quota
// recovery notices (I-01). The mark is dropped when the failure no
// longer stands (a later upload succeeded and cleared gist_last_error):
// re-sending a stale failure after the backup healed would be wrong.
func (h *Handler) retryPendingGistFailureNotice() {
	if h == nil || h.repo == nil || h.notifier == nil {
		return
	}
	pending := h.loadPendingGistFailure()
	if pending == nil {
		return
	}
	if strings.TrimSpace(h.gistConfig()[gistConfigLastError]) == "" {
		h.clearGistFailurePending()
		return
	}
	h.notifyGistBackupFailed(errors.New(pending.Message))
}

// --- API ---

type gistStatusView struct {
	Configured    bool   `json:"configured"`
	TokenHint     string `json:"tokenHint"`
	Encrypted     bool   `json:"encrypted"`
	IntervalHours int64  `json:"intervalHours"`
	GistURL       string `json:"gistUrl"`
	LastAt        int64  `json:"lastAt"`
	LastUpload    int64  `json:"lastUpload"`
	LastError     string `json:"lastError"`
}

// tokenHint 只露令牌首尾各 4 位，其余以 • 代替；短令牌全掩。
func tokenHint(t string) string {
	if len(t) <= 10 {
		return strings.Repeat("•", len(t))
	}
	return t[:4] + "••••••••" + t[len(t)-4:]
}

func gistStatus(cfg map[string]string) gistStatusView {
	token := strings.TrimSpace(cfg[gistConfigToken])
	v := gistStatusView{
		Configured: token != "", TokenHint: tokenHint(token), Encrypted: cfg[gistConfigPassword] != "",
		IntervalHours: gistInt64(cfg, gistConfigInterval), LastAt: gistInt64(cfg, gistConfigLastAt),
		LastUpload: gistInt64(cfg, gistConfigLastUpload), LastError: cfg[gistConfigLastError],
	}
	if id := strings.TrimSpace(cfg[gistConfigID]); id != "" {
		v.GistURL = "https://gist.github.com/" + id
	}
	return v
}

func (h *Handler) gistBackupStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	if !isAdminRequest(r) {
		response.WriteJSON(w, response.Err(403, "禁止访问敏感配置"))
		return
	}
	response.WriteJSON(w, response.OK(gistStatus(h.gistConfig())))
}

// gistBackupSave 保存 Gist 设置：空令牌/空密码 = 保留已存值，
// clearToken/clearPassword = 清除（清除令牌即关闭自动上传）。
// 新令牌先向 GitHub 校验可用才落库；间隔只接受 gistIntervals 白名单。
func (h *Handler) gistBackupSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	if !isAdminRequest(r) {
		response.WriteJSON(w, response.Err(403, "禁止访问敏感配置"))
		return
	}
	var req struct {
		Token         string `json:"token"`
		ClearToken    bool   `json:"clearToken"`
		Password      string `json:"password"`
		ClearPassword bool   `json:"clearPassword"`
		IntervalHours int    `json:"intervalHours"`
	}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	req.Token = strings.TrimSpace(req.Token)
	if !gistIntervals[req.IntervalHours] {
		response.WriteJSON(w, response.ErrDefault("自动上传间隔不正确"))
		return
	}
	if len(req.Token) > 255 || strings.ContainsAny(req.Token, " \r\n\t") || len(req.Password) > 255 {
		response.WriteJSON(w, response.ErrDefault("令牌或密码格式不正确"))
		return
	}
	if req.Token != "" {
		// 先校验令牌再落库：令牌本身不进错误信息与日志。
		if err := gistRequest(r.Context(), http.MethodGet, gistAPIBase()+"/gists?per_page=1", req.Token, nil, nil); err != nil {
			response.WriteJSON(w, response.ErrDefault("令牌不可用："+err.Error()))
			return
		}
	}
	// R14-F02（REA-P3-1 / A3-02）：保存后终态若 interval>0 且无密码在
	// 位（含本次 clearPassword）且令牌在位，拒绝保存——否则自动循环
	// 在无密码态下可达（每配置间隔一次失败尝试 + 去重后的失败通知，
	// lastAt/lastError 持续改写）。令牌未设时 gistDue 恒 false、循环
	// 不可达，仍按既有口径放行；清密码同时 interval 归 0 亦放行。
	cfg := h.gistConfig()
	effectiveToken := cfg[gistConfigToken]
	switch {
	case req.ClearToken:
		effectiveToken = ""
	case req.Token != "":
		effectiveToken = req.Token
	}
	effectivePassword := cfg[gistConfigPassword]
	switch {
	case req.ClearPassword:
		effectivePassword = ""
	case req.Password != "":
		effectivePassword = req.Password
	}
	if req.IntervalHours > 0 && effectivePassword == "" && strings.TrimSpace(effectiveToken) != "" {
		response.WriteJSON(w, response.ErrDefault("请先填写加密密码，或将自动备份间隔设为不启用"))
		return
	}
	fields := map[string]string{gistConfigInterval: strconv.Itoa(req.IntervalHours)}
	switch {
	case req.ClearToken:
		fields[gistConfigToken] = ""
	case req.Token != "":
		fields[gistConfigToken] = req.Token
	}
	switch {
	case req.ClearPassword:
		fields[gistConfigPassword] = ""
	case req.Password != "":
		fields[gistConfigPassword] = req.Password
	}
	now := time.Now().UnixMilli()
	if err := h.repo.UpsertConfigs(fields, now); err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	response.WriteJSON(w, response.OK(gistStatus(h.gistConfig())))
}

func (h *Handler) gistBackupUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	if !isAdminRequest(r) {
		response.WriteJSON(w, response.Err(403, "禁止访问敏感配置"))
		return
	}
	result, err := h.gistUpload(r.Context(), false)
	if err != nil {
		response.WriteJSON(w, response.ErrDefault("上传失败："+err.Error()))
		return
	}
	response.WriteJSON(w, response.OK(map[string]interface{}{"result": result, "status": gistStatus(h.gistConfig())}))
}

// --- versions（G-backend 新增；restore 本批不接）---

type gistVersionView struct {
	Version     string `json:"version"`
	CommittedAt int64  `json:"committedAt"`
}

// gistBackupVersions 列出 Gist history 最近 10 个版本（version +
// committedAt ms）。只列版本、不下载内容，故不需要加密密码；
// 令牌与 gist_id 缺失时明确报错。restore 端点归 G-恢复链批，
// 本端点不触发任何导入。
func (h *Handler) gistBackupVersions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	if !isAdminRequest(r) {
		response.WriteJSON(w, response.Err(403, "禁止访问敏感配置"))
		return
	}
	cfg := h.gistConfig()
	token := strings.TrimSpace(cfg[gistConfigToken])
	if token == "" {
		response.WriteJSON(w, response.ErrDefault("请先填写 GitHub 令牌"))
		return
	}
	id := strings.TrimSpace(cfg[gistConfigID])
	if id == "" {
		response.WriteJSON(w, response.ErrDefault("尚未上传到 Gist，没有可列出的版本"))
		return
	}
	var doc gistDoc
	if err := gistRequest(r.Context(), http.MethodGet, gistAPIBase()+"/gists/"+id, token, nil, &doc); err != nil {
		if errors.Is(err, errGistNotFound) {
			response.WriteJSON(w, response.ErrDefault("Gist 不存在，可能已在 GitHub 上被删除"))
			return
		}
		response.WriteJSON(w, response.ErrDefault("获取版本列表失败："+err.Error()))
		return
	}
	versions := make([]gistVersionView, 0, 10)
	for _, entry := range doc.History {
		if len(versions) >= 10 {
			break
		}
		var committedAt int64
		if t, err := time.Parse(time.RFC3339, entry.CommittedAt); err == nil {
			committedAt = t.UnixMilli()
		}
		versions = append(versions, gistVersionView{Version: entry.Version, CommittedAt: committedAt})
	}
	response.WriteJSON(w, response.OK(map[string]interface{}{"versions": versions}))
}

// gistVersionContent 取指定版本的 Gist 备份文件内容并解密，供未来
// G-恢复链批复用；本批不接恢复导入链（不调用 Import、不触
// backup_import_repush.go、不触发任何尾钩子）。
// version 校验与上游 restore 一致：限 64 字、禁 /?#；文件缺失、
// 大文件 raw_url 再取、加密信封解密均在此处理，返回
// (plain, encrypted, err)。
func gistVersionContent(ctx context.Context, token, id, version, password string) ([]byte, bool, error) {
	if strings.TrimSpace(token) == "" {
		return nil, false, errText("请先填写 GitHub 令牌")
	}
	if strings.TrimSpace(id) == "" {
		return nil, false, errText("尚未上传到 Gist，没有可恢复的版本")
	}
	if version == "" || len(version) > 64 || strings.ContainsAny(version, "/?#") {
		return nil, false, errText("版本不正确")
	}
	var doc gistDoc
	if err := gistRequest(ctx, http.MethodGet, gistAPIBase()+"/gists/"+id+"/"+version, token, nil, &doc); err != nil {
		if errors.Is(err, errGistNotFound) {
			return nil, false, errText("Gist 版本不存在，可能已在 GitHub 上被删除")
		}
		return nil, false, err
	}
	file, ok := doc.Files[gistFileName]
	if !ok {
		return nil, false, errText("Gist 里没有备份文件 " + gistFileName)
	}
	content := file.Content
	if file.Truncated {
		if file.RawURL == "" {
			return nil, false, errText("备份文件过大，无法读取完整内容")
		}
		var raw []byte
		// R7-C-01: raw_url comes from the API response — download it
		// via gistRawRequest so the token never leaves for it.
		if err := gistRawRequest(ctx, http.MethodGet, file.RawURL, nil, &raw); err != nil {
			return nil, false, err
		}
		content = string(raw)
	}
	return gistOpen([]byte(content), password)
}

// --- restore（PORT-3342/G-recovery）---

// gistRestoreTypes 是 Gist 恢复的段全集（T-3 钉死）：我方 16 段，
// 与 repository.go validBackupTypes / 前端 BACKUP_TYPE_VALUES 同集。
// 不得照抄上游 11 项类型表（丢 userInbounds、announcements、
// monitorPermissions、userSubscriptions、serviceMonitors）。
var gistRestoreTypes = []string{
	"users", "nodes", "tunnels", "forwards", "userTunnels", "speedLimits",
	"tunnelGroups", "userGroups", "permissions", "configs", "inbounds",
	"userInbounds", "announcements", "monitorPermissions",
	"userSubscriptions", "serviceMonitors",
}

// gistBackupRestore 按指定版本从 Gist 恢复：取版本内容（复用
// gistVersionContent：版本校验→取版本文件→truncated 经 raw_url 再取
// →gistOpen 解密，双认 flyx/flmx 信封）→JSON 解码为 BackupData 且
// Version 非空才继续→Import（16 段全集，其 1.0 版本门与 configs 段
// FilterBackupConfigs 过滤仍在：Gist 设置与凭据既不出备份、即使夹带
// 也被滤掉，恢复不覆盖当前 Gist 设置）→与现有 backupImport 完全相同
// 的导入尾钩子全集 runBackupImportTailHooks（计量对账、configs 缓存
// 失效、内核 reconcile、撤销清理、转发重推含 FINAL-P2-1 七段谓词）。
// SEC-01：恢复前不再做全量自动备份导出、回执不再含 autoBackup 快照
// （旧逻辑把整库快照塞进回执泄露凭据，且 Import 失败时从不用它回滚）。
// 严禁只调内核 reconcile、严禁直调 Import 后收尾了事。
// 仅管理员；请求体 {version, password}，password 为上传时用的加密
// 密码（明文 Gist 内容可空密码直通，由 gistOpen 判定）。
func (h *Handler) gistBackupRestore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	if !isAdminRequest(r) {
		response.WriteJSON(w, response.Err(403, "禁止访问敏感配置"))
		return
	}
	var req struct {
		Version  string `json:"version"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	cfg := h.gistConfig()
	plain, _, err := gistVersionContent(r.Context(), cfg[gistConfigToken], cfg[gistConfigID], req.Version, req.Password)
	if err != nil {
		response.WriteJSON(w, response.ErrDefault("从 Gist 恢复失败："+err.Error()))
		return
	}
	var backup model.BackupData
	if err := json.Unmarshal(plain, &backup); err != nil {
		response.WriteJSON(w, response.Err(-2, "备份内容不是合法的备份 JSON"))
		return
	}
	if backup.Version == "" {
		response.WriteJSON(w, response.Err(-2, "备份版本为空，拒绝恢复"))
		return
	}
	result, err := h.repo.ImportContext(r.Context(), &backup, gistRestoreTypes)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, fmt.Sprintf("导入失败: %v", err)))
		return
	}
	h.runBackupImportTailHooks(result, &backup, gistRestoreTypes)
	response.WriteJSON(w, response.OK(result))
}
