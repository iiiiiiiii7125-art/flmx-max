package handler

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

func TestBuildForwardServiceConfigsAppliesProxyProtocolReceiveAndSendIndependently(t *testing.T) {
	forward := &forwardRecord{
		ID:                   1,
		UserID:               2,
		TunnelID:             3,
		RemoteAddr:           "1.1.1.1:443",
		Strategy:             "fifo",
		ProxyProtocolReceive: 1,
		ProxyProtocolSend:    2,
	}
	tunnel := &tunnelRecord{Type: 1}
	node := &nodeRecord{
		InterfaceName: "eth0",
		TCPListenAddr: "0.0.0.0",
		UDPListenAddr: "0.0.0.0",
	}

	// R18-C5-01：receive 只在 allowlist 非空时装配——此处以
	// 可信发送方 203.0.113.5 入表，TCP 服务保留 receive 并带
	// allowlist（服务 metadata 与 listener metadata 两处）；
	// 无 allowlist 时见 TestAuditC5ProxyProtocolReceiveNoAllowlistDropsReceive。
	services := buildForwardServiceConfigs("1_2_3", forward, tunnel, node, 4001, "", forwardRuntimeLimiters{}, false, []string{"203.0.113.5"})
	if len(services) != 2 {
		t.Fatalf("expected 2 services, got %d", len(services))
	}

	for _, service := range services {
		serviceMetadata, ok := service["metadata"].(map[string]interface{})
		if !ok {
			t.Fatalf("expected metadata map, got %T", service["metadata"])
		}
		if serviceMetadata["interface"] != "eth0" {
			t.Fatalf("expected interface metadata eth0, got %v", serviceMetadata["interface"])
		}

		handlerConfig, ok := service["handler"].(map[string]interface{})
		if !ok {
			t.Fatalf("expected handler config map, got %T", service["handler"])
		}
		handlerMetadata, ok := handlerConfig["metadata"].(map[string]interface{})
		if !ok {
			t.Fatalf("expected handler metadata map, got %T", handlerConfig["metadata"])
		}

		// L1（Fix-1）：PROXY 协议只属于 TCP 服务——TCP 两档保留，
		// UDP 两档都不得出现（UDP 首包会被 PROXY 头污染）。
		if strings.HasSuffix(service["name"].(string), "_tcp") {
			if serviceMetadata["proxyProtocol"] != 1 {
				t.Fatalf("expected service proxyProtocol 1 for receive mode on tcp, got %v", serviceMetadata["proxyProtocol"])
			}
			if allow, _ := serviceMetadata["proxyProtocolAllowlist"].([]string); len(allow) != 1 || allow[0] != "203.0.113.5" {
				t.Fatalf("expected service proxyProtocolAllowlist [203.0.113.5], got %v", serviceMetadata["proxyProtocolAllowlist"])
			}
			listenerConfig, _ := service["listener"].(map[string]interface{})
			listenerMetadata, _ := listenerConfig["metadata"].(map[string]interface{})
			if allow, _ := listenerMetadata["proxyProtocolAllowlist"].([]string); len(allow) != 1 || allow[0] != "203.0.113.5" {
				t.Fatalf("expected listener proxyProtocolAllowlist [203.0.113.5], got %v", listenerMetadata["proxyProtocolAllowlist"])
			}
			if handlerMetadata["proxyProtocol"] != 2 {
				t.Fatalf("expected handler proxyProtocol 2 on tcp, got %v", handlerMetadata["proxyProtocol"])
			}
		} else {
			if _, ok := serviceMetadata["proxyProtocol"]; ok {
				t.Fatalf("udp service must not carry proxyProtocol: %v", serviceMetadata)
			}
			if _, ok := serviceMetadata["proxyProtocolAllowlist"]; ok {
				t.Fatalf("udp service must not carry proxyProtocolAllowlist: %v", serviceMetadata)
			}
			if _, ok := handlerMetadata["proxyProtocol"]; ok {
				t.Fatalf("udp handler must not carry proxyProtocol: %v", handlerMetadata)
			}
		}
	}
}

