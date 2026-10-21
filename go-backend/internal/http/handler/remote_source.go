package handler

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"go-backend/internal/http/response"
	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// Remote sources are subscription URLs, or uploaded subscription files,
// whose nodes the panel keeps as imported nodes (SourceID set). A refresh
// (or a new upload) updates them in place, so relays and subscriptions
// that use them keep working.

var remoteSourceCheckInterval = 10 * time.Minute

// remoteSourceMu serializes refreshes (the loop and the refresh button).
var remoteSourceMu sync.Mutex

type remoteSourceView struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	URL           string `json:"url"`
	Kind          string `json:"kind"`
	Role          string `json:"role"`
	IntervalHours int    `json:"intervalHours"`
	RelayOnly     bool   `json:"relayOnly"`
	Enabled       bool   `json:"enabled"`
	LastFetch     int64  `json:"lastFetch"`
	LastError     string `json:"lastError"`
	NodeCount     int    `json:"nodeCount"`
	Userinfo      string `json:"userinfo"`
}

func remoteSourceViewOf(s *model.RemoteSource) remoteSourceView {
	return remoteSourceView{
		ID: s.ID, Name: s.Name, URL: s.URL, Kind: sourceKind(s), Role: s.Role, IntervalHours: s.IntervalHours, RelayOnly: s.RelayOnly == 1,
		Enabled: s.Enabled == 1, LastFetch: s.LastFetch, LastError: s.LastError, NodeCount: s.NodeCount,
		Userinfo: s.Userinfo,
	}
}

func sourceKind(s *model.RemoteSource) string {
	if s.Kind == model.RemoteSourceFile {
		return model.RemoteSourceFile
	}
	return model.RemoteSourceURL
}

// remoteSyncResult is what one refresh changed.
type remoteSyncResult struct {
	Added   int `json:"added"`
	Updated int `json:"updated"`
	Removed int `json:"removed"`
	// Kept are nodes gone from the source that still have relays; they
	// are left alone so the relays keep their target.
	Kept   int      `json:"kept"`
	Errors []string `json:"errors"`
}

func (r remoteSyncResult) summary() string {
	parts := []string{fmt.Sprintf("新增 %d", r.Added), fmt.Sprintf("更新 %d", r.Updated), fmt.Sprintf("移除 %d", r.Removed)}
	if r.Kept > 0 {
		parts = append(parts, fmt.Sprintf("%d 个已不在订阅里但有中转，已保留", r.Kept))
	}
	return strings.Join(parts, "，")
}

func sameImported(row *model.NodeInbound, node kernel.ImportedNode, extra string) bool {
	// IMP-009：名称/地址匹配大小写不敏感。服务商改名或主机名大小写抖动时，
	// 严格相等会把同一节点误判为"已更新"（甚至重复导入）；地址侧 externalKey
	// 本就小写归一化，这里统一用 EqualFold 与之对齐。
	return strings.EqualFold(row.Name, truncateRunes(node.Name, 100)) &&
		strings.EqualFold(row.Listen, node.Host) && row.Port == node.Port && row.Extra == extra
}

