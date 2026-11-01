package handler

import (
	"reflect"
	"testing"
)

// LB-F-P3-1: resources 透传归一化须同时补 handler 侧 bind——
// 对端 <1.7.7 生成或手工构造的 relay 服务缺 bind 时，经
// applyPeerShareResource 下发后 SOCKS5-associate 会被 x 侧逐跳
// 门控拒掉。此面归一在 normalizePeerServiceUDPListenerMetadata
// 收口内、listener 门之前执行，与 listener 类型无关。

func TestKU11NormalizePeerServiceRelayHandlerBind(t *testing.T) {
	t.Run("relay+udp 双缺：bind 补齐且 listener 归一化不回退", func(t *testing.T) {
		config := map[string]interface{}{
			"handler":  map[string]interface{}{"type": "relay"},
			"listener": map[string]interface{}{"type": "udp"},
		}
		normalizePeerServiceUDPListenerMetadata(config)
		handler, _ := config["handler"].(map[string]interface{})
		hmeta, _ := handler["metadata"].(map[string]interface{})
		if hmeta["bind"] != true {
			t.Fatalf("handler metadata = %v, want bind=true", hmeta)
		}
		listener, _ := config["listener"].(map[string]interface{})
		lmeta, _ := listener["metadata"].(map[string]interface{})
		if lmeta["keepAlive"] != true || lmeta["ttl"] != "30s" {
			t.Fatalf("listener metadata = %v, want keepAlive=true ttl=30s (KU1 归一化不得回退)", lmeta)
		}
	})

	t.Run("relay+tcp listener：bind 与监听协议无关、listener 不被触碰", func(t *testing.T) {
		config := map[string]interface{}{
			"handler":  map[string]interface{}{"type": "relay"},
			"listener": map[string]interface{}{"type": "tcp"},
		}
		normalizePeerServiceUDPListenerMetadata(config)
		handler, _ := config["handler"].(map[string]interface{})
		hmeta, _ := handler["metadata"].(map[string]interface{})
		if hmeta["bind"] != true {
			t.Fatalf("handler metadata = %v, want bind=true", hmeta)
		}
		listener, _ := config["listener"].(map[string]interface{})
		if _, ok := listener["metadata"]; ok {
			t.Fatalf("tcp listener must stay without metadata, got %v", listener)
		}
	})

	t.Run("relay 无 listener 段：bind 仍补齐", func(t *testing.T) {
		config := map[string]interface{}{
			"handler": map[string]interface{}{"type": "relay"},
		}
		normalizePeerServiceUDPListenerMetadata(config)
		handler, _ := config["handler"].(map[string]interface{})
		hmeta, _ := handler["metadata"].(map[string]interface{})
		if hmeta["bind"] != true {
			t.Fatalf("handler metadata = %v, want bind=true", hmeta)
		}
	})

	t.Run("relay 已有 metadata 缺 bind：合并保留其他键", func(t *testing.T) {
		config := map[string]interface{}{
			"handler": map[string]interface{}{"type": "relay", "metadata": map[string]interface{}{
				"mux.keepaliveInterval": "10s",
				"readTimeout":           "20s",
			}},
			"listener": map[string]interface{}{"type": "udp", "metadata": map[string]interface{}{"keepAlive": true, "ttl": "30s"}},
		}
		normalizePeerServiceUDPListenerMetadata(config)
		handler, _ := config["handler"].(map[string]interface{})
		hmeta, _ := handler["metadata"].(map[string]interface{})
		want := map[string]interface{}{"bind": true, "mux.keepaliveInterval": "10s", "readTimeout": "20s"}
		if !reflect.DeepEqual(hmeta, want) {
			t.Fatalf("handler metadata = %v, want %v", hmeta, want)
		}
	})

	t.Run("relay 已有 bind：显式值不动", func(t *testing.T) {
		for _, explicit := range []interface{}{true, false} {
			config := map[string]interface{}{
				"handler": map[string]interface{}{"type": "relay", "metadata": map[string]interface{}{"bind": explicit, "nodelay": true}},
				"listener": map[string]interface{}{"type": "udp", "metadata": map[string]interface{}{"keepAlive": true, "ttl": "30s"}},
			}
			normalizePeerServiceUDPListenerMetadata(config)
			handler, _ := config["handler"].(map[string]interface{})
			hmeta, _ := handler["metadata"].(map[string]interface{})
			want := map[string]interface{}{"bind": explicit, "nodelay": true}
			if !reflect.DeepEqual(hmeta, want) {
				t.Fatalf("explicit bind=%v: handler metadata = %v, want %v", explicit, hmeta, want)
			}
		}
	})

	t.Run("relay 类型大小写形态：同样补齐", func(t *testing.T) {
		config := map[string]interface{}{
			"handler":  map[string]interface{}{"type": "Relay"},
			"listener": map[string]interface{}{"type": "tcp"},
		}
		normalizePeerServiceUDPListenerMetadata(config)
		handler, _ := config["handler"].(map[string]interface{})
		hmeta, _ := handler["metadata"].(map[string]interface{})
		if hmeta["bind"] != true {
			t.Fatalf("handler metadata = %v, want bind=true", hmeta)
		}
	})

	t.Run("非 relay handler：handler 侧逐字不变（listener 归一化照旧）", func(t *testing.T) {
		config := map[string]interface{}{
			"handler":  map[string]interface{}{"type": "forward"},
			"listener": map[string]interface{}{"type": "udp"},
		}
		normalizePeerServiceUDPListenerMetadata(config)
		handler, _ := config["handler"].(map[string]interface{})
		if _, ok := handler["metadata"]; ok {
			t.Fatalf("non-relay handler must not gain metadata, got %v", handler)
		}
		listener, _ := config["listener"].(map[string]interface{})
		lmeta, _ := listener["metadata"].(map[string]interface{})
		if lmeta["keepAlive"] != true || lmeta["ttl"] != "30s" {
			t.Fatalf("listener metadata = %v, want KU1 forward+udp 归一化照旧", lmeta)
		}
	})
}

