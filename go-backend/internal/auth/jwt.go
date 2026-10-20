package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"time"
)

const (
	algorithm  = "HmacSHA256"
	expireTime = 7 * 24 * time.Hour
)

type Claims struct {
	Sub    string `json:"sub"`
	Iat    int64  `json:"iat"`
	IatMs  int64  `json:"iat_ms"`
	Exp    int64  `json:"exp"`
	User   string `json:"user"`
	Name   string `json:"name"`
	RoleID int    `json:"role_id"`
	// PwdChangeRequired: 持此 token 者必须先改密（默认口令登录的管理员）。
	// 中间件仅放行 /api/v1/user/updatePassword；改密后旧 token 因
	// PasswordChangedAt 失效，需重新登录获取正常 token。
	PwdChangeRequired bool `json:"pwd_change_required,omitempty"`
}

type tokenHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
}

func GenerateToken(userID int64, username string, roleID int, secret string) (string, error) {
	return GenerateTokenAt(userID, username, roleID, secret, time.Now())
}

func GenerateTokenAt(userID int64, username string, roleID int, secret string, now time.Time) (string, error) {
	return generateTokenAt(userID, username, roleID, secret, now, false)
}

// GenerateTokenAtWithPwdChangeRequired 签发"需改密"受限 token（M-05）。
func GenerateTokenAtWithPwdChangeRequired(userID int64, username string, roleID int, secret string, now time.Time) (string, error) {
	return generateTokenAt(userID, username, roleID, secret, now, true)
}

func generateTokenAt(userID int64, username string, roleID int, secret string, now time.Time, pwdChangeRequired bool) (string, error) {
	header := tokenHeader{Alg: algorithm, Typ: "JWT"}
	claims := Claims{
		Sub:               strconv.FormatInt(userID, 10),
		Iat:               now.Unix(),
		IatMs:             now.UnixMilli(),
		Exp:               now.Add(expireTime).Unix(),
		User:              username,
		Name:              username,
		RoleID:            roleID,
		PwdChangeRequired: pwdChangeRequired,
	}

	headerPart, err := encodeJSON(header)
	if err != nil {
		return "", err
	}
	payloadPart, err := encodeJSON(claims)
	if err != nil {
		return "", err
	}
	sig := sign(headerPart+"."+payloadPart, secret)

	return headerPart + "." + payloadPart + "." + sig, nil
}

func ValidateToken(token, secret string) (Claims, bool) {
	claims, err := ParseClaims(token, secret)
	if err != nil {
		return Claims{}, false
	}
	return claims, true
}

func ParseClaims(token, secret string) (Claims, error) {
	parts := splitToken(token)
	if len(parts) != 3 {
		return Claims{}, errors.New("invalid token")
	}

	// F-067：显式校验 header alg 与签发端一致。签名恒按 HMAC-SHA256 比对，
	// 算法头此前从不解码——当前不可利用，但若未来引入公钥验签或更换
	// JWT 库，忽略 alg 会打开算法混淆面，故在验签前先断言。
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Claims{}, err
	}
	var header tokenHeader
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return Claims{}, err
	}
	if header.Alg != algorithm {
		return Claims{}, errors.New("unexpected signing algorithm")
	}

	signedContent := parts[0] + "." + parts[1]
	expected := sign(signedContent, secret)
	if !hmac.Equal([]byte(expected), []byte(parts[2])) {
		return Claims{}, errors.New("invalid signature")
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Claims{}, err
	}

	var claims Claims
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return Claims{}, err
	}

	if claims.Exp <= time.Now().Unix() {
		return Claims{}, errors.New("token expired")
	}

	return claims, nil
}

func splitToken(token string) []string {
	parts := make([]string, 0, 3)
	current := ""
	for i := 0; i < len(token); i++ {
		if token[i] == '.' {
			parts = append(parts, current)
			current = ""
			continue
		}
		current += string(token[i])
	}
	parts = append(parts, current)
	return parts
}

func encodeJSON(v interface{}) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func sign(content, secret string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(content))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
