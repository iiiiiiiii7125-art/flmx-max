package repo

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	gsqlite "github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"

	"go-backend/internal/kernel"
	"go-backend/internal/security"
	"go-backend/internal/store/model"
)

const (
	defaultPostgresMaxOpenConns = 32
	defaultPostgresMaxIdleConns = 8
	defaultPostgresConnMaxIdle  = 5 * time.Minute
	defaultPostgresConnMaxLife  = 30 * time.Minute
)

// ─── Type aliases for backward compatibility ─────────────────────────
// Handlers still reference repo.User, repo.BackupData, etc.

type User = model.User
type ViteConfig = model.ViteConfig
type Announcement = model.Announcement
type UserTunnelDetail = model.UserTunnelDetail
type UserForwardDetail = model.UserForwardDetail
type StatisticsFlow = model.StatisticsFlow
type Node = model.Node
type PeerShare = model.PeerShare
type PeerShareResource = model.PeerShareResource
type PeerShareRuntime = model.PeerShareRuntime
type FederationTunnelBinding = model.FederationTunnelBinding
type BackupData = model.BackupData
type UserBackup = model.UserBackup
type NodeBackup = model.NodeBackup
type TunnelBackup = model.TunnelBackup
type ChainTunnelBackup = model.ChainTunnelBackup
type ForwardBackup = model.ForwardBackup
type ForwardPortBackup = model.ForwardPortBackup
type UserTunnelBackup = model.UserTunnelBackup
type SpeedLimitBackup = model.SpeedLimitBackup
type TunnelGroupBackup = model.TunnelGroupBackup
type UserGroupBackup = model.UserGroupBackup
type PermissionBackup = model.PermissionBackup
type PermissionGrantBackup = model.PermissionGrantBackup
type ImportResult = model.ImportResult
type NodeMetric = model.NodeMetric
type TunnelMetric = model.TunnelMetric
type ServiceMonitor = model.ServiceMonitor
type ServiceMonitorResult = model.ServiceMonitorResult
type TunnelQuality = model.TunnelQuality

// ─── Repository ──────────────────────────────────────────────────────

type Repository struct {
	db     *gorm.DB
	dbPath string
}

type FlowUploadCounterDelta struct {
	ForwardID    int64
	UserID       int64
	UserTunnelID int64
	InFlow       int64
	OutFlow      int64
}

func (r *Repository) DB() *gorm.DB {
	if r == nil {
		return nil
	}
	return r.db
}

func sortedFlowUploadTargetIDs(totals map[int64][2]int64) []int64 {
	ids := make([]int64, 0, len(totals))
	for id := range totals {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func (r *Repository) ApplyFlowUploadDeltasBatch(deltas []FlowUploadCounterDelta) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	if len(deltas) == 0 {
		return nil
	}

	return r.db.Transaction(func(tx *gorm.DB) error {
		return applyFlowUploadDeltasTx(tx, deltas)
	})
}

func applyFlowUploadDeltasTx(tx *gorm.DB, deltas []FlowUploadCounterDelta) error {
	if tx == nil {
		return errors.New("database unavailable")
	}
	if len(deltas) == 0 {
		return nil
	}

	forwardTotals := make(map[int64][2]int64, len(deltas))
	userTotals := make(map[int64][2]int64, len(deltas))
	userTunnelTotals := make(map[int64][2]int64, len(deltas))
	for _, delta := range deltas {
		if delta.ForwardID > 0 {
			current := forwardTotals[delta.ForwardID]
			current[0] += delta.InFlow
			current[1] += delta.OutFlow
			forwardTotals[delta.ForwardID] = current
		}
		if delta.UserID > 0 {
			current := userTotals[delta.UserID]
			current[0] += delta.InFlow
			current[1] += delta.OutFlow
			userTotals[delta.UserID] = current
		}
		if delta.UserTunnelID > 0 {
			current := userTunnelTotals[delta.UserTunnelID]
			current[0] += delta.InFlow
			current[1] += delta.OutFlow
			userTunnelTotals[delta.UserTunnelID] = current
		}
	}

	for _, forwardID := range sortedFlowUploadTargetIDs(forwardTotals) {
		total := forwardTotals[forwardID]
		if err := tx.Model(&model.Forward{}).Where("id = ?", forwardID).UpdateColumns(map[string]interface{}{
			"in_flow":  gorm.Expr("in_flow + ?", total[0]),
			"out_flow": gorm.Expr("out_flow + ?", total[1]),
		}).Error; err != nil {
			return err
		}
	}
	for _, userID := range sortedFlowUploadTargetIDs(userTotals) {
		total := userTotals[userID]
		if err := tx.Model(&model.User{}).Where("id = ?", userID).UpdateColumns(map[string]interface{}{
			"in_flow":  gorm.Expr("in_flow + ?", total[0]),
			"out_flow": gorm.Expr("out_flow + ?", total[1]),
		}).Error; err != nil {
			return err
		}
	}
	for _, userTunnelID := range sortedFlowUploadTargetIDs(userTunnelTotals) {
		total := userTunnelTotals[userTunnelID]
		if err := tx.Model(&model.UserTunnel{}).Where("id = ?", userTunnelID).UpdateColumns(map[string]interface{}{
			"in_flow":  gorm.Expr("in_flow + ?", total[0]),
			"out_flow": gorm.Expr("out_flow + ?", total[1]),
		}).Error; err != nil {
			return err
		}
	}
	return nil
}

// ─── Open / Close ────────────────────────────────────────────────────

func Open(path string) (*Repository, error) {
	if err := ensureParentDir(path); err != nil {
		return nil, err
	}

	dsn := "file:" + path +
		"?_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)"

	db, err := gorm.Open(gsqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, err
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(1)

	if err := prepareSQLiteLegacyColumns(db); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("prepare sqlite legacy schema: %w", err)
	}
	if err := autoMigrateAll(db); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("auto migrate: %w", err)
	}

	seedData(db)

	if err := migrateSchema(db); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	if err := migrateSubscriptions(db, unixMilliNow()); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("migrate subscriptions: %w", err)
	}

	return &Repository{db: db, dbPath: path}, nil
}

func OpenPostgres(dsn string) (*Repository, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, fmt.Errorf("empty postgres dsn")
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, err
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	configurePostgresPool(sqlDB)
	if err := sqlDB.Ping(); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}

	if err := preparePostgresLegacySchema(db); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("prepare postgres legacy schema: %w", err)
	}

	if err := autoMigrateAll(db); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("auto migrate: %w", err)
	}

	seedData(db)

	if err := migrateSchema(db); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	if err := migrateSubscriptions(db, unixMilliNow()); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("migrate subscriptions: %w", err)
	}

	return &Repository{db: db}, nil
}

func configurePostgresPool(sqlDB *sql.DB) {
	if sqlDB == nil {
		return
	}
	sqlDB.SetMaxOpenConns(defaultPostgresMaxOpenConns)
	sqlDB.SetMaxIdleConns(defaultPostgresMaxIdleConns)
	sqlDB.SetConnMaxIdleTime(defaultPostgresConnMaxIdle)
	sqlDB.SetConnMaxLifetime(defaultPostgresConnMaxLife)
}

func (r *Repository) Close() error {
	if r == nil || r.db == nil {
		return nil
	}
	sqlDB, err := r.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

func autoMigrateAll(db *gorm.DB) error {
	if db.Dialector.Name() == "sqlite" {
		if err := prepareSQLiteNftablesColumns(db); err != nil {
			return err
		}
	}

	// Migrate user_inbound unique index from (user_id, inbound_id) to
	// (user_id, inbound_id, relay_id) for per-relay subscription assignment.
	// GORM AutoMigrate never drops indexes, so remove the old one explicitly.
	if db.Migrator().HasTable(&model.UserInbound{}) {
		if db.Migrator().HasIndex(&model.UserInbound{}, "idx_user_inbound_unique") {
			if err := db.Migrator().DropIndex(&model.UserInbound{}, "idx_user_inbound_unique"); err != nil {
				return err
			}
		}
	}

	models := []interface{}{
		&model.User{},
		&model.Passkey{},
		&model.UserQuota{},
		&model.Forward{},
		&model.ForwardPort{},
		&model.Node{},
		&model.NodeSSHConfig{},
		&model.NftRuleBinding{},
		&model.NftCounterState{},
		&model.NftPendingDelete{},
		&NodeSeq{},
		&model.SpeedLimit{},
		&model.StatisticsFlow{},
		&model.Tunnel{},
		&model.ChainTunnel{},
		&model.UserTunnel{},
		&model.TunnelGroup{},
		&model.UserGroup{},
		&model.TunnelGroupTunnel{},
		&model.UserGroupUser{},
		&model.GroupPermission{},
		&model.GroupPermissionGrant{},
		&model.MonitorPermission{},
		&model.ViteConfig{},
		&model.PeerShare{},
		&model.PeerShareRuntime{},
		&model.PeerShareResource{},
		&model.FederationTunnelBinding{},
		&model.FederationPendingRelease{},
		&model.Announcement{},
		&model.SchemaVersion{},
		&model.NodeMetric{},
		&model.NodeTraffic{},
		&model.NodeLatencySample{},
		&model.TunnelMetric{},
		&model.ServiceMonitor{},
		&model.ServiceMonitorResult{},
		&model.TunnelQuality{},
		&model.NodeKernel{},
		&model.NodeInbound{},
		&model.NodeInboundCounter{},
		&model.NodeInboundRelay{},
		&model.UserInbound{},
		&model.UserSubscription{},
		&model.SubUser{},
		&model.Subscription{},
		&model.SubscriptionCounter{},
		&model.SubscriptionTraffic{},
		&model.RemoteSource{},
	}

	if db.Dialector.Name() != "sqlite" {
		if err := db.AutoMigrate(models...); err != nil {
			return err
		}
		// B4/F-009: PG 此前在此提前 return，R5-B4 版本化迁移从未在 PG 上
		// 执行——四个唯一索引在 PG 永远建不上，migration_status 却按
		// "下次启动会自动建"汇报。迁移函数本身是双言写的（索引存在性
		// 检查有 pg_indexes 分支），在此与 SQLite 同口径执行。
		if err := migrateV1PortUserUnique(db); err != nil {
			return err
		}
		if err := migrateV2NodeInboundPartialUnique(db); err != nil {
			return err
		}
		if err := migrateV3PeerShareRuntimeCompositeUnique(db); err != nil {
			return err
		}
		// FIX-132（D3-04）: 存量 share 的 relay 凭据一次性回填。
		backfillPeerShareRelayCredentials(db)
		return nil
	}

	m := db.Migrator()

	for _, item := range models {
		if err := db.AutoMigrate(item); err != nil {
			return err
		}
	}

	// 显式补列兜底：防止未来新增字段时 AutoMigrate 未生效（历史教训：latency_targets 事件）
	// Node 表
	if m.HasTable(&model.Node{}) {
		if !m.HasColumn(&model.Node{}, "latency_targets") {
			if err := m.AddColumn(&model.Node{}, "latency_targets"); err != nil {
				return err
			}
		}
	}

	// R5-B4: 版本化迁移 v1（端口/用户名唯一索引）。
	// 不在 model tag 上加 uniqueIndex，避免 AutoMigrate 在有重复数据时静默建索引失败导致启动崩溃。
	// 本次执行于测试库；生产库需重跑 dry-run 并重新批准。
	if err := migrateV1PortUserUnique(db); err != nil {
		return err
	}

	// R5-B4 v2（B4/F-003）: node_inbound 唯一索引转部分索引
	// （WHERE kernel_id > 0），存量全量索引显式 DROP + CREATE 转换。
	if err := migrateV2NodeInboundPartialUnique(db); err != nil {
		return err
	}

	// FIX-132 v3（D3-05）: peer_share_runtime 唯一索引由单列
	// resource_key 改为复合 (share_id, resource_key)，存量单列索引
	// 显式 DROP + CREATE 转换（预检同 share 内重复时跳过、下次重试）。
	if err := migrateV3PeerShareRuntimeCompositeUnique(db); err != nil {
		return err
	}

	// FIX-132（D3-04）: 存量 share 的 relay 凭据一次性回填
	// （relay_password 为空者）；新 share 在创建时生成、消费侧懒补发
	// 另有 handler.ensurePeerShareRelayCredentials 兜底。
	backfillPeerShareRelayCredentials(db)

	return nil
}

// preparePostgresLegacySchema renames unique constraints that were created by
// the old schema.sql (which used inline UNIQUE column syntax) to the names
// expected by GORM's NamingStrategy ("uni_<table>_<column>"). Without this,
// GORM's AutoMigrate emits "DROP CONSTRAINT uni_..." against constraints that
// don't exist under that name, crashing startup on upgraded PostgreSQL installs.
func preparePostgresLegacySchema(db *gorm.DB) error {
	if db == nil || db.Dialector.Name() != "postgres" {
		return nil
	}

	type rename struct{ table, oldName, newName string }
	renames := []rename{
		{"vite_config", "vite_config_name_key", "uni_vite_config_name"},
		{"peer_share", "peer_share_token_key", "uni_peer_share_token"},
		{"peer_share_runtime", "peer_share_runtime_reservation_id_key", "uni_peer_share_runtime_reservation_id"},
		{"peer_share_runtime", "peer_share_runtime_resource_key_key", "uni_peer_share_runtime_resource_key"},
		{"federation_tunnel_binding", "federation_tunnel_binding_resource_key_key", "uni_federation_tunnel_binding_resource_key"},
	}

	for _, r := range renames {
		var count int64
		if err := db.Raw(
			`SELECT COUNT(*) FROM information_schema.table_constraints
			 WHERE constraint_schema = current_schema()
			   AND table_name = ?
			   AND constraint_name = ?
			   AND constraint_type = 'UNIQUE'`,
			r.table, r.oldName,
		).Scan(&count).Error; err != nil {
			return fmt.Errorf("check constraint %s.%s: %w", r.table, r.oldName, err)
		}
		if count == 0 {
			continue
		}
		if err := db.Exec(
			fmt.Sprintf(`ALTER TABLE %q RENAME CONSTRAINT %q TO %q`, r.table, r.oldName, r.newName),
		).Error; err != nil {
			return fmt.Errorf("rename constraint %s.%s→%s: %w", r.table, r.oldName, r.newName, err)
		}
	}
	return nil
}

func prepareSQLiteLegacyColumns(db *gorm.DB) error {
	if db == nil || db.Dialector.Name() != "sqlite" {
		return nil
	}
	m := db.Migrator()

	if m.HasTable(&model.Node{}) {
		for _, field := range []string{"ServerIPV4", "ServerIPV6", "ExtraIPs", "TCPListenAddr", "UDPListenAddr", "Inx", "IsRemote", "RemoteURL", "RemoteToken", "RemoteConfig", "Remark", "ExpiryTime", "RenewalCycle", "ExpiryReminderDismissed"} {
			if m.HasColumn(&model.Node{}, field) {
				continue
			}
			if err := m.AddColumn(&model.Node{}, field); err != nil {
				return fmt.Errorf("add node.%s: %w", field, err)
			}
		}
	}

	if m.HasTable(&model.Tunnel{}) {
		for _, field := range []string{"Inx", "IPPreference", "ProbeTargetHost", "ProbeTargetPort"} {
			if m.HasColumn(&model.Tunnel{}, field) {
				continue
			}
			if err := m.AddColumn(&model.Tunnel{}, field); err != nil {
				return fmt.Errorf("add tunnel.%s: %w", field, err)
			}
		}
	}

	if m.HasTable(&model.Forward{}) {
		for _, field := range []string{"MaxConn", "IPMaxConn", "IPSpeedID", "ProxyProtocol", "ProxyProtocolReceive", "ProxyProtocolSend"} {
			if m.HasColumn(&model.Forward{}, field) {
				continue
			}
			if err := m.AddColumn(&model.Forward{}, field); err != nil {
				return fmt.Errorf("add forward.%s: %w", field, err)
			}
		}
	}

	return nil
}

func prepareSQLiteNftablesColumns(db *gorm.DB) error {
	if db == nil || db.Dialector.Name() != "sqlite" {
		return nil
	}
	if !db.Migrator().HasTable(&model.Node{}) {
		return nil
	}
	if !db.Migrator().HasColumn(&model.Node{}, "forward_mode") {
		if err := db.Exec("ALTER TABLE node ADD COLUMN forward_mode varchar(20) NOT NULL DEFAULT 'agent'").Error; err != nil {
			return err
		}
	}
	return nil
}

// adminBootstrapRequired: id=1 缺失且 admin@user 被占用时为 true。
// 面板无管理员账号，需人工处理（不得自动提权）。
var adminBootstrapRequired = false

// IsAdminBootstrapRequired 返回是否需要人工补建管理员。
func IsAdminBootstrapRequired() bool {
	return adminBootstrapRequired
}

func seedData(db *gorm.DB) {
	var adminCount int64
	if err := db.Model(&model.User{}).Where("id = ?", 1).Count(&adminCount).Error; err == nil && adminCount == 0 {
		// R5-B4C: 补建前检查用户名是否被占用（防改名后撞唯一约束）。
		var nameCount int64
		db.Model(&model.User{}).Where("\"user\" = ?", "admin@user").Count(&nameCount)
		if nameCount > 0 {
			// 修正 A：不得静默跳过。ERROR 日志 + API 状态可见。
			adminBootstrapRequired = true
			log.Printf("ERROR: seed skipped: id=1 missing and username admin@user already taken. " +
				"面板缺少管理员账号，需人工处理（不得自动提权）。")
		} else {
			adminPwd, err := security.HashPassword("admin@user")
			if err != nil {
				log.Printf("seed admin password hash failed: %v", err)
			} else {
				adminUser := model.User{
					ID: 1, User: "admin@user", Pwd: adminPwd,
					RoleID: 0, ExpTime: 2727251700000, Flow: 99999, InFlow: 0, OutFlow: 0,
					FlowResetTime: 1, Num: 99999, CreatedTime: 1748914865000,
					UpdatedTime:       sql.NullInt64{Int64: 1754011744252, Valid: true},
					Status:            1,
					PasswordChangedAt: 1748914865000,
				}
				if err := db.Create(&adminUser).Error; err != nil {
					log.Printf("seed admin failed: %v", mapUniqueViolation(err, "username"))
				}
			}
		}
	}

	appNameConfig := model.ViteConfig{ID: 1, Name: "app_name", Value: "FLMX", Time: 1755147963000}
	db.Where("id = ?", 1).FirstOrCreate(&appNameConfig)
	// "flux" was the seeded name, hidden behind the old commercial license
	// (it could not be changed without one); show the product name instead.
	// "FLVX"/"FLVX-MAX" were the seeded names before the FLMX rename; same treatment.
	db.Model(&model.ViteConfig{}).Where("name = ? AND value IN ?", "app_name", []string{"flux", "FLVX", "FLVX-MAX"}).Update("value", "FLMX")
	now := time.Now().UnixMilli()
	for name, value := range map[string]string{
		"hide_footer_brand": "false",
	} {
		cfg := model.ViteConfig{Name: name, Value: value, Time: now}
		db.Where("name = ?", name).FirstOrCreate(&cfg)
	}
}

// ─── User Queries ────────────────────────────────────────────────────

func (r *Repository) GetUserByUsername(username string) (*model.User, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var user model.User
	err := r.db.Where(`"user" = ?`, username).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *Repository) GetUserByID(id int64) (*model.User, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	return getUserByID(r.db, id)
}

func (r *Repository) GetUserByIDTx(tx *gorm.DB, id int64) (*model.User, error) {
	if tx == nil {
		return nil, errors.New("database unavailable")
	}
	return getUserByID(tx, id)
}

func getUserByID(db *gorm.DB, id int64) (*model.User, error) {
	var user model.User
	err := db.Where("id = ?", id).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *Repository) UsernameExists(username string) (bool, error) {
	var count int64
	err := r.db.Model(&model.User{}).Where(`"user" = ?`, username).Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *Repository) UsernameExistsExceptID(username string, exceptID int64) (bool, error) {
	if r == nil || r.db == nil {
		return false, errors.New("repository not initialized")
	}
	var count int64
	err := r.db.Model(&model.User{}).Where(`"user" = ? AND id != ?`, username, exceptID).Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *Repository) UpdateUserNameAndPassword(userID int64, username, passwordMD5 string, now int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Model(&model.User{}).Where("id = ?", userID).Updates(map[string]interface{}{
		"user":                username,
		"pwd":                 passwordMD5,
		"password_changed_at": now,
		"updated_time":        now,
	}).Error
}

// ─── Config Queries ──────────────────────────────────────────────────

func (r *Repository) GetConfigByName(name string) (*model.ViteConfig, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var cfg model.ViteConfig
	err := r.db.Where("name = ?", name).First(&cfg).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (r *Repository) ListConfigs() (map[string]string, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var configs []model.ViteConfig
	if err := r.db.Find(&configs).Error; err != nil {
		return nil, err
	}
	result := make(map[string]string)
	for _, c := range configs {
		result[c.Name] = c.Value
	}
	return result, nil
}

func (r *Repository) GetConfigsByNames(names []string) (map[string]string, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	if len(names) == 0 {
		return map[string]string{}, nil
	}
	var configs []model.ViteConfig
	if err := r.db.Select("name", "value").Where("name IN ?", names).Find(&configs).Error; err != nil {
		return nil, err
	}
	result := make(map[string]string, len(configs))
	for _, c := range configs {
		result[c.Name] = c.Value
	}
	return result, nil
}

func (r *Repository) UpsertConfig(name, value string, now int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return upsertConfigTx(r.db, name, value, now)
}

func upsertConfigTx(db *gorm.DB, name, value string, now int64) error {
	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "name"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "time"}),
	}).Create(&model.ViteConfig{Name: name, Value: value, Time: now}).Error
}

func (r *Repository) UpsertConfigs(values map[string]string, now int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		for name, value := range values {
			if err := upsertConfigTx(tx, name, value, now); err != nil {
				return err
			}
		}
		return nil
	})
}

// DeleteConfig removes a config row so subsequent reads fall back to the
// built-in default for the key. Deleting a key that is not stored is a
// no-op, which makes "clear this setting" idempotent.
func (r *Repository) DeleteConfig(name string) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Where("name = ?", name).Delete(&model.ViteConfig{}).Error
}

// ApplyConfigBatch applies config upserts and clear-deletions in a single
// transaction: a settings save either lands completely or not at all.
func (r *Repository) ApplyConfigBatch(values map[string]string, deleteKeys []string, now int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		for name, value := range values {
			if err := upsertConfigTx(tx, name, value, now); err != nil {
				return err
			}
		}
		for _, name := range deleteKeys {
			if err := tx.Where("name = ?", name).Delete(&model.ViteConfig{}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// ─── Announcement Queries ────────────────────────────────────────────

func (r *Repository) GetAnnouncement() (*model.Announcement, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var ann model.Announcement
	err := r.db.Order("id DESC").First(&ann).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ann, nil
}

func (r *Repository) UpsertAnnouncement(content string, enabled int, now int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	var count int64
	if err := r.db.Model(&model.Announcement{}).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		// A2-02（零值家族）：Announcement.Enabled 带 `default:1`，首建时
		// 显式 enabled=0 会被 Create 当未设置丢弃、落库为 1——想首发
		// 「停用」公告实际直接上线。仿 CreateNodeInbound（BUG-A4-1）
		// 留意图、创建后写回。
		ann := &model.Announcement{
			Content: content, Enabled: enabled,
			CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true},
		}
		if err := r.db.Create(ann).Error; err != nil {
			return err
		}
		if enabled == 0 {
			return r.db.Model(&model.Announcement{}).Where("id = ?", ann.ID).Update("enabled", 0).Error
		}
		return nil
	}
	return r.db.Model(&model.Announcement{}).Where("1=1").Updates(map[string]interface{}{
		"content": content, "enabled": enabled, "updated_time": now,
	}).Error
}

// ─── User Package Queries ────────────────────────────────────────────

func (r *Repository) GetUserPackageTunnels(userID int64) ([]model.UserTunnelDetail, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var items []model.UserTunnelDetail
	err := r.db.Model(&model.UserTunnel{}).
		Select("user_tunnel.id, user_tunnel.user_id, user_tunnel.tunnel_id, tunnel.name AS tunnel_name, user_tunnel.status, tunnel.flow AS tunnel_flow, user_tunnel.flow, user_tunnel.flow_mib, user_tunnel.in_flow, user_tunnel.out_flow, user_tunnel.num, user_tunnel.flow_reset_time, user_tunnel.exp_time, user_tunnel.speed_id, speed_limit.name AS speed_limit, speed_limit.speed").
		Joins("LEFT JOIN tunnel ON tunnel.id = user_tunnel.tunnel_id").
		Joins("LEFT JOIN speed_limit ON speed_limit.id = user_tunnel.speed_id").
		Where("user_tunnel.user_id = ?", userID).
		Order("user_tunnel.id ASC").
		Find(&items).Error
	if err != nil {
		return nil, err
	}
	if items == nil {
		items = make([]model.UserTunnelDetail, 0)
	}
	return items, nil
}

func (r *Repository) GetUserPackageForwards(userID int64) ([]model.UserForwardDetail, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}

	type fwdRow struct {
		ID         int64
		Name       string
		TunnelID   int64
		TunnelName string
		RemoteAddr string
		InFlow     int64
		OutFlow    int64
		Status     int
		CreatedAt  int64
	}

	var rows []fwdRow
	err := r.db.Model(&model.Forward{}).
		Select("forward.id, forward.name, forward.tunnel_id, COALESCE(tunnel.name, '') AS tunnel_name, forward.remote_addr, forward.in_flow, forward.out_flow, forward.status, forward.created_time AS created_at").
		Joins("LEFT JOIN tunnel ON tunnel.id = forward.tunnel_id").
		Where("forward.user_id = ?", userID).
		Order("forward.id ASC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}

	items := make([]model.UserForwardDetail, 0, len(rows))
	for _, row := range rows {
		inIP, inPort, err := resolveForwardIngress(r.db, row.ID, row.TunnelID)
		if err != nil {
			return nil, err
		}
		items = append(items, model.UserForwardDetail{
			ID: row.ID, Name: row.Name, TunnelID: row.TunnelID,
			TunnelName: row.TunnelName, InIP: inIP, InPort: inPort,
			RemoteAddr: row.RemoteAddr, InFlow: row.InFlow, OutFlow: row.OutFlow,
			Status: row.Status, CreatedAt: row.CreatedAt,
		})
	}
	return items, nil
}

// ─── Statistics Queries ──────────────────────────────────────────────

func (r *Repository) GetStatisticsFlows(userID int64, limit int) ([]model.StatisticsFlow, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var items []model.StatisticsFlow
	err := r.db.Where("user_id = ?", userID).Order("id DESC").Limit(limit).Find(&items).Error
	if err != nil {
		return nil, err
	}
	if items == nil {
		items = make([]model.StatisticsFlow, 0)
	}
	return items, nil
}

// ─── Node Queries ────────────────────────────────────────────────────

func (r *Repository) NodeExistsBySecret(secret string) (bool, error) {
	if r == nil || r.db == nil {
		return false, errors.New("repository not initialized")
	}
	var count int64
	err := r.db.Model(&model.Node{}).Where("secret = ?", secret).Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *Repository) GetNodeBySecret(secret string) (*model.Node, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var n model.Node
	err := r.db.Where("secret = ?", secret).First(&n).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &n, nil
}

func (r *Repository) GetNodeByID(id int64) (*model.Node, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var n model.Node
	err := r.db.Where("id = ?", id).First(&n).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &n, nil
}

// GetNodeAgentVersion 返回节点最近一次 WS 握手上报的 agent 版本
// （从未上报时为空）。供按 agent 能力分版下发配置用（如 FIX2-C #8
// 的内置 bypass 版本门）。
func (r *Repository) GetNodeAgentVersion(id int64) (string, error) {
	if r == nil || r.db == nil {
		return "", errors.New("repository not initialized")
	}
	var n model.Node
	err := r.db.Select("version").Where("id = ?", id).First(&n).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return n.Version.String, nil
}

func (r *Repository) UpdateNodeOnline(nodeID int64, status int, version string, httpVal, tlsVal, socksVal int) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Model(&model.Node{}).Where("id = ?", nodeID).Updates(map[string]interface{}{
		"status": status, "version": version, "http": httpVal, "tls": tlsVal,
		"socks": socksVal, "updated_time": unixMilliNow(),
	}).Error
}