// syncRemoteSource fetches the source (a file source takes the uploaded
// content) and brings its imported nodes in line: nodes are matched by name
// first (providers keep names when they move a server), then by address.
func (h *Handler) syncRemoteSource(ctx context.Context, src *model.RemoteSource, upload string) (remoteSyncResult, error) {
	remoteSourceMu.Lock()
	defer remoteSourceMu.Unlock()
	res := remoteSyncResult{Errors: []string{}}
	now := time.Now().UnixMilli()
	fail := func(err error) (remoteSyncResult, error) {
		_ = h.repo.UpdateRemoteSourceStatus(src.ID, map[string]interface{}{"last_fetch": now, "last_error": truncate(err.Error(), 500)})
		return res, err
	}

	body, userinfo := upload, ""
	if sourceKind(src) == model.RemoteSourceFile {
		if strings.TrimSpace(upload) == "" {
			return fail(fmt.Errorf("文件订阅源不能自动更新，请重新上传文件"))
		}
	} else {
		var err error
		if body, userinfo, err = fetchSubscriptionInfo(ctx, src.URL); err != nil {
			return fail(fmt.Errorf("获取订阅失败：%v", err))
		}
	}
	nodes, parseErrs := kernel.ParseImport(body)
	if len(nodes) == 0 {
		msg := "订阅里没有可用的节点"
		if len(parseErrs) > 0 {
			msg += "：" + parseErrs[0]
		}
		// An empty answer is more likely a provider hiccup than a plan
		// without servers: keep the current nodes.
		return fail(fmt.Errorf("%s", msg))
	}
	if len(nodes) > maxImportNodes {
		nodes = nodes[:maxImportNodes]
		res.Errors = append(res.Errors, fmt.Sprintf("只保留前 %d 个节点", maxImportNodes))
	}
	res.Errors = append(res.Errors, parseErrs...)

	rows, err := h.repo.ListNodeInbounds()
	if err != nil {
		return fail(err)
	}
	var mine []*model.NodeInbound
	byName := map[string][]*model.NodeInbound{}
	byAddr := map[string][]*model.NodeInbound{}
	for i := range rows {
		row := &rows[i]
		if row.SourceID != src.ID || !isExternalInbound(row) {
			continue
		}
		mine = append(mine, row)
		// IMP-009：按名称匹配时大小写不敏感（providers 会抖动名称大小写）。
		byName[strings.ToLower(row.Name)] = append(byName[strings.ToLower(row.Name)], row)
		byAddr[externalKey(row.Listen, row.Port)] = append(byAddr[externalKey(row.Listen, row.Port)], row)
	}
	used := map[int64]bool{}
	take := func(list []*model.NodeInbound) *model.NodeInbound {
		for _, row := range list {
			if !used[row.ID] {
				used[row.ID] = true
				return row
			}
		}
		return nil
	}

	// F-025: 批内去重（与手动导入的 seen 同口径并扩到名称键）。byName/byAddr
	// 只是同步开始时的快照，本轮新建的行不回填映射——源内重复条目（同名或
	// 同址）会第二条再走新建分支：重复建行（external 行 kernel_id=0，
	// 部分唯一索引不兜底），后续轮次还在两行间漂移匹配。命中批内已处理
	// 的名称/地址即跳过并在结果里提示，不再重复建行。
	seen := map[string]bool{}

	for _, node := range nodes {
		fresh, err := externalRow(node, src.RelayOnly == 1, now)
		if err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s：%v", node.Name, err))
			continue
		}
		fresh.SourceID = src.ID
		nameKey := "name:" + strings.ToLower(fresh.Name)
		addrKey := "addr:" + externalKey(node.Host, node.Port)
		if seen[nameKey] || seen[addrKey] {
			res.Errors = append(res.Errors, fmt.Sprintf("%s：订阅源内重复条目，已跳过", node.Name))
			continue
		}
		seen[nameKey] = true
		seen[addrKey] = true
		row := take(byName[strings.ToLower(fresh.Name)])
		if row == nil {
			row = take(byAddr[externalKey(node.Host, node.Port)])
		}
		if row == nil {
			// AUDIT-R3 (F2X-001): per-node 端口分配锁（CreateNodeInbound 内部已去锁）。
			portMu := repo.AcquirePortAllocLock(fresh.NodeID)
			err := h.repo.CreateNodeInbound(&fresh)
			repo.ReleasePortAllocLock(portMu)
			if err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("%s：%v", node.Name, err))
				continue
			}
			res.Added++
			continue
		}
		// IMP-003：RelayOnly 变化也必须走更新分支，否则 sameImported 判"相同"
		// 直接跳过，订阅源的"只保留中转线路"开关不会同步到已有节点。
		if sameImported(row, node, fresh.Extra) && row.RelayOnly == fresh.RelayOnly {
			continue
		}
		moved := row.Listen != fresh.Listen || row.Port != fresh.Port
		row.Name, row.Protocol, row.Listen, row.Port = fresh.Name, fresh.Protocol, fresh.Listen, fresh.Port
		row.ShareHost, row.Extra, row.UpdatedTime = fresh.ShareHost, fresh.Extra, now
		// IMP-003：订阅源"只保留中转线路"开关变更要同步到已有节点。旧逻辑更新
		// 分支从不写 RelayOnly，改了开关后老节点行为不变，必须删源重建才生效。
		row.RelayOnly = fresh.RelayOnly
		// B4/F-132: 更新分支此前无锁无去重——地址变更可与手动导入/编辑
		// 并发撞出重复 (host,port)。在 0 号锁内复检去重后再保存（与
		// 导入、编辑路径同一临界区口径）。
		saveErr := func() error {
			portMu := repo.AcquirePortAllocLock(0)
			defer repo.ReleasePortAllocLock(portMu)
			if rowsNow, err := h.repo.ListNodeInbounds(); err == nil {
				key := externalKey(row.Listen, row.Port)
				for i := range rowsNow {
					other := &rowsNow[i]
					if other.ID != row.ID && isExternalInbound(other) && externalKey(other.Listen, other.Port) == key {
						return fmt.Errorf("已存在相同地址和端口的导入节点（%s）", key)
					}
				}
			}
			return h.repo.SaveNodeInbound(row)
		}()
		if saveErr != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s：%v", node.Name, saveErr))
			continue
		}
		res.Updated++
		if moved {
			for _, p := range h.retargetRelays(row) {
				res.Errors = append(res.Errors, row.Name+"："+p)
			}
		}
		for _, p := range h.applyChainReferrers(row.ID) {
			res.Errors = append(res.Errors, row.Name+"："+p)
		}
	}

	for _, row := range mine {
		if used[row.ID] {
			continue
		}
		if relays, err := h.repo.ListInboundRelays(row.ID); err == nil && len(relays) > 0 {
			res.Kept++
			continue
		}
		if len(h.chainReferrers(row.ID)) > 0 {
			res.Kept++
			continue
		}
		if err := h.repo.DeleteNodeInbound(row.ID); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s：%v", row.Name, err))
			continue
		}
		res.Removed++
	}

	count := 0
	if rows, err := h.repo.ListNodeInbounds(); err == nil {
		for _, row := range rows {
			if row.SourceID == src.ID {
				count++
			}
		}
	}
	_ = h.repo.UpdateRemoteSourceStatus(src.ID, map[string]interface{}{
		"last_fetch": now, "last_error": "", "node_count": count, "userinfo": userinfo,
	})
	return res, nil
}

