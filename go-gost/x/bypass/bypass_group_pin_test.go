package bypass

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/go-gost/core/bypass"
)

// R3-K1 / A02-001 回归（组合层）：生产接线恒以 BypassGroup 包装
// handler 的 Bypass（config/parsing/service parse.go 的
// handler.BypassOption(BypassGroup(List(...)))），而 bypassGroup
// 曾不实现 DialTargetPinner——Contains 在、落点钉死断裂，UDP 逐包
// 面的钉死断言恒失败并静默放行。本组测试把钉死钉在组合形态上：
// 组必须向组内 pinner 成员转发 PinDialTarget，多 pinner 语义
// fail-closed（任一拒绝即拒绝、逐个校验演进中的最终落点），组内
// 无 pinner 时目标原样通过（保持现状语义）。

type plainStubBypass struct{}

func (plainStubBypass) Contains(context.Context, string, string, ...bypass.Option) bool {
	return false
}
func (plainStubBypass) IsWhitelist() bool { return false }

type denyPinnerBypass struct{}

func (denyPinnerBypass) Contains(context.Context, string, string, ...bypass.Option) bool {
	return false
}
func (denyPinnerBypass) IsWhitelist() bool { return false }
func (denyPinnerBypass) PinDialTarget(context.Context, string, string) (string, error) {
	return "", errors.New("deny pinner: rejected")
}

// recordPinnerBypass 原样放行并记录它看到的落点，用于坐实链式
// 语义：后一个 pinner 校验的是前一个 pinner 钉死后的最终落点，
// 而不是原始主机名。
type recordPinnerBypass struct{ seen []string }

func (r *recordPinnerBypass) Contains(context.Context, string, string, ...bypass.Option) bool {
	return false
}
func (r *recordPinnerBypass) IsWhitelist() bool { return false }
func (r *recordPinnerBypass) PinDialTarget(_ context.Context, _, addr string) (string, error) {
	r.seen = append(r.seen, addr)
	return addr, nil
}

func TestBypassGroupImplementsDialTargetPinner(t *testing.T) {
	grp := BypassGroup(NewNoPrivateBypass())
	if _, ok := any(grp).(DialTargetPinner); !ok {
		t.Fatal("BypassGroup wrapping flmx-no-private must implement DialTargetPinner; " +
			"the UDP relays assert this interface on the handler bypass, which production wiring always wraps in a group")
	}
}

// TestBypassGroupPinDialTargetParityWithNaked 对同一用例表分别跑
// 裸 noPrivateBypass 与组包装形态，断言结果逐项一致（钉死值与
// 拒绝形态都一致）——组包装不得改变单体已验证的钉死行为。
func TestBypassGroupPinDialTargetParityWithNaked(t *testing.T) {
	publicLookup := func(host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("203.0.113.9"), net.ParseIP("203.0.113.10")}, nil
	}
	mixedFamilyLookup := func(host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("2001:db8::1"), net.ParseIP("203.0.113.9")}, nil
	}
	rebindLookup := func(host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("203.0.113.9"), net.ParseIP("127.0.0.1")}, nil
	}
	failLookup := func(host string) ([]net.IP, error) {
		return nil, errors.New("dns boom")
	}
	emptyLookup := func(host string) ([]net.IP, error) {
		return nil, nil
	}

	cases := []struct {
		name    string
		network string
		addr    string
		lookup  func(host string) ([]net.IP, error)
		want    string
		wantErr bool
	}{
		{"public literal", "udp", "203.0.113.9:443", nil, "203.0.113.9:443", false},
		{"loopback literal", "udp", "127.0.0.1:80", nil, "", true},
		{"rfc1918 literal", "udp", "10.0.0.5:53", nil, "", true},
		{"metadata literal", "udp", "169.254.169.254:80", nil, "", true},
		{"ipv6 loopback literal", "udp", "[::1]:53", nil, "", true},
		{"hostname pinned to first validated IP", "udp", "example.com:443", publicLookup, "203.0.113.9:443", false},
		{"rebinding answer set rejected", "udp", "rebind.test:80", rebindLookup, "", true},
		{"resolution failure closed", "udp", "gone.test:80", failLookup, "", true},
		{"empty answer set closed", "udp", "empty.test:80", emptyLookup, "", true},
		{"udp4 picks v4 answer", "udp4", "example.com:443", mixedFamilyLookup, "203.0.113.9:443", false},
		{"udp6 picks v6 answer", "udp6", "example.com:443", mixedFamilyLookup, "[2001:db8::1]:443", false},
		{"empty host passes through", "udp", "", nil, "", false},
	}

	naked, ok := any(NewNoPrivateBypass()).(DialTargetPinner)
	if !ok {
		t.Fatal("naked noPrivateBypass must implement DialTargetPinner")
	}
	grouped, ok := any(BypassGroup(NewNoPrivateBypass())).(DialTargetPinner)
	if !ok {
		t.Fatal("BypassGroup(noPrivateBypass) must implement DialTargetPinner")
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.lookup != nil {
				stubLookup(t, tc.lookup)
			}
			ctx := context.Background()
			nakedGot, nakedErr := naked.PinDialTarget(ctx, tc.network, tc.addr)
			groupGot, groupErr := grouped.PinDialTarget(ctx, tc.network, tc.addr)
			if (nakedErr != nil) != tc.wantErr {
				t.Fatalf("naked pin(%q): err = %v, wantErr %v", tc.addr, nakedErr, tc.wantErr)
			}
			if (groupErr != nil) != tc.wantErr {
				t.Fatalf("group pin(%q): err = %v, wantErr %v", tc.addr, groupErr, tc.wantErr)
			}
			if !tc.wantErr && groupGot != tc.want {
				t.Fatalf("group pin(%q) = %q, want %q", tc.addr, groupGot, tc.want)
			}
			if groupGot != nakedGot || (groupErr != nil) != (nakedErr != nil) {
				t.Fatalf("group/naked divergence on %q: group = %q, %v; naked = %q, %v",
					tc.addr, groupGot, groupErr, nakedGot, nakedErr)
			}
		})
	}
}

