package ws

import (
	"testing"
	"time"
)

// R-WS-TICKET：票据存储语义——单次使用、过期即废、未知票拒。
func TestWSTicketStoreIssueConsumeOnce(t *testing.T) {
	st := newWSTicketStore()
	id, err := st.issue(wsTicket{userID: 7, roleID: 0, user: "admin", iatMs: 100, expUnix: 200, epoch: 50})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if id == "" {
		t.Fatal("empty ticket id")
	}
	got, ok := st.consume(id)
	if !ok {
		t.Fatal("first consume must succeed")
	}
	if got.userID != 7 || got.roleID != 0 || got.user != "admin" || got.iatMs != 100 || got.expUnix != 200 || got.epoch != 50 {
		t.Fatalf("ticket payload drifted: %+v", got)
	}
	if _, ok := st.consume(id); ok {
		t.Fatal("second consume of the same ticket must fail (single-use)")
	}
}

func TestWSTicketStoreRejectsUnknownAndEmpty(t *testing.T) {
	st := newWSTicketStore()
	if _, ok := st.consume(""); ok {
		t.Fatal("empty ticket id must fail")
	}
	if _, ok := st.consume("no-such-ticket"); ok {
		t.Fatal("unknown ticket must fail")
	}
}

func TestWSTicketStoreExpiry(t *testing.T) {
	st := newWSTicketStore()
	base := time.Unix(1_700_000_000, 0)
	st.now = func() time.Time { return base }
	id, err := st.issue(wsTicket{userID: 7})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	st.now = func() time.Time { return base.Add(WSTicketTTL) }
	if _, ok := st.consume(id); ok {
		t.Fatal("ticket at TTL boundary must be expired")
	}
	// 过期消费尝试同样烧票：时钟回拨也不能复活。
	st.now = func() time.Time { return base }
	if _, ok := st.consume(id); ok {
		t.Fatal("expired-then-consumed ticket must stay burned")
	}
}

func TestWSTicketStoreIssueSweepsExpired(t *testing.T) {
	st := newWSTicketStore()
	base := time.Unix(1_700_000_000, 0)
	st.now = func() time.Time { return base }
	for i := 0; i < 3; i++ {
		if _, err := st.issue(wsTicket{userID: int64(i)}); err != nil {
			t.Fatalf("issue %d: %v", i, err)
		}
	}
	st.now = func() time.Time { return base.Add(2 * WSTicketTTL) }
	if _, err := st.issue(wsTicket{userID: 99}); err != nil {
		t.Fatalf("issue after expiry: %v", err)
	}
	if len(st.byID) != 1 {
		t.Fatalf("expired tickets must be swept on issue, outstanding = %d", len(st.byID))
	}
}
