package repo

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go-backend/internal/security"
	"go-backend/internal/store/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// UserExists 用户名占用检查。B02-003：大小写不敏感（经
// usernameFoldCount 统一口径），Admin 存在时 admin 亦视为已占用。
func (r *Repository) UserExists(username string) (bool, error) {
	if r == nil || r.db == nil {
		return false, errors.New("repository not initialized")
	}
	cnt, err := usernameFoldCount(r.db, username, 0)
	return cnt > 0, err
}

// UserExistsExcluding 同 UserExists，排除自身行（改名自查）。
func (r *Repository) UserExistsExcluding(username string, excludeID int64) (bool, error) {
	if r == nil || r.db == nil {
		return false, errors.New("repository not initialized")
	}
	cnt, err := usernameFoldCount(r.db, username, excludeID)
	return cnt > 0, err
}

func optionalFlowMiB(values []int64) int64 {
	if len(values) > 0 {
		return values[0]
	}
	return 0
}

func (r *Repository) CreateUser(username, pwdHash string, roleID int, expTime, flow, flowResetTime int64, num, status, maxConn int, now int64, flowMiB ...int64) (int64, error) {
	if r == nil || r.db == nil {
		return 0, errors.New("repository not initialized")
	}
	user := model.User{
		User:              username,
		Pwd:               pwdHash,
		RoleID:            roleID,
		ExpTime:           expTime,
		Flow:              flow,
		FlowMiB:           optionalFlowMiB(flowMiB),
		InFlow:            0,
		OutFlow:           0,
		FlowResetTime:     flowResetTime,
		FlowLastResetTime: now,
		Num:               num,
		MaxConn:           maxConn,
		CreatedTime:       now,
		UpdatedTime:       sql.NullInt64{Int64: now, Valid: true},
		Status:            status,
		PasswordChangedAt: now,
	}
	if err := r.db.Create(&user).Error; err != nil {
		// R5-B4 C-010: 唯一约束冲突 → "用户名已存在"（409 语义），不得 500。
		return 0, mapUniqueViolation(err, "username")
	}
	return user.ID, nil
}

// CreateUserTx 是 O5-004 的事务版：与 CreateUser 相同的写入，但跑在调用方事务内，
// 让 CreateUser + 配额配置 + 分组三步同成同败。
func (r *Repository) CreateUserTx(tx *gorm.DB, username, pwdHash string, roleID int, expTime, flow, flowResetTime int64, num, status, maxConn int, now int64, flowMiB ...int64) (int64, error) {
	if tx == nil {
		return 0, errors.New("database unavailable")
	}
	// R2X-049（补批13）：提交段内复检同名。userCreate 的第一道检查
	// 在事务之外，并发同名可同时通过预检；落库前在事务内再查一次，
	// 存在即拒绝。此处不依赖 DB 唯一索引：model.User 无 uniqueIndex
	// tag，R5-B4 v1 迁移在存量有重复时按 D23 跳过建索引，「无用户名
	// 唯一索引」是受支持状态，复检是该状态下的唯一防线。
	// X01-001：PG 默认隔离下 Count 复检本身不构成互斥（两并发事务
	// 可双双读到 0），复检前先按用户名取事务级咨询锁兜底串行化；
	// SQLite 为空操作，靠 MaxOpenConns(1) 串行化。见 pg_name_lock.go。
	// B02-003：复检与锁键均按大小写折叠口径——锁键折叠经
	// dbFoldUsername 由 DB LOWER() 取数（LB-K10-02：与判重同一
	// 折叠定义源，不用 Go strings.ToLower 另算），使 Admin/admin
	// 两路并发在锁层即互斥（旧实现按原文取锁，变体并发各持一把
	// 锁、复检又精确比较，双双落库）；复检经 usernameFoldCount
	// 与全部判重面同口径。v5 折叠唯一索引在场时另有 DB 兜底
	// （tx.Create 的 mapUniqueViolation）。
	lockFold, err := dbFoldUsername(tx, username)
	if err != nil {
		return 0, err
	}
	if err := lockNameForTx(tx, nameLockScopeUser, lockFold); err != nil {
		return 0, err
	}
	dupCnt, err := usernameFoldCount(tx, username, 0)
	if err != nil {
		return 0, err
	}
	if dupCnt > 0 {
		return 0, errUsernameExists
	}
	user := model.User{
		User:              username,
		Pwd:               pwdHash,
		RoleID:            roleID,
		ExpTime:           expTime,
		Flow:              flow,
		FlowMiB:           optionalFlowMiB(flowMiB),
		InFlow:            0,
		OutFlow:           0,
		FlowResetTime:     flowResetTime,
		FlowLastResetTime: now,
		Num:               num,
		MaxConn:           maxConn,
		CreatedTime:       now,
		UpdatedTime:       sql.NullInt64{Int64: now, Valid: true},
		Status:            status,
		PasswordChangedAt: now,
	}
	if err := tx.Create(&user).Error; err != nil {
		return 0, mapUniqueViolation(err, "username")
	}
	return user.ID, nil
}

func (r *Repository) GetUserRoleID(userID int64) (int, error) {
	if r == nil || r.db == nil {
		return 0, errors.New("repository not initialized")
	}
	var user model.User
	err := r.db.Select("role_id").Where("id = ?", userID).First(&user).Error
	if err != nil {
		return 0, normalizeNotFoundErr(err)
	}
	return user.RoleID, nil
}

// updateUserWithRenameGuard 是全部改名 repo 方法的事务封装（R3
// B02-002）：与 CreateUserTx 同构——事务内先经 dbFoldUsername 由 SQL
// LOWER() 取折叠键（与判重同一定义源，LB-K10-02）、取同名咨询锁
// （lockNameForTx，PG；SQLite 为空操作、靠单连接串行）、再以
// usernameFoldCount 折叠复检（排除自身），通过后才 Updates。
//
// 此前三个改名方法（UpdateUserWithPassword / UpdateUserWithoutPassword
// / UpdateUserNameAndPassword）均为非事务 Updates、只靠 handler 事务
// 外预检 + 唯一冲突映射兜底：v5 折叠索引在场时兜底成立，但降级态
// （存量双生子致索引按 D23 跳过）下并发改名交错可双双通过预检、
// 新建大小写双生子——正是 S-17 降级语义下应用层判重必须照常执法
// 的面，create 面已对齐、改名面漏齐，本封装补齐差集。v5 索引在场
// 时另有 mapUniqueViolation DB 兜底，口径不变。
func (r *Repository) updateUserWithRenameGuard(id int64, username string, updates map[string]interface{}) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		lockFold, err := dbFoldUsername(tx, username)
		if err != nil {
			return err
		}
		if err := lockNameForTx(tx, nameLockScopeUser, lockFold); err != nil {
			return err
		}
		if err := ensureUsernameAvailable(tx, username, id); err != nil {
			return err
		}
		if err := tx.Model(&model.User{}).Where("id = ?", id).Updates(updates).Error; err != nil {
			return mapUniqueViolation(err, "username")
		}
		return nil
	})
}

func (r *Repository) UpdateUserWithPassword(id int64, username, pwdHash string, flow int64, num int, expTime, flowResetTime int64, status, maxConn int, now int64, flowMiB ...int64) error {
	// R3 B02-002：改名经 updateUserWithRenameGuard 事务内折叠复检
	// （降级态同样执法），不再只靠事务外预检 + 唯一冲突映射。
	err := r.updateUserWithRenameGuard(id, username, map[string]interface{}{
		"user":                username,
		"pwd":                 pwdHash,
		"flow":                flow,
		"flow_mib":            optionalFlowMiB(flowMiB),
		"num":                 num,
		"exp_time":            expTime,
		"flow_reset_time":     flowResetTime,
		"status":              status,
		"max_conn":            maxConn,
		"password_changed_at": now,
		"updated_time":        sql.NullInt64{Int64: now, Valid: true},
	})
	// R1-12-01：同 UpdateUserNameAndPassword——id=1 真实改密后
	// 初始口令候选与一次性文件失效。
	if err == nil && id == 1 {
		clearInitialAdminPassword()
	}
	return err
}

func (r *Repository) UpdateUserWithoutPassword(id int64, username string, flow int64, num int, expTime, flowResetTime int64, status, maxConn int, now int64, flowMiB ...int64) error {
	// R3 B02-002：同 UpdateUserWithPassword，事务内折叠复检。
	return r.updateUserWithRenameGuard(id, username, map[string]interface{}{
		"user":            username,
		"flow":            flow,
		"flow_mib":        optionalFlowMiB(flowMiB),
		"num":             num,
		"exp_time":        expTime,
		"flow_reset_time": flowResetTime,
		"status":          status,
		"max_conn":        maxConn,
		"updated_time":    sql.NullInt64{Int64: now, Valid: true},
	})
}

// UpdateUserPassword 设置新哈希并推进 password_changed_at——推进即
// 吊销该用户全部既有会话（中间件/WS 以 IatMs<=PasswordChangedAt
// 判失效）。只适用于真实凭据变更语义；legacy 哈希透明迁移（密码
// 本身未变更）禁用本方法，须走 MigrateUserPasswordHash（R4B01-002）。
func (r *Repository) UpdateUserPassword(userID int64, pwdHash string, now int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	err := r.db.Model(&model.User{}).
		Where("id = ?", userID).
		Updates(map[string]interface{}{
			"pwd":                 pwdHash,
			"password_changed_at": now,
			"updated_time":        sql.NullInt64{Int64: now, Valid: true},
		}).Error
	// R1-12-01：同 UpdateUserNameAndPassword——id=1 真实改密后
	// 初始口令候选与一次性文件失效。
	if err == nil && userID == 1 {
		clearInitialAdminPassword()
	}
	return err
}

// MigrateUserPasswordHash 只替换存储哈希，不推进 password_changed_at
// （R4B01-002 机制根治）。专供 legacy 哈希透明迁移（rehash）：密码
// 本身未变更，纯存储格式升级不得产生会话吊销副作用——
// password_changed_at 只由真实凭据变更事件推进（本人改密
// UpdateUserNameAndPassword、管理员重置 UpdateUserWithPassword /
// UpdateUserPassword、RevokeUserSessions）。旧实现两条 rehash 路径
// 都走 UpdateUserPassword：login 路径靠签发补偿掩盖，sub_store 路径
// 不签发任何 token、无从补偿，存量 legacy 用户经其拉取一次订阅
// 用量即被吊销全部既有面板会话，两面口径分裂。
func (r *Repository) MigrateUserPasswordHash(userID int64, pwdHash string, now int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Model(&model.User{}).
		Where("id = ?", userID).
		Updates(map[string]interface{}{
			"pwd":          pwdHash,
			"updated_time": sql.NullInt64{Int64: now, Valid: true},
		}).Error
}