func TestBypassGroupPinDialTargetFailClosedAcrossMembers(t *testing.T) {
	ctx := context.Background()
	stubLookup(t, func(host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("203.0.113.9")}, nil
	})

	// 任一 pinner 成员拒绝即整组拒绝，与成员顺序无关。
	for name, grp := range map[string]bypass.Bypass{
		"pinner first": BypassGroup(NewNoPrivateBypass(), denyPinnerBypass{}),
		"pinner last":  BypassGroup(denyPinnerBypass{}, NewNoPrivateBypass()),
	} {
		pinner, ok := any(grp).(DialTargetPinner)
		if !ok {
			t.Fatalf("%s: group must implement DialTargetPinner", name)
		}
		if _, err := pinner.PinDialTarget(ctx, "udp", "example.com:443"); err == nil {
			t.Fatalf("%s: a rejecting member must fail the whole group pin", name)
		}
		// 字面量同样要过全部 pinner：拒绝成员对字面量也拒绝。
		if _, err := pinner.PinDialTarget(ctx, "udp", "203.0.113.9:443"); err == nil {
			t.Fatalf("%s: literal target must also be rejected when a member rejects", name)
		}
	}

	// 链式校验：第二个 pinner 看到的是第一个钉死后的字面量。
	rec := &recordPinnerBypass{}
	grp := BypassGroup(NewNoPrivateBypass(), rec)
	pinner := any(grp).(DialTargetPinner)
	got, err := pinner.PinDialTarget(ctx, "udp", "example.com:443")
	if err != nil {
		t.Fatalf("chained pin: %v", err)
	}
	if got != "203.0.113.9:443" {
		t.Fatalf("chained pin = %q, want 203.0.113.9:443", got)
	}
	if len(rec.seen) != 1 || rec.seen[0] != "203.0.113.9:443" {
		t.Fatalf("second pinner must validate the pinned literal, saw %v", rec.seen)
	}
}

func TestBypassGroupPinDialTargetNoPinnerPassesThrough(t *testing.T) {
	ctx := context.Background()
	for name, grp := range map[string]bypass.Bypass{
		"empty group":       BypassGroup(),
		"non-pinner member": BypassGroup(plainStubBypass{}),
		"nil member":        BypassGroup(nil),
	} {
		pinner, ok := any(grp).(DialTargetPinner)
		if !ok {
			t.Fatalf("%s: group type must always implement DialTargetPinner", name)
		}
		got, err := pinner.PinDialTarget(ctx, "udp", "target.test:53")
		if err != nil || got != "target.test:53" {
			t.Fatalf("%s: no pinner in group must pass target through unchanged, got %q err %v", name, got, err)
		}
	}

	// 非 pinner 成员与 nil 成员不得拖累组内 pinner 的钉死。
	grp := BypassGroup(nil, plainStubBypass{}, NewNoPrivateBypass())
	pinner := any(grp).(DialTargetPinner)
	if _, err := pinner.PinDialTarget(ctx, "udp", "192.168.0.8:443"); err == nil {
		t.Fatal("private literal must be rejected even with inert members around the pinner")
	}
	got, err := pinner.PinDialTarget(ctx, "udp", "203.0.113.9:443")
	if err != nil || got != "203.0.113.9:443" {
		t.Fatalf("public literal through mixed group: got %q err %v", got, err)
	}
}
