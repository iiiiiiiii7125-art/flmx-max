package kernel

import "testing"

// M-0001: the catalog display name for sing-box is "sing-box" while
// its Type is "singbox" — the alias is the name every operator
// naturally types. Lookup must fold it onto the canonical type, so
// all three faces (/kernel/versions, inbound creation, Normalize)
// agree instead of failing with three different wordings.
func TestLookupFoldsSingBoxAlias(t *testing.T) {
	for _, in := range []string{"sing-box", "Sing-Box", "SING-BOX", " sing-box ", "singbox", "SingBox"} {
		info, ok := Lookup(in)
		if !ok {
			t.Fatalf("Lookup(%q) = not found, want singbox", in)
		}
		if info.Type != TypeSingbox {
			t.Fatalf("Lookup(%q) = %q, want %q", in, info.Type, TypeSingbox)
		}
	}
}

func TestLookupOtherKernelsUnaffected(t *testing.T) {
	for in, want := range map[string]string{
		"xray":  TypeXray,
		"XRAY":  TypeXray,
		"realm": TypeRealm,
	} {
		info, ok := Lookup(in)
		if !ok || info.Type != want {
			t.Fatalf("Lookup(%q) = %+v, %v, want type %q", in, info, ok, want)
		}
	}
	// The Snell kernel was removed in 2.1.0 (DEV-RM-SNELL): "snell"
	// survives only as ProtocolSnell for imported nodes, never as a
	// kernel type, so it must not resolve through Lookup.
	if _, ok := Lookup("snell"); ok {
		t.Fatal("Lookup(snell) must not match: the Snell kernel was removed")
	}
	if _, ok := Lookup("singboxx"); ok {
		t.Fatal("Lookup(singboxx) must not match")
	}
	if _, ok := Lookup(""); ok {
		t.Fatal("Lookup(\"\") must not match")
	}
	if _, ok := Lookup("not-a-kernel"); ok {
		t.Fatal("Lookup(not-a-kernel) must not match")
	}
}

func TestCanonicalKernelType(t *testing.T) {
	if got := CanonicalKernelType("sing-box"); got != TypeSingbox {
		t.Fatalf("CanonicalKernelType(sing-box) = %q, want %q", got, TypeSingbox)
	}
	if got := CanonicalKernelType("singbox"); got != TypeSingbox {
		t.Fatalf("CanonicalKernelType(singbox) = %q, want %q", got, TypeSingbox)
	}
	// Unknown names pass through normalized, so DB lookups keep
	// today's not-found behaviour.
	if got := CanonicalKernelType("No-Such-Kernel"); got != "nosuchkernel" {
		t.Fatalf("CanonicalKernelType(No-Such-Kernel) = %q, want %q", got, "nosuchkernel")
	}
}

func TestNormalizeAcceptsHyphenAlias(t *testing.T) {
	// Normalize goes through Lookup: the alias must validate end to
	// end, while a genuinely unknown kernel keeps its wording.
	if err := Normalize("sing-box", &Inbound{Protocol: "vless", Port: 12345}); err != nil {
		t.Fatalf("Normalize(sing-box) = %v, want nil (alias accepted)", err)
	}
	if err := Normalize("no-such-kernel", &Inbound{Protocol: "vless", Port: 12345}); err == nil {
		t.Fatal("Normalize(no-such-kernel) must fail")
	} else if got := err.Error(); got != "暂不支持内核 no-such-kernel" {
		t.Fatalf("Normalize(no-such-kernel) = %q, want %q", got, "暂不支持内核 no-such-kernel")
	}
}