// SunsetLegacyUserPassword 把仍持 legacy MD5 哈希的存量账户收口
// 为与导入面 normalizeImportedUserPassword 相同的终态（R-MD5
// sunset）：随机 bcrypt 替换原哈希（原口令即刻不可用）、status=0、
// password_changed_at=now（吊销既有会话）；密码重置走既有管理员
// 通道，与导入的 legacy 账户同路，不另造重置语义。更新以「id＋
// 当前存储哈希」为守卫：并发下存储已被管理员重置（不再是原
// legacy 哈希）时不覆盖、返回 applied=false；存储不是 legacy
// 形态时同样不动作。
func (r *Repository) SunsetLegacyUserPassword(userID int64, currentHash string, now int64) (bool, error) {
	if r == nil || r.db == nil {
		return false, errors.New("repository not initialized")
	}
	if !security.IsLegacyPasswordHash(currentHash) {
		return false, nil
	}
	replacement, err := randomPasswordHash()
	if err != nil {
		return false, err
	}
	res := r.db.Model(&model.User{}).
		Where("id = ? AND pwd = ?", userID, currentHash).
		Updates(map[string]interface{}{
			"pwd":                 replacement,
			"status":              0,
			"password_changed_at": now,
			"updated_time":        sql.NullInt64{Int64: now, Valid: true},
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

func (r *Repository) PropagateUserFlowToTunnels(userID int64, flow int64, num int, expTime, flowResetTime int64, flowMiB ...int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Model(&model.UserTunnel{}).
		Where("user_id = ?", userID).
		Updates(map[string]interface{}{
			"flow":            flow,
			"flow_mib":        optionalFlowMiB(flowMiB),
			"num":             num,
			"exp_time":        expTime,
			"flow_reset_time": flowResetTime,
		}).Error
}

func (r *Repository) DeleteUserCascade(userID int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ?", userID).Delete(&model.Passkey{}).Error; err != nil {
			return err
		}
		forwardIDs := tx.Model(&model.Forward{}).Select("id").Where("user_id = ?", userID)
		if err := tx.Where("forward_id IN (?)", forwardIDs).Delete(&model.ForwardPort{}).Error; err != nil {
			return err
		}
		// B4/F-057＋F-076（A4-005）: 与 DeleteForwardCascade 对齐——删用户
		// 须同步清其转发的 nft 台账三表与中转行：计数基线不清会永久残留
		// 为孤儿（forward_id 不复用，无计费影响，但表随删用户单调膨胀），
		// pending 行不清会被后台重试循环反复取出对不存在的 forward 报错。
		// 单删/隧删/节点删均已在各自事务内内联清理，唯独用户级联曾独漏；
		// 预留的 DeleteNftCounterStatesByForward 非事务型（走 r.db），故
		// 此处与其它级联一致用 tx 内联删（R8-052：三表各删一趟，勿重复）。
		if err := tx.Where("forward_id IN (?)", forwardIDs).Delete(&model.NftCounterState{}).Error; err != nil {
			return err
		}
		if err := tx.Where("forward_id IN (?)", forwardIDs).Delete(&model.NftRuleBinding{}).Error; err != nil {
			return err
		}
		if err := tx.Where("forward_id IN (?)", forwardIDs).Delete(&model.NftPendingDelete{}).Error; err != nil {
			return err
		}
		// X2-01：删 relay 行前先物化其 id 集合，同步清理指向这些
		// relay 的 user_inbound 授权行——授权可属于其它用户（按用户
		// 独立分配），仅按 user_id 清理覆盖不到，会留下他人孤儿授权。
		var userForwardIDs []int64
		if err := tx.Model(&model.Forward{}).Where("user_id = ?", userID).Pluck("id", &userForwardIDs).Error; err != nil {
			return err
		}
		relayIDs, err := pluckInboundRelayIDsByColumnTx(tx, "forward_id", userForwardIDs)
		if err != nil {
			return err
		}
		if err := deleteUserInboundGrantsByRelayIDsTx(tx, relayIDs); err != nil {
			return err
		}
		// FIX-CASCADE（E1-12）：本级联删掉的转发同步摘除配额暂停
		// 清单（本用户的 UserQuota 行下方一并删除，此处覆盖清单被
		// 其他用户行引用的残余形态，口径与单删转发一致）。
		if err := prunePausedForwardIDsTx(tx, userForwardIDs); err != nil {
			return err
		}
		if err := tx.Where("forward_id IN (?)", forwardIDs).Delete(&model.NodeInboundRelay{}).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", userID).Delete(&model.Forward{}).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", userID).Delete(&model.UserTunnel{}).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", userID).Delete(&model.StatisticsFlow{}).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", userID).Delete(&model.UserQuota{}).Error; err != nil {
			return err
		}
		// R5-B7: 补 monitor_permission。
		if err := tx.Where("user_id = ?", userID).Delete(&model.MonitorPermission{}).Error; err != nil {
			return err
		}
		// O5-001：删除用户时清理 user_inbound，否则残留孤儿数据。
		// 参考 repository_kernel.go 删除入站/中转时的清理模式。
		if err := tx.Where("user_id = ?", userID).Delete(&model.UserInbound{}).Error; err != nil {
			return err
		}
		// B4/F-011: 1.0.12 订阅分配表 user_subscription 同步清理
		// （与删订阅侧 DeleteUserSubscriptionsBySubscription 对称）。
		if err := tx.Where("user_id = ?", userID).Delete(&model.UserSubscription{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", userID).Delete(&model.User{}).Error
	})
}

// ResetUserFlowByUser 清零用户及其全部 user_tunnel 的流量计数。
// B2-02：此前两处更新的错误被就地吞掉、签名无返回值，handler
// 只能恒报成功——重置实际未落库时管理员无从知晓。现回传合并
// 错误，与同文件 ResetForwardFlow/ResetUserQuotaUsage 同口径。
func (r *Repository) ResetUserFlowByUser(userID int64, now int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	errUser := r.db.Model(&model.User{}).
		Where("id = ?", userID).
		Updates(map[string]interface{}{
			"in_flow":      0,
			"out_flow":     0,
			"updated_time": sql.NullInt64{Int64: now, Valid: true},
		}).Error
	errTunnels := r.db.Model(&model.UserTunnel{}).
		Where("user_id = ?", userID).
		Updates(map[string]interface{}{"in_flow": 0, "out_flow": 0}).Error
	return errors.Join(errUser, errTunnels)
}

func (r *Repository) ResetUserFlowByUserTunnel(userTunnelID int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Model(&model.UserTunnel{}).
		Where("id = ?", userTunnelID).
		Updates(map[string]interface{}{"in_flow": 0, "out_flow": 0}).Error
}

func (r *Repository) ResetForwardFlow(forwardID int64, now int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Model(&model.Forward{}).
		Where("id = ?", forwardID).
		Updates(map[string]interface{}{
			"in_flow":      0,
			"out_flow":     0,
			"updated_time": now,
		}).Error
}

func (r *Repository) GetUsernameByID(userID int64) string {
	if r == nil || r.db == nil {
		return ""
	}
	var user model.User
	if err := r.db.Select("user").Where("id = ?", userID).First(&user).Error; err != nil {
		return ""
	}
	return user.User
}

func (r *Repository) GetUserDefaultsForTunnel(userID int64) (flow int64, num int, expTime int64, flowReset int64, err error) {
	if r == nil || r.db == nil {
		return 0, 0, 0, 0, errors.New("repository not initialized")
	}
	return getUserDefaultsForTunnel(r.db, userID)
}

func (r *Repository) GetUserDefaultsForTunnelTx(tx *gorm.DB, userID int64) (flow int64, num int, expTime int64, flowReset int64, err error) {
	if tx == nil {
		return 0, 0, 0, 0, errors.New("database unavailable")
	}
	return getUserDefaultsForTunnel(tx, userID)
}

func getUserDefaultsForTunnel(db *gorm.DB, userID int64) (flow int64, num int, expTime int64, flowReset int64, err error) {
	var user model.User
	err = db.Select("flow", "num", "exp_time", "flow_reset_time").Where("id = ?", userID).First(&user).Error
	if err != nil {
		return 0, 0, 0, 0, normalizeNotFoundErr(err)
	}
	return user.Flow, user.Num, user.ExpTime, user.FlowResetTime, nil
}

func (r *Repository) CreateNode(name, secret, serverIP string, serverIPV4, serverIPV6, port, interfaceName, version, remark, expiryTime, renewalCycle interface{}, httpFlag, tlsFlag, socksFlag int, now int64, status int, tcpAddr, udpAddr string, inx int, extraIPs interface{}, forwardMode string) (int64, error) {
	if r == nil || r.db == nil {
		return 0, errors.New("repository not initialized")
	}
	node := model.Node{
		Name:          name,
		Remark:        nullStringFromInterface(remark),
		ExpiryTime:    nullInt64FromInterface(expiryTime),
		RenewalCycle:  nullStringFromInterface(renewalCycle),
		Secret:        secret,
		ServerIP:      serverIP,
		ServerIPV4:    nullStringFromInterface(serverIPV4),
		ServerIPV6:    nullStringFromInterface(serverIPV6),
		ExtraIPs:      nullStringFromInterface(extraIPs),
		Port:          stringFromInterface(port),
		InterfaceName: nullStringFromInterface(interfaceName),
		Version:       nullStringFromInterface(version),
		HTTP:          httpFlag,
		TLS:           tlsFlag,
		Socks:         socksFlag,
		CreatedTime:   now,
		UpdatedTime:   sql.NullInt64{Int64: now, Valid: true},
		Status:        status,
		TCPListenAddr: tcpAddr,
		UDPListenAddr: udpAddr,
		Inx:           inx,
		ForwardMode:   defaultNodeForwardMode(forwardMode),
	}
	// O1-002：Create 返回的 node.ID 即新节点 ID，避免按 name+serverIP 反查
	//（并发创建同名同 IP 节点时可能查到错误的节点）。
	if err := r.db.Create(&node).Error; err != nil {
		return 0, err
	}
	return int64(node.ID), nil
}

func (r *Repository) GetNodeStatusFields(nodeID int64) (status, httpFlag, tlsFlag, socksFlag int, err error) {
	if r == nil || r.db == nil {
		return 0, 0, 0, 0, errors.New("repository not initialized")
	}
	var node model.Node
	err = r.db.Select("status", "http", "tls", "socks").Where("id = ?", nodeID).First(&node).Error
	if err != nil {
		return 0, 0, 0, 0, normalizeNotFoundErr(err)
	}
	return node.Status, node.HTTP, node.TLS, node.Socks, nil
}

func (r *Repository) UpdateNode(id int64, name, serverIP string, serverIPV4, serverIPV6, port, interfaceName, extraIPs, remark, expiryTime, renewalCycle interface{}, forwardMode string, httpFlag, tlsFlag, socksFlag int, tcpAddr, udpAddr string, now int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	updates := map[string]interface{}{
		"name":                      name,
		"remark":                    nullStringFromInterface(remark),
		"expiry_time":               nullInt64FromInterface(expiryTime),
		"renewal_cycle":             nullStringFromInterface(renewalCycle),
		"server_ip":                 serverIP,
		"server_ip_v4":              nullStringFromInterface(serverIPV4),
		"server_ip_v6":              nullStringFromInterface(serverIPV6),
		"extra_ips":                 nullStringFromInterface(extraIPs),
		"port":                      stringFromInterface(port),
		"interface_name":            nullStringFromInterface(interfaceName),
		"http":                      httpFlag,
		"tls":                       tlsFlag,
		"socks":                     socksFlag,
		"tcp_listen_addr":           tcpAddr,
		"udp_listen_addr":           udpAddr,
		"updated_time":              sql.NullInt64{Int64: now, Valid: true},
		"expiry_reminder_dismissed": 0,
	}
	if strings.TrimSpace(forwardMode) != "" {
		updates["forward_mode"] = defaultNodeForwardMode(forwardMode)
	}
	return r.db.Model(&model.Node{}).
		Where("id = ?", id).
		Updates(updates).Error
}

// UpdateNodeDisplayGroup 单独更新节点的展示分组标签（纯展示字
// 段）：创建/编辑面按 presence 调用，独立成法避免为一个展示列改
// CreateNode/UpdateNode 的长签名（其调用面含大量测试）。group 已
// 由 handler 归一（trim＋长度校验），空串＝脱组回未分组。
func (r *Repository) UpdateNodeDisplayGroup(id int64, group string) error {
	_, err := r.SetNodeDisplayGroup(id, group)
	return err
}

