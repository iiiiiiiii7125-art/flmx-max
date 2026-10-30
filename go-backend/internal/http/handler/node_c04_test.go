package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// C04（B04-002/B04-004/B04-005）handler 面回归。

func setupC04Handler(t *testing.T) *Handler {
	t.Helper()
	r, err := repo.Open(filepath.Join(t.TempDir(), "c04_handler.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	return &Handler{repo: r}
}

type c04Envelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

func decodeC04Envelope(t *testing.T, rec *httptest.ResponseRecorder) c04Envelope {
	t.Helper()
	var env c04Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v (body=%s)", err, rec.Body.String())
	}
	return env
}

// TestC04_IsValidNodeAddressRejectsBadNumerics：B04-005——全数字点分
// 形态 IP 解析失败必拒；host:port 的 port 必须为 1-65535 的数字。
func TestC04_IsValidNodeAddressRejectsBadNumerics(t *testing.T) {
	for _, addr := range []string{
		"999.999.1.1",             // 原始复现：非法 IPv4 落库
		"999.999.1.1:6365",        // 同上带端口
		"1.2.3.999",               // 末段越界
		"1.2.3.4.5",               // 五段全数字点分
		"panel.example.com:99999", // 原始复现：越界端口烤进安装命令
		"panel.example.com:0",     // 端口 0
		"panel.example.com:abc",   // 非数字端口
		"panel.example.com:",      // 空端口
		"[::1]:99999",             // 括号 IPv6 + 越界端口
		"10.0.0.1:65536",          // 私网地址 + 越界端口同样拒
	} {
		t.Run(addr, func(t *testing.T) {
			if err := IsValidNodeAddress(addr); err == nil {
				t.Fatalf("expected %q to be rejected (B04-005 regression)", addr)
			}
		})
	}
}

// TestC04_IsValidNodeAddressKeepsValidNumerics：数值校验不得误伤
// 合法形态（含数字标签域名、边界端口）。
func TestC04_IsValidNodeAddressKeepsValidNumerics(t *testing.T) {
	for _, addr := range []string{
		"1.2.3.4",
		"1.2.3.4:1",
		"1.2.3.4:65535",
		"123.example.com", // 含字母标签，不是全数字点分
		"node1.example.com:443",
		"10.0.0.1:8080",
		"[2001:db8::1]:6365",
	} {
		t.Run(addr, func(t *testing.T) {
			if err := IsValidNodeAddress(addr); err != nil {
				t.Fatalf("expected %q to be accepted: %v", addr, err)
			}
		})
	}
}

// TestC04_NftablesTestMissingNodeHumanError：B04-004——test/clear 对
// 不存在节点回人话「节点不存在」，绝不泄漏 SQL 原文。
func TestC04_NftablesTestMissingNodeHumanError(t *testing.T) {
	h := setupC04Handler(t)
	for name, fn := range map[string]func(http.ResponseWriter, *http.Request){
		"test":  h.nodeNftablesTest,
		"clear": h.nodeNftablesClear,
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/node/nftables/"+name, bytes.NewBufferString(`{"nodeId":99999}`))
			rec := httptest.NewRecorder()
			fn(rec, req)
			env := decodeC04Envelope(t, rec)
			if env.Code == 0 {
				t.Fatalf("expected failure for missing node, got code=0 (body=%s)", rec.Body.String())
			}
			if !strings.Contains(env.Msg, "节点不存在") {
				t.Fatalf("msg = %q, want 节点不存在", env.Msg)
			}
			if strings.Contains(env.Msg, "sql:") || strings.Contains(env.Msg, "no rows") {
				t.Fatalf("msg leaks SQL text: %q (B04-004 regression)", env.Msg)
			}
		})
	}
}

// TestC04_DismissExpiryReminderMissingNode：B04-002 dismiss 子项——
// repo 对 0 行回「节点不存在」后，handler 既有 err 分支透出失败
// 回执；存在节点仍正常成功（节点单删幂等口径不在本条范围）。
func TestC04_DismissExpiryReminderMissingNode(t *testing.T) {
	h := setupC04Handler(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/node/dismiss-expiry-reminder", bytes.NewBufferString(`{"id":99999}`))
	rec := httptest.NewRecorder()
	h.nodeDismissExpiryReminder(rec, req)
	env := decodeC04Envelope(t, rec)
	if env.Code == 0 {
		t.Fatalf("dismiss missing node: expected failure, got code=0 (body=%s)", rec.Body.String())
	}
	if !strings.Contains(env.Msg, "节点不存在") {
		t.Fatalf("dismiss missing node msg = %q, want 节点不存在", env.Msg)
	}

	now := time.Now().UnixMilli()
	id, err := h.repo.CreateNode("c04-node", "secret", "198.51.100.10", nil, nil, "1000-65535", nil, nil, nil, nil, nil, 0, 0, 0, now, 1, "", "", 1, 0, nil, nil, nil, nil, "")
	if err != nil {
		t.Fatalf("create node: %v", err)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/node/dismiss-expiry-reminder", bytes.NewBufferString(`{"id":`+strconv.FormatInt(id, 10)+`}`))
	rec = httptest.NewRecorder()
	h.nodeDismissExpiryReminder(rec, req)
	env = decodeC04Envelope(t, rec)
	if env.Code != 0 {
		t.Fatalf("dismiss existing node: expected code=0, got %d msg=%q", env.Code, env.Msg)
	}
}
