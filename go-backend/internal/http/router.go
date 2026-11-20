package httpserver

import (
	"net/http"
	"strings"

	"go-backend/internal/http/handler"
	"go-backend/internal/http/middleware"
	"go-backend/internal/http/response"
)

func NewRouter(h *handler.Handler, jwtSecret string) http.Handler {
	mux := http.NewServeMux()
	h.Register(mux)
	mux.Handle("/system-info", h.WebSocketHandler())

	// R18-B10-01：未注册的 /api/ 路径不得回 Go 默认的裸文本 404——
	// 已登录调用方（前端含）无法按信封 code 分支。mux.Handler 的
	// pattern 为空即无任何路由命中，此时回信封 404（HTTP 404 +
	// {code:-1,msg:"接口不存在"}）；其余一律交回 mux（注册路由、
	// 方法不匹配的 405、/system-info、非 /api/ 路径的标准 404
	// 均不受影响）。本层在 JWT 中间件之内，未登录调用方仍先被
	// 拦为信封 401，不会绕过鉴权走到这里。
	notFound := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			if _, pattern := mux.Handler(r); pattern == "" {
				response.WriteJSONStatus(w, http.StatusNotFound, response.Err(-1, "接口不存在"))
				return
			}
		}
		mux.ServeHTTP(w, r)
	})

	wrapped := middleware.Recover(notFound)
	wrapped = middleware.JWT(middleware.AuthOptions{JWTSecret: jwtSecret, GetUserAuthState: h.GetUserAuthState})(wrapped)
	wrapped = middleware.RequestLog(wrapped)
	wrapped = middleware.CORS(wrapped)
	return wrapped
}
