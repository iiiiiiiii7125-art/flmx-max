package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// PORT-REVIEW 复核修复批 A lane：F-04 (PORT-REVIEW-L1-D4U2, P3)。
// 旧调优动作 "restore"（TuneApply restore=true）只恢复内核参数、
// 会保留整形配置与 unit，已下线；面板 API 须拒掉它并提示走恢复原配置
// （restoreAuto）。前端类型同步移除，见 vite-frontend/src/api/index.ts。

func TestNodeTuneRestoreRetired(t *testing.T) {
	rec := httptest.NewRecorder()
	(&Handler{}).nodeTune(rec, httptest.NewRequest(http.MethodPost, "/api/v1/node/tune", strings.NewReader(`{"id":1,"action":"restore"}`)))
	var res struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("response is not JSON: %v\n%s", err, rec.Body.String())
	}
	if res.Code == 0 {
		t.Fatalf("D4U2: retired restore must not succeed: %+v", res)
	}
	if !strings.Contains(res.Msg, "restoreAuto") {
		t.Fatalf("D4U2: retired restore must point at restoreAuto: %+v", res)
	}
}