func (r *Repository) UpdateNodeStatus(nodeID int64, status int) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Model(&model.Node{}).Where("id = ?", nodeID).Updates(map[string]interface{}{
		"status": status, "updated_time": unixMilliNow(),
	}).Error
}

// ─── Flow ────────────────────────────────────────────────────────────

func (r *Repository) AddFlow(forwardID, userID int64, userTunnelID int64, inFlow, outFlow int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.Forward{}).Where("id = ?", forwardID).
			UpdateColumns(map[string]interface{}{
				"in_flow":  gorm.Expr("in_flow + ?", inFlow),
				"out_flow": gorm.Expr("out_flow + ?", outFlow),
			}).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.User{}).Where("id = ?", userID).
			UpdateColumns(map[string]interface{}{
				"in_flow":  gorm.Expr("in_flow + ?", inFlow),
				"out_flow": gorm.Expr("out_flow + ?", outFlow),
			}).Error; err != nil {
			return err
		}
		if userTunnelID > 0 {
			if err := tx.Model(&model.UserTunnel{}).Where("id = ?", userTunnelID).
				UpdateColumns(map[string]interface{}{
					"in_flow":  gorm.Expr("in_flow + ?", inFlow),
					"out_flow": gorm.Expr("out_flow + ?", outFlow),
				}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// ─── List Methods (return map[string]interface{}) ────────────────────

func (r *Repository) ListNodes() ([]map[string]interface{}, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var nodes []model.Node
	if err := r.db.Order("inx ASC, id ASC").Find(&nodes).Error; err != nil {
		return nil, err
	}
	nodeIDs := make([]int64, 0, len(nodes))
	for _, n := range nodes {
		if defaultNodeForwardMode(n.ForwardMode) == "nftables" {
			nodeIDs = append(nodeIDs, n.ID)
		}
	}
	sshConfigByNodeID := make(map[int64]map[string]interface{}, len(nodeIDs))
	if len(nodeIDs) > 0 {
		var configs []model.NodeSSHConfig
		if err := r.db.Where("node_id IN ?", nodeIDs).Find(&configs).Error; err != nil {
			return nil, err
		}
		for _, cfg := range configs {
			sshConfigByNodeID[cfg.NodeID] = map[string]interface{}{
				"host":     cfg.Host,
				"port":     cfg.Port,
				"username": cfg.Username,
				"authType": cfg.AuthType,
				"sudoMode": cfg.SudoMode,
			}
		}
	}
	items := make([]map[string]interface{}, 0, len(nodes))
	for _, n := range nodes {
		items = append(items, map[string]interface{}{
			"id": n.ID, "inx": n.Inx, "name": n.Name,
			"remark":       nullableString(n.Remark),
			"expiryTime":   nullableInt64(n.ExpiryTime),
			"renewalCycle": nullableString(n.RenewalCycle),
			"ip":           n.ServerIP, "serverIp": n.ServerIP,
			"serverIpV4":    nullableString(n.ServerIPV4),
			"serverIpV6":    nullableString(n.ServerIPV6),
			"extraIPs":      nullableString(n.ExtraIPs),
			"port":          n.Port,
			"tcpListenAddr": n.TCPListenAddr,
			"udpListenAddr": n.UDPListenAddr,
			"version":       nullableString(n.Version),
			"http":          n.HTTP, "tls": n.TLS, "socks": n.Socks,
			"status": n.Status, "isRemote": n.IsRemote,
			"forwardMode":             defaultNodeForwardMode(n.ForwardMode),
			"remoteUrl":               nullableString(n.RemoteURL),
			"remoteToken":             nullableString(n.RemoteToken),
			"remoteConfig":            nullableString(n.RemoteConfig),
			"expiryReminderDismissed": n.ExpiryReminderDismissed,
			"interfaceName":           nullableString(n.InterfaceName),
			"sshConfig":               sshConfigByNodeID[n.ID],
		})
	}
	return items, nil
}

func (r *Repository) ListUsers() ([]map[string]interface{}, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var users []model.User
	if err := r.db.Where("role_id != ?", 0).Order("id DESC").Find(&users).Error; err != nil {
		return nil, err
	}
	userIDs := make([]int64, 0, len(users))
	for _, u := range users {
		userIDs = append(userIDs, u.ID)
	}
	quotaMap, err := r.ListUserQuotaViewsByUserIDs(userIDs, time.Now())
	if err != nil {
		return nil, err
	}
	items := make([]map[string]interface{}, 0, len(users))
	for _, u := range users {
		item := map[string]interface{}{
			"id": u.ID, "user": u.User, "name": u.User,
			"roleId": u.RoleID, "status": u.Status,
			"flow": u.Flow, "flowMiB": u.FlowMiB, "num": u.Num, "expTime": u.ExpTime,
			"flowResetTime": u.FlowResetTime, "createdTime": u.CreatedTime,
			"updatedTime": nullableInt64(u.UpdatedTime),
			"inFlow":      u.InFlow, "outFlow": u.OutFlow,
			"maxConn": u.MaxConn,
		}
		if quota := quotaMap[u.ID]; quota != nil {
			item["dailyQuotaGB"] = quota.DailyLimitGB
			item["monthlyQuotaGB"] = quota.MonthlyLimitGB
			item["dailyUsedBytes"] = quota.DailyUsedBytes
			item["monthlyUsedBytes"] = quota.MonthlyUsedBytes
			item["disabledByQuota"] = quota.DisabledByQuota
			item["quotaDisabledAt"] = quota.DisabledAt
		}
		items = append(items, item)
	}
	return items, nil
}

func (r *Repository) ListSpeedLimits() ([]map[string]interface{}, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var limits []model.SpeedLimit
	if err := r.db.Order("id DESC").Find(&limits).Error; err != nil {
		return nil, err
	}
	items := make([]map[string]interface{}, 0, len(limits))
	for _, sl := range limits {
		item := map[string]interface{}{
			"id": sl.ID, "name": sl.Name, "speed": sl.Speed,
			"status": sl.Status, "createdTime": sl.CreatedTime,
			"updatedTime": nullableInt64(sl.UpdatedTime),
		}
		items = append(items, item)
	}
	return items, nil
}

func (r *Repository) ListForwards() ([]map[string]interface{}, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}

	type fwdRow struct {
		ID                   int64
		UserID               int64
		UserName             string
		Name                 string
		TunnelID             int64
		TunnelName           string
		TrafficRatio         float64
		RemoteAddr           string
		Strategy             string
		InFlow               int64
		OutFlow              int64
		CreatedTime          int64
		Status               int
		Inx                  int
		SpeedID              sql.NullInt64
		MaxConn              int
		IPMaxConn            int
		IPSpeedID            sql.NullInt64
		IPSpeedLimitName     string
		ProxyProtocol        int
		ProxyProtocolReceive int
		ProxyProtocolSend    int
	}

	var rows []fwdRow
	err := r.db.Model(&model.Forward{}).
		Select("forward.id, forward.user_id, forward.user_name, forward.name, forward.tunnel_id, COALESCE(tunnel.name, '') AS tunnel_name, COALESCE(tunnel.traffic_ratio, 1.0) AS traffic_ratio, forward.remote_addr, COALESCE(forward.strategy, 'fifo') AS strategy, forward.in_flow, forward.out_flow, forward.created_time, forward.status, forward.inx, forward.speed_id, forward.max_conn, forward.ip_max_conn, forward.ip_speed_id, COALESCE(ip_speed_limit.name, '') AS ip_speed_limit_name, forward.proxy_protocol, forward.proxy_protocol_receive, forward.proxy_protocol_send").
		Joins("LEFT JOIN tunnel ON tunnel.id = forward.tunnel_id").
		Joins("LEFT JOIN speed_limit AS ip_speed_limit ON ip_speed_limit.id = forward.ip_speed_id").
		// R5-B3 D1-001 矩阵 #1: pending-delete 行对用户/管理员列表隐藏。
		Where("forward.status <> ?", model.ForwardStatusPendingDelete).
		Order("forward.inx ASC, forward.id ASC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}

	items := make([]map[string]interface{}, 0, len(rows))
	for _, row := range rows {
		proxyProtocolReceive, proxyProtocolSend := normalizeForwardProxyProtocol(row.ProxyProtocol, row.ProxyProtocolReceive, row.ProxyProtocolSend)
		inIP, inPort, err := resolveForwardIngress(r.db, row.ID, row.TunnelID)
		if err != nil {
			return nil, err
		}
		item := map[string]interface{}{
			"id": row.ID, "userId": row.UserID, "userName": row.UserName,
			"name": row.Name, "tunnelId": row.TunnelID, "tunnelName": row.TunnelName,
			"tunnelTrafficRatio": row.TrafficRatio,
			"inIp":               nullableForwardIngress(inIP), "inPort": nullableInt64(inPort),
			"remoteAddr": row.RemoteAddr, "strategy": row.Strategy,
			"inFlow": row.InFlow, "outFlow": row.OutFlow,
			"createdTime": row.CreatedTime, "status": row.Status, "inx": int64(row.Inx),
			"maxConn":              row.MaxConn,
			"ipMaxConn":            row.IPMaxConn,
			"proxyProtocol":        row.ProxyProtocol,
			"proxyProtocolReceive": proxyProtocolReceive,
			"proxyProtocolSend":    proxyProtocolSend,
		}
		if row.SpeedID.Valid {
			item["speedId"] = row.SpeedID.Int64
		}
		if row.IPSpeedID.Valid {
			item["ipSpeedId"] = row.IPSpeedID.Int64
		}
		if strings.TrimSpace(row.IPSpeedLimitName) != "" {
			item["ipSpeedLimitName"] = row.IPSpeedLimitName
		}
		items = append(items, item)
	}
	return items, nil
}

func (r *Repository) ListUserAccessibleTunnels(userID int64) ([]map[string]interface{}, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}

	type row struct {
		ID   int64
		Name string
	}
	var rows []row
	err := r.db.Model(&model.UserTunnel{}).
		Select("tunnel.id, tunnel.name").
		Joins("JOIN tunnel ON tunnel.id = user_tunnel.tunnel_id").
		Where("user_tunnel.user_id = ? AND tunnel.status = 1", userID).
		Order("tunnel.inx ASC, tunnel.id ASC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}

	tunnelIDs := make([]int64, 0, len(rows))
	for _, rw := range rows {
		tunnelIDs = append(tunnelIDs, rw.ID)
	}
	portRangeMap := r.getTunnelEntryPortRanges(tunnelIDs)

	items := make([]map[string]interface{}, 0, len(rows))
	for _, rw := range rows {
		item := map[string]interface{}{"id": rw.ID, "name": rw.Name}
		if pr, ok := portRangeMap[rw.ID]; ok {
			item["portRangeMin"] = pr.min
			item["portRangeMax"] = pr.max
		}
		items = append(items, item)
	}
	return items, nil
}

func (r *Repository) ListEnabledTunnelSummaries() ([]map[string]interface{}, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}

	type row struct {
		ID   int64
		Name string
	}
	var rows []row
	err := r.db.Model(&model.Tunnel{}).Select("id, name").Where("status = 1").Order("inx ASC, id ASC").Find(&rows).Error
	if err != nil {
		return nil, err
	}

	tunnelIDs := make([]int64, 0, len(rows))
	for _, rw := range rows {
		tunnelIDs = append(tunnelIDs, rw.ID)
	}
	portRangeMap := r.getTunnelEntryPortRanges(tunnelIDs)

	items := make([]map[string]interface{}, 0, len(rows))
	for _, rw := range rows {
		item := map[string]interface{}{"id": rw.ID, "name": rw.Name}
		if pr, ok := portRangeMap[rw.ID]; ok {
			item["portRangeMin"] = pr.min
			item["portRangeMax"] = pr.max
		}
		items = append(items, item)
	}
	return items, nil
}

type tunnelPortRange struct {
	min int
	max int
}

func (r *Repository) getTunnelEntryPortRanges(tunnelIDs []int64) map[int64]tunnelPortRange {
	result := make(map[int64]tunnelPortRange)
	if len(tunnelIDs) == 0 {
		return result
	}

	type entryNode struct {
		TunnelID int64
		NodeID   int64
	}
	var entries []entryNode
	r.db.Model(&model.ChainTunnel{}).
		Select("tunnel_id, node_id").
		Where("tunnel_id IN (?) AND chain_type = ?", tunnelIDs, "1").
		Find(&entries)

	nodeIDs := make([]int64, 0, len(entries))
	nodeSet := make(map[int64]struct{})
	for _, e := range entries {
		if _, exists := nodeSet[e.NodeID]; !exists {
			nodeSet[e.NodeID] = struct{}{}
			nodeIDs = append(nodeIDs, e.NodeID)
		}
	}

	type nodePort struct {
		ID   int64
		Port string
	}
	var nodePorts []nodePort
	if len(nodeIDs) > 0 {
		r.db.Model(&model.Node{}).Select("id, port").Where("id IN (?)", nodeIDs).Find(&nodePorts)
	}

	nodePortMap := make(map[int64]string)
	for _, np := range nodePorts {
		nodePortMap[np.ID] = np.Port
	}

	for _, e := range entries {
		portSpec := nodePortMap[e.NodeID]
		if portSpec == "" {
			continue
		}
		minP, maxP := parsePortRangeMinMax(portSpec)
		if minP <= 0 || maxP <= 0 {
			continue
		}
		pr, exists := result[e.TunnelID]
		if !exists {
			result[e.TunnelID] = tunnelPortRange{min: minP, max: maxP}
		} else {
			if minP < pr.min {
				pr.min = minP
			}
			if maxP > pr.max {
				pr.max = maxP
			}
			result[e.TunnelID] = pr
		}
	}
	return result
}

func parsePortRangeMinMax(input string) (int, int) {
	input = strings.TrimSpace(input)
	if input == "" {
		return 0, 0
	}
	minPort, maxPort := 0, 0
	parts := strings.Split(input, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "-") {
			r := strings.SplitN(part, "-", 2)
			if len(r) != 2 {
				continue
			}
			start, end := parseIntPort(r[0]), parseIntPort(r[1])
			if start <= 0 || end <= 0 {
				continue
			}
			if end < start {
				start, end = end, start
			}
			if minPort == 0 || start < minPort {
				minPort = start
			}
			if maxPort == 0 || end > maxPort {
				maxPort = end
			}
			continue
		}
		p := parseIntPort(part)
		if p <= 0 {
			continue
		}
		if minPort == 0 || p < minPort {
			minPort = p
		}
		if maxPort == 0 || p > maxPort {
			maxPort = p
		}
	}
	return minPort, maxPort
}

func parseIntPort(s string) int {
	var p int
	fmt.Sscanf(strings.TrimSpace(s), "%d", &p)
	return p
}

func (r *Repository) ListTunnels() ([]map[string]interface{}, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}

	var tunnels []model.Tunnel
	if err := r.db.Order("inx ASC, id ASC").Find(&tunnels).Error; err != nil {
		return nil, err
	}

	tunnelMap := make(map[int64]map[string]interface{})
	orderedIDs := make([]int64, 0, len(tunnels))

	for _, t := range tunnels {
		tunnelMap[t.ID] = map[string]interface{}{
			"id": t.ID, "inx": t.Inx, "name": t.Name,
			"type": t.Type, "flow": t.Flow, "trafficRatio": t.TrafficRatio,
			"status": t.Status, "createdTime": t.CreatedTime,
			"engine":          t.Engine,
			"inIp":            nullableString(t.InIP),
			"ipPreference":    t.IPPreference,
			"probeTargetHost": t.ProbeTargetHost,
			"probeTargetPort": t.ProbeTargetPort,
			"inNodeId":        make([]map[string]interface{}, 0),
			"outNodeId":       make([]map[string]interface{}, 0),
			"chainNodes":      make([][]map[string]interface{}, 0),
		}
		orderedIDs = append(orderedIDs, t.ID)
	}

	// Build node IP map
	nodeIPMap := map[int64]string{}
	var nodeList []model.Node
	if err := r.db.Select("id, server_ip").Find(&nodeList).Error; err == nil {
		for _, n := range nodeList {
			nodeIPMap[n.ID] = n.ServerIP
		}
	}

	// Load chain tunnels
	var chains []model.ChainTunnel
	if err := r.db.Order("tunnel_id ASC, chain_type ASC, inx ASC, id ASC").Find(&chains).Error; err != nil {
		return nil, err
	}

	chainBucket := map[int64]map[int][]map[string]interface{}{}
	inNodeIPs := map[int64][]string{}

	for _, c := range chains {
		t, ok := tunnelMap[c.TunnelID]
		if !ok {
			continue
		}

		chainTypeInt := 0
		fmt.Sscanf(c.ChainType, "%d", &chainTypeInt)

		inx := int64(0)
		if c.Inx.Valid {
			inx = c.Inx.Int64
		}

		nodeObj := map[string]interface{}{
			"nodeId":    c.NodeID,
			"chainType": chainTypeInt,
			"inx":       inx,
		}
		if c.Protocol.Valid {
			nodeObj["protocol"] = c.Protocol.String
		}
		if c.Strategy.Valid {
			nodeObj["strategy"] = c.Strategy.String
		}
		if c.ConnectIP.Valid {
			nodeObj["connectIp"] = c.ConnectIP.String
		}

		switch chainTypeInt {
		case 1:
			t["inNodeId"] = append(t["inNodeId"].([]map[string]interface{}), nodeObj)
			if ip, ok := nodeIPMap[c.NodeID]; ok && ip != "" {
				inNodeIPs[c.TunnelID] = append(inNodeIPs[c.TunnelID], ip)
			}
		case 2:
			if _, ok := chainBucket[c.TunnelID]; !ok {
				chainBucket[c.TunnelID] = map[int][]map[string]interface{}{}
			}
			chainBucket[c.TunnelID][int(inx)] = append(chainBucket[c.TunnelID][int(inx)], nodeObj)
		case 3:
			t["outNodeId"] = append(t["outNodeId"].([]map[string]interface{}), nodeObj)
		}
	}

	for tunnelID, groups := range chainBucket {
		t := tunnelMap[tunnelID]
		if t == nil {
			continue
		}
		keys := make([]int, 0, len(groups))
		for k := range groups {
			keys = append(keys, k)
		}
		sort.Ints(keys)
		ordered := make([][]map[string]interface{}, 0, len(keys))
		for _, k := range keys {
			ordered = append(ordered, groups[k])
		}
		t["chainNodes"] = ordered

		if s, ok := t["inIp"].(string); !ok || strings.TrimSpace(s) == "" {
			if ips := inNodeIPs[tunnelID]; len(ips) > 0 {
				t["inIp"] = strings.Join(ips, ",")
			}
		}
	}

	result := make([]map[string]interface{}, 0, len(orderedIDs))
	for _, id := range orderedIDs {
		if t, ok := tunnelMap[id]; ok {
			result = append(result, t)
		}
	}
	return result, nil
}

// ─── Group Queries ───────────────────────────────────────────────────

func (r *Repository) ListTunnelGroups() ([]map[string]interface{}, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var groups []model.TunnelGroup
	if err := r.db.Order("id ASC").Find(&groups).Error; err != nil {
		return nil, err
	}
	result := make([]map[string]interface{}, 0, len(groups))
	for _, g := range groups {
		ids, names, err := r.listTunnelGroupMembers(g.ID)
		if err != nil {
			return nil, err
		}
		result = append(result, map[string]interface{}{
			"id": g.ID, "name": g.Name, "status": g.Status,
			"tunnelIds": ids, "tunnelNames": names,
			"createdTime": g.CreatedTime,
		})
	}
	return result, nil
}

func (r *Repository) ListUserGroups() ([]map[string]interface{}, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var groups []model.UserGroup
	if err := r.db.Order("id ASC").Find(&groups).Error; err != nil {
		return nil, err
	}
	result := make([]map[string]interface{}, 0, len(groups))
	for _, g := range groups {
		ids, names, err := r.listUserGroupMembers(g.ID)
		if err != nil {
			return nil, err
		}
		result = append(result, map[string]interface{}{
			"id": g.ID, "name": g.Name, "status": g.Status,
			"userIds": ids, "userNames": names,
			"createdTime": g.CreatedTime,
		})
	}
	return result, nil
}

func (r *Repository) ListGroupPermissions() ([]map[string]interface{}, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}

	type permRow struct {
		ID              int64
		UserGroupID     int64
		UserGroupName   sql.NullString
		TunnelGroupID   int64
		TunnelGroupName sql.NullString
		CreatedTime     int64
	}
	var rows []permRow
	err := r.db.Model(&model.GroupPermission{}).
		Select("group_permission.id, group_permission.user_group_id, user_group.name AS user_group_name, group_permission.tunnel_group_id, tunnel_group.name AS tunnel_group_name, group_permission.created_time").
		Joins("LEFT JOIN user_group ON user_group.id = group_permission.user_group_id").
		Joins("LEFT JOIN tunnel_group ON tunnel_group.id = group_permission.tunnel_group_id").
		Order("group_permission.id ASC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	result := make([]map[string]interface{}, 0, len(rows))
	for _, r := range rows {
		result = append(result, map[string]interface{}{
			"id": r.ID, "userGroupId": r.UserGroupID,
			"userGroupName":   nullableString(r.UserGroupName),
			"tunnelGroupId":   r.TunnelGroupID,
			"tunnelGroupName": nullableString(r.TunnelGroupName),
			"createdTime":     r.CreatedTime,
		})
	}
	return result, nil
}

func (r *Repository) listTunnelGroupMembers(groupID int64) ([]int64, []string, error) {
	type row struct {
		ID   int64
		Name string
	}
	var rows []row
	err := r.db.Model(&model.TunnelGroupTunnel{}).
		Select("tunnel.id, tunnel.name").
		Joins("JOIN tunnel ON tunnel.id = tunnel_group_tunnel.tunnel_id").
		Where("tunnel_group_tunnel.tunnel_group_id = ?", groupID).
		Order("tunnel.id ASC").
		Find(&rows).Error
	if err != nil {
		return nil, nil, err
	}
	ids := make([]int64, 0, len(rows))
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
		names = append(names, r.Name)
	}
	return ids, names, nil
}

func (r *Repository) listUserGroupMembers(groupID int64) ([]int64, []string, error) {
	type row struct {
		ID   int64
		Name string
	}
	var rows []row
	err := r.db.Model(&model.UserGroupUser{}).
		Select(`"user".id, "user"."user" AS name`).
		Joins(`JOIN "user" ON "user".id = user_group_user.user_id`).
		Where("user_group_user.user_group_id = ?", groupID).
		Order(`"user".id ASC`).
		Find(&rows).Error
	if err != nil {
		return nil, nil, err
	}
	ids := make([]int64, 0, len(rows))
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
		names = append(names, r.Name)
	}
	return ids, names, nil
}

// ─── PeerShare CRUD ──────────────────────────────────────────────────

func (r *Repository) CreatePeerShare(share *model.PeerShare) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Create(share).Error
}

func (r *Repository) UpdatePeerShare(share *model.PeerShare) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Model(&model.PeerShare{}).Where("id = ?", share.ID).Updates(map[string]interface{}{
		"name": share.Name, "max_bandwidth": share.MaxBandwidth,
		"expiry_time": share.ExpiryTime, "port_range_start": share.PortRangeStart,
		"port_range_end": share.PortRangeEnd, "is_active": share.IsActive,
		"updated_time": share.UpdatedTime, "allowed_domains": share.AllowedDomains,
		"allowed_ips": share.AllowedIPs,
	}).Error
}