// SetNodeDisplayGroup 同 UpdateNodeDisplayGroup，但回带受影响行
// 数：批量移入端点逐条聚合回执（M-0051 口径）靠行数区分「节点不
// 存在」（0 行）与写库失败（err），不得把不存在的节点报成成功。
func (r *Repository) SetNodeDisplayGroup(id int64, group string) (int64, error) {
	if r == nil || r.db == nil {
		return 0, errors.New("repository not initialized")
	}
	res := r.db.Model(&model.Node{}).
		Where("id = ?", id).
		Update("display_group", group)
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

// RenameNodeDisplayGroup 分组改名级联：把全部成员节点的标签从
// oldName 改为 newName，回带受影响行数（成员数）。两名均须已由
// handler 归一校验；oldName 无成员时回 0 行、不报错（空组改名只
// 动顺序名单，由 handler 侧处理）。
func (r *Repository) RenameNodeDisplayGroup(oldName, newName string) (int64, error) {
	if r == nil || r.db == nil {
		return 0, errors.New("repository not initialized")
	}
	res := r.db.Model(&model.Node{}).
		Where("display_group = ?", oldName).
		Update("display_group", newName)
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

// ClearNodeDisplayGroup 删除分组的成员回落：把该组全部节点的标
// 签清为空（未分组），回带受影响行数。
func (r *Repository) ClearNodeDisplayGroup(group string) (int64, error) {
	if r == nil || r.db == nil {
		return 0, errors.New("repository not initialized")
	}
	res := r.db.Model(&model.Node{}).
		Where("display_group = ?", group).
		Update("display_group", "")
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

// nodeDisplayGroupOrderConfigKey 是展示分组顺序名单的配置键，与
// handler 包的 nodeGroupOrderConfigKey 同值：名单的新值由 handler
// 序列化为完整 JSON 后整值传入，本包只负责与成员写同事务落库。
const nodeDisplayGroupOrderConfigKey = "node_display_group_order"

// RenameNodeDisplayGroupWithRegistry 分组改名的原子写（S3-2）：顺
// 序名单新值与成员标签级联在同一事务内落库，任一失败整体回滚——
// 名单永不先于标签生效，失败不留「新名空组＋旧名在用」的分叉；
// 且失败后以同名重试不会撞上「新名已在名单」的重名门（旧两步写
// 先落名单正是被该门堵死）。回带级联行数。
func (r *Repository) RenameNodeDisplayGroupWithRegistry(oldName, newName, registryValue string, now int64) (int64, error) {
	if r == nil || r.db == nil {
		return 0, errors.New("repository not initialized")
	}
	var updated int64
	err := r.db.Transaction(func(tx *gorm.DB) error {
		if err := upsertConfigTx(tx, nodeDisplayGroupOrderConfigKey, registryValue, now); err != nil {
			return err
		}
		res := tx.Model(&model.Node{}).
			Where("display_group = ?", oldName).
			Update("display_group", newName)
		if res.Error != nil {
			return res.Error
		}
		updated = res.RowsAffected
		return nil
	})
	if err != nil {
		return 0, err
	}
	return updated, nil
}

// ClearNodeDisplayGroupWithRegistry 删组原子写（S3-2）：名单摘除后
// 的新值与成员回落同事务落库，失败整体回滚、不留分叉，口径同
// RenameNodeDisplayGroupWithRegistry。回带回落行数。
func (r *Repository) ClearNodeDisplayGroupWithRegistry(group, registryValue string, now int64) (int64, error) {
	if r == nil || r.db == nil {
		return 0, errors.New("repository not initialized")
	}
	var updated int64
	err := r.db.Transaction(func(tx *gorm.DB) error {
		if err := upsertConfigTx(tx, nodeDisplayGroupOrderConfigKey, registryValue, now); err != nil {
			return err
		}
		res := tx.Model(&model.Node{}).
			Where("display_group = ?", group).
			Update("display_group", "")
		if res.Error != nil {
			return res.Error
		}
		updated = res.RowsAffected
		return nil
	})
	if err != nil {
		return 0, err
	}
	return updated, nil
}

// CountNodesByDisplayGroup 在用分组标签的成员计数（空标签不计）：
// 分组管理面（重名判定/成员数展示）的权威读面。
func (r *Repository) CountNodesByDisplayGroup() (map[string]int64, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	type groupCount struct {
		DisplayGroup string `gorm:"column:display_group"`
		Count        int64  `gorm:"column:cnt"`
	}
	var rows []groupCount
	if err := r.db.Model(&model.Node{}).
		Select("display_group, COUNT(*) AS cnt").
		Where("display_group <> ''").
		Group("display_group").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(rows))
	for _, row := range rows {
		out[row.DisplayGroup] = row.Count
	}
	return out, nil
}

func defaultNodeForwardMode(mode string) string {
	switch strings.TrimSpace(strings.ToLower(mode)) {
	case "nftables":
		return "nftables"
	default:
		return "agent"
	}
}

func (r *Repository) GetNodeSecret(nodeID int64) (string, error) {
	if r == nil || r.db == nil {
		return "", errors.New("repository not initialized")
	}
	var node model.Node
	err := r.db.Select("secret").Where("id = ?", nodeID).First(&node).Error
	if err != nil {
		return "", normalizeNotFoundErr(err)
	}
	return node.Secret, nil
}

func (r *Repository) GetViteConfigValue(name string) (string, error) {
	if r == nil || r.db == nil {
		return "", errors.New("repository not initialized")
	}
	var cfg model.ViteConfig
	err := r.db.Select("value").Where("name = ?", name).First(&cfg).Error
	if err != nil {
		return "", normalizeNotFoundErr(err)
	}
	return cfg.Value, nil
}

// UpdateNodeOrder 更新单个节点的排序序号，返回受影响行数与错误
// （B04-002：旧实现 `_ = ....Error` 且不看 RowsAffected，对不存在
// 的节点也静默无回执，handler 只能恒报成功）。handler 侧的逐条
// 聚合回执（batchOperationResult 口径）在 mutations.go 的
// nodeUpdateOrder 里补齐——该文件属其他簇，本函数先把契约立住：
// 调用方必须检查返回值，不得再静默丢弃。
func (r *Repository) UpdateNodeOrder(nodeID int64, inx int, now int64) (int64, error) {
	if r == nil || r.db == nil {
		return 0, errors.New("repository not initialized")
	}
	res := r.db.Model(&model.Node{}).
		Where("id = ?", nodeID).
		Updates(map[string]interface{}{
			"inx":          inx,
			"updated_time": sql.NullInt64{Int64: now, Valid: true},
		})
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

// UpdateNodeIdleTimeoutSeconds stores one node's forwarding-plane
// no-progress teardown window (seconds, 0 = disabled). The panel is
// the authority; the value reaches the agent as service metadata at
// the next config generation for the node. Same not-found contract
// as UpdateNodeExpiryReminderDismissed.
func (r *Repository) UpdateNodeIdleTimeoutSeconds(nodeID int64, seconds int) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	res := r.db.Model(&model.Node{}).
		Where("id = ?", nodeID).
		Update("idle_timeout_seconds", seconds)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errors.New("节点不存在")
	}
	return nil
}

func (r *Repository) UpdateNodeExpiryReminderDismissed(nodeID int64, dismissed int) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	res := r.db.Model(&model.Node{}).
		Where("id = ?", nodeID).
		Update("expiry_reminder_dismissed", dismissed)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		// B04-002：GORM 对不存在的行 Update 不报错，旧实现据此对
		// 不存在的节点也报成功。对齐 node/update 口径回「节点不
		// 存在」（handler 的既有 err 分支会原样透出该文案）。
		return errors.New("节点不存在")
	}
	return nil
}

// SoleSegmentTunnel 表示一条隧道在某个段角色（入口/出口）上只剩
// 指定节点一行：删掉该节点后隧道在该角色上再无节点、结构不完整。
type SoleSegmentTunnel struct {
	TunnelID   int64
	TunnelName string
	ChainType  int // 1 = 入口，3 = 出口
}

// ListSoleSegmentTunnelsForNode 返回「该节点是唯一入口 / 唯一出口」
// 的隧道清单（L4-03）。DeleteNodeCascade 会删掉该节点全部
// ChainTunnel 行；此前删节点回执只披露跨节点入站引用
// （brokenReferrers），被删节点是某隧道唯一入口/出口时无任何点名，
// 隧道变残缺只能靠后续 apply/诊断报错显形。本查询供 handler 在级
// 联删除前点名披露（守卫面，与 R3X-015 同口径：查询失败由调用方
// 中止删除）。
func (r *Repository) ListSoleSegmentTunnelsForNode(nodeID int64) ([]SoleSegmentTunnel, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var rows []struct {
		TunnelID  int64  `gorm:"column:tunnel_id"`
		ChainType string `gorm:"column:chain_type"`
	}
	if err := r.db.Model(&model.ChainTunnel{}).
		Select("DISTINCT tunnel_id, chain_type").
		Where("node_id = ? AND chain_type IN ?", nodeID, []string{"1", "3"}).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]SoleSegmentTunnel, 0, len(rows))
	for _, row := range rows {
		var others int64
		if err := r.db.Model(&model.ChainTunnel{}).
			Where("tunnel_id = ? AND chain_type = ? AND node_id <> ?", row.TunnelID, row.ChainType, nodeID).
			Count(&others).Error; err != nil {
			return nil, err
		}
		if others > 0 {
			continue
		}
		chainType := 1
		if row.ChainType == "3" {
			chainType = 3
		}
		var name string
		_ = r.db.Model(&model.Tunnel{}).Select("name").Where("id = ?", row.TunnelID).Scan(&name).Error
		out = append(out, SoleSegmentTunnel{TunnelID: row.TunnelID, TunnelName: name, ChainType: chainType})
	}
	return out, nil
}

func (r *Repository) DeleteNodeCascade(nodeID int64) error {
	_, err := r.DeleteNodeCascadeWithOutcome(nodeID)
	return err
}

