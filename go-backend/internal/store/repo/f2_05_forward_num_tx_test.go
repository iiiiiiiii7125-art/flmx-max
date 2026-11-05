package repo

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"go-backend/internal/store/model"
)

// TestCreateForwardTxNumQuotaF205 is the F2-05 regression: the
// forward-count quota (user.Num and the (user,tunnel) grant's Num)
// must be enforced INSIDE CreateForwardTx's transaction, under the
// user-row lock. The handler pre-check alone races: at baseline,
// Num=1 with 10 concurrent creates produced 9 forwards (live-c).
// Pre-fix every create below succeeds, including the second one.
func TestCreateForwardTxNumQuotaF205(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "f205.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()
	now := int64(1762387200000)

	seed := func(row interface{}) {
		t.Helper()
		if err := r.DB().Create(row).Error; err != nil {
			t.Fatalf("seed %T: %v", row, err)
		}
	}
	seed(&model.Tunnel{ID: 601, Name: "t601", TrafficRatio: 1, Type: 1, Protocol: "tls", Flow: 1, CreatedTime: now, UpdatedTime: now, Status: 1})
	seed(&model.User{ID: 501, User: "u501", RoleID: 1, Status: 1, Num: 1, CreatedTime: now})
	seed(&model.User{ID: 502, User: "u502", RoleID: 1, Status: 1, Num: 0, CreatedTime: now})
	seed(&model.UserTunnel{ID: 901, UserID: 502, TunnelID: 601, Num: 1, Status: 1})
	seed(&model.User{ID: 503, User: "u503", RoleID: 1, Status: 1, Num: 1, CreatedTime: now})

	create := func(userID int64, name string) error {
		_, err := r.CreateForwardTx(userID, "u", name, 601, "1.1.1.1:80", "fifo", now, 1, nil, 0, "", nil, 0, 0, nil, 0, 0, 0)
		return err
	}

	// User-level Num=1: first create passes, second is rejected
	// inside the transaction with the quota sentinel.
	if err := create(501, "f501-a"); err != nil {
		t.Fatalf("first create for user 501: %v", err)
	}
	if err := create(501, "f501-b"); !errors.Is(err, ErrForwardNumExceeded) {
		t.Fatalf("F2-05: second create for Num=1 user: err=%v, want ErrForwardNumExceeded", err)
	}

	// (user,tunnel)-level Num=1 with unlimited user Num.
	if err := create(502, "f502-a"); err != nil {
		t.Fatalf("first create for user 502: %v", err)
	}
	if err := create(502, "f502-b"); !errors.Is(err, ErrUserTunnelForwardNumExceeded) {
		t.Fatalf("F2-05: second create over grant Num=1: err=%v, want ErrUserTunnelForwardNumExceeded", err)
	}

	// Concurrency: 10 simultaneous creates against Num=1 must
	// produce exactly one forward.
	var wg sync.WaitGroup
	var mu sync.Mutex
	successes := 0
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := create(503, "f503"); err == nil {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if successes != 1 {
		t.Fatalf("F2-05: concurrent creates at Num=1 produced %d forwards, want exactly 1", successes)
	}
	var cnt int64
	if err := r.DB().Model(&model.Forward{}).Where("user_id = ? AND status = 1", 503).Count(&cnt).Error; err != nil {
		t.Fatalf("count forwards: %v", err)
	}
	if cnt != 1 {
		t.Fatalf("F2-05: persisted active forwards for user 503 = %d, want 1", cnt)
	}
}
