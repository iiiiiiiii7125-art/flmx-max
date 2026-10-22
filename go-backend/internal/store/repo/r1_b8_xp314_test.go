package repo

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// X-P3-14 (L-03-3) repo 侧：订阅 token/凭据生成不再忽略 rand 错误。
func TestR1B8_XP314_RepoRandFailureFailsClosed(t *testing.T) {
	orig := cryptoRandRead
	cryptoRandRead = func([]byte) (int, error) { return 0, errors.New("rand boom") }
	defer func() { cryptoRandRead = orig }()

	if _, err := randomToken(); err == nil {
		t.Fatalf("repo randomToken 在 rand 失败时必须返回错误")
	}
	if _, _, err := subscriptionCredentials(); err == nil {
		t.Fatalf("subscriptionCredentials 在 rand 失败时必须返回错误")
	}

	// 先以健康 rand 打开（Open 会种子默认订阅），删掉默认订阅行后在
	// rand 失败下走「无订阅则新建默认订阅」路径，必须报错而非落库。
	cryptoRandRead = orig
	r, err := Open(filepath.Join(t.TempDir(), "xp314.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.DB().Exec(`DELETE FROM subscription`).Error; err != nil {
		t.Fatal(err)
	}
	cryptoRandRead = func([]byte) (int, error) { return 0, errors.New("rand boom") }
	if _, err := r.DefaultSubscription(time.Now().UnixMilli()); err == nil {
		t.Fatalf("DefaultSubscription 在 rand 失败时必须报错，不得落全零/可预测凭据")
	}
	var n int64
	if err := r.DB().Raw(`SELECT COUNT(*) FROM subscription`).Row().Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("rand 失败时不应落订阅行, got %d", n)
	}
}
