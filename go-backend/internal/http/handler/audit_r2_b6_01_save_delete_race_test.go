package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// R18-R2-B6-01（R18-B6-02 保存腿剩余）：换址保存的下载窗口内
// （最长 20s）被删的模板会被 Save() 的 upsert 复活——保存路径
// 全程无存在性复核，而刷新腿（2ccd5d45）已有。保存前复核：
// 下载窗口内删除 → 保存被拒、模板仍不在。
func TestR18R2B601SaveDuringDownloadAfterDeleteIsRefused(t *testing.T) {
	// 本地下载须过 SecureFetch 私网 allowlist（仅测试内放行回环）。
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "127.0.0.1")

	r, err := repo.Open(t.TempDir() + "/r2-b6-01-save-race.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")

	// 下载在管理员 B 删除模板前开始、在删除后返回：用阻塞式
	// 本地源钉住下载窗口，而非赌时序。
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		once.Do(func() { close(entered) })
		select {
		case <-release:
		case <-time.After(10 * time.Second):
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(b604Content))
	}))
	defer srv.Close()

	tpl := &model.SubscriptionTemplate{Name: "r2-b6-01-tpl", Kind: "singbox",
		SourceURL: "http://127.0.0.1:1/r2-b6-01-old.json", Content: b604Content, LastFetch: 111}
	if err := r.SaveSubscriptionTemplate(tpl); err != nil {
		t.Fatalf("seed template: %v", err)
	}

	// 换址保存：新 URL 指向阻塞源，下载即开始。注意 goroutine 内
	// 不用 callTemplateSave（它调 t.Fatalf，禁在测试 goroutine
	// 之外调用），此处内联 POST 并把解码失败也当结果回传。
	type saveResult struct {
		code int
		msg  string
		err  error
	}
	done := make(chan saveResult, 1)
	go func() {
		body := fmt.Sprintf(`{"id":%d,"name":"r2-b6-01-renamed","kind":"singbox","sourceUrl":%q}`,
			tpl.ID, srv.URL+"/r2-b6-01-new.json")
		rec := httptest.NewRecorder()
		h.subscriptionTemplateSave(rec, httptest.NewRequest("POST", "/api/v1/subscription/template/save", strings.NewReader(body)))
		var env struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			done <- saveResult{err: fmt.Errorf("decode save response: %v (body=%s)", err, rec.Body.String())}
			return
		}
		done <- saveResult{code: env.Code, msg: env.Msg}
	}()

	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("download never started")
	}
	// 管理员 B 在下载窗口内删除该模板（无订阅在用，放行）。
	if err := r.DeleteSubscriptionTemplate(tpl.ID); err != nil {
		t.Fatalf("delete template mid-download: %v", err)
	}
	close(release)

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("save request failed: %v", res.err)
		}
		if res.code == 0 || !strings.Contains(res.msg, "已被删除") {
			t.Fatalf("save of a template deleted mid-download must be refused as deleted, got code=%d msg=%q", res.code, res.msg)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("save never returned")
	}
	if got, err := r.GetSubscriptionTemplate(tpl.ID); err != nil || got != nil {
		t.Fatalf("deleted template must stay deleted after save, got %+v err=%v", got, err)
	}
}
