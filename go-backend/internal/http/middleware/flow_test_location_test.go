package middleware

import (
	"strings"
	"testing"
)

// L4-01：GET /flow/test 必须经前端反代直通后端、回纯文本 test，
// 而不是落入 location / 的 SPA index.html（200 假活）。
// 修法是补 location（不是撤后端注册）：docker healthcheck 与
// 后端升级门共用 /flow/test 作探针锚点，探针语义须保留。
func TestNginxFlowTestProxiesToBackend(t *testing.T) {
	conf, ok := repoRootFile(t, "vite-frontend/nginx.conf")
	if !ok {
		t.Skip("nginx.conf not present in this checkout")
	}
	blocks := nginxLocationBlocks(conf)
	block, found := blocks["location = /flow/test"]
	if !found {
		t.Fatal("nginx location = /flow/test missing: GET /flow/test falls through to location / and returns the SPA index.html (200 false-positive) instead of backend \"test\"")
	}
	if !strings.Contains(block, "proxy_pass http://backend:6365/flow/test;") {
		t.Fatalf("location = /flow/test must proxy to the backend; block:\n%s", block)
	}
	if strings.Contains(block, "try_files") {
		t.Fatalf("location = /flow/test must not fall back to try_files/index.html; block:\n%s", block)
	}
	if !strings.Contains(block, "access_log off") {
		t.Fatalf("location = /flow/test must disable access_log like the other /flow/* blocks; block:\n%s", block)
	}
}

// 全族门：后端注册的每一条 /flow/* 路由都必须在 nginx 有对应
// location——L4-01 是同族第三个缺口（前两个随 2.1.12 补），
// 以后新增 /flow/* 端点忘补 location 时本测试先红。
func TestNginxAllBackendFlowRoutesHaveLocation(t *testing.T) {
	handlerSrc, ok := repoRootFile(t, "go-backend/internal/http/handler/handler.go")
	if !ok {
		t.Skip("handler.go not present in this checkout")
	}
	conf, ok := repoRootFile(t, "vite-frontend/nginx.conf")
	if !ok {
		t.Skip("nginx.conf not present in this checkout")
	}
	routes := map[string]struct{}{}
	for _, line := range strings.Split(handlerSrc, "\n") {
		line = strings.TrimSpace(line)
		const prefix = `mux.HandleFunc("/flow/`
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		rest := line[len(prefix):]
		end := strings.Index(rest, `"`)
		if end < 0 {
			t.Fatalf("unparseable flow route registration: %q", line)
		}
		routes["/flow/"+rest[:end]] = struct{}{}
	}
	if len(routes) == 0 {
		t.Fatal("no /flow/* routes parsed from handler.go; the gate itself is broken")
	}
	blocks := nginxLocationBlocks(conf)
	for route := range routes {
		if _, ok := blocks["location = "+route]; ok {
			continue
		}
		if _, ok := blocks["location "+route]; ok {
			continue
		}
		t.Errorf("backend route %s has no nginx location (request would fall through to the SPA); add a location block or remove the backend registration", route)
	}
}
