package handler

import (
	"context"
	"strings"
	"testing"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// R18-B6-02: a template deleted while a refresh was in flight was
// resurrected by SaveSubscriptionTemplate's GORM upsert (Save() with
// a non-zero ID re-inserts). refreshTemplate re-checks existence
// before saving; a deleted template stays deleted.
func TestR18B602RefreshAfterDeleteDoesNotResurrect(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/b602-refresh-race.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")

	tpl := &model.SubscriptionTemplate{Name: "b6-02-tpl", Kind: "clash",
		SourceURL: "http://127.0.0.1:1/b6-02-template.yaml", Content: "proxies: []\n"}
	if err := r.SaveSubscriptionTemplate(tpl); err != nil {
		t.Fatalf("seed template: %v", err)
	}
	// The admin deletes the template while its refresh is in flight
	// (here the delete lands before refresh starts; the re-check sits
	// after the fetch either way, so the outcome is the same).
	if err := r.DeleteSubscriptionTemplate(tpl.ID); err != nil {
		t.Fatalf("delete template: %v", err)
	}
	err = h.refreshTemplate(context.Background(), tpl)
	if err == nil || !strings.Contains(err.Error(), "已被删除") {
		t.Fatalf("refresh of a deleted template must report it as deleted, got %v", err)
	}
	if got, err := r.GetSubscriptionTemplate(tpl.ID); err != nil || got != nil {
		t.Fatalf("deleted template must stay deleted after refresh, got %+v err=%v", got, err)
	}
}
