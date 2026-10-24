package handler

import (
	"bytes"
	"log"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// R11X-005（批 6）关闭断言回归：用户/UT 月度流量重置改 due 型补跑。
// 构造格以 R11-F 探针 TestR11FProbeMonthlyResetMissedDay 同格为正本：
// flow_reset_time=15、user in=111、UT in=333，重置日停机后次日/多日首跑补跑。

func seedR11B6MonthlyResetRows(t *testing.T, r *repo.Repository, resetDay int64, created time.Time) {
	t.Helper()
	createdMs := created.UnixMilli()
	if err := r.DB().Exec(`
		INSERT INTO user(id, user, pwd, role_id, exp_time, flow, in_flow, out_flow, flow_reset_time, num, created_time, updated_time, status)
		VALUES(2, 'r11b6_user', 'x', 1, 0, 100, 111, 222, ?, 1, ?, ?, 1)
	`, resetDay, createdMs, createdMs).Error; err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if err := r.DB().Exec(`
		INSERT INTO tunnel(id, name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, in_ip, inx)
		VALUES(1, 't1', 1.0, 1, 'tls', 1, ?, ?, 1, NULL, 0)
	`, createdMs, createdMs).Error; err != nil {
		t.Fatalf("insert tunnel: %v", err)
	}
	if err := r.DB().Exec(`
		INSERT INTO user_tunnel(id, user_id, tunnel_id, speed_id, num, flow, in_flow, out_flow, flow_reset_time, exp_time, status)
		VALUES(10, 2, 1, NULL, 1, 1, 333, 444, ?, 0, 1)
	`, resetDay).Error; err != nil {
		t.Fatalf("insert user_tunnel: %v", err)
	}
}

func r11B6Flows(t *testing.T, r *repo.Repository) (userIn, userOut, utIn, utOut int64) {
	t.Helper()
	userIn, userOut, _ = mustQueryInt64Int64Int(t, r, `SELECT in_flow, out_flow, status FROM user WHERE id = 2`)
	utIn, utOut, _ = mustQueryInt64Int64Int(t, r, `SELECT in_flow, out_flow, status FROM user_tunnel WHERE id = 10`)
	return userIn, userOut, utIn, utOut
}

func openR11B6Repo(t *testing.T, name string) *repo.Repository {
	t.Helper()
	r, err := repo.Open(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// 断言①（次日补跑）：重置日（15 日）停机未跑，16 日首跑 job → 补跑清零。
func TestR11B6MonthlyResetMissedDayCatchUpNextDay(t *testing.T) {
	r := openR11B6Repo(t, "r11b6-missed-next.db")
	h := New(r, "secret")
	seedR11B6MonthlyResetRows(t, r, 15, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))

	// 15 日当天不跑（停机），16 日首跑。
	h.runResetAndExpiryJob(time.Date(2026, 3, 16, 0, 0, 5, 0, time.UTC))

	userIn, userOut, utIn, utOut := r11B6Flows(t, r)
	if userIn != 0 || userOut != 0 || utIn != 0 || utOut != 0 {
		t.Fatalf("missed-day catch-up (next day): expected user+UT zeroed, got user in=%d out=%d ut in=%d out=%d", userIn, userOut, utIn, utOut)
	}
}

// 断言①（多日后补跑）：重置日停机，20 日才首跑 → 仍补跑清零（F 探针同格）。
func TestR11B6MonthlyResetMissedDayCatchUpDaysLater(t *testing.T) {
	r := openR11B6Repo(t, "r11b6-missed-later.db")
	h := New(r, "secret")
	seedR11B6MonthlyResetRows(t, r, 15, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))

	h.runResetAndExpiryJob(time.Date(2026, 3, 20, 0, 0, 5, 0, time.UTC))

	userIn, userOut, utIn, utOut := r11B6Flows(t, r)
	if userIn != 0 || userOut != 0 || utIn != 0 || utOut != 0 {
		t.Fatalf("missed-day catch-up (day 20): expected user+UT zeroed, got user in=%d out=%d ut in=%d out=%d", userIn, userOut, utIn, utOut)
	}
}

// 断言②：补跑恰一次——补跑后同周期内 job 连跑（次日/多日/月末）不得
// 再次清零补跑后新累积的计数；到下个周期重置日才再次清零。
func TestR11B6MonthlyResetCatchUpExactlyOncePerPeriod(t *testing.T) {
	r := openR11B6Repo(t, "r11b6-exactly-once.db")
	h := New(r, "secret")
	seedR11B6MonthlyResetRows(t, r, 15, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))

	h.runResetAndExpiryJob(time.Date(2026, 3, 16, 0, 0, 5, 0, time.UTC))
	userIn, _, utIn, _ := r11B6Flows(t, r)
	if userIn != 0 || utIn != 0 {
		t.Fatalf("catch-up on day 16: expected zeroed, got user in=%d ut in=%d", userIn, utIn)
	}

	// 补跑后新累积的流量。
	if err := r.DB().Exec(`UPDATE user SET in_flow = 50, out_flow = 60 WHERE id = 2`).Error; err != nil {
		t.Fatalf("reseed user flow: %v", err)
	}
	if err := r.DB().Exec(`UPDATE user_tunnel SET in_flow = 70, out_flow = 80 WHERE id = 10`).Error; err != nil {
		t.Fatalf("reseed ut flow: %v", err)
	}
	for _, day := range []int{17, 20, 31} {
		h.runResetAndExpiryJob(time.Date(2026, 3, day, 0, 0, 5, 0, time.UTC))
		userIn, userOut, utIn, utOut := r11B6Flows(t, r)
		if userIn != 50 || userOut != 60 || utIn != 70 || utOut != 80 {
			t.Fatalf("same-period rerun on day %d must not reset again, got user in=%d out=%d ut in=%d out=%d", day, userIn, userOut, utIn, utOut)
		}
	}

	// 下个周期重置日（4 月 15 日）应再次清零。
	h.runResetAndExpiryJob(time.Date(2026, 4, 15, 0, 0, 5, 0, time.UTC))
	userIn, userOut, utIn, utOut := r11B6Flows(t, r)
	if userIn != 0 || userOut != 0 || utIn != 0 || utOut != 0 {
		t.Fatalf("next-period reset on Apr 15: expected zeroed, got user in=%d out=%d ut in=%d out=%d", userIn, userOut, utIn, utOut)
	}
}