func remoteSourceDue(s *model.RemoteSource, now time.Time) bool {
	if s.Enabled != 1 || s.IntervalHours <= 0 || sourceKind(s) == model.RemoteSourceFile {
		return false
	}
	return now.Sub(time.UnixMilli(s.LastFetch)) >= time.Duration(s.IntervalHours)*time.Hour
}

func (h *Handler) refreshDueRemoteSources(ctx context.Context, now time.Time) {
	sources, err := h.repo.ListRemoteSources()
	if err != nil {
		return
	}
	for i := range sources {
		s := &sources[i]
		if !remoteSourceDue(s, now) {
			continue
		}
		if _, err := h.syncRemoteSource(ctx, s, ""); err != nil {
			log.Printf("remote source %d refresh failed: %v", s.ID, err)
		}
	}
}

func (h *Handler) runRemoteSourceLoop(ctx context.Context) {
	defer h.jobsWG.Done()
	ticker := time.NewTicker(remoteSourceCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		h.refreshDueRemoteSources(ctx, time.Now())
	}
}

func (h *Handler) remoteSourceList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	sources, err := h.repo.ListRemoteSources()
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	out := make([]remoteSourceView, 0, len(sources))
	for i := range sources {
		out = append(out, remoteSourceViewOf(&sources[i]))
	}
	response.WriteJSON(w, response.OK(out))
}