// DeleteNodeCascadeWithOutcome 是 DeleteNodeCascade 的结果变体：
// 除级联删除外，把「失去最后一个入口端口而整行删除的 Forward」
// 带回供 handler 在删节点回执中点名（FIX-CASCADE / E1-11）。
func (r *Repository) DeleteNodeCascadeWithOutcome(nodeID int64) (*NodeCascadeOutcome, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	outcome := &NodeCascadeOutcome{}
	err := r.db.Transaction(func(tx *gorm.DB) error {
		// R5-B7: 补全级联删除（C-004~C-008）。
		// 数据面清理由 handler 的 deleteNodeByID 在调用本函数前尽力尝试
		// （B4/F-056：删除其上转发服务 + nftables 节点清表，失败记录日志
		// 不阻断——死节点必须能删）；本函数只负责 DB 行。此前注释声称的
		// 「调用方 pending 流程」并不存在，已按实际改写。
		// FIX-CASCADE（E1-11）：摘除本节点端口行之前先物化受影响的
		// Forward 集——摘除后失去最后一个入口端口的 Forward 必须整
		// 行删除，否则留下零端口僵尸行（列表 inPort 为空、gost 同步
		// 报错、realm 零端点渲染）；多入口 Forward 只摘本节点行。
		var nodeForwardIDs []int64
		if err := tx.Model(&model.ForwardPort{}).Where("node_id = ?", nodeID).
			Distinct("forward_id").Pluck("forward_id", &nodeForwardIDs).Error; err != nil {
			return err
		}
		if err := tx.Where("node_id = ?", nodeID).Delete(&model.ForwardPort{}).Error; err != nil {
			return err
		}
		if len(nodeForwardIDs) > 0 {
			remaining, err := forwardIDsWithRemainingPortsTx(tx, nodeForwardIDs)
			if err != nil {
				return err
			}
			var zeroPortIDs []int64
			for _, fid := range nodeForwardIDs {
				if _, ok := remaining[fid]; !ok {
					zeroPortIDs = append(zeroPortIDs, fid)
				}
			}
			if len(zeroPortIDs) > 0 {
				if err := deleteForwardCascadeSetTx(tx, zeroPortIDs); err != nil {
					return err
				}
				outcome.DeletedForwardIDs = zeroPortIDs
			}
		}
		if err := tx.Where("node_id = ?", nodeID).Delete(&model.ChainTunnel{}).Error; err != nil {
			return err
		}
		if err := deleteNodeKernelsTx(tx, nodeID); err != nil {
			return err
		}
		// C-004: SSH 配置（含私钥）。
		if err := tx.Where("node_id = ?", nodeID).Delete(&model.NodeSSHConfig{}).Error; err != nil {
			return err
		}
		// C-005: nft 绑定与计数器。
		if err := tx.Where("node_id = ?", nodeID).Delete(&model.NftRuleBinding{}).Error; err != nil {
			return err
		}
		if err := tx.Where("node_id = ?", nodeID).Delete(&model.NftCounterState{}).Error; err != nil {
			return err
		}
		// AUDIT-R18 (DAT18-003): NftPendingDelete（node_id），与 DeleteForwardCascade
		// 的 R5-B3 D1-001 补充保持一致，避免孤儿 pending 行触发不必要 reconcile。
		if err := tx.Where("node_id = ?", nodeID).Delete(&model.NftPendingDelete{}).Error; err != nil {
			return err
		}
		// AUDIT-R18 (DAT18-003): 节点侧服务监控（NodeID>0）与监控结果，
		// 删除节点后残留会持续产生"节点不在线"失败记录。
		if err := tx.Where("node_id = ?", nodeID).Delete(&model.ServiceMonitor{}).Error; err != nil {
			return err
		}
		if err := tx.Where("node_id = ?", nodeID).Delete(&model.ServiceMonitorResult{}).Error; err != nil {
			return err
		}
		// C-006: 指标。
		if err := tx.Where("node_id = ?", nodeID).Delete(&model.NodeMetric{}).Error; err != nil {
			return err
		}
		if err := tx.Where("node_id = ?", nodeID).Delete(&model.TunnelMetric{}).Error; err != nil {
			return err
		}
		if err := tx.Where("node_id = ?", nodeID).Delete(&model.NodeTraffic{}).Error; err != nil {
			return err
		}
		// C-008: 订阅计数器。
		if err := tx.Where("node_id = ?", nodeID).Delete(&model.SubscriptionCounter{}).Error; err != nil {
			return err
		}
		// A3-10: 延迟样本与流量序号台账同为按 node_id 的行，一并清理
		// （样本原靠 7 天保留期自愈、node_seq 原永久残留一行/节点）。
		if err := tx.Where("node_id = ?", nodeID).Delete(&model.NodeLatencySample{}).Error; err != nil {
			return err
		}
		if err := tx.Where("node_id = ?", nodeID).Delete(&NodeSeq{}).Error; err != nil {
			return err
		}
		// 流量可靠上报 v2 的流游标没有 node_id 列，按 scope 前缀
		// （flow:<nodeID>:<stream>，见 traffic_stream.go）归属节点，
		// 一并补删，否则节点每次换 stream（重装/自愈）都在本表留
		// 一行永久孤儿。前缀含结尾冒号，节点 12 不会误伤 123。
		if err := tx.Where("scope LIKE ?", fmt.Sprintf("flow:%d:%%", nodeID)).Delete(&TrafficStreamCursor{}).Error; err != nil {
			return err
		}
		// FIX-CASCADE（E1-04 / R2-E3-F01）：legacy 批次去重登记按
		// node_id 归属本节点——此前不在级联内，唯一清理点是该节点
		// 下一次领取时顺带清过期行，节点已删永不触发、永久残留。
		if err := tx.Where("node_id = ?", nodeID).Delete(&FlowLegacyBatchDedupe{}).Error; err != nil {
			return err
		}
		// FIX-CASCADE（E1-05）：vite_config 的待结算标记与孤儿重查
		// 标记都按 node_id 后缀键归属本节点，节点删除后永不再被消
		// 费/收敛，随节点一并删除（NodeUninstall 的 secret 键行是
		// 有意悬置拦截，不在此列）。
		if err := tx.Where("name IN ?", []string{
			pendingRealmSettleConfigKey(nodeID),
			kernelOrphanRecheckKey(nodeID),
		}).Delete(&model.ViteConfig{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", nodeID).Delete(&model.Node{}).Error
	})
	if err != nil {
		return nil, err
	}
	return outcome, nil
}

func (r *Repository) GetNodePortRange(nodeID int64) (string, error) {
	if r == nil || r.db == nil {
		return "", errors.New("repository not initialized")
	}
	var node model.Node
	err := r.db.Select("port").Where("id = ?", nodeID).First(&node).Error
	if err != nil {
		return "", normalizeNotFoundErr(err)
	}
	return node.Port, nil
}

func (r *Repository) TunnelNameExists(name string) (bool, error) {
	if r == nil || r.db == nil {
		return false, errors.New("repository not initialized")
	}
	return r.TunnelNameExistsTx(r.db, name)
}

// TunnelNameExistsTx 是 TunnelNameExists 的事务句柄版（F1）：查询走
// 传入的 tx/r.db 句柄本身。事务闭包内必须用本函数——SQLite
// MaxOpenConns(1) 下事务已占住唯一连接，闭包内再调池版
// TunnelNameExists（走 r.db 另取连接）会无限等待连接释放、自死锁
// （tunnelUpdate 提交段改名复检曾中招，生产 SQLite 部署任何隧道
// 改名都会挂死）。与 NodePortOccupiedForTunnelTx 同口径。
func (r *Repository) TunnelNameExistsTx(tx *gorm.DB, name string) (bool, error) {
	if tx == nil {
		return false, errors.New("database unavailable")
	}
	var cnt int64
	err := tx.Model(&model.Tunnel{}).Where("name = ?", name).Count(&cnt).Error
	return cnt > 0, err
}

func (r *Repository) BeginTx() *gorm.DB {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db.Begin()
}

// UpdateTunnelOrder 更新单个隧道的排序序号，返回受影响行数与错误
// （B06-004：旧实现无返回值且 `_ = ....Error` 吞掉一切错误，不存在
// 的隧道 0 行更新与真实 DB 错误都不可见，handler 只能恒报成功）。
// handler 侧的逐条聚合回执（batchOperationResult 口径）在
// mutations.go 的 tunnelUpdateOrder 里补齐：调用方必须检查返回值，
// 不得再静默丢弃；与 UpdateNodeOrder（B04-002）同口径。
func (r *Repository) UpdateTunnelOrder(tunnelID int64, inx int, now int64) (int64, error) {
	if r == nil || r.db == nil {
		return 0, errors.New("repository not initialized")
	}
	res := r.db.Model(&model.Tunnel{}).
		Where("id = ?", tunnelID).
		Updates(map[string]interface{}{"inx": inx, "updated_time": now})
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

func (r *Repository) UpdateTunnelTx(tx *gorm.DB, tunnelID int64, name string, typeVal int, flow int64, trafficRatio float64, status int, inIP, ipPreference string, protocol string, probeTargetHost string, probeTargetPort int, now int64) error {
	if tx == nil {
		return errors.New("database unavailable")
	}
	return tx.Model(&model.Tunnel{}).
		Where("id = ?", tunnelID).
		Updates(map[string]interface{}{
			"name":              name,
			"type":              typeVal,
			"flow":              flow,
			"traffic_ratio":     trafficRatio,
			"status":            status,
			"in_ip":             nullStringFromInterface(inIP),
			"ip_preference":     ipPreference,
			"protocol":          protocol,
			"probe_target_host": probeTargetHost,
			"probe_target_port": probeTargetPort,
			"updated_time":      now,
		}).Error
}

// SetTunnelEngineTx sets which engine runs a tunnel's forwards.
func (r *Repository) SetTunnelEngineTx(tx *gorm.DB, tunnelID int64, engine string) error {
	if tx == nil {
		return errors.New("database unavailable")
	}
	return tx.Model(&model.Tunnel{}).Where("id = ?", tunnelID).Update("engine", engine).Error
}

func (r *Repository) DeleteChainTunnelsByTunnelTx(tx *gorm.DB, tunnelID int64) error {
	if tx == nil {
		return errors.New("database unavailable")
	}
	return tx.Where("tunnel_id = ?", tunnelID).Delete(&model.ChainTunnel{}).Error
}

func (r *Repository) CreateChainTunnelTx(tx *gorm.DB, tunnelID int64, chainType string, nodeID int64, port sql.NullInt64, strategy string, inx int, protocol string, connectIp string) error {
	if tx == nil {
		return errors.New("database unavailable")
	}
	ct := model.ChainTunnel{
		TunnelID:  tunnelID,
		ChainType: chainType,
		NodeID:    nodeID,
		Port:      port,
		Strategy:  nullStringFromInterface(strategy),
		Inx:       nullInt64FromInterface(inx),
		Protocol:  nullStringFromInterface(protocol),
		ConnectIP: sql.NullString{String: connectIp, Valid: connectIp != ""},
	}
	return tx.Create(&ct).Error
}

func (r *Repository) PickNodePortTx(tx *gorm.DB, nodeID int64, allocated map[int64]int, excludeTunnelID int64) (int, error) {
	return r.pickNodePortTx(tx, nodeID, allocated, excludeTunnelID, false)
}

func (r *Repository) PickRandomNodePortTx(tx *gorm.DB, nodeID int64, allocated map[int64]int, excludeTunnelID int64) (int, error) {
	return r.pickNodePortTx(tx, nodeID, allocated, excludeTunnelID, true)
}

func (r *Repository) pickNodePortTx(tx *gorm.DB, nodeID int64, allocated map[int64]int, excludeTunnelID int64, randomPick bool) (int, error) {
	if tx == nil {
		return 0, errors.New("database unavailable")
	}
	if nodeID <= 0 {
		return 0, errors.New("节点不存在")
	}
	if port, ok := allocated[nodeID]; ok && port > 0 {
		return port, nil
	}

	var node model.Node
	err := tx.Select("port").Where("id = ?", nodeID).First(&node).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, errors.New("节点不存在")
	}
	if err != nil {
		return 0, err
	}

	candidates := parsePortRangeSpec(node.Port)
	if len(candidates) == 0 {
		return 0, errors.New("节点端口已满，无可用端口")
	}

	// P01-001：已用集走统一三源谓词 UsedPortsOnNodeTx。此前这里手写
	// 三源（chain/forward/inbound），漏联邦有效预留口：随机选口能挑中
	// 分享运行时正在用的口，同节点同口双分配、后下发方绑口失败。
	// 排除口径不变：仅排除本隧道自身链路行（更新沿用/重挑时不被
	// 自己的旧行挡住）。
	var pickExcl PortExclusions
	if excludeTunnelID > 0 {
		pickExcl.TunnelIDs = []int64{excludeTunnelID}
	}
	used, err := r.UsedPortsOnNodeTx(tx, nodeID, pickExcl)
	if err != nil {
		return 0, err
	}

	var available []int
	for _, candidate := range candidates {
		if candidate <= 0 {
			continue
		}
		if _, ok := used[candidate]; ok {
			continue
		}
		available = append(available, candidate)
	}

	if len(available) == 0 {
		return 0, errors.New("节点端口已满，无可用端口")
	}
	if !randomPick {
		allocated[nodeID] = available[0]
		return available[0], nil
	}

	idx, err := rand.Int(rand.Reader, big.NewInt(int64(len(available))))
	if err != nil {
		allocated[nodeID] = available[0]
		return available[0], nil
	}
	port := available[idx.Int64()]
	allocated[nodeID] = port
	return port, nil
}

// ListTunnelIPPreferences 批量读取多条隧道的出口 IP 偏好（D6-02）：
// 隧道质量探测原先逐隧道一次查询，一轮 N 条隧道即 N 次；现整轮一次
// IN 查询拿全。缺失的隧道不在结果 map 中（读取方按空偏好处理，与
// GetTunnelIPPreference 的缺失语义一致）。
func (r *Repository) ListTunnelIPPreferences(ids []int64) (map[int64]string, error) {
	out := make(map[int64]string, len(ids))
	if r == nil || r.db == nil || len(ids) == 0 {
		return out, nil
	}
	var rows []model.Tunnel
	// T2-NEW-01：ids 为整轮质量探测的全部隧道集合（整表规模），按块
	// 查避免 IN ? 变量越界；结果进 map 与分块无关。
	for _, chunk := range chunkInt64IDs(ids) {
		var chunkRows []model.Tunnel
		if err := r.db.Select("id", "ip_preference").Where("id IN ?", chunk).Find(&chunkRows).Error; err != nil {
			return nil, err
		}
		rows = append(rows, chunkRows...)
	}
	for _, t := range rows {
		out[t.ID] = t.IPPreference
	}
	return out, nil
}

func (r *Repository) GetTunnelIPPreference(tunnelID int64) string {
	if r == nil || r.db == nil {
		return ""
	}
	var tunnel model.Tunnel
	if err := r.db.Select("ip_preference").Where("id = ?", tunnelID).First(&tunnel).Error; err != nil {
		return ""
	}
	return tunnel.IPPreference
}

func (r *Repository) DeleteTunnelCascade(tunnelID int64) error {
	_, err := r.DeleteTunnelCascadeWithOutcome(tunnelID)
	return err
}

// DeleteTunnelCascadeWithOutcome 是 DeleteTunnelCascade 的结果
// 变体：把墓碑转发的终局/保留分流带回供 handler 回执点名
// （FIX-CASCADE / E1-01）。
func (r *Repository) DeleteTunnelCascadeWithOutcome(tunnelID int64) (*TunnelCascadeOutcome, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var outcome *TunnelCascadeOutcome
	err := r.db.Transaction(func(tx *gorm.DB) error {
		var err error
		outcome, err = deleteTunnelCascadeTx(tx, tunnelID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return outcome, nil
}

func deleteTunnelCascadeTx(tx *gorm.DB, tunnelID int64) (*TunnelCascadeOutcome, error) {
	outcome := &TunnelCascadeOutcome{}
	// AUDIT-R1 (TST-001): 物化 ID 列表。原子查询在 Forward/UserTunnel 行删除后
	// 即失效（子查询变空），而级联清理必须引用已删行的 ID，故先物化。
	var forwardIDs []int64
	if err := tx.Model(&model.Forward{}).Where("tunnel_id = ?", tunnelID).Pluck("id", &forwardIDs).Error; err != nil {
		return nil, err
	}
	var userTunnelIDs []int64
	if err := tx.Model(&model.UserTunnel{}).Where("tunnel_id = ?", tunnelID).Pluck("id", &userTunnelIDs).Error; err != nil {
		return nil, err
	}
	// R1 X-P1-1：pending-delete 墓碑转发（status=2）不随隧道级联删除——
	// 其转发行的端口/计数/绑定与 NftPendingDelete 台账是后台重试清理
	// 的唯一依据，级联清掉会让节点侧规则成无台账孤儿。这类转发只删
	// relay 关联（下方按全部 forwardIDs），其余行保留待重试收敛后由
	// DeleteForwardCascade 终删。
	// FIX-CASCADE（E1-01）：上款的保留只对「仍在重试调度内」的墓
	// 碑成立。台账已耗尽（next_retry_at=0 哨兵）的墓碑已退出调度
	// （ListDue 恒排除），隧道删除后只会以已删 tunnel_id 无限悬
	// 空——这类在本级联内终删并点名
	// （outcome.FinalizedPendingForwardIDs）。仍在活调度内（含台账
	// 行缺失的形状——状态翻转唯一写入点 MarkForwardPendingDelete
	// 与台账建行同事务，生产不可达，K8 契约按保留编码）的保留待
	// 后台重试：节点被删/长离线时重试必失败、24h 总帽耗尽后，若
	// 隧道已删由 retryNftPendingDeletes 在耗尽点补终删（见
	// jobs.go），若隧道后删则由本级联的耗尽分支终删，收敛终点
	// 在两条路径上闭合。
	var pendingForwardIDs []int64
	if err := tx.Model(&model.Forward{}).Where("tunnel_id = ? AND status = ?", tunnelID, model.ForwardStatusPendingDelete).Pluck("id", &pendingForwardIDs).Error; err != nil {
		return nil, err
	}
	pendingSet := make(map[int64]struct{}, len(pendingForwardIDs))
	for _, id := range pendingForwardIDs {
		pendingSet[id] = struct{}{}
	}
	cascadeForwardIDs := make([]int64, 0, len(forwardIDs))
	for _, id := range forwardIDs {
		if _, pending := pendingSet[id]; !pending {
			cascadeForwardIDs = append(cascadeForwardIDs, id)
		}
	}
	if len(pendingForwardIDs) > 0 {
		ledgerRows, err := loadNftPendingDeletesByForwardIDsTx(tx, pendingForwardIDs)
		if err != nil {
			return nil, err
		}
		ledgerByForward := make(map[int64]*model.NftPendingDelete, len(ledgerRows))
		for i := range ledgerRows {
			ledgerByForward[ledgerRows[i].ForwardID] = &ledgerRows[i]
		}
		for _, fid := range pendingForwardIDs {
			row, ok := ledgerByForward[fid]
			terminal := ok && row.NextRetryAt == 0
			if terminal {
				outcome.FinalizedPendingForwardIDs = append(outcome.FinalizedPendingForwardIDs, fid)
				cascadeForwardIDs = append(cascadeForwardIDs, fid)
			} else {
				outcome.RetainedPendingForwardIDs = append(outcome.RetainedPendingForwardIDs, fid)
			}
		}
	}
	// K8/X01-001：物化集（forwardIDs / cascadeForwardIDs /
	// userTunnelIDs）随隧道规模无上界，全部回灌删除走分块助手，
	// 删除顺序与集合语义不变（物化本身因 TST-001 顺序约束保留）。
	if err := deleteByIDSliceInChunksTx(tx, &model.ForwardPort{}, "forward_id", cascadeForwardIDs); err != nil {
		return nil, err
	}
	// AUDIT-R1 (TST-001): NodeInboundRelay / NftCounterState 没有 tunnel_id 列，
	// 它们通过 forward_id 关联本隧道的转发；移到 Forward 删除之前清理。
	// X2-01：删 relay 行前先物化其 id 集合，同步清理指向这些 relay
	// 的 user_inbound 授权行，避免孤儿授权残留。
	relayIDs, err := pluckInboundRelayIDsByColumnTx(tx, "forward_id", forwardIDs)
	if err != nil {
		return nil, err
	}
	if err := deleteUserInboundGrantsByRelayIDsTx(tx, relayIDs); err != nil {
		return nil, err
	}
	if err := deleteByIDSliceInChunksTx(tx, &model.NodeInboundRelay{}, "forward_id", forwardIDs); err != nil {
		return nil, err
	}
	if err := deleteByIDSliceInChunksTx(tx, &model.NftCounterState{}, "forward_id", cascadeForwardIDs); err != nil {
		return nil, err
	}
	if err := deleteByIDSliceInChunksTx(tx, &model.NftRuleBinding{}, "forward_id", cascadeForwardIDs); err != nil {
		return nil, err
	}
	// AUDIT-R18 (DAT18-001): NftPendingDelete 通过 forward_id 关联，与
	// DeleteForwardCascade 的 R5-B3 D1-001 补充保持一致；
	// R1 X-P1-1：活调度墓碑的台账行除外（cascadeForwardIDs 只含
	// 非墓碑 + 已终局墓碑，保留墓碑的台账行不在集内）。
	if err := deleteByIDSliceInChunksTx(tx, &model.NftPendingDelete{}, "forward_id", cascadeForwardIDs); err != nil {
		return nil, err
	}
	if err := tx.Where("tunnel_id = ? AND status <> ?", tunnelID, model.ForwardStatusPendingDelete).Delete(&model.Forward{}).Error; err != nil {
		return nil, err
	}
	// E1-01 终局墓碑的 Forward 本行（上行按 status 排除墓碑，此处
	// 按终局集点删）。
	if err := deleteByIDSliceInChunksTx(tx, &model.Forward{}, "id", outcome.FinalizedPendingForwardIDs); err != nil {
		return nil, err
	}
	// FIX-CASCADE（E1-12）：本级联整行删掉的全部转发同步摘除配额
	// 暂停清单（保留墓碑未删、不摘）。
	if err := prunePausedForwardIDsTx(tx, cascadeForwardIDs); err != nil {
		return nil, err
	}
	if err := tx.Where("tunnel_id = ?", tunnelID).Delete(&model.UserTunnel{}).Error; err != nil {
		return nil, err
	}
	if err := tx.Where("tunnel_id = ?", tunnelID).Delete(&model.ChainTunnel{}).Error; err != nil {
		return nil, err
	}
	// R5-B7 C-009: 补全隧道级联删除（以下模型确有 tunnel_id 列）。
	if err := tx.Where("tunnel_id = ?", tunnelID).Delete(&model.SpeedLimit{}).Error; err != nil {
		return nil, err
	}
	if err := tx.Where("tunnel_id = ?", tunnelID).Delete(&model.TunnelMetric{}).Error; err != nil {
		return nil, err
	}
	// AUDIT-R18 (DAT18-002): 隧道质量探测历史（tunnel_id 索引）。
	if err := tx.Where("tunnel_id = ?", tunnelID).Delete(&model.TunnelQuality{}).Error; err != nil {
		return nil, err
	}
	if err := tx.Where("id = ?", tunnelID).Delete(&model.Tunnel{}).Error; err != nil {
		return nil, err
	}
	return outcome, nil
}

func (r *Repository) TunnelEntryNodeIDs(tunnelID int64) ([]int64, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var ids []int64
	err := r.db.Model(&model.ChainTunnel{}).
		Where("tunnel_id = ? AND chain_type = ?", tunnelID, "1").
		Order("inx ASC, id ASC").
		Pluck("node_id", &ids).Error
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// TunnelIDsByEntryNode returns the tunnel IDs for which nodeID is an entry
// node (chain_type "1"), ordered by tunnel ID. R35-FIX-F01: nodeUpdate
// must refuse a forward-mode flip while the node still serves forwards
// through any of these tunnels.
func (r *Repository) TunnelIDsByEntryNode(nodeID int64) ([]int64, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var ids []int64
	err := r.db.Model(&model.ChainTunnel{}).
		Where("node_id = ? AND chain_type = ?", nodeID, "1").
		Order("tunnel_id ASC").
		Pluck("tunnel_id", &ids).Error
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// DeleteUserTunnel removes a user_tunnel row.
func (r *Repository) DeleteUserTunnel(id int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		return deleteUserTunnelTx(tx, id)
	})
}

func (r *Repository) DeleteUserTunnelTx(tx *gorm.DB, id int64) error {
	if tx == nil {
		return errors.New("database unavailable")
	}
	return deleteUserTunnelTx(tx, id)
}

func deleteUserTunnelTx(tx *gorm.DB, id int64) error {
	return tx.Where("id = ?", id).Delete(&model.UserTunnel{}).Error
}

func (r *Repository) UpdateUserTunnel(id int64, flow int64, num int, expTime, flowResetTime int64, speedID interface{}, status int, flowMiB ...int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Model(&model.UserTunnel{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"flow":            flow,
			"flow_mib":        optionalFlowMiB(flowMiB),
			"num":             num,
			"exp_time":        expTime,
			"flow_reset_time": flowResetTime,
			"speed_id":        nullInt64FromInterface(speedID),
			"status":          status,
		}).Error
}

func (r *Repository) GetUserTunnelUserAndTunnel(id int64) (userID, tunnelID int64, err error) {
	if r == nil || r.db == nil {
		return 0, 0, errors.New("repository not initialized")
	}
	var ut model.UserTunnel
	err = r.db.Select("user_id", "tunnel_id").Where("id = ?", id).First(&ut).Error
	if err != nil {
		return 0, 0, normalizeNotFoundErr(err)
	}
	return ut.UserID, ut.TunnelID, nil
}

func (r *Repository) GetExistingUserTunnel(userID, tunnelID int64) (id int64, flow, num, expTime, flowReset int64, speedID sql.NullInt64, status int, err error) {
	if r == nil || r.db == nil {
		return 0, 0, 0, 0, 0, sql.NullInt64{}, 0, errors.New("repository not initialized")
	}
	return getExistingUserTunnel(r.db, userID, tunnelID)
}

func (r *Repository) GetExistingUserTunnelTx(tx *gorm.DB, userID, tunnelID int64) (id int64, flow, num, expTime, flowReset int64, speedID sql.NullInt64, status int, err error) {
	if tx == nil {
		return 0, 0, 0, 0, 0, sql.NullInt64{}, 0, errors.New("database unavailable")
	}
	return getExistingUserTunnel(tx, userID, tunnelID)
}

func getExistingUserTunnel(db *gorm.DB, userID, tunnelID int64) (id int64, flow, num, expTime, flowReset int64, speedID sql.NullInt64, status int, err error) {
	var ut model.UserTunnel
	err = db.Select("id", "flow", "num", "exp_time", "flow_reset_time", "speed_id", "status").
		Where("user_id = ? AND tunnel_id = ?", userID, tunnelID).
		First(&ut).Error
	if err != nil {
		return 0, 0, 0, 0, 0, sql.NullInt64{}, 0, normalizeNotFoundErr(err)
	}
	return ut.ID, ut.Flow, int64(ut.Num), ut.ExpTime, ut.FlowResetTime, ut.SpeedID, ut.Status, nil
}

func (r *Repository) InsertUserTunnel(userID, tunnelID int64, speedID interface{}, num int, flow, flowResetTime, expTime int64, status int, flowMiB ...int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return insertUserTunnel(r.db, userID, tunnelID, speedID, num, flow, flowResetTime, expTime, status, flowMiB...)
}

func (r *Repository) InsertUserTunnelTx(tx *gorm.DB, userID, tunnelID int64, speedID interface{}, num int, flow, flowResetTime, expTime int64, status int, flowMiB ...int64) error {
	if tx == nil {
		return errors.New("database unavailable")
	}
	return insertUserTunnel(tx, userID, tunnelID, speedID, num, flow, flowResetTime, expTime, status, flowMiB...)
}

func insertUserTunnel(db *gorm.DB, userID, tunnelID int64, speedID interface{}, num int, flow, flowResetTime, expTime int64, status int, flowMiB ...int64) error {
	ut := model.UserTunnel{
		UserID:        userID,
		TunnelID:      tunnelID,
		SpeedID:       nullInt64FromInterface(speedID),
		Num:           num,
		Flow:          flow,
		FlowMiB:       optionalFlowMiB(flowMiB),
		InFlow:        0,
		OutFlow:       0,
		FlowResetTime: flowResetTime,
		// R11X-005: counting starts now; stamp the monthly-reset
		// marker so the row's first reset is its first reset day.
		FlowLastResetTime: unixMilliNow(),
		ExpTime:           expTime,
		Status:            status,
	}
	return db.Create(&ut).Error
}

func (r *Repository) UpdateUserTunnelFields(id int64, speedID interface{}, flow int64, num int, expTime, flowResetTime int64, status int, flowMiB ...int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return updateUserTunnelFields(r.db, id, speedID, flow, num, expTime, flowResetTime, status, flowMiB...)
}

func (r *Repository) UpdateUserTunnelFieldsTx(tx *gorm.DB, id int64, speedID interface{}, flow int64, num int, expTime, flowResetTime int64, status int, flowMiB ...int64) error {
	if tx == nil {
		return errors.New("database unavailable")
	}
	return updateUserTunnelFields(tx, id, speedID, flow, num, expTime, flowResetTime, status, flowMiB...)
}

func updateUserTunnelFields(db *gorm.DB, id int64, speedID interface{}, flow int64, num int, expTime, flowResetTime int64, status int, flowMiB ...int64) error {
	return db.Model(&model.UserTunnel{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"speed_id":        nullInt64FromInterface(speedID),
			"flow":            flow,
			"flow_mib":        optionalFlowMiB(flowMiB),
			"num":             num,
			"exp_time":        expTime,
			"flow_reset_time": flowResetTime,
			"status":          status,
		}).Error
}

func (r *Repository) GetMinForwardPort(forwardID int64) sql.NullInt64 {
	if r == nil || r.db == nil {
		return sql.NullInt64{}
	}
	var p sql.NullInt64
	_ = r.db.Model(&model.ForwardPort{}).
		Select("MIN(port)").
		Where("forward_id = ?", forwardID).
		Scan(&p).Error
	return p
}

func (r *Repository) UpdateForward(id int64, name string, tunnelID int64, remoteAddr, strategy string, now int64, speedID interface{}, maxConn int, ipMaxConn int, ipSpeedID interface{}, proxyProtocol int, proxyProtocolReceive int, proxyProtocolSend int) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	_, proxyProtocolSend = normalizeForwardProxyProtocol(proxyProtocol, proxyProtocolReceive, proxyProtocolSend)
	return r.db.Model(&model.Forward{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"name":                   name,
			"tunnel_id":              tunnelID,
			"remote_addr":            remoteAddr,
			"strategy":               strategy,
			"speed_id":               nullInt64FromInterface(speedID),
			"max_conn":               maxConn,
			"ip_max_conn":            ipMaxConn,
			"ip_speed_id":            nullInt64FromInterface(ipSpeedID),
			"proxy_protocol":         proxyProtocol,
			"proxy_protocol_receive": proxyProtocolReceive,
			"proxy_protocol_send":    proxyProtocolSend,
			"updated_time":           now,
		}).Error
}

// UpdateForwardOrder 更新单个转发的排序序号并返回错误（B08-002：
// 旧实现 `_ = ....Error` 吞掉写库错误，handler 丢弃返回值后 DB 写
// 失败仍报成功）。handler（forwardUpdateOrder）已改两段式：先全量
// 校验归属/存在性、全部通过后才逐条调用本函数并检查返回值。
func (r *Repository) UpdateForwardOrder(forwardID int64, inx int, now int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Model(&model.Forward{}).
		Where("id = ?", forwardID).
		Updates(map[string]interface{}{"inx": inx, "updated_time": now}).Error
}

func (r *Repository) UpdateForwardTunnel(forwardID, tunnelID int64, now int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return updateForwardTunnelTx(r.db, forwardID, tunnelID, now)
}

func updateForwardTunnelTx(db *gorm.DB, forwardID, tunnelID int64, now int64) error {
	return db.Model(&model.Forward{}).
		Where("id = ?", forwardID).
		Updates(map[string]interface{}{"tunnel_id": tunnelID, "updated_time": now}).Error
}

func (r *Repository) DeleteForwardCascade(forwardID int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	// 实现收口到 deleteForwardCascadeSetTx（单元素集）：nft 三表
	// （R5-B3 D1-001 补充 A.3）、ForwardPort、relay 物化集授权清理
	// （A3-03/X2-01）与 Forward 本行同口径，FIX-CASCADE 起另含
	// UserQuota.PausedForwardIDs 摘除（E1-12）。
	return r.db.Transaction(func(tx *gorm.DB) error {
		return deleteForwardCascadeSetTx(tx, []int64{forwardID})
	})
}

// lockPortAllocForEntries 按 nodeID 升序获取 entries 涉及节点的
// per-node 端口分配锁（去重、逆序释放），供端口替换类写路径共用。
func lockPortAllocForEntries(entries []struct {
	NodeID int64
	Port   int
	InIP   string
}) func() {
	nodeIDs := make([]int64, 0, len(entries))
	seen := make(map[int64]struct{})
	for _, e := range entries {
		if _, ok := seen[e.NodeID]; !ok {
			seen[e.NodeID] = struct{}{}
			nodeIDs = append(nodeIDs, e.NodeID)
		}
	}
	sort.Slice(nodeIDs, func(i, j int) bool { return nodeIDs[i] < nodeIDs[j] })
	locks := make([]*sync.Mutex, 0, len(nodeIDs))
	for _, nodeID := range nodeIDs {
		locks = append(locks, AcquirePortAllocLock(nodeID))
	}
	return func() {
		for i := len(locks) - 1; i >= 0; i-- {
			ReleasePortAllocLock(locks[i])
		}
	}
}

// replaceForwardPortsTx 是 ReplaceForwardPorts 的事务内核心：删旧口、
// 锁内三源占用复检（B4/F-013；handler 预检在锁外存在「先查后写」
// 窗口，旧端口已在本事务内删除、自占用天然排除）、插入新口。
func (r *Repository) replaceForwardPortsTx(tx *gorm.DB, forwardID int64, entries []struct {
	NodeID int64
	Port   int
	InIP   string
}) error {
	// R2-05-01: 入口按节点去重（保首现序）。存量重复 chain_type=1
	// 行使 TunnelEntryNodeIDs 返回重复节点、三处消费点据此逐行建
	// 条目时，下方占用复检排除转发自身，同一调用内的重复条目互
	// 不可见，会为同一转发写出重复 forward_port 行（模型无唯一
	// 索引兜底）。与 CreateForwardTx 的 B4-04 去重同形态，但收口
	// 在本事务核心一处，三消费点（ReplaceForwardPorts /
	// ChangeForwardTunnelAndPorts / handler 替换面）共用。
	seenNodes := make(map[int64]struct{}, len(entries))
	uniqueEntries := make([]struct {
		NodeID int64
		Port   int
		InIP   string
	}, 0, len(entries))
	for _, e := range entries {
		if _, dup := seenNodes[e.NodeID]; dup {
			continue
		}
		seenNodes[e.NodeID] = struct{}{}
		uniqueEntries = append(uniqueEntries, e)
	}
	entries = uniqueEntries
	if err := tx.Where("forward_id = ?", forwardID).Delete(&model.ForwardPort{}).Error; err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}
	for _, e := range entries {
		occupied, err := r.HasOtherForwardOnNodePortTx(tx, e.NodeID, e.Port, forwardID)
		if err != nil {
			return err
		}
		if occupied {
			return errPortInUse
		}
	}
	rows := make([]model.ForwardPort, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, model.ForwardPort{
			ForwardID: forwardID,
			NodeID:    e.NodeID,
			Port:      e.Port,
			InIP:      sql.NullString{String: e.InIP, Valid: e.InIP != ""},
		})
	}
	return tx.Create(&rows).Error
}

// ChangeForwardTunnelAndPorts X-P3-17（L-04-3）：批量换隧道的逐条
// DB 写收敛为单事务——换隧道与换端口同 tx 提交，占用集在锁内以本
// tx 复检（预取口径与 handler 校验环一致）。旧实现两次独立提交
// （UpdateForwardTunnel 后 ReplaceForwardPorts），中间态靠 handler
// 补偿回滚兜底且补偿失败被静默吞；收敛后 DB 段任一步失败整条回滚、
// 无中间态，handler 补偿仅保留给提交后的运行时下发失败。
func (r *Repository) ChangeForwardTunnelAndPorts(forwardID, tunnelID int64, remoteAddr string, entries []struct {
	NodeID int64
	Port   int
	InIP   string
}, now int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	release := lockPortAllocForEntries(entries)
	defer release()
	err := r.db.Transaction(func(tx *gorm.DB) error {
		if err := updateForwardTunnelTx(tx, forwardID, tunnelID, now); err != nil {
			return err
		}
		// 集成收口（批次 3 X-P2-4 ② 与批次 8 X-P3-17 同段合并）：托管
		// 中转的 RemoteAddr 重算与换隧道/换端口同 tx 落库，未重算时
		// 调用方传 effectiveRemoteAddr（= 现值），写回同值无副作用。
		if err := tx.Model(&model.Forward{}).Where("id = ?", forwardID).Update("remote_addr", remoteAddr).Error; err != nil {
			return err
		}
		return r.replaceForwardPortsTx(tx, forwardID, entries)
	})
	if err != nil {
		// 唯一冲突翻译为中文友好错误（与 ReplaceForwardPorts 一致）。
		return mapUniqueViolation(err, "port")
	}
	return nil
}

func (r *Repository) ReplaceForwardPorts(forwardID int64, entries []struct {
	NodeID int64
	Port   int
	InIP   string
}) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	// O2-004：加 per-node 端口分配锁（与 CreateForwardTx 的 R5-B4 模式一致）。
	// 旧逻辑未加锁，并发更新端口时靠唯一索引兜底，错误不友好。
	release := lockPortAllocForEntries(entries)
	defer release()
	err := r.db.Transaction(func(tx *gorm.DB) error {
		return r.replaceForwardPortsTx(tx, forwardID, entries)
	})
	if err != nil {
		// O2-004：唯一冲突翻译为中文友好错误（与 CreateForwardTx 的 R5-B4 C-002 一致）。
		return mapUniqueViolation(err, "port")
	}
	return nil
}

func (r *Repository) UpdateForwardPortBindIP(forwardID, nodeID int64, port int, inIP string) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	if forwardID <= 0 || nodeID <= 0 || port <= 0 {
		return nil
	}
	return r.db.Model(&model.ForwardPort{}).
		Where("forward_id = ? AND node_id = ? AND port = ?", forwardID, nodeID, port).
		Update("in_ip", sql.NullString{String: inIP, Valid: strings.TrimSpace(inIP) != ""}).Error
}