// UpdatePeerShareRelayCredentials 只更新 share 的 D3-04 relay 凭据
// 两列（UpdatePeerShare 的显式列集合不含凭据列，避免常规更新路径
// 用旧结构体覆盖凭据）。
func (r *Repository) UpdatePeerShareRelayCredentials(id int64, username, password string, updatedTime int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Model(&model.PeerShare{}).Where("id = ?", id).Updates(map[string]interface{}{
		"relay_username": username, "relay_password": password, "updated_time": updatedTime,
	}).Error
}

func (r *Repository) DeletePeerShare(id int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("share_id = ?", id).Delete(&model.PeerShareRuntime{}).Error; err != nil {
			return err
		}
		if err := tx.Where("share_id = ?", id).Delete(&model.PeerShareResource{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", id).Delete(&model.PeerShare{}).Error
	})
}

func (r *Repository) GetPeerShare(id int64) (*model.PeerShare, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var s model.PeerShare
	err := r.db.Where("id = ?", id).First(&s).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *Repository) GetPeerShareByToken(token string) (*model.PeerShare, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var s model.PeerShare
	err := r.db.Where("token = ?", token).First(&s).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *Repository) ListPeerShares() ([]model.PeerShare, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var shares []model.PeerShare
	err := r.db.Order("id DESC").Find(&shares).Error
	return shares, err
}

func (r *Repository) AddPeerShareCurrentFlow(shareID int64, delta int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	if shareID <= 0 || delta <= 0 {
		return nil
	}
	return r.db.Model(&model.PeerShare{}).Where("id = ?", shareID).
		UpdateColumns(map[string]interface{}{
			"current_flow": gorm.Expr("current_flow + ?", delta),
			"updated_time": unixMilliNow(),
		}).Error
}

func (r *Repository) ResetPeerShareCurrentFlow(shareID int64, updatedTime int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	if shareID <= 0 {
		return nil
	}
	if updatedTime <= 0 {
		updatedTime = unixMilliNow()
	}
	return r.db.Model(&model.PeerShare{}).Where("id = ?", shareID).Updates(map[string]interface{}{
		"current_flow": 0, "updated_time": updatedTime,
	}).Error
}

// ─── PeerShareRuntime CRUD ───────────────────────────────────────────

func (r *Repository) CreatePeerShareRuntime(item *model.PeerShareRuntime) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	if item == nil {
		return errors.New("runtime item is nil")
	}
	return r.db.Create(item).Error
}

func (r *Repository) UpdatePeerShareRuntime(item *model.PeerShareRuntime) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	if item == nil {
		return errors.New("runtime item is nil")
	}
	return r.db.Model(&model.PeerShareRuntime{}).Where("id = ?", item.ID).Updates(map[string]interface{}{
		"reservation_id": item.ReservationID,
		"binding_id":     item.BindingID, "role": item.Role,
		"chain_name": item.ChainName, "service_name": item.ServiceName,
		"protocol": item.Protocol, "strategy": item.Strategy,
		"port": item.Port, "target": item.Target,
		"applied": item.Applied, "status": item.Status,
		"updated_time": item.UpdatedTime,
	}).Error
}

func (r *Repository) MarkPeerShareRuntimeReleased(id int64, updatedTime int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Model(&model.PeerShareRuntime{}).Where("id = ?", id).Updates(map[string]interface{}{
		"status": 0, "updated_time": updatedTime,
	}).Error
}

func (r *Repository) GetPeerShareRuntimeByResourceKey(shareID int64, resourceKey string) (*model.PeerShareRuntime, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var item model.PeerShareRuntime
	err := r.db.Where("share_id = ? AND resource_key = ?", shareID, resourceKey).First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func (r *Repository) GetPeerShareRuntimeByReservationID(shareID int64, reservationID string) (*model.PeerShareRuntime, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var item model.PeerShareRuntime
	err := r.db.Where("share_id = ? AND reservation_id = ?", shareID, reservationID).First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func (r *Repository) GetPeerShareRuntimeByBindingID(shareID int64, bindingID string) (*model.PeerShareRuntime, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var item model.PeerShareRuntime
	err := r.db.Where("share_id = ? AND binding_id = ?", shareID, bindingID).First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func (r *Repository) GetPeerShareRuntimeByID(id int64) (*model.PeerShareRuntime, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var item model.PeerShareRuntime
	err := r.db.Where("id = ?", id).First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func (r *Repository) ListActivePeerShareRuntimesByShareID(shareID int64) ([]model.PeerShareRuntime, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var out []model.PeerShareRuntime
	err := r.db.Where("share_id = ? AND status = 1", shareID).Order("port ASC, id ASC").Find(&out).Error
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = make([]model.PeerShareRuntime, 0)
	}
	return out, nil
}

func (r *Repository) ListActivePeerShareRuntimePorts(shareID int64, nodeID int64) ([]int, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var ports []int
	err := r.db.Model(&model.PeerShareRuntime{}).
		Where("share_id = ? AND node_id = ? AND status = 1 AND port > 0", shareID, nodeID).
		Pluck("port", &ports).Error
	if err != nil {
		return nil, err
	}
	if ports == nil {
		ports = make([]int, 0)
	}
	return ports, nil
}

func (r *Repository) ListActiveForwardPeerShareRuntimesByServiceName(serviceName string) ([]model.PeerShareRuntime, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var items []model.PeerShareRuntime
	err := r.db.Where("service_name = ? AND status = 1 AND role = ?", serviceName, "forward").
		Order("id ASC").
		Find(&items).Error
	if err != nil {
		return nil, err
	}
	if items == nil {
		items = make([]model.PeerShareRuntime, 0)
	}
	return items, nil
}

func (r *Repository) ListActiveForwardPeerShareRuntimesByNodeAndServiceName(nodeID int64, serviceName string) ([]model.PeerShareRuntime, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	serviceName = strings.TrimSpace(serviceName)
	if serviceName == "" {
		return []model.PeerShareRuntime{}, nil
	}
	var items []model.PeerShareRuntime
	err := r.db.Where("node_id = ? AND service_name = ? AND status = 1 AND role = ?", nodeID, serviceName, "forward").
		Order("id ASC").
		Find(&items).Error
	if err != nil {
		return nil, err
	}
	if items == nil {
		items = make([]model.PeerShareRuntime, 0)
	}
	return items, nil
}

func (r *Repository) ListActiveForwardPeerShareRuntimesByNode(nodeID int64) ([]model.PeerShareRuntime, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var items []model.PeerShareRuntime
	err := r.db.Where("node_id = ? AND status = 1 AND role = ?", nodeID, "forward").Find(&items).Error
	if err != nil {
		return nil, err
	}
	if items == nil {
		items = make([]model.PeerShareRuntime, 0)
	}
	return items, nil
}

func (r *Repository) GetActiveForwardPeerShareRuntimeByPort(shareID int64, port int) (*model.PeerShareRuntime, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var item model.PeerShareRuntime
	err := r.db.Where("share_id = ? AND port = ? AND status = 1 AND role = ?", shareID, port, "forward").First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func (r *Repository) GetActiveForwardPeerShareRuntimeByServiceName(shareID int64, serviceName string) (*model.PeerShareRuntime, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	serviceName = strings.TrimSpace(serviceName)
	if shareID <= 0 || serviceName == "" {
		return nil, nil
	}
	var item model.PeerShareRuntime
	err := r.db.Where("share_id = ? AND service_name = ? AND status = 1 AND role = ?", shareID, serviceName, "forward").
		Order("id ASC").
		First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func (r *Repository) ExistsActivePeerShareRuntimeOnNodePort(nodeID int64, port int) (bool, error) {
	if r == nil || r.db == nil {
		return false, errors.New("repository not initialized")
	}
	var count int64
	err := r.db.Model(&model.PeerShareRuntime{}).
		Where("node_id = ? AND port = ? AND status = 1", nodeID, port).
		Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *Repository) UpdatePeerShareRuntimeServiceName(id int64, serviceName string, updatedTime int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Model(&model.PeerShareRuntime{}).Where("id = ?", id).Updates(map[string]interface{}{
		"service_name": serviceName,
		"applied":      1,
		"updated_time": updatedTime,
	}).Error
}

func (r *Repository) MarkPeerShareRuntimeReleasedByPort(shareID int64, port int, updatedTime int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return markPeerShareRuntimeReleasedByPortTx(r.db, shareID, port, updatedTime)
}

func markPeerShareRuntimeReleasedByPortTx(tx *gorm.DB, shareID int64, port int, updatedTime int64) error {
	if tx == nil {
		return errors.New("database unavailable")
	}
	if shareID <= 0 || port <= 0 {
		return nil
	}
	if updatedTime <= 0 {
		updatedTime = unixMilliNow()
	}
	return tx.Model(&model.PeerShareRuntime{}).Where("share_id = ? AND port = ? AND status = 1", shareID, port).Updates(map[string]interface{}{
		"status":       0,
		"applied":      0,
		"service_name": "",
		"updated_time": updatedTime,
	}).Error
}

func (r *Repository) MarkForwardPeerShareRuntimeReleasedByServiceName(shareID int64, serviceName string, updatedTime int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	serviceName = strings.TrimSpace(serviceName)
	if shareID <= 0 || serviceName == "" {
		return nil
	}
	if updatedTime <= 0 {
		updatedTime = unixMilliNow()
	}
	return r.db.Model(&model.PeerShareRuntime{}).
		Where("share_id = ? AND status = 1 AND role = ? AND service_name = ?", shareID, "forward", serviceName).
		Updates(map[string]interface{}{
			"status":       0,
			"applied":      0,
			"service_name": "",
			"updated_time": updatedTime,
		}).Error
}

// ─── FederationTunnelBinding ─────────────────────────────────────────

func (r *Repository) UpsertFederationTunnelBinding(item *model.FederationTunnelBinding) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	if item == nil {
		return errors.New("binding item is nil")
	}
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "tunnel_id"}, {Name: "node_id"}, {Name: "chain_type"}, {Name: "hop_inx"},
		},
		DoUpdates: clause.AssignmentColumns([]string{
			"remote_url", "resource_key", "remote_binding_id",
			"allocated_port", "status", "updated_time",
		}),
	}).Create(item).Error
}

func (r *Repository) ListActiveFederationTunnelBindingsByTunnel(tunnelID int64) ([]model.FederationTunnelBinding, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var out []model.FederationTunnelBinding
	err := r.db.Where("tunnel_id = ? AND status = 1", tunnelID).
		Order("chain_type ASC, hop_inx ASC, id ASC").Find(&out).Error
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = make([]model.FederationTunnelBinding, 0)
	}
	return out, nil
}

func (r *Repository) DeleteFederationTunnelBindingsByTunnel(tunnelID int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Where("tunnel_id = ?", tunnelID).Delete(&model.FederationTunnelBinding{}).Error
}

// ─── Export Methods ──────────────────────────────────────────────────

// withContext 返回共享同一连接池、但 DB 会话绑定 ctx 的仓库克隆。
// X-P3-21+X-P3-25（L-08-3＋L-10-1）：Export/Import 经克隆把请求
// ctx 下传到每条查询（GORM WithContext），客户端断连后导出不再
// 照跑到底；导入整单事务语义维持（有意），ctx 取消可中止等待。
func (r *Repository) withContext(ctx context.Context) *Repository {
	if r == nil || r.db == nil || ctx == nil {
		return r
	}
	clone := *r
	clone.db = r.db.WithContext(ctx)
	return &clone
}

// ExportAllContext 同 ExportAll，但全部查询绑定 ctx。
func (r *Repository) ExportAllContext(ctx context.Context) (*model.BackupData, error) {
	return r.withContext(ctx).ExportAll()
}

// ExportPartialContext 同 ExportPartial，但全部查询绑定 ctx。
func (r *Repository) ExportPartialContext(ctx context.Context, types []string) (*model.BackupData, error) {
	return r.withContext(ctx).ExportPartial(types)
}

// ImportContext 同 Import（整单事务语义不变），但事务绑定 ctx：
// 请求取消/断连可中止导入的连接等待，不再无限排队。
func (r *Repository) ImportContext(ctx context.Context, backup *model.BackupData, types []string) (*model.ImportResult, error) {
	return r.withContext(ctx).Import(backup, types)
}

func (r *Repository) ExportAll() (*model.BackupData, error) {
	backup := &model.BackupData{Version: "1.0", ExportedAt: unixMilliNow()}

	users, err := r.exportUsers()
	if err != nil {
		return nil, fmt.Errorf("export users failed: %w", err)
	}
	backup.Users = users

	nodes, err := r.exportNodes()
	if err != nil {
		return nil, fmt.Errorf("export nodes failed: %w", err)
	}
	backup.Nodes = nodes

	// FIX132-C（A3-05）：SSH 配置与面板共享定义随 nodes 段导出。
	nodeSSHConfigs, err := r.exportNodeSSHConfigs()
	if err != nil {
		return nil, fmt.Errorf("export node ssh configs failed: %w", err)
	}
	backup.NodeSSHConfigs = nodeSSHConfigs

	peerShares, err := r.exportPeerShares()
	if err != nil {
		return nil, fmt.Errorf("export peer shares failed: %w", err)
	}
	backup.PeerShares = peerShares

	tunnels, err := r.exportTunnels()
	if err != nil {
		return nil, fmt.Errorf("export tunnels failed: %w", err)
	}
	backup.Tunnels = tunnels

	forwards, err := r.exportForwards()
	if err != nil {
		return nil, fmt.Errorf("export forwards failed: %w", err)
	}
	backup.Forwards = forwards

	userTunnels, err := r.exportUserTunnels()
	if err != nil {
		return nil, fmt.Errorf("export user tunnels failed: %w", err)
	}
	backup.UserTunnels = userTunnels

	speedLimits, err := r.exportSpeedLimits()
	if err != nil {
		return nil, fmt.Errorf("export speed limits failed: %w", err)
	}
	backup.SpeedLimits = speedLimits

	tunnelGroups, err := r.exportTunnelGroups()
	if err != nil {
		return nil, fmt.Errorf("export tunnel groups failed: %w", err)
	}
	backup.TunnelGroups = tunnelGroups

	userGroups, err := r.exportUserGroups()
	if err != nil {
		return nil, fmt.Errorf("export user groups failed: %w", err)
	}
	backup.UserGroups = userGroups

	permissions, err := r.exportPermissions()
	if err != nil {
		return nil, fmt.Errorf("export permissions failed: %w", err)
	}
	backup.Permissions = permissions

	configs, err := r.ListConfigs()
	if err != nil {
		return nil, fmt.Errorf("export configs failed: %w", err)
	}
	backup.Configs = FilterBackupConfigs(configs)

	inbounds, err := r.exportInbounds()
	if err != nil {
		return nil, fmt.Errorf("export inbounds failed: %w", err)
	}
	backup.Inbounds = inbounds

	// O7-001：导出 user_inbound 表（订阅分配关系）
	userInbounds, err := r.exportUserInbounds()
	if err != nil {
		return nil, fmt.Errorf("export user inbounds failed: %w", err)
	}
	backup.UserInbounds = userInbounds

	// O7-007：导出 announcement 整表（公告）
	announcements, err := r.exportAnnouncements()
	if err != nil {
		return nil, fmt.Errorf("export announcements failed: %w", err)
	}
	backup.Announcements = announcements

	// O7-008：导出 monitor_permission 整表（监控权限）
	monitorPermissions, err := r.exportMonitorPermissions()
	if err != nil {
		return nil, fmt.Errorf("export monitor permissions failed: %w", err)
	}
	backup.MonitorPermissions = monitorPermissions

	// B4/F-004：导出 user_subscription 整表（订阅分配关系）
	userSubscriptions, err := r.exportUserSubscriptions()
	if err != nil {
		return nil, fmt.Errorf("export user subscriptions failed: %w", err)
	}
	backup.UserSubscriptions = userSubscriptions

	// B4/F-030：导出 service_monitor 整表（监控定义）
	serviceMonitors, err := r.exportServiceMonitors()
	if err != nil {
		return nil, fmt.Errorf("export service monitors failed: %w", err)
	}
	backup.ServiceMonitors = serviceMonitors

	return backup, nil
}

func (r *Repository) ExportPartial(types []string) (*model.BackupData, error) {
	backup := &model.BackupData{Version: "1.0", ExportedAt: unixMilliNow()}
	typeSet := make(map[string]bool)
	for _, t := range types {
		typeSet[t] = true
	}

	if typeSet["users"] {
		v, err := r.exportUsers()
		if err != nil {
			return nil, fmt.Errorf("export users failed: %w", err)
		}
		backup.Users = v
	}
	if typeSet["nodes"] {
		v, err := r.exportNodes()
		if err != nil {
			return nil, fmt.Errorf("export nodes failed: %w", err)
		}
		backup.Nodes = v
		// A3-05：SSH 配置与面板共享定义随 nodes 段。
		sc, err := r.exportNodeSSHConfigs()
		if err != nil {
			return nil, fmt.Errorf("export node ssh configs failed: %w", err)
		}
		backup.NodeSSHConfigs = sc
		ps, err := r.exportPeerShares()
		if err != nil {
			return nil, fmt.Errorf("export peer shares failed: %w", err)
		}
		backup.PeerShares = ps
	}
	if typeSet["tunnels"] {
		v, err := r.exportTunnels()
		if err != nil {
			return nil, fmt.Errorf("export tunnels failed: %w", err)
		}
		backup.Tunnels = v
	}
	if typeSet["forwards"] {
		v, err := r.exportForwards()
		if err != nil {
			return nil, fmt.Errorf("export forwards failed: %w", err)
		}
		backup.Forwards = v
	}
	if typeSet["userTunnels"] {
		v, err := r.exportUserTunnels()
		if err != nil {
			return nil, fmt.Errorf("export user tunnels failed: %w", err)
		}
		backup.UserTunnels = v
	}
	if typeSet["speedLimits"] {
		v, err := r.exportSpeedLimits()
		if err != nil {
			return nil, fmt.Errorf("export speed limits failed: %w", err)
		}
		backup.SpeedLimits = v
	}
	if typeSet["tunnelGroups"] {
		v, err := r.exportTunnelGroups()
		if err != nil {
			return nil, fmt.Errorf("export tunnel groups failed: %w", err)
		}
		backup.TunnelGroups = v
	}
	if typeSet["userGroups"] {
		v, err := r.exportUserGroups()
		if err != nil {
			return nil, fmt.Errorf("export user groups failed: %w", err)
		}
		backup.UserGroups = v
	}
	if typeSet["permissions"] {
		v, err := r.exportPermissions()
		if err != nil {
			return nil, fmt.Errorf("export permissions failed: %w", err)
		}
		backup.Permissions = v
	}
	if typeSet["configs"] {
		v, err := r.ListConfigs()
		if err != nil {
			return nil, fmt.Errorf("export configs failed: %w", err)
		}
		backup.Configs = FilterBackupConfigs(v)
	}
	if typeSet["inbounds"] {
		v, err := r.exportInbounds()
		if err != nil {
			return nil, fmt.Errorf("export inbounds failed: %w", err)
		}
		backup.Inbounds = v
	}
	// O7-001/O7-007/O7-008：补齐 user_inbound/announcement/monitor_permission 三表
	// （O7-001 时 ExportPartial 遗漏，此处一并补上）
	if typeSet["userInbounds"] {
		v, err := r.exportUserInbounds()
		if err != nil {
			return nil, fmt.Errorf("export user inbounds failed: %w", err)
		}
		backup.UserInbounds = v
	}
	if typeSet["announcements"] {
		v, err := r.exportAnnouncements()
		if err != nil {
			return nil, fmt.Errorf("export announcements failed: %w", err)
		}
		backup.Announcements = v
	}
	if typeSet["monitorPermissions"] {
		v, err := r.exportMonitorPermissions()
		if err != nil {
			return nil, fmt.Errorf("export monitor permissions failed: %w", err)
		}
		backup.MonitorPermissions = v
	}
	// B4/F-004/F-030：补齐 user_subscription/service_monitor 两表
	if typeSet["userSubscriptions"] {
		v, err := r.exportUserSubscriptions()
		if err != nil {
			return nil, fmt.Errorf("export user subscriptions failed: %w", err)
		}
		backup.UserSubscriptions = v
	}
	if typeSet["serviceMonitors"] {
		v, err := r.exportServiceMonitors()
		if err != nil {
			return nil, fmt.Errorf("export service monitors failed: %w", err)
		}
		backup.ServiceMonitors = v
	}
	return backup, nil
}

func (r *Repository) exportUsers() ([]model.UserBackup, error) {
	var users []model.User
	if err := r.db.Order("id ASC").Find(&users).Error; err != nil {
		return nil, err
	}
	userIDs := make([]int64, 0, len(users))
	for _, u := range users {
		userIDs = append(userIDs, u.ID)
	}
	quotaMap, err := r.ListUserQuotaViewsByUserIDs(userIDs, time.Now())
	if err != nil {
		return nil, err
	}
	out := make([]model.UserBackup, 0, len(users))
	for _, u := range users {
		b := model.UserBackup{
			ID: u.ID, User: u.User, Pwd: u.Pwd, RoleID: u.RoleID,
			ExpTime: u.ExpTime, Flow: u.Flow, FlowMiB: u.FlowMiB, InFlow: u.InFlow, OutFlow: u.OutFlow,
			FlowResetTime: u.FlowResetTime, Num: u.Num,
			CreatedTime: u.CreatedTime, Status: u.Status,
			PasswordChangedAt: u.PasswordChangedAt, MaxConn: u.MaxConn,
		}
		if quota := quotaMap[u.ID]; quota != nil {
			b.DailyQuotaGB = quota.DailyLimitGB
			b.MonthlyQuotaGB = quota.MonthlyLimitGB
			b.DisabledByQuota = quota.DisabledByQuota
			b.QuotaDisabledAt = quota.DisabledAt
			// D4-01：已用量、暂停清单与窗口键一并导出（视图已按当前
			// 窗口归一，过期窗口的已用量在视图层即为 0）。
			b.DailyUsedBytes = quota.DailyUsedBytes
			b.MonthlyUsedBytes = quota.MonthlyUsedBytes
			b.PausedForwardIDs = quota.PausedForwardIDs
			b.QuotaDayKey = quota.DayKey
			b.QuotaMonthKey = quota.MonthKey
		}
		if u.UpdatedTime.Valid {
			b.UpdatedTime = u.UpdatedTime.Int64
		}
		out = append(out, b)
	}
	return out, nil
}

func (r *Repository) exportNodes() ([]model.NodeBackup, error) {
	var nodes []model.Node
	if err := r.db.Order("inx ASC, id ASC").Find(&nodes).Error; err != nil {
		return nil, err
	}
	out := make([]model.NodeBackup, 0, len(nodes))
	for _, n := range nodes {
		b := model.NodeBackup{
			ID: n.ID, Name: n.Name, Secret: n.Secret, ServerIP: n.ServerIP,
			Remark: n.Remark.String, RenewalCycle: n.RenewalCycle.String,
			Port: n.Port, HTTP: n.HTTP, TLS: n.TLS, Socks: n.Socks,
			CreatedTime: n.CreatedTime, Status: n.Status,
			TCPListenAddr: n.TCPListenAddr, UDPListenAddr: n.UDPListenAddr,
			Inx: n.Inx, IsRemote: n.IsRemote,
		}
		if n.ExpiryTime.Valid {
			b.ExpiryTime = n.ExpiryTime.Int64
		}
		if n.UpdatedTime.Valid {
			b.UpdatedTime = n.UpdatedTime.Int64
		}
		if n.ServerIPV4.Valid {
			b.ServerIPv4 = n.ServerIPV4.String
		}
		if n.ServerIPV6.Valid {
			b.ServerIPv6 = n.ServerIPV6.String
		}
		// B4/F-027：ExtraIPs 字段在 NodeBackup 中存在但导出曾漏写，
		// 多 IP 节点恢复后附加 IP 全丢。
		if n.ExtraIPs.Valid {
			b.ExtraIPs = n.ExtraIPs.String
		}
		if n.InterfaceName.Valid {
			b.InterfaceName = n.InterfaceName.String
		}
		if n.Version.Valid {
			b.Version = n.Version.String
		}
		if n.RemoteURL.Valid {
			b.RemoteURL = n.RemoteURL.String
		}
		if n.RemoteToken.Valid {
			b.RemoteToken = n.RemoteToken.String
		}
		if n.RemoteConfig.Valid {
			b.RemoteConfig = n.RemoteConfig.String
		}
		b.LatencyTargets = n.LatencyTargets
		b.ForwardMode = n.ForwardMode
		// O7-006：恢复后保留已忽略的到期提醒状态
		b.ExpiryReminderDismissed = n.ExpiryReminderDismissed
		out = append(out, b)
	}
	return out, nil
}