// remoteSourceSave creates or edits a source; a new one (or a changed URL, a
// changed relayOnly flag — IMP-003, or an uploaded file) is synced right away.
func (h *Handler) remoteSourceSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
		URL  string `json:"url"`
		Kind string `json:"kind"`
		Role string `json:"role"`
		// Content is an uploaded file (file sources).
		Content       string `json:"content"`
		IntervalHours int    `json:"intervalHours"`
		RelayOnly     bool   `json:"relayOnly"`
		Enabled       *bool  `json:"enabled"`
	}
	// A JSON-escaped file can be larger than the file itself. decodeJSON
	// 本就限 10MB，此处再以 2×5MB 同口径收紧一层，有效上限不变（仍 10MB）。
	if err := decodeJSON(http.MaxBytesReader(w, r.Body, 2*maxSubscriptionBytes), &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误或文件超过 5MB"))
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.URL = strings.TrimSpace(req.URL)
	if req.Name == "" || len([]rune(req.Name)) > 100 {
		response.WriteJSON(w, response.ErrDefault("请填写名称（最多 100 个字）"))
		return
	}
	if req.Kind != model.RemoteSourceFile {
		req.Kind = model.RemoteSourceURL
	}
	if req.Role != "relay" && req.Role != "landing" {
		req.Role = ""
	}
	if len(req.Content) > maxSubscriptionBytes {
		response.WriteJSON(w, response.ErrDefault("文件超过 5MB"))
		return
	}
	if req.Kind == model.RemoteSourceURL && !isSubscriptionURL(req.URL) {
		response.WriteJSON(w, response.ErrDefault("请填写 http(s) 订阅地址"))
		return
	}
	if req.IntervalHours < 0 || req.IntervalHours > 24*7 {
		response.WriteJSON(w, response.ErrDefault("更新间隔不正确"))
		return
	}
	now := time.Now().UnixMilli()
	src := &model.RemoteSource{CreatedTime: now, Enabled: 1}
	if req.ID > 0 {
		var err error
		if src, err = h.repo.GetRemoteSource(req.ID); err != nil || src == nil {
			response.WriteJSON(w, response.ErrDefault("订阅源不存在"))
			return
		}
	}
	if req.Kind == model.RemoteSourceFile {
		req.URL, req.IntervalHours = "", 0
		if strings.TrimSpace(req.Content) == "" && (src.ID == 0 || sourceKind(src) != model.RemoteSourceFile) {
			response.WriteJSON(w, response.ErrDefault("请选择订阅文件"))
			return
		}
	} else {
		req.Content = ""
	}
	// IMP-003：RelayOnly 变更也触发立即同步（之前还需删源重建才生效）。
	fetch := src.ID == 0 || src.URL != req.URL || sourceKind(src) != req.Kind || req.Content != "" || src.RelayOnly != boolToInt(req.RelayOnly)
	// A6-001：文件源无新上传时唯一的 fetch 触发只可能是 RelayOnly 变化，
	// 而整源同步对文件源无 upload 必然失败（"文件订阅源不能自动更新"）—
	// 旧路径把假 last_error 写回源记录，且节点行 RelayOnly 永不更新，
	// 管理员与实际收紧状态分叉。覆写前先记住是否为这种「仅改
	// RelayOnly」的情形，保存后直接把新值落到导入节点行。
	relayOnlyOnlyChange := src.ID > 0 && sourceKind(src) == model.RemoteSourceFile &&
		req.Kind == model.RemoteSourceFile && strings.TrimSpace(req.Content) == "" &&
		src.RelayOnly != boolToInt(req.RelayOnly)
	src.Name, src.URL, src.IntervalHours, src.Kind, src.Role = req.Name, req.URL, req.IntervalHours, req.Kind, req.Role
	src.RelayOnly = boolToInt(req.RelayOnly)
	if req.Enabled != nil {
		src.Enabled = boolToInt(*req.Enabled)
	}
	src.UpdatedTime = now
	if err := h.repo.SaveRemoteSource(src); err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if relayOnlyOnlyChange {
		h.applyRemoteSourceRelayOnly(src)
		saved, _ := h.repo.GetRemoteSource(src.ID)
		if saved != nil {
			src = saved
		}
		response.WriteJSON(w, response.OK(remoteSourceViewOf(src)))
		return
	}
	if !fetch {
		src, _ = h.repo.GetRemoteSource(src.ID)
		// IMP-006：保存时若为公网 http 地址，提前提示同步必败（SecureFetch 对
		// 公网强制 https）。精确判定在同步时由 SecureFetch 按解析 IP 做。
		if isLikelyPublicHTTP(req.URL) {
			response.WriteJSON(w, response.R{Code: 0, Msg: "订阅源已保存。注意：公网 http 地址在同步时会被拒绝，建议改用 https 地址。", Data: remoteSourceViewOf(src), TS: now})
			return
		}
		response.WriteJSON(w, response.OK(remoteSourceViewOf(src)))
		return
	}
	h.respondRemoteSync(w, r.Context(), src, req.Content)
}

