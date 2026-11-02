package repo

import (
	"path/filepath"
	"strings"
	"testing"

	"go-backend/internal/store/model"
)

// F3-D（LEDGER3 C-F5-01）：配置键 ip 的恢复面必须与保存面同口径
// （B14-002）——保存面 normalizeAndValidateConfigValue 的 ip 分支有
// validateNoEmptyLabelAddr＋validatePanelInstallAddr（→IsValidNodeAddress
// 全语法门），恢复面此前只有空标签谓词，含空格/下划线/双端口/分号
// 等保存面必拒形态经备份导入原样落库。本文件的语料与保存面一半
// （handler 包 config_ip_corpus_f3d_test.go 的 f3dConfigIPCorpus）
// 逐字相同，两份语料任一面谓词漂移即有一侧变红。集成用例写于修复
// 前：基线下坏值直接落库、用例为红。

type f3dIPCorpusCase struct {
	value   string
	wantErr bool
}

func f3dConfigIPCorpus() []f3dIPCorpusCase {
	return []f3dIPCorpusCase{
		// 保存面必拒形态（恢复面同拒）。
		{"x.com;id", true},
		{"panel example.com", true},
		{"pan_el.example.com", true},
		{"panel.example.com:80:443", true},
		{"panel.example.com:99999", true},
		{"panel.example.com:65536", true},
		{"panel.example.com:abc", true},
		{"panel.example.com:0", true},
		{"panel.example.com:", true},
		{"999.999.1.1", true},
		{"a..b", true},
		{"panel.example.com/path", true},
		{"panel.example.com?q=1", true},
		{"user@panel.example.com", true},
		{"panel.example.com,other.example.com", true},
		{"panel.example.com\nevil.example.com", true},
		{"https://pan_el.example.com", true},
		{"http://panel.example.com/path", true},
		{"https://panel.example.com:99999", true},
		{"[2001:db8::9", true},
		// 保存面放行形态（恢复面同放，含既有可存性）。
		{"", false},
		{"panel.example.com", false},
		{"panel.example.com:8443", false},
		{"panel.example.com:65535", false},
		{"https://panel.example.com", false},
		{"wss://panel.example.com:8443", false},
		{"https://192.0.2.5:8443", false},
		{"192.0.2.5", false},
		{"192.0.2.5:8443", false},
		{"127.0.0.1:6365", false},
		{"10.0.0.8", false},
		{"localhost", false},
		{"localhost:6365", false},
		{"[2001:db8::9]:8443", false},
		{"2001:db8::9", false},
		{"fe80::1%eth0", false},
		{"  panel.example.com:8443  ", false},
	}
}

func TestF3DValidatePanelInstallAddrRestoreCorpus(t *testing.T) {
	for _, tc := range f3dConfigIPCorpus() {
		err := validatePanelInstallAddrForRestore(tc.value)
		if (err != nil) != tc.wantErr {
			t.Fatalf("validatePanelInstallAddrForRestore(%q) err = %v, wantErr = %v", tc.value, err, tc.wantErr)
		}
	}
}

func f3dImportIP(t *testing.T, r *Repository, raw string) *model.ImportResult {
	t.Helper()
	res, err := r.Import(&model.BackupData{Version: "1.0", Configs: map[string]string{"ip": raw}}, []string{"configs"})
	if err != nil {
		t.Fatalf("Import configs failed: %v", err)
	}
	return res
}

func f3dStoredIP(t *testing.T, r *Repository) (string, bool) {
	t.Helper()
	cfg, err := r.GetConfigByName("ip")
	if err != nil {
		t.Fatalf("GetConfigByName: %v", err)
	}
	if cfg == nil {
		return "", false
	}
	return cfg.Value, true
}

func TestF3DConfigIPRestoreSkipsBadValues(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	// 任务点名形态：空格/下划线/双端口/分号——逐一须跳过＋warning
	// 点名 ip＋不落库（基线下全部落库，本用例为红）。
	for _, bad := range []string{
		"pan el.example.com",
		"pan_el.example.com",
		"panel.example.com:80:443",
		"x.com;id",
	} {
		res := f3dImportIP(t, r, bad)
		if res.ConfigsImported != 0 {
			t.Fatalf("bad %q: ConfigsImported = %d, want 0", bad, res.ConfigsImported)
		}
		if got, ok := f3dStoredIP(t, r); ok {
			t.Fatalf("bad %q: stored ip %q, want no row", bad, got)
		}
		if !strings.Contains(strings.Join(res.ImportWarnings, "\n"), `"ip"`) {
			t.Fatalf("bad %q: warnings %v do not name ip", bad, res.ImportWarnings)
		}
	}

	// 合法形态逐字恢复（含 scheme 前缀与 host:port 既有可存形态）。
	for _, good := range []string{"panel.example.com", "panel.example.com:8443", "https://panel.example.com:8443", "192.0.2.5:8443"} {
		if err := r.DB().Exec(`DELETE FROM vite_config WHERE name = 'ip'`).Error; err != nil {
			t.Fatalf("clear ip: %v", err)
		}
		res := f3dImportIP(t, r, good)
		if res.ConfigsImported != 1 {
			t.Fatalf("good %q: ConfigsImported = %d, want 1 (warnings %v)", good, res.ConfigsImported, res.ImportWarnings)
		}
		if got, ok := f3dStoredIP(t, r); !ok || got != good {
			t.Fatalf("good %q: stored %q (ok=%v), want verbatim", good, got, ok)
		}
	}

	// 空值按保存面可清空语义恢复为空行。
	if err := r.DB().Exec(`DELETE FROM vite_config WHERE name = 'ip'`).Error; err != nil {
		t.Fatalf("clear ip: %v", err)
	}
	res := f3dImportIP(t, r, "")
	if res.ConfigsImported != 1 {
		t.Fatalf("empty: ConfigsImported = %d, want 1", res.ConfigsImported)
	}
	if got, ok := f3dStoredIP(t, r); !ok || got != "" {
		t.Fatalf("empty: stored %q (ok=%v), want empty row", got, ok)
	}
}
