package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

const jwtSecretFileName = "jwt_secret"

// EnsureJWTSecret 在 JWT_SECRET 未设置时，从文件加载或生成持久化密钥。
// 语义：优先环境变量（JWTEphemeral=false 时直接返回）→ 读 <DBPath 同级>/jwt_secret 文件
// → 都没有则生成 32 字节随机值并以 0600 权限写入文件。
// 写入为原子发布（同目录临时文件＋rename，R7-19）：崩溃/断电至多
// 遗留一份 .tmp 临时文件，jwt_secret 本体恒为完整旧值或完整新值，
// 不会出现半写撕裂被下次启动当作真值接受。两个来源分支（读到既有
// 密钥／新生成密钥）各打一行来源日志（只记文件路径，绝不含密钥
// 本体或片段），便于事后区分会话失效的成因。
// 这保证重启后旧 token 仍有效。FromEnv 的临时生成逻辑保留作为兜底
// （如文件不可写时，仍使用内存中的临时密钥）。
//
// 适配说明：通过 JWTEphemeral 标志判断是否为临时密钥，
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
			// R7-19 来源日志：读到既有密钥（日志只记路径）。
			log.Printf("jwt_secret 来源: 读到既有密钥（文件 %s）", path)
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
	// R7-19 原子写：先以 0600 写同目录临时文件，再 rename 发布
	//（与导入前快照 saveImportAutoBackup 同范式）。rename 前崩溃
	// 只遗留 .tmp 文件、本体不受影响；rename 后本体即完整新值。
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(secret+"\n"), 0o600); err != nil {
		return false, fmt.Errorf("写入 %s: %w", tmp, err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return false, fmt.Errorf("设置 %s 权限: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return false, fmt.Errorf("发布 %s: %w", path, err)
	}
	// R7-19 来源日志：新生成密钥（日志只记路径）。
	log.Printf("jwt_secret 来源: 新生成密钥（文件 %s）", path)
	cfg.JWTSecret = secret
	cfg.JWTEphemeral = false
	return true, nil
}