// exportNodeSSHConfigs 导出 node_ssh_config 全表（A3-05）。
func (r *Repository) exportNodeSSHConfigs() ([]model.NodeSSHConfigBackup, error) {
	var rows []model.NodeSSHConfig
	if err := r.db.Order("node_id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]model.NodeSSHConfigBackup, 0, len(rows))
	for _, c := range rows {
		out = append(out, model.NodeSSHConfigBackup{
			NodeID: c.NodeID, Host: c.Host, Port: c.Port, Username: c.Username,
			AuthType: c.AuthType, Password: c.Password.String, PrivateKey: c.PrivateKey.String,
			Passphrase: c.Passphrase.String, SudoMode: c.SudoMode, CreatedTime: c.CreatedTime,
		})
	}
	return out, nil
}

// exportPeerShares 导出 peer_share 定义全表（A3-05；runtime/resource
// 等运行时表按登记排除，不在此列）。
func (r *Repository) exportPeerShares() ([]model.PeerShareBackup, error) {
	var rows []model.PeerShare
	if err := r.db.Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]model.PeerShareBackup, 0, len(rows))
	for _, s := range rows {
		out = append(out, model.PeerShareBackup{
			ID: s.ID, Name: s.Name, NodeID: s.NodeID, Token: s.Token,
			MaxBandwidth: s.MaxBandwidth, ExpiryTime: s.ExpiryTime,
			PortRangeStart: s.PortRangeStart, PortRangeEnd: s.PortRangeEnd,
			CurrentFlow: s.CurrentFlow, IsActive: s.IsActive,
			CreatedTime: s.CreatedTime, UpdatedTime: s.UpdatedTime,
			AllowedDomains: s.AllowedDomains, AllowedIPs: s.AllowedIPs,
			RelayUsername: s.RelayUsername, RelayPassword: s.RelayPassword,
		})
	}
	return out, nil
}

func (r *Repository) exportTunnels() ([]model.TunnelBackup, error) {
	var tunnels []model.Tunnel
	if err := r.db.Order("inx ASC, id ASC").Find(&tunnels).Error; err != nil {
		return nil, err
	}
	out := make([]model.TunnelBackup, 0, len(tunnels))
	for _, t := range tunnels {
		b := model.TunnelBackup{
			ID: t.ID, Name: t.Name, TrafficRatio: t.TrafficRatio,
			Type: t.Type, Protocol: t.Protocol, Flow: t.Flow,
			CreatedTime: t.CreatedTime, UpdatedTime: t.UpdatedTime,
			Status: t.Status, Inx: t.Inx, IPPreference: t.IPPreference,
			ProbeTargetHost: t.ProbeTargetHost, ProbeTargetPort: t.ProbeTargetPort,
			Engine: t.Engine,
		}
		if t.InIP.Valid {
			b.InIP = t.InIP.String
		}
		chains, err := r.exportChainTunnels(t.ID)
		if err != nil {
			return nil, err
		}
		b.ChainTunnels = chains
		out = append(out, b)
	}
	return out, nil
}

func (r *Repository) exportChainTunnels(tunnelID int64) ([]model.ChainTunnelBackup, error) {
	var chains []model.ChainTunnel
	if err := r.db.Where("tunnel_id = ?", tunnelID).Order("inx ASC, id ASC").Find(&chains).Error; err != nil {
		return nil, err
	}
	out := make([]model.ChainTunnelBackup, 0, len(chains))
	for _, c := range chains {
		// BUG-A2-3：可空列仅在 Valid 时写指针，NULL 在备份中缺失，
		// 与零值严格区分（旧实现把 NULL 与 0/'' 一并丢弃）。
		b := model.ChainTunnelBackup{
			ID: c.ID, TunnelID: c.TunnelID, ChainType: c.ChainType, NodeID: c.NodeID,
			Port:      nullInt64Ptr(c.Port),
			Strategy:  nullStringPtr(c.Strategy),
			Inx:       nullInt64Ptr(c.Inx),
			Protocol:  nullStringPtr(c.Protocol),
			ConnectIP: nullStringPtr(c.ConnectIP),
		}
		out = append(out, b)
	}
	return out, nil
}

// nullInt64Ptr / nullStringPtr：导出侧把 sql.Null* 转为备份指针
// （NULL → nil，备份 JSON 中缺失）。
func nullInt64Ptr(v sql.NullInt64) *int {
	if !v.Valid {
		return nil
	}
	n := int(v.Int64)
	return &n
}

func nullStringPtr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	s := v.String
	return &s
}

// ptrToNullInt64 / ptrToNullString：导入侧把备份指针还原为
// sql.Null*（nil → NULL），与导出严格互逆。
func ptrToNullInt64(p *int) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*p), Valid: true}
}

func ptrToNullString(p *string) sql.NullString {
	if p == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *p, Valid: true}
}

func (r *Repository) exportForwards() ([]model.ForwardBackup, error) {
	var forwards []model.Forward
	if err := r.db.Order("id ASC").Find(&forwards).Error; err != nil {
		return nil, err
	}
	out := make([]model.ForwardBackup, 0, len(forwards))
	for _, f := range forwards {
		b := model.ForwardBackup{
			ID: f.ID, UserID: f.UserID, UserName: f.UserName, Name: f.Name,
			TunnelID: f.TunnelID, RemoteAddr: f.RemoteAddr, Strategy: f.Strategy,
			InFlow: f.InFlow, OutFlow: f.OutFlow, CreatedTime: f.CreatedTime,
			UpdatedTime: f.UpdatedTime, Status: f.Status, Inx: f.Inx,
			MaxConn:              f.MaxConn,
			IPMaxConn:            f.IPMaxConn,
			ProxyProtocol:        f.ProxyProtocol,
			ProxyProtocolReceive: f.ProxyProtocolReceive,
			ProxyProtocolSend:    f.ProxyProtocolSend,
		}
		if f.SpeedID.Valid {
			v := f.SpeedID.Int64
			b.SpeedID = &v
		}
		if f.IPSpeedID.Valid {
			v := f.IPSpeedID.Int64
			b.IPSpeedID = &v
		}
		ports, err := r.exportForwardPorts(f.ID)
		if err != nil {
			return nil, err
		}
		portsCopy := append([]model.ForwardPortBackup(nil), ports...)
		b.ForwardPorts = &portsCopy
		out = append(out, b)
	}
	return out, nil
}

func (r *Repository) exportForwardPorts(forwardID int64) ([]model.ForwardPortBackup, error) {
	var fps []model.ForwardPort
	if err := r.db.Where("forward_id = ?", forwardID).Order("id ASC").Find(&fps).Error; err != nil {
		return nil, err
	}
	out := make([]model.ForwardPortBackup, 0, len(fps))
	for _, fp := range fps {
		b := model.ForwardPortBackup{NodeID: fp.NodeID, Port: fp.Port}
		if fp.InIP.Valid {
			b.InIP = fp.InIP.String
		}
		out = append(out, b)
	}
	return out, nil
}

func (r *Repository) exportUserTunnels() ([]model.UserTunnelBackup, error) {
	var uts []model.UserTunnel
	if err := r.db.Order("id ASC").Find(&uts).Error; err != nil {
		return nil, err
	}
	out := make([]model.UserTunnelBackup, 0, len(uts))
	for _, ut := range uts {
		b := model.UserTunnelBackup{
			ID: ut.ID, UserID: ut.UserID, TunnelID: ut.TunnelID,
			Num: ut.Num, Flow: ut.Flow, FlowMiB: ut.FlowMiB, InFlow: ut.InFlow, OutFlow: ut.OutFlow,
			FlowResetTime: ut.FlowResetTime, ExpTime: ut.ExpTime, Status: ut.Status,
		}
		if ut.SpeedID.Valid {
			b.SpeedID = ut.SpeedID.Int64
		}
		out = append(out, b)
	}
	return out, nil
}

func (r *Repository) exportSpeedLimits() ([]model.SpeedLimitBackup, error) {
	var sls []model.SpeedLimit
	if err := r.db.Order("id ASC").Find(&sls).Error; err != nil {
		return nil, err
	}
	out := make([]model.SpeedLimitBackup, 0, len(sls))
	for _, sl := range sls {
		b := model.SpeedLimitBackup{
			ID: sl.ID, Name: sl.Name, Speed: int64(sl.Speed),
			CreatedTime: sl.CreatedTime, Status: sl.Status,
		}
		// G2 审计补漏：TunnelID/TunnelName 字段在备份结构里早已存在，
		// 但导出从不赋值、导入写死 NULL——绑定隧道的限速规则恢复后
		// 会静默变成全局规则。与 BUG-A2-3 同属备份往返丢可空字段。
		if sl.TunnelID.Valid {
			v := sl.TunnelID.Int64
			b.TunnelID = &v
		}
		if sl.TunnelName.Valid {
			b.TunnelName = sl.TunnelName.String
		}
		if sl.UpdatedTime.Valid {
			b.UpdatedTime = sl.UpdatedTime.Int64
		}
		out = append(out, b)
	}
	return out, nil
}

// O7-001：导出 user_inbound 表（用户订阅分配关系）
func (r *Repository) exportUserInbounds() ([]model.UserInboundBackup, error) {
	var rows []model.UserInbound
	if err := r.db.Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]model.UserInboundBackup, 0, len(rows))
	for _, row := range rows {
		out = append(out, model.UserInboundBackup{
			ID: row.ID, UserID: row.UserID, InboundID: row.InboundID, RelayID: row.RelayID,
		})
	}
	return out, nil
}

// B4/F-004：导出 user_subscription 整表（订阅分配关系）
func (r *Repository) exportUserSubscriptions() ([]model.UserSubscriptionBackup, error) {
	var rows []model.UserSubscription
	if err := r.db.Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]model.UserSubscriptionBackup, 0, len(rows))
	for _, row := range rows {
		out = append(out, model.UserSubscriptionBackup{
			ID: row.ID, UserID: row.UserID, SubscriptionID: row.SubscriptionID, CreatedTime: row.CreatedTime,
		})
	}
	return out, nil
}

// B4/F-030：导出 service_monitor 整表（监控定义）
func (r *Repository) exportServiceMonitors() ([]model.ServiceMonitorBackup, error) {
	var rows []model.ServiceMonitor
	if err := r.db.Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]model.ServiceMonitorBackup, 0, len(rows))
	for _, row := range rows {
		out = append(out, model.ServiceMonitorBackup{
			ID: row.ID, Name: row.Name, Type: row.Type, Target: row.Target,
			IntervalSec: row.IntervalSec, TimeoutSec: row.TimeoutSec,
			NodeID: row.NodeID, Enabled: row.Enabled,
			CreatedTime: row.CreatedTime, UpdatedTime: row.UpdatedTime,
		})
	}
	return out, nil
}

// O7-007：导出 announcement 整表（公告备份）
func (r *Repository) exportAnnouncements() ([]model.AnnouncementBackup, error) {
	var rows []model.Announcement
	if err := r.db.Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]model.AnnouncementBackup, 0, len(rows))
	for _, a := range rows {
		b := model.AnnouncementBackup{
			ID:          a.ID,
			Content:     a.Content,
			Enabled:     a.Enabled,
			CreatedTime: a.CreatedTime,
		}
		if a.UpdatedTime.Valid {
			b.UpdatedTime = a.UpdatedTime.Int64
		}
		out = append(out, b)
	}
	return out, nil
}

// O7-008：导出 monitor_permission 整表（监控权限备份）
func (r *Repository) exportMonitorPermissions() ([]model.MonitorPermissionBackup, error) {
	var rows []model.MonitorPermission
	if err := r.db.Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]model.MonitorPermissionBackup, 0, len(rows))
	for _, mp := range rows {
		out = append(out, model.MonitorPermissionBackup{
			ID:          mp.ID,
			UserID:      mp.UserID,
			CreatedTime: mp.CreatedTime,
		})
	}
	return out, nil
}

func (r *Repository) exportTunnelGroups() ([]model.TunnelGroupBackup, error) {
	var groups []model.TunnelGroup
	if err := r.db.Order("id ASC").Find(&groups).Error; err != nil {
		return nil, err
	}
	out := make([]model.TunnelGroupBackup, 0, len(groups))
	for _, tg := range groups {
		b := model.TunnelGroupBackup{
			ID: tg.ID, Name: tg.Name, CreatedTime: tg.CreatedTime,
			UpdatedTime: tg.UpdatedTime, Status: tg.Status,
		}
		var tunnelIDs []int64
		r.db.Model(&model.TunnelGroupTunnel{}).Where("tunnel_group_id = ?", tg.ID).Pluck("tunnel_id", &tunnelIDs)
		b.Tunnels = tunnelIDs
		out = append(out, b)
	}
	return out, nil
}

func (r *Repository) exportUserGroups() ([]model.UserGroupBackup, error) {
	var groups []model.UserGroup
	if err := r.db.Order("id ASC").Find(&groups).Error; err != nil {
		return nil, err
	}
	out := make([]model.UserGroupBackup, 0, len(groups))
	for _, ug := range groups {
		b := model.UserGroupBackup{
			ID: ug.ID, Name: ug.Name, CreatedTime: ug.CreatedTime,
			UpdatedTime: ug.UpdatedTime, Status: ug.Status,
		}
		var userIDs []int64
		r.db.Model(&model.UserGroupUser{}).Where("user_group_id = ?", ug.ID).Pluck("user_id", &userIDs)
		b.Users = userIDs
		out = append(out, b)
	}
	return out, nil
}

func (r *Repository) exportPermissions() ([]model.PermissionBackup, error) {
	var perms []model.GroupPermission
	if err := r.db.Order("id ASC").Find(&perms).Error; err != nil {
		return nil, err
	}
	out := make([]model.PermissionBackup, 0, len(perms))
	for _, p := range perms {
		b := model.PermissionBackup{
			ID: p.ID, UserGroupID: p.UserGroupID, TunnelGroupID: p.TunnelGroupID,
			CreatedTime: p.CreatedTime,
		}
		var grants []model.GroupPermissionGrant
		r.db.Where("user_group_id = ? AND tunnel_group_id = ?", p.UserGroupID, p.TunnelGroupID).Find(&grants)
		for _, g := range grants {
			b.Grants = append(b.Grants, model.PermissionGrantBackup{
				ID: g.ID, UserGroupID: g.UserGroupID, TunnelGroupID: g.TunnelGroupID,
				UserTunnelID: g.UserTunnelID, CreatedTime: g.CreatedTime,
				CreatedByGroup: g.CreatedByGroup,
			})
		}
		out = append(out, b)
	}
	return out, nil
}

// ─── Import Methods ──────────────────────────────────────────────────

// importReport 收集一次 Import 的跳过与提示，最终落入 ImportResult
// 回执（FIX132-C / D4-03：恢复不再静默写悬空行、静默顶替或整单回滚，
// 无法落位的行跳过并如实计数、顶替与降级如实提示，对齐订阅段
// BUG-A2-4 的标杆口径）。
type importReport struct {
	skipped            map[string]int
	warnings           []string
	restoredSSHNodeIDs []int64
	// R3X-005 facet (a)：导入事务内撤销的组建授权，最终经
	// ImportResult.RevokedUserTunnels 回传 handler 清转发。
	revoked []RevokedUserTunnelPair
}

// addRevoked 登记导入撤销的 (user, tunnel) 对，按对去重（同一 UT
// 可能先后命中成员替换与停用收敛两条路径，删行只发生一次）。
func (rep *importReport) addRevoked(pairs []RevokedUserTunnelPair) {
	if rep == nil {
		return
	}
	for _, p := range pairs {
		dup := false
		for _, q := range rep.revoked {
			if q.UserID == p.UserID && q.TunnelID == p.TunnelID {
				dup = true
				break
			}
		}
		if !dup {
			rep.revoked = append(rep.revoked, p)
		}
	}
}

func newImportReport() *importReport {
	return &importReport{skipped: make(map[string]int)}
}

func (rep *importReport) skip(segment string) {
	if rep == nil {
		return
	}
	rep.skipped[segment]++
}

func (rep *importReport) warnf(format string, args ...interface{}) {
	if rep == nil {
		return
	}
	rep.warnings = append(rep.warnings, fmt.Sprintf(format, args...))
}

// rowExistsTx 判断 id 行是否存在（引用存在性校验，D4-03）。
func rowExistsTx(tx *gorm.DB, model interface{}, id int64) (bool, error) {
	if id <= 0 {
		return false, nil
	}
	var n int64
	if err := tx.Model(model).Where("id = ?", id).Count(&n).Error; err != nil {
		return false, err
	}
	return n > 0, nil
}

func (r *Repository) Import(backup *model.BackupData, types []string) (*model.ImportResult, error) {
	// O7-009：版本兼容性检查（导出仅写 "1.0"），未知版本拒绝导入，
	// 避免不兼容格式被错误恢复。
	if backup == nil || backup.Version != "1.0" {
		version := ""
		if backup != nil {
			version = backup.Version
		}
		return nil, fmt.Errorf("不支持的备份版本 %q（仅支持 1.0）", version)
	}
	result := &model.ImportResult{}
	typeSet := make(map[string]bool)
	for _, t := range types {
		typeSet[t] = true
	}

	// B4/F-055: 端口预检 + per-node 锁（见 repository_backup_precheck.go）。
	claims := backupClaims(backup, typeSet)
	releasePortLocks := lockBackupPortNodes(claims)
	defer releasePortLocks()
	if err := r.precheckBackupPortConflicts(backup, typeSet, claims); err != nil {
		return nil, err
	}

	rep := newImportReport()
	err := r.db.Transaction(func(tx *gorm.DB) error {
		now := unixMilliNow()
		// R4X-006：事务尾授权收口只裁定本次导入新落的行——先快照
		// userTunnels / forwards 备份 id 在目标库的存量存在性，尾部
		// 清扫据此区分「本次复活」与「存量行被刷新」（后者是目标库
		// 现行数据，不在导入裁定范围内）。
		preUserTunnelIDs := make(map[int64]bool)
		if typeSet["userTunnels"] && len(backup.UserTunnels) > 0 {
			ids := make([]int64, 0, len(backup.UserTunnels))
			for _, ut := range backup.UserTunnels {
				ids = append(ids, ut.ID)
			}
			var existing []int64
			if err := tx.Model(&model.UserTunnel{}).Where("id IN ?", ids).Pluck("id", &existing).Error; err != nil {
				return fmt.Errorf("import pre-snapshot user tunnels failed: %w", err)
			}
			for _, id := range existing {
				preUserTunnelIDs[id] = true
			}
		}
		preForwardIDs := make(map[int64]bool)
		if typeSet["forwards"] && len(backup.Forwards) > 0 {
			ids := make([]int64, 0, len(backup.Forwards))
			for _, f := range backup.Forwards {
				ids = append(ids, f.ID)
			}
			var existing []int64
			if err := tx.Model(&model.Forward{}).Where("id IN ?", ids).Pluck("id", &existing).Error; err != nil {
				return fmt.Errorf("import pre-snapshot forwards failed: %w", err)
			}
			for _, id := range existing {
				preForwardIDs[id] = true
			}
		}
		// BUG-A2-4：订阅在 inbounds 段按 token 重建、可能换 id；
		// subRemap 把备份订阅 id 映射到落库 id，供 userSubscriptions
		// 段重写分配引用（inbounds 段在本事务中先于该段执行）。
		subRemap := make(map[int64]int64)

		if typeSet["users"] && len(backup.Users) > 0 {
			count, err := importUsers(tx, backup.Users, now)
			if err != nil {
				return fmt.Errorf("import users failed: %w", err)
			}
			result.UsersImported = count
		}
		if typeSet["nodes"] && len(backup.Nodes) > 0 {
			count, err := importNodes(tx, backup.Nodes, now, rep)
			if err != nil {
				return fmt.Errorf("import nodes failed: %w", err)
			}
			result.NodesImported = count
		}
		// FIX132-C（A3-05）：SSH 配置与面板共享定义随 nodes 段恢复。
		if typeSet["nodes"] && len(backup.NodeSSHConfigs) > 0 {
			count, err := importNodeSSHConfigs(tx, backup.NodeSSHConfigs, now, rep)
			if err != nil {
				return fmt.Errorf("import node ssh configs failed: %w", err)
			}
			result.NodeSSHConfigsImported = count
		}
		if typeSet["nodes"] && len(backup.PeerShares) > 0 {
			count, err := importPeerShares(tx, backup.PeerShares, now, rep)
			if err != nil {
				return fmt.Errorf("import peer shares failed: %w", err)
			}
			result.PeerSharesImported = count
		}
		if typeSet["tunnels"] && len(backup.Tunnels) > 0 {
			count, err := importTunnels(tx, backup.Tunnels, now, rep)
			if err != nil {
				return fmt.Errorf("import tunnels failed: %w", err)
			}
			result.TunnelsImported = count
		}
		if typeSet["forwards"] && len(backup.Forwards) > 0 {
			count, err := importForwards(tx, backup.Forwards, now, rep)
			if err != nil {
				return fmt.Errorf("import forwards failed: %w", err)
			}
			result.ForwardsImported = count
		}
		if typeSet["userTunnels"] && len(backup.UserTunnels) > 0 {
			count, err := importUserTunnels(tx, backup.UserTunnels, now, rep)
			if err != nil {
				return fmt.Errorf("import user tunnels failed: %w", err)
			}
			result.UserTunnelsImported = count
		}
		if typeSet["speedLimits"] && len(backup.SpeedLimits) > 0 {
			count, err := importSpeedLimits(tx, backup.SpeedLimits, now, rep)
			if err != nil {
				return fmt.Errorf("import speed limits failed: %w", err)
			}
			result.SpeedLimitsImported = count
		}
		if typeSet["tunnelGroups"] && len(backup.TunnelGroups) > 0 {
			count, err := importTunnelGroups(r, tx, backup.TunnelGroups, now, rep)
			if err != nil {
				return fmt.Errorf("import tunnel groups failed: %w", err)
			}
			result.TunnelGroupsImported = count
		}
		if typeSet["userGroups"] && len(backup.UserGroups) > 0 {
			count, err := importUserGroups(r, tx, backup.UserGroups, now, rep)
			if err != nil {
				return fmt.Errorf("import user groups failed: %w", err)
			}
			result.UserGroupsImported = count
		}
		if typeSet["permissions"] && len(backup.Permissions) > 0 {
			count, err := importPermissions(r, tx, backup.Permissions, now, rep)
			if err != nil {
				return fmt.Errorf("import permissions failed: %w", err)
			}
			result.PermissionsImported = count
		}
		if typeSet["configs"] && len(backup.Configs) > 0 {
			count, err := importConfigs(tx, backup.Configs, now)
			if err != nil {
				return fmt.Errorf("import configs failed: %w", err)
			}
			result.ConfigsImported = count
		}
		if typeSet["inbounds"] && backup.Inbounds != nil {
			count, err := importInbounds(tx, backup.Inbounds, now, subRemap, rep)
			if err != nil {
				return fmt.Errorf("import inbounds failed: %w", err)
			}
			result.InboundsImported = count
		}
		// O7-001：导入 user_inbound 表（用户订阅分配关系）
		if typeSet["userInbounds"] && len(backup.UserInbounds) > 0 {
			count, err := importUserInbounds(tx, backup.UserInbounds, rep)
			if err != nil {
				return fmt.Errorf("import user inbounds failed: %w", err)
			}
			result.UserInboundsImported = count
		}
		// O7-007：导入 announcement 表（公告）
		if typeSet["announcements"] && len(backup.Announcements) > 0 {
			count, err := importAnnouncements(tx, backup.Announcements, now)
			if err != nil {
				return fmt.Errorf("import announcements failed: %w", err)
			}
			result.AnnouncementsImported = count
		}
		// O7-008：导入 monitor_permission 表（监控权限）
		if typeSet["monitorPermissions"] && len(backup.MonitorPermissions) > 0 {
			count, err := importMonitorPermissions(tx, backup.MonitorPermissions, rep)
			if err != nil {
				return fmt.Errorf("import monitor permissions failed: %w", err)
			}
			result.MonitorPermissionsImported = count
		}
		// B4/F-004：导入 user_subscription 表（订阅分配关系）。
		// 旧备份无该字段时 len=0 自动跳过，不报错。
		// BUG-A2-4：分配引用经 subRemap 重写到重建后的订阅 id；
		// 无法落位的分配跳过并计入 UserSubscriptionsSkipped 如实回执。
		if typeSet["userSubscriptions"] && len(backup.UserSubscriptions) > 0 {
			count, skipped, err := importUserSubscriptions(tx, backup.UserSubscriptions, subRemap)
			if err != nil {
				return fmt.Errorf("import user subscriptions failed: %w", err)
			}
			result.UserSubscriptionsImported = count
			result.UserSubscriptionsSkipped = skipped
		}
		// B4/F-030：导入 service_monitor 表（监控定义）
		if typeSet["serviceMonitors"] && len(backup.ServiceMonitors) > 0 {
			count, err := importServiceMonitors(tx, backup.ServiceMonitors, now, rep)
			if err != nil {
				return fmt.Errorf("import service monitors failed: %w", err)
			}
			result.ServiceMonitorsImported = count
		}
		// R4X-006：全部段落库后做一次授权一致性收口（与 R2X-014
		// speed 清扫同为事务尾落点）：段序 forwards 先于 userTunnels、
		// permissions 在 groups 之后，段内即时校验会误伤同批全量
		// 恢复，只有在事务尾以终态裁定才不误杀同批补写的授权。
		var authUserTunnels []model.UserTunnelBackup
		if typeSet["userTunnels"] {
			authUserTunnels = backup.UserTunnels
		}
		var authForwards []model.ForwardBackup
		if typeSet["forwards"] {
			authForwards = backup.Forwards
		}
		if len(authUserTunnels) > 0 || len(authForwards) > 0 {
			utSkipped, fwdSkipped, err := sweepImportedAuthorizationTx(tx, authUserTunnels, authForwards,
				preUserTunnelIDs, preForwardIDs, typeSet["userGroups"] && typeSet["tunnelGroups"], rep)
			if err != nil {
				return fmt.Errorf("import authorization sweep failed: %w", err)
			}
			result.UserTunnelsImported -= utSkipped
			result.ForwardsImported -= fwdSkipped
		}
		// R2X-014：全部段落库后做一次 speed 引用清扫（限速段在段序
		// 末尾，逐行校验会误杀全量恢复的合法绑定——见
		// sweepDanglingSpeedRefsTx 注释）。只清扫本轮勾选段的行。
		var sweepForwards []model.ForwardBackup
		if typeSet["forwards"] {
			sweepForwards = backup.Forwards
		}
		var sweepUserTunnels []model.UserTunnelBackup
		if typeSet["userTunnels"] {
			sweepUserTunnels = backup.UserTunnels
		}
		if len(sweepForwards) > 0 || len(sweepUserTunnels) > 0 {
			if err := sweepDanglingSpeedRefsTx(tx, sweepForwards, sweepUserTunnels, rep); err != nil {
				return fmt.Errorf("import speed ref sweep failed: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(rep.skipped) > 0 {
		result.SkippedRows = rep.skipped
	}
	result.ImportWarnings = rep.warnings
	result.RestoredSSHNodeIDs = rep.restoredSSHNodeIDs
	if len(rep.revoked) > 0 {
		result.RevokedUserTunnels = make([]model.RevokedImportPair, 0, len(rep.revoked))
		for _, p := range rep.revoked {
			result.RevokedUserTunnels = append(result.RevokedUserTunnels, model.RevokedImportPair{UserID: p.UserID, TunnelID: p.TunnelID})
		}
	}
	return result, nil
}

func importUsers(tx *gorm.DB, users []model.UserBackup, now int64) (int, error) {
	count := 0
	for _, u := range users {
		pwdHash, status, err := normalizeImportedUserPassword(u.Pwd, u.Status)
		if err != nil {
			return count, err
		}
		// O7-003：PasswordChangedAt 恢复策略
		// - legacy MD5/空密码被重置为随机密码时：用导入时间作为吊销时间戳（安全语义，见 TestImportUsersDoesNotStoreLegacyMD5Passwords）
		// - 正常 bcrypt 密码导入时：从备份恢复原始值（旧逻辑硬写 now 会丢失强制改密时间线）
		passwordChangedAt := u.PasswordChangedAt
		if security.IsLegacyPasswordHash(u.Pwd) || u.Pwd == "" {
			passwordChangedAt = now
		}
		item := model.User{
			ID:                u.ID,
			User:              u.User,
			Pwd:               pwdHash,
			RoleID:            u.RoleID,
			ExpTime:           u.ExpTime,
			Flow:              u.Flow,
			FlowMiB:           u.FlowMiB,
			InFlow:            u.InFlow,
			OutFlow:           u.OutFlow,
			FlowResetTime:     u.FlowResetTime,
			Num:               u.Num,
			CreatedTime:       u.CreatedTime,
			UpdatedTime:       sql.NullInt64{Int64: now, Valid: true},
			Status:            status,
			PasswordChangedAt: passwordChangedAt,
			MaxConn:           u.MaxConn,
		}
		// R5-B4C: 备份导入查重。重名时明确报错，不静默覆盖。
		if err := ensureUsernameAvailable(tx, u.User, u.ID); err != nil {
			return count, fmt.Errorf("import user %q: %w", u.User, err)
		}
		err = tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"user", "pwd", "role_id", "exp_time", "flow", "flow_mib", "in_flow", "out_flow",
				"flow_reset_time", "num", "updated_time", "status", "password_changed_at", "max_conn",
			}),
		}).Create(&item).Error
		if err != nil {
			// R5-B4C: 并发兜底，映射为友好错误。
			return count, mapUniqueViolation(err, "username")
		}
		if u.DailyQuotaGB > 0 || u.MonthlyQuotaGB > 0 || u.DisabledByQuota != 0 || u.QuotaDisabledAt > 0 ||
			u.PausedForwardIDs != "" || u.DailyUsedBytes != 0 || u.MonthlyUsedBytes != 0 {
			// D4-01：新备份带窗口键时，已用量与暂停清单原样恢复
			//（跨日恢复由配额视图的滚动归零兜底）；旧备份无这批
			// 字段，沿用旧口径（导入日窗口 + 已用量清零）。
			dayKey, monthKey := u.QuotaDayKey, u.QuotaMonthKey
			dailyUsed, monthlyUsed := u.DailyUsedBytes, u.MonthlyUsedBytes
			pausedForwardIDs := u.PausedForwardIDs
			if dayKey <= 0 || monthKey <= 0 {
				current := time.UnixMilli(now)
				dayKey = int64(current.Year()*10000 + int(current.Month())*100 + current.Day())
				monthKey = int64(current.Year()*100 + int(current.Month()))
				dailyUsed, monthlyUsed, pausedForwardIDs = 0, 0, ""
			}
			quotaItem := model.UserQuota{
				UserID:           u.ID,
				DailyLimitGB:     u.DailyQuotaGB,
				MonthlyLimitGB:   u.MonthlyQuotaGB,
				DailyUsedBytes:   dailyUsed,
				MonthlyUsedBytes: monthlyUsed,
				DayKey:           dayKey,
				MonthKey:         monthKey,
				DisabledByQuota:  u.DisabledByQuota,
				DisabledAt:       u.QuotaDisabledAt,
				PausedForwardIDs: pausedForwardIDs,
				CreatedTime:      now,
				UpdatedTime:      now,
			}
			err = tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "user_id"}},
				DoUpdates: clause.AssignmentColumns([]string{
					"daily_limit_gb", "monthly_limit_gb", "daily_used_bytes", "monthly_used_bytes",
					"day_key", "month_key", "disabled_by_quota", "disabled_at", "paused_forward_ids", "updated_time",
				}),
			}).Create(&quotaItem).Error
			if err != nil {
				return count, err
			}
		} else {
			// FIX2-B（D4-11）：处置选择「保行」。备份无任何配额信号
			// 只说明备份侧没有可恢复的配额数据（旧备份根本没有这批
			// 字段），不构成「该用户应无配额」的断言；旧实现据此删
			// 除目标库现存配额行，同实例重导入一份旧备份即静默剥
			// 掉用户的日/月限额。导入其余段均为 upsert 语义（备份
			// 里没有的行从不反删），此处与之对齐：无信号即不动。
		}
		count++
	}
	return count, nil
}

