package security

import (
	"errors"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// MaxPasswordBytes 是 bcrypt 的有效输入上限（字节数，按 UTF-8
// 编码后计）：超出部分被 bcrypt 静默忽略，两条只差 72 字节之后
// 内容的口令在校验时完全等价（B1-02）。此前全仓设密面只有最小
// 长度、无上限，使用长口令的用户（往往是最在意安全者，含被强制
// 改密的管理员）得到的熵远低于其认知且永不报错。
const MaxPasswordBytes = 72

// ErrPasswordTooLong 是设密面拒绝超长口令的具名错误，各设密面
// 经既有错误通道原样回给调用方点名。
var ErrPasswordTooLong = errors.New("密码超过 72 字节上限（bcrypt 限制），请使用更短的密码")

func HashPassword(plain string) (string, error) {
	if len(plain) > MaxPasswordBytes {
		return "", ErrPasswordTooLong
	}
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
		// B1-02：超 72 字节的候选口令不得靠 bcrypt 截断与存储口令
		// 的前 72 字节「等价命中」——校验面同拒（补跑 dummy bcrypt
		// 拉平时序，与下方 legacy 错密路径同口径）。legacy MD5 分支
		// 不受此限（MD5 无截断语义，存量长口令仍可登录并在改密面
		// 被引导缩短）。
		if len(plain) > MaxPasswordBytes {
			_ = bcrypt.CompareHashAndPassword([]byte(dummyBcryptHash), []byte(plain))
			return false, false
		}
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
