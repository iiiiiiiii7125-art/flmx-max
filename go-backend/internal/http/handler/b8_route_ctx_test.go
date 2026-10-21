package handler

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// F-079 (audit FINAL §5-B8 / A7-010): traceNodeRoutes 派发前查 ctx.Err()，
// 但收尾 wg.Wait() 不响应取消——请求方断开后仍等全部在途 traceroute
// 跑完。与 node_latency.go 的 L-002 已修模式同构（done channel + select）。
// 修复前本测试失败：取消后函数不返回（等满阻塞桩的释放）。
func TestTraceNodeRoutesHonorsContextCancelF079(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "b8-route.db"))
	if err != nil {
		t.Fatal(err)
	}
	h := New(r, "unit"+"-test-secret")

	now := time.Now().UnixMilli()
	if err := r.DB().Exec(`INSERT INTO vite_config(name, value, time) VALUES('latency_targets', '[{"name":"p1","host":"203.0.113.1","port":80,"enabled":true}]', ?)`, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.DB().Exec(`
		INSERT INTO node(name, secret, server_ip, server_ip_v4, server_ip_v6, port, interface_name, version, http, tls, socks, created_time, updated_time, status, tcp_listen_addr, udp_listen_addr, inx, is_remote, latency_targets)
		VALUES('f079-node', 'f079-secret', '10.0.0.1', '10.0.0.1', '', '10000-20000', '', 'v1', 1, 1, 1, ?, ?, 1, '[::]', '[::]', 0, 0, '')
	`, now, now).Error; err != nil {
		t.Fatal(err)
	}

	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	exited := make(chan struct{}, 1)
	orig := nodeTraceroute
	nodeTraceroute = func(h *Handler, nodeID int64, host string, family int) (map[string]interface{}, error) {
		entered <- struct{}{}
		<-release // 阻塞，模拟慢速在途 traceroute
		exited <- struct{}{}
		return map[string]interface{}{}, nil
	}
	defer func() {
		close(release) // 放行在途 worker，再恢复全局桩，避免与 worker 读写竞态
		select {
		case <-exited:
		case <-time.After(2 * time.Second):
		}
		nodeTraceroute = orig
	}()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := h.traceNodeRoutes(ctx, nil, true)
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("traceroute stub was never invoked; seed did not produce a job")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled trace must return ctx.Err(), got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("traceNodeRoutes did not return after context cancellation")
	}
}
