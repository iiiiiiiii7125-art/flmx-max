package security

import (
	"strings"

	"golang.org/x/crypto/bcrypt"
)

func HashPassword(plain string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// dummyBcryptHash 与 handler 包 K-001 占位同值（合法 bcrypt 格式，
// cost 10）：legacy 校验失败路径用它补跑一次 bcrypt，使失败耗时
// 与 bcrypt 账号错密同量级。
const dummyBcryptHash = "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"

// VerifyPassword 返回 (是否命中， 存储哈希是否为 legacy MD5)。
// R3 B01-003：legacy 分支原为 MD5 快速比较——错密在微秒级返回，
// 与「用户不存在（dummy bcrypt）/bcrypt 账号错密（~100ms）」形成
// 可区分的三档时序，可枚举仍用 MD5 存量口令的账号。现把成本补齐
// 下沉到本函数：legacy 错密在返回前补跑一次 dummy bcrypt，全部
// 调用方（login、sub_store 及未来校验面）自动同口径；legacy 命中
// 保持快速路径（成功后调用方立即 rehash，时序差不可用于枚举）。
func VerifyPassword(storedHash, plain string) (bool, bool) {
	if strings.HasPrefix(storedHash, "$2") {
		return bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(plain)) == nil, false
	}
	if MD5(plain) == storedHash {
		return true, true
	}
	_ = bcrypt.CompareHashAndPassword([]byte(dummyBcryptHash), []byte(plain))
	return false, true
}

func IsLegacyPasswordHash(storedHash string) bool {
	storedHash = strings.TrimSpace(storedHash)
	if len(storedHash) != 32 {
		return false
	}
	for _, r := range storedHash {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F') {
			continue
		}
		return false
	}
	return true
}
