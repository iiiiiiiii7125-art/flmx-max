package handler

import "testing"

// R1-11-03 回归：渠道分类须看 GitHub Prerelease 标志——纯数字 tag
// 被标 prerelease 时不属稳定渠道（与 /releases/latest 兜底口径
// 一致），不得在 API 列表路径被当作正式版呈现。
func TestReleaseChannelHonorsPrereleaseFlag(t *testing.T) {
	preNumeric := githubRelease{TagName: "1.9.10", Prerelease: true}
	if releaseMatchesChannel(preNumeric, releaseChannelStable) {
		t.Fatal("numeric tag flagged prerelease must not match the stable channel")
	}
	if releaseMatchesChannel(preNumeric, releaseChannelDev) {
		t.Fatal("numeric tag shape must not match the dev channel either")
	}

	plain := githubRelease{TagName: "1.9.9"}
	if !releaseMatchesChannel(plain, releaseChannelStable) {
		t.Fatal("plain numeric tag must match the stable channel")
	}

	// 列表面：最新的是被标 prerelease 的数字版时，稳定渠道只剩
	// 真正的正式版。
	out := releasesForChannel([]githubRelease{preNumeric, plain}, releaseChannelStable)
	if len(out) != 1 || out[0].Version != "1.9.9" || out[0].Prerelease {
		t.Fatalf("stable releases = %+v, want only 1.9.9 non-prerelease", out)
	}

	// dev 渠道形态（B7-05 后）：带后缀的 prerelease 按形态仍归类
	// dev（releaseMatchesChannel 不变），但面板内列表面不再列出
	// dev 版本——列出也走不通（解析/执行面点名拒绝），故
	// releasesForChannel 对 dev 返回空集。
	dev := githubRelease{TagName: "2.0.0-beta1", Prerelease: true}
	if !releaseMatchesChannel(dev, releaseChannelDev) {
		t.Fatal("suffixed prerelease tag must still classify as the dev channel by shape")
	}
	outDev := releasesForChannel([]githubRelease{dev}, releaseChannelDev)
	if len(outDev) != 0 {
		t.Fatalf("dev releases = %+v, want empty listing under B7-05", outDev)
	}
}
