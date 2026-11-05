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

// VerifyPassword 返回 (是否命中， 存储哈希是否为 legacy MD5 格式)。
// R-MD5 sunset：legacy MD5 此前在下方「验证通过即命中」、调用方
// 随后透明 rehash 为 bcrypt——只有登录过的账户才自愈，休眠账户的
// 无盐 MD5 无限期留在库中（库/备份泄露即口令明文）。现 legacy
// 存储恒不命中：第二返回值如实报告存储形态，登录面（login、
// sub_store）据此把账户收口为与导入面 normalizeImportedUserPassword
// 相同的终态（凭据作废＋停用、经既有管理员通道重置），其余校验面
// （updatePassword、passkey step-up）自然 fail-closed。
// 时序口径（R3 B01-003）保留并收紧：legacy 存储的任何候选（含
// 「正确」口令）都先补跑一次 dummy bcrypt 再返回，与 bcrypt 错密、
// 用户不存在三类失败同量级，不给「该账户仍持 legacy 哈希且口令
// 正确」留可区分信号。
func VerifyPassword(storedHash, plain string) (bool, bool) {
	if strings.HasPrefix(storedHash, "$2") {
		// B1-02：超 72 字节的候选口令不得靠 bcrypt 截断与存储口令
		// 的前 72 字节「等价命中」——校验面同拒（补跑 dummy bcrypt
		// 拉平时序，与下方 legacy 路径同口径）。
		if len(plain) > MaxPasswordBytes {
			_ = bcrypt.CompareHashAndPassword([]byte(dummyBcryptHash), []byte(plain))
			return false, false
		}
		return bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(plain)) == nil, false
	}
	_ = bcrypt.CompareHashAndPassword([]byte(dummyBcryptHash), []byte(plain))
	if IsLegacyPasswordHash(storedHash) {
		return false, true
	}
	return false, false
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