func normalizeImportedUserPassword(password string, status int) (string, int, error) {
	password = strings.TrimSpace(password)
	if strings.HasPrefix(password, "$2") {
		return password, status, nil
	}
	if security.IsLegacyPasswordHash(password) || password == "" {
		replacement, err := randomPasswordHash()
		if err != nil {
			return "", status, err
		}
		return replacement, 0, nil
	}
	hash, err := security.HashPassword(password)
	if err != nil {
		return "", status, err
	}
	return hash, status, nil
}

func randomPasswordHash() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return security.HashPassword(hex.EncodeToString(buf))
}

func importNodes(tx *gorm.DB, nodes []model.NodeBackup, now int64, rep *importReport) (int, error) {
	count := 0
	for _, n := range nodes {
		// D4-03：同 id 异源顶替不再静默——覆盖前比对既有行的身份
		// 字段（名称/密钥），不同则在回执中如实提示（备份为准的
		// 恢复语义不变，仅补披露，同订阅段 BUG-A2-4 的回执口径）。
		var existing model.Node
		existingRes := tx.Select("id", "name", "secret").Where("id = ?", n.ID).Limit(1).Find(&existing)
		if existingRes.Error != nil {
			return count, existingRes.Error
		}
		if existingRes.RowsAffected > 0 && (existing.Name != n.Name || existing.Secret != n.Secret) {
			rep.warnf("节点 #%d 已被备份覆盖：目标库原记录（名称 %q）与备份（名称 %q）的名称/密钥不同，已按备份为准恢复", n.ID, existing.Name, n.Name)
		}
		// R3-E-01：节点地址含结构字符（@、#、逗号、空白/控制字符等，
		// 与保存面 IsValidNodeAddress 同一字符集）时清空并披露——
		// 该值是订阅 auto 模式与中转入口的地址源，坏值恢复落库即
		// 改写订阅权威段。合法形态（主机名/IP 字面量/回环/
		// RFC1918）逐字恢复，备份面不比保存面更严。
		serverIP := n.ServerIP
		if kernel.HostHasStructuralChars(serverIP) {
			rep.warnf("备份恢复：节点地址（节点 #%d）含非法结构字符，已清空，恢复后请在节点设置中重新填写", n.ID)
			serverIP = ""
		}
		item := model.Node{
			ID:           n.ID,
			Name:         n.Name,
			Remark:       sql.NullString{String: n.Remark, Valid: n.Remark != ""},
			ExpiryTime:   sql.NullInt64{Int64: n.ExpiryTime, Valid: n.ExpiryTime > 0},
			RenewalCycle: sql.NullString{String: n.RenewalCycle, Valid: n.RenewalCycle != ""},
			Secret:       n.Secret,
			ServerIP:     serverIP,
			ServerIPV4:   sql.NullString{String: n.ServerIPv4, Valid: n.ServerIPv4 != ""},
			ServerIPV6:   sql.NullString{String: n.ServerIPv6, Valid: n.ServerIPv6 != ""},
			// B4/F-027：导入侧同样补 ExtraIPs（此前导出/导入双漏）。
			ExtraIPs:      sql.NullString{String: n.ExtraIPs, Valid: n.ExtraIPs != ""},
			Port:          n.Port,
			InterfaceName: sql.NullString{String: n.InterfaceName, Valid: n.InterfaceName != ""},
			Version:       sql.NullString{String: n.Version, Valid: n.Version != ""},
			HTTP:          n.HTTP,
			TLS:           n.TLS,
			Socks:         n.Socks,
			CreatedTime:   n.CreatedTime,
			UpdatedTime:   sql.NullInt64{Int64: now, Valid: true},
			Status:        n.Status,
			TCPListenAddr: n.TCPListenAddr,
			UDPListenAddr: n.UDPListenAddr,
			Inx:           n.Inx,
			IsRemote:      n.IsRemote,
			// 导出仅在 Valid 时写 remote* 三字段，'' 不是合法取值：
			// 空即 NULL（旧实现无条件 Valid:true 把 NULL 写成 ''，
			// 同 BUG-A2-3 的丢 NULL 类）。
			RemoteURL:      sql.NullString{String: n.RemoteURL, Valid: n.RemoteURL != ""},
			RemoteToken:    sql.NullString{String: n.RemoteToken, Valid: n.RemoteToken != ""},
			RemoteConfig:   sql.NullString{String: n.RemoteConfig, Valid: n.RemoteConfig != ""},
			LatencyTargets: n.LatencyTargets,
			ForwardMode:    n.ForwardMode,
			// O7-006：恢复后保留已忽略的到期提醒状态
			ExpiryReminderDismissed: n.ExpiryReminderDismissed,
		}
		err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"name", "remark", "expiry_time", "renewal_cycle", "secret", "server_ip", "server_ip_v4", "server_ip_v6", "extra_ips", "port", "interface_name", "version",
				"http", "tls", "socks", "updated_time", "status", "tcp_listen_addr", "udp_listen_addr",
				"inx", "is_remote", "remote_url", "remote_token", "remote_config", "latency_targets", "forward_mode", "expiry_reminder_dismissed",
			}),
		}).Create(&item).Error
		if err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

// importNodeSSHConfigs 恢复 node_ssh_config（A3-05）。以 node_id 为
// 业务键 upsert；节点不存在的行跳过并计数（D4-03）。每个恢复成功的
// 节点都登记进回执的「需重新下发」清单并附提示：SSH 配置回来了，
// 但数据面（nft 规则绑定/内核状态）不随备份，须经启动 reconcile
// 或下一次下发重新校验——不再静默降级。
func importNodeSSHConfigs(tx *gorm.DB, rows []model.NodeSSHConfigBackup, now int64, rep *importReport) (int, error) {
	count := 0
	for _, c := range rows {
		nodeExists, err := rowExistsTx(tx, &model.Node{}, c.NodeID)
		if err != nil {
			return count, err
		}
		if !nodeExists {
			rep.skip("nodeSSHConfigs")
			continue
		}
		item := model.NodeSSHConfig{
			NodeID:      c.NodeID,
			Host:        c.Host,
			Port:        c.Port,
			Username:    c.Username,
			AuthType:    c.AuthType,
			Password:    sql.NullString{String: c.Password, Valid: c.Password != ""},
			PrivateKey:  sql.NullString{String: c.PrivateKey, Valid: c.PrivateKey != ""},
			Passphrase:  sql.NullString{String: c.Passphrase, Valid: c.Passphrase != ""},
			SudoMode:    c.SudoMode,
			CreatedTime: c.CreatedTime,
			UpdatedTime: now,
		}
		if item.CreatedTime == 0 {
			item.CreatedTime = now
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "node_id"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"host", "port", "username", "auth_type", "password", "private_key",
				"passphrase", "sudo_mode", "updated_time",
			}),
		}).Create(&item).Error; err != nil {
			return count, err
		}
		rep.restoredSSHNodeIDs = append(rep.restoredSSHNodeIDs, c.NodeID)
		count++
	}
	if count > 0 {
		rep.warnf("已恢复 %d 个节点的 SSH 配置；这些节点的数据面（nftables 规则/计数基线）不随备份恢复，将在启动 reconcile 或下一次下发时重新校验（需重新下发），节点清单见 restoredSSHNodeIds", count)
	}
	return count, nil
}

// importPeerShares 恢复 peer_share 定义（A3-05）。token 是全局唯一
// 业务键：同 token 异 id 的既有行先删占位（与 user_subscriptions
// 导入的既有处理一致）；节点不存在的定义跳过并计数（D4-03）。
// runtime/resource 等运行时表不随备份，对端凭原 token 重新接入后
// 由运行时重建，回执如实提示。
func importPeerShares(tx *gorm.DB, rows []model.PeerShareBackup, now int64, rep *importReport) (int, error) {
	count := 0
	for _, s := range rows {
		nodeExists, err := rowExistsTx(tx, &model.Node{}, s.NodeID)
		if err != nil {
			return count, err
		}
		if !nodeExists {
			rep.skip("peerShares")
			continue
		}
		if s.Token != "" {
			// H-S01：删占位 share 时连带删其 runtime/resource 行
			// （与 DeletePeerShare 的级联口径对齐），不留 share_id
			// 悬空的运行时残留行。
			var occupyingShares []int64
			if err := tx.Model(&model.PeerShare{}).Where("token = ? AND id <> ?", s.Token, s.ID).Pluck("id", &occupyingShares).Error; err != nil {
				return count, err
			}
			if len(occupyingShares) > 0 {
				if err := tx.Where("share_id IN ?", occupyingShares).Delete(&model.PeerShareRuntime{}).Error; err != nil {
					return count, err
				}
				if err := tx.Where("share_id IN ?", occupyingShares).Delete(&model.PeerShareResource{}).Error; err != nil {
					return count, err
				}
			}
			if err := tx.Where("token = ? AND id <> ?", s.Token, s.ID).Delete(&model.PeerShare{}).Error; err != nil {
				return count, err
			}
		}
		updated := s.UpdatedTime
		if updated == 0 {
			updated = now
		}
		item := model.PeerShare{
			ID: s.ID, Name: s.Name, NodeID: s.NodeID, Token: s.Token,
			MaxBandwidth: s.MaxBandwidth, ExpiryTime: s.ExpiryTime,
			PortRangeStart: s.PortRangeStart, PortRangeEnd: s.PortRangeEnd,
			CurrentFlow: s.CurrentFlow, IsActive: s.IsActive,
			CreatedTime: s.CreatedTime, UpdatedTime: updated,
			AllowedDomains: s.AllowedDomains, AllowedIPs: s.AllowedIPs,
			RelayUsername: s.RelayUsername, RelayPassword: s.RelayPassword,
		}
		if item.CreatedTime == 0 {
			item.CreatedTime = now
		}
		// FIX2-B（V3-01）：凭据列仅在备份确实携带时才进 DoUpdates；
		// 旧备份无此二字段（恒空），无条件更新会把同实例现存凭据
		// 清零（与 D4-11「无信号即不动」同口径）。全新恢复走插入
		// 分支，凭据随 item 落库，不受此限。
		peerShareDoUpdates := []string{
			"name", "node_id", "token", "max_bandwidth", "expiry_time",
			"port_range_start", "port_range_end", "current_flow", "is_active",
			"updated_time", "allowed_domains", "allowed_ips",
		}
		if s.RelayUsername != "" || s.RelayPassword != "" {
			peerShareDoUpdates = append(peerShareDoUpdates, "relay_username", "relay_password")
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns(peerShareDoUpdates),
		}).Create(&item).Error; err != nil {
			return count, err
		}
		// FIX2-B（V4-N2）：is_active 带 default:1，新插入分支会吞掉
		// 停用共享的显式 0 并静默重新激活（原 token 立即可用）——
		// 与 kernel/inbound 等恢复路径同口径补写回。意图必须取备份
		// 行 s（Create 后 GORM 会把默认值回填进 item，不可读）。
		if err := restoreZeroValuedInt(tx, &model.PeerShare{}, item.ID, "is_active", s.IsActive); err != nil {
			return count, err
		}
		count++
	}
	if count > 0 {
		rep.warnf("已恢复 %d 条面板共享（联邦）定义；运行时状态未随备份恢复，对端凭原 token 重新接入后自动重建", count)
	}
	return count, nil
}

// normalizeBackupTunnelEngine mirrors handler.normalizeTunnelEngine (the repo
// layer cannot import the handler): "" or "gost" select the agent's
// forwarder, "realm" selects realm; anything else is an error, never
// silently dropped.
func normalizeBackupTunnelEngine(raw string) (string, error) {
	switch e := strings.ToLower(strings.TrimSpace(raw)); e {
	case "", "gost":
		return "", nil
	case "realm":
		return e, nil
	default:
		return "", fmt.Errorf("不支持的转发引擎 %s", raw)
	}
}

func importTunnels(tx *gorm.DB, tunnels []model.TunnelBackup, now int64, rep *importReport) (int, error) {
	// BUG-A2-3：chain 行先删后插。本批隧道在当前 DB 中的既有 chain 行
	// 整体删除后再按备份重建——既避免恢复到已漂移状态时残留行与新行
	// 在 (node_id, port) 唯一索引上互撞，也保证恢复结果与备份严格
	// 一致（被删掉的链路行不会在恢复后残留）。
	if len(tunnels) > 0 {
		tunnelIDs := make([]int64, 0, len(tunnels))
		for _, t := range tunnels {
			tunnelIDs = append(tunnelIDs, t.ID)
		}
		if err := tx.Where("tunnel_id IN ?", tunnelIDs).Delete(&model.ChainTunnel{}).Error; err != nil {
			return 0, err
		}
	}
	count := 0
	for _, t := range tunnels {
		engine, err := normalizeBackupTunnelEngine(t.Engine)
		if err != nil {
			return count, err
		}
		// R2X-016：TrafficRatio 意图值在 Create 前捕获（Create 后
		// GORM 会把 default:1.0 回填进 item，读 item 判不出意图）。
		ratioIntent := t.TrafficRatio
		item := model.Tunnel{
			ID:           t.ID,
			Name:         t.Name,
			TrafficRatio: t.TrafficRatio,
			Type:         t.Type,
			Protocol:     t.Protocol,
			Flow:         t.Flow,
			CreatedTime:  t.CreatedTime,
			UpdatedTime:  now,
			Status:       t.Status,
			// 导出仅在 Valid 时写 inIp，'' 不是合法 IP：空即 NULL，
			// 与 ConnectIP 的既有口径一致（旧实现无条件 Valid:true
			// 把 NULL 写成 ''，同 BUG-A2-3 的丢 NULL 类）。
			InIP:            sql.NullString{String: t.InIP, Valid: t.InIP != ""},
			Inx:             t.Inx,
			IPPreference:    t.IPPreference,
			ProbeTargetHost: t.ProbeTargetHost,
			ProbeTargetPort: t.ProbeTargetPort,
			Engine:          engine,
		}
		err = tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"name", "traffic_ratio", "type", "protocol", "flow", "updated_time", "status", "in_ip", "inx", "ip_preference", "probe_target_host", "probe_target_port", "engine",
			}),
		}).Create(&item).Error
		if err != nil {
			return count, err
		}
		// R2X-016：default:1.0 吞掉的显式 0 倍率写回（插入/更新两
		// 分支同病，见 restoreZeroValuedFloat）。
		if err := restoreZeroValuedFloat(tx, &model.Tunnel{}, item.ID, "traffic_ratio", ratioIntent); err != nil {
			return count, err
		}
		for _, ct := range t.ChainTunnels {
			// D4-03：链路引用的节点不存在时跳过该行并计数，不写悬空行。
			nodeExists, err := rowExistsTx(tx, &model.Node{}, ct.NodeID)
			if err != nil {
				return count, err
			}
			if !nodeExists {
				rep.skip("chainTunnels")
				continue
			}
			// BUG-A2-3：可空列按指针还原 NULL（旧实现一律 Valid:true，
			// NULL port 被写成 0，两条同节点 NULL 链路即撞唯一索引
			// 整单回滚）。chain 行已在上方整批删除，这里直接插入。
			chainItem := model.ChainTunnel{
				ID:        ct.ID,
				TunnelID:  ct.TunnelID,
				ChainType: ct.ChainType,
				NodeID:    ct.NodeID,
				Port:      ptrToNullInt64(ct.Port),
				Strategy:  ptrToNullString(ct.Strategy),
				Inx:       ptrToNullInt64(ct.Inx),
				Protocol:  ptrToNullString(ct.Protocol),
				ConnectIP: ptrToNullString(ct.ConnectIP),
			}
			if err := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "id"}},
				DoUpdates: clause.AssignmentColumns([]string{
					"tunnel_id", "chain_type", "node_id", "port", "strategy", "inx", "protocol", "connect_ip",
				}),
			}).Create(&chainItem).Error; err != nil {
				return count, err
			}
		}
		count++
	}
	return count, nil
}

