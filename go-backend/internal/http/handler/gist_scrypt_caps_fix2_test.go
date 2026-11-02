package handler

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// R2-046（B-admin-03）：gistOpen 的信封 scrypt 参数接受面收紧到写
// 面剖面附近（N≤2^16、r≤8、p≤4；写侧固定 32768/8/1，含全历史与
// 互通格式）。敌意参数信封必须在任何 KDF 计算之前被拒（格式错
// 误），不得触发大额内存分配。

func buildEnvelopeWithParams(t *testing.T, password string, n, r, p int, plain []byte) []byte {
	t.Helper()
	salt := []byte("0123456789abcdef")
	nonce := []byte("0123456789ab")
	key, err := gistKey(password, salt, n, r, p)
	if err != nil {
		t.Fatalf("gistKey(%d,%d,%d): %v", n, r, p, err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("aes: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("gcm: %v", err)
	}
	env := gistEnvelope{
		Format: gistEnvelopeFormat, V: 1, KDF: "scrypt", N: n, R: r, P: p,
		Salt:  base64.StdEncoding.EncodeToString(salt),
		Nonce: base64.StdEncoding.EncodeToString(nonce),
		Data:  base64.StdEncoding.EncodeToString(gcm.Seal(nil, nonce, plain, nil)),
	}
	out, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return out
}

func TestGistOpenRejectsHostileScryptParams(t *testing.T) {
	// 旧上限组合（N=2^20、r=32、p=16）约需 128·N·r ≈ 4 GiB 内存；
	// 信封字段本身合法（base64 可解、nonce 12 字节），唯一拒绝理
	// 由是参数剖面——且必须在 KDF 之前拒绝。
	env := gistEnvelope{
		Format: gistEnvelopeFormat, V: 1, KDF: "scrypt", N: 1 << 20, R: 32, P: 16,
		Salt:  base64.StdEncoding.EncodeToString([]byte("0123456789abcdef")),
		Nonce: base64.StdEncoding.EncodeToString([]byte("0123456789ab")),
		Data:  base64.StdEncoding.EncodeToString([]byte("ciphertext")),
	}
	content, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	start := time.Now()
	_, encrypted, err := gistOpen(content, "pw")
	elapsed := time.Since(start)
	if !encrypted {
		t.Fatal("hostile envelope must be recognized as an encrypted envelope")
	}
	if err == nil || !strings.Contains(err.Error(), "不认识的加密格式") {
		t.Fatalf("hostile scrypt params must be rejected as unknown format before KDF, got err=%v", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("hostile envelope rejection took %v; KDF must not run on envelope-supplied params", elapsed)
	}
}

func TestGistOpenScryptParamCapsBoundaries(t *testing.T) {
	plain := []byte(`{"version":"1.0"}`)
	// 上限内（含写侧剖面的 2 倍 N 头寸与更高的 r/p）必须可正常打开。
	within := buildEnvelopeWithParams(t, "pw", 1<<16, 8, 4, plain)
	got, encrypted, err := gistOpen(within, "pw")
	if err != nil || !encrypted || string(got) != string(plain) {
		t.Fatalf("in-cap envelope (N=2^16,r=8,p=4) must open: got=%q encrypted=%v err=%v", got, encrypted, err)
	}
	// 任一维越上限即格式拒绝。
	for _, tc := range []struct {
		name    string
		n, r, p int
	}{
		{"N above cap", 1 << 17, 8, 1},
		{"R above cap", 1 << 15, 16, 1},
		{"P above cap", 1 << 15, 8, 8},
	} {
		env := gistEnvelope{
			Format: gistEnvelopeFormat, V: 1, KDF: "scrypt", N: tc.n, R: tc.r, P: tc.p,
			Salt:  base64.StdEncoding.EncodeToString([]byte("0123456789abcdef")),
			Nonce: base64.StdEncoding.EncodeToString([]byte("0123456789ab")),
			Data:  base64.StdEncoding.EncodeToString([]byte("ciphertext")),
		}
		content, _ := json.Marshal(env)
		if _, _, err := gistOpen(content, "pw"); err == nil || !strings.Contains(err.Error(), "不认识的加密格式") {
			t.Fatalf("%s: params (%d,%d,%d) must be rejected as unknown format, got err=%v", tc.name, tc.n, tc.r, tc.p, err)
		}
	}
}

// R2-047（B-admin-04）：Gist 保存的「255 字」上限按字（rune）计，
// 与 doc/guide/gist-backup.md 一致；ASCII 令牌字节数与字数相等、
// 行为不变。多字节密码 86 字（258 字节）修复前被拒 → 红。

func gistSaveBody(t *testing.T, h *Handler, payload string) (int, string) {
	t.Helper()
	req := adminRequest(t, "POST", "/api/v1/backup/gist/save", []byte(payload))
	rec := httptest.NewRecorder()
	h.gistBackupSave(rec, req)
	code, msg, _ := decodeGistEnvelope(t, rec)
	return code, msg
}

func TestGistSavePasswordLimitCountsRunes(t *testing.T) {
	h := setupGistTest(t)

	// 86 个多字节字 = 258 字节：按字计未越 255，须可保存。
	pw86 := strings.Repeat("密", 86)
	payload, _ := json.Marshal(map[string]interface{}{
		"token": "", "password": pw86, "intervalHours": 0,
	})
	code, msg := gistSaveBody(t, h, string(payload))
	if code != 0 {
		t.Fatalf("86-rune multibyte password must save, got code=%d msg=%q", code, msg)
	}
	if cfg := h.gistConfig(); cfg[gistConfigPassword] != pw86 {
		t.Fatalf("saved password mismatch: got %q", cfg[gistConfigPassword])
	}

	// 256 个字越限：拒绝且不落库（旧值保留）。
	pw256 := strings.Repeat("密", 256)
	payload, _ = json.Marshal(map[string]interface{}{
		"token": "", "password": pw256, "intervalHours": 0,
	})
	code, msg = gistSaveBody(t, h, string(payload))
	if code != -1 {
		t.Fatalf("256-rune password must be rejected, got code=%d msg=%q", code, msg)
	}
	if cfg := h.gistConfig(); cfg[gistConfigPassword] != pw86 {
		t.Fatalf("rejected save must not overwrite stored password, got %q", cfg[gistConfigPassword])
	}
}

func TestGistSaveTokenLimitCountsRunes(t *testing.T) {
	h := setupGistTest(t)
	// ASCII 令牌 256 字在长度门即拒（不进入令牌可用性校验）。
	token256 := strings.Repeat("a", 256)
	payload, _ := json.Marshal(map[string]interface{}{
		"token": token256, "password": "", "intervalHours": 0,
	})
	code, msg := gistSaveBody(t, h, string(payload))
	if code != -1 || !strings.Contains(msg, "格式不正确") {
		t.Fatalf("256-char token must be rejected at the length gate, got code=%d msg=%q", code, msg)
	}
}
