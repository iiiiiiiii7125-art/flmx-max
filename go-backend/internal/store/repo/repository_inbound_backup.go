package repo

import (
	"go-backend/internal/store/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (r *Repository) exportInbounds() (*model.InboundsBackup, error) {
	out := &model.InboundsBackup{
		Kernels:  []model.KernelBackup{},
		Inbounds: []model.InboundBackup{},
		Relays:   []model.InboundRelayBackup{},
	}

	var kernels []model.NodeKernel
	if err := r.db.Order("id ASC").Find(&kernels).Error; err != nil {
		return nil, err
	}
	for _, k := range kernels {
		out.Kernels = append(out.Kernels, model.KernelBackup{
			ID: k.ID, NodeID: k.NodeID, KernelType: k.KernelType, Version: k.Version,
			Enabled: k.Enabled, CreatedTime: k.CreatedTime,
		})
	}

	var inbounds []model.NodeInbound
	if err := r.db.Order("id ASC").Find(&inbounds).Error; err != nil {
		return nil, err
	}
	for _, i := range inbounds {
		out.Inbounds = append(out.Inbounds, model.InboundBackup{
			ID: i.ID, KernelID: i.KernelID, NodeID: i.NodeID, Name: i.Name, Protocol: i.Protocol,
			Listen: i.Listen, Port: i.Port, Transport: i.Transport, Security: i.Security,
			Settings: i.Settings, Extra: i.Extra, ShareHost: i.ShareHost, Remark: i.Remark,
			RelayOnly: i.RelayOnly, AcceptProxyProtocol: i.AcceptProxyProtocol,
			RelayWhitelist: i.RelayWhitelist, WhitelistExtra: i.WhitelistExtra, Enabled: i.Enabled,
			UploadBytes: i.UploadBytes, DownloadBytes: i.DownloadBytes, Inx: i.Inx,
			CreatedTime: i.CreatedTime, SourceID: i.SourceID, OutboundID: i.OutboundID, SplitRules: i.SplitRules,
			OutboundIPStrategy: i.OutboundIPStrategy,
		})
	}

	var relays []model.NodeInboundRelay
	if err := r.db.Order("id ASC").Find(&relays).Error; err != nil {
		return nil, err
	}
	for _, rel := range relays {
		out.Relays = append(out.Relays, model.InboundRelayBackup{
			ID: rel.ID, InboundID: rel.InboundID, ForwardID: rel.ForwardID, Name: rel.Name,
			ShareHost: rel.ShareHost, Managed: rel.Managed, CreatedTime: rel.CreatedTime,
		})
	}

	var counters []model.NodeInboundCounter
	if err := r.db.Order("id ASC").Find(&counters).Error; err != nil {
		return nil, err
	}
	for _, c := range counters {
		out.Counters = append(out.Counters, model.InboundCounterBackup{
			InboundID: c.InboundID, Protocol: c.Protocol, Direction: c.Direction,
			Epoch: c.Epoch, Bytes: c.Bytes,
		})
	}
	var sources []model.RemoteSource
	if err := r.db.Order("id ASC").Find(&sources).Error; err != nil {
		return nil, err
	}
	for _, src := range sources {
		out.Sources = append(out.Sources, model.RemoteSourceBackup{
			ID: src.ID, Name: src.Name, URL: src.URL, IntervalHours: src.IntervalHours,
			RelayOnly: src.RelayOnly, Kind: src.Kind, Role: src.Role, Enabled: src.Enabled, CreatedTime: src.CreatedTime,
		})
	}
	// A3-04：订阅计数基线与入站基线同等待遇随备份导出。
	var subCounters []model.SubscriptionCounter
	if err := r.db.Order("id ASC").Find(&subCounters).Error; err != nil {
		return nil, err
	}
	for _, c := range subCounters {
		out.SubscriptionCounters = append(out.SubscriptionCounters, model.SubscriptionCounterBackup{
			NodeID: c.NodeID, SubscriptionID: c.SubscriptionID, Direction: c.Direction,
			Epoch: c.Epoch, Bytes: c.Bytes,
		})
	}
	var subs []model.Subscription
	if err := r.db.Order("id ASC").Find(&subs).Error; err != nil {
		return nil, err
	}
	for _, sub := range subs {
		out.Subscriptions = append(out.Subscriptions, model.SubscriptionBackup{
			ID: sub.ID, Name: sub.Name, Kind: sub.Kind, Token: sub.Token, UUID: sub.UUID, Password: sub.Password,
			SourceAll: sub.SourceAll, InboundIDs: sub.InboundIDs, SourceIDs: sub.SourceIDs, Options: sub.Options, ExpireTime: sub.ExpireTime,
			Enabled: sub.Enabled, Remark: sub.Remark, Inx: sub.Inx, CreatedTime: sub.CreatedTime,
			UploadBytes: sub.UploadBytes, DownloadBytes: sub.DownloadBytes, TrafficLimit: sub.TrafficLimit,
			ResetDay: sub.ResetDay, TrafficResetTime: sub.TrafficResetTime,
		})
	}
	return out, nil
}

