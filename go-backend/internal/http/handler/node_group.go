package handler

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go-backend/internal/http/response"
	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
)

// Node groups: reusable node lists for subscriptions. A group picks
// inbounds and remote sources and processes their nodes (filter, rename,
// flag, prefix, dedupe, sort, script); a subscription splices the nodes
// of its groups, in the order it lists them, in front of its own
// sources. The subscription's own sources and pipeline are unchanged:
// groups are a reuse layer on top, not a replacement.

// groupPipeline is the JSON node processing stored per node group: the
// subscription pipeline minus the parts that only make sense per
// subscription (chain, rules, region groups, relay scope).
type groupPipeline struct {
	Lines     string   `json:"lines"`
	Regions   []string `json:"regions"`
	Protocols []string `json:"protocols"`
	Include   string   `json:"include"`
	Exclude   string   `json:"exclude"`
	Rename    string   `json:"rename"`
	AddFlag   bool     `json:"addFlag"`
	Prefix    string   `json:"prefix"`
	Dedupe    bool     `json:"dedupe"`
	Sort      string   `json:"sort"`
	Script    string   `json:"script"`
}

func (p groupPipeline) options() kernel.SubOptions {
	return kernel.SubOptions{
		Lines: p.Lines, Regions: p.Regions, Protocols: p.Protocols, Include: p.Include, Exclude: p.Exclude,
		Rename: p.Rename, AddFlag: p.AddFlag, Prefix: p.Prefix, Dedupe: p.Dedupe, Sort: p.Sort, Script: p.Script,
	}
}

// normalize validates a group pipeline with exactly the subscription
// pipeline's rules (length caps, known regions / protocols pruning,
// expression validation), by round-tripping through subPipeline.
func (p *groupPipeline) normalize() error {
	sp := subPipeline{
		Lines: p.Lines, Regions: p.Regions, Protocols: p.Protocols, Include: p.Include, Exclude: p.Exclude,
		Rename: p.Rename, AddFlag: p.AddFlag, Prefix: p.Prefix, Dedupe: p.Dedupe, Sort: p.Sort, Script: p.Script,
	}
	if err := sp.normalize(); err != nil {
		return err
	}
	p.Lines, p.Regions, p.Protocols = sp.Lines, sp.Regions, sp.Protocols
	p.Include, p.Exclude, p.Rename = sp.Include, sp.Exclude, sp.Rename
	p.AddFlag, p.Prefix, p.Dedupe, p.Sort, p.Script = sp.AddFlag, sp.Prefix, sp.Dedupe, sp.Sort, sp.Script
	return nil
}

func parseGroupPipeline(raw string) groupPipeline {
	var p groupPipeline
	_ = json.Unmarshal([]byte(raw), &p)
	if p.Regions == nil {
		p.Regions = []string{}
	}
	if p.Protocols == nil {
		p.Protocols = []string{}
	}
	return p
}

// formatOrderedIDList renders ids in the given order (a subscription's
// group list is ordered; formatIDList sorts and cannot be used).
func formatOrderedIDList(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return strings.Join(parts, ",")
}

