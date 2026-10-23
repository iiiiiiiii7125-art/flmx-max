package contract_test

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go-backend/internal/auth"
	"go-backend/internal/store/repo"
)

type notifySink struct {
	mu       sync.Mutex
	telegram []map[string]interface{}
	webhook  []map[string]interface{}
	paths    []string
}

func (s *notifySink) server(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]interface{}
		_ = json.Unmarshal(raw, &body)
		s.mu.Lock()
		s.paths = append(s.paths, r.URL.Path)
		if strings.HasPrefix(r.URL.Path, "/bot") {
			s.telegram = append(s.telegram, body)
		} else {
			s.webhook = append(s.webhook, body)
		}
		s.mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (s *notifySink) waitWebhook(t *testing.T, n int) []map[string]interface{} {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		if len(s.webhook) >= n {
			out := append([]map[string]interface{}(nil), s.webhook...)
			s.mu.Unlock()
			return out
		}
		s.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t.Fatalf("expected %d webhook calls, got %d", n, len(s.webhook))
	return nil
}

func setConfig(t *testing.T, r *repo.Repository, name, value string) {
	t.Helper()
	if err := r.DB().Exec(`
		INSERT INTO vite_config(name, value, time) VALUES(?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET value = excluded.value, time = excluded.time`,
		name, value, time.Now().UnixMilli()).Error; err != nil {
		t.Fatal(err)
	}
}

func TestNotifyTestEndpointContract(t *testing.T) {
	secret := "contract-jwt-secret"
	router, _ := setupContractRouter(t, secret)
	admin, _ := auth.GenerateToken(1, "admin@user", 0, secret)
	user, _ := auth.GenerateToken(2, "normal_user", 1, secret)
	sink := &notifySink{}
	srv := sink.server(t)

	if out := kernelContractPost(t, router, user, "/api/v1/notify/test", `{}`); out.Code == 0 {
		t.Fatalf("non-admins must be refused: %+v", out)
	}
	if out := kernelContractPost(t, router, admin, "/api/v1/notify/test", `{}`); out.Code == 0 {
		t.Fatalf("nothing configured must be an error: %+v", out)
	}

	// Unsaved form values are used as given.
	out := kernelContractPost(t, router, admin, "/api/v1/notify/test",
		`{"telegramToken":"123:abc","telegramChatId":"42","telegramApiBase":"`+srv.URL+`","webhookUrl":"`+srv.URL+`/hook"}`)
	if out.Code != 0 {
		t.Fatalf("test send: %+v", out)
	}
	if len(sink.telegram) != 1 || sink.paths[0] != "/bot123:abc/sendMessage" || sink.telegram[0]["chat_id"] != "42" ||
		!strings.Contains(sink.telegram[0]["text"].(string), "测试通知") {
		t.Fatalf("telegram call: %v %v", sink.paths, sink.telegram)
	}
	if len(sink.webhook) != 1 || sink.webhook[0]["event"] != "test" || sink.webhook[0]["title"] != "测试通知" {
		t.Fatalf("webhook call: %v", sink.webhook)
	}

	// A failing endpoint reports why without leaking the bot token.
	out = kernelContractPost(t, router, admin, "/api/v1/notify/test",
		`{"telegramToken":"123:secret-token","telegramChatId":"42","telegramApiBase":"http://127.0.0.1:1"}`)
	if out.Code == 0 || !strings.Contains(out.Msg, "Telegram") || strings.Contains(out.Msg, "secret-token") {
		t.Fatalf("failure message: %+v", out)
	}

	// Credentials stay hidden from non-admins and out of backups.
	for _, key := range []string{"notify_telegram_bot_token", "notify_webhook_url"} {
		if !repo.IsSensitiveConfigKey(key) {
			t.Fatalf("%s must be sensitive", key)
		}
	}
}

