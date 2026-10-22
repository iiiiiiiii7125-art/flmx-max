package middleware

import (
	"strings"
	"testing"
)

// 1.3.3 hotfix（用户反馈：面板更新后浏览器死缓存，刷新仍是旧界面）。
// 根因不在 index.html 的 HTTP 头（html 块本就有 no-store），而在
// Service Worker：vite-plugin-pwa 用 registerType "prompt"，新 SW
// 必须等用户点更新 toast 才接管；用户点「稍后」或没看到 toast，
// 旧 SW 就一直用预缓存的旧 index.html/旧资源应答导航，HTTP 层
// 再刷新多少次都绕不过 SW。次因是 nginx 静态正则块给无哈希的
// public/ 直出文件（favicon、PWA 图标、背景图）也发 30d immutable，
// 且 expires 与 add_header 并存发出两份 Cache-Control。
// 本组静态断言只锁「全部浏览器出站面」的缓存口径，防任一面回退：
//
//   面 1 前端容器 nginx.conf：入口文档 no-store、/assets/ 哈希资源
//        一年 immutable（单头）、无哈希 public 文件 1 天适中缓存
//        且禁 immutable/禁 expires 双头、SW 文件 no-store；
//   面 2 后端直出 HTML：仅 errorPage 一处（面板 UI 不经后端出站），
//        须带 Cache-Control: no-cache；
//   面 3 panel_install.sh 的 Caddy 模板：裸 reverse_proxy 透传源站
//        头，脚本不得自行写 Cache-Control（双写会合并出怪头）；
//   面 4 vite PWA 配置与注册代码：autoUpdate + skipWaiting +
//        clientsClaim，禁止 onNeedRefresh 提示式更新回潮。
// 文件缺失（裁剪检出）时跳过，与本包既有静态断言同风格。
// repoRootFile / nginxLocationBlocks 复用 log_redaction_fix132_test.go 的
// 同名实现（并入 main 时去重，两份实现逐字等价、断言互补全保留）。

func TestNginxHashedAssetsImmutable(t *testing.T) {
	conf, ok := repoRootFile(t, "vite-frontend/nginx.conf")
	if !ok {
		t.Skip("nginx.conf not present in this checkout")
	}
	blocks := nginxLocationBlocks(conf)
	block, found := blocks["location ^~ /assets/"]
	if !found {
		t.Fatal("nginx location ^~ /assets/ missing: hashed build assets fall through to the extension regex block and lose the one-year immutable policy")
	}
	if !strings.Contains(block, `add_header Cache-Control "public, max-age=31536000, immutable" always;`) {
		t.Fatalf("hashed assets must be cached one year immutable; block:\n%s", block)
	}
	if strings.Contains(block, "expires") {
		t.Fatalf("expires next to add_header Cache-Control emits a second Cache-Control header; block:\n%s", block)
	}
	if !strings.Contains(block, "try_files $uri =404;") {
		t.Fatalf("/assets/ must 404 on miss, never fall back to index.html; block:\n%s", block)
	}
}

func TestNginxEntryDocumentNeverCached(t *testing.T) {
	conf, ok := repoRootFile(t, "vite-frontend/nginx.conf")
	if !ok {
		t.Skip("nginx.conf not present in this checkout")
	}
	blocks := nginxLocationBlocks(conf)
	block, found := blocks[`location ~* \.(?:htm|html)$`]
	if !found {
		t.Fatal("nginx HTML location missing: SPA entry document would ship without explicit cache headers and be heuristically cached")
	}
	if !strings.Contains(block, `Cache-Control "no-cache, no-store, must-revalidate"`) {
		t.Fatalf("entry document must be no-store so an update is picked up on the next load; block:\n%s", block)
	}
}

func TestNginxUnhashedStaticsNotImmutable(t *testing.T) {
	conf, ok := repoRootFile(t, "vite-frontend/nginx.conf")
	if !ok {
		t.Skip("nginx.conf not present in this checkout")
	}
	blocks := nginxLocationBlocks(conf)
	block, found := blocks[`location ~* \.(js|css|png|jpg|jpeg|gif|ico|svg)$`]
	if !found {
		t.Fatal("nginx static-extension location missing")
	}
	// public/ 直出文件（favicon、PWA 图标、背景图）文件名无哈希：
	// immutable 会让改图一个月刷不出来，这是死缓存投诉的次级成因。
	if strings.Contains(block, "immutable") {
		t.Fatalf("unhashed public files must not be immutable; block:\n%s", block)
	}
	if strings.Contains(block, "expires") {
		t.Fatalf("expires next to add_header Cache-Control emits a second Cache-Control header; block:\n%s", block)
	}
	if !strings.Contains(block, `add_header Cache-Control "public, max-age=86400" always;`) {
		t.Fatalf("unhashed statics should carry a moderate one-day cache; block:\n%s", block)
	}
}

