package repo

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"go-backend/internal/store/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Legacy settings (3.0.10) that became the default subscription.
const (
	legacySubTokenKey = "node_sub_token"
	legacySubRename   = "sub_rename_rules"
	legacySubPrefix   = "sub_name_prefix"
	legacySubSort     = "sub_sort"
	legacySubRegions  = "sub_region_groups"
)

// cryptoRandRead 是凭据随机源的测试 seam（生产恒为 crypto/rand.Read）。
var cryptoRandRead = rand.Read

// randomToken / subscriptionCredentials：X-P3-14（L-03-3）随机源
// 失败 fail-closed 返回错误。旧实现忽略 rand 错误、落全零 token/
// 凭据（可预测），调用方必须把错误上抛、不得落库。
func randomToken() (string, error) {
	b := make([]byte, 16)
	if _, err := cryptoRandRead(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// subscriptionCredentials returns a fresh UUID and password for a
// subscription's own credentials.
func subscriptionCredentials() (string, string, error) {
	b := make([]byte, 16)
	if _, err := cryptoRandRead(b); err != nil {
		return "", "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	token, err := randomToken()
	if err != nil {
		return "", "", err
	}
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32], token, nil
}

// newDefaultSubscription is "我的订阅", with its own credentials like every
// subscription.
func newDefaultSubscription(token, options string, now int64) (model.Subscription, error) {
	id, pw, err := subscriptionCredentials()
	if err != nil {
		return model.Subscription{}, err
	}
	return model.Subscription{
		Name: "我的订阅", Kind: model.SubscriptionShare, Token: token, UUID: id, Password: pw, SourceAll: 1,
		Options: options, Enabled: 1, CreatedTime: now, UpdatedTime: now,
	}, nil
}

// shareOwnSubscriptions gives subscriptions that used the owner's
// credentials ("own", before every subscription had its own) their own
// credentials. AppliedActive stays 0, so the sync loop pushes them to the
// kernels; the link (token) is kept.
func shareOwnSubscriptions(db *gorm.DB) error {
	var own []model.Subscription
	if err := db.Where("kind <> ?", model.SubscriptionShare).Find(&own).Error; err != nil {
		return err
	}
	for _, s := range own {
		fields := map[string]interface{}{"kind": model.SubscriptionShare, "applied_active": 0}
		if s.UUID == "" || s.Password == "" {
			uuid, pw, credErr := subscriptionCredentials()
			if credErr != nil {
				return credErr
			}
			fields["uuid"], fields["password"] = uuid, pw
		}
		if err := db.Model(&model.Subscription{}).Where("id = ?", s.ID).Updates(fields).Error; err != nil {
			return err
		}
	}
	return nil
}

// migrateSubscriptions makes sure there is at least one subscription and
// that every subscription has its own credentials. On upgrade the old
// single subscription (token and naming settings) becomes the default one
// and shared users become subscriptions, keeping their links and
// credentials.
func migrateSubscriptions(db *gorm.DB, now int64) error {
	if err := shareOwnSubscriptions(db); err != nil {
		return err
	}
	var count int64
	if err := db.Model(&model.Subscription{}).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	return db.Transaction(func(tx *gorm.DB) error {
		cfg := map[string]string{}
		var rows []model.ViteConfig
		if err := tx.Where("name IN ?", []string{legacySubTokenKey, legacySubRename, legacySubPrefix, legacySubSort, legacySubRegions}).
			Find(&rows).Error; err != nil {
			return err
		}
		for _, r := range rows {
			cfg[r.Name] = r.Value
		}
		opts, _ := json.Marshal(map[string]interface{}{
			"rename":       cfg[legacySubRename],
			"prefix":       cfg[legacySubPrefix],
			"sort":         cfg[legacySubSort],
			"regionGroups": cfg[legacySubRegions] == "true",
		})
		token := cfg[legacySubTokenKey]
		if len(token) < 32 {
			var tokenErr error
			token, tokenErr = randomToken()
			if tokenErr != nil {
				return tokenErr
			}
		}
		def, defErr := newDefaultSubscription(token, string(opts), now)
		if defErr != nil {
			return defErr
		}
		if err := tx.Create(&def).Error; err != nil {
			return err
		}

		var users []model.SubUser
		if err := tx.Order("id ASC").Find(&users).Error; err != nil {
			return err
		}
		for i, u := range users {
			sub := model.Subscription{
				Name: u.Name, Kind: model.SubscriptionShare, Token: u.Token, UUID: u.UUID, Password: u.Password,
				SourceAll: 0, InboundIDs: u.InboundIDs, Options: string(opts), ExpireTime: u.ExpireTime,
				Enabled: u.Enabled, AppliedActive: u.AppliedActive, Remark: u.Remark, Inx: i + 1,
				CreatedTime: u.CreatedTime, UpdatedTime: now,
			}
			if err := tx.Create(&sub).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("1 = 1").Delete(&model.SubUser{}).Error; err != nil {
			return err
		}
		return tx.Where("name IN ?", []string{legacySubTokenKey, legacySubRename, legacySubPrefix, legacySubSort, legacySubRegions}).
			Delete(&model.ViteConfig{}).Error
	})
}

func (r *Repository) ListSubscriptions() ([]model.Subscription, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var out []model.Subscription
	err := r.db.Order("inx ASC, id ASC").Find(&out).Error
	return out, err
}

func (r *Repository) GetSubscription(id int64) (*model.Subscription, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var s model.Subscription
	res := r.db.Where("id = ?", id).Limit(1).Find(&s)
	if res.Error != nil || res.RowsAffected == 0 {
		return nil, res.Error
	}
	return &s, nil
}

func (r *Repository) GetSubscriptionByToken(token string) (*model.Subscription, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var s model.Subscription
	res := r.db.Where("token = ?", token).Limit(1).Find(&s)
	if res.Error != nil || res.RowsAffected == 0 {
		return nil, res.Error
	}
	return &s, nil
}

// DefaultSubscription is the first subscription (the one the old
// single-subscription API refers to), created if none exists.
func (r *Repository) DefaultSubscription(now int64) (*model.Subscription, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	// R5-B4 C-011: 查 + 建在同一事务内。
	// 注意：仅解决单进程并发。依赖单实例部署（passkey.md 已声明）；
	// 多实例部署下仍有窗口，需外部协调（已知限制，不声称彻底解决）。
	var s model.Subscription
	err := r.db.Transaction(func(tx *gorm.DB) error {
		res := tx.Order("inx ASC, id ASC").Limit(1).Find(&s)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected > 0 {
			return nil
		}
		token, tokenErr := randomToken()
		if tokenErr != nil {
			return tokenErr
		}
		def, defErr := newDefaultSubscription(token, "", now)
		if defErr != nil {
			return defErr
		}
		s = def
		return tx.Create(&s).Error
	})
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *Repository) SaveSubscription(s *model.Subscription) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return saveSubscriptionTx(r.db, s)
}

func saveSubscriptionTx(db *gorm.DB, s *model.Subscription) error {
	// Traffic columns are only changed by the counter updates, so a save
	// based on a stale read cannot drop traffic counted meanwhile.
	if s.ID == 0 {
		return db.Create(s).Error
	}
	return db.Omit("upload_bytes", "download_bytes", "traffic_reset_time").Save(s).Error
}

// SaveSubscriptionWithResetTime 在同一事务内落重置时间与订阅本体。
// X-P3-24（L-09-6）：subscriptionSave 旧实现先单独 Mark 重置时间再
// Save，后者失败时重置时间已改而 ResetDay 未改（下次保存才自愈）。
func (r *Repository) SaveSubscriptionWithResetTime(s *model.Subscription, resetTime int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		if s.ID > 0 {
			if err := tx.Model(&model.Subscription{}).Where("id = ?", s.ID).
				Update("traffic_reset_time", resetTime).Error; err != nil {
				return err
			}
		}
		return saveSubscriptionTx(tx, s)
	})
}

