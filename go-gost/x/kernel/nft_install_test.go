package kernel

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// nftables 自动安装测试。安装器本身（发行版→命令、失败明错）与
// sync 钩子（白名单在场才装、装不上 fail-closed）分开钉死；测试
// 只碰桩变量，永不执行真实包管理器。

// installStub 是 nft_install_test 的桩集合：os-release 内容、nft
// 是否在场（安装命令可将其翻为在场，模拟二进制出现）、包管理器
// 命令记录、dnf/yum 有无。
type installStub struct {
	avail    bool
	calls    []string
	lookPath map[string]error
	runErr   error // installRunner 固定返回的错误（nil=成功）
}

func stubNftInstall(t *testing.T, osRelease string, s *installStub) {
	t.Helper()
	dir := t.TempDir()
	rel := filepath.Join(dir, "os-release")
	if err := os.WriteFile(rel, []byte(osRelease), 0o644); err != nil {
		t.Fatal(err)
	}
	oldPath, oldAvail, oldRunner, oldLook := osReleasePath, nftAvailable, installRunner, packageManagerLookPath
	osReleasePath = rel
	nftAvailable = func() error {
		if s.avail {
			return nil
		}
		return errors.New("nft 不存在 (stub)")
	}
	installRunner = func(_ context.Context, name string, args ...string) ([]byte, error) {
		s.calls = append(s.calls, name+" "+strings.Join(args, " "))
		if s.runErr != nil {
			return nil, s.runErr
		}
		// 安装命令成功即模拟 nft 二进制出现。
		if (name == "apt-get" && len(args) > 0 && args[0] == "install") ||
			(name == "dnf" || name == "yum") ||
			(name == "apk" && len(args) > 0 && args[0] == "add") {
			s.avail = true
		}
		return []byte("ok"), nil
	}
	packageManagerLookPath = func(name string) (string, error) {
		if err, ok := s.lookPath[name]; ok {
			return "/usr/bin/" + name, err
		}
		return "/usr/bin/" + name, nil
	}
	t.Cleanup(func() {
		osReleasePath, nftAvailable, installRunner, packageManagerLookPath = oldPath, oldAvail, oldRunner, oldLook
	})
}

func TestDetectDistroFamily(t *testing.T) {
	cases := []struct {
		name    string
		release string
		want    string
	}{
		{"debian", "ID=debian\n", "apt"},
		{"ubuntu", "ID=ubuntu\nID_LIKE=debian\n", "apt"},
		{"ubuntu-like", "ID=pop\nID_LIKE=ubuntu debian\n", "apt"},
		{"centos", "ID=centos\nID_LIKE=\"rhel fedora\"\n", "dnf"},
		{"rhel", "ID=rhel\n", "dnf"},
		{"fedora", "ID=fedora\n", "dnf"},
		{"rocky", "ID=rocky\nID_LIKE=\"rhel centos fedora\"\n", "dnf"},
		{"alpine", "ID=alpine\n", "apk"},
		{"arch-unsupported", "ID=arch\n", ""},
		{"empty", "", ""},
		{"garbage", "FOO=bar\n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			rel := filepath.Join(dir, "os-release")
			if err := os.WriteFile(rel, []byte(tc.release), 0o644); err != nil {
				t.Fatal(err)
			}
			old := osReleasePath
			osReleasePath = rel
			t.Cleanup(func() { osReleasePath = old })
			if got := detectDistroFamily(); got != tc.want {
				t.Fatalf("detectDistroFamily() = %q, want %q (release=%q)", got, tc.want, tc.release)
			}
		})
	}
}

func TestNftEnsureInstalledAlreadyPresent(t *testing.T) {
	s := &installStub{avail: true}
	stubNftInstall(t, "ID=debian\n", s)
	installed, err := nftEnsureInstalled(context.Background())
	if err != nil || installed {
		t.Fatalf("already present: installed=%v err=%v, want (false, nil)", installed, err)
	}
	if len(s.calls) != 0 {
		t.Fatalf("already present must not invoke the package manager, got %v", s.calls)
	}
}

