package middleware

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"go-backend/internal/auth"
	"go-backend/internal/http/response"
)

type contextKey string

const ClaimsContextKey contextKey = "claims"

type AuthOptions struct {
	JWTSecret        string
	GetUserAuthState func(userID int64) (*auth.UserAuthState, error)
}

func JWT(opts AuthOptions) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if shouldSkip(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}

			if !strings.HasPrefix(r.URL.Path, "/api/") {
				next.ServeHTTP(w, r)
				return
			}

			token := strings.TrimSpace(r.Header.Get("Authorization"))
			if token == "" {
				if allowsOptionalAuth(r.URL.Path) {
					next.ServeHTTP(w, r)
					return
				}
				response.WriteJSON(w, response.Err(401, "未登录或token已过期"))
				return
			}

			claims, ok := auth.ValidateToken(token, opts.JWTSecret)
			if !ok {
				if allowsOptionalAuth(r.URL.Path) {
					next.ServeHTTP(w, r)
					return
				}
				response.WriteJSON(w, response.Err(401, "无效的token或token已过期"))
				return
			}

			var authState *auth.UserAuthState
			if opts.GetUserAuthState != nil {
				userID, err := strconv.ParseInt(claims.Sub, 10, 64)
				if err != nil {
					if allowsOptionalAuth(r.URL.Path) {
						next.ServeHTTP(w, r)
						return
					}
					response.WriteJSON(w, response.Err(401, "无效的token或token已过期"))
					return
				}
				state, err := opts.GetUserAuthState(userID)
				if err != nil || state == nil || state.Status != 1 || state.RoleID != claims.RoleID || claims.IatMs <= state.PasswordChangedAt {
					if allowsOptionalAuth(r.URL.Path) {
						next.ServeHTTP(w, r)
						return
					}
					response.WriteJSON(w, response.Err(401, "无效的token或token已过期"))
					return
				}
				authState = state
			}

			if requiresAdmin(r.URL.Path) && claims.RoleID != 0 {
				response.WriteJSON(w, response.Err(403, "权限不足，仅管理员可操作"))
				return
			}

			// M-05: 种子默认口令未改时强制先改密。除改密接口外一律 403。
			// 之前仅靠登录返回的 requirePasswordChange 标志提示前端，服务端无任何强制。
			if isDefaultSeedAdminPassword(authState) && r.URL.Path != "/api/v1/user/updatePassword" {
				response.WriteJSON(w, response.Err(403, "请先修改默认管理员密码"))
				return
			}

			ctx := context.WithValue(r.Context(), ClaimsContextKey, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := r.Context().Value(ClaimsContextKey)
		claims, ok := raw.(auth.Claims)
		if !ok {
			response.WriteJSON(w, response.Err(401, "无法获取用户权限信息"))
			return
		}
		if claims.RoleID != 0 {
			response.WriteJSON(w, response.Err(403, "权限不足，仅管理员可操作"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func allowsOptionalAuth(path string) bool {
	return path == "/api/v1/config/get"
}

func shouldSkip(path string) bool {
	switch {
	case strings.HasPrefix(path, "/flow/"):
		return true
	case strings.HasPrefix(path, "/api/v1/open_api/"):
		return true
	case strings.HasPrefix(path, "/api/v1/captcha/"):
		return true
	case path == "/api/v1/config/get":
		return false
	case path == "/api/v1/user/login":
		return true
	case path == "/api/v1/user/passkey/status" || path == "/api/v1/user/passkey/login/begin" || path == "/api/v1/user/passkey/login/finish":
		return true
	case path == "/api/v1/public/config/get":
		return true
	case path == "/api/v1/federation/connect":
		return true
	case path == "/api/v1/federation/tunnel/create":
		return true
	case path == "/api/v1/federation/runtime/reserve-port":
		return true
	case path == "/api/v1/federation/runtime/apply-role":
		return true
	case path == "/api/v1/federation/runtime/release-role":
		return true
	case path == "/api/v1/federation/runtime/diagnose":
		return true
	case path == "/api/v1/federation/runtime/command":
		return true
	default:
		return false
	}
}

func requiresAdmin(path string) bool {
	if strings.HasPrefix(path, "/api/v1/monitor/permission/") {
		return true
	}

	if strings.HasPrefix(path, "/api/v1/system/") {
		return true
	}

	if strings.HasPrefix(path, "/api/v1/group/") {
		return true
	}

	if strings.HasPrefix(path, "/api/v1/federation/share/") || strings.HasPrefix(path, "/api/v1/federation/node/") {
		return true
	}

	if strings.HasPrefix(path, "/api/v1/node/") {
		return true
	}

	if strings.HasPrefix(path, "/api/v1/kernel/") || strings.HasPrefix(path, "/api/v1/inbound/") ||
		strings.HasPrefix(path, "/api/v1/notify/") || strings.HasPrefix(path, "/api/v1/subscription/") {
		// "我的订阅"页（普通用户）需要读取默认订阅的 path 来拼链接；reset（换 token）仍仅管理员可在 handler 内执行
		if path == "/api/v1/inbound/sub/info" {
			return false
		}
		return true
	}

	if strings.HasPrefix(path, "/api/v1/speed-limit/") {
		return true
	}

	if strings.HasPrefix(path, "/api/v1/backup/") {
		return true
	}

	if strings.HasPrefix(path, "/api/v1/api/v1/backup/") {
		return true
	}

	if strings.HasPrefix(path, "/api/v1/tunnel/") {
		if strings.HasPrefix(path, "/api/v1/tunnel/user/tunnel") {
			return false
		}
		return true
	}

	switch path {
	case "/api/v1/user/create", "/api/v1/user/list", "/api/v1/user/update", "/api/v1/user/delete", "/api/v1/user/reset":
		return true
	case "/api/v1/user/quota/reset":
		return true // H-12: 任意登录用户可清零任意用户配额，须仅管理员可调用
	case "/api/v1/config/update", "/api/v1/config/update-single":
		return true
	case "/api/v1/announcement/update":
		return true
	default:
		return false
	}
}

// seedAdminPasswordChangedAt 必须与 store/repo/repository.go seedData 中种子
// 管理员的 PasswordChangedAt 保持一致：该值是"从未改过种子默认口令"的唯一信号
//（updatePassword / UpdateUserPassword 都会更新 password_changed_at）。
const seedAdminPasswordChangedAt = 1748914865000

// isDefaultSeedAdminPassword 判断该用户是否仍在使用种子默认口令。
func isDefaultSeedAdminPassword(state *auth.UserAuthState) bool {
	if state == nil || state.RoleID != 0 {
		return false
	}
	if state.Username != "admin@user" && state.Username != "admin_user" {
		return false
	}
	return state.PasswordChangedAt == seedAdminPasswordChangedAt
}