func nullableBackupInt64(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

// restoreZeroValuedInt undoes GORM's default-tag swallowing after an
// OnConflict Create during backup import: an int column tagged
// `default:1` (Enabled / Managed) whose backup intent is 0 is dropped
// from the INSERT, and the conflict-update assignments reuse the insert
// values, so the row lands as 1 on BOTH branches (disabled inbounds,
// kernels, announcements and monitors came back enabled; unmanaged
// relays came back managed — W10 follow-up probe, both branches
// reproduced). Reading the struct after Create cannot detect it —
// GORM backfills the default into it — so the intent is captured by
// the caller beforehand and written back explicitly here.
// Same root as BUG-A4-1.
func restoreZeroValuedInt(tx *gorm.DB, model interface{}, id int64, column string, intent int) error {
	if intent != 0 {
		return nil
	}
	return tx.Model(model).Where("id = ?", id).Update(column, 0).Error
}

// restoreZeroValuedFloat is the float twin of restoreZeroValuedInt
// (R2X-016): Tunnel.TrafficRatio is the one float column with a
// non-zero default (`default:1.0`), and 0 is a legal business value
// (free tunnel, zero billing — see nftables_traffic.go). Without this
// write-back a restored ratio=0 tunnel silently came back at 1.0 on
// both the insert and the conflict-update branch.
func restoreZeroValuedFloat(tx *gorm.DB, model interface{}, id int64, column string, intent float64) error {
	if intent != 0 {
		return nil
	}
	return tx.Model(model).Where("id = ?", id).Update(column, 0.0).Error
}

// userTunnelCoveredByDisabledPairTx 判断 (userID, tunnelID) 是否被
// 一个现行停用分组权限对覆盖：存在 group_permission 对，其
// user_group 含该 user、tunnel_group 含该 tunnel，且任一侧组
// status≠1。停用撤销的台账痕迹只剩成员/权限对/组状态三者（grant
// 与 UT 已被撤销清零），这是事务尾唯一可用的现行撤销状态判据。
func userTunnelCoveredByDisabledPairTx(tx *gorm.DB, userID, tunnelID int64) (bool, error) {
	var n int64
	err := tx.Model(&model.GroupPermission{}).
		Joins("JOIN user_group ON user_group.id = group_permission.user_group_id").
		Joins("JOIN tunnel_group ON tunnel_group.id = group_permission.tunnel_group_id").
		Joins("JOIN user_group_user ON user_group_user.user_group_id = group_permission.user_group_id AND user_group_user.user_id = ?", userID).
		Joins("JOIN tunnel_group_tunnel ON tunnel_group_tunnel.tunnel_group_id = group_permission.tunnel_group_id AND tunnel_group_tunnel.tunnel_id = ?", tunnelID).
		Where("user_group.status <> 1 OR tunnel_group.status <> 1").
		Count(&n).Error
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// sweepImportedAuthorizationTx 是 R4X-006 的导入段授权收口，落点为
// repo 事务尾（verdicts §5 写死：段序 forwards 先于 userTunnels，
// 段内即时校验会误伤同批全量恢复）。以全部段落库后的终态裁定：
//
//   - userTunnels：本次新落的 UT 若无任何 grant 台账、且被目标库
//     现行停用权限对覆盖，则它复活的是已被撤销的组授权（无台账、
//     后续任何生命周期触点都不可见），删除该行并计 skipped +
//     ImportWarnings。有 grant（存量或同批 permissions 段补写）
//     的行、未被停用对覆盖的手工行、存量行（导入前已存在、本次
//     仅刷新）均不动。例外：userGroups 与 tunnelGroups 两段同批
//     导入时，权限对的组状态/成员本身来自同一备份（全量恢复语
//     义），终态以备份为准，不按目标库口径裁定。
//   - forwards：本次新落的转发若在终态下找不到对应 (user,tunnel)
//     的 user_tunnel 授权行（运行时 forwardCreate 本就被
//     ensureTunnelPermission 门控，无授权转发不可用），删除该行
//     （含其端口/中转附属行，与 DeleteForwardCascade 同口径）并
//     计 skipped + ImportWarnings。同批 userTunnels 段后落的授权
//     在事务尾已可见，段序不误伤。
//
// 返回被裁定跳过的行数，供 Import 回填 imported 计数。
func sweepImportedAuthorizationTx(tx *gorm.DB, userTunnels []model.UserTunnelBackup, forwards []model.ForwardBackup, preUserTunnelIDs, preForwardIDs map[int64]bool, groupStateFromBackup bool, rep *importReport) (utSkipped, fwdSkipped int, err error) {
	for _, ub := range userTunnels {
		if preUserTunnelIDs[ub.ID] {
			continue
		}
		var row model.UserTunnel
		res := tx.Where("id = ?", ub.ID).Limit(1).Find(&row)
		if res.Error != nil {
			return utSkipped, fwdSkipped, res.Error
		}
		if res.RowsAffected == 0 {
			continue // 身份引用校验已跳过，未落行
		}
		var grants int64
		if err := tx.Model(&model.GroupPermissionGrant{}).Where("user_tunnel_id = ?", row.ID).Count(&grants).Error; err != nil {
			return utSkipped, fwdSkipped, err
		}
		if grants > 0 || groupStateFromBackup {
			continue
		}
		covered, err := userTunnelCoveredByDisabledPairTx(tx, row.UserID, row.TunnelID)
		if err != nil {
			return utSkipped, fwdSkipped, err
		}
		if !covered {
			continue
		}
		if err := tx.Where("id = ?", row.ID).Delete(&model.UserTunnel{}).Error; err != nil {
			return utSkipped, fwdSkipped, err
		}
		rep.skip("userTunnelsRevoked")
		utSkipped++
	}
	if utSkipped > 0 {
		rep.warnf("备份恢复：%d 条用户隧道授权与目标库现行停用分组的撤销状态冲突（无授权台账、属已撤销的组授权），已按现行授权状态跳过未恢复", utSkipped)
	}
	for _, fb := range forwards {
		if preForwardIDs[fb.ID] {
			continue
		}
		var row model.Forward
		res := tx.Where("id = ?", fb.ID).Limit(1).Find(&row)
		if res.Error != nil {
			return utSkipped, fwdSkipped, res.Error
		}
		if res.RowsAffected == 0 {
			continue
		}
		var uts int64
		if err := tx.Model(&model.UserTunnel{}).Where("user_id = ? AND tunnel_id = ?", row.UserID, row.TunnelID).Count(&uts).Error; err != nil {
			return utSkipped, fwdSkipped, err
		}
		if uts > 0 {
			continue
		}
		// 与 DeleteForwardCascade 同口径清理附属行（刚落库的转发
		// 通常只有 ForwardPort，其余为存量同 id 残留的兜底）。
		if err := tx.Where("forward_id = ?", row.ID).Delete(&model.NftCounterState{}).Error; err != nil {
			return utSkipped, fwdSkipped, err
		}
		if err := tx.Where("forward_id = ?", row.ID).Delete(&model.NftRuleBinding{}).Error; err != nil {
			return utSkipped, fwdSkipped, err
		}
		if err := tx.Where("forward_id = ?", row.ID).Delete(&model.NftPendingDelete{}).Error; err != nil {
			return utSkipped, fwdSkipped, err
		}
		if err := tx.Where("forward_id = ?", row.ID).Delete(&model.ForwardPort{}).Error; err != nil {
			return utSkipped, fwdSkipped, err
		}
		relayIDs, err := pluckInboundRelayIDsTx(tx, "forward_id = ?", row.ID)
		if err != nil {
			return utSkipped, fwdSkipped, err
		}
		if err := deleteUserInboundGrantsByRelayIDsTx(tx, relayIDs); err != nil {
			return utSkipped, fwdSkipped, err
		}
		if err := tx.Where("forward_id = ?", row.ID).Delete(&model.NodeInboundRelay{}).Error; err != nil {
			return utSkipped, fwdSkipped, err
		}
		if err := tx.Where("id = ?", row.ID).Delete(&model.Forward{}).Error; err != nil {
			return utSkipped, fwdSkipped, err
		}
		rep.skip("forwardsUnauthorized")
		fwdSkipped++
	}
	if fwdSkipped > 0 {
		rep.warnf("备份恢复：%d 条转发在目标库中无对应用户隧道授权，已跳过未恢复", fwdSkipped)
	}
	return utSkipped, fwdSkipped, nil
}

// sweepDanglingSpeedRefsTx 清扫本轮导入 forwards/userTunnels 的悬空
// 限速引用（R2X-014）。必须在 Import 全部段落库之后执行：限速段在
// 段序中位于 forwards/userTunnels 之后，逐行校验会在全量恢复时误杀
// 合法绑定（H2-01 的段序陷阱）；后置清扫天然免疫段序。附属引用不
// 存在时按 verdicts §2.7 剥离（置 NULL）+ 计 ImportWarnings，不跳
// 整行——部分恢复（未勾 speedLimits）时整行跳过会连带丢转发。
// 只处理备份清单内的行 id；被身份引用校验跳过的行在库中不存在，
// Find 不命中自动略过，存量无关行不受影响。
func sweepDanglingSpeedRefsTx(tx *gorm.DB, forwards []model.ForwardBackup, userTunnels []model.UserTunnelBackup, rep *importReport) error {
	fwdSpeed, fwdIPSpeed, utSpeed := 0, 0, 0
	for _, f := range forwards {
		var row model.Forward
		res := tx.Where("id = ?", f.ID).Limit(1).Find(&row)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			continue
		}
		if row.SpeedID.Valid {
			exists, err := rowExistsTx(tx, &model.SpeedLimit{}, row.SpeedID.Int64)
			if err != nil {
				return err
			}
			if !exists {
				if err := tx.Model(&model.Forward{}).Where("id = ?", f.ID).Update("speed_id", nil).Error; err != nil {
					return err
				}
				fwdSpeed++
			}
		}
		if row.IPSpeedID.Valid {
			exists, err := rowExistsTx(tx, &model.SpeedLimit{}, row.IPSpeedID.Int64)
			if err != nil {
				return err
			}
			if !exists {
				if err := tx.Model(&model.Forward{}).Where("id = ?", f.ID).Update("ip_speed_id", nil).Error; err != nil {
					return err
				}
				fwdIPSpeed++
			}
		}
	}
	for _, ut := range userTunnels {
		var row model.UserTunnel
		res := tx.Where("id = ?", ut.ID).Limit(1).Find(&row)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			continue
		}
		if row.SpeedID.Valid {
			exists, err := rowExistsTx(tx, &model.SpeedLimit{}, row.SpeedID.Int64)
			if err != nil {
				return err
			}
			if !exists {
				if err := tx.Model(&model.UserTunnel{}).Where("id = ?", ut.ID).Update("speed_id", nil).Error; err != nil {
					return err
				}
				utSpeed++
			}
		}
	}
	if total := fwdSpeed + fwdIPSpeed + utSpeed; total > 0 {
		rep.warnf("备份恢复：%d 处限速引用指向不存在的限速规则，已剥离（置空）（转发 speed_id %d 处、转发 ip_speed_id %d 处、用户隧道 speed_id %d 处），转发/用户隧道行本身已照常恢复", total, fwdSpeed, fwdIPSpeed, utSpeed)
	}
	return nil
}

func importForwards(tx *gorm.DB, forwards []model.ForwardBackup, now int64, rep *importReport) (int, error) {
	count := 0
	for _, f := range forwards {
		// D4-03：引用（用户/隧道）不存在时整行跳过并计数，不写悬空行。
		userExists, err := rowExistsTx(tx, &model.User{}, f.UserID)
		if err != nil {
			return count, err
		}
		tunnelExists, err := rowExistsTx(tx, &model.Tunnel{}, f.TunnelID)
		if err != nil {
			return count, err
		}
		if !userExists || !tunnelExists {
			rep.skip("forwards")
			continue
		}
		item := model.Forward{
			ID:                   f.ID,
			UserID:               f.UserID,
			UserName:             f.UserName,
			Name:                 f.Name,
			TunnelID:             f.TunnelID,
			RemoteAddr:           f.RemoteAddr,
			Strategy:             f.Strategy,
			InFlow:               f.InFlow,
			OutFlow:              f.OutFlow,
			CreatedTime:          f.CreatedTime,
			UpdatedTime:          now,
			Status:               f.Status,
			Inx:                  f.Inx,
			SpeedID:              sql.NullInt64{Int64: nullableBackupInt64(f.SpeedID), Valid: f.SpeedID != nil && *f.SpeedID > 0},
			MaxConn:              f.MaxConn,
			IPMaxConn:            f.IPMaxConn,
			IPSpeedID:            sql.NullInt64{Int64: nullableBackupInt64(f.IPSpeedID), Valid: f.IPSpeedID != nil && *f.IPSpeedID > 0},
			ProxyProtocol:        f.ProxyProtocol,
			ProxyProtocolReceive: f.ProxyProtocolReceive,
			ProxyProtocolSend:    f.ProxyProtocolSend,
		}
		err = tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"user_id", "user_name", "name", "tunnel_id", "remote_addr", "strategy",
				"in_flow", "out_flow", "updated_time", "status", "inx", "speed_id", "max_conn", "ip_max_conn", "ip_speed_id", "proxy_protocol", "proxy_protocol_receive", "proxy_protocol_send",
			}),
		}).Create(&item).Error
		if err != nil {
			return count, err
		}
		if f.ForwardPorts != nil {
			if err := tx.Where("forward_id = ?", f.ID).Delete(&model.ForwardPort{}).Error; err != nil {
				return count, err
			}
			for _, fp := range *f.ForwardPorts {
				// D4-03：端口引用的节点不存在时跳过该端口行并计数。
				portNodeExists, err := rowExistsTx(tx, &model.Node{}, fp.NodeID)
				if err != nil {
					return count, err
				}
				if !portNodeExists {
					rep.skip("forwardPorts")
					continue
				}
				// O7-005：恢复 ForwardPort 的 InIP（sql.NullString）
				if err := tx.Create(&model.ForwardPort{
					ForwardID: f.ID, NodeID: fp.NodeID, Port: fp.Port,
					InIP: sql.NullString{String: fp.InIP, Valid: fp.InIP != ""},
				}).Error; err != nil {
					return count, err
				}
			}
		}
		count++
	}
	return count, nil
}

func importUserTunnels(tx *gorm.DB, userTunnels []model.UserTunnelBackup, _ int64, rep *importReport) (int, error) {
	count := 0
	for _, ut := range userTunnels {
		// D4-03：引用（用户/隧道）不存在时整行跳过并计数。
		userExists, err := rowExistsTx(tx, &model.User{}, ut.UserID)
		if err != nil {
			return count, err
		}
		tunnelExists, err := rowExistsTx(tx, &model.Tunnel{}, ut.TunnelID)
		if err != nil {
			return count, err
		}
		if !userExists || !tunnelExists {
			rep.skip("userTunnels")
			continue
		}
		item := model.UserTunnel{
			ID:            ut.ID,
			UserID:        ut.UserID,
			TunnelID:      ut.TunnelID,
			SpeedID:       sql.NullInt64{Int64: ut.SpeedID, Valid: ut.SpeedID > 0},
			Num:           ut.Num,
			Flow:          ut.Flow,
			FlowMiB:       ut.FlowMiB,
			InFlow:        ut.InFlow,
			OutFlow:       ut.OutFlow,
			FlowResetTime: ut.FlowResetTime,
			ExpTime:       ut.ExpTime,
			Status:        ut.Status,
		}
		err = tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"user_id", "tunnel_id", "speed_id", "num", "flow", "flow_mib", "in_flow", "out_flow",
				"flow_reset_time", "exp_time", "status",
			}),
		}).Create(&item).Error
		if err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func importSpeedLimits(tx *gorm.DB, speedLimits []model.SpeedLimitBackup, now int64, rep *importReport) (int, error) {
	count := 0
	for _, sl := range speedLimits {
		// D4-03：绑定了隧道而隧道不存在时整行跳过并计数（未绑定
		// 的全局限速不受影响）。
		if sl.TunnelID != nil {
			tunnelExists, err := rowExistsTx(tx, &model.Tunnel{}, *sl.TunnelID)
			if err != nil {
				return count, err
			}
			if !tunnelExists {
				rep.skip("speedLimits")
				continue
			}
		}
		item := model.SpeedLimit{
			ID:          sl.ID,
			Name:        sl.Name,
			Speed:       int(sl.Speed),
			TunnelID:    sql.NullInt64{Int64: nullableBackupInt64(sl.TunnelID), Valid: sl.TunnelID != nil},
			TunnelName:  sql.NullString{String: sl.TunnelName, Valid: sl.TunnelName != ""},
			CreatedTime: sl.CreatedTime,
			UpdatedTime: sql.NullInt64{Int64: now, Valid: true},
			Status:      sl.Status,
		}
		err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"name", "speed", "tunnel_id", "tunnel_name", "updated_time", "status",
			}),
		}).Create(&item).Error
		if err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

// O7-001：导入 user_inbound 表（用户订阅分配关系）
//
// FIX132-C（D4-03）：引用存在性校验对齐订阅段标杆——用户/入站
// 不存在，或 relay 不存在/不属于该入站时，整行跳过并计入
// SkippedRows["userInbounds"]，不写悬空行（旧实现原样落库且回执
// 假报成功，D4 探针 C 坐实）。D4-02：(user_id, inbound_id, relay_id)
// 复合唯一占位预处理，同键异 id 先删。
func importUserInbounds(tx *gorm.DB, rows []model.UserInboundBackup, rep *importReport) (int, error) {
	count := 0
	for _, r := range rows {
		userExists, err := rowExistsTx(tx, &model.User{}, r.UserID)
		if err != nil {
			return count, err
		}
		inboundExists, err := rowExistsTx(tx, &model.NodeInbound{}, r.InboundID)
		if err != nil {
			return count, err
		}
		relayOK := true
		if r.RelayID > 0 {
			var relay model.NodeInboundRelay
			res := tx.Where("id = ?", r.RelayID).Limit(1).Find(&relay)
			if res.Error != nil {
				return count, res.Error
			}
			relayOK = res.RowsAffected > 0 && relay.InboundID == r.InboundID
		}
		if !userExists || !inboundExists || !relayOK {
			rep.skip("userInbounds")
			continue
		}
		if err := tx.Where("user_id = ? AND inbound_id = ? AND relay_id = ? AND id <> ?", r.UserID, r.InboundID, r.RelayID, r.ID).
			Delete(&model.UserInbound{}).Error; err != nil {
			return count, err
		}
		item := model.UserInbound{
			ID:        r.ID,
			UserID:    r.UserID,
			InboundID: r.InboundID,
			RelayID:   r.RelayID,
		}
		err = tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"user_id", "inbound_id", "relay_id",
			}),
		}).Create(&item).Error
		if err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

// B4/F-004：导入 user_subscription 表（订阅分配关系）
//
// BUG-A2-4：备份里的 subscription_id 是备份侧的订阅 id。订阅在
// inbounds 段按 token 匹配重建，被删过的订阅会换新 id——分配必须
// 先经 subRemap 重写再落库，否则全部悬空且回执假报成功。重写后仍
// 不存在的订阅（备份不含该订阅、当前库也没有）无法落位：跳过该行
// 并计入 skipped 由回执如实报告，不写悬空行、不报假成功。
func importUserSubscriptions(tx *gorm.DB, rows []model.UserSubscriptionBackup, subRemap map[int64]int64) (imported int, skipped int, err error) {
	for _, r := range rows {
		subID := r.SubscriptionID
		if mapped, ok := subRemap[subID]; ok {
			subID = mapped
		}
		// R1 X-P2-7（H1-01，BC-08）：此前只校验订阅侧，用户不存在
		// 时悬空分配照样落库且回执假报 imported。落库前补 user
		// 存在性校验（与订阅侧同口径）：不存在 → skipped++ 不落库。
		userExists, err := rowExistsTx(tx, &model.User{}, r.UserID)
		if err != nil {
			return imported, skipped, err
		}
		if !userExists {
			skipped++
			continue
		}
		var exists int64
		if err := tx.Model(&model.Subscription{}).Where("id = ?", subID).Count(&exists).Error; err != nil {
			return imported, skipped, err
		}
		if exists == 0 {
			skipped++
			continue
		}
		// (user_id, subscription_id) 唯一对若被另一行占用（例如恢复
		// 期间管理员已把同一订阅手工分配给同一用户），先删占位行，
		// 以备份为准——与 kernels/relays 导入的既有处理一致。
		if err := tx.Where("user_id = ? AND subscription_id = ? AND id <> ?", r.UserID, subID, r.ID).
			Delete(&model.UserSubscription{}).Error; err != nil {
			return imported, skipped, err
		}
		item := model.UserSubscription{
			ID:             r.ID,
			UserID:         r.UserID,
			SubscriptionID: subID,
			CreatedTime:    r.CreatedTime,
		}
		err = tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"user_id", "subscription_id", "created_time",
			}),
		}).Create(&item).Error
		if err != nil {
			return imported, skipped, err
		}
		imported++
	}
	return imported, skipped, nil
}

// B4/F-030：导入 service_monitor 表（监控定义）
func importServiceMonitors(tx *gorm.DB, rows []model.ServiceMonitorBackup, now int64, rep *importReport) (int, error) {
	count := 0
	for _, r := range rows {
		// D4-03：绑定了节点而节点不存在时整行跳过并计数
		//（NodeID=0 的面板本地监控不受影响）。
		if r.NodeID > 0 {
			nodeExists, err := rowExistsTx(tx, &model.Node{}, r.NodeID)
			if err != nil {
				return count, err
			}
			if !nodeExists {
				rep.skip("serviceMonitors")
				continue
			}
		}
		updated := r.UpdatedTime
		if updated == 0 {
			updated = now
		}
		item := model.ServiceMonitor{
			ID:          r.ID,
			Name:        r.Name,
			Type:        r.Type,
			Target:      r.Target,
			IntervalSec: r.IntervalSec,
			TimeoutSec:  r.TimeoutSec,
			NodeID:      r.NodeID,
			Enabled:     r.Enabled,
			CreatedTime: r.CreatedTime,
			UpdatedTime: updated,
		}
		err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"name", "type", "target", "interval_sec", "timeout_sec",
				"node_id", "enabled", "created_time", "updated_time",
			}),
		}).Create(&item).Error
		if err != nil {
			return count, err
		}
		if err := restoreZeroValuedInt(tx, &model.ServiceMonitor{}, r.ID, "enabled", r.Enabled); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

// O7-007：导入 announcement 表（公告）
func importAnnouncements(tx *gorm.DB, rows []model.AnnouncementBackup, now int64) (int, error) {
	count := 0
	for _, a := range rows {
		item := model.Announcement{
			ID:          a.ID,
			Content:     a.Content,
			Enabled:     a.Enabled,
			CreatedTime: a.CreatedTime,
			UpdatedTime: sql.NullInt64{Int64: now, Valid: true},
		}
		err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"content", "enabled", "updated_time",
			}),
		}).Create(&item).Error
		if err != nil {
			return count, err
		}
		if err := restoreZeroValuedInt(tx, &model.Announcement{}, a.ID, "enabled", a.Enabled); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

// O7-008：导入 monitor_permission 表（监控权限）
//
// FIX132-C（D4-02）：user_id 全局唯一——同用户异 id 的既有行先删
// 占位，以备份为准（旧实现只有 OnConflict(id)，业务键冲突即整单
// 回滚，D4 探针 B 坐实）。D4-03：用户不存在时跳过并计数。
func importMonitorPermissions(tx *gorm.DB, rows []model.MonitorPermissionBackup, rep *importReport) (int, error) {
	count := 0
	for _, mp := range rows {
		userExists, err := rowExistsTx(tx, &model.User{}, mp.UserID)
		if err != nil {
			return count, err
		}
		if !userExists {
			rep.skip("monitorPermissions")
			continue
		}
		if err := tx.Where("user_id = ? AND id <> ?", mp.UserID, mp.ID).Delete(&model.MonitorPermission{}).Error; err != nil {
			return count, err
		}
		item := model.MonitorPermission{
			ID:          mp.ID,
			UserID:      mp.UserID,
			CreatedTime: mp.CreatedTime,
		}
		err = tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"user_id", "created_time",
			}),
		}).Create(&item).Error
		if err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func importTunnelGroups(r *Repository, tx *gorm.DB, tunnelGroups []model.TunnelGroupBackup, now int64, rep *importReport) (int, error) {
	count := 0
	for _, tg := range tunnelGroups {
		// D4-02：组名唯一——同名异 id 的既有组先删占位（含其成员），
		// 以备份为准（与 kernels/relays/user_subscriptions 既有处理一致）。
		if err := deleteSameNameGroupTx(tx, &model.TunnelGroup{}, "tunnel_group_id", tg.Name, tg.ID, rep); err != nil {
			return count, err
		}
		// R3X-005 facet (a)：成员替换前先读旧成员并按台账撤销被
		// 移出隧道的组建授权（与运行时 groupTunnelAssign 差集撤销
		// 同口径，复用 RevokeGroupGrantsForRemovedTunnelsTx 的候选
		// 收集）；被撤销 UT 经 rep 回传 handler 清转发。
		var prevTunnelIDs []int64
		if err := tx.Model(&model.TunnelGroupTunnel{}).Where("tunnel_group_id = ?", tg.ID).Pluck("tunnel_id", &prevTunnelIDs).Error; err != nil {
			return count, err
		}
		revoked, err := r.RevokeGroupGrantsForRemovedTunnelsTx(tx, tg.ID, prevTunnelIDs, tg.Tunnels)
		if err != nil {
			return count, err
		}
		rep.addRevoked(revoked)
		if len(revoked) > 0 {
			rep.warnf("备份恢复：隧道分组 %q 成员替换已撤销 %d 条被移出隧道的组建授权（对应转发将按导入回执逐对清理）", tg.Name, len(revoked))
		}
		item := model.TunnelGroup{
			ID:          tg.ID,
			Name:        tg.Name,
			CreatedTime: tg.CreatedTime,
			UpdatedTime: now,
			Status:      tg.Status,
		}
		err = tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns([]string{"name", "updated_time", "status"}),
		}).Create(&item).Error
		if err != nil {
			return count, err
		}
		if err := tx.Where("tunnel_group_id = ?", tg.ID).Delete(&model.TunnelGroupTunnel{}).Error; err != nil {
			return count, err
		}
		for _, tunnelID := range tg.Tunnels {
			// D4-03：成员引用的隧道不存在时跳过该成员并计数。
			tunnelExists, err := rowExistsTx(tx, &model.Tunnel{}, tunnelID)
			if err != nil {
				return count, err
			}
			if !tunnelExists {
				rep.skip("tunnelGroupMembers")
				continue
			}
			if err := tx.Create(&model.TunnelGroupTunnel{TunnelGroupID: tg.ID, TunnelID: tunnelID, CreatedTime: now}).Error; err != nil {
				return count, err
			}
		}
		// R3X-005 facet (c)：导入落 status≠1 的组不保留其 grant——
		// 与运行时停用撤销同口径统一收敛（成员替换撤销之后执行，
		// 覆盖未被成员差集触及的剩余组建授权；手工 UT 不动）。
		if tg.Status != 1 {
			converged, err := r.RevokeGroupGrantsByGroupTx(tx, "tunnel_group", tg.ID)
			if err != nil {
				return count, err
			}
			rep.addRevoked(converged)
			if len(converged) > 0 {
				rep.warnf("备份恢复：隧道分组 %q 恢复为停用状态，已按停用口径收敛撤销其 %d 条组建授权", tg.Name, len(converged))
			}
		}
		count++
	}
	return count, nil
}

// deleteSameNameGroupTx 删除与待恢复分组同名、异 id 的既有分组行
// （分组名全局唯一，占位行不删则 upsert 撞唯一约束整单回滚——
// D4-02）。成员行按组 id 一并删除（成员随后按备份重建）。
// groupModel 为 TunnelGroup/UserGroup，memberGroupColumn 为成员表
// 的组外键列名（与 group_permission / group_permission_grant 的
// 组键列同名）。
//
// R3X-005 facet (b)（verdicts §5 裁定，从 H 案、不走删台账路线）：
// 删占位组前先把 permission/grant 中引用占位组 id 的组键重指向
// 同名继任组 keepID（同名即同一业务组的继任者，授权随组延续），
// 计数入 ImportWarnings；继任组已有同键行时合并去重（删占位侧
// 行），避免撞复合唯一约束整单回滚。
func deleteSameNameGroupTx(tx *gorm.DB, groupModel interface{}, memberGroupColumn, name string, keepID int64, rep *importReport) error {
	var occupying []int64
	if err := tx.Model(groupModel).Where("name = ? AND id <> ?", name, keepID).Pluck("id", &occupying).Error; err != nil {
		return err
	}
	if len(occupying) == 0 {
		return nil
	}
	var memberModel interface{}
	switch groupModel.(type) {
	case *model.TunnelGroup:
		memberModel = &model.TunnelGroupTunnel{}
	case *model.UserGroup:
		memberModel = &model.UserGroupUser{}
	default:
		return nil
	}
	groupCol := memberGroupColumn
	otherCol := "tunnel_group_id"
	if groupCol == "tunnel_group_id" {
		otherCol = "user_group_id"
	}
	repointed, merged := 0, 0
	for _, occID := range occupying {
		var perms []model.GroupPermission
		if err := tx.Where(groupCol+" = ?", occID).Find(&perms).Error; err != nil {
			return err
		}
		for _, p := range perms {
			otherVal := p.TunnelGroupID
			if groupCol == "tunnel_group_id" {
				otherVal = p.UserGroupID
			}
			var dup int64
			if err := tx.Model(&model.GroupPermission{}).Where(groupCol+" = ? AND "+otherCol+" = ?", keepID, otherVal).Count(&dup).Error; err != nil {
				return err
			}
			if dup > 0 {
				if err := tx.Where("id = ?", p.ID).Delete(&model.GroupPermission{}).Error; err != nil {
					return err
				}
				merged++
				continue
			}
			if err := tx.Model(&model.GroupPermission{}).Where("id = ?", p.ID).Update(groupCol, keepID).Error; err != nil {
				return err
			}
			repointed++
		}
		var grants []model.GroupPermissionGrant
		if err := tx.Where(groupCol+" = ?", occID).Find(&grants).Error; err != nil {
			return err
		}
		for _, g := range grants {
			otherVal := g.TunnelGroupID
			if groupCol == "tunnel_group_id" {
				otherVal = g.UserGroupID
			}
			var dup int64
			if err := tx.Model(&model.GroupPermissionGrant{}).Where(groupCol+" = ? AND "+otherCol+" = ? AND user_tunnel_id = ?", keepID, otherVal, g.UserTunnelID).Count(&dup).Error; err != nil {
				return err
			}
			if dup > 0 {
				if err := tx.Where("id = ?", g.ID).Delete(&model.GroupPermissionGrant{}).Error; err != nil {
					return err
				}
				merged++
				continue
			}
			if err := tx.Model(&model.GroupPermissionGrant{}).Where("id = ?", g.ID).Update(groupCol, keepID).Error; err != nil {
				return err
			}
			repointed++
		}
	}
	if repointed+merged > 0 {
		rep.warnf("备份恢复：同名分组 %q 的占位组被继任组 #%d 替换，已将 %d 条分组权限/授权的组引用重指向继任组（其中 %d 条与继任组既有记录重复、已合并去重）", name, keepID, repointed+merged, merged)
	}
	if err := tx.Where(memberGroupColumn+" IN ?", occupying).Delete(memberModel).Error; err != nil {
		return err
	}
	return tx.Where("id IN ?", occupying).Delete(groupModel).Error
}

