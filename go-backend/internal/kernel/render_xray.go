package kernel

import (
	"fmt"
	"strconv"
	"strings"
)

type obj = map[string]interface{}

func renderXray(inbounds []Inbound) (string, error) {
	// Per-inbound idle timeout: every inbound with ConnIdleSeconds set
	// gets its own policy level (1..N, in inbound-ID order — inbounds
	// arrive ID-sorted from RenderWith, so the assignment is stable
	// across renders). Level 0 is never written: it is Xray's shared
	// default and touching it would retime every other inbound on the
	// node. Clients carry their inbound's level (see xraySettings /
	// xrayUserClients); the level numbers are render-internal — no
	// subscription link or share URI carries them, so a reshuffle after
	// a delete is harmless.
	levels, err := connIdleLevels(inbounds)
	if err != nil {
		return "", err
	}
	list := make([]interface{}, 0, len(inbounds))
	for _, in := range inbounds {
		item, err := xrayInbound(in, levels[in.ID])
		if err != nil {
			return "", fmt.Errorf("入站 %d: %v", in.ID, err)
		}
		list = append(list, item)
	}
	cfg := obj{
		"log":      obj{"loglevel": "warning"},
		"inbounds": list,
		"outbounds": []interface{}{
			obj{"protocol": "freedom", "tag": "direct"},
			obj{"protocol": "blackhole", "tag": "block"},
		},
	}
	if len(levels) > 0 {
		lv := obj{}
		for _, in := range inbounds {
			if l, ok := levels[in.ID]; ok {
				lv[strconv.Itoa(l)] = obj{"connIdle": in.ConnIdleSeconds}
			}
		}
		cfg["policy"] = obj{"levels": lv}
	}
	// Chained inbounds leave through their target; their rules come first
	// so their users do not match the direct per-user rules below.
	chained, rules, err := xrayChainRoutes(inbounds)
	if err != nil {
		return "", err
	}
	outbounds := append(cfg["outbounds"].([]interface{}), chained...)
	// Each user leaves through a direct outbound whose sockets carry the
	// user's mark (see UserMark).
	for _, id := range markedUsers(inbounds) {
		tag := userOutboundTag(id)
		outbounds = append(outbounds, obj{"protocol": "freedom", "tag": tag,
			"streamSettings": obj{"sockopt": obj{"mark": UserMark(id)}}})
		rules = append(rules, obj{"type": "field", "user": []string{User{ID: id}.Email()}, "outboundTag": tag})
	}
	cfg["outbounds"] = outbounds
	if len(rules) > 0 {
		cfg["routing"] = obj{"rules": rules}
	}
	if err := applyOutboundIPPolicies(TypeXray, cfg, inbounds, false); err != nil {
		return "", err
	}
	return marshalConfig(cfg)
}

// connIdleLevels assigns one policy level per inbound that sets an idle
// timeout, in inbound-ID order starting at 1. Inbounds without a value
// get no entry and stay on Xray's level-0 default.
func connIdleLevels(inbounds []Inbound) (map[int64]int, error) {
	levels := make(map[int64]int)
	next := 0
	for _, in := range inbounds {
		if in.ConnIdleSeconds == 0 {
			continue
		}
		next++
		if next > maxConnIdleLevel {
			return nil, fmt.Errorf("入站 ID %d 的空闲超时无法分配 level：同一内核下带空闲超时的入站不能超过 %d 个（SS 的 level 字段为单字节）", in.ID, maxConnIdleLevel)
		}
		levels[in.ID] = next
	}
	return levels, nil
}

func xrayInbound(in Inbound, level int) (obj, error) {
	settings, err := xraySettings(in, level)
	if err != nil {
		return nil, err
	}
	item := obj{
		"tag":      in.Tag(),
		"listen":   in.Listen,
		"port":     in.Port,
		"protocol": in.Protocol,
		"settings": settings,
		"sniffing": obj{
			"enabled":      true,
			"destOverride": []string{"http", "tls", "quic"},
		},
	}
	if in.Protocol != ProtocolShadowsocks {
		item["streamSettings"] = xrayStream(in)
	} else if in.AcceptProxyProtocol {
		item["streamSettings"] = obj{"network": TransportTCP}
	}
	if in.AcceptProxyProtocol {
		stream := item["streamSettings"].(obj)
		stream["sockopt"] = obj{"acceptProxyProtocol": true}
	}
	if err := mergeExtra(item, in.Extra); err != nil {
		return nil, err
	}
	return item, nil
}