// isLikelyPublicHTTP 粗略判断订阅地址是否为公网 http（IMP-006，保存时提示用）。
// 域名在保存时不做 DNS 解析，一律按"可能公网"提示；IP 字面量则按私网/回环豁免。
func isLikelyPublicHTTP(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "http" {
		return false
	}
	host := u.Hostname()
	if host == "" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return !isPrivateIP(ip)
	}
	return true
}

// applyRemoteSourceRelayOnly 把订阅源的 RelayOnly 新值直接落到其导入
// 节点行（A6-001）：与同步路径的更新分支同口径——改值后对引用了该
// 入站的链式中转补一次 applyChainReferrers（地址未变，无需 retarget）。
// 与整源同步共用 remoteSourceMu 串行化，避免与定时刷新竞态。
func (h *Handler) applyRemoteSourceRelayOnly(src *model.RemoteSource) {
	remoteSourceMu.Lock()
	defer remoteSourceMu.Unlock()
	rows, err := h.repo.ListNodeInbounds()
	if err != nil {
		log.Printf("remote source %d relay-only apply list failed: %v", src.ID, err)
		return
	}
	now := time.Now().UnixMilli()
	for i := range rows {
		row := &rows[i]
		if row.SourceID != src.ID || !isExternalInbound(row) {
			continue
		}
		if row.RelayOnly == src.RelayOnly {
			continue
		}
		row.RelayOnly = src.RelayOnly
		row.UpdatedTime = now
		if err := h.repo.SaveNodeInbound(row); err != nil {
			log.Printf("remote source %d relay-only apply inbound %d failed: %v", src.ID, row.ID, err)
			continue
		}
		for _, problem := range h.applyChainReferrers(row.ID) {
			log.Printf("remote source %d relay-only chain re-apply inbound %d: %s", src.ID, row.ID, problem)
		}
	}
}

func (h *Handler) respondRemoteSync(w http.ResponseWriter, ctx context.Context, src *model.RemoteSource, upload string) {
	res, err := h.syncRemoteSource(ctx, src, upload)
	saved, _ := h.repo.GetRemoteSource(src.ID)
	if saved == nil {
		saved = src
	}
	data := map[string]interface{}{"source": remoteSourceViewOf(saved), "result": res}
	if err != nil {
		response.WriteJSON(w, response.R{Code: -1, Msg: err.Error(), Data: data, TS: time.Now().UnixMilli()})
		return
	}
	response.WriteJSON(w, response.R{Code: 0, Msg: res.summary(), Data: data, TS: time.Now().UnixMilli()})
}

func (h *Handler) remoteSourceRefresh(w http.ResponseWriter, r *http.Request) {
	id := idFromBody(r, w)
	if id <= 0 {
		return
	}
	src, err := h.repo.GetRemoteSource(id)
	if err != nil || src == nil {
		response.WriteJSON(w, response.ErrDefault("订阅源不存在"))
		return
	}
	if sourceKind(src) == model.RemoteSourceFile {
		response.WriteJSON(w, response.ErrDefault("文件订阅源请在编辑里重新上传文件"))
		return
	}
	h.respondRemoteSync(w, r.Context(), src, "")
}

// remoteSourceDelete removes a source and, unless keepNodes is set, its
// nodes (with their relays).
func (h *Handler) remoteSourceDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req struct {
		ID        int64 `json:"id"`
		KeepNodes bool  `json:"keepNodes"`
	}
	if err := decodeJSON(r.Body, &req); err != nil || req.ID <= 0 {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	remoteSourceMu.Lock()
	defer remoteSourceMu.Unlock()
	if req.KeepNodes {
		if err := h.repo.DetachSourceInbounds(req.ID); err != nil {
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
	} else if rows, err := h.repo.ListNodeInbounds(); err == nil {
		for i := range rows {
			if rows[i].SourceID != req.ID {
				continue
			}
			if msg := h.chainInUseMessage(&rows[i]); msg != "" {
				response.WriteJSON(w, response.ErrDefault(msg+"，或删除时保留节点"))
				return
			}
		}
		for _, row := range rows {
			if row.SourceID != req.ID {
				continue
			}
			h.removeInboundRelays(row.ID)
			if err := h.repo.DeleteNodeInbound(row.ID); err != nil {
				response.WriteJSON(w, response.Err(-2, err.Error()))
				return
			}
		}
	}
	if err := h.repo.DeleteRemoteSource(req.ID); err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	response.WriteJSON(w, response.OKEmpty())
}
