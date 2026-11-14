package kernel

import (
	"fmt"
	"sort"
)

// User is an extra client of an inbound (a subscription user). Each user
// has its own credentials so it can be cut off without touching others.
type User struct {
	ID       int64
	UUID     string
	Password string
}

// Email tags the user in Xray configs (must be unique per inbound).
func (u User) Email() string { return fmt.Sprintf("u%d@flmx", u.ID) }

// Name tags the user in sing-box configs and is the NaïveProxy username.
func (u User) Name() string { return fmt.Sprintf("u%d", u.ID) }

// SupportsUsers reports whether the inbound can carry per-user credentials.
// Shadowsocks stays shared: 2022 multi-user mode would require every
// client, the owner's included, to send a user identity, breaking
// existing links.
func (in Inbound) SupportsUsers() bool {
	switch in.Protocol {
	case ProtocolVLESS, ProtocolVMess, ProtocolTrojan, ProtocolHysteria2, ProtocolTUIC, ProtocolAnyTLS, ProtocolNaive:
		return true
	}
	return false
}

// UserCapable reports whether this inbound actually carries per-user
// credentials in a kernel config: natively user-capable protocols, plus
// SS companion inbounds (Shadowsocks 2022 multi-user). The capability is
// gated per inbound, never per protocol — flipping it for Shadowsocks
// itself would switch every existing SS inbound to multi-user mode in
// place and break its already-distributed links; the origin inbound
// therefore stays shared and only its derived companion is capable.
func (in Inbound) UserCapable() bool {
	if in.SupportsUsers() {
		return true
	}
	return in.Companion && in.Protocol == ProtocolShadowsocks
}

// ForUser returns the inbound as the given user connects to it: the same
// server with the user's credentials. Inbounds without user support are
// returned unchanged (shared credentials).
func (in Inbound) ForUser(u User) Inbound {
	if !in.SupportsUsers() {
		// SS companion inbound: the client password is the combined
		// iPSK:uPSK (the inbound's own password is the identity PSK,
		// the user's derived PSK identifies the subscription). No other
		// SS inbound reaches this branch — origins stay shared.
		if in.UserCapable() {
			if psk := SSUserPSK(u.Password, u.ID, in.Settings.Method); psk != "" {
				out := in
				out.Users = nil
				out.Settings.Password = in.Settings.Password + ":" + psk
				return out
			}
		}
		return in
	}
	out := in
	out.Users = nil
	switch in.Protocol {
	case ProtocolVLESS, ProtocolVMess:
		out.Settings.UUID = u.UUID
	case ProtocolTUIC:
		out.Settings.UUID = u.UUID
		out.Settings.Password = u.Password
	case ProtocolNaive:
		out.Settings.Username = u.Name()
		out.Settings.Password = u.Password
	default:
		out.Settings.Password = u.Password
	}
	return out
}

// xrayUserClients returns the extra Xray clients for the inbound's users.
// level is the inbound's policy level (0 = default): every client carries
// it so the per-inbound idle timeout applies to subscription users too.
func xrayUserClients(in Inbound, level int) []interface{} {
	if !in.UserCapable() {
		return nil
	}
	out := make([]interface{}, 0, len(in.Users))
	for _, u := range in.Users {
		var c obj
		switch in.Protocol {
		case ProtocolVLESS:
			c = obj{"id": u.UUID}
			if in.Settings.Flow != "" {
				c["flow"] = in.Settings.Flow
			}
		case ProtocolVMess:
			c = obj{"id": u.UUID}
		case ProtocolTrojan:
			c = obj{"password": u.Password}
		case ProtocolShadowsocks:
			// SS2022 multi-user (companion inbounds only): the client
			// password is the user's derived PSK; email carries the
			// identity the per-user routing rules match on.
			psk := SSUserPSK(u.Password, u.ID, in.Settings.Method)
			if psk == "" {
				continue
			}
			c = obj{"password": psk}
		default:
			continue
		}
		c["email"] = u.Email()
		if level > 0 {
			c["level"] = level
		}
		out = append(out, c)
	}
	return out
}

// singboxUsers returns the extra sing-box users for the inbound's users.
func singboxUsers(in Inbound) []interface{} {
	if !in.UserCapable() {
		return nil
	}
	out := make([]interface{}, 0, len(in.Users))
	for _, u := range in.Users {
		var item obj
		switch in.Protocol {
		case ProtocolVLESS:
			item = obj{"uuid": u.UUID}
			if in.Settings.Flow != "" {
				item["flow"] = in.Settings.Flow
			}
		case ProtocolVMess:
			item = obj{"uuid": u.UUID, "alterId": 0}
		case ProtocolTrojan, ProtocolHysteria2, ProtocolAnyTLS:
			item = obj{"password": u.Password}
		case ProtocolShadowsocks:
			// SS2022 multi-user (companion inbounds only): the user
			// password is the subscription's derived PSK; the name
			// below carries the identity auth_user rules match on.
			psk := SSUserPSK(u.Password, u.ID, in.Settings.Method)
			if psk == "" {
				continue
			}
			item = obj{"password": psk}
		case ProtocolTUIC:
			item = obj{"uuid": u.UUID, "password": u.Password}
		case ProtocolNaive:
			out = append(out, obj{"username": u.Name(), "password": u.Password})
			continue
		default:
			continue
		}
		item["name"] = u.Name()
		out = append(out, item)
	}
	return out
}