func (r *Repository) RollbackForwardFields(id, userID int64, userName, name string, tunnelID int64, remoteAddr, strategy string, status int, speedID interface{}, maxConn int, ipMaxConn int, ipSpeedID interface{}, proxyProtocol int, proxyProtocolReceive int, proxyProtocolSend int, now int64) {
	if r == nil || r.db == nil {
		return
	}
	_, proxyProtocolSend = normalizeForwardProxyProtocol(proxyProtocol, proxyProtocolReceive, proxyProtocolSend)
	_ = r.db.Model(&model.Forward{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"user_id":                userID,
			"user_name":              userName,
			"name":                   name,
			"tunnel_id":              tunnelID,
			"remote_addr":            remoteAddr,
			"strategy":               strategy,
			"status":                 status,
			"speed_id":               nullInt64FromInterface(speedID),
			"max_conn":               maxConn,
			"ip_max_conn":            ipMaxConn,
			"ip_speed_id":            nullInt64FromInterface(ipSpeedID),
			"proxy_protocol":         proxyProtocol,
			"proxy_protocol_receive": proxyProtocolReceive,
			"proxy_protocol_send":    proxyProtocolSend,
			"updated_time":           now,
		}).Error
}

func (r *Repository) GetUsedPortsOnNodeAsMap(nodeID int64) (map[int]bool, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	// P01-001：三源已用集收敛到统一谓词 UsedPortsOnNodeTx，本函数只做
	// map[int]struct{} → map[int]bool 的形状适配。B4/F-013（入站口纳入
	// 自动选口排除集）与 X-P2-13（联邦有效预留口纳入）的结论由统一
	// 谓词承载；external 入站行 node_id=0 天然不在本节点集合内。
	set, err := r.UsedPortsOnNodeTx(r.db, nodeID, PortExclusions{})
	if err != nil {
		return nil, err
	}
	used := make(map[int]bool, len(set))
	for p := range set {
		used[p] = true
	}
	return used, nil
}

