package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestGlobalInterceptorUnauthenticatedReturns401: a request without a
// valid Basic Authorization header must get a 401 JSON answer with a
// WWW-Authenticate challenge — not a silently dropped connection
// (empty reply), which is what the old hijack-and-close did.
func TestGlobalInterceptorUnauthenticatedReturns401(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(GlobalInterceptor())
	r.GET("/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	cases := []struct {
		name string
		auth string // "" means no Authorization header
	}{
		{"no header", ""},
		{"bearer token", "Bearer xyz"},
		{"malformed basic", "Basic !!!not-base64!!!"},
		{"basic without credentials", "Basic "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/ping", nil)
			if tc.auth != "" {
				req.Header.Set("Authorization", tc.auth)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401; body %q", w.Code, w.Body.String())
			}
			if got := w.Header().Get("WWW-Authenticate"); got != "Basic" {
				t.Fatalf("WWW-Authenticate = %q, want %q", got, "Basic")
			}
			var resp Response
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("body is not JSON: %v (body %q)", err, w.Body.String())
			}
			if resp.Code != http.StatusUnauthorized || resp.Msg != "Unauthorized" {
				t.Fatalf("response = %+v, want Code 401 Msg Unauthorized", resp)
			}
		})
	}
}

// TestGlobalInterceptorValidBasicPassesThrough: a request with a valid
// Basic Authorization header shape must reach the handler.
func TestGlobalInterceptorValidBasicPassesThrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(GlobalInterceptor())
	r.GET("/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	req := httptest.NewRequest("GET", "/ping", nil)
	req.SetBasicAuth("admin", "admin")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %q", w.Code, w.Body.String())
	}
}
