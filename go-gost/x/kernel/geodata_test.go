package kernel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// F-139：geodata 下载的镜像回退——校验文件与数据文件在镜像不可用时
// 都应剥离镜像前缀直连成功（此前 stageGeoFile 两处都是裸 fetch 单次
// 尝试，镜像一挂整轮更新失败）。
func TestStageGeoFileFallsBackToDirectWhenMirrorFails(t *testing.T) {
	data := []byte("geo data via direct")
	sum := sha256.Sum256(data)
	checksum := hex.EncodeToString(sum[:]) + "  geosite.dat\n"
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/geosite.dat":
			_, _ = w.Write(data)
		case "/geosite.dat.sha256sum":
			_, _ = w.Write([]byte(checksum))
		default:
			http.NotFound(w, r)
		}
	}))
	defer direct.Close()
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer mirror.Close()

	binDir := t.TempDir()
	f := GeoFile{
		Name:        "geosite.dat",
		URL:         mirror.URL + "/" + direct.URL + "/geosite.dat",
		ChecksumURL: mirror.URL + "/" + direct.URL + "/geosite.dat.sha256sum",
	}
	changed, err := stageGeoFile(context.Background(), binDir, f)
	if err != nil {
		t.Fatalf("镜像失败时应回退直连: %v", err)
	}
	if !changed {
		t.Fatal("应报告文件已更新")
	}
	b, err := os.ReadFile(filepath.Join(binDir, "geosite.dat.new"))
	if err != nil {
		t.Fatalf("暂存文件应存在: %v", err)
	}
	if string(b) != string(data) {
		t.Fatalf("暂存内容不符: %q", b)
	}
}

func TestUpdateGeoData(t *testing.T) {
	data := []byte("new geosite data")
	sum := sha256.Sum256(data)
	checksum := hex.EncodeToString(sum[:]) + "  geosite.dat\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/geosite.dat":
			_, _ = w.Write(data)
		case "/geosite.dat.sha256sum":
			_, _ = w.Write([]byte(checksum))
		case "/bad.dat":
			_, _ = w.Write([]byte("tampered"))
		case "/bad.dat.sha256sum":
			_, _ = w.Write([]byte(checksum))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	binDir := t.TempDir()
	path := filepath.Join(binDir, "geosite.dat")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := NewManager(t.TempDir())
	inst := &instance{logs: &logRing{}, binDir: binDir, req: ApplyRequest{Type: "xray", GeoData: []GeoFile{
		{Name: "geosite.dat", URL: srv.URL + "/geosite.dat", ChecksumURL: srv.URL + "/geosite.dat.sha256sum"},
	}}}
	m.instances["xray"] = inst

	m.UpdateGeoData(context.Background())
	if b, _ := os.ReadFile(path); string(b) != string(data) {
		t.Fatalf("file not replaced: %q", b)
	}
	if logs := strings.Join(inst.logs.tail(5), "\n"); !strings.Contains(logs, "分流数据已更新: geosite.dat") {
		t.Fatalf("logs: %s", logs)
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Fatal("backup must be removed after a good update")
	}

	// Unchanged: nothing is downloaded or logged again.
	before := len(inst.logs.tail(100))
	m.UpdateGeoData(context.Background())
	if len(inst.logs.tail(100)) != before {
		t.Fatalf("an unchanged file must not be updated: %v", inst.logs.tail(5))
	}

	// A download that fails its checksum keeps the installed file.
	inst.req.GeoData[0] = GeoFile{Name: "geosite.dat", URL: srv.URL + "/bad.dat", ChecksumURL: srv.URL + "/bad.dat.sha256sum"}
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.UpdateGeoData(context.Background())
	if b, _ := os.ReadFile(path); string(b) != "old" {
		t.Fatalf("a bad download must not replace the file: %q", b)
	}
	if _, err := os.Stat(path + ".new"); !os.IsNotExist(err) {
		t.Fatal("the rejected download must be cleaned up")
	}
	if logs := strings.Join(inst.logs.tail(5), "\n"); !strings.Contains(logs, "SHA256 校验失败") {
		t.Fatalf("logs: %s", logs)
	}
}

func TestGeoFileValidation(t *testing.T) {
	for _, g := range []GeoFile{
		{Name: "../x.dat", URL: "https://a/x", ChecksumURL: "https://a/x.sha256sum"},
		{Name: "geosite.txt", URL: "https://a/x", ChecksumURL: "https://a/x.sha256sum"},
		{Name: "geosite.dat", URL: "file:///etc/passwd", ChecksumURL: "https://a/x.sha256sum"},
	} {
		if g.validate() == nil {
			t.Fatalf("must reject %+v", g)
		}
	}
}