func (r *Repository) CreateSpeedLimit(name string, speed int, now int64, status int) (int64, error) {
	if r == nil || r.db == nil {
		return 0, errors.New("repository not initialized")
	}
	sl := model.SpeedLimit{
		Name:        name,
		Speed:       speed,
		TunnelID:    sql.NullInt64{Int64: 0, Valid: false},
		TunnelName:  sql.NullString{String: "", Valid: false},
		CreatedTime: now,
		UpdatedTime: sql.NullInt64{Int64: now, Valid: true},
		Status:      status,
	}
	if err := r.db.Create(&sl).Error; err != nil {
		return 0, err
	}
	return sl.ID, nil
}

func (r *Repository) UpdateSpeedLimit(id int64, name string, speed int, status int, now int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	updates := map[string]interface{}{
		"name":        name,
		"speed":       speed,
		"status":      status,
		"tunnel_id":   nil,
		"tunnel_name": nil,
		"updated_time": sql.NullInt64{
			Int64: now,
			Valid: true,
		},
	}
	return r.db.Model(&model.SpeedLimit{}).
		Where("id = ?", id).
		Updates(updates).Error
}

func (r *Repository) DeleteSpeedLimit(id int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	// R2-E1-03：删除与引用剥离同事务。此前为裸删，删后
	// forward.speed_id / forward.ip_speed_id / user_tunnel.speed_id
	// 三处仍以悬空 id 指向已删规则，消费面 LEFT JOIN 使限速静默
	// 失效且无点名——与备份导入尾 sweepDanglingSpeedRefsTx 同
	// 口径置 NULL（引用行本身保留）。数据面回退重下发由 handler
	// 删除前枚举引用集、删除后 resync 承担（R6X-017）。
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("id = ?", id).Delete(&model.SpeedLimit{}).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.Forward{}).Where("speed_id = ?", id).Update("speed_id", nil).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.Forward{}).Where("ip_speed_id = ?", id).Update("ip_speed_id", nil).Error; err != nil {
			return err
		}
		return tx.Model(&model.UserTunnel{}).Where("speed_id = ?", id).Update("speed_id", nil).Error
	})
}

