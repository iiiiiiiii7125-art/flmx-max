package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// F3-NEW-03 回归：公告内容与开关均未变时保存，不得改写 updated_time
// ——前端按 update_time 判「新公告」，无改动保存也会让横幅/弹窗重弹。
// 红绿双证：修前第二次保存会推高 update_time（本测试失败），修后不变。
func TestF3NEW03UnchangedAnnouncementSaveKeepsUpdateTime(t *testing.T) {
	h, r := newPanelsecHandler(t)
	post := func(content string, enabled int) {
		t.Helper()
		body := fmt.Sprintf(`{"content":%q,"enabled":%d}`, content, enabled)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/announcement/update", strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.updateAnnouncement(rec, req)
		var env struct {
			Code int `json:"code"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Code != 0 {
			t.Fatalf("updateAnnouncement(%q, %d) failed: code=%d body=%s err=%v", content, enabled, env.Code, rec.Body.String(), err)
		}
	}
	updateTime := func() int64 {
		t.Helper()
		ann, err := r.GetAnnouncement()
		if err != nil || ann == nil {
			t.Fatalf("GetAnnouncement failed: ann=%v err=%v", ann, err)
		}
		if ann.UpdatedTime.Valid {
			return ann.UpdatedTime.Int64
		}
		return ann.CreatedTime
	}

	post("hello", 1)
	first := updateTime()
	// 让时钟走过至少 1ms：若实现仍写库，update_time 必然变大。
	time.Sleep(2 * time.Millisecond)

	post("hello", 1)
	if got := updateTime(); got != first {
		t.Fatalf("F3-NEW-03: unchanged save moved update_time %d -> %d, want unchanged", first, got)
	}

	// 有变化时行为不变：仍写库、update_time 推进。
	post("hello2", 1)
	if got := updateTime(); got <= first {
		t.Fatalf("changed save must advance update_time, first=%d got=%d", first, got)
	}
}