// 断言③a：当日路径不回归——重置日当日在跑即重置。
func TestR11B6MonthlyResetSameDayStillResets(t *testing.T) {
	r := openR11B6Repo(t, "r11b6-same-day.db")
	h := New(r, "secret")
	seedR11B6MonthlyResetRows(t, r, 15, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))

	h.runResetAndExpiryJob(time.Date(2026, 3, 15, 0, 0, 5, 0, time.UTC))

	userIn, userOut, utIn, utOut := r11B6Flows(t, r)
	if userIn != 0 || userOut != 0 || utIn != 0 || utOut != 0 {
		t.Fatalf("same-day reset: expected zeroed, got user in=%d out=%d ut in=%d out=%d", userIn, userOut, utIn, utOut)
	}
}

// 断言③b：月末钳制格不回归——flow_reset_time=31 在 2 月于月末日（28 日）
// 重置；28 日之前（27 日）不得提前重置。
func TestR11B6MonthlyResetMonthEndClamp(t *testing.T) {
	r := openR11B6Repo(t, "r11b6-clamp.db")
	h := New(r, "secret")
	seedR11B6MonthlyResetRows(t, r, 31, time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC))

	h.runResetAndExpiryJob(time.Date(2026, 2, 27, 0, 0, 5, 0, time.UTC))
	userIn, _, utIn, _ := r11B6Flows(t, r)
	if userIn != 111 || utIn != 333 {
		t.Fatalf("clamp: Feb 27 must not reset day-31 rows yet, got user in=%d ut in=%d", userIn, utIn)
	}

	h.runResetAndExpiryJob(time.Date(2026, 2, 28, 0, 0, 5, 0, time.UTC))
	userIn, userOut, utIn, utOut := r11B6Flows(t, r)
	if userIn != 0 || userOut != 0 || utIn != 0 || utOut != 0 {
		t.Fatalf("clamp: Feb 28 (last day) must reset day-31 rows, got user in=%d out=%d ut in=%d out=%d", userIn, userOut, utIn, utOut)
	}
}

// 断言③c：未到重置日不得提前重置（due 的下界）。
func TestR11B6MonthlyResetNotDueBeforeDay(t *testing.T) {
	r := openR11B6Repo(t, "r11b6-not-due.db")
	h := New(r, "secret")
	seedR11B6MonthlyResetRows(t, r, 15, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))

	h.runResetAndExpiryJob(time.Date(2026, 3, 14, 0, 0, 5, 0, time.UTC))

	userIn, _, utIn, _ := r11B6Flows(t, r)
	if userIn != 111 || utIn != 333 {
		t.Fatalf("not due: Mar 14 must not reset day-15 rows, got user in=%d ut in=%d", userIn, utIn)
	}
}

// 断言④：两调用注入错误时日志留痕可查（不得再 `_ =` 静默吞）。
// 以删表注入 DB 错误：user / user_tunnel 两面各跑一次，日志须分别
// 含两调用的上下文。
func TestR11B6MonthlyResetErrorsAreLogged(t *testing.T) {
	r := openR11B6Repo(t, "r11b6-errlog.db")
	h := New(r, "secret")
	seedR11B6MonthlyResetRows(t, r, 15, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))

	var buf bytes.Buffer
	oldOut := log.Writer()
	oldFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(oldOut)
		log.SetFlags(oldFlags)
	}()

	if err := r.DB().Exec(`DROP TABLE user`).Error; err != nil {
		t.Fatalf("drop user table: %v", err)
	}
	h.resetMonthlyFlow(time.Date(2026, 3, 16, 0, 0, 5, 0, time.UTC))
	if !strings.Contains(buf.String(), "ResetUserMonthlyFlow") {
		t.Fatalf("expected log trace for ResetUserMonthlyFlow failure, got %q", buf.String())
	}

	buf.Reset()
	if err := r.DB().Exec(`DROP TABLE user_tunnel`).Error; err != nil {
		t.Fatalf("drop user_tunnel table: %v", err)
	}
	h.resetMonthlyFlow(time.Date(2026, 3, 16, 0, 0, 5, 0, time.UTC))
	if !strings.Contains(buf.String(), "ResetUserTunnelMonthlyFlow") {
		t.Fatalf("expected log trace for ResetUserTunnelMonthlyFlow failure, got %q", buf.String())
	}
}