// userMarkBase tags the sockets of a user's outgoing connections ("FV" +
// user id). The agent counts each mark in nftables: packets out with the
// mark are the user's upload, packets back on the marked connection the
// download. This works on official builds of both kernels (sing-box ships
// without a per-user stats API).
//
// F2-DBL-01: the legacy base is host-global — two agents on one host
// install identical mark rules in their own nft tables, so the same
// physical packets are counted once per agent and the panel books the
// subscription N×. The per-agent layout keeps the user id in the low
// 16 bits and spends bits 16-23 on the agent (bits 24-31 are the tag):
// mark = 0x47000000 | (agentByte << 16) | id, where agentByte is the
// panel node id's low byte. Node ids are sequential, so co-located
// agents (a handful per host, ids far below 256 apart) always get
// distinct bases; a collision would need two co-located nodes whose
// ids differ by exactly a multiple of 256, in which case only those
// two nodes double-count each other (the old behaviour, not a new
// failure mode).
//
// F-R4-01: the per-agent tag is 0x47, NOT 0x46 — the old 0x46 tag made
// the per-agent space contain the legacy base itself (node id ≡ 86
// mod 256 minted 0x46560000 = LegacyUserMarkBase), so a new agent on
// such a node double-counted with every old agent on the host during
// the transition. The 0x47 space is disjoint from legacy by
// construction, for every node id. The legacy base stays 0x46560000
// for agents older than the per-agent rollout.
const (
	userMarkBase = 0x46560000
	userMarkMax  = 0xffff
)

// LegacyUserMarkBase is the host-global socket-mark base used before
// F2-DBL-01 (and still used for agents older than the per-agent-base
// rollout): user marks are LegacyUserMarkBase | id.
const LegacyUserMarkBase int64 = userMarkBase

// UserMarkBaseForAgent returns the socket-mark base for one agent (one
// panel node). 0x47000000 | ((nodeID & 0xff) << 16) — the 0x47 tag
// keeps the per-agent space disjoint from the legacy base (F-R4-01).
func UserMarkBaseForAgent(nodeID int64) int64 {
	return 0x47000000 | ((nodeID & 0xff) << 16)
}

// UserMarkForAgent is the socket mark of a user's traffic on one agent;
// 0 when the id does not fit (id > MaxUserMarkID), and the user is then
// not counted. base == 0 means the legacy host-global base (old agents
// and renders that predate the per-agent layout).
func UserMarkForAgent(base, id int64) int64 {
	if id <= 0 || id > userMarkMax {
		return 0
	}
	if base == 0 {
		base = userMarkBase
	}
	return base | id
}

// MaxUserMarkID is the largest subscription (user) id the socket-mark
// metering scheme can encode. The mark layout used to be mirrored by the
// agent (go-gost/x/kernel carried the same constants); since F2-DBL-01
// the panel is authoritative — it renders the per-agent base into the
// kernel config and sends the same base in the KernelApply payload, and
// the agent's nft rules use the payload base (falling back to the legacy
// base when the payload omits it, i.e. an older panel). Widening the id
// field is still a coordinated two-sided protocol change: during a
// mixed-version rollout the two sides would mint different marks and
// miscount silently. Until such a change ships, ids above this limit
// are guarded explicitly on the panel side (L4-08): the subscription
// view flags them (meteringUnsupported) and the mount path logs a
// one-time warning, instead of the previous silent drop.
const MaxUserMarkID = userMarkMax

// UserMark is the socket mark of the user's traffic; 0 when the id does not
// fit (id > MaxUserMarkID), and the user is then not counted. Legacy
// host-global layout (0x46560000 | id); new renders use UserMarkForAgent
// with the per-agent base.
func UserMark(id int64) int64 {
	return UserMarkForAgent(userMarkBase, id)
}

// UserFromMark is the inverse of UserMark/UserMarkForAgent (0 for other
// marks). Both layouts decode the same way: the user id sits in the low
// 16 bits, and the top byte is 0x46 for legacy (0x4656xxxx) or 0x47
// for per-agent (0x47aaxxxx) — the 0x46 tag also decodes pre-fix
// per-agent marks from agents that have not re-applied yet, so a
// panel upgrade never blinds in-flight counters.
func UserFromMark(mark int64) int64 {
	top := mark >> 24
	if top != 0x46 && top != 0x47 {
		return 0
	}
	return mark & userMarkMax
}

// markedUsers returns the ids of the counted users, sorted.
func markedUsers(inbounds []Inbound) []int64 {
	seen := map[int64]bool{}
	var ids []int64
	for _, in := range inbounds {
		if !in.UserCapable() {
			continue
		}
		for _, u := range in.Users {
			if in.userMark(u.ID) != 0 && !seen[u.ID] {
				seen[u.ID] = true
				ids = append(ids, u.ID)
			}
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func userOutboundTag(id int64) string { return fmt.Sprintf("flmx-u%d", id) }
