package repo

import (
	"database/sql"
	"errors"

	"go-backend/internal/auth"
	"go-backend/internal/store/model"
)

func (r *Repository) GetUserAuthState(userID int64) (*auth.UserAuthState, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var user struct {
		ID                  int64 `gorm:"column:id"`
		RoleID              int   `gorm:"column:role_id"`
		Status              int   `gorm:"column:status"`
		PasswordChangedAt   int64 `gorm:"column:password_changed_at"`
		TokensRevokedBefore int64 `gorm:"column:tokens_revoked_before"`
	}
	if err := r.db.Model(&model.User{}).Select("id", "role_id", "status", "password_changed_at", "tokens_revoked_before").Where("id = ?", userID).First(&user).Error; err != nil {
		return nil, normalizeNotFoundErr(err)
	}
	return &auth.UserAuthState{
		ID:                  user.ID,
		RoleID:              user.RoleID,
		Status:              user.Status,
		PasswordChangedAt:   user.PasswordChangedAt,
		TokensRevokedBefore: user.TokensRevokedBefore,
	}, nil
}

// O11-P3-1: RevokeUserSessions 将 password_changed_at 推进到 now（毫秒），
// 使此前签发的全部 JWT 失效：中间件以 claims.IatMs <= PasswordChangedAt
// 判定 token 失效（middleware/auth.go、ws/server.go 同逻辑）。
// 用于删除通行证密钥等凭据变更场景，不修改密码本身。
func (r *Repository) RevokeUserSessions(userID int64, now int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Model(&model.User{}).
		Where("id = ?", userID).
		Updates(map[string]interface{}{
			"password_changed_at": now,
			"updated_time":        sql.NullInt64{Int64: now, Valid: true},
		}).Error
}

// E1-02: RevokeUserTokens 将 tokens_revoked_before 推进到 now（毫秒），
// 使此前签发的全部 JWT 失效：复核侧以 RevocationWatermark（改密时刻
// 与本水位取大）判定（auth.UserAuthState.TokenStale，中间件/WS 同
// 逻辑）。用于服务端登出：只推进吊销水位，不修改密码与
// PasswordChangedAt，重新登录签发的新 token 不受影响。
func (r *Repository) RevokeUserTokens(userID int64, now int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Model(&model.User{}).
		Where("id = ?", userID).
		Updates(map[string]interface{}{
			"tokens_revoked_before": now,
			"updated_time":          sql.NullInt64{Int64: now, Valid: true},
		}).Error
}
