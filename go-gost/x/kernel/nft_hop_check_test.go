package kernel

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"
)

// stubHopCheck replaces the per-hop-rule validator with a fake: rules whose
// rendered expression contains any of rejectSubstr are rejected the way a
// kernel without nft_fib would reject them.
func stubHopCheck(t *testing.T, rejectSubstr ...string) {
	t.Helper()
	old := nftHopCheck
	nftHopCheck = func(_ context.Context, h hopRule) error {
		for _, sub := range rejectSubstr {
			if strings.Contains(h.expr(), sub) {
				return errors.New("nft: fake kernel rejected the rule")
			}
		}
		return nil
	}
	t.Cleanup(func() { nftHopCheck = old })
}

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(old) })
	return &buf
}

func hopPorts() []Port {
	return []Port{
		{InboundID: 3, Port: 443, Network: "udp", HopFrom: 20000, HopTo: 30000},
		{InboundID: 4, Port: 8443, Network: "udp", HopFrom: 40000, HopTo: 50000},
	}
}

// F-091 补强①：一条 hop 规则被内核拒绝时只跳过该条，其余 hop 与计数
// 规则照常下发，整体成功并留下可定位的 warning。
func TestSyncSkipsRejectedHopRule(t *testing.T) {
	rec := stubNft(t)
	stubHopCheck(t, "40000")
	logs := captureLog(t)

	n := newNftCounters(t.TempDir())
	if err := n.sync(hopPorts(), nil); err != nil {
		t.Fatalf("sync must succeed when only one hop rule is rejected: %v", err)
	}
	if len(rec.scripts) != 1 {
		t.Fatalf("expected exactly one applied script, got %d", len(rec.scripts))
	}
	script := rec.scripts[0]
	if !strings.Contains(script, "fib daddr type local udp dport 20000-30000 redirect to :443") {
		t.Fatalf("the valid hop rule must still be applied:\n%s", script)
	}
	if strings.Contains(script, "40000-50000") {
		t.Fatalf("the rejected hop rule must be skipped:\n%s", script)
	}
	out := logs.String()
	for _, want := range []string{"WARN: hop rule skipped", "40000-50000", "fib daddr type local"} {
		if !strings.Contains(out, want) {
			t.Fatalf("warning log must contain %q (rule id + expression), got:\n%s", want, out)
		}
	}
}

// F-091 补强②：全部 hop 规则被拒时，计数规则仍须下发（失败隔离），且
// 返回的 error 必须逐条点名规则标识与表达式，不许只回一句语法错误。
func TestSyncAllHopRulesRejected(t *testing.T) {
	rec := stubNft(t)
	stubHopCheck(t, "fib") // every hop rule rejected
	captureLog(t)

	n := newNftCounters(t.TempDir())
	err := n.sync(hopPorts(), nil)
	if err == nil {
		t.Fatal("sync must report an error when every hop rule is rejected")
	}
	for _, want := range []string{"hop[0]", "20000-30000", "hop[1]", "40000-50000", "fib daddr type local"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error must identify each rejected rule (%q missing): %v", want, err)
		}
	}
	if len(rec.scripts) != 1 {
		t.Fatalf("counter rules must still be applied exactly once, got %d scripts", len(rec.scripts))
	}
	script := rec.scripts[0]
	if !strings.Contains(script, "counter in_3_udp") {
		t.Fatalf("counter rules must survive hop rejection:\n%s", script)
	}
	if strings.Contains(script, "hop_") {
		t.Fatalf("no hop chains may be applied when all hops are rejected:\n%s", script)
	}
}

// F-091 补强③：全部 hop 规则通过校验时全部下发，且不产生 warning。
func TestSyncAllHopRulesAccepted(t *testing.T) {
	rec := stubNft(t)
	stubHopCheck(t) // rejects nothing
	logs := captureLog(t)

	n := newNftCounters(t.TempDir())
	if err := n.sync(hopPorts(), nil); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if len(rec.scripts) != 1 {
		t.Fatalf("expected exactly one applied script, got %d", len(rec.scripts))
	}
	script := rec.scripts[0]
	for _, want := range []string{"20000-30000 redirect to :443", "40000-50000 redirect to :8443"} {
		if !strings.Contains(script, want) {
			t.Fatalf("all valid hop rules must be applied, missing %q:\n%s", want, script)
		}
	}
	if strings.Contains(logs.String(), "WARN: hop rule skipped") {
		t.Fatalf("no warning expected when nothing is skipped, got:\n%s", logs.String())
	}
}

// 生产校验器接线：nftHopCheck 必须用 `nft -c` 只校验含单条规则的最小
// 脚本，并原样向上传播 nft 的错误。
func TestNftHopCheckUsesNftCheckOnSingleRule(t *testing.T) {
	var gotArgs []string
	var gotScript string
	old := nftRunner
	nftRunner = func(_ context.Context, stdin string, args ...string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		gotScript = stdin
		return nil, errors.New("nft: Error: No such file or directory")
	}
	t.Cleanup(func() { nftRunner = old })

	h := hopRule{From: 20000, To: 30000, Port: 443}
	err := nftHopCheck(context.Background(), h)
	if err == nil || !strings.Contains(err.Error(), "No such file or directory") {
		t.Fatalf("checker must propagate the nft error, got: %v", err)
	}
	if strings.Join(gotArgs, " ") != "-c -f -" {
		t.Fatalf("checker must run nft -c, got args %v", gotArgs)
	}
	if !strings.Contains(gotScript, h.expr()) {
		t.Fatalf("probe script must contain the rule expression %q:\n%s", h.expr(), gotScript)
	}
	if strings.Count(gotScript, "redirect") != 2 {
		t.Fatalf("probe covers both hop hooks exactly once, got:\n%s", gotScript)
	}
}