// TestNotifyImportedNodeDownContract — R5X-006 迁移案 (b) 拆层后的
// contract 层口径（DB-4 正向断言）：手动端点 /api/v1/inbound/probe
// （probeRoundTargeted）只返回探测结果、刷新缓存，**不驱动**
// inbound_down 告警——手动轮不计 streak 是 1.4.0 已发布设计，严禁
// 改回。告警状态机断言（定时轮打满 streak → 掉线/只报一次/恢复/
// 未选事件静默）已下沉 handler 包，见
// handler.TestR5B9ImportedNodeDownScheduledRoundAlerts（复用 B10
// 定时轮直调模式）。本用例在有 RFC1918 地址的环境实跑以下断言；
// 无地址环境由夹具 Skip 明示，引用结论时须标注 Skip/Pass 区分。
func TestNotifyImportedNodeDownContract(t *testing.T) {
	// R2 登记 #3：probe 目标迁至本机非 loopback RFC1918 地址（无可用地址时 Skip）。
	probeAddr := probeFixtureLocalAddr(t)
	secret := "contract-jwt-secret"
	router, r := setupContractRouter(t, secret)
	admin, _ := auth.GenerateToken(1, "admin@user", 0, secret)
	// webhook/telegram 桩是 notify 出站面（口径为仅硬拒层、loopback 合法），
	// 不在迁移范围，保持 loopback；只迁下方被 probe 探测的节点目标。
	sink := &notifySink{}
	srv := sink.server(t)
	setConfig(t, r, "notify_enabled", "true")
	setConfig(t, r, "notify_webhook_url", srv.URL+"/hook")
	setConfig(t, r, "notify_events", "inbound_down")

	ln, err := net.Listen("tcp", net.JoinHostPort(probeAddr, "0"))
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	link := "ss://" + base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:secret")) + "@198.51.100.50:1001#落地"
	if out := kernelContractPost(t, router, admin, "/api/v1/inbound/import", `{"links":"`+link+`"}`); out.Code != 0 {
		t.Fatalf("import: %+v", out)
	}
	if err := r.DB().Exec(`UPDATE node_inbound SET listen = ?, port = ?`, probeAddr, port).Error; err != nil {
		t.Fatal(err)
	}
	inboundID := mustQueryInt64(t, r, `SELECT id FROM node_inbound LIMIT 1`)

	// 手动探测打满并超过 streak 次数：端点每次都返回该入站的失败
	// 探测结果（DB-4 正向断言的对象是「结果」），但 webhook 恒为 0
	// ——手动轮既不累加 streak 也不产生掉线跃迁。
	for i := 0; i < 3; i++ {
		out := kernelContractPost(t, router, admin, "/api/v1/inbound/probe", `{}`)
		if out.Code != 0 {
			t.Fatalf("manual probe %d: %+v", i, out)
		}
		data, ok := out.Data.(map[string]interface{})
		if !ok {
			t.Fatalf("manual probe %d: data is %T, want per-inbound results", i, out.Data)
		}
		res, ok := data[jsonInt64(inboundID)].(map[string]interface{})
		if !ok {
			t.Fatalf("manual probe %d: no result for inbound %d in %v", i, inboundID, data)
		}
		if res["ok"] != false {
			t.Fatalf("manual probe %d: dead target must report ok=false, got %v", i, res)
		}
	}
	time.Sleep(200 * time.Millisecond)
	if len(sink.webhook) != 0 {
		t.Fatalf("DB-4: manual probes must not drive inbound_down alerts, got %d webhooks", len(sink.webhook))
	}

	// 目标恢复后手动探测返回 ok=true，同样不得产生「恢复」告警
	// （恢复跃迁只归定时轮）。单列子用例：这一腿要求能拨通本机已
	// 监听的 RFC1918 地址，个别沙箱对 RFC1918 自连施加 connect()
	// EPERM（R5-I 报告 E3 在案）——子用例先自检，自检不过 Skip
	// 明示，不拖累父用例已实跑的断言，引用结论时区分 Skip/Pass。
	t.Run("manualProbeAfterRecovery", func(t *testing.T) {
		ln, err := net.Listen("tcp", net.JoinHostPort(probeAddr, jsonInt64(int64(port))))
		if err != nil {
			t.Skipf("port reused: %v", err)
		}
		defer ln.Close()
		conn, dialErr := net.DialTimeout("tcp", net.JoinHostPort(probeAddr, jsonInt64(int64(port))), 2*time.Second)
		if dialErr != nil {
			t.Skipf("本环境无法拨通已监听的 RFC1918 地址（沙箱 connect() 策略），恢复腿待 CI/无过滤环境验收: %v", dialErr)
		}
		_ = conn.Close()
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				_ = c.Close()
			}
		}()
		out := kernelContractPost(t, router, admin, "/api/v1/inbound/probe", `{}`)
		if out.Code != 0 {
			t.Fatalf("manual probe after recovery: %+v", out)
		}
		data, _ := out.Data.(map[string]interface{})
		res, _ := data[jsonInt64(inboundID)].(map[string]interface{})
		if res == nil || res["ok"] != true {
			t.Fatalf("manual probe after recovery must report ok=true, got %v", res)
		}
		time.Sleep(200 * time.Millisecond)
		if len(sink.webhook) != 0 {
			t.Fatalf("DB-4: manual probes must not drive recovery alerts, got %d webhooks", len(sink.webhook))
		}
	})
}
