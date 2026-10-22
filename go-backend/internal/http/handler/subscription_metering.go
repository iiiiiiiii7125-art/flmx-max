package handler

import (
	"log"
	"sync"
	"sync/atomic"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// ---- SS subscription metering: companion inbounds ----
//
// Shadowsocks is a shared-credential protocol, so the per-user counter
// chain (kernel mark routing -> agent nft counters ->
// ApplySubscriptionCounterSamples) has nothing to attribute: the
// kernel sees one password for everyone. SS2022 multi-user mode could
// distinguish users, but enabling it on the origin inbound in place
// would break every already-distributed link (a bare identity PSK is
// rejected in multi-user mode) — so metering rides on a derived
// COMPANION inbound instead (additive compatibility):
//
//   - the origin inbound is never touched: old links keep working,
//     unmetered, exactly as before;
//   - the companion is a real NodeInbound row (companion_of = origin
//     id) on the same node/kernel with its own port and a fresh
//     identity PSK; it carries the origin's active share subscriptions
//     as 2022 multi-user clients (u{id} identities, derived uPSKs);
//   - share subscriptions' new direct SS endpoints are retargeted to
//     the companion with the combined iPSK:uPSK password, so their
//     traffic flows through the existing mark counters unchanged.
//
// Companions are derived state: reconcile materializes, syncs and
// recycles them from the origins' metering switches and the active
// share-subscription demand. They are hidden from inbound lists,
// subscription sources and backups, and are never edited by hand.

var meteringMu sync.Mutex
var lastMeteringReconcile atomic.Int64

// meteringReconcileInterval throttles the reconcile on the (frequent)
// subscription sync beat; explicit triggers (inbound save/delete) run
// it immediately.
const meteringReconcileInterval = 30 * time.Second

// ssMeteringEligible reports whether a self-built inbound can carry SS
// subscription metering at all: Shadowsocks with a 2022 method and no
// ShadowTLS front (the front would need its own companion pair; v1
// boundary). Legacy SS methods have no multi-user mode in either
// kernel, and Snell has no user concept at all.
func ssMeteringEligible(in kernel.Inbound) bool {
	return in.Protocol == kernel.ProtocolShadowsocks &&
		kernel.SS2022KeyLen(in.Settings.Method) > 0 &&
		in.Security.Type != kernel.SecurityShadowTLS
}

// ssMeteringWanted reports whether the origin should currently have a
// companion: switch on, eligible, direct-serving (a relay-only,
// PROXY-protocol or chained-exit origin has no direct subscription
// endpoints to retarget, and a companion would silently reroute them),
// and demanded by at least one active share subscription.
func (h *Handler) ssMeteringWanted(origin *model.NodeInbound, demand map[int64][]kernel.User) bool {
	if origin == nil || origin.CompanionOf > 0 || origin.MeteringEnabled != 1 {
		return false
	}
	if isExternalInbound(origin) || origin.RelayOnly == 1 || origin.AcceptProxyProtocol == 1 || origin.OutboundID != 0 {
		return false
	}
	if len(demand[origin.ID]) == 0 {
		return false
	}
	in, err := inboundFromModel(*origin)
	if err != nil {
		return false
	}
	return ssMeteringEligible(in)
}

// companionName is the companion row's display name. It never reaches
// clients (subscription endpoints keep the origin's name); it only
// makes the row recognizable in the database.
func companionName(originName string) string {
	const suffix = "（计量）"
	r := []rune(originName)
	if len(r) > 96 {
		r = r[:96]
	}
	return string(r) + suffix
}

// reconcileSSMetering brings the companion set in line with the
// origins' switches and the current share-subscription demand. It is
// idempotent, self-healing (orphans and duplicates are removed) and
// failure-tolerant: a failed step is logged and retried on the next
// run, mirroring the subscription push discipline (AUDIT-R8/F7X-001).
func (h *Handler) reconcileSSMetering() {
	meteringMu.Lock()
	defer meteringMu.Unlock()
	lastMeteringReconcile.Store(time.Now().UnixMilli())

	rows, err := h.repo.ListNodeInbounds()
	if err != nil {
		log.Printf("ss metering reconcile: list inbounds: %v", err)
		return
	}
	demand := h.rawActiveUsersByInbound(time.Now())
	now := time.Now().UnixMilli()

	origins := make(map[int64]*model.NodeInbound, len(rows))
	companions := make(map[int64][]model.NodeInbound)
	for i := range rows {
		r := &rows[i]
		if r.CompanionOf > 0 {
			companions[r.CompanionOf] = append(companions[r.CompanionOf], *r)
		} else {
			origins[r.ID] = r
		}
	}

	affected := map[int64]bool{}
	for originID, comps := range companions {
		origin, ok := origins[originID]
		if !ok || !h.ssMeteringWanted(origin, demand) {
			for _, c := range comps {
				if h.deleteCompanionRow(&c) {
					affected[c.KernelID] = true
				}
			}
			continue
		}
		for _, extra := range comps[1:] {
			if h.deleteCompanionRow(&extra) {
				affected[extra.KernelID] = true
			}
		}
		if h.syncCompanion(origin, &comps[0], now) {
			affected[comps[0].KernelID] = true
		}
	}
	for id, origin := range origins {
		if _, exists := companions[id]; exists {
			continue
		}
		if h.ssMeteringWanted(origin, demand) && h.createCompanion(origin, now) {
			affected[origin.KernelID] = true
		}
	}
	for kernelID := range affected {
		if err := h.applyKernel(kernelID, false); err != nil {
			log.Printf("ss metering reconcile: apply kernel %d: %v", kernelID, err)
		}
	}
}

// maybeReconcileSSMetering runs the reconcile at most once per
// interval; it rides the subscription sync beat.
func (h *Handler) maybeReconcileSSMetering() {
	if last := lastMeteringReconcile.Load(); last > 0 &&
		time.Since(time.UnixMilli(last)) < meteringReconcileInterval {
		return
	}
	h.reconcileSSMetering()
}

// createCompanion materializes the companion row for an origin. The
// port pick and the insert run under the node's port-allocation lock,
// and existence is re-checked inside it, so concurrent triggers
// cannot create duplicates (the partial unique index on companion_of
// is the backstop).
func (h *Handler) createCompanion(origin *model.NodeInbound, now int64) bool {
	lock := repo.AcquirePortAllocLock(origin.NodeID)
	defer repo.ReleasePortAllocLock(lock)
	if existing, err := h.repo.GetCompanionInbound(origin.ID); err != nil {
		log.Printf("ss metering: lookup companion of inbound %d: %v", origin.ID, err)
		return false
	} else if existing != nil {
		return false
	}
	port, err := h.pickFreeInboundPort(origin.NodeID, 0)
	if err != nil {
		log.Printf("ss metering: pick port for companion of inbound %d: %v", origin.ID, err)
		return false
	}
	in, err := inboundFromModel(*origin)
	if err != nil {
		log.Printf("ss metering: read origin inbound %d: %v", origin.ID, err)
		return false
	}
	in.ID = 0
	in.Port = port
	// Fresh identity PSK; the origin's shared password is never reused
	// or exposed on the companion.
	in.Settings.Password = kernel.ShadowsocksPassword(in.Settings.Method)
	in.Users = nil
	in.Via = nil
	in.Split = nil
	in.AcceptProxyProtocol = false

	row := model.NodeInbound{
		KernelID:         origin.KernelID,
		NodeID:           origin.NodeID,
		Name:             companionName(origin.Name),
		Remark:           "SS 订阅计量伴生入站（系统管理，请勿编辑）",
		ShareAddressMode: origin.ShareAddressMode,
		ShareHost:        origin.ShareHost,
		Enabled:          origin.Enabled,
		Inx:              origin.Inx,
		CompanionOf:      origin.ID,
		CreatedTime:      now,
		UpdatedTime:      now,
	}
	if err := applyInboundToModel(in, &row); err != nil {
		log.Printf("ss metering: build companion of inbound %d: %v", origin.ID, err)
		return false
	}
	if err := h.repo.CreateNodeInbound(&row); err != nil {
		log.Printf("ss metering: create companion of inbound %d: %v", origin.ID, err)
		return false
	}
	return true
}

// syncCompanion folds origin changes (listen, transport, security,
// settings, enabled) into an existing companion. The companion keeps
// its port and its identity PSK; the PSK is regenerated only when the
// method changes, since the key length and the uPSK derivation both
// follow the method.
func (h *Handler) syncCompanion(origin, comp *model.NodeInbound, now int64) bool {
	desired, err := inboundFromModel(*origin)
	if err != nil {
		return false
	}
	current, err := inboundFromModel(*comp)
	if err != nil {
		return false
	}
	password := current.Settings.Password
	if desired.Settings.Method != current.Settings.Method || password == "" {
		password = kernel.ShadowsocksPassword(desired.Settings.Method)
	}
	desired.ID = comp.ID
	desired.Port = comp.Port
	desired.Settings.Password = password
	desired.Users = nil
	desired.Via = nil
	desired.Split = nil
	desired.AcceptProxyProtocol = false

	updated := *comp
	if err := applyInboundToModel(desired, &updated); err != nil {
		return false
	}
	updated.Name = companionName(origin.Name)
	updated.Enabled = origin.Enabled
	if updated.Settings == comp.Settings && updated.Transport == comp.Transport &&
		updated.Security == comp.Security && updated.Listen == comp.Listen &&
		updated.Extra == comp.Extra && updated.OutboundIPStrategy == comp.OutboundIPStrategy &&
		updated.Name == comp.Name && updated.Enabled == comp.Enabled {
		return false
	}
	updated.UpdatedTime = now
	if err := h.repo.SaveNodeInbound(&updated); err != nil {
		log.Printf("ss metering: sync companion %d of inbound %d: %v", comp.ID, origin.ID, err)
		return false
	}
	return true
}

func (h *Handler) deleteCompanionRow(comp *model.NodeInbound) bool {
	if err := h.repo.DeleteNodeInbound(comp.ID); err != nil {
		log.Printf("ss metering: delete companion %d of inbound %d: %v", comp.ID, comp.CompanionOf, err)
		return false
	}
	return true
}

// deleteCompanionOf removes an origin's companion row ahead of the
// origin's own deletion (the delete path's kernel push covers the
// change). Companion rows have no relays of their own, but the relay
// cleanup runs anyway for symmetry with origin deletes.
func (h *Handler) deleteCompanionOf(originID int64) {
	comp, err := h.repo.GetCompanionInbound(originID)
	if err != nil || comp == nil {
		return
	}
	h.removeInboundRelays(comp.ID)
	if err := h.repo.DeleteNodeInbound(comp.ID); err != nil {
		log.Printf("ss metering: delete companion %d of inbound %d: %v", comp.ID, originID, err)
	}
}

// companionInboundsByOrigin maps origin inbound ids to their live
// companions' kernel form (enabled companions only), for subscription
// endpoint retargeting. Errors degrade to no retargeting — the origin
// endpoints are then served shared, exactly as before this feature.
func (h *Handler) companionInboundsByOrigin() map[int64]kernel.Inbound {
	comps, err := h.repo.ListCompanionInbounds()
	if err != nil {
		log.Printf("ss metering: list companions: %v", err)
		return nil
	}
	out := make(map[int64]kernel.Inbound, len(comps))
	for _, c := range comps {
		if c.Enabled != 1 {
			continue
		}
		if _, dup := out[c.CompanionOf]; dup {
			continue
		}
		in, err := inboundFromModel(c)
		if err != nil {
			continue
		}
		out[c.CompanionOf] = in
	}
	return out
}
