package repo

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// RR-S2-01：分组三件套回执字段整族对齐——TunnelGroupsIgnored /
// UserGroupsIgnored / PermissionsIgnored 据实累加被丢弃条数（与
// ImportWarnings 文本披露同数），回执 JSON 用新名、旧 *Imported
// 名不得再出现（monitorPermissionsImported 是另一字段、真导入
// 计数，不在改名范围，其键名含 monitor 前缀、不与旧名混淆）。
func TestRRRemovedGroupSegmentsReceiptCounts(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "rr-removed-segments.db")
	backup := &model.BackupData{
		Version: "1.0",
		TunnelGroups: []model.TunnelGroupBackup{
			{ID: 1, Name: "tg1", Status: 1, CreatedTime: now},
			{ID: 2, Name: "tg2", Status: 1, CreatedTime: now},
		},
		UserGroups: []model.UserGroupBackup{
			{ID: 1, Name: "ug1", Status: 1, CreatedTime: now},
			{ID: 2, Name: "ug2", Status: 1, CreatedTime: now},
			{ID: 3, Name: "ug3", Status: 1, CreatedTime: now},
		},
		Permissions: []model.PermissionBackup{
			{ID: 1, UserGroupID: 1, TunnelGroupID: 1, CreatedTime: now},
		},
	}
	result, err := dest.Import(backup, []string{"users"})
	if err != nil {
		t.Fatalf("import with removed segments: %v", err)
	}
	if result.TunnelGroupsIgnored != 2 || result.UserGroupsIgnored != 3 || result.PermissionsIgnored != 1 {
		t.Fatalf("ignored counts = %d/%d/%d, want 2/3/1 (same numbers the warnings disclose)",
			result.TunnelGroupsIgnored, result.UserGroupsIgnored, result.PermissionsIgnored)
	}
	// 文本披露同数仍在（结构化字段是补充而非替代）。
	for _, seg := range []string{"tunnelGroups", "userGroups", "permissions"} {
		found := false
		for _, w := range result.ImportWarnings {
			if strings.Contains(w, seg) {
				found = true
			}
		}
		if !found {
			t.Fatalf("segment %s must still be named in import warnings, got %v", seg, result.ImportWarnings)
		}
	}

	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal receipt: %v", err)
	}
	body := string(raw)
	for _, want := range []string{`"tunnelGroupsIgnored":2`, `"userGroupsIgnored":3`, `"permissionsIgnored":1`} {
		if !strings.Contains(body, want) {
			t.Fatalf("receipt JSON missing %s: %s", want, body)
		}
	}
	for _, old := range []string{`"tunnelGroupsImported"`, `"userGroupsImported"`, `"permissionsImported"`} {
		if strings.Contains(body, old) {
			t.Fatalf("receipt JSON must not carry renamed-away field %s: %s", old, body)
		}
	}

	// 无已移除段的导入：三计数为 0、无凭空 warning。
	dest2 := openBackupRepo(t, "rr-no-removed-segments.db")
	result2, err := dest2.Import(&model.BackupData{Version: "1.0"}, []string{"users"})
	if err != nil {
		t.Fatalf("clean import: %v", err)
	}
	if result2.TunnelGroupsIgnored != 0 || result2.UserGroupsIgnored != 0 || result2.PermissionsIgnored != 0 {
		t.Fatalf("clean import ignored counts = %d/%d/%d, want 0/0/0",
			result2.TunnelGroupsIgnored, result2.UserGroupsIgnored, result2.PermissionsIgnored)
	}
	if len(result2.ImportWarnings) != 0 {
		t.Fatalf("clean import must not warn about removed segments, got %v", result2.ImportWarnings)
	}
}