func importUserGroups(r *Repository, tx *gorm.DB, userGroups []model.UserGroupBackup, now int64, rep *importReport) (int, error) {
	count := 0
	for _, ug := range userGroups {
		// D4-02：组名唯一占位预处理（同 importTunnelGroups）。
		if err := deleteSameNameGroupTx(tx, &model.UserGroup{}, "user_group_id", ug.Name, ug.ID, rep); err != nil {
			return count, err
		}
		// R3X-005 facet (a)：成员替换前先读旧成员并按台账撤销被
		// 移出成员的组建授权（与运行时 groupUserAssign 差集撤销
		// 同口径，复用 RevokeGroupGrantsForRemovedUsersTx 的候选
		// 收集）；被撤销 UT 经 rep 回传 handler 清转发。
		var prevUserIDs []int64
		if err := tx.Model(&model.UserGroupUser{}).Where("user_group_id = ?", ug.ID).Pluck("user_id", &prevUserIDs).Error; err != nil {
			return count, err
		}
		revoked, err := r.RevokeGroupGrantsForRemovedUsersTx(tx, ug.ID, prevUserIDs, ug.Users)
		if err != nil {
			return count, err
		}
		rep.addRevoked(revoked)
		if len(revoked) > 0 {
			rep.warnf("备份恢复：用户分组 %q 成员替换已撤销 %d 条被移出成员的组建授权（对应转发将按导入回执逐对清理）", ug.Name, len(revoked))
		}
		item := model.UserGroup{
			ID:          ug.ID,
			Name:        ug.Name,
			CreatedTime: ug.CreatedTime,
			UpdatedTime: now,
			Status:      ug.Status,
		}
		err = tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns([]string{"name", "updated_time", "status"}),
		}).Create(&item).Error
		if err != nil {
			return count, err
		}
		if err := tx.Where("user_group_id = ?", ug.ID).Delete(&model.UserGroupUser{}).Error; err != nil {
			return count, err
		}
		for _, userID := range ug.Users {
			// D4-03：成员引用的用户不存在时跳过该成员并计数。
			userExists, err := rowExistsTx(tx, &model.User{}, userID)
			if err != nil {
				return count, err
			}
			if !userExists {
				rep.skip("userGroupMembers")
				continue
			}
			if err := tx.Create(&model.UserGroupUser{UserGroupID: ug.ID, UserID: userID, CreatedTime: now}).Error; err != nil {
				return count, err
			}
		}
		// R3X-005 facet (c)：导入落 status≠1 的组不保留其 grant
		// （与隧道组段同口径统一收敛，见 importTunnelGroups）。
		if ug.Status != 1 {
			converged, err := r.RevokeGroupGrantsByGroupTx(tx, "user_group", ug.ID)
			if err != nil {
				return count, err
			}
			rep.addRevoked(converged)
			if len(converged) > 0 {
				rep.warnf("备份恢复：用户分组 %q 恢复为停用状态，已按停用口径收敛撤销其 %d 条组建授权", ug.Name, len(converged))
			}
		}
		count++
	}
	return count, nil
}

func importPermissions(r *Repository, tx *gorm.DB, permissions []model.PermissionBackup, _ int64, rep *importReport) (int, error) {
	count := 0
	for _, p := range permissions {
		// D4-03：引用的用户组/隧道组不存在时整行跳过并计数。
		ugExists, err := rowExistsTx(tx, &model.UserGroup{}, p.UserGroupID)
		if err != nil {
			return count, err
		}
		tgExists, err := rowExistsTx(tx, &model.TunnelGroup{}, p.TunnelGroupID)
		if err != nil {
			return count, err
		}
		if !ugExists || !tgExists {
			rep.skip("permissions")
			continue
		}
		// D4-02：(user_group_id, tunnel_group_id) 复合唯一——同键
		// 异 id 的既有行先删占位，以备份为准。
		if err := tx.Where("user_group_id = ? AND tunnel_group_id = ? AND id <> ?", p.UserGroupID, p.TunnelGroupID, p.ID).
			Delete(&model.GroupPermission{}).Error; err != nil {
			return count, err
		}
		item := model.GroupPermission{
			ID:            p.ID,
			UserGroupID:   p.UserGroupID,
			TunnelGroupID: p.TunnelGroupID,
			CreatedTime:   p.CreatedTime,
		}
		err = tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns([]string{"user_group_id", "tunnel_group_id"}),
		}).Create(&item).Error
		if err != nil {
			return count, err
		}
		// R3X-005 facet (c)：任一侧组 status≠1 时跳过该 pair 的
		// grants 并计 warning（运行时 assign/sync 对停用组绝不产生
		// grant，导入面同口径）；该 pair 在目标库的既有组建授权
		// 一并按台账收敛撤销，导入后不留停用组的 grant。
		ugStatus, _, err := getGroupStatusTx(tx, "user_group", p.UserGroupID)
		if err != nil {
			return count, err
		}
		tgStatus, _, err := getGroupStatusTx(tx, "tunnel_group", p.TunnelGroupID)
		if err != nil {
			return count, err
		}
		if ugStatus != 1 || tgStatus != 1 {
			converged, err := r.RevokeGroupPermissionPairTx(tx, p.UserGroupID, p.TunnelGroupID)
			if err != nil {
				return count, err
			}
			rep.addRevoked(converged)
			for range p.Grants {
				rep.skip("permissionGrantsDisabled")
			}
			rep.warnf("备份恢复：权限 #%d 涉及停用分组（用户组 #%d 状态 %d / 隧道组 #%d 状态 %d），已跳过其 %d 条授权并收敛该权限对的既有组建授权 %d 条", p.ID, p.UserGroupID, ugStatus, p.TunnelGroupID, tgStatus, len(p.Grants), len(converged))
			count++
			continue
		}
		for _, g := range p.Grants {
			// D4-03：grant 引用的 user_tunnel 不存在时跳过该条并计数。
			utExists, err := rowExistsTx(tx, &model.UserTunnel{}, g.UserTunnelID)
			if err != nil {
				return count, err
			}
			if !utExists {
				rep.skip("permissionGrants")
				continue
			}
			// R2X-037：grant 组键强制取父 permission 组键。导出不变量
			// 是「grant 组键恒等于父组键」（exportPermissions 按父组键
			// 查 grant），自带值与父不一致本身即不一致数据——忽略
			// 自带值、按父组键写回并计 warning，不写悬空/错组行。
			if g.UserGroupID != p.UserGroupID || g.TunnelGroupID != p.TunnelGroupID {
				rep.warnf("备份恢复：权限 #%d 的授权 #%d 自带组引用（用户组 %d / 隧道组 %d）与父权限组（%d / %d）不一致，已按父权限组写回", p.ID, g.ID, g.UserGroupID, g.TunnelGroupID, p.UserGroupID, p.TunnelGroupID)
			}
			// R4-G-S02 顺查（批 4 落地）：grant 的成员对应校验——
			// UT 的 (user,tunnel) 应分别属父权限对的 user_group /
			// tunnel_group 成员（合法备份的导出不变量）。对应关系
			// 不成立时仅计 warning 不跳行：permissions 单段部分
			// 导入时成员面由分组段另行恢复，跳行会误伤
			// R2X-037 已定的父组键写回口径；且 R4X-005 的防误
			// 提升已保证此类行不会被提升为组建来源。
			var utRow model.UserTunnel
			if err := tx.Select("user_id", "tunnel_id").Where("id = ?", g.UserTunnelID).First(&utRow).Error; err != nil {
				return count, err
			}
			holds, err := grantPairMembershipHoldsTx(tx, p.UserGroupID, p.TunnelGroupID, utRow.UserID, utRow.TunnelID)
			if err != nil {
				return count, err
			}
			if !holds {
				rep.warnf("备份恢复：权限 #%d 的授权 #%d 指向的用户隧道 #%d（用户 %d / 隧道 %d）与权限对成员对应关系不成立（用户不在用户组 #%d 成员内或隧道不在隧道组 #%d 成员内），已按父权限组写回，请核对该授权来源", p.ID, g.ID, g.UserTunnelID, utRow.UserID, utRow.TunnelID, p.UserGroupID, p.TunnelGroupID)
			}
			// D4-02：(user_group_id, tunnel_group_id, user_tunnel_id)
			// 复合唯一占位预处理（键取强制后的父组键）。
			if err := tx.Where("user_group_id = ? AND tunnel_group_id = ? AND user_tunnel_id = ? AND id <> ?",
				p.UserGroupID, p.TunnelGroupID, g.UserTunnelID, g.ID).
				Delete(&model.GroupPermissionGrant{}).Error; err != nil {
				return count, err
			}
			grantItem := model.GroupPermissionGrant{
				ID:             g.ID,
				UserGroupID:    p.UserGroupID,
				TunnelGroupID:  p.TunnelGroupID,
				UserTunnelID:   g.UserTunnelID,
				CreatedTime:    g.CreatedTime,
				CreatedByGroup: g.CreatedByGroup,
			}
			// R2X-037：DoUpdates 补组键两列，存量错误行可被重导入纠正
			//（旧列单只有 user_tunnel_id/created_by_group，错组永不更新）。
			err = tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "id"}},
				DoUpdates: clause.AssignmentColumns([]string{"user_group_id", "tunnel_group_id", "user_tunnel_id", "created_by_group"}),
			}).Create(&grantItem).Error
			if err != nil {
				return count, err
			}
		}
		count++
	}
	return count, nil
}

func importConfigs(tx *gorm.DB, configs map[string]string, now int64) (int, error) {
	configs = FilterBackupConfigs(configs)
	count := 0
	for name, value := range configs {
		err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "name"}},
			DoUpdates: clause.AssignmentColumns([]string{"value", "time"}),
		}).Create(&model.ViteConfig{Name: name, Value: value, Time: now}).Error
		if err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

// ─── Jobs Queries (background stats / expiry) ───────────────────────

func (r *Repository) PurgeOldStatisticsFlows(cutoffMs int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Where("created_time < ?", cutoffMs).Delete(&model.StatisticsFlow{}).Error
}

func (r *Repository) ListAllUserFlowSnapshots() ([]model.UserFlowSnapshot, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var users []model.User
	err := r.db.Order("id ASC").Find(&users).Error
	if err != nil {
		return nil, err
	}
	out := make([]model.UserFlowSnapshot, len(users))
	for i, u := range users {
		out[i] = model.UserFlowSnapshot{UserID: u.ID, InFlow: u.InFlow, OutFlow: u.OutFlow}
	}
	return out, nil
}

func (r *Repository) GetLastStatisticsFlowTotal(userID int64) (sql.NullInt64, error) {
	if r == nil || r.db == nil {
		return sql.NullInt64{}, errors.New("repository not initialized")
	}
	var sf model.StatisticsFlow
	err := r.db.Where("user_id = ?", userID).Order("id DESC").First(&sf).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return sql.NullInt64{}, nil
	}
	if err != nil {
		return sql.NullInt64{}, err
	}
	return sql.NullInt64{Int64: sf.TotalFlow, Valid: true}, nil
}

func (r *Repository) CreateStatisticsFlow(userID, flow, totalFlow int64, timeText string, createdTime int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Create(&model.StatisticsFlow{
		UserID: userID, Flow: flow, TotalFlow: totalFlow,
		Time: timeText, CreatedTime: createdTime,
	}).Error
}

func (r *Repository) ResetUserMonthlyFlow(day int, lastDay int) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	updates := map[string]interface{}{"in_flow": 0, "out_flow": 0}
	if day == lastDay {
		return r.db.Model(&model.User{}).
			Where("flow_reset_time != 0 AND (flow_reset_time = ? OR flow_reset_time > ?)", day, lastDay).
			Updates(updates).Error
	}
	return r.db.Model(&model.User{}).
		Where("flow_reset_time != 0 AND flow_reset_time = ?", day).
		Updates(updates).Error
}

func (r *Repository) ResetUserTunnelMonthlyFlow(day int, lastDay int) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	updates := map[string]interface{}{"in_flow": 0, "out_flow": 0}
	if day == lastDay {
		return r.db.Model(&model.UserTunnel{}).
			Where("flow_reset_time != 0 AND (flow_reset_time = ? OR flow_reset_time > ?)", day, lastDay).
			Updates(updates).Error
	}
	return r.db.Model(&model.UserTunnel{}).
		Where("flow_reset_time != 0 AND flow_reset_time = ?", day).
		Updates(updates).Error
}

func (r *Repository) ListExpiredActiveUserIDs(nowMs int64) ([]int64, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var ids []int64
	err := r.db.Model(&model.User{}).
		Where("role_id != 0 AND status = 1 AND exp_time > 0 AND exp_time < ?", nowMs).
		Pluck("id", &ids).Error
	if err != nil {
		return nil, err
	}
	return ids, nil
}

func (r *Repository) DisableUser(userID int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Model(&model.User{}).Where("id = ?", userID).Update("status", 0).Error
}

func (r *Repository) ListExpiredActiveUserTunnels(nowMs int64) ([]model.ExpiredUserTunnel, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var uts []model.UserTunnel
	err := r.db.Where("status = 1 AND exp_time > 0 AND exp_time < ?", nowMs).Find(&uts).Error
	if err != nil {
		return nil, err
	}
	out := make([]model.ExpiredUserTunnel, len(uts))
	for i, ut := range uts {
		out[i] = model.ExpiredUserTunnel{ID: ut.ID, UserID: ut.UserID, TunnelID: ut.TunnelID}
	}
	return out, nil
}

func (r *Repository) DisableUserTunnel(id int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Model(&model.UserTunnel{}).Where("id = ?", id).Update("status", 0).Error
}

func (r *Repository) GetUserTunnelByID(id int64) (*model.UserTunnel, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	return getUserTunnelByID(r.db, id)
}

func (r *Repository) GetUserTunnelByIDTx(tx *gorm.DB, id int64) (*model.UserTunnel, error) {
	if tx == nil {
		return nil, errors.New("database unavailable")
	}
	return getUserTunnelByID(tx, id)
}

func getUserTunnelByID(db *gorm.DB, id int64) (*model.UserTunnel, error) {
	var ut model.UserTunnel
	err := db.Where("id = ?", id).First(&ut).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ut, nil
}

// ─── Migration ───────────────────────────────────────────────────────

const currentSchemaVersion = 6

var ensurePostgresIDDefaultsFn = ensurePostgresIDDefaults
var migrateViteConfigValueColumnTypeFn = migrateViteConfigValueColumnType
var migrateSpeedLimitTunnelBindingFn = migrateSpeedLimitTunnelBinding
var migratePostgresTrafficInt64ColumnsFn = migratePostgresTrafficInt64Columns
var migrateTunnelMetricBucketUniqueIndexFn = migrateTunnelMetricBucketUniqueIndex

func getSchemaVersion(db *gorm.DB) int {
	var v model.SchemaVersion
	if err := db.First(&v).Error; err != nil {
		db.Create(&model.SchemaVersion{Version: 0})
		return 0
	}
	return v.Version
}

func setSchemaVersion(db *gorm.DB, ver int) {
	db.Model(&model.SchemaVersion{}).Where("1=1").Update("version", ver)
}

func migrateSchema(db *gorm.DB) error {
	if db == nil {
		return errors.New("nil db")
	}

	if err := ensurePostgresIDDefaultsFn(db); err != nil {
		return err
	}

	ver := getSchemaVersion(db)
	if ver >= currentSchemaVersion {
		return nil
	}

	// Normalize strategy columns
	normalizeStrategy := func(modelRef interface{}, table, defaultValue string) error {
		result := db.Model(modelRef).Where("strategy IS NULL").Update("strategy", defaultValue)
		if result.Error != nil {
			msg := strings.ToLower(result.Error.Error())
			if strings.Contains(msg, "no such table") || (strings.Contains(msg, "relation") && strings.Contains(msg, "does not exist")) {
				return nil
			}
			return fmt.Errorf("normalize %s.strategy: %w", table, result.Error)
		}
		return nil
	}

	if err := normalizeStrategy(&model.Forward{}, "forward", "fifo"); err != nil {
		return err
	}
	if err := normalizeStrategy(&model.ChainTunnel{}, "chain_tunnel", "round"); err != nil {
		return err
	}
	if err := normalizeStrategy(&model.PeerShareRuntime{}, "peer_share_runtime", "round"); err != nil {
		return err
	}

	if ver < 3 {
		if err := migrateViteConfigValueColumnTypeFn(db); err != nil {
			return err
		}
	}

	if ver < 4 {
		if err := migrateSpeedLimitTunnelBindingFn(db); err != nil {
			return err
		}
	}

	if ver < 5 {
		if err := migratePostgresTrafficInt64ColumnsFn(db); err != nil {
			return err
		}
	}

	if ver < 6 {
		if err := migrateTunnelMetricBucketUniqueIndexFn(db); err != nil {
			return err
		}
	}

	setSchemaVersion(db, currentSchemaVersion)
	return nil
}

func migrateViteConfigValueColumnType(db *gorm.DB) error {
	if db == nil {
		return errors.New("nil db")
	}

	if !db.Migrator().HasTable(&model.ViteConfig{}) {
		return nil
	}

	if db.Dialector.Name() != "postgres" {
		return nil
	}

	type columnRow struct {
		DataType string `gorm:"column:data_type"`
	}

	var row columnRow
	if err := db.Raw(
		`SELECT data_type FROM information_schema.columns
		 WHERE table_schema = current_schema()
		   AND table_name = ?
		   AND column_name = ?`,
		"vite_config", "value",
	).Scan(&row).Error; err != nil {
		return fmt.Errorf("inspect vite_config.value type: %w", err)
	}

	if strings.EqualFold(row.DataType, "text") {
		return nil
	}

	if err := db.Exec(`ALTER TABLE "vite_config" ALTER COLUMN "value" TYPE TEXT`).Error; err != nil {
		return fmt.Errorf("alter vite_config.value to text: %w", err)
	}

	return nil
}

func migrateSpeedLimitTunnelBinding(db *gorm.DB) error {
	if db == nil {
		return errors.New("nil db")
	}

	if !db.Migrator().HasTable(&model.SpeedLimit{}) {
		return nil
	}

	if err := db.Model(&model.SpeedLimit{}).
		Where("tunnel_id IS NOT NULL OR tunnel_name IS NOT NULL").
		UpdateColumns(map[string]interface{}{
			"tunnel_id":   nil,
			"tunnel_name": nil,
		}).Error; err != nil {
		return fmt.Errorf("clear speed_limit tunnel binding: %w", err)
	}

	return nil
}

func migratePostgresTrafficInt64Columns(db *gorm.DB) error {
	if db == nil {
		return errors.New("nil db")
	}

	if db.Dialector.Name() != "postgres" {
		return nil
	}

	type trafficColumn struct {
		TableName  string
		ColumnName string
	}

	columns := []trafficColumn{
		{TableName: "user", ColumnName: "flow"},
		{TableName: "user", ColumnName: "in_flow"},
		{TableName: "user", ColumnName: "out_flow"},
		{TableName: "forward", ColumnName: "in_flow"},
		{TableName: "forward", ColumnName: "out_flow"},
		{TableName: "statistics_flow", ColumnName: "flow"},
		{TableName: "statistics_flow", ColumnName: "total_flow"},
		{TableName: "tunnel", ColumnName: "flow"},
		{TableName: "user_tunnel", ColumnName: "flow"},
		{TableName: "user_tunnel", ColumnName: "in_flow"},
		{TableName: "user_tunnel", ColumnName: "out_flow"},
		{TableName: "peer_share", ColumnName: "max_bandwidth"},
		{TableName: "peer_share", ColumnName: "current_flow"},
	}

	for _, column := range columns {
		if err := alterPostgresColumnToBigIntIfNeeded(db, column.TableName, column.ColumnName); err != nil {
			return err
		}
	}

	return nil
}

func migrateTunnelMetricBucketUniqueIndex(db *gorm.DB) error {
	if db == nil {
		return errors.New("nil db")
	}

	if !db.Migrator().HasTable(&model.TunnelMetric{}) {
		return nil
	}

	return db.Transaction(func(tx *gorm.DB) error {
		// Only do the heavier dedupe work when needed.
		var dupGroups int64
		q := `
			SELECT COUNT(1) AS cnt
			FROM (
				SELECT 1
				FROM tunnel_metric
				GROUP BY tunnel_id, node_id, timestamp
				HAVING COUNT(*) > 1
			) t
		`
		if err := tx.Raw(q).Scan(&dupGroups).Error; err != nil {
			return fmt.Errorf("inspect tunnel_metric duplicates: %w", err)
		}

		if dupGroups > 0 {
			switch tx.Dialector.Name() {
			case "postgres":
				sql := `
					WITH agg AS (
						SELECT MIN(id) AS keep_id,
						       tunnel_id,
						       node_id,
						       timestamp,
						       SUM(bytes_in) AS bytes_in,
						       SUM(bytes_out) AS bytes_out,
						       SUM(connections) AS connections,
						       SUM(errors) AS errors,
						       AVG(avg_latency_ms) AS avg_latency_ms
						FROM tunnel_metric
						GROUP BY tunnel_id, node_id, timestamp
						HAVING COUNT(*) > 1
					), updated AS (
						UPDATE tunnel_metric tm
						SET bytes_in = agg.bytes_in,
						    bytes_out = agg.bytes_out,
						    connections = agg.connections,
						    errors = agg.errors,
						    avg_latency_ms = agg.avg_latency_ms
						FROM agg
						WHERE tm.id = agg.keep_id
						RETURNING tm.id
					)
					DELETE FROM tunnel_metric tm
					USING agg
					WHERE tm.tunnel_id = agg.tunnel_id
					  AND tm.node_id = agg.node_id
					  AND tm.timestamp = agg.timestamp
					  AND tm.id <> agg.keep_id
				`
				if err := tx.Exec(sql).Error; err != nil {
					return fmt.Errorf("dedupe tunnel_metric buckets: %w", err)
				}
			default:
				// SQLite (and other) path.
				if err := tx.Exec(`DROP TABLE IF EXISTS tunnel_metric_dedupe`).Error; err != nil {
					return fmt.Errorf("prepare tunnel_metric dedupe table: %w", err)
				}
				if err := tx.Exec(`
					CREATE TEMP TABLE tunnel_metric_dedupe AS
					SELECT MIN(id) AS keep_id,
					       tunnel_id,
					       node_id,
					       timestamp,
					       SUM(bytes_in) AS bytes_in,
					       SUM(bytes_out) AS bytes_out,
					       SUM(connections) AS connections,
					       SUM(errors) AS errors,
					       AVG(avg_latency_ms) AS avg_latency_ms
					FROM tunnel_metric
					GROUP BY tunnel_id, node_id, timestamp
					HAVING COUNT(*) > 1
				`).Error; err != nil {
					return fmt.Errorf("build tunnel_metric dedupe table: %w", err)
				}
				if err := tx.Exec(`
					UPDATE tunnel_metric
					SET bytes_in = (SELECT bytes_in FROM tunnel_metric_dedupe d WHERE d.keep_id = tunnel_metric.id),
					    bytes_out = (SELECT bytes_out FROM tunnel_metric_dedupe d WHERE d.keep_id = tunnel_metric.id),
					    connections = (SELECT connections FROM tunnel_metric_dedupe d WHERE d.keep_id = tunnel_metric.id),
					    errors = (SELECT errors FROM tunnel_metric_dedupe d WHERE d.keep_id = tunnel_metric.id),
					    avg_latency_ms = (SELECT avg_latency_ms FROM tunnel_metric_dedupe d WHERE d.keep_id = tunnel_metric.id)
					WHERE id IN (SELECT keep_id FROM tunnel_metric_dedupe)
				`).Error; err != nil {
					return fmt.Errorf("update tunnel_metric deduped rows: %w", err)
				}
				if err := tx.Exec(`
					DELETE FROM tunnel_metric
					WHERE id IN (
						SELECT tm.id
						FROM tunnel_metric tm
						JOIN tunnel_metric_dedupe d
						  ON tm.tunnel_id = d.tunnel_id
						 AND tm.node_id = d.node_id
						 AND tm.timestamp = d.timestamp
						WHERE tm.id <> d.keep_id
					)
				`).Error; err != nil {
					return fmt.Errorf("delete tunnel_metric duplicates: %w", err)
				}
				_ = tx.Exec(`DROP TABLE IF EXISTS tunnel_metric_dedupe`).Error
			}
		}

		// Uniqueness is required for safe upsert on (tunnel_id, node_id, timestamp).
		if err := tx.Exec(
			`CREATE UNIQUE INDEX IF NOT EXISTS uidx_tunnel_metric_bucket ON tunnel_metric(tunnel_id, node_id, timestamp)`,
		).Error; err != nil {
			return fmt.Errorf("create tunnel_metric unique index: %w", err)
		}
		return nil
	})
}

