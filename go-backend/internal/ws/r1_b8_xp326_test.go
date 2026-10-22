package ws

import (
	"context"
	"errors"
	"testing"
	"time"
)

// X-P3-26 (L-10-2): SendCommand 加 ctx 变体——ctx 已取消时立即以
// ctx 错误返回，不再等节点响应/超时。旧实现无 ctx 入口，批量循环
// 断连后仍逐个下发全部节点。
func TestR1B8_XP326_SendCommandContextCancelled(t *testing.T) {
	server := NewServer(nil, fix132Secret)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := server.SendCommandContext(ctx, 42, "TcpPing", nil, 30*time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("已取消 ctx 应立即返回 context.Canceled, got %v", err)
	}
	// 防过修：未取消 ctx 走原路径（节点不在线错误）。
	_, err = server.SendCommandContext(context.Background(), 42, "TcpPing", nil, time.Second)
	if err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("未取消 ctx 应走原下发路径并报节点不在线, got %v", err)
	}
}
