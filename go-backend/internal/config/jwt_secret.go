package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const jwtSecretFileName = "jwt_secret"

// EnsureJWTSecret 在 JWT_SECRET 未设置时，从文件加载或生成持久化密钥。
// 语义：优先环境变量（JWTEphemeral=false 时直接返回）→ 读 <DBPath 同级>/jwt_secret 文件
// → 都没有则生成 32 字节随机值并以 0600 权限写入文件。
// 这保证重启后旧 token 仍有效。FromEnv 的临时生成逻辑保留作为兜底
// （如文件不可写时，仍使用内存中的临时密钥）。
//
// 移植自 kure29/flyx，适配：通过 JWTEphemeral 标志判断是否为临时密钥，
// 而非检查 JWTSecret 是否为空（我们的 FromEnv 总会生成临时密钥）。
func EnsureJWTSecret(cfg *Config) (generated bool, err error) {
	// 环境变量已配置，或已经是持久化密钥，无需处理。
	if !cfg.JWTEphemeral {
		return false, nil
	}

	path := filepath.Join(filepath.Dir(cfg.DBPath), jwtSecretFileName)
	data, err := os.ReadFile(path)
	if err == nil {
		if secret := strings.TrimSpace(string(data)); secret != "" {
			cfg.JWTSecret = secret
			cfg.JWTEphemeral = false
			return false, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("读取 %s: %w", path, err)
	}

	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return false, fmt.Errorf("生成 JWT 密钥: %w", err)
	}
	secret := hex.EncodeToString(buf)

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, fmt.Errorf("创建目录 %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(secret+"\n"), 0o600); err != nil {
		return false, fmt.Errorf("写入 %s: %w", path, err)
	}
	cfg.JWTSecret = secret
	cfg.JWTEphemeral = false
	return true, nil
}
