package handler

import (
	"errors"
	"testing"
)

// B7-05: dev 通道在面板内路径的口径一致化——解析点点名拒绝、
// 列表面不列; 正式版通道行为不变。

func TestB705DevChannelRejectedAtResolvers(t *testing.T) {
	if _, err := resolveLatestReleaseByChannel("dev"); !errors.Is(err, errDevChannelNotSupported) {
		t.Fatalf("node resolver must reject dev by name, got %v", err)
	}
	h := &Handler{}
	if _, err := h.resolveSystemUpgradeLatestReleaseByChannel("dev"); !errors.Is(err, errDevChannelNotSupported) {
		t.Fatalf("panel resolver must reject dev by name, got %v", err)
	}
}

func TestB705DevReleasesNotListed(t *testing.T) {
	releases := []githubRelease{
		{TagName: "1.9.10"},
		{TagName: "1.9.11-beta1", Prerelease: true},
	}
	if got := releasesForChannel(releases, "dev"); len(got) != 0 {
		t.Fatalf("dev channel must list nothing on the in-panel surface, got %+v", got)
	}
	got := releasesForChannel(releases, "stable")
	if len(got) != 1 || got[0].Version != "1.9.10" {
		t.Fatalf("stable listing must be unchanged, got %+v", got)
	}
}