// RevokedUserTunnelPair holds the (userID, tunnelID) of a deleted user_tunnel row,
// so the handler layer can clean up associated forwarding rules.
type RevokedUserTunnelPair struct {
	UserID   int64
	TunnelID int64
}

func (r *Repository) CreateForwardTx(userID int64, userName, name string, tunnelID int64, remoteAddr, strategy string, now int64, inx int, entryNodeIDs []int64, port int, inIp string, speedID interface{}, maxConn int, ipMaxConn int, ipSpeedID interface{}, proxyProtocol int, proxyProtocolReceive int, proxyProtocolSend int) (int64, error) {
	if r == nil || r.db == nil {
		return 0, errors.New("repository not initialized")
	}
	// B4-04：入口节点先去重再加锁/落库。恢复面可导入同节点重复
	// 的入口行（importTunnels 不去重、TunnelEntryNodeIDs 无
	// DISTINCT），未去重的列表逐节点取不可重入的端口锁会自死锁
	// （同 goroutine 第二次取同一把锁永等），且会写出重复的
	// forward_port 行。去重保首现序，事务内的占口复检与端口行
	// 插入同样走去重后的列表。
	seenNodes := make(map[int64]struct{}, len(entryNodeIDs))
	uniqueEntryNodeIDs := make([]int64, 0, len(entryNodeIDs))
	for _, nodeID := range entryNodeIDs {
		if _, dup := seenNodes[nodeID]; dup {
			continue
		}
		seenNodes[nodeID] = struct{}{}
		uniqueEntryNodeIDs = append(uniqueEntryNodeIDs, nodeID)
	}
	// R5-B4: per-node 端口分配锁（事务之前获取，排序后加锁防死锁）。
	// 锁内禁止网络调用。
	sortedNodes := append([]int64(nil), uniqueEntryNodeIDs...)
	sort.Slice(sortedNodes, func(i, j int) bool { return sortedNodes[i] < sortedNodes[j] })
	locks := make([]*sync.Mutex, 0, len(sortedNodes))
	for _, nodeID := range sortedNodes {
		locks = append(locks, AcquirePortAllocLock(nodeID))
	}
	defer func() {
		for i := len(locks) - 1; i >= 0; i-- {
			ReleasePortAllocLock(locks[i])
		}
	}()
	_, proxyProtocolSend = normalizeForwardProxyProtocol(proxyProtocol, proxyProtocolReceive, proxyProtocolSend)
	var forwardID int64
	err := r.db.Transaction(func(tx *gorm.DB) error {
		// F2-05: 条数配额的权威复检在事务内。先以 FOR UPDATE 锁
		// 用户行串行化同用户的并发创建（SQLite 方言自动忽略 FOR
		// 子句、MaxOpenConns(1) 本就串行，与 F-S01 同口径；PG
		// Read Committed 下后到事务阻塞至先行提交、重读已提交计
		// 数），再按 handler 预检（ensureUserTunnelForwardAllowed）
		// 同口径复检用户级 Num 与 (用户,隧道) 级 Num。预检保留作
		// 快速失败，但并发正确性只由这里保证：基线预检在事务外，
		// Num=1 并发 10 发可成 9（live-c 实测）。
		var userRow model.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", userID).First(&userRow).Error; err != nil {
			return err
		}
		if userRow.Num > 0 {
			var cnt int64
			if err := tx.Model(&model.Forward{}).
				Where("user_id = ? AND status = 1", userID).Count(&cnt).Error; err != nil {
				return err
			}
			if cnt >= int64(userRow.Num) {
				return ErrForwardNumExceeded
			}
		}
		var utRow model.UserTunnel
		utErr := tx.Where("user_id = ? AND tunnel_id = ?", userID, tunnelID).
			Order("id ASC").First(&utRow).Error
		if utErr != nil && !errors.Is(utErr, gorm.ErrRecordNotFound) {
			return utErr
		}
		if utErr == nil && utRow.Num > 0 {
			var cnt int64
			if err := tx.Model(&model.Forward{}).
				Where("user_id = ? AND tunnel_id = ? AND status = 1", userID, tunnelID).Count(&cnt).Error; err != nil {
				return err
			}
			if cnt >= int64(utRow.Num) {
				return ErrUserTunnelForwardNumExceeded
			}
		}
		// B4/F-013: 锁内三源复检（同 ReplaceForwardPorts）。handler 预检
		// 在锁外，跨表并发（入站/隧道创建）可穿透窗口双占用落库。
		for _, nodeID := range uniqueEntryNodeIDs {
			occupied, err := r.HasOtherForwardOnNodePortTx(tx, nodeID, port, 0)
			if err != nil {
				return err
			}
			if occupied {
				return errPortInUse
			}
		}
		fwd := model.Forward{
			UserID:               userID,
			UserName:             userName,
			Name:                 name,
			TunnelID:             tunnelID,
			RemoteAddr:           remoteAddr,
			Strategy:             strategy,
			InFlow:               0,
			OutFlow:              0,
			CreatedTime:          now,
			UpdatedTime:          now,
			Status:               1,
			Inx:                  inx,
			MaxConn:              maxConn,
			SpeedID:              nullInt64FromInterface(speedID),
			IPMaxConn:            ipMaxConn,
			IPSpeedID:            nullInt64FromInterface(ipSpeedID),
			ProxyProtocol:        proxyProtocol,
			ProxyProtocolReceive: proxyProtocolReceive,
			ProxyProtocolSend:    proxyProtocolSend,
		}
		if err := tx.Create(&fwd).Error; err != nil {
			return err
		}
		forwardID = fwd.ID
		for _, nodeID := range uniqueEntryNodeIDs {
			fp := model.ForwardPort{
				ForwardID: forwardID,
				NodeID:    nodeID,
				Port:      port,
				InIP:      sql.NullString{String: inIp, Valid: inIp != ""},
			}
			if err := tx.Create(&fp).Error; err != nil {
				return err
			}
		}
		return nil
	})
	// R5-B4 C-002: 唯一约束冲突 → "端口已被占用"，不得 500。
	if err != nil {
		err = mapUniqueViolation(err, "port")
	}
	return forwardID, err
}