// 非 relay 服务（listener 归一化亦不适用的形态）经收口后逐字一致。
func TestKU11NormalizeNonRelayServiceVerbatim(t *testing.T) {
	cases := []struct {
		name   string
		config map[string]interface{}
	}{
		{
			name: "forward+tcp 带 metadata",
			config: map[string]interface{}{
				"name":     "svc-a",
				"handler":  map[string]interface{}{"type": "forward", "metadata": map[string]interface{}{"proxyProtocol": int64(1)}},
				"listener": map[string]interface{}{"type": "tcp", "metadata": map[string]interface{}{"mptcp": false}},
			},
		},
		{
			name: "tcp handler 无 metadata",
			config: map[string]interface{}{
				"name":     "svc-b",
				"handler":  map[string]interface{}{"type": "tcp"},
				"listener": map[string]interface{}{"type": "tcp"},
			},
		},
		{
			name: "无 handler 段",
			config: map[string]interface{}{
				"name":     "svc-c",
				"listener": map[string]interface{}{"type": "tcp"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := deepCopyKU11(t, tc.config)
			normalizePeerServiceUDPListenerMetadata(tc.config)
			if !reflect.DeepEqual(tc.config, before) {
				t.Fatalf("config mutated: got %v, want verbatim %v", tc.config, before)
			}
		})
	}
}

func deepCopyKU11(t *testing.T, v map[string]interface{}) map[string]interface{} {
	t.Helper()
	out := make(map[string]interface{}, len(v))
	for key, child := range v {
		if nested, ok := child.(map[string]interface{}); ok {
			out[key] = deepCopyKU11(t, nested)
			continue
		}
		out[key] = child
	}
	return out
}
