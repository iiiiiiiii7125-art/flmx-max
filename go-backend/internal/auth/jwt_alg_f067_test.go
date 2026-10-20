package auth

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// F-067 回归测试：ParseClaims 必须显式校验 header alg。
//
// 旧缺陷：ParseClaims 从不解码 header，alg=none / alg=RS256 的 token
// 只要签名是正确的 HMAC-SHA256 就会被接受（当前因签名恒按 HMAC 比对
// 而不可利用，属防御纵深缺口）。修复后非 HmacSHA256 一律拒绝。

func craftTokenWithHeader(t *testing.T, header map[string]string, secret string) string {
	t.Helper()
	headerPart, err := encodeJSON(header)
	if err != nil {
		t.Fatalf("encode header: %v", err)
	}
	claims := Claims{
		Sub:    "42",
		Iat:    time.Now().Unix(),
		IatMs:  time.Now().UnixMilli(),
		Exp:    time.Now().Add(time.Hour).Unix(),
		User:   "algtest",
		Name:   "algtest",
		RoleID: 1,
	}
	payloadPart, err := encodeJSON(claims)
	if err != nil {
		t.Fatalf("encode claims: %v", err)
	}
	return headerPart + "." + payloadPart + "." + sign(headerPart+"."+payloadPart, secret)
}

func TestParseClaimsRejectsNonHMACAlg(t *testing.T) {
	const secret = "alg-test-secret"
	for _, alg := range []string{"none", "RS256", "HS384", ""} {
		token := craftTokenWithHeader(t, map[string]string{"alg": alg, "typ": "JWT"}, secret)
		if _, err := ParseClaims(token, secret); err == nil {
			t.Fatalf("alg=%q token must be rejected (修复前会被接受)", alg)
		}
		if _, ok := ValidateToken(token, secret); ok {
			t.Fatalf("alg=%q token must not validate", alg)
		}
	}
}

func TestParseClaimsAcceptsIssuedToken(t *testing.T) {
	const secret = "alg-test-secret"
	token, err := GenerateToken(42, "algtest", 1, secret)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	claims, err := ParseClaims(token, secret)
	if err != nil {
		t.Fatalf("issued token must parse: %v", err)
	}
	if claims.Sub != "42" || claims.RoleID != 1 {
		t.Fatalf("unexpected claims: %+v", claims)
	}
	// 签名错误仍必须拒绝（防 alg 校验取代签名校验的误修）。
	parts := strings.Split(token, ".")
	raw, _ := json.Marshal(map[string]string{"alg": "HmacSHA256", "typ": "JWT"})
	_ = raw
	if _, err := ParseClaims(parts[0]+"."+parts[1]+".invalidsig", secret); err == nil {
		t.Fatalf("bad signature must be rejected")
	}
}
