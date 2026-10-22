package handler

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

// FIX2-C #5（V1-N1）：fetch 审计/成功日志点不得把第三方订阅源
// URL 的 path 型 token 原样落盘。logredact.URL 的段掩码单测在
// internal/logredact；这里钉住日志点本身确实走了脱敏。
func TestFix2CFetchAuditLogMasksPathToken(t *testing.T) {
	token := "9f8e7d6c5b4a39281706f5e4d3c2b1a0"
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)

	auditFetchLog("allow", "https://provider.example.com/link/"+token, "93.184.216.34", "ok")

	out := buf.String()
	if strings.Contains(out, token) {
		t.Fatalf("fetch_audit log leaked the path token: %s", out)
	}
	if !strings.Contains(out, "provider.example.com/link/***") {
		t.Fatalf("fetch_audit log should keep the masked URL shape, got: %s", out)
	}
}
