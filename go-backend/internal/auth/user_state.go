package auth

type UserAuthState struct {
	ID                  int64
	RoleID              int
	Status              int
	PasswordChangedAt   int64
	TokensRevokedBefore int64
}

// RevocationWatermark 返回该用户的吊销水位（unix 毫秒）：改密时刻
// （PasswordChangedAt）与服务端登出吊销水位（TokensRevokedBefore，
// E1-02）取大。复核侧以 IatMs <= 水位判 token 失效（与既有改密口径
// 一致），签发侧须保证新 token 的 IatMs 严格大于水位（F-068）。
func (s *UserAuthState) RevocationWatermark() int64 {
	if s == nil {
		return 0
	}
	if s.TokensRevokedBefore > s.PasswordChangedAt {
		return s.TokensRevokedBefore
	}
	return s.PasswordChangedAt
}

// TokenStale 判定签发时刻为 iatMs 的 token 是否已被吊销水位覆盖。
func (s *UserAuthState) TokenStale(iatMs int64) bool {
	return iatMs <= s.RevocationWatermark()
}
