package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"go-backend/internal/kernel"
)

// 发现 2：sameVersionInstallWarning 须精确比较、fork 后缀计入版本
// 身份——fork 1.14.4-flmx1 与上游 1.14.4 底版相同但不是同一版本，
// 任一方向换装都不得谎报"已安装该版本，本次未重新下载"；同构建
// 重装仍报，banner 形态的已装版本同样识别。
func TestSameVersionInstallWarningForkUpstreamDirections(t *testing.T) {
	cases := []struct {
		name          string
		latest        bool
		prevInstalled string
		version       string
		want          string
	}{
		{"fork reinstall is same", false, "1.14.4-flmx1", "1.14.4-flmx1", "已安装该版本 1.14.4-flmx1，本次未重新下载"},
		{"fork to upstream is a change", false, "1.14.4-flmx1", "1.14.4", ""},
		{"upstream to fork is a change", false, "1.14.4", "1.14.4-flmx1", ""},
		{"fork banner to upstream is a change", false, "sing-box version 1.14.4-flmx1 linux/amd64", "1.14.4", ""},
		{"fork banner to fork is same", false, "sing-box version 1.14.4-flmx1 linux/amd64", "1.14.4-flmx1", "已安装该版本 1.14.4-flmx1，本次未重新下载"},
		{"upstream banner to fork is a change", false, "sing-box version 1.14.4 linux/amd64", "1.14.4-flmx1", ""},
		{"upstream banner reinstall is same", false, "sing-box version 1.14.4 linux/amd64", "1.14.4", "已安装该版本 1.14.4，本次未重新下载"},
		{"xray banner reinstall is same", false, "Xray 26.11.9 (Xray, Penetrates Everything.)", "26.11.9", "已安装该版本 26.11.9，本次未重新下载"},
		{"substring is not same", false, "1.14.4-flmx1", "1.14.4-flmx", ""},
	}
	for _, c := range cases {
		if got := sameVersionInstallWarning(c.latest, c.prevInstalled, c.version); got != c.want {
			t.Errorf("%s: sameVersionInstallWarning(%v, %q, %q) = %q, want %q", c.name, c.latest, c.prevInstalled, c.version, got, c.want)
		}
	}
}

// 发现 1：上游构建安装警告只在显式装非 fork 的 sing-box 具体版本
// 时出现；fork 版、latest 哨兵与非 sing-box 内核不警告。
func TestSingboxUpstreamInstallWarning(t *testing.T) {
	cases := []struct {
		kernelType, version string
		wantWarning         bool
	}{
		{kernel.TypeSingbox, "1.14.4", true},
		{kernel.TypeSingbox, "1.14.4-flmx1", false},
		{kernel.TypeSingbox, "latest", false},
		{kernel.TypeSingbox, "", false},
		{kernel.TypeSingboxCompanion, "1.14.4", true},
		{kernel.TypeXray, "26.11.9", false},
	}
	for _, c := range cases {
		got := singboxUpstreamInstallWarning(c.kernelType, c.version)
		if (got != "") != c.wantWarning {
			t.Errorf("singboxUpstreamInstallWarning(%q, %q) = %q, wantWarning=%v", c.kernelType, c.version, got, c.wantWarning)
		}
	}
	if got := singboxUpstreamInstallWarning(kernel.TypeSingbox, "1.14.4"); !strings.Contains(got, "上游") || !strings.Contains(got, kernel.SingboxForkVersion) || !strings.Contains(got, "200") {
		t.Errorf("upstream warning must name the upstream build, the fork version and the 200-packet burst, got %q", got)
	}
}