func alterPostgresColumnToBigIntIfNeeded(db *gorm.DB, tableName, columnName string) error {
	if db == nil {
		return errors.New("nil db")
	}

	if tableName == "" || columnName == "" {
		return errors.New("empty table or column name")
	}

	type columnRow struct {
		DataType string `gorm:"column:data_type"`
	}

	var row columnRow
	if err := db.Raw(
		`SELECT data_type FROM information_schema.columns
		 WHERE table_schema = current_schema()
		   AND table_name = ?
		   AND column_name = ?`,
		tableName, columnName,
	).Scan(&row).Error; err != nil {
		return fmt.Errorf("inspect %s.%s type: %w", tableName, columnName, err)
	}

	if row.DataType == "" || strings.EqualFold(row.DataType, "bigint") {
		return nil
	}
	if !strings.EqualFold(row.DataType, "integer") {
		return nil
	}

	if err := db.Exec(fmt.Sprintf(
		"ALTER TABLE %s ALTER COLUMN %s TYPE BIGINT",
		quoteSQLIdentifier(tableName),
		quoteSQLIdentifier(columnName),
	)).Error; err != nil {
		return fmt.Errorf("alter %s.%s to bigint: %w", tableName, columnName, err)
	}

	return nil
}

func ensurePostgresIDDefaults(db *gorm.DB) error {
	if db.Dialector.Name() != "postgres" {
		return nil
	}
	type idRow struct {
		TableSchema string
		TableName   string
	}
	var rows []idRow
	err := db.Table("information_schema.table_constraints AS tc").
		Select("c.table_schema, c.table_name").
		Joins("JOIN information_schema.key_column_usage AS kcu ON tc.constraint_name = kcu.constraint_name AND tc.table_schema = kcu.table_schema").
		Joins("JOIN information_schema.columns AS c ON c.table_schema = kcu.table_schema AND c.table_name = kcu.table_name AND c.column_name = kcu.column_name").
		Where("tc.constraint_type = ?", "PRIMARY KEY").
		Where("kcu.column_name = ?", "id").
		Where("c.data_type IN ?", []string{"integer", "bigint"}).
		Where("c.is_identity = ?", "NO").
		Where("c.table_schema = current_schema()").
		Order("c.table_name ASC").
		Scan(&rows).Error
	if err != nil {
		return fmt.Errorf("discover postgres id columns: %w", err)
	}

	for _, r := range rows {
		if err := ensurePostgresTableIDDefault(db, r.TableSchema, r.TableName); err != nil {
			return fmt.Errorf("repair %s.%s id default: %w", r.TableSchema, r.TableName, err)
		}
	}
	return nil
}

func ensurePostgresTableIDDefault(db *gorm.DB, schemaName, tableName string) error {
	type defaultRow struct {
		ColumnDefault sql.NullString `gorm:"column:column_default"`
	}
	var row defaultRow
	err := db.Table("information_schema.columns").
		Select("column_default").
		Where("table_schema = ? AND table_name = ? AND column_name = 'id'", schemaName, tableName).
		Limit(1).
		Scan(&row).Error
	if err != nil {
		return err
	}
	defaultExpr := row.ColumnDefault

	hasNextvalDefault := defaultExpr.Valid && strings.Contains(strings.ToLower(defaultExpr.String), "nextval(")
	seqRef := ""
	if hasNextvalDefault {
		seqRef = extractNextvalRegclass(defaultExpr.String)
	}

	if !hasNextvalDefault || seqRef == "" {
		seqName := tableName + "_id_seq"
		if err := db.Exec(fmt.Sprintf("CREATE SEQUENCE IF NOT EXISTS %s.%s", quoteSQLIdentifier(schemaName), quoteSQLIdentifier(seqName))).Error; err != nil {
			return err
		}
		seqRef = schemaName + "." + seqName
		if err := db.Exec(fmt.Sprintf(
			"ALTER TABLE %s.%s ALTER COLUMN id SET DEFAULT nextval(%s::regclass)",
			quoteSQLIdentifier(schemaName), quoteSQLIdentifier(tableName), quoteSQLLiteral(seqRef),
		)).Error; err != nil {
			return err
		}
		if err := db.Exec(fmt.Sprintf(
			"ALTER SEQUENCE %s.%s OWNED BY %s.%s.id",
			quoteSQLIdentifier(schemaName), quoteSQLIdentifier(seqName),
			quoteSQLIdentifier(schemaName), quoteSQLIdentifier(tableName),
		)).Error; err != nil {
			return err
		}
	}

	return syncPostgresTableIDSequence(db, schemaName, tableName, seqRef)
}

func syncPostgresTableIDSequence(db *gorm.DB, schemaName, tableName, seqRef string) error {
	type maxRow struct {
		MaxID int64 `gorm:"column:max_id"`
	}
	var row maxRow
	qualifiedTable := fmt.Sprintf("%s.%s", quoteSQLIdentifier(schemaName), quoteSQLIdentifier(tableName))
	err := db.Table(qualifiedTable).
		Select("COALESCE(MAX(id), 0) AS max_id").
		Scan(&row).Error
	if err != nil {
		return err
	}
	maxID := row.MaxID

	setVal := maxID
	isCalled := true
	if maxID <= 0 {
		setVal = 1
		isCalled = false
	}

	return db.Exec(`SELECT setval(?::regclass, ?, ?)`, seqRef, setVal, isCalled).Error
}

func extractNextvalRegclass(defaultExpr string) string {
	nextvalIdx := strings.Index(strings.ToLower(defaultExpr), "nextval(")
	if nextvalIdx < 0 {
		return ""
	}
	expr := defaultExpr[nextvalIdx:]
	firstQuote := strings.Index(expr, "'")
	if firstQuote < 0 {
		return ""
	}
	expr = expr[firstQuote+1:]
	secondQuote := strings.Index(expr, "'")
	if secondQuote < 0 {
		return ""
	}
	return strings.TrimSpace(expr[:secondQuote])
}

func quoteSQLIdentifier(ident string) string {
	return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"`
}

func quoteSQLLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

// ─── Helper Functions ────────────────────────────────────────────────

func resolveForwardIngress(db *gorm.DB, forwardID int64, tunnelID int64) (string, sql.NullInt64, error) {
	var tunnelInIP sql.NullString
	db.Model(&model.Tunnel{}).Select("in_ip").Where("id = ?", tunnelID).Limit(1).Scan(&tunnelInIP)

	type fpRow struct {
		Port     sql.NullInt64
		ServerIP sql.NullString
		InIP     sql.NullString
	}
	var fpRows []fpRow
	err := db.Model(&model.ForwardPort{}).
		Select("forward_port.port, node.server_ip, forward_port.in_ip").
		Joins("LEFT JOIN node ON node.id = forward_port.node_id").
		Where("forward_port.forward_id = ?", forwardID).
		Order("forward_port.id ASC").
		Find(&fpRows).Error
	if err != nil {
		return "", sql.NullInt64{}, err
	}

	ports := make([]int64, 0)
	entries := make([]string, 0)
	seenPorts := make(map[int64]struct{})
	seenPairs := make(map[string]struct{})

	for _, row := range fpRows {
		if !row.Port.Valid {
			continue
		}
		if _, ok := seenPorts[row.Port.Int64]; !ok {
			seenPorts[row.Port.Int64] = struct{}{}
			ports = append(ports, row.Port.Int64)
		}

		var ip string
		if row.InIP.Valid && strings.TrimSpace(row.InIP.String) != "" {
			ip = strings.TrimSpace(row.InIP.String)
		} else if row.ServerIP.Valid && strings.TrimSpace(row.ServerIP.String) != "" {
			ip = strings.TrimSpace(row.ServerIP.String)
		}

		if ip != "" {
			pair := formatForwardIngressAddress(ip, row.Port.Int64)
			if _, ok := seenPairs[pair]; !ok {
				seenPairs[pair] = struct{}{}
				entries = append(entries, pair)
			}
		}
	}

	if len(ports) == 0 {
		return "", sql.NullInt64{}, nil
	}

	inPort := sql.NullInt64{Int64: ports[0], Valid: true}

	return strings.Join(entries, ","), inPort, nil
}

func formatForwardIngressAddress(host string, port int64) string {
	host = strings.TrimSpace(host)
	if host == "" || port <= 0 {
		return ""
	}
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	}
	return net.JoinHostPort(host, strconv.FormatInt(port, 10))
}

func nullableString(v sql.NullString) interface{} {
	if v.Valid {
		return v.String
	}
	return nil
}

func nullableForwardIngress(v string) interface{} {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	return v
}

func nullableInt64(v sql.NullInt64) interface{} {
	if v.Valid {
		return v.Int64
	}
	return nil
}

func unixMilliNow() int64 {
	return time.Now().UnixMilli()
}

func ensureParentDir(dbPath string) error {
	if dbPath == "" {
		return fmt.Errorf("empty db path")
	}
	dir := filepath.Dir(dbPath)
	if dir == "" || dir == "." {
		return nil
	}
	return osMkdirAll(dir)
}

var osMkdirAll = func(path string) error {
	return os.MkdirAll(path, 0o755)
}

// Suppress unused import warning for log
var _ = log.Printf

func (r *Repository) InsertNodeMetric(m *model.NodeMetric) error {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db.Create(m).Error
}

func (r *Repository) InsertNodeMetricBatch(metrics []*model.NodeMetric) error {
	if r == nil || r.db == nil || len(metrics) == 0 {
		return nil
	}
	return r.db.CreateInBatches(metrics, 100).Error
}

func (r *Repository) GetNodeMetrics(nodeID int64, startMs, endMs int64) ([]model.NodeMetric, error) {
	if r == nil || r.db == nil {
		return nil, nil
	}

	rangeMs := endMs - startMs
	const maxRawRangeMs = int64(60 * 60 * 1000) // 1 hour — return raw data for short ranges
	const targetPoints = 500                    // target number of chart points for downsampled data

	// For short ranges, return raw data (full resolution).
	if rangeMs <= maxRawRangeMs {
		var metrics []model.NodeMetric
		err := r.db.Where("node_id = ? AND timestamp >= ? AND timestamp <= ?", nodeID, startMs, endMs).
			Order("timestamp ASC").
			Limit(5000).
			Find(&metrics).Error
		return metrics, err
	}

	// For longer ranges, downsample via SQL aggregation to keep the response small and fast.
	bucketMs := rangeMs / targetPoints
	if bucketMs < 1000 {
		bucketMs = 1000 // minimum 1-second buckets
	}

	bucketExpr := fmt.Sprintf("(timestamp / %d * %d)", bucketMs, bucketMs)
	groupExpr := fmt.Sprintf("timestamp / %d", bucketMs)

	var metrics []model.NodeMetric
	err := r.db.Model(&model.NodeMetric{}).
		Select(
			fmt.Sprintf(
				"%d AS node_id, "+
					"CAST(%s AS BIGINT) AS timestamp, "+
					"AVG(cpu_usage) AS cpu_usage, "+
					"AVG(mem_usage) AS mem_usage, "+
					"AVG(disk_usage) AS disk_usage, "+
					"CAST(AVG(net_in_bytes) AS BIGINT) AS net_in_bytes, "+
					"CAST(AVG(net_out_bytes) AS BIGINT) AS net_out_bytes, "+
					"CAST(AVG(net_in_speed) AS BIGINT) AS net_in_speed, "+
					"CAST(AVG(net_out_speed) AS BIGINT) AS net_out_speed, "+
					"AVG(load1) AS load1, "+
					"AVG(load5) AS load5, "+
					"AVG(load15) AS load15, "+
					"CAST(AVG(tcp_conns) AS BIGINT) AS tcp_conns, "+
					"CAST(AVG(udp_conns) AS BIGINT) AS udp_conns, "+
					"CAST(MAX(uptime) AS BIGINT) AS uptime",
				nodeID, bucketExpr,
			),
		).
		Where("node_id = ? AND timestamp >= ? AND timestamp <= ?", nodeID, startMs, endMs).
		Group(groupExpr).
		Order("timestamp ASC").
		Limit(targetPoints + 100). // safety margin
		Scan(&metrics).Error

	if metrics == nil {
		metrics = make([]model.NodeMetric, 0)
	}
	return metrics, err
}

func (r *Repository) GetLatestNodeMetric(nodeID int64) (*model.NodeMetric, error) {
	if r == nil || r.db == nil {
		return nil, nil
	}
	var m model.NodeMetric
	err := r.db.Where("node_id = ?", nodeID).Order("timestamp DESC").First(&m).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &m, nil
}

func (r *Repository) PruneNodeMetrics(olderThanMs int64) error {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db.Where("timestamp < ?", olderThanMs).Delete(&model.NodeMetric{}).Error
}

// AddNodeTraffic adds hourly node NIC traffic deltas to the rows,
// creating the (node_id, hour) bucket on first write.
func (r *Repository) AddNodeTraffic(rows []model.NodeTraffic) error {
	if r == nil || r.db == nil || len(rows) == 0 {
		return nil
	}
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "node_id"}, {Name: "hour"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"in_bytes":  gorm.Expr("node_traffic.in_bytes + excluded.in_bytes"),
			"out_bytes": gorm.Expr("node_traffic.out_bytes + excluded.out_bytes"),
		}),
	}).Create(&rows).Error
}

// NodeTrafficSince sums each node's NIC traffic from fromMs on.
// Values are [in, out] whole-machine NIC bytes — not tunnel billing.
//
// Buckets are UTC-hour aligned, so a fromMs in a non-whole-hour viewer
// zone falls inside the first bucket: that bucket contributes only its
// post-fromMs share, prorated by minutes the way the subscription side
// splits straddling buckets (BC-12) — the counted segment is the row's
// last segment and takes the remainder after the excluded share is
// floored. Whole-hour zones align exactly and are summed as before.
func (r *Repository) NodeTrafficSince(fromMs int64) (map[int64][2]int64, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	const hourMs = int64(time.Hour / time.Millisecond)
	bucketStart := fromMs - ((fromMs%hourMs)+hourMs)%hourMs
	fullFrom := bucketStart
	if fromMs > bucketStart {
		fullFrom = bucketStart + hourMs
	}
	var rows []struct {
		NodeID   int64
		InBytes  int64
		OutBytes int64
	}
	err := r.db.Model(&model.NodeTraffic{}).Select("node_id, SUM(in_bytes) AS in_bytes, SUM(out_bytes) AS out_bytes").
		Where("hour >= ?", fullFrom).Group("node_id").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[int64][2]int64, len(rows))
	for _, row := range rows {
		out[row.NodeID] = [2]int64{row.InBytes, row.OutBytes}
	}
	if fromMs > bucketStart {
		var boundary []model.NodeTraffic
		if err := r.db.Where("hour = ?", bucketStart).Find(&boundary).Error; err != nil {
			return nil, err
		}
		pre := fromMs - bucketStart
		for _, b := range boundary {
			cur := out[b.NodeID]
			cur[0] += b.InBytes - b.InBytes*pre/hourMs
			cur[1] += b.OutBytes - b.OutBytes*pre/hourMs
			out[b.NodeID] = cur
		}
	}
	return out, nil
}

// PruneNodeTraffic drops the hourly buckets before beforeMs.
func (r *Repository) PruneNodeTraffic(beforeMs int64) error {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db.Where("hour < ?", beforeMs).Delete(&model.NodeTraffic{}).Error
}

func (r *Repository) InsertTunnelMetric(m *model.TunnelMetric) error {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db.Create(m).Error
}

func (r *Repository) InsertTunnelMetricBatch(metrics []*model.TunnelMetric) error {
	if r == nil || r.db == nil || len(metrics) == 0 {
		return nil
	}
	return r.db.CreateInBatches(metrics, 100).Error
}

// UpsertTunnelMetricBuckets adds the provided metric deltas into per-minute buckets.
// Requires a unique index on (tunnel_id, node_id, timestamp) for safe upserts.
func (r *Repository) UpsertTunnelMetricBuckets(metrics []*model.TunnelMetric) error {
	if r == nil || r.db == nil || len(metrics) == 0 {
		return nil
	}

	// Postgres rejects a single INSERT ... ON CONFLICT when the input contains
	// duplicate conflict keys. Pre-aggregate within this batch to keep inserts safe.
	type bucketKey struct {
		tunnelID  int64
		nodeID    int64
		timestamp int64
	}

	agg := make(map[bucketKey]*model.TunnelMetric, len(metrics))
	for _, m := range metrics {
		if m == nil {
			continue
		}
		if m.TunnelID <= 0 || m.NodeID <= 0 || m.Timestamp <= 0 {
			continue
		}
		if m.BytesIn == 0 && m.BytesOut == 0 && m.Connections == 0 && m.Errors == 0 {
			continue
		}

		k := bucketKey{tunnelID: m.TunnelID, nodeID: m.NodeID, timestamp: m.Timestamp}
		if existing, ok := agg[k]; ok {
			existing.BytesIn += m.BytesIn
			existing.BytesOut += m.BytesOut
			existing.Connections += m.Connections
			existing.Errors += m.Errors
			if existing.AvgLatencyMs == 0 && m.AvgLatencyMs != 0 {
				existing.AvgLatencyMs = m.AvgLatencyMs
			}
			continue
		}
		cp := *m
		agg[k] = &cp
	}
	if len(agg) == 0 {
		return nil
	}

	rows := make([]*model.TunnelMetric, 0, len(agg))
	for _, v := range agg {
		rows = append(rows, v)
	}

	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "tunnel_id"}, {Name: "node_id"}, {Name: "timestamp"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"bytes_in":    gorm.Expr("tunnel_metric.bytes_in + excluded.bytes_in"),
			"bytes_out":   gorm.Expr("tunnel_metric.bytes_out + excluded.bytes_out"),
			"connections": gorm.Expr("tunnel_metric.connections + excluded.connections"),
			"errors":      gorm.Expr("tunnel_metric.errors + excluded.errors"),
			// avg_latency_ms is not additive; keep the existing bucket value.
		}),
	}).CreateInBatches(rows, 100).Error
}

func (r *Repository) GetTunnelMetrics(tunnelID int64, startMs, endMs int64) ([]model.TunnelMetric, error) {
	if r == nil || r.db == nil {
		return nil, nil
	}
	var metrics []model.TunnelMetric
	err := r.db.Where("tunnel_id = ? AND timestamp >= ? AND timestamp <= ?", tunnelID, startMs, endMs).
		Order("timestamp DESC").
		Limit(5000).
		Find(&metrics).Error
	if len(metrics) > 1 {
		for i, j := 0, len(metrics)-1; i < j; i, j = i+1, j-1 {
			metrics[i], metrics[j] = metrics[j], metrics[i]
		}
	}
	return metrics, err
}

// GetTunnelMetricsAggregated returns tunnel-level aggregated series (one point per timestamp).
// Storage remains per (tunnel_id, node_id, timestamp) for future drill-down.
func (r *Repository) GetTunnelMetricsAggregated(tunnelID int64, startMs, endMs int64) ([]model.TunnelMetric, error) {
	if r == nil || r.db == nil {
		return nil, nil
	}

	var metrics []model.TunnelMetric
	err := r.db.Model(&model.TunnelMetric{}).
		Select(
			"tunnel_id, 0 AS node_id, timestamp, "+
				"SUM(bytes_in) AS bytes_in, "+
				"SUM(bytes_out) AS bytes_out, "+
				"SUM(connections) AS connections, "+
				"SUM(errors) AS errors, "+
				"AVG(avg_latency_ms) AS avg_latency_ms",
		).
		Where("tunnel_id = ? AND timestamp >= ? AND timestamp <= ?", tunnelID, startMs, endMs).
		Group("tunnel_id, timestamp").
		Order("timestamp ASC").
		Limit(5000).
		Scan(&metrics).Error
	if metrics == nil {
		metrics = make([]model.TunnelMetric, 0)
	}
	return metrics, err
}

func (r *Repository) PruneTunnelMetrics(olderThanMs int64) error {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db.Where("timestamp < ?", olderThanMs).Delete(&model.TunnelMetric{}).Error
}

func (r *Repository) ListServiceMonitors() ([]model.ServiceMonitor, error) {
	if r == nil || r.db == nil {
		return nil, nil
	}
	var monitors []model.ServiceMonitor
	err := r.db.Order("id ASC").Find(&monitors).Error
	return monitors, err
}

func (r *Repository) ListEnabledServiceMonitors() ([]model.ServiceMonitor, error) {
	if r == nil || r.db == nil {
		return nil, nil
	}
	var monitors []model.ServiceMonitor
	err := r.db.Where("enabled = 1 AND type IN (?)", []string{"tcp", "icmp"}).Order("id ASC").Find(&monitors).Error
	return monitors, err
}

func (r *Repository) GetServiceMonitor(id int64) (*model.ServiceMonitor, error) {
	if r == nil || r.db == nil {
		return nil, nil
	}
	var m model.ServiceMonitor
	err := r.db.First(&m, id).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &m, nil
}

func (r *Repository) CreateServiceMonitor(m *model.ServiceMonitor) error {
	if r == nil || r.db == nil {
		return nil
	}
	// A2-03（零值家族）：ServiceMonitor.Enabled 带 `default:1`，以停用
	// 状态创建的监控会被 Create 吞掉 0、落库为 1 并立即开始探测。
	// 仿 CreateNodeInbound（BUG-A4-1）留意图、创建后写回。
	wantEnabled := m.Enabled
	if err := r.db.Create(m).Error; err != nil {
		return err
	}
	if wantEnabled == 0 {
		if err := r.db.Model(&model.ServiceMonitor{}).Where("id = ?", m.ID).Update("enabled", 0).Error; err != nil {
			return err
		}
		m.Enabled = 0
	}
	return nil
}

func (r *Repository) UpdateServiceMonitor(m *model.ServiceMonitor) error {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db.Save(m).Error
}

func (r *Repository) DeleteServiceMonitor(id int64) error {
	if r == nil || r.db == nil {
		return nil
	}
	if id <= 0 {
		return nil
	}

	// Keep API/UI semantics simple: deleting a monitor also deletes its history.
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("monitor_id = ?", id).Delete(&model.ServiceMonitorResult{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.ServiceMonitor{}, id).Error
	})
}

func (r *Repository) InsertServiceMonitorResult(result *model.ServiceMonitorResult) error {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db.Create(result).Error
}

func (r *Repository) GetServiceMonitorResults(monitorID int64, limit int) ([]model.ServiceMonitorResult, error) {
	if r == nil || r.db == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 100
	}
	var results []model.ServiceMonitorResult
	err := r.db.Where("monitor_id = ?", monitorID).
		Order("timestamp DESC").
		Limit(limit).
		Find(&results).Error
	return results, err
}

// GetServiceMonitorResultsByTimeRange returns results for a monitor within [startMs, endMs].
// Mirrors GetNodeMetrics / GetTunnelMetrics pattern for time-range based charting.
func (r *Repository) GetServiceMonitorResultsByTimeRange(monitorID int64, startMs, endMs int64) ([]model.ServiceMonitorResult, error) {
	if r == nil || r.db == nil {
		return nil, nil
	}
	var results []model.ServiceMonitorResult
	err := r.db.Where("monitor_id = ? AND timestamp >= ? AND timestamp <= ?", monitorID, startMs, endMs).
		Order("timestamp DESC").
		Limit(5000).
		Find(&results).Error
	if len(results) > 1 {
		for i, j := 0, len(results)-1; i < j; i, j = i+1, j-1 {
			results[i], results[j] = results[j], results[i]
		}
	}
	return results, err
}

// GetLatestServiceMonitorResults returns the newest result per monitor_id.
// This is intended for list rendering (avoid N+1 queries).
func (r *Repository) GetLatestServiceMonitorResults() ([]model.ServiceMonitorResult, error) {
	if r == nil || r.db == nil {
		return nil, nil
	}

	var results []model.ServiceMonitorResult

	// Prefer a window-function query (works on modern SQLite + Postgres).
	q1 := `
		SELECT id, monitor_id, node_id, timestamp, success, latency_ms, status_code, error_message
		FROM (
			SELECT *, ROW_NUMBER() OVER (PARTITION BY monitor_id ORDER BY timestamp DESC, id DESC) AS rn
			FROM service_monitor_result
		) t
		WHERE rn = 1
		ORDER BY monitor_id ASC
	`
	if err := r.db.Raw(q1).Scan(&results).Error; err == nil {
		return results, nil
	}

	// Fallback: just return newest rows (best-effort). This avoids hard failure on older SQLite builds.
	// Note: This may not include all monitors if the table is extremely large and skewed.
	results = nil
	q2 := `
		SELECT id, monitor_id, node_id, timestamp, success, latency_ms, status_code, error_message
		FROM service_monitor_result
		ORDER BY timestamp DESC, id DESC
		LIMIT 5000
	`
	err := r.db.Raw(q2).Scan(&results).Error
	if err != nil {
		return nil, err
	}

	seen := make(map[int64]struct{}, len(results))
	out := make([]model.ServiceMonitorResult, 0, len(results))
	for _, row := range results {
		if row.MonitorID <= 0 {
			continue
		}
		if _, ok := seen[row.MonitorID]; ok {
			continue
		}
		seen[row.MonitorID] = struct{}{}
		out = append(out, row)
	}
	// Keep response stable for the frontend.
	sort.Slice(out, func(i, j int) bool { return out[i].MonitorID < out[j].MonitorID })
	return out, nil
}

func (r *Repository) PruneServiceMonitorResults(olderThanMs int64) error {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db.Where("timestamp < ?", olderThanMs).Delete(&model.ServiceMonitorResult{}).Error
}