// importInbounds upserts kernels, inbounds and relay links by id. Kernels
// come back as pending with no applied config, so the panel pushes them to
// the nodes again (and installs the kernel where it is missing).
//
// subRemap (BUG-A2-4) collects backup subscription id -> restored row id.
// Subscriptions are matched by token, not by backup id: a subscription
// that was deleted after the backup comes back under a fresh id, and
// user_subscription rows in the same backup still carry the old id.
// Import() feeds this map to importUserSubscriptions so assignments are
// rewritten onto the restored rows instead of dangling.
func importInbounds(tx *gorm.DB, b *model.InboundsBackup, now int64, subRemap map[int64]int64, rep *importReport) (int, error) {
	for _, k := range b.Kernels {
		// D4-03：引用的节点不存在时跳过该内核并计数。
		kernelNodeExists, err := rowExistsTx(tx, &model.Node{}, k.NodeID)
		if err != nil {
			return 0, err
		}
		if !kernelNodeExists {
			rep.skip("kernels")
			continue
		}
		// One kernel per node and type: drop a different row in the way.
		if err := tx.Where("node_id = ? AND kernel_type = ? AND id <> ?", k.NodeID, k.KernelType, k.ID).
			Delete(&model.NodeKernel{}).Error; err != nil {
			return 0, err
		}
		version := k.Version
		if version == "" {
			version = "latest"
		}
		item := model.NodeKernel{
			ID: k.ID, NodeID: k.NodeID, KernelType: k.KernelType, Version: version,
			Status: "pending", Enabled: k.Enabled, CreatedTime: k.CreatedTime, UpdatedTime: now,
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"node_id", "kernel_type", "version", "installed_version", "status", "last_error",
				"config_hash", "applied_hash", "enabled", "updated_time",
			}),
		}).Create(&item).Error; err != nil {
			return 0, err
		}
		if err := restoreZeroValuedInt(tx, &model.NodeKernel{}, k.ID, "enabled", k.Enabled); err != nil {
			return 0, err
		}
	}

	// Sources first: imported nodes point at them. A refresh fills in the
	// status fields.
	for _, src := range b.Sources {
		kind := src.Kind
		if kind != model.RemoteSourceFile {
			kind = model.RemoteSourceURL // backups from before file sources
		}
		item := model.RemoteSource{
			ID: src.ID, Name: src.Name, URL: src.URL, IntervalHours: src.IntervalHours,
			RelayOnly: src.RelayOnly, Kind: kind, Role: src.Role, Enabled: src.Enabled, CreatedTime: src.CreatedTime, UpdatedTime: now,
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns([]string{"name", "url", "interval_hours", "relay_only", "kind", "role", "enabled", "updated_time"}),
		}).Create(&item).Error; err != nil {
			return 0, err
		}
	}

	for _, i := range b.Inbounds {
		// D4-03：引用的内核/节点不存在时跳过该入站并计数。注意
		// kernel_id=0 / node_id=0 是合法的「未绑定」语义（导入型
		// 入站，见 v1 迁移对 kernel_id>0 部分索引的口径），只校验
		// 非零引用。
		kernelExists := true
		if i.KernelID > 0 {
			var err error
			kernelExists, err = rowExistsTx(tx, &model.NodeKernel{}, i.KernelID)
			if err != nil {
				return 0, err
			}
		}
		inboundNodeExists := true
		if i.NodeID > 0 {
			var err error
			inboundNodeExists, err = rowExistsTx(tx, &model.Node{}, i.NodeID)
			if err != nil {
				return 0, err
			}
		}
		if !kernelExists || !inboundNodeExists {
			rep.skip("inbounds")
			continue
		}
		listen := i.Listen
		if listen == "" {
			listen = "::"
		}
		// 旧备份没有该字段；空值按 auto（与 Normalize 口径一致）。
		strategy := i.OutboundIPStrategy
		if strategy == "" {
			strategy = "auto"
		}
		item := model.NodeInbound{
			ID: i.ID, KernelID: i.KernelID, NodeID: i.NodeID, Name: i.Name, Protocol: i.Protocol,
			Listen: listen, Port: i.Port, Transport: i.Transport, Security: i.Security,
			Settings: i.Settings, Extra: i.Extra, ShareHost: i.ShareHost, Remark: i.Remark,
			RelayOnly: i.RelayOnly, AcceptProxyProtocol: i.AcceptProxyProtocol,
			RelayWhitelist: i.RelayWhitelist, WhitelistExtra: i.WhitelistExtra, Enabled: i.Enabled,
			UploadBytes: i.UploadBytes, DownloadBytes: i.DownloadBytes, Inx: i.Inx,
			CreatedTime: i.CreatedTime, UpdatedTime: now, SourceID: i.SourceID, OutboundID: i.OutboundID,
			SplitRules: i.SplitRules, OutboundIPStrategy: strategy,
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"kernel_id", "node_id", "name", "protocol", "listen", "port", "transport", "security",
				"settings", "extra", "share_host", "remark", "relay_only", "accept_proxy_protocol",
				"relay_whitelist", "whitelist_extra", "enabled", "upload_bytes", "download_bytes",
				"inx", "updated_time", "source_id", "outbound_id", "split_rules", "outbound_ip_strategy",
			}),
		}).Create(&item).Error; err != nil {
			return 0, err
		}
		if err := restoreZeroValuedInt(tx, &model.NodeInbound{}, i.ID, "enabled", i.Enabled); err != nil {
			return 0, err
		}
	}

	for _, rel := range b.Relays {
		// D4-03：引用的入站/转发不存在时跳过该中转并计数。
		relayInboundExists, err := rowExistsTx(tx, &model.NodeInbound{}, rel.InboundID)
		if err != nil {
			return 0, err
		}
		relayForwardExists, err := rowExistsTx(tx, &model.Forward{}, rel.ForwardID)
		if err != nil {
			return 0, err
		}
		if !relayInboundExists || !relayForwardExists {
			rep.skip("relays")
			continue
		}
		if err := tx.Where("forward_id = ? AND id <> ?", rel.ForwardID, rel.ID).
			Delete(&model.NodeInboundRelay{}).Error; err != nil {
			return 0, err
		}
		item := model.NodeInboundRelay{
			ID: rel.ID, InboundID: rel.InboundID, ForwardID: rel.ForwardID, Name: rel.Name,
			ShareHost: rel.ShareHost, Managed: rel.Managed, CreatedTime: rel.CreatedTime,
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns([]string{"inbound_id", "forward_id", "name", "share_host", "managed"}),
		}).Create(&item).Error; err != nil {
			return 0, err
		}
		if err := restoreZeroValuedInt(tx, &model.NodeInboundRelay{}, rel.ID, "managed", rel.Managed); err != nil {
			return 0, err
		}
	}

	// Counter baselines keep the next traffic sample from being counted
	// twice when the restored panel talks to the same nodes.
	for _, c := range b.Counters {
		// D4-03：引用的入站不存在时跳过该基线并计数。
		counterInboundExists, err := rowExistsTx(tx, &model.NodeInbound{}, c.InboundID)
		if err != nil {
			return 0, err
		}
		if !counterInboundExists {
			rep.skip("inboundCounters")
			continue
		}
		item := model.NodeInboundCounter{
			InboundID: c.InboundID, Protocol: c.Protocol, Direction: c.Direction,
			Epoch: c.Epoch, Bytes: c.Bytes, UpdatedTime: now,
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "inbound_id"}, {Name: "protocol"}, {Name: "direction"}},
			DoUpdates: clause.AssignmentColumns([]string{"epoch", "bytes", "updated_time"}),
		}).Create(&item).Error; err != nil {
			return 0, err
		}
	}
	// Subscriptions keep their links and credentials, matched by token.
	// AppliedActive starts at 0 so the expiry loop pushes the active share
	// subscriptions to the kernels.
	subs := append([]model.SubscriptionBackup(nil), b.Subscriptions...)
	for _, u := range b.Users {
		subs = append(subs, model.SubscriptionBackup{
			Name: u.Name, Kind: model.SubscriptionShare, Token: u.Token, UUID: u.UUID, Password: u.Password,
			InboundIDs: u.InboundIDs, ExpireTime: u.ExpireTime, Enabled: u.Enabled, Remark: u.Remark,
			CreatedTime: u.CreatedTime,
		})
	}
	for _, sb := range subs {
		if sb.Token == "" {
			continue
		}
		var existing model.Subscription
		res := tx.Where("token = ?", sb.Token).Limit(1).Find(&existing)
		if res.Error != nil {
			return 0, res.Error
		}
		item := model.Subscription{
			Name: sb.Name, Kind: sb.Kind, Token: sb.Token, UUID: sb.UUID, Password: sb.Password,
			SourceAll: sb.SourceAll, InboundIDs: sb.InboundIDs, SourceIDs: sb.SourceIDs, Options: sb.Options, ExpireTime: sb.ExpireTime,
			Enabled: sb.Enabled, Remark: sb.Remark, Inx: sb.Inx, CreatedTime: sb.CreatedTime, UpdatedTime: now,
			UploadBytes: sb.UploadBytes, DownloadBytes: sb.DownloadBytes, TrafficLimit: sb.TrafficLimit,
			ResetDay: sb.ResetDay, TrafficResetTime: sb.TrafficResetTime,
		}
		// Every subscription has its own credentials (older backups may
		// carry owner-credential ones).
		item.Kind = model.SubscriptionShare
		if item.UUID == "" || item.Password == "" {
			item.UUID, item.Password = subscriptionCredentials()
		}
		if res.RowsAffected > 0 {
			item.ID = existing.ID
		}
		if err := tx.Save(&item).Error; err != nil {
			return 0, err
		}
		// BUG-A2-4：登记备份 id -> 实际落库 id（按 token 命中旧行时
		// 两者可不同；用户派生订阅 sb.ID=0 不参与分配引用，跳过）。
		if sb.ID > 0 && subRemap != nil {
			subRemap[sb.ID] = item.ID
		}
	}

	// A3-04：订阅计数基线随恢复落位。订阅 id 先经 subRemap 按 token
	// 重映射（与分配同口径）；节点/订阅任一不存在则跳过并计数
	//（D4-03）。基线与累计同恢复后，首个样本按差分计，不再整段
	// 重计；旧备份无此段时消费者侧「缺失只建基线」兜底。
	for _, c := range b.SubscriptionCounters {
		subID := c.SubscriptionID
		if mapped, ok := subRemap[subID]; ok {
			subID = mapped
		}
		counterNodeExists, err := rowExistsTx(tx, &model.Node{}, c.NodeID)
		if err != nil {
			return 0, err
		}
		counterSubExists, err := rowExistsTx(tx, &model.Subscription{}, subID)
		if err != nil {
			return 0, err
		}
		if !counterNodeExists || !counterSubExists {
			rep.skip("subscriptionCounters")
			continue
		}
		item := model.SubscriptionCounter{
			NodeID: c.NodeID, SubscriptionID: subID, Direction: c.Direction,
			Epoch: c.Epoch, Bytes: c.Bytes, UpdatedTime: now,
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "node_id"}, {Name: "subscription_id"}, {Name: "direction"}},
			DoUpdates: clause.AssignmentColumns([]string{"epoch", "bytes", "updated_time"}),
		}).Create(&item).Error; err != nil {
			return 0, err
		}
	}
	return len(b.Inbounds), nil
}
