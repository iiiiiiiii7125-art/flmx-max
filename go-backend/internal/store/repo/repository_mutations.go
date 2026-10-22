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

	"go-backend/internal/store/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (r *Repository) UserExists(username string) (bool, error) {
	if r == nil || r.db == nil {
		return false, errors.New("repository not initialized")
	}
	var cnt int64
	err := r.db.Model(&model.User{}).Where(`"user" = ?`, username).Count(&cnt).Error
	return cnt > 0, err
}

func (r *Repository) UserExistsExcluding(username string, excludeID int64) (bool, error) {
	if r == nil || r.db == nil {
		return false, errors.New("repository not initialized")
	}
	var cnt int64
	err := r.db.Model(&model.User{}).
		Where(`"user" = ? AND id != ?`, username, excludeID).
		Count(&cnt).Error
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

func (r *Repository) UpdateUserWithPassword(id int64, username, pwdHash string, flow int64, num int, expTime, flowResetTime int64, status, maxConn int, now int64, flowMiB ...int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	err := r.db.Model(&model.User{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
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
		}).Error
	// R5-B4C: 并发兜底（handler 已查重，但有竞态窗口）。
	return mapUniqueViolation(err, "username")
}

func (r *Repository) UpdateUserWithoutPassword(id int64, username string, flow int64, num int, expTime, flowResetTime int64, status, maxConn int, now int64, flowMiB ...int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	err := r.db.Model(&model.User{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"user":            username,
			"flow":            flow,
			"flow_mib":        optionalFlowMiB(flowMiB),
			"num":             num,
			"exp_time":        expTime,
			"flow_reset_time": flowResetTime,
			"status":          status,
			"max_conn":        maxConn,
			"updated_time":    sql.NullInt64{Int64: now, Valid: true},
		}).Error
	// R5-B4C: 并发兜底。
	return mapUniqueViolation(err, "username")
}

func (r *Repository) UpdateUserPassword(userID int64, pwdHash string, now int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Model(&model.User{}).
		Where("id = ?", userID).
		Updates(map[string]interface{}{
			"pwd":                 pwdHash,
			"password_changed_at": now,
			"updated_time":        sql.NullInt64{Int64: now, Valid: true},
		}).Error
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
		// F-076（A4-005）：补 nft_counter_state 清理。单删走
		// DeleteForwardCascade、隧删与节点删均已在各自事务内内联
		// 清理计数基线，唯独用户级联独漏——其转发的基线行会永久
		// 残留为孤儿（forward_id 不复用，无计费影响，但表随删用户
		// 单调膨胀）。注：预留的 DeleteNftCounterStatesByForward
		// 非事务型（走 r.db），故此处与其它级联一致用 tx 内联删。
		if err := tx.Where("forward_id IN (?)", forwardIDs).Delete(&model.NftCounterState{}).Error; err != nil {
			return err
		}
		if err := tx.Where("forward_id IN (?)", forwardIDs).Delete(&model.ForwardPort{}).Error; err != nil {
			return err
		}
		// B4/F-057: 与 DeleteForwardCascade 对齐——删用户须同步清其转发的
		// nft 台账三表与中转行，否则残留 pending 行会被后台重试循环反复
		// 取出对不存在的 forward 报错。
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
		relayIDs, err := pluckInboundRelayIDsTx(tx, "forward_id IN ?", userForwardIDs)
		if err != nil {
			return err
		}
		if err := deleteUserInboundGrantsByRelayIDsTx(tx, relayIDs); err != nil {
			return err
		}
		if err := tx.Where("forward_id IN (?)", forwardIDs).Delete(&model.NodeInboundRelay{}).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", userID).Delete(&model.Forward{}).Error; err != nil {
			return err
		}
		userTunnelIDs := tx.Model(&model.UserTunnel{}).Select("id").Where("user_id = ?", userID)
		if err := tx.Where("user_tunnel_id IN (?)", userTunnelIDs).Delete(&model.GroupPermissionGrant{}).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", userID).Delete(&model.UserTunnel{}).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", userID).Delete(&model.UserGroupUser{}).Error; err != nil {
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

func (r *Repository) ResetUserFlowByUser(userID int64, now int64) {
	if r == nil || r.db == nil {
		return
	}
	_ = r.db.Model(&model.User{}).
		Where("id = ?", userID).
		Updates(map[string]interface{}{
			"in_flow":      0,
			"out_flow":     0,
			"updated_time": sql.NullInt64{Int64: now, Valid: true},
		}).Error
	_ = r.db.Model(&model.UserTunnel{}).
		Where("user_id = ?", userID).
		Updates(map[string]interface{}{"in_flow": 0, "out_flow": 0}).Error
}

func (r *Repository) ResetUserFlowByUserTunnel(userTunnelID int64) {
	if r == nil || r.db == nil {
		return
	}
	_ = r.db.Model(&model.UserTunnel{}).
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
	var user model.User
	err = r.db.Select("flow", "num", "exp_time", "flow_reset_time").Where("id = ?", userID).First(&user).Error
	if err != nil {
		return 0, 0, 0, 0, normalizeNotFoundErr(err)
	}
	return user.Flow, user.Num, user.ExpTime, user.FlowResetTime, nil
}

func (r *Repository) CreateNode(name, secret, serverIP string, serverIPV4, serverIPV6, port, interfaceName, version, remark, expiryTime, renewalCycle interface{}, httpFlag, tlsFlag, socksFlag int, now int64, status int, tcpAddr, udpAddr string, inx, isRemote int, remoteURL, remoteToken, remoteConfig, extraIPs interface{}, forwardMode string) (int64, error) {
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
		IsRemote:      isRemote,
		ForwardMode:   defaultNodeForwardMode(forwardMode),
		RemoteURL:     nullStringFromInterface(remoteURL),
		RemoteToken:   nullStringFromInterface(remoteToken),
		RemoteConfig:  nullStringFromInterface(remoteConfig),
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

func (r *Repository) UpdateNodeOrder(nodeID int64, inx int, now int64) {
	if r == nil || r.db == nil {
		return
	}
	_ = r.db.Model(&model.Node{}).
		Where("id = ?", nodeID).
		Updates(map[string]interface{}{
			"inx":          inx,
			"updated_time": sql.NullInt64{Int64: now, Valid: true},
		}).Error
}

func (r *Repository) UpdateNodeExpiryReminderDismissed(nodeID int64, dismissed int) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Model(&model.Node{}).
		Where("id = ?", nodeID).
		Update("expiry_reminder_dismissed", dismissed).Error
}

func (r *Repository) DeleteNodeCascade(nodeID int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		// R5-B7: 补全级联删除（C-004~C-008）。
		// 数据面清理由 handler 的 deleteNodeByID 在调用本函数前尽力尝试
		// （B4/F-056：删除其上转发服务 + nftables 节点清表，失败记录日志
		// 不阻断——死节点必须能删）；本函数只负责 DB 行。此前注释声称的
		// 「调用方 pending 流程」并不存在，已按实际改写。
		if err := tx.Where("node_id = ?", nodeID).Delete(&model.ForwardPort{}).Error; err != nil {
			return err
		}
		if err := tx.Where("node_id = ?", nodeID).Delete(&model.ChainTunnel{}).Error; err != nil {
			return err
		}
		if err := tx.Where("node_id = ?", nodeID).Delete(&model.FederationTunnelBinding{}).Error; err != nil {
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
		// C-007: peer share。
		if err := tx.Where("node_id = ?", nodeID).Delete(&model.PeerShareRuntime{}).Error; err != nil {
			return err
		}
		// AUDIT-R18 (R5R18-001): PeerShare 与 PeerShareResource（node_id），
		// 与 C-007 的 PeerShareRuntime 清理保持一致。已删节点的 share token
		// 在 GetPeerShareByToken 中不校验节点存在性，残留即未吊销。
		if err := tx.Where("node_id = ?", nodeID).Delete(&model.PeerShareResource{}).Error; err != nil {
			return err
		}
		if err := tx.Where("node_id = ?", nodeID).Delete(&model.PeerShare{}).Error; err != nil {
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
		return tx.Where("id = ?", nodeID).Delete(&model.Node{}).Error
	})
}

func (r *Repository) GetNodeRemoteFields(nodeID int64) (isRemote int, remoteURL, remoteToken sql.NullString, err error) {
	if r == nil || r.db == nil {
		return 0, sql.NullString{}, sql.NullString{}, errors.New("repository not initialized")
	}
	return r.GetNodeRemoteFieldsTx(r.db, nodeID)
}

func (r *Repository) GetNodeRemoteFieldsTx(tx *gorm.DB, nodeID int64) (isRemote int, remoteURL, remoteToken sql.NullString, err error) {
	if tx == nil {
		return 0, sql.NullString{}, sql.NullString{}, errors.New("database unavailable")
	}
	var node model.Node
	err = tx.Select("is_remote", "remote_url", "remote_token").Where("id = ?", nodeID).First(&node).Error
	if err != nil {
		return 0, sql.NullString{}, sql.NullString{}, normalizeNotFoundErr(err)
	}
	return node.IsRemote, node.RemoteURL, node.RemoteToken, nil
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
	var cnt int64
	err := r.db.Model(&model.Tunnel{}).Where("name = ?", name).Count(&cnt).Error
	return cnt > 0, err
}

func (r *Repository) BeginTx() *gorm.DB {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db.Begin()
}

func (r *Repository) UpdateTunnelOrder(tunnelID int64, inx int, now int64) {
	if r == nil || r.db == nil {
		return
	}
	_ = r.db.Model(&model.Tunnel{}).
		Where("id = ?", tunnelID).
		Updates(map[string]interface{}{"inx": inx, "updated_time": now}).Error
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

func (r *Repository) IsRemoteNodeTx(tx *gorm.DB, nodeID int64) (bool, error) {
	if tx == nil {
		return false, errors.New("database unavailable")
	}
	if nodeID <= 0 {
		return false, errors.New("节点不存在")
	}
	var node model.Node
	err := tx.Select("is_remote").Where("id = ?", nodeID).First(&node).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, errors.New("节点不存在")
	}
	if err != nil {
		return false, err
	}
	return node.IsRemote == 1, nil
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

	used := make(map[int]struct{})

	chainQuery := tx.Model(&model.ChainTunnel{}).Where("node_id = ? AND port > 0", nodeID)
	if excludeTunnelID > 0 {
		chainQuery = chainQuery.Where("tunnel_id != ?", excludeTunnelID)
	}
	var chainPorts []int
	if err := chainQuery.Pluck("port", &chainPorts).Error; err != nil {
		return 0, err
	}
	for _, p := range chainPorts {
		if p > 0 {
			used[p] = struct{}{}
		}
	}

	var forwardPorts []int
	if err := tx.Model(&model.ForwardPort{}).
		Where("node_id = ? AND port > 0", nodeID).
		Pluck("port", &forwardPorts).Error; err != nil {
		return 0, err
	}
	for _, p := range forwardPorts {
		if p > 0 {
			used[p] = struct{}{}
		}
	}

	inboundPorts, err := listNodeInboundPortsTx(tx, nodeID, 0)
	if err != nil {
		return 0, err
	}
	for _, p := range inboundPorts {
		used[p] = struct{}{}
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
	if err := r.db.Select("id", "ip_preference").Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, err
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
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		// AUDIT-R1 (TST-001): 物化 ID 列表。原子查询在 Forward/UserTunnel 行删除后
		// 即失效（子查询变空），而级联清理必须引用已删行的 ID，故先物化。
		var forwardIDs []int64
		if err := tx.Model(&model.Forward{}).Where("tunnel_id = ?", tunnelID).Pluck("id", &forwardIDs).Error; err != nil {
			return err
		}
		var userTunnelIDs []int64
		if err := tx.Model(&model.UserTunnel{}).Where("tunnel_id = ?", tunnelID).Pluck("id", &userTunnelIDs).Error; err != nil {
			return err
		}
		if err := tx.Where("forward_id IN (?)", forwardIDs).Delete(&model.ForwardPort{}).Error; err != nil {
			return err
		}
		// AUDIT-R1 (TST-001): NodeInboundRelay / NftCounterState 没有 tunnel_id 列，
		// 它们通过 forward_id 关联本隧道的转发；移到 Forward 删除之前清理。
		// X2-01：删 relay 行前先物化其 id 集合，同步清理指向这些 relay
		// 的 user_inbound 授权行，避免孤儿授权残留。
		relayIDs, err := pluckInboundRelayIDsTx(tx, "forward_id IN ?", forwardIDs)
		if err != nil {
			return err
		}
		if err := deleteUserInboundGrantsByRelayIDsTx(tx, relayIDs); err != nil {
			return err
		}
		if err := tx.Where("forward_id IN (?)", forwardIDs).Delete(&model.NodeInboundRelay{}).Error; err != nil {
			return err
		}
		if err := tx.Where("forward_id IN (?)", forwardIDs).Delete(&model.NftCounterState{}).Error; err != nil {
			return err
		}
		if err := tx.Where("forward_id IN (?)", forwardIDs).Delete(&model.NftRuleBinding{}).Error; err != nil {
			return err
		}
		// AUDIT-R18 (DAT18-001): NftPendingDelete 通过 forward_id 关联，与
		// DeleteForwardCascade 的 R5-B3 D1-001 补充保持一致。
		if err := tx.Where("forward_id IN (?)", forwardIDs).Delete(&model.NftPendingDelete{}).Error; err != nil {
			return err
		}
		if err := tx.Where("tunnel_id = ?", tunnelID).Delete(&model.Forward{}).Error; err != nil {
			return err
		}
		// AUDIT-R1 (TST-001): GroupPermissionGrant 没有 tunnel_id 列，它通过
		// user_tunnel_id 关联本隧道的用户隧道；在 UserTunnel 删除前清理。
		if err := tx.Where("user_tunnel_id IN (?)", userTunnelIDs).Delete(&model.GroupPermissionGrant{}).Error; err != nil {
			return err
		}
		if err := tx.Where("tunnel_id = ?", tunnelID).Delete(&model.UserTunnel{}).Error; err != nil {
			return err
		}
		if err := tx.Where("tunnel_id = ?", tunnelID).Delete(&model.ChainTunnel{}).Error; err != nil {
			return err
		}
		if err := tx.Where("tunnel_id = ?", tunnelID).Delete(&model.FederationTunnelBinding{}).Error; err != nil {
			return err
		}
		// R5-B7 C-009: 补全隧道级联删除（以下模型确有 tunnel_id 列）。
		if err := tx.Where("tunnel_id = ?", tunnelID).Delete(&model.TunnelGroupTunnel{}).Error; err != nil {
			return err
		}
		if err := tx.Where("tunnel_id = ?", tunnelID).Delete(&model.SpeedLimit{}).Error; err != nil {
			return err
		}
		if err := tx.Where("tunnel_id = ?", tunnelID).Delete(&model.TunnelMetric{}).Error; err != nil {
			return err
		}
		// AUDIT-R18 (DAT18-002): 隧道质量探测历史（tunnel_id 索引）。
		if err := tx.Where("tunnel_id = ?", tunnelID).Delete(&model.TunnelQuality{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", tunnelID).Delete(&model.Tunnel{}).Error
	})
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

func (r *Repository) DeleteUserTunnel(id int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Where("id = ?", id).Delete(&model.UserTunnel{}).Error
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
	var ut model.UserTunnel
	err = r.db.Select("id", "flow", "num", "exp_time", "flow_reset_time", "speed_id", "status").
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
		ExpTime:       expTime,
		Status:        status,
	}
	return r.db.Create(&ut).Error
}

func (r *Repository) UpdateUserTunnelFields(id int64, speedID interface{}, flow int64, num int, expTime, flowResetTime int64, status int, flowMiB ...int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Model(&model.UserTunnel{}).
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

func (r *Repository) UpdateForwardOrder(forwardID int64, inx int, now int64) {
	if r == nil || r.db == nil {
		return
	}
	_ = r.db.Model(&model.Forward{}).
		Where("id = ?", forwardID).
		Updates(map[string]interface{}{"inx": inx, "updated_time": now}).Error
}

func (r *Repository) UpdateForwardTunnel(forwardID, tunnelID int64, now int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Model(&model.Forward{}).
		Where("id = ?", forwardID).
		Updates(map[string]interface{}{"tunnel_id": tunnelID, "updated_time": now}).Error
}

func (r *Repository) DeleteForwardCascade(forwardID int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("forward_id = ?", forwardID).Delete(&model.NftCounterState{}).Error; err != nil {
			return err
		}
		// R5-B3 D1-001 补充 A.3: pending 行最终删除时同步清理 binding 与
		// pending bookkeeping，避免残留基线与未来同 ID 新转发串号。
		if err := tx.Where("forward_id = ?", forwardID).Delete(&model.NftRuleBinding{}).Error; err != nil {
			return err
		}
		if err := tx.Where("forward_id = ?", forwardID).Delete(&model.NftPendingDelete{}).Error; err != nil {
			return err
		}
		if err := tx.Where("forward_id = ?", forwardID).Delete(&model.ForwardPort{}).Error; err != nil {
			return err
		}
		// A3-03/X2-01：删 relay 行前先物化其 id 集合，同步清理指向
		// 这些 relay 的 user_inbound 授权行，否则留下孤儿授权
		// （与单删 DeleteInboundRelay 的口径一致）。
		relayIDs, err := pluckInboundRelayIDsTx(tx, "forward_id = ?", forwardID)
		if err != nil {
			return err
		}
		if err := deleteUserInboundGrantsByRelayIDsTx(tx, relayIDs); err != nil {
			return err
		}
		if err := tx.Where("forward_id = ?", forwardID).Delete(&model.NodeInboundRelay{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", forwardID).Delete(&model.Forward{}).Error
	})
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
	defer func() {
		for i := len(locks) - 1; i >= 0; i-- {
			ReleasePortAllocLock(locks[i])
		}
	}()
	err := r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("forward_id = ?", forwardID).Delete(&model.ForwardPort{}).Error; err != nil {
			return err
		}
		if len(entries) == 0 {
			return nil
		}
		// B4/F-013: 锁内三表复检（forward_port/node_inbound/chain_tunnel）。
		// handler 的预检在锁外，与并发的入站/隧道创建存在「先查后写」
		// 窗口；旧端口已在本事务内删除，自占用天然排除。
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
	used := make(map[int]bool)
	var forwardPorts []int
	if err := r.db.Model(&model.ForwardPort{}).Where("node_id = ?", nodeID).Pluck("port", &forwardPorts).Error; err != nil {
		return nil, err
	}
	for _, p := range forwardPorts {
		used[p] = true
	}
	var chainPorts []int
	if err := r.db.Model(&model.ChainTunnel{}).Where("node_id = ? AND port > 0", nodeID).Pluck("port", &chainPorts).Error; err != nil {
		return nil, err
	}
	for _, p := range chainPorts {
		used[p] = true
	}
	// B4/F-013: 自动选港同样必须排除入站占用端口（此前只排除了
	// forward_port 与 chain_tunnel，随机选中入站端口后转发建得出、
	// 起不来）。external 行 node_id=0 天然不在本节点集合内。
	var inboundPorts []int
	if err := r.db.Model(&model.NodeInbound{}).Where("node_id = ?", nodeID).Pluck("port", &inboundPorts).Error; err != nil {
		return nil, err
	}
	for _, p := range inboundPorts {
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
	return r.db.Where("id = ?", id).Delete(&model.SpeedLimit{}).Error
}

func (r *Repository) GroupCreate(table, name string, status int, now int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	switch table {
	case "tunnel_group":
		return r.db.Create(&model.TunnelGroup{
			Name:        name,
			CreatedTime: now,
			UpdatedTime: now,
			Status:      status,
		}).Error
	case "user_group":
		return r.db.Create(&model.UserGroup{
			Name:        name,
			CreatedTime: now,
			UpdatedTime: now,
			Status:      status,
		}).Error
	default:
		return errors.New("invalid group table")
	}
}

func (r *Repository) GroupUpdate(table string, id int64, name string, status int, now int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	updates := map[string]interface{}{"name": name, "status": status, "updated_time": now}
	switch table {
	case "tunnel_group":
		return r.db.Model(&model.TunnelGroup{}).Where("id = ?", id).Updates(updates).Error
	case "user_group":
		return r.db.Model(&model.UserGroup{}).Where("id = ?", id).Updates(updates).Error
	default:
		return errors.New("invalid group table")
	}
}

func (r *Repository) GroupDeleteCascade(table string, id int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		switch table {
		case "tunnel_group":
			if err := tx.Where("tunnel_group_id = ?", id).Delete(&model.TunnelGroupTunnel{}).Error; err != nil {
				return err
			}
			if err := tx.Where("tunnel_group_id = ?", id).Delete(&model.GroupPermission{}).Error; err != nil {
				return err
			}
			if err := tx.Where("tunnel_group_id = ?", id).Delete(&model.GroupPermissionGrant{}).Error; err != nil {
				return err
			}
			return tx.Where("id = ?", id).Delete(&model.TunnelGroup{}).Error
		case "user_group":
			if err := tx.Where("user_group_id = ?", id).Delete(&model.UserGroupUser{}).Error; err != nil {
				return err
			}
			if err := tx.Where("user_group_id = ?", id).Delete(&model.GroupPermission{}).Error; err != nil {
				return err
			}
			if err := tx.Where("user_group_id = ?", id).Delete(&model.GroupPermissionGrant{}).Error; err != nil {
				return err
			}
			return tx.Where("id = ?", id).Delete(&model.UserGroup{}).Error
		default:
			return errors.New("invalid group table")
		}
	})
}

// GroupDeleteCascadeWithRevoke deletes a group like GroupDeleteCascade but
// first collects the group's GroupPermissionGrant ledger so group-created
// user_tunnel rows are revoked (and returned) instead of being orphaned
// with no ledger left to trace them (X-P1-2 path: groupDelete). Manual
// user_tunnel rows (created_by_group=0, or still granted by another group)
// survive.
func (r *Repository) GroupDeleteCascadeWithRevoke(table string, id int64) ([]RevokedUserTunnelPair, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var revoked []RevokedUserTunnelPair
	err := r.db.Transaction(func(tx *gorm.DB) error {
		var grantWhere string
		switch table {
		case "tunnel_group":
			grantWhere = "tunnel_group_id = ?"
		case "user_group":
			grantWhere = "user_group_id = ?"
		default:
			return errors.New("invalid group table")
		}
		var rows []groupGrantRow
		if err := tx.Model(&model.GroupPermissionGrant{}).
			Select("user_tunnel_id, created_by_group").
			Where(grantWhere, id).
			Find(&rows).Error; err != nil {
			return err
		}
		candidates := groupCreatedUTIDs(rows)

		switch table {
		case "tunnel_group":
			if err := tx.Where("tunnel_group_id = ?", id).Delete(&model.TunnelGroupTunnel{}).Error; err != nil {
				return err
			}
			if err := tx.Where("tunnel_group_id = ?", id).Delete(&model.GroupPermission{}).Error; err != nil {
				return err
			}
			if err := tx.Where("tunnel_group_id = ?", id).Delete(&model.GroupPermissionGrant{}).Error; err != nil {
				return err
			}
			if err := tx.Where("id = ?", id).Delete(&model.TunnelGroup{}).Error; err != nil {
				return err
			}
		case "user_group":
			if err := tx.Where("user_group_id = ?", id).Delete(&model.UserGroupUser{}).Error; err != nil {
				return err
			}
			if err := tx.Where("user_group_id = ?", id).Delete(&model.GroupPermission{}).Error; err != nil {
				return err
			}
			if err := tx.Where("user_group_id = ?", id).Delete(&model.GroupPermissionGrant{}).Error; err != nil {
				return err
			}
			if err := tx.Where("id = ?", id).Delete(&model.UserGroup{}).Error; err != nil {
				return err
			}
		}
		pairs, err := deleteOrphanedGroupCreatedUserTunnelsTx(tx, candidates)
		if err != nil {
			return err
		}
		revoked = pairs
		return nil
	})
	if err != nil {
		return nil, err
	}
	return revoked, nil
}

func (r *Repository) ListUserIDsByUserGroupTx(tx *gorm.DB, userGroupID int64) ([]int64, error) {
	if tx == nil {
		return nil, errors.New("database unavailable")
	}
	var ids []int64
	err := tx.Model(&model.UserGroupUser{}).
		Where("user_group_id = ?", userGroupID).
		Pluck("user_id", &ids).Error
	if err != nil {
		return nil, err
	}
	if ids == nil {
		ids = make([]int64, 0)
	}
	return ids, nil
}

func (r *Repository) ReplaceTunnelGroupMembersTx(tx *gorm.DB, groupID int64, tunnelIDs []int64, now int64) error {
	if tx == nil {
		return errors.New("database unavailable")
	}
	if err := tx.Where("tunnel_group_id = ?", groupID).Delete(&model.TunnelGroupTunnel{}).Error; err != nil {
		return err
	}
	if len(tunnelIDs) == 0 {
		return nil
	}
	rows := make([]model.TunnelGroupTunnel, 0, len(tunnelIDs))
	for _, tunnelID := range tunnelIDs {
		if tunnelID <= 0 {
			continue
		}
		rows = append(rows, model.TunnelGroupTunnel{TunnelGroupID: groupID, TunnelID: tunnelID, CreatedTime: now})
	}
	if len(rows) == 0 {
		return nil
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error
}

func (r *Repository) ReplaceUserGroupMembersTx(tx *gorm.DB, groupID int64, userIDs []int64, now int64) error {
	if tx == nil {
		return errors.New("database unavailable")
	}
	if err := tx.Where("user_group_id = ?", groupID).Delete(&model.UserGroupUser{}).Error; err != nil {
		return err
	}
	if len(userIDs) == 0 {
		return nil
	}
	rows := make([]model.UserGroupUser, 0, len(userIDs))
	for _, userID := range userIDs {
		if userID <= 0 {
			continue
		}
		rows = append(rows, model.UserGroupUser{UserGroupID: groupID, UserID: userID, CreatedTime: now})
	}
	if len(rows) == 0 {
		return nil
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error
}

func (r *Repository) GetGroupPermissionPairByIDTx(tx *gorm.DB, id int64) (userGroupID int64, tunnelGroupID int64, exists bool, err error) {
	if tx == nil {
		return 0, 0, false, errors.New("database unavailable")
	}
	var gp model.GroupPermission
	err = tx.Select("user_group_id", "tunnel_group_id").Where("id = ?", id).First(&gp).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, false, err
	}
	return gp.UserGroupID, gp.TunnelGroupID, true, nil
}

func (r *Repository) DeleteGroupPermissionByIDTx(tx *gorm.DB, id int64) error {
	if tx == nil {
		return errors.New("database unavailable")
	}
	return tx.Where("id = ?", id).Delete(&model.GroupPermission{}).Error
}

// RevokedUserTunnelPair holds the (userID, tunnelID) of a deleted user_tunnel row,
// so the handler layer can clean up associated forwarding rules.
type RevokedUserTunnelPair struct {
	UserID   int64
	TunnelID int64
}

// deleteOrphanedGroupCreatedUserTunnelsTx deletes the given group-created
// user_tunnel rows that no longer have any GroupPermissionGrant referencing
// them, returning the (userID, tunnelID) pairs actually deleted so the
// handler can clean up their forwards. Manual grants (created_by_group=0)
// are never in the candidate set, so hand-assigned tunnels survive.
func deleteOrphanedGroupCreatedUserTunnelsTx(tx *gorm.DB, candidates map[int64]struct{}) ([]RevokedUserTunnelPair, error) {
	var revoked []RevokedUserTunnelPair
	for userTunnelID := range candidates {
		var remaining int64
		if err := tx.Model(&model.GroupPermissionGrant{}).Where("user_tunnel_id = ?", userTunnelID).Count(&remaining).Error; err != nil {
			return revoked, err
		}
		if remaining == 0 {
			var ut model.UserTunnel
			if lookupErr := tx.Select("user_id", "tunnel_id").Where("id = ?", userTunnelID).First(&ut).Error; lookupErr == nil {
				revoked = append(revoked, RevokedUserTunnelPair{UserID: ut.UserID, TunnelID: ut.TunnelID})
			}
			if err := tx.Where("id = ?", userTunnelID).Delete(&model.UserTunnel{}).Error; err != nil {
				return revoked, err
			}
		}
	}
	return revoked, nil
}

type groupGrantRow struct {
	UserTunnelID   int64
	CreatedByGroup int
}

func groupCreatedUTIDs(rows []groupGrantRow) map[int64]struct{} {
	out := make(map[int64]struct{})
	for _, row := range rows {
		if row.CreatedByGroup == 1 && row.UserTunnelID > 0 {
			out[row.UserTunnelID] = struct{}{}
		}
	}
	return out
}

// RevokeGroupGrantsForUserTx revokes one user's group-created grants that
// came through userGroupID (X-P1-2 path: userUpdate removing the user from
// that group). Only grants recorded under this user group are removed, and
// only user_tunnel rows the group materialization created are deleted.
func (r *Repository) RevokeGroupGrantsForUserTx(tx *gorm.DB, userGroupID, userID int64) ([]RevokedUserTunnelPair, error) {
	if tx == nil {
		return nil, errors.New("database unavailable")
	}
	var rows []groupGrantRow
	if err := tx.Model(&model.GroupPermissionGrant{}).
		Select("group_permission_grant.user_tunnel_id, group_permission_grant.created_by_group").
		Joins("JOIN user_tunnel ON user_tunnel.id = group_permission_grant.user_tunnel_id").
		Where("group_permission_grant.user_group_id = ? AND user_tunnel.user_id = ?", userGroupID, userID).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	userTunnelIDs := tx.Model(&model.UserTunnel{}).Select("id").Where("user_id = ?", userID)
	if err := tx.Where("user_group_id = ? AND user_tunnel_id IN (?)", userGroupID, userTunnelIDs).
		Delete(&model.GroupPermissionGrant{}).Error; err != nil {
		return nil, err
	}
	return deleteOrphanedGroupCreatedUserTunnelsTx(tx, groupCreatedUTIDs(rows))
}

func (r *Repository) RevokeGroupGrantsForRemovedUsersTx(tx *gorm.DB, userGroupID int64, previousUserIDs, currentUserIDs []int64) ([]RevokedUserTunnelPair, error) {
	if tx == nil {
		return nil, errors.New("database unavailable")
	}
	currentSet := make(map[int64]struct{}, len(currentUserIDs))
	for _, uid := range currentUserIDs {
		if uid > 0 {
			currentSet[uid] = struct{}{}
		}
	}

	var revoked []RevokedUserTunnelPair
	for _, uid := range previousUserIDs {
		if uid <= 0 {
			continue
		}
		if _, ok := currentSet[uid]; ok {
			continue
		}
		pairs, err := r.RevokeGroupGrantsForUserTx(tx, userGroupID, uid)
		if err != nil {
			return revoked, err
		}
		revoked = append(revoked, pairs...)
	}
	return revoked, nil
}

// RevokeGroupGrantsForRemovedTunnelsTx revokes group-created grants whose
// tunnel was removed from tunnelGroupID (X-P1-2 path: groupTunnelAssign
// replacing the tunnel group's members). Scoped by
// (tunnelGroupID, removed tunnel): grants from other tunnel groups and
// manual (created_by_group=0) user_tunnel rows are untouched.
func (r *Repository) RevokeGroupGrantsForRemovedTunnelsTx(tx *gorm.DB, tunnelGroupID int64, previousTunnelIDs, currentTunnelIDs []int64) ([]RevokedUserTunnelPair, error) {
	if tx == nil {
		return nil, errors.New("database unavailable")
	}
	currentSet := make(map[int64]struct{}, len(currentTunnelIDs))
	for _, tid := range currentTunnelIDs {
		if tid > 0 {
			currentSet[tid] = struct{}{}
		}
	}

	var revoked []RevokedUserTunnelPair
	for _, tunnelID := range previousTunnelIDs {
		if tunnelID <= 0 {
			continue
		}
		if _, ok := currentSet[tunnelID]; ok {
			continue
		}
		var rows []groupGrantRow
		if err := tx.Model(&model.GroupPermissionGrant{}).
			Select("group_permission_grant.user_tunnel_id, group_permission_grant.created_by_group").
			Joins("JOIN user_tunnel ON user_tunnel.id = group_permission_grant.user_tunnel_id").
			Where("group_permission_grant.tunnel_group_id = ? AND user_tunnel.tunnel_id = ?", tunnelGroupID, tunnelID).
			Find(&rows).Error; err != nil {
			return revoked, err
		}
		userTunnelIDs := tx.Model(&model.UserTunnel{}).Select("id").Where("tunnel_id = ?", tunnelID)
		if err := tx.Where("tunnel_group_id = ? AND user_tunnel_id IN (?)", tunnelGroupID, userTunnelIDs).
			Delete(&model.GroupPermissionGrant{}).Error; err != nil {
			return revoked, err
		}
		pairs, err := deleteOrphanedGroupCreatedUserTunnelsTx(tx, groupCreatedUTIDs(rows))
		if err != nil {
			return revoked, err
		}
		revoked = append(revoked, pairs...)
	}
	return revoked, nil
}

// ListTunnelIDsByTunnelGroupTx is the Tx variant of ListTunnelIDsByTunnelGroup.
func (r *Repository) ListTunnelIDsByTunnelGroupTx(tx *gorm.DB, tunnelGroupID int64) ([]int64, error) {
	if tx == nil {
		return nil, errors.New("database unavailable")
	}
	var ids []int64
	err := tx.Model(&model.TunnelGroupTunnel{}).
		Where("tunnel_group_id = ?", tunnelGroupID).
		Pluck("tunnel_id", &ids).Error
	if err != nil {
		return nil, err
	}
	if ids == nil {
		ids = make([]int64, 0)
	}
	return ids, nil
}

func (r *Repository) RevokeGroupPermissionPairTx(tx *gorm.DB, userGroupID, tunnelGroupID int64) ([]RevokedUserTunnelPair, error) {
	if tx == nil {
		return nil, errors.New("database unavailable")
	}

	type grantRow struct {
		UserTunnelID   int64
		CreatedByGroup int
	}

	var rows []grantRow
	if err := tx.Model(&model.GroupPermissionGrant{}).
		Select("user_tunnel_id, created_by_group").
		Where("user_group_id = ? AND tunnel_group_id = ?", userGroupID, tunnelGroupID).
		Find(&rows).Error; err != nil {
		return nil, err
	}

	groupCreatedTunnelIDs := make(map[int64]struct{})
	for _, row := range rows {
		if row.CreatedByGroup == 1 && row.UserTunnelID > 0 {
			groupCreatedTunnelIDs[row.UserTunnelID] = struct{}{}
		}
	}

	if err := tx.Where("user_group_id = ? AND tunnel_group_id = ?", userGroupID, tunnelGroupID).
		Delete(&model.GroupPermissionGrant{}).Error; err != nil {
		return nil, err
	}

	var revoked []RevokedUserTunnelPair
	for userTunnelID := range groupCreatedTunnelIDs {
		var remaining int64
		if err := tx.Model(&model.GroupPermissionGrant{}).Where("user_tunnel_id = ?", userTunnelID).Count(&remaining).Error; err != nil {
			return revoked, err
		}
		if remaining == 0 {
			var ut model.UserTunnel
			if lookupErr := tx.Select("user_id", "tunnel_id").Where("id = ?", userTunnelID).First(&ut).Error; lookupErr == nil {
				revoked = append(revoked, RevokedUserTunnelPair{UserID: ut.UserID, TunnelID: ut.TunnelID})
			}
			if err := tx.Where("id = ?", userTunnelID).Delete(&model.UserTunnel{}).Error; err != nil {
				return revoked, err
			}
		}
	}

	return revoked, nil
}

func (r *Repository) ReplaceFederationTunnelBindingsTx(tx *gorm.DB, tunnelID int64, bindings []FederationTunnelBinding) error {
	if tx == nil {
		return errors.New("database unavailable")
	}
	if err := tx.Where("tunnel_id = ?", tunnelID).Delete(&model.FederationTunnelBinding{}).Error; err != nil {
		return err
	}
	if len(bindings) == 0 {
		return nil
	}

	rows := make([]model.FederationTunnelBinding, 0, len(bindings))
	now := time.Now().UnixMilli()
	for _, b := range bindings {
		created := b.CreatedTime
		if created <= 0 {
			created = now
		}
		updated := b.UpdatedTime
		if updated <= 0 {
			updated = created
		}
		rows = append(rows, model.FederationTunnelBinding{
			TunnelID:        tunnelID,
			NodeID:          b.NodeID,
			ChainType:       b.ChainType,
			HopInx:          b.HopInx,
			RemoteURL:       b.RemoteURL,
			ResourceKey:     b.ResourceKey,
			RemoteBindingID: b.RemoteBindingID,
			AllocatedPort:   b.AllocatedPort,
			Status:          b.Status,
			CreatedTime:     created,
			UpdatedTime:     updated,
		})
	}

	// FIX2-B（V4-N2 同族）：status 带 default:1，Status=0 的 binding
	// 会在 Create 时被吞成 1（停用绑定被静默激活）。意图先行捕获——
	// Create 后 GORM 会把默认值回填进 rows，不可再读。
	intents := make([]int, len(rows))
	for i := range rows {
		intents[i] = rows[i].Status
	}
	if err := tx.Create(&rows).Error; err != nil {
		return err
	}
	for i := range rows {
		if err := restoreZeroValuedInt(tx, &model.FederationTunnelBinding{}, rows[i].ID, "status", intents[i]); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) InsertGroupPermission(userGroupID, tunnelGroupID int64, now int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	gp := model.GroupPermission{UserGroupID: userGroupID, TunnelGroupID: tunnelGroupID, CreatedTime: now}
	return r.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&gp).Error
}

func (r *Repository) InsertGroupPermissionGrant(userGroupID, tunnelGroupID, userTunnelID int64, createdByGroup int, now int64) {
	if r == nil || r.db == nil {
		return
	}
	g := model.GroupPermissionGrant{
		UserGroupID:    userGroupID,
		TunnelGroupID:  tunnelGroupID,
		UserTunnelID:   userTunnelID,
		CreatedByGroup: createdByGroup,
		CreatedTime:    now,
	}
	_ = r.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&g).Error
}

func (r *Repository) EnsureUserTunnelGrant(userID, tunnelID int64) (int64, bool, error) {
	if r == nil || r.db == nil {
		return 0, false, errors.New("repository not initialized")
	}
	var existing model.UserTunnel
	err := r.db.Select("id").Where("user_id = ? AND tunnel_id = ?", userID, tunnelID).First(&existing).Error
	if err == nil {
		return existing.ID, false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, false, err
	}
	var user model.User
	if err := r.db.Select("flow, flow_mib, num, exp_time, flow_reset_time").Where("id = ?", userID).First(&user).Error; err != nil {
		return 0, false, err
	}
	flow := user.Flow
	num := user.Num
	expTime := user.ExpTime
	flowReset := user.FlowResetTime
	ut := model.UserTunnel{
		UserID:        userID,
		TunnelID:      tunnelID,
		Num:           num,
		Flow:          flow,
		FlowMiB:       user.FlowMiB,
		InFlow:        0,
		OutFlow:       0,
		FlowResetTime: flowReset,
		ExpTime:       expTime,
		Status:        1,
	}
	if err := r.db.Create(&ut).Error; err != nil {
		return 0, false, err
	}
	return ut.ID, true, nil
}

func (r *Repository) CreateForwardTx(userID int64, userName, name string, tunnelID int64, remoteAddr, strategy string, now int64, inx int, entryNodeIDs []int64, port int, inIp string, speedID interface{}, maxConn int, ipMaxConn int, ipSpeedID interface{}, proxyProtocol int, proxyProtocolReceive int, proxyProtocolSend int) (int64, error) {
	if r == nil || r.db == nil {
		return 0, errors.New("repository not initialized")
	}
	// R5-B4: per-node 端口分配锁（事务之前获取，排序后加锁防死锁）。
	// 锁内禁止网络调用。
	sortedNodes := append([]int64(nil), entryNodeIDs...)
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
		// B4/F-013: 锁内三表复检（同 ReplaceForwardPorts）。handler 预检
		// 在锁外，跨表并发（入站/隧道创建）可穿透窗口双占用落库。
		for _, nodeID := range entryNodeIDs {
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
		for _, nodeID := range entryNodeIDs {
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

func (r *Repository) BatchUpdateForwardStatus(ids []int64, status int) (int, int) {
	if r == nil || r.db == nil {
		return 0, len(ids)
	}
	s := 0
	f := 0
	now := time.Now().UnixMilli()
	for _, id := range ids {
		if err := r.db.Model(&model.Forward{}).Where("id = ?", id).Updates(map[string]interface{}{"status": status, "updated_time": now}).Error; err != nil {
			f++
		} else {
			s++
		}
	}
	return s, f
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
	if err := tx.Create(&tunnel).Error; err != nil {
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
			for p := start; p <= end; p++ {
				set[p] = struct{}{}
			}
			continue
		}
		p, err := strconv.Atoi(part)
		if err != nil || p <= 0 {
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

func (r *Repository) AddUserToGroups(userID int64, groupIDs []int64, now int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	if len(groupIDs) == 0 {
		return nil
	}
	rows := make([]model.UserGroupUser, 0, len(groupIDs))
	for _, gid := range groupIDs {
		if gid <= 0 {
			continue
		}
		rows = append(rows, model.UserGroupUser{UserGroupID: gid, UserID: userID, CreatedTime: now})
	}
	if len(rows) == 0 {
		return nil
	}
	return r.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error
}

// validateUserGroupIDsExistTx checks (X-P3-5) that every referenced user
// group exists, using one batched IN query; any missing ID fails the whole
// write, matching the subscription-assignment validation standard.
func validateUserGroupIDsExistTx(tx *gorm.DB, groupIDs []int64) error {
	seen := make(map[int64]struct{})
	ids := make([]int64, 0, len(groupIDs))
	for _, gid := range groupIDs {
		if gid <= 0 {
			continue
		}
		if _, ok := seen[gid]; ok {
			continue
		}
		seen[gid] = struct{}{}
		ids = append(ids, gid)
	}
	if len(ids) == 0 {
		return nil
	}
	var existing []int64
	if err := tx.Model(&model.UserGroup{}).Where("id IN ?", ids).Pluck("id", &existing).Error; err != nil {
		return err
	}
	existingSet := make(map[int64]struct{}, len(existing))
	for _, id := range existing {
		existingSet[id] = struct{}{}
	}
	missing := make([]int64, 0)
	for _, id := range ids {
		if _, ok := existingSet[id]; !ok {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("用户分组不存在: %v", missing)
	}
	return nil
}

// AddUserToGroupsTx 是 O5-004 的事务版：跑在调用方事务内（与 CreateUserTx 同 tx）。
func (r *Repository) AddUserToGroupsTx(tx *gorm.DB, userID int64, groupIDs []int64, now int64) error {
	if tx == nil {
		return errors.New("database unavailable")
	}
	if len(groupIDs) == 0 {
		return nil
	}
	if err := validateUserGroupIDsExistTx(tx, groupIDs); err != nil {
		return err
	}
	rows := make([]model.UserGroupUser, 0, len(groupIDs))
	for _, gid := range groupIDs {
		if gid <= 0 {
			continue
		}
		rows = append(rows, model.UserGroupUser{UserGroupID: gid, UserID: userID, CreatedTime: now})
	}
	if len(rows) == 0 {
		return nil
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error
}

// ReplaceUserGroupsByUserIDTx is the transactional variant of the old
// ReplaceUserGroupsByUserID (X-P2-6): it runs entirely on the caller's tx
// (same shape as ReplaceUserGroupMembersTx) so the handler can wrap
// "replace + revoke removed groups' grants" in one transaction, and any
// failure propagates instead of leaving membership half-replaced.
// removedGroupIDs reports old − new so the caller can revoke group grants
// for exactly the groups the user left.
func (r *Repository) ReplaceUserGroupsByUserIDTx(tx *gorm.DB, userID int64, newGroupIDs []int64, now int64) (affectedGroupIDs []int64, removedGroupIDs []int64, err error) {
	if tx == nil {
		return nil, nil, errors.New("database unavailable")
	}
	if err := validateUserGroupIDsExistTx(tx, newGroupIDs); err != nil {
		return nil, nil, err
	}

	var oldGroupIDs []int64
	if err = tx.Model(&model.UserGroupUser{}).
		Where("user_id = ?", userID).
		Pluck("user_group_id", &oldGroupIDs).Error; err != nil {
		return nil, nil, err
	}

	newSet := make(map[int64]struct{})
	for _, id := range newGroupIDs {
		if id > 0 {
			newSet[id] = struct{}{}
		}
	}
	seen := make(map[int64]struct{})
	for _, id := range oldGroupIDs {
		seen[id] = struct{}{}
		if _, ok := newSet[id]; !ok {
			removedGroupIDs = append(removedGroupIDs, id)
		}
	}
	for id := range newSet {
		seen[id] = struct{}{}
	}
	for id := range seen {
		affectedGroupIDs = append(affectedGroupIDs, id)
	}

	if err = tx.Where("user_id = ?", userID).Delete(&model.UserGroupUser{}).Error; err != nil {
		return nil, nil, err
	}

	rows := make([]model.UserGroupUser, 0, len(newGroupIDs))
	for _, gid := range newGroupIDs {
		if gid <= 0 {
			continue
		}
		rows = append(rows, model.UserGroupUser{UserGroupID: gid, UserID: userID, CreatedTime: now})
	}
	if len(rows) > 0 {
		if err = tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error; err != nil {
			return nil, nil, err
		}
	}
	return affectedGroupIDs, removedGroupIDs, nil
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