func TestBuildForwardServiceConfigsKeepsLegacyProxyProtocolAsSend(t *testing.T) {
	forward := &forwardRecord{
		ID:            1,
		UserID:        2,
		TunnelID:      3,
		RemoteAddr:    "1.1.1.1:443",
		Strategy:      "fifo",
		ProxyProtocol: 2,
	}
	tunnel := &tunnelRecord{Type: 1}
	node := &nodeRecord{
		TCPListenAddr: "0.0.0.0",
		UDPListenAddr: "0.0.0.0",
	}

	services := buildForwardServiceConfigs("1_2_3", forward, tunnel, node, 4001, "", forwardRuntimeLimiters{}, false, nil)
	if len(services) != 2 {
		t.Fatalf("expected 2 services, got %d", len(services))
	}

	for _, service := range services {
		serviceMetadata, _ := service["metadata"].(map[string]interface{})
		if _, ok := serviceMetadata["proxyProtocol"]; ok {
			t.Fatalf("legacy proxyProtocol should not enable receive mode: %v", serviceMetadata)
		}

		handlerConfig, ok := service["handler"].(map[string]interface{})
		if !ok {
			t.Fatalf("expected handler config map, got %T", service["handler"])
		}
		handlerMetadata, ok := handlerConfig["metadata"].(map[string]interface{})
		if !ok {
			t.Fatalf("expected handler metadata map, got %T", handlerConfig["metadata"])
		}
		// L1（Fix-1）：legacy proxyProtocol 只作 TCP 发送档。
		if strings.HasSuffix(service["name"].(string), "_tcp") {
			if handlerMetadata["proxyProtocol"] != 2 {
				t.Fatalf("expected legacy proxyProtocol to send version 2 on tcp, got %v", handlerMetadata["proxyProtocol"])
			}
		} else if _, ok := handlerMetadata["proxyProtocol"]; ok {
			t.Fatalf("udp handler must not carry legacy proxyProtocol: %v", handlerMetadata)
		}
	}
}

// TestBuildForwardServiceConfigsProxyProtocolTCPOnly 是 L1 的差集门：
// 无论收/发档如何组合，生成的 UDP 服务在服务 metadata 与 handler
// metadata 两层都不得出现 proxyProtocol 键，TCP 服务则原样保留。
func TestBuildForwardServiceConfigsProxyProtocolTCPOnly(t *testing.T) {
	forward := &forwardRecord{
		ID:                   1,
		UserID:               2,
		TunnelID:             3,
		RemoteAddr:           "1.1.1.1:443",
		Strategy:             "fifo",
		ProxyProtocolReceive: 1,
		ProxyProtocolSend:    2,
	}
	node := &nodeRecord{
		TCPListenAddr: "0.0.0.0",
		UDPListenAddr: "0.0.0.0",
	}

	// R18-C5-01：receive=1 时 allowlist 非空才保留 receive（此处
	// 以可信发送方 203.0.113.5 入表）；allowlist 为空的反例见
	// TestAuditC5ProxyProtocolReceiveNoAllowlistDropsReceive。
	services := buildForwardServiceConfigs("1_2_3", forward, &tunnelRecord{Type: 2}, node, 4001, "", forwardRuntimeLimiters{}, false, []string{"203.0.113.5"})
	if len(services) != 2 {
		t.Fatalf("expected 2 services, got %d", len(services))
	}
	for _, service := range services {
		name, _ := service["name"].(string)
		serviceMetadata, _ := service["metadata"].(map[string]interface{})
		handlerConfig, _ := service["handler"].(map[string]interface{})
		handlerMetadata, _ := handlerConfig["metadata"].(map[string]interface{})
		if strings.HasSuffix(name, "_udp") {
			if _, ok := serviceMetadata["proxyProtocol"]; ok {
				t.Fatalf("udp service %q must not carry service proxyProtocol: %v", name, serviceMetadata)
			}
			if _, ok := serviceMetadata["proxyProtocolAllowlist"]; ok {
				t.Fatalf("udp service %q must not carry service proxyProtocolAllowlist: %v", name, serviceMetadata)
			}
			if _, ok := handlerMetadata["proxyProtocol"]; ok {
				t.Fatalf("udp service %q must not carry handler proxyProtocol: %v", name, handlerMetadata)
			}
			continue
		}
		if !strings.HasSuffix(name, "_tcp") {
			t.Fatalf("unexpected service name %q", name)
		}
		if serviceMetadata["proxyProtocol"] != 1 {
			t.Fatalf("tcp service must keep receive proxyProtocol 1, got %v", serviceMetadata["proxyProtocol"])
		}
		if allow, _ := serviceMetadata["proxyProtocolAllowlist"].([]string); len(allow) != 1 || allow[0] != "203.0.113.5" {
			t.Fatalf("tcp service must carry proxyProtocolAllowlist [203.0.113.5], got %v", serviceMetadata["proxyProtocolAllowlist"])
		}
		if handlerMetadata["proxyProtocol"] != 2 {
			t.Fatalf("tcp service must keep send proxyProtocol 2, got %v", handlerMetadata["proxyProtocol"])
		}
	}
}