// InsertTunnelTx 是 tunnelCreate 提交段的落库函数（R2X-049 补批13）：
// 在提交事务内先复检同名、再建行并回填 ID。tunnelCreate 的第一道
// 检查在任何锁/事务之外，其后隔着验证段与可达秒级的联邦 HTTP 才到
// 提交段，无复检时并发同名可双建；tunnel 表无任何唯一索引，复检是
// 唯一防线（裁定明确不加 uniqueIndex：N-29 迁移口径 + 存量重名
// 未预检）。
// X01-001：与 CreateUserTx 同族——PG 下复检前先按隧道名取事务级
// 咨询锁串行化同名并发（见 pg_name_lock.go）。
func (r *Repository) InsertTunnelTx(tx *gorm.DB, tunnel *model.Tunnel) error {
	if tx == nil || tunnel == nil {
		return errors.New("database unavailable")
	}
	if err := lockNameForTx(tx, nameLockScopeTunnel, tunnel.Name); err != nil {
		return err
	}
	var dupCnt int64
	if err := tx.Model(&model.Tunnel{}).Where("name = ?", tunnel.Name).Count(&dupCnt).Error; err != nil {
		return err
	}
	if dupCnt > 0 {
		return ErrTunnelNameDuplicate
	}
	// R4X-010(a)：TrafficRatio 带 GORM `default:1.0`，显式 0 会在 Create
	// 时被吞成 1——意图值须在 Create 前捕获、落库后写回（与备份导入的
	// restoreZeroValuedFloat 同型），显式 0 倍率（零计费）才落得下去。
	ratioIntent := tunnel.TrafficRatio
	if err := tx.Create(tunnel).Error; err != nil {
		return err
	}
	return restoreZeroValuedFloat(tx, &model.Tunnel{}, tunnel.ID, "traffic_ratio", ratioIntent)
}

func (r *Repository) CreateTunnelTx(tx *gorm.DB, name string, trafficRatio float64, typeVal int, flow int64, now int64, status int, inIP interface{}, inx int, ipPreference string, probeTargetHost string, probeTargetPort int) (int64, error) {
	inIPVal := nullStringFromInterface(inIP)
	tunnel := model.Tunnel{
		Name:            name,
		TrafficRatio:    trafficRatio,
		Type:            typeVal,
		Protocol:        "tls",
		Flow:            flow,
		CreatedTime:     now,
		UpdatedTime:     now,
		Status:          status,
		InIP:            inIPVal,
		Inx:             inx,
		IPPreference:    ipPreference,
		ProbeTargetHost: probeTargetHost,
		ProbeTargetPort: probeTargetPort,
	}
	// R4X-010(a)：同 InsertTunnelTx——显式 0 倍率不得被 default:1.0 吞掉。
	if err := tx.Create(&tunnel).Error; err != nil {
		return 0, err
	}
	if err := restoreZeroValuedFloat(tx, &model.Tunnel{}, tunnel.ID, "traffic_ratio", trafficRatio); err != nil {
		return 0, err
	}
	return tunnel.ID, nil
}

func normalizeNotFoundErr(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return sql.ErrNoRows
	}
	return err
}

func nullStringFromInterface(v interface{}) sql.NullString {
	switch t := v.(type) {
	case nil:
		return sql.NullString{}
	case sql.NullString:
		return t
	case *sql.NullString:
		if t == nil {
			return sql.NullString{}
		}
		return *t
	case string:
		if t == "" {
			return sql.NullString{}
		}
		return sql.NullString{String: t, Valid: true}
	case *string:
		if t == nil || *t == "" {
			return sql.NullString{}
		}
		return sql.NullString{String: *t, Valid: true}
	default:
		return sql.NullString{}
	}
}

func nullInt64FromInterface(v interface{}) sql.NullInt64 {
	switch t := v.(type) {
	case nil:
		return sql.NullInt64{}
	case sql.NullInt64:
		return t
	case *sql.NullInt64:
		if t == nil {
			return sql.NullInt64{}
		}
		return *t
	case int64:
		return sql.NullInt64{Int64: t, Valid: true}
	case *int64:
		if t == nil {
			return sql.NullInt64{}
		}
		return sql.NullInt64{Int64: *t, Valid: true}
	case int:
		return sql.NullInt64{Int64: int64(t), Valid: true}
	case int32:
		return sql.NullInt64{Int64: int64(t), Valid: true}
	case int16:
		return sql.NullInt64{Int64: int64(t), Valid: true}
	case int8:
		return sql.NullInt64{Int64: int64(t), Valid: true}
	case uint64:
		return sql.NullInt64{Int64: int64(t), Valid: true}
	case uint:
		return sql.NullInt64{Int64: int64(t), Valid: true}
	case uint32:
		return sql.NullInt64{Int64: int64(t), Valid: true}
	case uint16:
		return sql.NullInt64{Int64: int64(t), Valid: true}
	case uint8:
		return sql.NullInt64{Int64: int64(t), Valid: true}
	case float64:
		return sql.NullInt64{Int64: int64(t), Valid: true}
	default:
		return sql.NullInt64{}
	}
}

func stringFromInterface(v interface{}) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case sql.NullString:
		if t.Valid {
			return t.String
		}
		return ""
	case *sql.NullString:
		if t != nil && t.Valid {
			return t.String
		}
		return ""
	case []byte:
		return string(t)
	default:
		return ""
	}
}

func parsePortRangeSpec(input string) []int {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil
	}
	set := make(map[int]struct{})
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
			start, err1 := strconv.Atoi(strings.TrimSpace(r[0]))
			end, err2 := strconv.Atoi(strings.TrimSpace(r[1]))
			if err1 != nil || err2 != nil || start <= 0 || end <= 0 {
				continue
			}
			if end < start {
				start, end = end, start
			}
			// 复扫 F-PORT-01：端口范围串无上限钳制——"1-99999" 会把
			// >65535 的端口发给分配器，最终只能在 agent 绑定时失败；
			// 与单值端口的 1–65535 校验同口径，区间上界钳到 65535。
			if start > 65535 {
				continue
			}
			if end > 65535 {
				end = 65535
			}
			for p := start; p <= end; p++ {
				set[p] = struct{}{}
			}
			continue
		}
		p, err := strconv.Atoi(part)
		if err != nil || p <= 0 || p > 65535 {
			continue
		}
		set[p] = struct{}{}
	}
	out := make([]int, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Ints(out)
	return out
}

// validateIDsExistTx is the generic batched existence check (R2X-012):
// every positive referenced ID must exist in target's table (one IN query);
// any missing ID fails the whole write before anything is persisted,
// matching the X-P3-5 / subscription-assignment validation standard.
func validateIDsExistTx(tx *gorm.DB, target interface{}, label string, ids []int64) error {
	seen := make(map[int64]struct{})
	uniq := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		uniq = append(uniq, id)
	}
	if len(uniq) == 0 {
		return nil
	}
	var existing []int64
	// T2-NEW-01：uniq 来自请求载荷、无条数上限，按块查避免 IN ?
	// 变量越界（跨块累加进同一集合，与单查等价；下方缺失判定仍按
	// uniq 原序逐个核对，错误文案不变）。
	for _, chunk := range chunkInt64IDs(uniq) {
		var chunkExisting []int64
		if err := tx.Model(target).Where("id IN ?", chunk).Pluck("id", &chunkExisting).Error; err != nil {
			return err
		}
		existing = append(existing, chunkExisting...)
	}
	existingSet := make(map[int64]struct{}, len(existing))
	for _, id := range existing {
		existingSet[id] = struct{}{}
	}
	missing := make([]int64, 0)
	for _, id := range uniq {
		if _, ok := existingSet[id]; !ok {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%s: %v", label, missing)
	}
	return nil
}

// validateKindTarget maps a ValidateIDsExist kind to its model and the
// label used in the missing-ID error.
func validateKindTarget(kind string) (interface{}, string, error) {
	switch kind {
	case "user":
		return &model.User{}, "用户不存在", nil
	case "tunnel":
		return &model.Tunnel{}, "隧道不存在", nil
	case "inbound":
		return &model.NodeInbound{}, "入站不存在", nil
	case "subscription":
		return &model.Subscription{}, "订阅不存在", nil
	default:
		return nil, "", fmt.Errorf("unknown existence-check kind %q", kind)
	}
}

// ValidateIDsExistTx validates referenced IDs of the given kind inside the
// caller's transaction (R2X-012). Kinds: user, tunnel, inbound,
// subscription.
func (r *Repository) ValidateIDsExistTx(tx *gorm.DB, kind string, ids []int64) error {
	if tx == nil {
		return errors.New("database unavailable")
	}
	target, label, err := validateKindTarget(kind)
	if err != nil {
		return err
	}
	return validateIDsExistTx(tx, target, label, ids)
}

// ValidateIDsExist is the non-transactional variant of ValidateIDsExistTx,
// for handlers that validate before opening their write transaction.
func (r *Repository) ValidateIDsExist(kind string, ids []int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	target, label, err := validateKindTarget(kind)
	if err != nil {
		return err
	}
	return validateIDsExistTx(r.db, target, label, ids)
}

func (r *Repository) AdvanceNodeRenewalCycles(now int64) (int, error) {
	if r == nil || r.db == nil {
		return 0, nil
	}

	var nodes []model.Node
	if err := r.db.Where("renewal_cycle IS NOT NULL AND renewal_cycle != '' AND expiry_time IS NOT NULL").Find(&nodes).Error; err != nil {
		return 0, fmt.Errorf("list nodes with renewal cycle: %w", err)
	}

	advanced := 0
	for _, node := range nodes {
		if !node.ExpiryTime.Valid || node.ExpiryTime.Int64 <= 0 {
			continue
		}

		cycleMonths := 0
		switch node.RenewalCycle.String {
		case "month":
			cycleMonths = 1
		case "quarter":
			cycleMonths = 3
		case "year":
			cycleMonths = 12
		default:
			continue
		}

		anchorTime := node.ExpiryTime.Int64
		for anchorTime <= now {
			nextAnchor := advanceByMonths(anchorTime, cycleMonths)
			if nextAnchor <= anchorTime {
				break
			}
			anchorTime = nextAnchor
		}

		if anchorTime == node.ExpiryTime.Int64 {
			continue
		}

		if err := r.db.Model(&model.Node{}).Where("id = ?", node.ID).Update("expiry_time", anchorTime).Error; err != nil {
			continue
		}
		advanced++
	}

	return advanced, nil
}

func advanceByMonths(timestamp int64, months int) int64 {
	t := time.Unix(timestamp/1000, 0)
	next := t.AddDate(0, months, 0)
	return next.UnixMilli()
}

func (r *Repository) ListTunnelChainNodeIDsTx(tx *gorm.DB, tunnelID int64) ([]int64, error) {
	var ids []int64
	err := tx.Model(&model.ChainTunnel{}).Where("tunnel_id = ?", tunnelID).Pluck("node_id", &ids).Error
	return ids, err
}