func TestNftEnsureInstalledApt(t *testing.T) {
	s := &installStub{}
	stubNftInstall(t, "ID=ubuntu\nID_LIKE=debian\n", s)
	installed, err := nftEnsureInstalled(context.Background())
	if err != nil || !installed {
		t.Fatalf("apt install: installed=%v err=%v, want (true, nil)", installed, err)
	}
	want := []string{"apt-get update", "apt-get install -y nftables"}
	if !equalStrings(s.calls, want) {
		t.Fatalf("apt commands = %v, want %v", s.calls, want)
	}
}

func TestNftEnsureInstalledAptUpdateFailureTolerated(t *testing.T) {
	s := &installStub{}
	stubNftInstall(t, "ID=debian\n", s)
	// update 失败、install 成功：仍算装好（失败原因以 install 为准）。
	orig := installRunner
	installRunner = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "apt-get" && len(args) > 0 && args[0] == "update" {
			return nil, errors.New("apt-get update: 网络不可达 (stub)")
		}
		return orig(ctx, name, args...)
	}
	installed, err := nftEnsureInstalled(context.Background())
	if err != nil || !installed {
		t.Fatalf("update-fails-but-install-ok: installed=%v err=%v, want (true, nil)", installed, err)
	}
}

func TestNftEnsureInstalledDnfAndYumFallback(t *testing.T) {
	t.Run("dnf", func(t *testing.T) {
		s := &installStub{}
		stubNftInstall(t, "ID=fedora\n", s)
		installed, err := nftEnsureInstalled(context.Background())
		if err != nil || !installed {
			t.Fatalf("dnf install: installed=%v err=%v", installed, err)
		}
		if !equalStrings(s.calls, []string{"dnf install -y nftables"}) {
			t.Fatalf("dnf commands = %v", s.calls)
		}
	})
	t.Run("yum-fallback", func(t *testing.T) {
		s := &installStub{lookPath: map[string]error{"dnf": errors.New("no dnf")}}
		stubNftInstall(t, "ID=centos\nID_LIKE=\"rhel fedora\"\n", s)
		installed, err := nftEnsureInstalled(context.Background())
		if err != nil || !installed {
			t.Fatalf("yum fallback: installed=%v err=%v", installed, err)
		}
		if !equalStrings(s.calls, []string{"yum install -y nftables"}) {
			t.Fatalf("yum commands = %v", s.calls)
		}
	})
	t.Run("neither", func(t *testing.T) {
		s := &installStub{lookPath: map[string]error{
			"dnf": errors.New("no dnf"), "yum": errors.New("no yum"),
		}}
		stubNftInstall(t, "ID=rhel\n", s)
		_, err := nftEnsureInstalled(context.Background())
		if err == nil || !strings.Contains(err.Error(), "nftables") || !strings.Contains(err.Error(), "dnf/yum") {
			t.Fatalf("no package manager must fail naming nftables + dnf/yum, got %v", err)
		}
		if len(s.calls) != 0 {
			t.Fatalf("no package manager must run nothing, got %v", s.calls)
		}
	})
}

func TestNftEnsureInstalledApk(t *testing.T) {
	s := &installStub{}
	stubNftInstall(t, "ID=alpine\n", s)
	installed, err := nftEnsureInstalled(context.Background())
	if err != nil || !installed {
		t.Fatalf("apk install: installed=%v err=%v", installed, err)
	}
	if !equalStrings(s.calls, []string{"apk update", "apk add nftables"}) {
		t.Fatalf("apk commands = %v", s.calls)
	}
}

func TestNftEnsureInstalledUnknownDistro(t *testing.T) {
	s := &installStub{}
	stubNftInstall(t, "ID=arch\n", s)
	_, err := nftEnsureInstalled(context.Background())
	if err == nil || !strings.Contains(err.Error(), "nftables") || !strings.Contains(err.Error(), "未知发行版") {
		t.Fatalf("unknown distro must fail naming nftables + 未知发行版, got %v", err)
	}
	if len(s.calls) != 0 {
		t.Fatalf("unknown distro must run nothing, got %v", s.calls)
	}
}