func xraySettings(in Inbound, level int) (obj, error) {
	s := in.Settings
	switch in.Protocol {
	case ProtocolVLESS:
		client := obj{"id": s.UUID}
		if s.Flow != "" {
			client["flow"] = s.Flow
		}
		if level > 0 {
			client["level"] = level
		}
		return obj{"clients": append([]interface{}{client}, xrayUserClients(in, level)...), "decryption": s.DecryptionString()}, nil
	case ProtocolVMess:
		client := obj{"id": s.UUID}
		if level > 0 {
			client["level"] = level
		}
		return obj{"clients": append([]interface{}{client}, xrayUserClients(in, level)...)}, nil
	case ProtocolTrojan:
		client := obj{"password": s.Password}
		if level > 0 {
			client["level"] = level
		}
		return obj{"clients": append([]interface{}{client}, xrayUserClients(in, level)...)}, nil
	case ProtocolShadowsocks:
		// GATE-F1：同 sing-box 服务端口径，合法单段 SS2022 key 归一 Std。
		out := obj{"method": s.Method, "password": NormalizedSS2022Password(s.Method, s.Password), "network": "tcp,udp"}
		// The top-level settings level is the single-password user's
		// level (upstream Xray ShadowsocksServerConfig.level); without
		// it the primary credential would stay on level 0 and the
		// per-inbound timeout would silently miss it.
		if level > 0 {
			out["level"] = level
		}
		// SS2022 multi-user (companion inbounds only): clients carry the
		// subscriptions; the top-level password stays the identity PSK
		// and no owner client is added — a companion has no owner user.
		if in.UserCapable() {
			if clients := xrayUserClients(in, level); len(clients) > 0 {
				out["clients"] = clients
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("Xray 不支持协议 %s", in.Protocol)
}

// canonicalXHTTPMode 是 R12X-002 自建渲染侧归一：备份逐字恢复变体
// （R5X-024 受保护行为，落库逐字不动）绕过 Normalize 直达原文消费点，
// 故渲染时再做一次小写愈合——与 normalizeTransport 的合法集
// {auto, packet-up, stream-up, stream-one} 同集（EqualFold 意义下）；
// 愈合后仍域外（含空值）回退默认 auto，安全方向，与 chain.go 空值
// 回退同向。只愈合渲染产物，绝不回写原对象、不改落库值、
// 不改 normalizeTransport 本体。
func canonicalXHTTPMode(mode string) string {
	healed := strings.ToLower(strings.TrimSpace(mode))
	if !xhttpModes[healed] {
		return defaultXHTTPMode
	}
	return healed
}

func xrayStream(in Inbound) obj {
	t := in.Transport
	stream := obj{"network": t.Type, "security": in.Security.Type}
	switch t.Type {
	case TransportWS:
		ws := obj{"path": t.Path}
		if t.Host != "" {
			ws["host"] = t.Host
		}
		stream["wsSettings"] = ws
	case TransportGRPC:
		stream["grpcSettings"] = obj{"serviceName": t.ServiceName}
	case TransportXHTTP:
		x := obj{"path": t.Path, "mode": canonicalXHTTPMode(t.Mode)}
		if t.Host != "" {
			x["host"] = t.Host
		}
		stream["xhttpSettings"] = x
	}

	sec := in.Security
	switch sec.Type {
	case SecurityTLS:
		tls := obj{"serverName": sec.SNI}
		if len(sec.ALPN) > 0 {
			tls["alpn"] = sec.ALPN
		}
		if sec.CertMode == "file" {
			tls["certificates"] = []interface{}{obj{"certificateFile": sec.CertFile, "keyFile": sec.KeyFile}}
		} else {
			tls["certificates"] = []interface{}{obj{
				"certificate": pemLines(sec.CertPEM),
				"key":         pemLines(sec.KeyPEM),
			}}
		}
		stream["tlsSettings"] = tls
	case SecurityReality:
		stream["realitySettings"] = obj{
			"show":        false,
			"dest":        sec.RealityDest,
			"xver":        0,
			"serverNames": []string{sec.SNI},
			"privateKey":  sec.RealityPrivateKey,
			"shortIds":    sec.RealityShortIDs,
		}
		// Inbounds saved before the field existed get the default too.
		minVer := sec.RealityMinClientVer
		if minVer == "" {
			minVer = DefaultRealityMinClientVer
		}
		stream["realitySettings"].(obj)["minClientVer"] = minVer
	}
	return stream
}

// pemLines splits a PEM block into the line array form Xray accepts inline.
func pemLines(pem string) []string {
	lines := strings.Split(strings.TrimSpace(pem), "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}