// normalizeGroupIDs drops zero and repeated ids, keeping the order.
func normalizeGroupIDs(ids []int64) []int64 {
	out := []int64{}
	seen := map[int64]bool{}
	for _, id := range ids {
		if id > 0 && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// nodeGroupsByID loads the node groups once per memo (a nil memo loads
// fresh). A load error degrades to no groups: subscriptions then render
// their own sources only, exactly as before groups existed.
func (h *Handler) nodeGroupsByID(memo *companionGateMemo) map[int64]*model.NodeGroup {
	if memo != nil && memo.groupsLoaded {
		return memo.groups
	}
	out := map[int64]*model.NodeGroup{}
	list, err := h.repo.ListNodeGroups()
	if err != nil {
		log.Printf("nodeGroupsByID: ListNodeGroups failed, groups ignored: %v", err)
	} else {
		for i := range list {
			out[list[i].ID] = &list[i]
		}
	}
	if memo != nil {
		memo.groups, memo.groupsLoaded = out, true
	}
	return out
}

// subGroups are the subscription's node groups that still exist, in the
// order the subscription lists them.
func (h *Handler) subGroups(s *model.Subscription, memo *companionGateMemo) []*model.NodeGroup {
	if s == nil || s.GroupIDs == "" {
		return nil
	}
	byID := h.nodeGroupsByID(memo)
	var out []*model.NodeGroup
	for _, id := range parseIDList(s.GroupIDs) {
		if g := byID[id]; g != nil {
			out = append(out, g)
		}
	}
	return out
}

// groupInboundIDs resolves a group's sources against the inbounds, the
// same way subInboundIDs resolves a subscription's own sources
// (companion inbounds are never sources).
func groupInboundIDs(g *model.NodeGroup, rows []model.NodeInbound) []int64 {
	filtered := make([]model.NodeInbound, 0, len(rows))
	for _, r := range rows {
		if r.CompanionOf == 0 {
			filtered = append(filtered, r)
		}
	}
	rows = filtered
	if g.SourceAll == 1 {
		ids := make([]int64, 0, len(rows))
		for _, r := range rows {
			ids = append(ids, r.ID)
		}
		return ids
	}
	known := make(map[int64]bool, len(rows))
	for _, r := range rows {
		known[r.ID] = true
	}
	var ids []int64
	picked := make(map[int64]bool)
	for _, id := range parseIDList(g.InboundIDs) {
		if known[id] && !picked[id] {
			picked[id] = true
			ids = append(ids, id)
		}
	}
	sources := map[int64]bool{}
	for _, id := range parseIDList(g.SourceIDs) {
		sources[id] = true
	}
	for _, r := range rows {
		if r.SourceID != 0 && sources[r.SourceID] && !picked[r.ID] {
			picked[r.ID] = true
			ids = append(ids, r.ID)
		}
	}
	return ids
}

// subGroupInboundIDs are the inbounds a subscription draws through its
// groups (deduplicated).
func (h *Handler) subGroupInboundIDs(s *model.Subscription, rows []model.NodeInbound, memo *companionGateMemo) []int64 {
	var out []int64
	seen := map[int64]bool{}
	for _, g := range h.subGroups(s, memo) {
		for _, id := range groupInboundIDs(g, rows) {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out
}

// subAllInboundIDs are every inbound a subscription draws from: its own
// sources plus its groups'. Credential pushes, membership checks and
// traffic attribution must use this set, or group nodes would carry
// credentials no kernel accepts.
func (h *Handler) subAllInboundIDs(s *model.Subscription, rows []model.NodeInbound, memo *companionGateMemo) []int64 {
	ids := subInboundIDs(s, rows)
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		seen[id] = true
	}
	for _, id := range h.subGroupInboundIDs(s, rows, memo) {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}

// groupEndpoints are one group's processed nodes. s is the subscription
// the nodes are built for (its credentials are stamped where the
// protocol allows); s may be nil for a standalone view of the group, in
// which case no credentials are stamped and no relay scope applies.
func (h *Handler) groupEndpoints(g *model.NodeGroup, s *model.Subscription, rows []model.NodeInbound, memo *companionGateMemo) []kernel.Endpoint {
	eps := h.subEndpointsForIDs(s, rows, memo, groupInboundIDs(g, rows))
	return kernel.ApplySubOptions(eps, parseGroupPipeline(g.Options).options())
}

// subGroupEndpoints are the subscription's group nodes, spliced in
// group order.
func (h *Handler) subGroupEndpoints(s *model.Subscription, rows []model.NodeInbound, memo *companionGateMemo) []kernel.Endpoint {
	var out []kernel.Endpoint
	for _, g := range h.subGroups(s, memo) {
		out = append(out, h.groupEndpoints(g, s, rows, memo)...)
	}
	return out
}

// subRenderEndpointsMemo is what a subscription renders: its groups'
// processed nodes in group order, then its own sources (which the
// caller passes through the subscription's pipeline at render time, as
// before). Subscriptions without groups get exactly subEndpointsMemo.
func (h *Handler) subRenderEndpointsMemo(s *model.Subscription, rows []model.NodeInbound, memo *companionGateMemo) []kernel.Endpoint {
	out := h.subGroupEndpoints(s, rows, memo)
	return append(out, h.subEndpointsMemo(s, rows, memo)...)
}

func (h *Handler) subRenderEndpoints(s *model.Subscription, rows []model.NodeInbound) []kernel.Endpoint {
	return h.subRenderEndpointsMemo(s, rows, nil)
}

// subscriptionsUsingGroup lists the subscriptions that draw from a group.
func subscriptionsUsingGroup(subs []model.Subscription, id int64) []model.Subscription {
	var out []model.Subscription
	for _, s := range subs {
		for _, gid := range parseIDList(s.GroupIDs) {
			if gid == id {
				out = append(out, s)
				break
			}
		}
	}
	return out
}

// groupPushInboundIDs are the inbounds whose kernels must be re-pushed
// because a group changed: every inbound of every subscription using
// the group, computed against current database state. Call it before
// and after the change and push the union.
func (h *Handler) groupPushInboundIDs(groupID int64) []int64 {
	subs, err := h.repo.ListSubscriptions()
	if err != nil {
		return nil
	}
	rows, err := h.repo.ListNodeInbounds()
	if err != nil {
		return nil
	}
	memo := &companionGateMemo{}
	seen := map[int64]bool{}
	var out []int64
	for _, s := range subscriptionsUsingGroup(subs, groupID) {
		s := s
		for _, id := range h.subAllInboundIDs(&s, rows, memo) {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out
}

// validateGroupIDs refuses group ids that do not exist (the same
// stance subscription save takes for inbound ids: a typo must not
// silently store a reference every reader then drops).
func (h *Handler) validateGroupIDs(ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	byID := h.nodeGroupsByID(nil)
	var unknown []int64
	for _, id := range ids {
		if byID[id] == nil {
			unknown = append(unknown, id)
		}
	}
	if len(unknown) > 0 {
		return errText(fmt.Sprintf("节点组不存在: %v", unknown))
	}
	return nil
}

// ---- API ----

type nodeGroupRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type nodeGroupView struct {
	ID         int64         `json:"id"`
	Name       string        `json:"name"`
	SourceAll  bool          `json:"sourceAll"`
	InboundIDs []int64       `json:"inboundIds"`
	SourceIDs  []int64       `json:"sourceIds"`
	Options    groupPipeline `json:"options"`
	NodeCount  int           `json:"nodeCount"`
	// Subscriptions draw from the group.
	Subscriptions []nodeGroupRef `json:"subscriptions"`
}

func (h *Handler) nodeGroupView(g *model.NodeGroup, rows []model.NodeInbound, subs []model.Subscription, memo *companionGateMemo) nodeGroupView {
	known := make(map[int64]bool, len(rows))
	for _, r := range rows {
		known[r.ID] = true
	}
	inbounds, sources := []int64{}, []int64{}
	if g.SourceAll != 1 {
		for _, id := range parseIDList(g.InboundIDs) {
			if known[id] {
				inbounds = append(inbounds, id)
			}
		}
		sources = append(sources, parseIDList(g.SourceIDs)...)
	}
	refs := []nodeGroupRef{}
	for _, s := range subscriptionsUsingGroup(subs, g.ID) {
		refs = append(refs, nodeGroupRef{ID: s.ID, Name: s.Name})
	}
	return nodeGroupView{
		ID: g.ID, Name: g.Name, SourceAll: g.SourceAll == 1, InboundIDs: inbounds, SourceIDs: sources,
		Options: parseGroupPipeline(g.Options), NodeCount: len(h.groupEndpoints(g, nil, rows, memo)), Subscriptions: refs,
	}
}

func (h *Handler) nodeGroupList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	rows, err := h.repo.ListNodeInbounds()
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	subs, err := h.repo.ListSubscriptions()
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	list, err := h.repo.ListNodeGroups()
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	memo := &companionGateMemo{}
	items := make([]nodeGroupView, 0, len(list))
	for i := range list {
		items = append(items, h.nodeGroupView(&list[i], rows, subs, memo))
	}
	response.WriteJSON(w, response.OK(map[string]interface{}{
		"items":   items,
		"regions": append(kernel.RegionNames(), kernel.RegionOther),
	}))
}

type nodeGroupRequest struct {
	ID         int64         `json:"id"`
	Name       string        `json:"name"`
	SourceAll  bool          `json:"sourceAll"`
	InboundIDs []int64       `json:"inboundIds"`
	SourceIDs  []int64       `json:"sourceIds"`
	Options    groupPipeline `json:"options"`
}

// validate checks the request and keeps the inbounds that exist.
// Companion inbounds can never be sources (the same choke point
// groupInboundIDs enforces at read time); ids matching no inbound at
// all are refused, not silently dropped.
func (req *nodeGroupRequest) validate(rows []model.NodeInbound) error {
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len([]rune(req.Name)) > 100 {
		return errText("请填写名称（最多 100 个字）")
	}
	if err := req.Options.normalize(); err != nil {
		return err
	}
	known := make(map[int64]bool, len(rows))
	for _, r := range rows {
		if r.CompanionOf == 0 {
			known[r.ID] = true
		}
	}
	var ids []int64
	var unknown []int64
	seenUnknown := map[int64]bool{}
	for _, id := range req.InboundIDs {
		if known[id] {
			ids = append(ids, id)
		} else if !seenUnknown[id] {
			seenUnknown[id] = true
			unknown = append(unknown, id)
		}
	}
	if len(unknown) > 0 {
		return errText(fmt.Sprintf("入站不存在: %v", unknown))
	}
	req.InboundIDs = ids
	var sources []int64
	for _, id := range req.SourceIDs {
		if id > 0 {
			sources = append(sources, id)
		}
	}
	req.SourceIDs = sources
	if !req.SourceAll && len(ids) == 0 && len(sources) == 0 {
		return errText("请至少选择一个入站或订阅源，或选择全部")
	}
	return nil
}

func (req *nodeGroupRequest) draft() *model.NodeGroup {
	opts, _ := json.Marshal(req.Options)
	return &model.NodeGroup{
		Name: req.Name, SourceAll: boolToInt(req.SourceAll),
		InboundIDs: formatIDList(req.InboundIDs), SourceIDs: formatIDList(req.SourceIDs),
		Options: string(opts),
	}
}

func (h *Handler) nodeGroupSave(w http.ResponseWriter, r *http.Request) {
	extendWriteDeadline(w, kernelApplyWriteDeadline)
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req nodeGroupRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	rows, err := h.repo.ListNodeInbounds()
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if err := req.validate(rows); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	now := time.Now()
	g := req.draft()
	var push []int64
	if req.ID > 0 {
		old, err := h.repo.GetNodeGroup(req.ID)
		if err != nil {
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
		if old == nil {
			response.WriteJSON(w, response.ErrDefault("节点组不存在"))
			return
		}
		// The subscriptions drawing from the group may gain or lose
		// inbounds: their credentials move with them, so push the union
		// of the before and after inbound sets.
		push = h.groupPushInboundIDs(req.ID)
		g.ID = old.ID
		g.Inx = old.Inx
		g.CreatedTime = old.CreatedTime
		g.UpdatedTime = now.UnixMilli()
		if err := h.repo.SaveNodeGroup(g); err != nil {
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
		push = append(push, h.groupPushInboundIDs(req.ID)...)
	} else {
		inx, err := h.repo.NextNodeGroupIndex()
		if err != nil {
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
		g.Inx = inx
		g.CreatedTime = now.UnixMilli()
		g.UpdatedTime = now.UnixMilli()
		if err := h.repo.SaveNodeGroup(g); err != nil {
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
	}
	subs, err := h.repo.ListSubscriptions()
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	saved, err := h.repo.GetNodeGroup(g.ID)
	if err != nil || saved == nil {
		response.WriteJSON(w, response.Err(-2, "节点组保存后读取失败"))
		return
	}
	view := h.nodeGroupView(saved, rows, subs, &companionGateMemo{})
	h.respondAfterSubPush(w, push, view)
}

func (h *Handler) nodeGroupDelete(w http.ResponseWriter, r *http.Request) {
	extendWriteDeadline(w, kernelApplyWriteDeadline)
	id := idFromBody(r, w)
	if id <= 0 {
		return
	}
	g, err := h.repo.GetNodeGroup(id)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if g == nil {
		// Deleting what is already gone is a success (idempotent).
		response.WriteJSON(w, response.OKEmpty())
		return
	}
	// Subscriptions keep the (now dangling) id and simply render without
	// the group; their credentials on the group's inbounds must still be
	// withdrawn, so compute the push set before deleting.
	push := h.groupPushInboundIDs(id)
	if err := h.repo.DeleteNodeGroup(id); err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	h.respondAfterSubPush(w, push, nil)
}

func (h *Handler) nodeGroupOrder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req struct {
		IDs []int64 `json:"ids"`
	}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	if err := h.repo.ReorderNodeGroups(normalizeGroupIDs(req.IDs)); err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	response.WriteJSON(w, response.OKEmpty())
}

// nodeGroupPreview runs an (unsaved) group's pipeline and lists the
// resulting nodes, mirroring subscriptionPreview's response shape.
func (h *Handler) nodeGroupPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req nodeGroupRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	if req.Name == "" {
		req.Name = "preview"
	}
	rows, err := h.repo.ListNodeInbounds()
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if err := req.validate(rows); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	g := req.draft()
	draft := &model.Subscription{}
	source := h.subEndpointsForIDs(draft, rows, nil, groupInboundIDs(g, rows))
	result, scriptErr := kernel.ApplySubOptionsReport(source, req.Options.options())
	items := make([]subPreviewItem, 0, len(result))
	for _, e := range result {
		line := "relay"
		if e.Direct {
			line = "direct"
		}
		items = append(items, subPreviewItem{Name: e.Name, Original: e.Original, Region: kernel.RegionOf(e.Name),
			Protocol: kernel.EndpointProtocol(e), Line: line, Chain: e.ChainRole, SourceID: e.SourceID, Own: e.Proxy == nil && e.SourceID == 0})
	}
	resp := map[string]interface{}{
		"items":   items,
		"total":   len(source),
		"dropped": len(source) - len(items),
	}
	if scriptErr != nil {
		resp["scriptError"] = scriptErr.Error()
	}
	response.WriteJSON(w, response.OK(resp))
}
