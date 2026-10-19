package handler

import (
	"testing"

	"go-backend/internal/store/model"
)

// AUDIT-R7 (L2 变异测试 M2)：isUserQuotaExceeded 的边界条件测试。
// 变异将 >= 改为 > 后存活，说明精确边界 (Used == Limit) 无覆盖。
// 当前语义为 fail-closed（达到即限），本测试锁定该行为。
func TestIsUserQuotaExceededBoundary(t *testing.T) {
	const gb = int64(1024 * 1024 * 1024)

	tests := []struct {
		name  string
		view  *model.UserQuotaView
		want  bool
	}{
		{"nil view", nil, false},
		{"no limits", &model.UserQuotaView{}, false},
		{"daily just below", &model.UserQuotaView{DailyLimitGB: 10, DailyUsedBytes: 10*gb - 1}, false},
		{"daily exactly at limit", &model.UserQuotaView{DailyLimitGB: 10, DailyUsedBytes: 10 * gb}, true},
		{"daily above limit", &model.UserQuotaView{DailyLimitGB: 10, DailyUsedBytes: 10*gb + 1}, true},
		{"monthly just below", &model.UserQuotaView{MonthlyLimitGB: 100, MonthlyUsedBytes: 100*gb - 1}, false},
		{"monthly exactly at limit", &model.UserQuotaView{MonthlyLimitGB: 100, MonthlyUsedBytes: 100 * gb}, true},
		{"monthly above limit", &model.UserQuotaView{MonthlyLimitGB: 100, MonthlyUsedBytes: 100*gb + 1}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isUserQuotaExceeded(tt.view); got != tt.want {
				t.Errorf("isUserQuotaExceeded() = %v, want %v", got, tt.want)
			}
		})
	}
}