func (r *Repository) DeleteSubscription(id int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("subscription_id = ?", id).Delete(&model.SubscriptionCounter{}).Error; err != nil {
			return err
		}
		if err := tx.Where("subscription_id = ?", id).Delete(&model.SubscriptionTraffic{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", id).Delete(&model.Subscription{}).Error
	})
}

func (r *Repository) UpdateSubscriptionApplied(id int64, active int) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Model(&model.Subscription{}).Where("id = ?", id).Update("applied_active", active).Error
}

// SubscriptionCounterSample is one cumulative per-user counter of a node.
type SubscriptionCounterSample struct {
	SubscriptionID int64
	Direction      string // "up" or "down"
	Bytes          int64
}

// ApplySubscriptionCounterSamples adds what each counter grew by since the
// last sample of the same epoch to the subscriptions' traffic. A missing
// baseline and an epoch change only (re)establish the baseline and count
// nothing (FIX2-A V2-05 adjudication, aligned with the forward-counter
// path); a same-epoch rollback counts the whole current value, which is
// traffic accrued since the in-epoch counter reset.
func (r *Repository) ApplySubscriptionCounterSamples(nodeID int64, epoch string, samples []SubscriptionCounterSample, now int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	if len(samples) == 0 {
		return nil
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		var states []model.SubscriptionCounter
		if err := tx.Where("node_id = ?", nodeID).Find(&states).Error; err != nil {
			return err
		}
		type key struct {
			id  int64
			dir string
		}
		prev := make(map[key]model.SubscriptionCounter, len(states))
		for _, st := range states {
			prev[key{st.SubscriptionID, st.Direction}] = st
		}
		// F-075（A4-004）：同一批内同 (subscription, direction) 的
		// 重复样本先合并（保留最大 Bytes）再算增量。旧实现 prev 只
		// 由 DB 旧状态构建、批次内不推进，两条样本各按同一旧基线算
		// delta 并累加，增量被重复计入一次（流量虚增、可提前触发
		// 订阅流量上限断开）。
		merged := make(map[key]SubscriptionCounterSample, len(samples))
		mergedOrder := make([]key, 0, len(samples))
		for _, s := range samples {
			k := key{s.SubscriptionID, s.Direction}
			if cur, ok := merged[k]; ok {
				if s.Bytes > cur.Bytes {
					merged[k] = s
				}
				continue
			}
			merged[k] = s
			mergedOrder = append(mergedOrder, k)
		}
		ordered := make([]SubscriptionCounterSample, 0, len(mergedOrder))
		for _, k := range mergedOrder {
			ordered = append(ordered, merged[k])
		}

		up := map[int64]int64{}
		down := map[int64]int64{}
		for _, s := range ordered {
			if s.Bytes < 0 || (s.Direction != "up" && s.Direction != "down") {
				continue
			}
			old, seen := prev[key{s.SubscriptionID, s.Direction}]
			var delta int64
			switch {
			case !seen:
				// A3-04/X2：基线缺失只建基线、不计增量（与 nft 转发
				// 路径 buildNftCounterDeltas 同口径）。旧语义把整段
				// 累计当增量：干净恢复（基线未随备份）或基线行丢失后
				// 首个样本会把历史流量重复计入一遍，可把正常订阅顶
				// 过上限被停用（X2 探针 D：1000/2000 → 2000/4000）。
				delta = 0
			case old.Epoch != epoch:
				// FIX2-A（V2-05，主控裁定）：epoch 变化视同基线重
				// 建——只推进基线、不把新 epoch 的累计值计入用量，
				// 与转发侧（nft/realm hash 变化同语义）及上方首采基
				// 线同口径，消除「同节点 agent 重启后订阅计、realm
				// 转发漏计」的口径分叉。旧语义对 epoch 变化全量计
				// （累计自零起整段计入），本裁定 supersede 之：代价
				// 是重启到首采样之间的订阅流量不计，与转发侧一致。
				delta = 0
			case s.Bytes >= old.Bytes:
				delta = s.Bytes - old.Bytes
			default:
				// 同 epoch 计数回退（计数器在同代内重置）：当前值
				// 整段为重置后未计过的新流量，全量计（既有语义
				// 不变；与 epoch 变化的区别是代际未换、不存在跨
				// 代累计归属歧义）。
				delta = s.Bytes
			}
			if delta > 0 {
				if s.Direction == "up" {
					up[s.SubscriptionID] += delta
				} else {
					down[s.SubscriptionID] += delta
				}
			}
			row := model.SubscriptionCounter{NodeID: nodeID, SubscriptionID: s.SubscriptionID, Direction: s.Direction,
				Epoch: epoch, Bytes: s.Bytes, UpdatedTime: now}
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "node_id"}, {Name: "subscription_id"}, {Name: "direction"}},
				DoUpdates: clause.AssignmentColumns([]string{"epoch", "bytes", "updated_time"}),
			}).Create(&row).Error; err != nil {
				return err
			}
		}
		ids := map[int64]bool{}
		for id := range up {
			ids[id] = true
		}
		for id := range down {
			ids[id] = true
		}
		// #88（3.3.29）：同一事务顺带按当前小时记桶。up/down 已是
		// F-075 合并后的最终增量，桶累加与累计值严格同源。
		hour := now - now%int64(time.Hour/time.Millisecond)
		var hourly []model.SubscriptionTraffic
		for id := range ids {
			if err := tx.Model(&model.Subscription{}).Where("id = ?", id).Updates(map[string]interface{}{
				"upload_bytes":   gorm.Expr("upload_bytes + ?", up[id]),
				"download_bytes": gorm.Expr("download_bytes + ?", down[id]),
			}).Error; err != nil {
				return err
			}
			hourly = append(hourly, model.SubscriptionTraffic{SubscriptionID: id, Hour: hour, UploadBytes: up[id], DownloadBytes: down[id]})
		}
		if len(hourly) == 0 {
			return nil
		}
		// The hour's history, for the traffic charts.
		return tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "subscription_id"}, {Name: "hour"}},
			DoUpdates: clause.Assignments(map[string]interface{}{
				"upload_bytes":   gorm.Expr("subscription_traffic.upload_bytes + excluded.upload_bytes"),
				"download_bytes": gorm.Expr("subscription_traffic.download_bytes + excluded.download_bytes"),
			}),
		}).Create(&hourly).Error
	})
}

// ListSubscriptionTraffic returns a subscription's hours from fromMs on,
// oldest first.
func (r *Repository) ListSubscriptionTraffic(id, fromMs int64) ([]model.SubscriptionTraffic, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var out []model.SubscriptionTraffic
	err := r.db.Where("subscription_id = ? AND hour >= ?", id, fromMs).Order("hour ASC").Find(&out).Error
	return out, err
}

// PurgeSubscriptionTraffic drops the hours before beforeMs.
func (r *Repository) PurgeSubscriptionTraffic(beforeMs int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Where("hour < ?", beforeMs).Delete(&model.SubscriptionTraffic{}).Error
}

// ResetSubscriptionTraffic zeroes a subscription's traffic.
func (r *Repository) ResetSubscriptionTraffic(id int64, now int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Model(&model.Subscription{}).Where("id = ?", id).Updates(map[string]interface{}{
		"upload_bytes": 0, "download_bytes": 0, "traffic_reset_time": now,
	}).Error
}

// MarkSubscriptionResetTime records when the traffic was last reset.
func (r *Repository) MarkSubscriptionResetTime(id int64, at int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Model(&model.Subscription{}).Where("id = ?", id).Update("traffic_reset_time", at).Error
}
