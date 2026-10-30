package repo

import (
	"math"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"testing"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// C06 PG 腿返修（T2 真机证伪，2026-10-30）：fix-C06 的饱和表达式最初把
// 饱和边界写成 SQL 内的 `? - ?`。pgx 发送的参数不带类型标注，PostgreSQL
// 在两个操作数都没有已知类型时无法定操作符——PG 面板上每一次流量入账
// （含普通小批次）都报 "operator is not unique: unknown - unknown
// (SQLSTATE 42725)"，计费整体瘫痪；SQLite 的动态类型在沙箱里把这个
// 缺陷掩盖了。修复把边界改在 Go 侧预计算，SQL 里不再出现占位符之间
// 的算术。本文件用字符串级断言锁死这一形态：任何把参数间算术写回
// 表达式的改动都会在这里变红，不必等真机 PG 才发现。

var paramParamArithmetic = regexp.MustCompile(`\?\s*[-+*/%]\s*\?`)

func TestSaturatingCounterIncrExprNoParamParamArithmetic_C06PG(t *testing.T) {
	expr := saturatingCounterIncrExpr("in_flow", 5)
	wantSQL := "CASE WHEN ? > 0 AND in_flow > ? THEN ? " +
		"WHEN ? < 0 AND in_flow < ? THEN ? " +
		"ELSE in_flow + ? END"
	if expr.SQL != wantSQL {
		t.Fatalf("expression shape changed:\n got: %s\nwant: %s", expr.SQL, wantSQL)
	}
	if paramParamArithmetic.MatchString(expr.SQL) {
		t.Fatalf("expression contains arithmetic between placeholders (? op ?): %s — "+
			"untyped under pgx, PostgreSQL rejects it with SQLSTATE 42725", expr.SQL)
	}

	// 参数布局逐值锁定：delta, hi, MaxInt64, delta, lo, MinInt64, delta，
	// 其中 hi=MaxInt64-delta（delta>0), lo=MinInt64-delta（delta<0)，
	// 另一侧的惰性默认取界值本身（分支被 delta 符号门控，不承载语义）。
	cases := []struct {
		delta int64
		vars  []interface{}
	}{
		{5, []interface{}{int64(5), int64(math.MaxInt64 - 5), int64(math.MaxInt64),
			int64(5), int64(math.MinInt64), int64(math.MinInt64), int64(5)}},
		{-5, []interface{}{int64(-5), int64(math.MaxInt64), int64(math.MaxInt64),
			int64(-5), int64(math.MinInt64 + 5), int64(math.MinInt64), int64(-5)}},
		{0, []interface{}{int64(0), int64(math.MaxInt64), int64(math.MaxInt64),
			int64(0), int64(math.MinInt64), int64(math.MinInt64), int64(0)}},
		{math.MaxInt64, []interface{}{int64(math.MaxInt64), int64(0), int64(math.MaxInt64),
			int64(math.MaxInt64), int64(math.MinInt64), int64(math.MinInt64), int64(math.MaxInt64)}},
		{math.MinInt64, []interface{}{int64(math.MinInt64), int64(math.MaxInt64), int64(math.MaxInt64),
			int64(math.MinInt64), int64(0), int64(math.MinInt64), int64(math.MinInt64)}},
	}
	for _, c := range cases {
		got := saturatingCounterIncrExpr("in_flow", c.delta)
		if !reflect.DeepEqual(got.Vars, c.vars) {
			t.Fatalf("delta=%d: vars mismatch:\n got: %v\nwant: %v", c.delta, got.Vars, c.vars)
		}
		if paramParamArithmetic.MatchString(got.SQL) {
			t.Fatalf("delta=%d: param-param arithmetic reintroduced: %s", c.delta, got.SQL)
		}
	}
}

// SQLite 语义等价性：新表达式（Go 预计算边界）与旧表达式（SQL 内
// `? - ?`）在真 SQLite 上对边界组合逐一同值，且都等于 Go 侧
// saturatingAddInt64 的饱和结果——证明返修没有改动已真机 PASS 的
// SQLite 腿行为。旧形态只能在 SQLite 上跑（PG 直接 42725)，这正是
// 当初缺陷被掩盖的原因。
func TestSaturatingCounterIncrExprSQLiteEquivalence_C06PG(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "flow-expr-equiv.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	oldExpr := func(balance, delta int64) clause.Expr {
		return gorm.Expr(
			"CASE WHEN ? > 0 AND ? > ? - ? THEN ? "+
				"WHEN ? < 0 AND ? < ? - ? THEN ? "+
				"ELSE ? + ? END",
			delta, balance, int64(math.MaxInt64), delta, int64(math.MaxInt64),
			delta, balance, int64(math.MinInt64), delta, int64(math.MinInt64),
			balance, delta)
	}
	eval := func(e clause.Expr) int64 {
		var got int64
		if err := r.DB().Raw("SELECT "+e.SQL, e.Vars...).Scan(&got).Error; err != nil {
			t.Fatalf("eval %q: %v", e.SQL, err)
		}
		return got
	}

	pairs := [][2]int64{
		{2500, math.MaxInt64}, {2500, 5}, {math.MaxInt64, 1},
		{math.MaxInt64 - 3, 10}, {0, -5}, {math.MinInt64 + 3, -10},
		{100, -250}, {math.MaxInt64, math.MaxInt64},
		{math.MinInt64, math.MinInt64}, {5, 0}, {-7, 3},
		{math.MinInt64, -1}, {math.MaxInt64, -1},
	}
	for _, p := range pairs {
		balance, delta := p[0], p[1]
		want := saturatingAddInt64(balance, delta)
		// 新表达式以字面量充当列位求值（column 参数原样拼入 SQL）。
		colLit := strconv.FormatInt(balance, 10)
		if got := eval(saturatingCounterIncrExpr(colLit, delta)); got != want {
			t.Fatalf("new expr: balance=%d delta=%d: got %d, want %d", balance, delta, got, want)
		}
		if got := eval(oldExpr(balance, delta)); got != want {
			t.Fatalf("old expr: balance=%d delta=%d: got %d, want %d (equivalence baseline broken)", balance, delta, got, want)
		}
	}
}
