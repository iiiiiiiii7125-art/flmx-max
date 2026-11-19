package repo

import (
	"strings"
	"testing"
)

// F2-PORT-01 P0-2：带排除集的随机选口。排除集内的口不得再被挑中；
// 排除整段后必须明确报「节点端口已满」，不得空转或回落已排除的口。

func TestPickRandomNodePortExcludingHonorsTriedPorts(t *testing.T) {
	r := openCascadeRepo(t, "k9-pick-excl.db")
	seedK9Node(t, r)
	db := r.DB()

	// 节点规格只有 36005-36006：排除 36005 → 只剩 36006。
	picked, err := r.PickRandomNodePortExcludingTx(db, k9NodeID, map[int64]int{}, 0, map[int]bool{36005: true})
	if err != nil {
		t.Fatalf("pick excluding 36005: %v", err)
	}
	if picked != 36006 {
		t.Fatalf("picked = %d, want 36006 (only remaining port)", picked)
	}

	// 排除整段 → 明确报端口已满。
	if _, err := r.PickRandomNodePortExcludingTx(db, k9NodeID, map[int64]int{}, 0, map[int]bool{36005: true, 36006: true}); err == nil {
		t.Fatal("excluding the whole range must fail, got no error")
	} else if !strings.Contains(err.Error(), "节点端口已满") {
		t.Fatalf("exhausted pick error = %q, want 节点端口已满", err.Error())
	}

	// DB 占用与排除集叠加：36005 被转发占、36006 被排除 → 无可用端口。
	seedK9Forward(t, r)
	if _, err := r.PickRandomNodePortExcludingTx(db, k9NodeID, map[int64]int{}, 0, map[int]bool{36006: true}); err == nil {
		t.Fatal("DB-occupied 36005 + excluded 36006 must fail, got no error")
	} else if !strings.Contains(err.Error(), "节点端口已满") {
		t.Fatalf("exhausted pick error = %q, want 节点端口已满", err.Error())
	}

	// 无排除集时口径不变：不得挑中 DB 占用的 36005。
	for i := 0; i < 8; i++ {
		picked, err := r.PickRandomNodePortExcludingTx(db, k9NodeID, map[int64]int{}, 0, nil)
		if err != nil {
			t.Fatalf("pick without exclusions: %v", err)
		}
		if picked != 36006 {
			t.Fatalf("picked = %d, want 36006 (must avoid DB-occupied 36005)", picked)
		}
	}
}