func TestNftEnsureInstalledOsReleaseMissing(t *testing.T) {
	s := &installStub{}
	stubNftInstall(t, "ID=debian\n", s)
	osReleasePath = filepath.Join(t.TempDir(), "no-such-os-release")
	_, err := nftEnsureInstalled(context.Background())
	if err == nil || !strings.Contains(err.Error(), "nftables") || !strings.Contains(err.Error(), "未知发行版") {
		t.Fatalf("missing os-release must fail naming nftables + 未知发行版, got %v", err)
	}
}

func TestNftEnsureInstalledInstallFailure(t *testing.T) {
	s := &installStub{runErr: errors.New("E: Unable to fetch some archives (stub)")}
	stubNftInstall(t, "ID=debian\n", s)
	installed, err := nftEnsureInstalled(context.Background())
	if installed || err == nil {
		t.Fatalf("install failure: installed=%v err=%v, want (false, error)", installed, err)
	}
	if !strings.Contains(err.Error(), "nftables 自动安装失败") || !strings.Contains(err.Error(), "Unable to fetch") {
		t.Fatalf("install failure must name nftables + the cause, got %v", err)
	}
}

func TestNftEnsureInstalledStillMissingAfterInstall(t *testing.T) {
	s := &installStub{}
	stubNftInstall(t, "ID=debian\n", s)
	// 安装命令“成功”但 nft 仍不可用：模拟包管理器谎报成功。
	orig := installRunner
	installRunner = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		s.calls = append(s.calls, name+" "+strings.Join(args, " "))
		return []byte("ok"), nil // 不翻 s.avail
	}
	_ = orig
	_, err := nftEnsureInstalled(context.Background())
	if err == nil || !strings.Contains(err.Error(), "仍不可用") {
		t.Fatalf("installed-but-still-missing must fail naming 仍不可用, got %v", err)
	}
}

// stubSyncNft 桩住 ruleset 下发面（-f - 收脚本、list table 回显），
// 供 sync 钩子测试用；installStub 负责安装面。
func stubSyncNft(t *testing.T, s *installStub) *[]string {
	t.Helper()
	var scripts []string
	oldRunner := nftRunner
	nftRunner = func(_ context.Context, stdin string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case joined == "-f -":
			scripts = append(scripts, stdin)
			return nil, nil
		case strings.HasPrefix(joined, "list table"):
			if len(scripts) == 0 {
				return nil, errors.New("no table (stub)")
			}
			return []byte("table inet flmx_kernel {}"), nil
		case strings.HasPrefix(joined, "-j list counters"):
			return []byte(`{"nftables":[]}`), nil
		}
		return nil, fmt.Errorf("unexpected nft call %q", joined)
	}
	t.Cleanup(func() { nftRunner = oldRunner })
	return &scripts
}

func TestNftSyncAutoInstallsForWhitelist(t *testing.T) {
	s := &installStub{}
	stubNftInstall(t, "ID=debian\n", s)
	scripts := stubSyncNft(t, s)
	n := newNftCounters(t.TempDir())
	ports := []Port{{InboundID: 7, Port: 443, Network: "tcp", Allow: []string{"203.0.113.0/24"}}}
	if err := n.sync(ports, nil); err != nil {
		t.Fatalf("sync with auto-installable nft must succeed, got %v", err)
	}
	if !s.avail {
		t.Fatal("sync must have attempted the install")
	}
	auto, ierr := n.installStatus()
	if !auto || ierr != "" {
		t.Fatalf("installStatus = (%v, %q), want (true, \"\")", auto, ierr)
	}
	if len(*scripts) == 0 {
		t.Fatal("ruleset must have been applied after the install")
	}
}

