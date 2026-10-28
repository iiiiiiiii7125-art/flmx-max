package handler

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// F-02 回归：ActivateForwardIfNotPending 必须是原子条件更新，
// 并发删除不得把 pending-delete 行复活为 active。
// 修复前：串行断言通过，但竞态测试可观测到复活（红）；
// 修复后：竞态测试零复活（绿）。

func newF02Handler(t *testing.T) (*Handler, int64) {
	t.Helper()
	r, err := repo.Open(filepath.Join(t.TempDir(), "f02.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	if err := r.DB().Create(&model.Node{ID: 1, Name: "f02-node", Secret: "f02-node-secret", ServerIP: "127.0.0.1", Port: "30000", Status: 1}).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	fwd := model.Forward{UserID: 1, UserName: "admin", Name: "f02-line", TunnelID: 1, RemoteAddr: "203.0.113.1:443", Status: model.ForwardStatusActive, CreatedTime: now, UpdatedTime: now}
	if err := r.DB().Create(&fwd).Error; err != nil {
		t.Fatal(err)
	}
	return &Handler{repo: r}, fwd.ID
}

func f02NewForward(t *testing.T, h *Handler, name string) int64 {
	t.Helper()
	now := time.Now().UnixMilli()
	fwd := model.Forward{UserID: 1, UserName: "admin", Name: name, TunnelID: 1, RemoteAddr: "203.0.113.1:443", Status: model.ForwardStatusActive, CreatedTime: now, UpdatedTime: now}
	if err := h.repo.DB().Create(&fwd).Error; err != nil {
		t.Fatal(err)
	}
	return fwd.ID
}

func f02ReadStatus(h *Handler, id int64) (int, error) {
	fr, err := h.repo.GetForwardRecord(id)
	if err != nil || fr == nil {
		return 0, err
	}
	return fr.Status, nil
}

func f02ForwardStatus(t *testing.T, h *Handler, id int64) int {
	t.Helper()
	st, err := f02ReadStatus(h, id)
	if err != nil {
		t.Fatalf("read forward %d: %v", id, err)
	}
	return st
}

func TestActivateForwardIfNotPendingRejectsPendingDelete(t *testing.T) {
	h, id := newF02Handler(t)
	now := time.Now().UnixMilli()
	// 待删行：激活必须被拒、状态保持 pending-delete。
	if err := h.repo.MarkForwardPendingDelete(id, 1, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := h.ActivateForwardIfNotPending(id, now); !errors.Is(err, errForwardPendingDelete) {
		t.Fatalf("pending-delete activation must return errForwardPendingDelete, got %v", err)
	}
	if got := f02ForwardStatus(t, h, id); got != model.ForwardStatusPendingDelete {
		t.Fatalf("pending-delete row must stay status=2, got %d", got)
	}
}

func TestActivateForwardIfNotPendingActivatesNonPending(t *testing.T) {
	h, id := newF02Handler(t)
	now := time.Now().UnixMilli()
	// 暂停行：正常激活回归。
	if err := h.repo.DB().Model(&model.Forward{}).Where("id = ?", id).Update("status", 0).Error; err != nil {
		t.Fatal(err)
	}
	if err := h.ActivateForwardIfNotPending(id, now); err != nil {
		t.Fatalf("activating paused forward must succeed, got %v", err)
	}
	if got := f02ForwardStatus(t, h, id); got != model.ForwardStatusActive {
		t.Fatalf("activated forward must be status=1, got %d", got)
	}
	// 已 active 行：重复激活仍成功。
	if err := h.ActivateForwardIfNotPending(id, now); err != nil {
		t.Fatalf("re-activating active forward must succeed, got %v", err)
	}
	// 不存在的行：返回错误（与修复前 getForwardRecord 行为一致）。
	if err := h.ActivateForwardIfNotPending(id+1000, now); err == nil {
		t.Fatal("activating missing forward must return error")
	}
}

func TestUpdateForwardStatusIfNotPendingConditional(t *testing.T) {
	h, id := newF02Handler(t)
	now := time.Now().UnixMilli()
	// active 行：条件更新命中。
	updated, err := h.repo.UpdateForwardStatusIfNotPending(id, model.ForwardStatusActive, now)
	if err != nil || !updated {
		t.Fatalf("conditional update on active row must hit: updated=%v err=%v", updated, err)
	}
	// pending 行：条件更新 0 行、状态不变。
	if err := h.repo.MarkForwardPendingDelete(id, 1, time.Now()); err != nil {
		t.Fatal(err)
	}
	updated, err = h.repo.UpdateForwardStatusIfNotPending(id, model.ForwardStatusActive, now)
	if err != nil || updated {
		t.Fatalf("conditional update on pending row must miss: updated=%v err=%v", updated, err)
	}
	if got := f02ForwardStatus(t, h, id); got != model.ForwardStatusPendingDelete {
		t.Fatalf("pending row must stay status=2, got %d", got)
	}
	// 不存在的行：0 行、无错误。
	updated, err = h.repo.UpdateForwardStatusIfNotPending(id+1000, model.ForwardStatusActive, now)
	if err != nil || updated {
		t.Fatalf("conditional update on missing row must miss: updated=%v err=%v", updated, err)
	}
}

// f02RaceRound 对打一轮：8 个激活 goroutine 与 8 个删除 goroutine
// 并发 2 秒。返回本轮观测到的复活次数（0 为干净）与删除次数。
func f02RaceRound(t *testing.T, h *Handler, id int64) (resurrections int64, marks int64) {
	t.Helper()
	// 判定器（无假阳性）：某次删除已完成后才开始的 Activate 若返回
	// 成功，说明它读到 status=1——而删除完成后 status 为 2、唯一能
	// 把它写回 1 的就是另一次交错的 Activate，即已发生复活。
	// 另附采样器作第二判定：先见 2 后见 1 即复活（窗口极短，仅作佐证）。
	stop := make(chan struct{})
	var markCount atomic.Int64
	var resurrected atomic.Int64
	var onceTwo atomic.Bool
	var samplerSawResurrection atomic.Bool
	var samplerDone sync.WaitGroup
	samplerDone.Add(1)
	go func() {
		defer samplerDone.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			st, err := f02ReadStatus(h, id)
			if err != nil {
				continue
			}
			if st == model.ForwardStatusPendingDelete {
				onceTwo.Store(true)
			} else if onceTwo.Load() {
				samplerSawResurrection.Store(true)
			}
			time.Sleep(50 * time.Microsecond)
		}
	}()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				marksBefore := markCount.Load()
				if err := h.ActivateForwardIfNotPending(id, time.Now().UnixMilli()); err == nil && marksBefore > 0 {
					resurrected.Add(1)
				}
			}
		}()
	}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if err := h.repo.MarkForwardPendingDelete(id, 1, time.Now()); err == nil {
					markCount.Add(1)
				}
			}
		}()
	}
	time.Sleep(2 * time.Second)
	close(stop)
	wg.Wait()
	samplerDone.Wait()
	if samplerSawResurrection.Load() {
		resurrected.Add(1)
	}
	return resurrected.Load(), markCount.Load()
}

func TestActivateForwardIfNotPendingRaceWithPendingDelete(t *testing.T) {
	h, _ := newF02Handler(t)
	// 单轮命中率约 5/6（修复前实测），跑至多 4 轮、首轮见复活即失败；
	// 修复后每轮恒干净。轮次用新转发，避免上一轮的 pending 状态干扰。
	for round := 1; round <= 4; round++ {
		id := f02NewForward(t, h, fmt.Sprintf("f02-race-%d", round))
		resurrections, marks := f02RaceRound(t, h, id)
		if marks == 0 {
			t.Fatalf("round %d: race harness never marked pending-delete", round)
		}
		if resurrections > 0 {
			t.Fatalf("round %d: pending-delete forward was resurrected to active %d times during concurrent delete+activate", round, resurrections)
		}
		// 收尾：串行再标记一次，行必须稳定在 pending-delete。
		if err := h.repo.MarkForwardPendingDelete(id, 1, time.Now()); err != nil {
			t.Fatal(err)
		}
		if got := f02ForwardStatus(t, h, id); got != model.ForwardStatusPendingDelete {
			t.Fatalf("round %d: forward must end pending-delete, got status=%d", round, got)
		}
	}
}