func TestNginxServiceWorkerFilesNeverCached(t *testing.T) {
	conf, ok := repoRootFile(t, "vite-frontend/nginx.conf")
	if !ok {
		t.Skip("nginx.conf not present in this checkout")
	}
	blocks := nginxLocationBlocks(conf)
	block, found := blocks[`location ~* (sw\.js|service-worker\.js|workbox-.*\.js|manifest\.webmanifest)$`]
	if !found {
		t.Fatal("nginx SW/manifest location missing: browsers would not reliably notice a new service worker")
	}
	if !strings.Contains(block, `Cache-Control "no-cache, no-store, must-revalidate"`) {
		t.Fatalf("sw.js and manifest must be revalidated on every load; block:\n%s", block)
	}
}

func TestBackendErrorPageNoCache(t *testing.T) {
	src, ok := repoRootFile(t, "go-backend/internal/http/handler/handler.go")
	if !ok {
		t.Skip("handler.go not present in this checkout")
	}
	i := strings.Index(src, "func (h *Handler) errorPage")
	if i < 0 {
		t.Fatal("errorPage handler not found")
	}
	end := strings.Index(src[i:], "\n}\n")
	if end < 0 {
		t.Fatal("errorPage body not terminated as expected")
	}
	if !strings.Contains(src[i:i+end], `w.Header().Set("Cache-Control", "no-cache")`) {
		t.Fatal("errorPage is the backend's only direct HTML surface and must send Cache-Control: no-cache like the frontend entry document")
	}
	// 面板 UI 不得经后端出站：后端不许出现 embed/FileServer 直出前端。
	for _, bad := range []string{"go:embed", "http.FileServer"} {
		if strings.Contains(src, bad) {
			t.Fatalf("handler.go unexpectedly serves files via %s; the UI ships only through the frontend container", bad)
		}
	}
}

func TestCaddyTemplatePassesOriginCacheHeaders(t *testing.T) {
	script, ok := repoRootFile(t, "panel_install.sh")
	if !ok {
		t.Skip("panel_install.sh not present in this checkout")
	}
	if !strings.Contains(script, "reverse_proxy 127.0.0.1:") {
		t.Fatal("Caddy template in panel_install.sh no longer reverse-proxies to the frontend container; re-audit its cache headers")
	}
	// Caddy 裸 reverse_proxy 原样透传源站（前端 nginx）的响应头，
	// 这是菜单 5 部署下缓存口径的唯一事实来源。脚本若开始自写
	// Cache-Control，浏览器会收到两份头合并出的怪值，必须先改本断言。
	if strings.Contains(script, "Cache-Control") {
		t.Fatal("panel_install.sh writes its own Cache-Control; Caddy would merge it with the origin's header — unify the policy in vite-frontend/nginx.conf instead")
	}
}

func TestPWAUsesAutoUpdate(t *testing.T) {
	cfg, ok := repoRootFile(t, "vite-frontend/vite.config.ts")
	if !ok {
		t.Skip("vite.config.ts not present in this checkout")
	}
	if strings.Contains(cfg, `registerType: "prompt"`) {
		t.Fatal("registerType \"prompt\" leaves the new SW waiting on a dismissible toast while the old SW serves the stale precached UI — use \"autoUpdate\"")
	}
	for _, want := range []string{`registerType: "autoUpdate"`, "skipWaiting: true", "clientsClaim: true"} {
		if !strings.Contains(cfg, want) {
			t.Fatalf("vite.config.ts must contain %q for instant SW takeover", want)
		}
	}
	main, ok := repoRootFile(t, "vite-frontend/src/main.tsx")
	if !ok {
		t.Skip("main.tsx not present in this checkout")
	}
	if strings.Contains(main, "onNeedRefresh") {
		t.Fatal("main.tsx must not gate SW updates behind an onNeedRefresh prompt")
	}
	if !strings.Contains(main, "registerSW({ immediate: true })") {
		t.Fatal("main.tsx must register the service worker immediately")
	}
}
