package middleware

import (
	"log"
	"net/http"

	"go-backend/internal/http/response"
	"go-backend/internal/logredact"
)

func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				// Log the panic server-side, but don't leak internal
				// details to the client.
				log.Printf("panic recovered: %s %s: %v", r.Method, logredact.Path(r.URL.Path), rec)
				response.WriteJSON(w, response.Err(-2, "internal error"))
			}
		}()
		next.ServeHTTP(w, r)
	})
}