func TestNftSyncInstallFailureFailsClosed(t *testing.T) {
	s := &installStub{runErr: errors.New("E: Unable to fetch some archives (stub)")}
	stubNftInstall(t, "ID=debian\n", s)
	scripts := stubSyncNft(t, s)
	n := newNftCounters(t.TempDir())
	ports := []Port{{InboundID: 7, Port: 443, Network: "tcp", Allow: []string{"203.0.113.0/24"}}}
	err := n.sync(ports, nil)
	if err == nil || !strings.Contains(err.Error(), "nftables 自动安装失败") || !strings.Contains(err.Error(), "Unable to fetch") {
		t.Fatalf("sync with failing install must fail naming nftables + cause, got %v", err)
	}
	auto, ierr := n.installStatus()
	if auto || ierr == "" {
		t.Fatalf("installStatus = (%v, %q), want (false, non-empty)", auto, ierr)
	}
	if len(*scripts) != 0 {
		t.Fatal("no ruleset may be applied when the install failed")
	}
}

func TestNftSyncNoWhitelistDoesNotInstall(t *testing.T) {
	s := &installStub{}
	stubNftInstall(t, "ID=debian\n", s)
	stubSyncNft(t, s)
	n := newNftCounters(t.TempDir())
	ports := []Port{{InboundID: 7, Port: 443, Network: "tcp"}}
	if err := n.sync(ports, nil); err != nil {
		t.Fatalf("sync without whitelist must stay best-effort, got %v", err)
	}
	if len(s.calls) != 0 {
		t.Fatalf("no whitelist must never invoke the package manager, got %v", s.calls)
	}
}

func TestManagerApplyWhitelistAutoInstallsNft(t *testing.T) {
	s := &installStub{}
	stubNftInstall(t, "ID=debian\n", s)
	scripts := stubSyncNft(t, s)
	m := NewManager(t.TempDir())
	t.Cleanup(m.Shutdown)
	marker := filepath.Join(t.TempDir(), "starts")
	placeMarkerKernel(t, m, "xray", "9.9.9", "xray", marker)

	req := whitelistRequest("xray", "9.9.9", "xray", "cfg-v1", []string{"203.0.113.0/24"})
	st, err := m.Apply(req)
	if err != nil || !st.Running {
		t.Fatalf("whitelist apply with auto-installable nft must succeed, state=%+v err=%v", st, err)
	}
	if got := markerStarts(t, marker); got != 1 {
		t.Fatalf("kernel must have started once after the install, got %d", got)
	}
	if len(*scripts) == 0 {
		t.Fatal("ruleset must have been applied after the install")
	}
	status := m.Status("")
	if !status.NftAvailable || !status.NftAutoInstalled {
		t.Fatalf("Status must report nft available + auto-installed, got %+v", status)
	}
}

func TestManagerApplyWhitelistInstallFailureNamesReason(t *testing.T) {
	s := &installStub{runErr: errors.New("E: Unable to fetch some archives (stub)")}
	stubNftInstall(t, "ID=debian\n", s)
	stubSyncNft(t, s)
	m := NewManager(t.TempDir())
	t.Cleanup(m.Shutdown)
	marker := filepath.Join(t.TempDir(), "starts")
	placeMarkerKernel(t, m, "xray", "9.9.9", "xray", marker)

	req := whitelistRequest("xray", "9.9.9", "xray", "cfg-v1", []string{"203.0.113.0/24"})
	st, err := m.Apply(req)
	if err == nil || !strings.Contains(err.Error(), "nftables 自动安装失败") || !strings.Contains(err.Error(), "Unable to fetch") {
		t.Fatalf("whitelist apply with failing install must fail naming nftables + cause, got state=%+v err=%v", st, err)
	}
	if st.Running {
		t.Fatal("whitelisted kernel must not run when the install failed")
	}
	if got := markerStarts(t, marker); got != 0 {
		t.Fatalf("kernel must never start when the install failed, got %d", got)
	}
	status := m.Status("")
	if status.NftAvailable || status.NftError == "" || !strings.Contains(status.NftError, "Unable to fetch") {
		t.Fatalf("Status must report the install failure, got %+v", status)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
