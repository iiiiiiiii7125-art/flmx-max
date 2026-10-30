package handler

import (
	"encoding/json"
	"strings"
	"testing"
)

// R2 B03-001 / B09-004: 分组名（user_group / tunnel_group）与限速规则名
// 此前无长度校验、列宽同为 varchar(100)——SQLite 静默落库、PG 吐未
// 转译的原始落库错误（O5-002 用户名同型先例）。修复后四个写面
// （group create/update、speed-limit create/update）一律按列宽 100
// 点名拒绝、不落库；100 字符整好合法。

func k1Code(t *testing.T, body string) int {
	t.Helper()
	var env struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return env.Code
}

func TestR2K1GroupNameLengthBounded(t *testing.T) {
	r, h := r2b2Open(t)
	name101 := strings.Repeat("g", 101)
	name100 := strings.Repeat("g", 100)

	if body := r2b2Post(t, h.groupUserCreate, "/api/v1/group/user/create", map[string]interface{}{"name": name101}, ""); k1Code(t, body) == 0 {
		t.Fatalf("user group create with 101-rune name accepted: %s", body)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM user_group`); n != 0 {
		t.Fatalf("rejected user group create persisted %d rows", n)
	}
	if body := r2b2Post(t, h.groupTunnelCreate, "/api/v1/group/tunnel/create", map[string]interface{}{"name": name101}, ""); k1Code(t, body) == 0 {
		t.Fatalf("tunnel group create with 101-rune name accepted: %s", body)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM tunnel_group`); n != 0 {
		t.Fatalf("rejected tunnel group create persisted %d rows", n)
	}

	// 100 字符整好合法。
	r2b2AssertCode0(t, "user group create 100", r2b2Post(t, h.groupUserCreate, "/api/v1/group/user/create", map[string]interface{}{"name": name100}, ""))
	var gid int64
	if err := r.DB().Raw(`SELECT id FROM user_group WHERE name = ?`, name100).Scan(&gid).Error; err != nil || gid <= 0 {
		t.Fatalf("read created group: %v", err)
	}

	// update 改名超长：拒绝且原名不变。
	if body := r2b2Post(t, h.groupUserUpdate, "/api/v1/group/user/update", map[string]interface{}{"id": gid, "name": name101}, ""); k1Code(t, body) == 0 {
		t.Fatalf("user group update with 101-rune name accepted: %s", body)
	}
	var got string
	if err := r.DB().Raw(`SELECT name FROM user_group WHERE id = ?`, gid).Scan(&got).Error; err != nil {
		t.Fatalf("read group name: %v", err)
	}
	if got != name100 {
		t.Fatalf("group name changed to %d runes after rejected update, want unchanged 100", len([]rune(got)))
	}
}

func TestR2K1SpeedLimitNameLengthBounded(t *testing.T) {
	r, h := r2b2Open(t)
	name101 := strings.Repeat("s", 101)
	name100 := strings.Repeat("s", 100)

	if body := r2b2Post(t, h.speedLimitCreate, "/api/v1/speed-limit/create", map[string]interface{}{"name": name101, "speed": 10}, ""); k1Code(t, body) == 0 {
		t.Fatalf("speed-limit create with 101-rune name accepted: %s", body)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM speed_limit`); n != 0 {
		t.Fatalf("rejected speed-limit create persisted %d rows", n)
	}

	r2b2AssertCode0(t, "speed-limit create 100", r2b2Post(t, h.speedLimitCreate, "/api/v1/speed-limit/create", map[string]interface{}{"name": name100, "speed": 10}, ""))
	var sid int64
	if err := r.DB().Raw(`SELECT id FROM speed_limit WHERE name = ?`, name100).Scan(&sid).Error; err != nil || sid <= 0 {
		t.Fatalf("read created speed limit: %v", err)
	}

	if body := r2b2Post(t, h.speedLimitUpdate, "/api/v1/speed-limit/update", map[string]interface{}{"id": sid, "name": name101}, ""); k1Code(t, body) == 0 {
		t.Fatalf("speed-limit update with 101-rune name accepted: %s", body)
	}
	// presence 口径：不带 name 的无关编辑不受存量名长影响（此处名合法，
	// 仅作对照证明 update 仍可用）。
	r2b2AssertCode0(t, "speed-limit update speed only", r2b2Post(t, h.speedLimitUpdate, "/api/v1/speed-limit/update", map[string]interface{}{"id": sid, "speed": 20}, ""))
	var got string
	if err := r.DB().Raw(`SELECT name FROM speed_limit WHERE id = ?`, sid).Scan(&got).Error; err != nil {
		t.Fatalf("read speed limit name: %v", err)
	}
	if got != name100 {
		t.Fatalf("speed limit name changed after rejected update, want unchanged")
	}
}
