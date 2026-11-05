package handler

import (
	"errors"
	"testing"
)

// B3-02: 纯随机 64 试在高占用下会假报无可用端口。确定性扫描兜底
// 后：范围内只剩一个空口时必须每次都找到；全满必须报错；冲突
// 查询出错必须原样上抛（不吞成「无可用端口」）。

func TestPickFreePortWithFallbackFindsSparseFreePort(t *testing.T) {
	const freePort = 59999
	conflict := func(port int) (bool, error) {
		return port != freePort, nil
	}
	for i := 0; i < 25; i++ {
		got, err := pickFreePortWithFallback(20000, 60000, conflict)
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		if got != freePort {
			t.Fatalf("iteration %d: got %d, want the only free port %d", i, got, freePort)
		}
	}
}

func TestPickFreePortWithFallbackAllTaken(t *testing.T) {
	conflict := func(port int) (bool, error) { return true, nil }
	if _, err := pickFreePortWithFallback(20000, 60000, conflict); err == nil {
		t.Fatal("all ports taken must return an error")
	}
}

func TestPickFreePortWithFallbackPropagatesConflictError(t *testing.T) {
	sentinel := errors.New("conflict check exploded")
	conflict := func(port int) (bool, error) { return false, sentinel }
	if _, err := pickFreePortWithFallback(20000, 60000, conflict); !errors.Is(err, sentinel) {
		t.Fatalf("conflict error must propagate, got %v", err)
	}
}
