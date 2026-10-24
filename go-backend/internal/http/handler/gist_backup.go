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
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/scrypt"

	"go-backend/internal/http/response"
)

// PORT-3342/B1：备份上传 GitHub Gist（移植自上游 kure29/flyx 3.3.42
// go-backend/internal/http/handler/gist_backup.go，仅上传链）。
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
// 本轮上传侧不调用它（仅单测验证加密往返与未来恢复侧复用）。
func gistOpen(content []byte, password string) ([]byte, bool, error) {
	var env gistEnvelope
	if json.Unmarshal(content, &env) != nil || env.Format != gistEnvelopeFormat {
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
}

var errGistNotFound = errors.New("gist not found")

func gistRequest(ctx context.Context, method, url, token string, body interface{}, out interface{}) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return err
	}
	// 注意：此处 Bearer 是 GitHub API 的要求，与面板自身的 raw JWT
	//（无 Bearer 前缀）无关，不得混用。
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "flmx-panel")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
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
	return result, err
}

func (h *Handler) gistUploadLocked(ctx context.Context, cfg map[string]string, token string, force bool, now int64) (gistUploadResult, error) {
	password := cfg[gistConfigPassword]
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
			h.notify(notifyEventBackup, "Gist 备份失败", err.Error())
		}
	}
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
