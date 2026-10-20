package kernel

import (
	"crypto/ecdh"
	"crypto/mlkem"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// VLESS 加密（VLESS Encryption，mlkem768x25519plus）：Xray 25.6.8+ 的协议层
// 加密。字符串文法（点分段）：
//
//	服务端 decryption: mlkem768x25519plus.<外观>.<ticket有效期>.<填充链>.<服务端认证段>
//	客户端 encryption: mlkem768x25519plus.<外观>.<0rtt|1rtt>.<填充链>.<客户端认证段>
//
// 认证二选一且两端必须一致：x25519（服务端存私钥、客户端用公钥）或
// mlkem768（服务端存 64 字节 seed、客户端用 1184 字节公钥）。临时密钥
// 交换恒为 ML-KEM-768+X25519 混合，与认证方式无关。
const (
	EncryptionAuthX25519 = "x25519"
	EncryptionAuthMLKEM  = "mlkem768"

	EncryptionModeNative = "native"
	EncryptionModeXorpub = "xorpub"
	EncryptionModeRandom = "random"

	// 与 xray vlessenc 默认形态一致的填充链、票据有效期与客户端 RTT 策略。
	encryptionPaddingChain = "100-111-1111.75-0-111.50-0-3333"
	encryptionTicket       = "600s"
	encryptionRTT          = "0rtt"
)

// EncryptionEnabled 表示该入站启用了 VLESS 加密。
func (s Settings) EncryptionEnabled() bool { return s.EncryptionAuth != "" }

func (s Settings) encryptionMode() string {
	if s.EncryptionMode == "" {
		return EncryptionModeNative
	}
	return s.EncryptionMode
}

// GenerateEncryptionSecret 生成服务端认证秘密（RawURLEncoding 存储）：
// x25519 为 32 字节私钥，mlkem768 为 64 字节 seed。
func GenerateEncryptionSecret(auth string) (string, error) {
	switch auth {
	case EncryptionAuthX25519:
		key, err := ecdh.X25519().GenerateKey(rand.Reader)
		if err != nil {
			return "", err
		}
		return base64.RawURLEncoding.EncodeToString(key.Bytes()), nil
	case EncryptionAuthMLKEM:
		seed := make([]byte, 64)
		if _, err := rand.Read(seed); err != nil {
			return "", err
		}
		return base64.RawURLEncoding.EncodeToString(seed), nil
	}
	return "", fmt.Errorf("不支持的 VLESS 加密认证方式 %s", auth)
}

// ValidateEncryptionSecret 校验已存秘密的编码与长度。
func ValidateEncryptionSecret(auth, secret string) error {
	raw, err := base64.RawURLEncoding.DecodeString(secret)
	if err != nil {
		return errors.New("VLESS 加密密钥格式错误")
	}
	want := 0
	switch auth {
	case EncryptionAuthX25519:
		want = 32
	case EncryptionAuthMLKEM:
		want = 64
	}
	if want == 0 || len(raw) != want {
		return errors.New("VLESS 加密密钥长度不正确")
	}
	return nil
}

// encryptionPublic 把服务端秘密单向派生为客户端认证段。
func encryptionPublic(auth, secret string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(secret)
	if err != nil {
		return "", err
	}
	switch auth {
	case EncryptionAuthX25519:
		key, err := ecdh.X25519().NewPrivateKey(raw)
		if err != nil {
			return "", err
		}
		return base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), nil
	case EncryptionAuthMLKEM:
		key, err := mlkem.NewDecapsulationKey768(raw)
		if err != nil {
			return "", err
		}
		return base64.RawURLEncoding.EncodeToString(key.EncapsulationKey().Bytes()), nil
	}
	return "", fmt.Errorf("不支持的 VLESS 加密认证方式 %s", auth)
}

// DecryptionString 组装 Xray 入站 settings.decryption；未启用返回 "none"。
func (s Settings) DecryptionString() string {
	if !s.EncryptionEnabled() {
		return "none"
	}
	return strings.Join([]string{"mlkem768x25519plus", s.encryptionMode(), encryptionTicket, encryptionPaddingChain, s.EncryptionSecret}, ".")
}

// EncryptionString 组装客户端 encryption 串（订阅下发用）；未启用返回 "none"。
func (s Settings) EncryptionString() (string, error) {
	if !s.EncryptionEnabled() {
		return "none", nil
	}
	pub, err := encryptionPublic(s.EncryptionAuth, s.EncryptionSecret)
	if err != nil {
		return "", err
	}
	return strings.Join([]string{"mlkem768x25519plus", s.encryptionMode(), encryptionRTT, encryptionPaddingChain, pub}, "."), nil
}