// 发现 1（F3 面）：请求上游版时实测为上游版即通过，实测为 fork
// 版同样通过（fork 满足上游请求）；请求 fork 版时实测为上游版
// 仍判不一致（既有钉死用例见 TestKernelApplyVersionMismatch）。
func TestKernelApplyVersionMismatchUpstreamRequest(t *testing.T) {
	cases := []struct {
		kernelType, requested, reported string
		wantMismatch                    bool
	}{
		{kernel.TypeSingbox, "1.14.4", "sing-box version 1.14.4 linux/amd64", false},
		{kernel.TypeSingbox, "1.14.4", "sing-box version 1.14.4-flmx1 linux/amd64", false},
		{kernel.TypeSingbox, "1.14.4", "sing-box version dev linux/amd64", true},
		{kernel.TypeSingbox, "1.14.4", "", true},
	}
	for _, c := range cases {
		got := kernelApplyVersionMismatch(c.kernelType, c.requested, c.reported)
		if (got != "") != c.wantMismatch {
			t.Errorf("kernelApplyVersionMismatch(%q, %q, %q) = %q, wantMismatch=%v", c.kernelType, c.requested, c.reported, got, c.wantMismatch)
		}
	}
}

// postKernelInstallWarning 与 postKernelInstall 同口径，另解出信封
// 的 warning 字段（上游构建回执的警告走该字段，不走 msg）。
func postKernelInstallWarning(t *testing.T, h *Handler, nodeID int64, kernelType, version string) (int, string, string) {
	t.Helper()
	body := `{"nodeId":` + strconv.FormatInt(nodeID, 10) + `,"kernelType":"` + kernelType + `","version":"` + version + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/kernel/install", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.kernelInstall(rec, req)
	var env struct {
		Code    int    `json:"code"`
		Msg     string `json:"msg"`
		Warning string `json:"warning"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode install response: %v (body=%s)", err, rec.Body.String())
	}
	return env.Code, env.Msg, env.Warning
}

// 发现 1（回执面）：显式安装上游 1.14.4（回滚）成功、版本落库，
// 但回执 warning 必须点名上游构建、无队列修复、200 包突发可能
// 丢包——回滚路保留，只是不得再全程绿灯。
func TestForkInstallUpstreamWarnsOnReceipt(t *testing.T) {
	stubReleaseTagAPI(t, map[string]int{"v1.14.4": 200})
	h, r, mock, _, k, nodeID := setupKernelNotifyRig(t)
	mock.setApplyVersion("sing-box version 1.14.4 linux/amd64")
	if err := r.DB().Exec("UPDATE node_kernel SET kernel_type=? WHERE id=?", kernel.TypeSingbox, k.ID).Error; err != nil {
		t.Fatal(err)
	}
	code, _, warning := postKernelInstallWarning(t, h, nodeID, kernel.TypeSingbox, "1.14.4")
	if code != 0 {
		t.Fatalf("upstream rollback install must still succeed, got code=%d", code)
	}
	if !strings.Contains(warning, "上游") || !strings.Contains(warning, kernel.SingboxForkVersion) || !strings.Contains(warning, "200") {
		t.Fatalf("upstream install receipt must warn about the upstream build, the fork version and the 200-packet burst, got warning=%q", warning)
	}
	after, err := r.GetNodeKernel(k.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != "1.14.4" || !strings.Contains(after.InstalledVersion, "1.14.4") {
		t.Fatalf("upstream install must store version=%q installed=%q", after.Version, after.InstalledVersion)
	}
}

// 发现 1（回执面对照）：安装 fork 版成功时回执不得带上游警告。
func TestForkInstallForkHasNoUpstreamWarning(t *testing.T) {
	stubReleaseTagAPI(t, map[string]int{"v1.14.4-flmx1": 200})
	h, r, mock, _, k, nodeID := setupKernelNotifyRig(t)
	mock.setApplyVersion("sing-box version 1.14.4-flmx1 linux/amd64")
	if err := r.DB().Exec("UPDATE node_kernel SET kernel_type=? WHERE id=?", kernel.TypeSingbox, k.ID).Error; err != nil {
		t.Fatal(err)
	}
	code, _, warning := postKernelInstallWarning(t, h, nodeID, kernel.TypeSingbox, "1.14.4-flmx1")
	if code != 0 {
		t.Fatalf("fork install must succeed, got code=%d", code)
	}
	if strings.Contains(warning, "上游") {
		t.Fatalf("fork install receipt must not carry the upstream warning, got warning=%q", warning)
	}
}