func TestRollbackForwardMutationRestoresProxyProtocol(t *testing.T) {
	r, err := repo.Open(":memory:")
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	now := time.Now().UnixMilli()
	if err := r.DB().Create(&model.Forward{
		UserID:        2,
		UserName:      "rollback-user",
		Name:          "rollback-forward",
		TunnelID:      3,
		RemoteAddr:    "9.9.9.9:443",
		Strategy:      "fifo",
		CreatedTime:   now,
		UpdatedTime:   now,
		Status:        1,
		IPMaxConn:     5,
		IPSpeedID:     sql.NullInt64{Int64: 21, Valid: true},
		ProxyProtocol: 2,
	}).Error; err != nil {
		t.Fatalf("create forward: %v", err)
	}

	forwardID := mustLastInsertID(t, r, "rollback-forward")
	if err := r.DB().Model(&model.Forward{}).Where("id = ?", forwardID).Updates(map[string]interface{}{
		"name":           "changed-forward",
		"ip_max_conn":    0,
		"ip_speed_id":    nil,
		"proxy_protocol": 0,
		"updated_time":   now + 1,
	}).Error; err != nil {
		t.Fatalf("mutate forward: %v", err)
	}

	h := &Handler{repo: r}
	h.rollbackForwardMutation(&forwardRecord{
		ID:            forwardID,
		UserID:        2,
		UserName:      "rollback-user",
		Name:          "rollback-forward",
		TunnelID:      3,
		RemoteAddr:    "9.9.9.9:443",
		Strategy:      "fifo",
		Status:        1,
		IPMaxConn:     5,
		IPSpeedID:     sql.NullInt64{Int64: 21, Valid: true},
		ProxyProtocol: 2,
	}, nil)

	var record model.Forward
	if err := r.DB().Where("id = ?", forwardID).First(&record).Error; err != nil {
		t.Fatalf("query forward: %v", err)
	}
	if record.ProxyProtocol != 2 {
		t.Fatalf("expected proxyProtocol restored to 2, got %d", record.ProxyProtocol)
	}
	if record.IPMaxConn != 5 {
		t.Fatalf("expected ipMaxConn restored to 5, got %d", record.IPMaxConn)
	}
	if !record.IPSpeedID.Valid || record.IPSpeedID.Int64 != 21 {
		t.Fatalf("expected ipSpeedId restored to 21, got %+v", record.IPSpeedID)
	}
}
