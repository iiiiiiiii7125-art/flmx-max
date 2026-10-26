package handler

import "testing"

// PORT-CLOSURE 封口修复批 Lane A（基线 main @ c6e90c7）。
// PC-03（P2，F-03）：processServerAddress 把 "2001:db8::1:443"（裸 IPv6 +
// Sprintf 拼端口）当合法 IPv6 字面量，返回 "[2001:db8::1:443]"——端口被折进
// 地址、结果无端口分量。期望：按 host:port 切分 → "[2001:db8::1]:443"。
// PC-06（P3，F-06）：parseTargetAddress 回退分支静默误切双端口目标
// （"203.0.113.7:8080:9090" → host "203.0.113.7:8080"）；期望：报格式错误。
// 无括号 IPv6 回退（"2001:db8::1:443" → host "2001:db8::1"，port 443）须保留。

func TestPortClosurePC03ProcessServerAddressBareIPv6Port(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "bare ipv6 plus sprintf port splits host port",
			in:   "2001:db8::1:443",
			want: "[2001:db8::1]:443",
		},
		{
			name: "already bracketed host port unchanged",
			in:   "[2001:db8::1]:443",
			want: "[2001:db8::1]:443",
		},
		{
			name: "bare ipv6 host only bracketed",
			in:   "2001:db8::1",
			want: "[2001:db8::1]",
		},
		{
			name: "bracketed wildcard with port unchanged",
			in:   "[::]:443",
			want: "[::]:443",
		},
		{
			name: "bare ipv6 ending in decimal group is not a port",
			in:   "2001:db8::443",
			want: "[2001:db8::443]",
		},
		{
			name: "ipv4 host port unchanged",
			in:   "203.0.113.7:8080",
			want: "203.0.113.7:8080",
		},
		{
			name: "loopback ipv6 host only bracketed",
			in:   "::1",
			want: "[::1]",
		},
	}

	for _, tt := range tests {
		if got := processServerAddress(tt.in); got != tt.want {
			t.Fatalf("%s: processServerAddress(%q) = %q, want %q", tt.name, tt.in, got, tt.want)
		}
	}
}

func TestPortClosureC02ParseTargetDoublePort(t *testing.T) {
	// 双端口目标必须报格式错误，不得静默误切出 host "203.0.113.7:8080"。
	if host, port, err := parseTargetAddress("203.0.113.7:8080:9090"); err == nil {
		t.Fatalf("double-port target: expected error, got host %q port %d", host, port)
	}
	if host, port, err := parseTargetAddress("example.com:8080:9090"); err == nil {
		t.Fatalf("double-port hostname target: expected error, got host %q port %d", host, port)
	}

	// 对照：单端口目标正常。
	if host, port, err := parseTargetAddress("203.0.113.7:8080"); err != nil || host != "203.0.113.7" || port != 8080 {
		t.Fatalf("single-port target: got host %q port %d err %v, want host %q port %d", host, port, err, "203.0.113.7", 8080)
	}

	// 对照：无括号 IPv6 回退完整保留（SplitHostPort 失败 → LastIndex 切分）。
	if host, port, err := parseTargetAddress("2001:db8::1:443"); err != nil || host != "2001:db8::1" || port != 443 {
		t.Fatalf("bare ipv6 target: got host %q port %d err %v, want host %q port %d", host, port, err, "2001:db8::1", 443)
	}
	if host, port, err := parseTargetAddress("[2001:db8::1]:443"); err != nil || host != "2001:db8::1" || port != 443 {
		t.Fatalf("bracketed ipv6 target: got host %q port %d err %v, want host %q port %d", host, port, err, "2001:db8::1", 443)
	}

	// 消费面：诊断双端口 RemoteAddr → “目标地址格式错误”。
	if _, err := resolveDiagnosisTargets("203.0.113.7:8080:9090"); err == nil {
		t.Fatalf("resolveDiagnosisTargets double-port: expected error, got nil")
	}

	// 消费面对照：无括号 IPv6 目标诊断正常（需 PC-03 先把
	// "2001:db8::1:443" 规范化为 "[2001:db8::1]:443"）。
	targets, err := resolveDiagnosisTargets("2001:db8::1:443")
	if err != nil {
		t.Fatalf("resolveDiagnosisTargets bare ipv6: unexpected error %v", err)
	}
	if len(targets) != 1 || targets[0].IP != "2001:db8::1" || targets[0].Port != 443 {
		t.Fatalf("resolveDiagnosisTargets bare ipv6: got %+v, want IP %q Port %d", targets, "2001:db8::1", 443)
	}
}
