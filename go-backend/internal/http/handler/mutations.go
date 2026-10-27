package handler

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go-backend/internal/http/client"
	"go-backend/internal/http/response"
	"go-backend/internal/security"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"

	"gorm.io/gorm"
)

const tunnelServiceBindRetryDelay = 150 * time.Millisecond

func (h *Handler) userCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req map[string]interface{}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	// R2X-004：创建面接入 Idempotency-Key（机制见 idempotency.go）。
	{
		var finish func()
		var replayed bool
		w, finish, replayed = h.idemBegin(w, r, idemUserID(r), "user:create")
		defer finish()
		if replayed {
			return
		}
	}

	username := asString(req["user"])
	pwd := asString(req["pwd"])
	if username == "" || pwd == "" {
		response.WriteJSON(w, response.ErrDefault("用户名或密码不能为空"))
		return
	}
	// O5-002：用户名长度校验（DB 列 varchar(100)，超出会让 PG 报 500、SQLite 静默存入）
	if n := len([]rune(username)); n < 1 || n > 100 {
		response.WriteJSON(w, response.ErrDefault("用户名长度必须为 1-100 个字符"))
		return
	}
	if len([]rune(pwd)) < 6 {
		response.WriteJSON(w, response.ErrDefault("密码至少6位"))
		return
	}

	exists, err := h.repo.UserExists(username)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if exists {
		response.WriteJSON(w, response.ErrDefault("用户名已存在"))
		return
	}

	status := asInt(req["status"], 1)
	// O5-007：status 枚举校验（仅 0/1 有意义）
	if status != 0 && status != 1 {
		response.WriteJSON(w, response.ErrDefault("状态值无效，仅允许 0 或 1"))
		return
	}
	flow, flowMiB, flowErr := parseTrafficLimit(req, 100)
	if flowErr != nil {
		response.WriteJSON(w, response.ErrDefault(flowErr.Error()))
		return
	}
	num := asInt(req["num"], 10)
	// O5-005：num（设备数限制）拒绝负值
	if num < 0 {
		response.WriteJSON(w, response.ErrDefault("设备数限制不能小于0"))
		return
	}
	expTime := asInt64(req["expTime"], time.Now().Add(365*24*time.Hour).UnixMilli())
	flowResetTime := asInt64(req["flowResetTime"], 1)
	// O5-008：flowResetTime 有效值校验：0=不自动重置，1-31=每月几号重置
	if flowResetTime < 0 || flowResetTime > 31 {
		response.WriteJSON(w, response.ErrDefault("流量重置日无效，仅允许 0-31（0 表示不自动重置）"))
		return
	}
	dailyQuotaGB := asInt64(req["dailyQuotaGB"], 0)
	monthlyQuotaGB := asInt64(req["monthlyQuotaGB"], 0)
	if dailyQuotaGB < 0 || monthlyQuotaGB < 0 {
		response.WriteJSON(w, response.ErrDefault("配额不能小于0"))
		return
	}
	roleID := 1
	now := time.Now().UnixMilli()
	maxConn := asInt(req["maxConn"], 0)
	// O5-006：maxConn 拒绝负值
	if maxConn < 0 {
		response.WriteJSON(w, response.ErrDefault("连接数限制不能小于0"))
		return
	}

	hashedPassword, err := security.HashPassword(pwd)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}

	// O5-004：CreateUser + 配额配置 + 分组三步包在同一个事务里，任一步失败整体回滚。
	// 旧逻辑三步各自提交，后续失败会留下半成品账号（重试时用户名已被占用）。
	groupIDs := asInt64Slice(req["groupIds"])
	tx := h.repo.BeginTx()
	if tx == nil || tx.Error != nil {
		response.WriteJSON(w, response.Err(-2, "database unavailable"))
		return
	}
	defer func() { tx.Rollback() }()
	userID, err := h.repo.CreateUserTx(tx, username, hashedPassword, roleID, expTime, flow, flowResetTime, num, status, maxConn, now, flowMiB)
	if err != nil {
		// R2X-049：提交段复检命中同名时，与锁外预检同口径回友好文案。
		if errors.Is(err, repo.ErrUsernameExists) {
			response.WriteJSON(w, response.ErrDefault("用户名已存在"))
			return
		}
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if dailyQuotaGB > 0 || monthlyQuotaGB > 0 {
		if err := h.repo.SaveUserQuotaConfigTx(tx, userID, dailyQuotaGB, monthlyQuotaGB, now); err != nil {
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
	}
	if len(groupIDs) > 0 {
		if err := h.repo.AddUserToGroupsTx(tx, userID, groupIDs, now); err != nil {
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
	}
	if err := tx.Commit().Error; err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}

	// 分组父权限同步是提交后的副作用，不纳入事务。
	for _, gid := range groupIDs {
		_ = h.syncPermissionsByUserGroup(gid)
	}

	response.WriteJSON(w, response.OKEmpty())
}

func (h *Handler) userUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req map[string]interface{}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	id := asInt64(req["id"], 0)
	if id <= 0 {
		response.WriteJSON(w, response.ErrDefault("用户ID不能为空"))
		return
	}
	username := asString(req["user"])
	if username == "" {
		response.WriteJSON(w, response.ErrDefault("用户名不能为空"))
		return
	}
	// O5-002：用户名长度校验（1-100），语义同 userCreate
	if n := len([]rune(username)); n < 1 || n > 100 {
		response.WriteJSON(w, response.ErrDefault("用户名长度必须为 1-100 个字符"))
		return
	}

	roleID, err := h.repo.GetUserRoleID(id)
	if err != nil {
		if err == sql.ErrNoRows {
			response.WriteJSON(w, response.ErrDefault("用户不存在"))
			return
		}
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if roleID == 0 {
		response.WriteJSON(w, response.ErrDefault("请不要作死"))
		return
	}
	oldUser, err := h.repo.GetUserByID(id)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if oldUser == nil {
		response.WriteJSON(w, response.ErrDefault("用户不存在"))
		return
	}

	dup, err := h.repo.UserExistsExcluding(username, id)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if dup {
		response.WriteJSON(w, response.ErrDefault("用户名已存在"))
		return
	}

	// FIX2-A（V2-07）：在场但不可解析的数值字段 fail-closed 报错、
	// 不落库（同族全扫：含配额两键），不再静默套硬默认/旧值。
	for _, key := range []string{"flow", "num", "expTime", "flowResetTime", "status", "maxConn", "dailyQuotaGB", "monthlyQuotaGB"} {
		if presentNumericFieldInvalid(req, key) {
			response.WriteJSON(w, response.ErrDefault(fmt.Sprintf("%s 参数无效", key)))
			return
		}
	}

	// A2-04（BC-06）：真部分更新——未传字段保持现值（oldUser），不再
	// 「缺省即重置」（旧口径：未传 status 把停用用户重新启用、未传
	// expTime 自动续期一年、未传 flow 改回 100GB）。与 userTunnelUpdate
	// 的 presence 语义一致；显式传值（含 0）照常生效。
	flow, flowMiB, flowErr := parseTrafficLimit(req, 100)
	if flowErr != nil {
		response.WriteJSON(w, response.ErrDefault(flowErr.Error()))
		return
	}
	if _, hasFlow := req["flow"]; !hasFlow {
		if _, hasFlowMiB := req["flowMiB"]; !hasFlowMiB {
			flow, flowMiB = oldUser.Flow, oldUser.FlowMiB
		}
	}
	if _, supplied := req["flowMiB"]; !supplied {
		if current, err := h.repo.GetUserByID(id); err == nil && current != nil && current.Flow == flow {
			flowMiB = current.FlowMiB
		}
	}
	num := oldUser.Num
	if _, ok := req["num"]; ok {
		num = asInt(req["num"], 10)
	}
	// O5-005：num 拒绝负值
	if num < 0 {
		response.WriteJSON(w, response.ErrDefault("设备数限制不能小于0"))
		return
	}
	expTime := oldUser.ExpTime
	if _, ok := req["expTime"]; ok {
		expTime = asInt64(req["expTime"], oldUser.ExpTime)
	}
	flowResetTime := oldUser.FlowResetTime
	if _, ok := req["flowResetTime"]; ok {
		flowResetTime = asInt64(req["flowResetTime"], oldUser.FlowResetTime)
	}
	// O5-008：flowResetTime 有效值校验：0=不自动重置，1-31=每月几号重置
	if flowResetTime < 0 || flowResetTime > 31 {
		response.WriteJSON(w, response.ErrDefault("流量重置日无效，仅允许 0-31（0 表示不自动重置）"))
		return
	}
	status := oldUser.Status
	if _, ok := req["status"]; ok {
		status = asInt(req["status"], oldUser.Status)
	}
	// O5-007：status 枚举校验（仅 0/1）
	if status != 0 && status != 1 {
		response.WriteJSON(w, response.ErrDefault("状态值无效，仅允许 0 或 1"))
		return
	}
	_, hasDailyQuota := req["dailyQuotaGB"]
	_, hasMonthlyQuota := req["monthlyQuotaGB"]
	now := time.Now().UnixMilli()
	maxConn := oldUser.MaxConn
	if _, ok := req["maxConn"]; ok {
		maxConn = asInt(req["maxConn"], oldUser.MaxConn)
	}
	// O5-006：maxConn 拒绝负值
	if maxConn < 0 {
		response.WriteJSON(w, response.ErrDefault("连接数限制不能小于0"))
		return
	}

	// R2X-036（校验前置档）：分组存在性与配额解析全部前移到首写之前。
	// 旧实现主行先落库、分组段/配额段后失败，报错与落库事实不符
	// （改名已生效却回「保存失败」）。配额单侧补齐的只读查询同理前移，
	// 其读取失败也不再发生在主行写入之后。
	var updateGroupIDs []int64
	hasUpdateGroupIDs := false
	if groupIDsRaw, ok := req["groupIds"]; ok {
		hasUpdateGroupIDs = true
		updateGroupIDs = asInt64Slice(groupIDsRaw)
		if err := h.repo.ValidateIDsExist("user_group", updateGroupIDs); err != nil {
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
	}
	var updateDailyQuotaGB, updateMonthlyQuotaGB int64
	hasUpdateQuota := hasDailyQuota || hasMonthlyQuota
	if hasUpdateQuota {
		updateDailyQuotaGB = asInt64(req["dailyQuotaGB"], 0)
		updateMonthlyQuotaGB = asInt64(req["monthlyQuotaGB"], 0)
		if updateDailyQuotaGB < 0 || updateMonthlyQuotaGB < 0 {
			response.WriteJSON(w, response.ErrDefault("配额不能小于0"))
			return
		}
		if !(hasDailyQuota && hasMonthlyQuota) {
			// FIX2-A（V2-07 同族）：单传一侧时另一侧从当前视图补齐；
			// 读取失败报错中止（现于首写之前），视图为空（尚无配额
			// 行）才是合法的 0。
			currentQuota, qErr := h.repo.GetUserQuotaView(id, time.Now())
			if qErr != nil {
				response.WriteJSON(w, response.Err(-2, qErr.Error()))
				return
			}
			if currentQuota != nil {
				if !hasDailyQuota {
					updateDailyQuotaGB = currentQuota.DailyLimitGB
				}
				if !hasMonthlyQuota {
					updateMonthlyQuotaGB = currentQuota.MonthlyLimitGB
				}
			}
		}
	}

	pwd := asString(req["pwd"])
	if strings.TrimSpace(pwd) == "" {
		if err := h.repo.UpdateUserWithoutPassword(id, username, flow, num, expTime, flowResetTime, status, maxConn, now, flowMiB); err != nil {
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
	} else {
		hashedPassword, err := security.HashPassword(pwd)
		if err != nil {
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
		if err := h.repo.UpdateUserWithPassword(id, username, hashedPassword, flow, num, expTime, flowResetTime, status, maxConn, now, flowMiB); err != nil {
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
	}

	if err := h.repo.PropagateUserFlowToTunnels(id, flow, num, expTime, flowResetTime, flowMiB); err != nil {
		log.Printf("propagate user flow to tunnels failed user_id=%d err=%v", id, err)
	}
	if hasUpdateQuota {
		tx := h.repo.BeginTx()
		if tx == nil || tx.Error != nil {
			response.WriteJSON(w, response.Err(-2, "database unavailable"))
			return
		}
		defer func() { tx.Rollback() }()
		if err := h.repo.SaveUserQuotaConfigTx(tx, id, updateDailyQuotaGB, updateMonthlyQuotaGB, now); err != nil {
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
		if err := tx.Commit().Error; err != nil {
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
	}

	if hasUpdateGroupIDs {
		newGroupIDs := updateGroupIDs
		// X-P2-6 + X-P1-2①：分组替换与「被移出分组的组建授权撤销」在同一
		// 事务内完成；replaceErr 必须上抛，不再吞错回成功。
		tx := h.repo.BeginTx()
		if tx == nil || tx.Error != nil {
			response.WriteJSON(w, response.Err(-2, "database unavailable"))
			return
		}
		defer func() { tx.Rollback() }()
		affected, removed, replaceErr := h.repo.ReplaceUserGroupsByUserIDTx(tx, id, newGroupIDs, now)
		if replaceErr != nil {
			response.WriteJSON(w, response.Err(-2, replaceErr.Error()))
			return
		}
		var revokedPairs []repo.RevokedUserTunnelPair
		for _, gid := range removed {
			pairs, revokeErr := h.repo.RevokeGroupGrantsForUserTx(tx, gid, id)
			if revokeErr != nil {
				response.WriteJSON(w, response.Err(-2, revokeErr.Error()))
				return
			}
			revokedPairs = append(revokedPairs, pairs...)
		}
		if err := tx.Commit().Error; err != nil {
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
		for _, pair := range revokedPairs {
			h.cleanupForwardsForUserTunnel(pair.UserID, pair.TunnelID)
		}
		for _, gid := range affected {
			_ = h.syncPermissionsByUserGroup(gid)
		}
	}
	if oldUser.MaxConn != maxConn {
		warnings, syncErr := h.syncUserMaxConnForwards(id)
		if syncErr != nil {
			response.WriteJSON(w, response.ErrDefault(fmt.Sprintf("最大连接数下发失败: %v", syncErr)))
			return
		}
		if len(warnings) > 0 {
			response.WriteJSON(w, response.OK(map[string]interface{}{"warnings": warnings}))
			return
		}
	}

	response.WriteJSON(w, response.OKEmpty())
}

func (h *Handler) userGroups(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	// F-066：分组归属可辅助推断组织/权限结构，仅管理员或本人可查
	// （同组其余 user 管理端点均仅管理员；此前任意登录用户可查任意用户）。
	actorID, roleID, err := userRoleFromRequest(r)
	if err != nil {
		response.WriteJSON(w, response.Err(401, "未登录或token已过期"))
		return
	}
	id := idFromBody(r, w)
	if id <= 0 {
		return
	}
	if roleID != 0 && actorID != id {
		response.WriteJSON(w, response.Err(403, "无权限"))
		return
	}
	ids, err := h.repo.GetUserGroupIDsByUserID(id)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	response.WriteJSON(w, response.OK(ids))
}

func (h *Handler) userDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	id := idFromBody(r, w)
	if id <= 0 {
		return
	}

	roleID, err := h.repo.GetUserRoleID(id)
	if err != nil {
		if err == sql.ErrNoRows {
			// X-P3-28（L-11-3，V-12）：delete 对不存在统一幂等成功
			// （与 node/tunnel/speedLimit 对齐；删除不存在对象无副作用）。
			response.WriteJSON(w, response.OKEmpty())
			return
		}
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if roleID == 0 {
		response.WriteJSON(w, response.ErrDefault("请不要作死"))
		return
	}

	// B4/F-057: 此前删用户直接 DB 级联其全部转发，gost 服务不删、nft 不
	// 标 pending，孤儿监听继续转发。先按隧道分组逐条走与删隧道相同
	// 的数据面清理（cleanupForwardsDataPlane），全部成功后才级联删库；
	// 任一失败中止删用户（单条转发可用 force-delete 兜底）。
	userForwards, err := h.repo.ListForwardsByUser(id)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	// R2X-008（E2-01）：级联删转发会删 relay 行，拨号节点集随之变化——
	// 必须在清理之前按 forward 收集受影响入站，删后刷新白名单（与
	// tunnelDelete 的先收集口径一致）。
	var affectedInboundIDs []int64
	for _, fwd := range userForwards {
		affectedInboundIDs = append(affectedInboundIDs, h.inboundIDsForForward(fwd.ID)...)
	}
	byTunnel := make(map[int64][]model.ForwardRecord)
	tunnelOrder := make([]int64, 0)
	for _, fwd := range userForwards {
		if _, ok := byTunnel[fwd.TunnelID]; !ok {
			tunnelOrder = append(tunnelOrder, fwd.TunnelID)
		}
		byTunnel[fwd.TunnelID] = append(byTunnel[fwd.TunnelID], fwd)
	}
	sort.Slice(tunnelOrder, func(i, j int) bool { return tunnelOrder[i] < tunnelOrder[j] })
	for _, tunnelID := range tunnelOrder {
		if _, err := h.cleanupForwardsDataPlane(tunnelID, byTunnel[tunnelID], "用户", false); err != nil {
			// R5X-004(b)：清理是逐组逐条落库的前进式操作，中止时靠前
			// 分组的转发/relay 已实际删除——失败返回前以删前收集集按
			// 现行 DB 补刷一次白名单（与成功路径同集、幂等），否则已
			// 删转发的拨号地址无限驻留 allow（回执文案不变）。
			h.refreshInboundWhitelists(affectedInboundIDs)
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
	}

	if err := h.repo.DeleteUserCascade(id); err != nil {
		// R5X-004(b)：同上——全部转发已删、级联删用户失败时同样补刷。
		h.refreshInboundWhitelists(affectedInboundIDs)
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	h.refreshInboundWhitelists(affectedInboundIDs)
	response.WriteJSON(w, response.OKEmpty())
}

func (h *Handler) userResetFlow(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req map[string]interface{}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	id := asInt64(req["id"], 0)
	typeVal := asInt(req["type"], 0)
	if id <= 0 || (typeVal != 1 && typeVal != 2) {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}

	if typeVal == 1 {
		h.repo.ResetUserFlowByUser(id, time.Now().UnixMilli())
	} else {
		h.repo.ResetUserFlowByUserTunnel(id)
	}
	response.WriteJSON(w, response.OKEmpty())
}

// PORT-CLOSURE Lane C（F-04/F-05/F-07(i)）：intake 侧“剥离 + 格式校验”。
// 全部剥离 helper 均为单次剥离（SplitHostPort 一次成功即取 host，
// 失败原样返回），双端口值 "host:port:port" 会以 too many colons
// 失败而原样穿透；IsValidNodeAddress 对该形态明确拒绝（裸 IPv6 先
// 经 ParseAddr 放行、SplitHostPort 失败且非 missing port 即拒）。
// 故 intake 在剥离后追加同一校验，非法值直接拒绝、不落库。
// 存量双端口行不改写（与 R32 F-01 存量口径一致）：未提交字段保持
// 原值；已落库的双端口值仍由消费侧既有容忍/跳过行为承接。

// stripAndValidateNodeAddress strips a single trailing :port (R25 口径
// stripShareHostPort) and rejects the result unless it is a valid bare
// host/IP or single host:port. Empty input stays empty (nullable fields).
func stripAndValidateNodeAddress(value string) (string, error) {
	stripped := stripShareHostPort(value)
	if strings.TrimSpace(stripped) == "" {
		return "", nil
	}
	if err := IsValidNodeAddress(stripped); err != nil {
		return "", err
	}
	return stripped, nil
}

// stripAndValidateNodeAddressList strips :port per comma-separated item
// and validates each stripped item (PORT-CLOSURE F-07 扩展：隧道显式
// in_ip 双端口收口，与 PC-04/PC-05 同款 intake 校验）。单端口逐项剥离、
// 双端口整串拒绝——单次剥离对双端口是 no-op，此前原值落库后消费侧
// 畸形。空项原样保留（与 stripShareHostPortList 形态一致）；空串
// 返回 ""（调用方仍走 buildTunnelInIP 派生）。
func stripAndValidateNodeAddressList(value string) (string, error) {
	parts := strings.Split(value, ",")
	for i, part := range parts {
		stripped, err := stripAndValidateNodeAddress(part)
		if err != nil {
			return "", err
		}
		parts[i] = stripped
	}
	return strings.Join(parts, ","), nil
}

// validateListenAddrValue strips a single trailing :port from a node
// listen address and validates the result. Unlike stripShareHostPort
// it never trims brackets, so the stored default "[::]" passes through
// unchanged (same semantics as R32-FIX-F01 stripListenAddrPort, which
// lives in kernel_share_address.go on the R32 branch; this lane is
// scoped to mutations.go, so the bracket-preserving strip is spelled
// out here — dedup with stripListenAddrPort at integration).
// Double-port values fail the single strip and are rejected by
// IsValidNodeAddress. Empty input stays empty (caller defaults it).
func validateListenAddrValue(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	if err := IsValidNodeAddress(value); err != nil {
		return "", err
	}
	return value, nil
}

// validatePanelInstallAddr validates a panel address from the site
// config (O-C-01) before it is baked into the node install command:
// an optional scheme is stripped first (normalizeNodeInstallAddr
// accepts http/https/ws/wss), then the host part must be a valid bare
// host/IP or single host:port — a double-port value is rejected.
func validatePanelInstallAddr(panelAddr string) error {
	raw := strings.TrimSpace(panelAddr)
	if raw == "" {
		return nil
	}
	if idx := strings.Index(raw, "://"); idx > 0 {
		raw = raw[idx+3:]
	}
	return IsValidNodeAddress(raw)
}

func (h *Handler) nodeCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req map[string]interface{}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	// X-P3-27（L-11-2）：与 X-P2-17 同一 Idempotency-Key 机制（创建面）。
	{
		var finish func()
		var replayed bool
		w, finish, replayed = h.idemBegin(w, r, idemUserID(r), "node:create")
		defer finish()
		if replayed {
			return
		}
	}
	name := asString(req["name"])
	// R30-FIX-F04（写侧归一化，用户 2026-10-26 拍板）：入库前剥离
	// :port 再存。先归一化再校验，":port-only" 等退化值归一化为
	// 空后由下方的空值检查拒绝；非法值（非 host:port 形态）按
	// stripShareHostPort 语义原样通过。
	serverIP := stripShareHostPort(asString(req["serverIp"]))
	if name == "" || serverIP == "" {
		response.WriteJSON(w, response.ErrDefault("节点名称和地址不能为空"))
		return
	}
	if err := IsValidNodeAddress(serverIP); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	// D3-02: 直填 remoteUrl 与 nodeImport 共用同一校验（validateRemoteNodeURL），
	// 此前该路径零校验，可把内网/元数据地址写成远端节点 URL。
	if asInt(req["isRemote"], 0) == 1 {
		if remoteURL := strings.TrimSpace(asString(req["remoteUrl"])); remoteURL != "" {
			if err := validateRemoteNodeURL(remoteURL); err != nil {
				writeRemoteURLValidationError(w, err)
				return
			}
		}
	}

	now := time.Now().UnixMilli()
	inx := h.repo.NextIndex("node")
	forwardMode := defaultNodeForwardMode(asString(req["forwardMode"]))
	status := 0
	if forwardMode == "nftables" {
		status = 1
	}
	nodeSecret, err := randomToken(16)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	// PORT-CLOSURE F-07(i)：显式 V4/V6 在剥离后复用 IsValidNodeAddress，
	// 双端口即拒（此前只单次剥离、无校验）。
	serverIPV4, err := stripAndValidateNodeAddress(asString(req["serverIpV4"]))
	if err != nil {
		response.WriteJSON(w, response.ErrDefault("节点 IPv4 地址格式无效："+err.Error()))
		return
	}
	serverIPV6, err := stripAndValidateNodeAddress(asString(req["serverIpV6"]))
	if err != nil {
		response.WriteJSON(w, response.ErrDefault("节点 IPv6 地址格式无效："+err.Error()))
		return
	}
	// PORT-CLOSURE F-05：监听地址剥离后须过格式校验，双端口即拒
	// （此前原值落库、零校验；单次剥离缺口见 FIX-PLAN F-05）。
	tcpListenAddr, err := validateListenAddrValue(asString(req["tcpListenAddr"]))
	if err != nil {
		response.WriteJSON(w, response.ErrDefault("TCP 监听地址格式无效："+err.Error()))
		return
	}
	udpListenAddr, err := validateListenAddrValue(asString(req["udpListenAddr"]))
	if err != nil {
		response.WriteJSON(w, response.ErrDefault("UDP 监听地址格式无效："+err.Error()))
		return
	}
	// O1-002：直接用 CreateNode 返回的新 ID，不再按 name+serverIP 反查（并发 TOCTOU）。
	nodeID, err := h.repo.CreateNode(
		name,
		nodeSecret,
		serverIP,
		// R30-FIX-F04：显式字段同口径归一化（写侧）。
		nullableText(serverIPV4),
		nullableText(serverIPV6),
		defaultString(asString(req["port"]), "1000-65535"),
		nullableText(asString(req["interfaceName"])),
		nullableText(""),
		nullableText(strings.TrimSpace(asString(req["remark"]))),
		nullableUnixMilli(asInt64(req["expiryTime"], 0)),
		nullableText(normalizeNodeRenewalCycle(asString(req["renewalCycle"]))),
		asInt(req["http"], 0),
		asInt(req["tls"], 0),
		asInt(req["socks"], 0),
		now,
		status,
		// PORT-CLOSURE F-05：监听地址已在上方剥离 + 校验后落库。
		defaultString(tcpListenAddr, "[::]"),
		defaultString(udpListenAddr, "[::]"),
		inx,
		asInt(req["isRemote"], 0),
		nullableText(asString(req["remoteUrl"])),
		nullableText(asString(req["remoteToken"])),
		nullableText(asString(req["remoteConfig"])),
		// PORT-CLOSURE B-NEW-01（顺手收口）：extraIPs 按逗号分项剥离
		// :port 后落库（此前原值落库；唯一消费 nodeAddresses 逐 token
		// 剥离，故此前无未剥离消费点）。
		nullableText(stripShareHostPortList(asString(req["extraIPs"]))),
		forwardMode,
	)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if err := h.persistNodeSSHConfig(nodeID, req, forwardMode, now, false); err != nil {
		// X-P2-15（L-09-1）：第二步失败补偿删除刚建节点行，不留无 SSH
		// 配置的半成品节点（forward/tunnel 创建均有 delete 回滚，独此
		// 路此前缺）。补偿失败只记日志：原错误仍须回给调用方。
		if _, delErr := h.deleteNodeByID(nodeID); delErr != nil {
			log.Printf("node create compensation delete failed node_id=%d err=%v", nodeID, delErr)
		}
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	response.WriteJSON(w, response.OKEmpty())
}

func (h *Handler) nodeUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req map[string]interface{}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	id := asInt64(req["id"], 0)
	if id <= 0 {
		response.WriteJSON(w, response.ErrDefault("节点ID不能为空"))
		return
	}
	// FIX2-A（V2-07）：在场但不可解析的数值字段 fail-closed，不落
	// 库（expiryTime 旧口径静默套 0 = 清空到期，http/tls/socks 同族）。
	for _, key := range []string{"http", "tls", "socks", "expiryTime"} {
		if presentNumericFieldInvalid(req, key) {
			response.WriteJSON(w, response.ErrDefault(fmt.Sprintf("%s 参数无效", key)))
			return
		}
	}

	currentStatus, currentHTTP, currentTLS, currentSocks, err := h.repo.GetNodeStatusFields(id)
	if err != nil {
		if err == sql.ErrNoRows {
			response.WriteJSON(w, response.ErrDefault("节点不存在"))
			return
		}
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}

	newHTTP := asInt(req["http"], currentHTTP)
	newTLS := asInt(req["tls"], currentTLS)
	newSocks := asInt(req["socks"], currentSocks)
	currentForwardMode, err := h.repo.GetNodeForwardMode(id)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	// A2-04（BC-06）：真部分更新——未传字段保持现值。旧口径未传
	// forwardMode 时 defaultNodeForwardMode("") 恒回 "agent"，会把
	// nftables 节点静默翻回 agent 模式；name/serverIp/port/监听地址/
	// 附加字段同理被清空或复位。currentNode 为保值来源。
	currentNode, err := h.repo.GetNodeByID(id)
	if err != nil || currentNode == nil {
		response.WriteJSON(w, response.Err(-2, errString(err)))
		return
	}
	forwardMode := defaultNodeForwardMode(currentForwardMode)
	if _, ok := req["forwardMode"]; ok {
		forwardMode = defaultNodeForwardMode(strings.TrimSpace(asString(req["forwardMode"])))
	}
	serverIP := currentNode.ServerIP
	if _, ok := req["serverIp"]; ok {
		// R30-FIX-F04（写侧归一化）：仅提交的字段归一化；未提交
		// 的字段保持存量原值。先归一化再校验（下方 IsValidNodeAddress）。
		serverIP = stripShareHostPort(asString(req["serverIp"]))
	}
	if serverIP != "" {
		if err := IsValidNodeAddress(serverIP); err != nil {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
	}
	// R35-FIX-F01（a）：转发模式翻转不迁移任何数据面服务——由 agent
	// 翻为 nftables 时既有 gost 服务会继续监听，由 nftables 翻回
	// agent 时既有 nft 规则会继续生效；翻转后 tunnelUsesNftables
	//（ANY 语义）立即改变，暂停/删除/恢复只走 nft 分支且只处理
	// entryNodeIDs[0]，另一引擎的服务永不被触碰。任一方向的翻转在
	// 节点仍是已有转发隧道的入口节点时一律前置拒绝并点名隧道；
	// 修复路径是先删除或移出该隧道的转发（或编辑隧道去掉本节点
	// 入口），而不是翻完再补。未改变模式的部分更新不受影响。
	if forwardMode != defaultNodeForwardMode(currentForwardMode) {
		if tunnelID, tunnelName, gErr := h.entryNodeTunnelWithForwards(id); gErr != nil {
			response.WriteJSON(w, response.Err(-2, gErr.Error()))
			return
		} else if tunnelID > 0 {
			response.WriteJSON(w, response.ErrDefault(fmt.Sprintf("节点 %s 是隧道 %s（ID %d）的入口节点，该隧道下已有转发；请先删除或移出转发后再切换转发模式", currentNode.Name, tunnelName, tunnelID)))
			return
		}
	}
	usesNftablesRuntime := forwardMode == "nftables" || defaultNodeForwardMode(currentForwardMode) == "nftables"
	if currentStatus == 1 && !usesNftablesRuntime && (newHTTP != currentHTTP || newTLS != currentTLS || newSocks != currentSocks) {
		if err := h.applyNodeProtocolChange(id, newHTTP, newTLS, newSocks); err != nil {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
	}

	// A2-04：其余字段同理按 presence 解析（未传用现值，含 sql.Null* 直传）。
	resolveText := func(key string, current interface{}) interface{} {
		if _, ok := req[key]; ok {
			return nullableText(asString(req[key]))
		}
		return current
	}
	// R30-FIX-F04：节点地址字段的归一化版 resolve——仅提交的字段
	// 剥离 :port 后存；未提交的字段保持存量原值（存量不改写）。
	resolveNodeIP := func(key string, current interface{}) interface{} {
		if _, ok := req[key]; ok {
			return nullableText(stripShareHostPort(asString(req[key])))
		}
		return current
	}
	// PORT-CLOSURE B-NEW-01（顺手收口）：仅提交的 extraIPs 按逗号
	// 分项剥离 :port 后存；未提交的字段保持存量原值。
	resolveExtraIPs := func(key string, current interface{}) interface{} {
		if _, ok := req[key]; ok {
			return nullableText(stripShareHostPortList(asString(req[key])))
		}
		return current
	}
	// PORT-CLOSURE F-07(i)：仅提交的 V4/V6 字段在剥离后复用
	// IsValidNodeAddress，双端口即拒（与 nodeCreate 同口径）。
	if _, ok := req["serverIpV4"]; ok {
		if _, err := stripAndValidateNodeAddress(asString(req["serverIpV4"])); err != nil {
			response.WriteJSON(w, response.ErrDefault("节点 IPv4 地址格式无效："+err.Error()))
			return
		}
	}
	if _, ok := req["serverIpV6"]; ok {
		if _, err := stripAndValidateNodeAddress(asString(req["serverIpV6"])); err != nil {
			response.WriteJSON(w, response.ErrDefault("节点 IPv6 地址格式无效："+err.Error()))
			return
		}
	}
	nodeName := currentNode.Name
	if _, ok := req["name"]; ok {
		nodeName = asString(req["name"])
	}
	nodePort := currentNode.Port
	if _, ok := req["port"]; ok {
		nodePort = defaultString(asString(req["port"]), "1000-65535")
	}
	if strings.TrimSpace(nodePort) == "" {
		nodePort = "1000-65535"
	}
	remarkVal := interface{}(currentNode.Remark)
	if _, ok := req["remark"]; ok {
		remarkVal = nullableText(strings.TrimSpace(asString(req["remark"])))
	}
	expiryVal := interface{}(currentNode.ExpiryTime)
	if _, ok := req["expiryTime"]; ok {
		expiryVal = nullableUnixMilli(asInt64(req["expiryTime"], 0))
	}
	renewalVal := interface{}(currentNode.RenewalCycle)
	if _, ok := req["renewalCycle"]; ok {
		renewalVal = nullableText(normalizeNodeRenewalCycle(asString(req["renewalCycle"])))
	}
	tcpAddr := currentNode.TCPListenAddr
	if _, ok := req["tcpListenAddr"]; ok {
		// PORT-CLOSURE F-05：仅提交的字段剥离 + 校验后存；未提交的
		// 字段保持存量原值（存量不改写）。
		v, err := validateListenAddrValue(asString(req["tcpListenAddr"]))
		if err != nil {
			response.WriteJSON(w, response.ErrDefault("TCP 监听地址格式无效："+err.Error()))
			return
		}
		tcpAddr = defaultString(v, "[::]")
	}
	if strings.TrimSpace(tcpAddr) == "" {
		tcpAddr = "[::]"
	}
	udpAddr := currentNode.UDPListenAddr
	if _, ok := req["udpListenAddr"]; ok {
		v, err := validateListenAddrValue(asString(req["udpListenAddr"]))
		if err != nil {
			response.WriteJSON(w, response.ErrDefault("UDP 监听地址格式无效："+err.Error()))
			return
		}
		udpAddr = defaultString(v, "[::]")
	}
	if strings.TrimSpace(udpAddr) == "" {
		udpAddr = "[::]"
	}

	now := time.Now().UnixMilli()
	if err := h.repo.UpdateNode(id,
		nodeName,
		serverIP,
		resolveNodeIP("serverIpV4", currentNode.ServerIPV4),
		resolveNodeIP("serverIpV6", currentNode.ServerIPV6),
		nodePort,
		resolveText("interfaceName", currentNode.InterfaceName),
		resolveExtraIPs("extraIPs", currentNode.ExtraIPs),
		remarkVal,
		expiryVal,
		renewalVal,
		forwardMode,
		newHTTP,
		newTLS,
		newSocks,
		tcpAddr,
		udpAddr,
		now,
	); err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if err := h.persistNodeSSHConfig(id, req, forwardMode, now, true); err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if forwardMode == "nftables" && currentStatus != 1 {
		if err := h.repo.UpdateNodeStatus(id, 1); err != nil {
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
	}
	// R2X-008/R2X-009 子项③（E S-1）：节点地址（ServerIP/V4/V6/ExtraIPs）
	// 是物化状态的两处输入——入站白名单的拨号节点地址集与落地在本节点
	// 的中转 RemoteAddr。改址落库后：刷新经本节点拨号的全部入站白
	// 名单，并对落在本节点的 Managed 中转逐条重算 RemoteAddr。
	addrChanged := serverIP != currentNode.ServerIP
	// R30-FIX-F04：变更检测按归一化后的提交值比对——提交与存量
	// 仅差 :port 时不触发无意义的白名单/中转重算。
	if _, ok := req["serverIpV4"]; ok && stripShareHostPort(asString(req["serverIpV4"])) != currentNode.ServerIPV4.String {
		addrChanged = true
	}
	if _, ok := req["serverIpV6"]; ok && stripShareHostPort(asString(req["serverIpV6"])) != currentNode.ServerIPV6.String {
		addrChanged = true
	}
	if _, ok := req["extraIPs"]; ok && stripShareHostPortList(asString(req["extraIPs"])) != currentNode.ExtraIPs.String {
		addrChanged = true
	}
	if addrChanged {
		// R3X-015：拨号入站集查询失败不得静默当空集跳过刷新——记
		// 错留痕（更新本身已落库，此面为刷新面而非删除守卫）。
		if dialedIDs, derr := h.inboundIDsDialedByNode(id); derr != nil {
			log.Printf("node update: dialed-inbound lookup failed node_id=%d err=%v (whitelist refresh skipped)", id, derr)
		} else {
			h.refreshInboundWhitelists(dialedIDs)
		}
		if nodeInbounds, err := h.repo.ListNodeInboundsByNode(id); err == nil {
			for _, inbound := range nodeInbounds {
				for _, warning := range h.resyncManagedRelayRemoteAddrsForInbound(inbound.ID) {
					log.Printf("node update: relay remote addr resync node_id=%d inbound_id=%d: %s", id, inbound.ID, warning)
				}
			}
		}
	}
	response.WriteJSON(w, response.OKEmpty())
}

func (h *Handler) persistNodeSSHConfig(nodeID int64, req map[string]interface{}, forwardMode string, now int64, preserveSecrets bool) error {
	if h == nil || h.repo == nil {
		return errors.New("handler not initialized")
	}
	if nodeID <= 0 {
		return errors.New("节点ID不能为空")
	}
	if forwardMode != "nftables" {
		return h.repo.DeleteNodeSSHConfig(nodeID)
	}
	// V2-06：部分更新（preserveSecrets=true）未带 sshConfig 整段时
	// 整段不动——旧实现仍会用请求值重建并 upsert，其中 port 被
	// asInt(...,22) 填成 22 后保值分支（port<=0 才保值）永不触发，
	// 任何不带完整 sshConfig 的 nodeUpdate 都把非 22 端口的 nft
	// 节点 SSH 配置静默改回 22，数据面随之失联。
	if preserveSecrets {
		if _, ok := req["sshConfig"]; !ok {
			return nil
		}
	}
	cfgMap := asMap(req["sshConfig"])
	host := strings.TrimSpace(asString(cfgMap["host"]))
	if host == "" {
		// R30-FIX-F03（sweep）：serverIp 派生的 SSH host 回退可含
		// :port；下游 runner 以 JoinHostPort(host, sshPort) 拼出
		// "IP:port:22" 畸形地址致拨号失败。仅此派生分支剥离，
		// 显式填的 sshConfig.host 不动。
		host = stripShareHostPort(asString(req["serverIp"]))
	}
	// V2-06/V2-07：port 改 presence/零值语义——缺席即 0，更新路径
	// 由下方保值分支回填既有值，create 路径由既有校验（port<=0
	// 报错）明确拒绝，不再静默默认 22；在场但不可解析直接报错。
	if rawPort, ok := cfgMap["port"]; ok && asString(rawPort) != "" {
		if _, err := strconv.ParseInt(asString(rawPort), 10, 64); err != nil {
			return errors.New("nftables 节点 SSH 端口无效")
		}
	}
	port := asInt(cfgMap["port"], 0)
	username := strings.TrimSpace(asString(cfgMap["username"]))
	authType := strings.TrimSpace(asString(cfgMap["authType"]))
	password := asString(cfgMap["password"])
	privateKey := asString(cfgMap["privateKey"])
	passphrase := asString(cfgMap["passphrase"])
	sudoMode := strings.TrimSpace(asString(cfgMap["sudoMode"]))

	if preserveSecrets {
		existing, err := h.repo.GetNodeSSHConfig(nodeID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if existing != nil {
			if host == "" {
				host = strings.TrimSpace(existing.Host)
			}
			if port <= 0 {
				port = existing.Port
			}
			if username == "" {
				username = strings.TrimSpace(existing.Username)
			}
			if authType == "" {
				authType = strings.TrimSpace(existing.AuthType)
			}
			if strings.TrimSpace(password) == "" && existing.Password.Valid {
				password = existing.Password.String
			}
			if strings.TrimSpace(privateKey) == "" && existing.PrivateKey.Valid {
				privateKey = existing.PrivateKey.String
			}
			if strings.TrimSpace(passphrase) == "" && existing.Passphrase.Valid {
				passphrase = existing.Passphrase.String
			}
			if sudoMode == "" {
				sudoMode = strings.TrimSpace(existing.SudoMode)
			}
		}
	}

	if host == "" || username == "" {
		return errors.New("nftables 节点 SSH 配置不完整")
	}
	if port <= 0 || port > 65535 {
		return errors.New("nftables 节点 SSH 端口无效")
	}

	authType = strings.ToLower(authType)
	switch authType {
	case "password":
		if strings.TrimSpace(password) == "" {
			return errors.New("nftables 节点 SSH 密码不能为空")
		}
		privateKey = ""
	case "private_key", "":
		authType = "private_key"
		if strings.TrimSpace(privateKey) == "" {
			return errors.New("nftables 节点 SSH 私钥不能为空")
		}
		password = ""
	default:
		return errors.New("nftables 节点 SSH 认证方式无效")
	}

	switch strings.ToLower(sudoMode) {
	case "", "none":
		sudoMode = "none"
	case "sudo", "sudo_su":
	default:
		return errors.New("nftables 节点 sudo 模式无效")
	}

	return h.repo.UpsertNodeSSHConfig(nodeID, repo.NftSSHConfigInput{
		Host:       host,
		Port:       port,
		Username:   username,
		AuthType:   authType,
		Password:   password,
		PrivateKey: privateKey,
		Passphrase: passphrase,
		SudoMode:   sudoMode,
	}, now)
}

func (h *Handler) nodeDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	id := idFromBody(r, w)
	if id <= 0 {
		return
	}
	// R2X-045：删除语义不变（尽力而为，N-05），但删除打断的跨节点引用
	// 清单随响应回传，不再静默。
	brokenEntries, err := h.deleteNodeByID(id)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	// R5-B5 D14: 节点删除时释放 per-node 锁表条目，避免长期运行后 map 增长。
	//（锁已下沉到 serializedNftManager。）
	if h.nftablesManager != nil {
		h.nftablesManager.ReleaseNode(id)
	}
	// R3X-014：内部清单是 (ID, 显示串) 结构，单删响应契约仍是
	// []string 显示串，在此投影。
	brokenReferrers := make([]string, 0, len(brokenEntries))
	for _, entry := range brokenEntries {
		brokenReferrers = append(brokenReferrers, entry.Display)
	}
	response.WriteJSON(w, response.OK(map[string]interface{}{
		"brokenReferrers":     brokenReferrers,
		"brokenReferrerCount": len(brokenReferrers),
	}))
}

// panelRequestScheme 推断本次管理请求所见的面板实际服务方式
// （"http"/"https"）：可信代理（回环/私网/链路本地，与登录限流
// XFF 同一信任口径）的 X-Forwarded-Proto 优先，其次连接自身 TLS。
// 配套：vite-frontend/nginx.conf 已改为透传上游 X-Forwarded-Proto
// （此前被覆写为 nginx 自身 $scheme，Caddy 终止 TLS 的部署里后端
// 永远只能看到 http）。
func panelRequestScheme(r *http.Request) string {
	if r != nil {
		if ip := parseIPLiteral(r.RemoteAddr); ip != nil && isTrustedProxyIP(ip) {
			proto := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]))
			if proto == "http" || proto == "https" {
				return proto
			}
		}
		if r.TLS != nil {
			return "https"
		}
	}
	return "http"
}

// normalizeNodeInstallAddr 把站点配置的面板地址归一为带显式
// scheme 的 agent 连接地址（FIX2-C #2 / V6 §4）：
//   - 地址自带 scheme：http/ws → ws://、https/wss → wss:// 显式化；
//   - 裸地址：按面板实际服务方式（panelScheme）补 ws:// 或 wss://。
//
// 此前生成命令把地址原样写入节点 config.json，而 processServerAddress
// 还会把自带 scheme 剥掉——FIX-H 禁止 agent 自动降级后，裸地址默认
// wss，无 TLS 面板的新装节点重启即离线、只能人工改 config。归一只
// 作用于新生成的安装命令，存量节点 config 不动。
func normalizeNodeInstallAddr(panelAddr, panelScheme string) string {
	raw := strings.TrimSpace(panelAddr)
	scheme := ""
	if idx := strings.Index(raw, "://"); idx > 0 {
		switch strings.ToLower(raw[:idx]) {
		case "http", "https", "ws", "wss":
			scheme = strings.ToLower(raw[:idx])
		}
	}
	host := processServerAddress(raw)
	if host == "" {
		return raw
	}
	switch scheme {
	case "https", "wss":
		return "wss://" + host
	case "http", "ws":
		return "ws://" + host
	}
	if panelScheme == "https" {
		return "wss://" + host
	}
	return "ws://" + host
}

func (h *Handler) nodeInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}

	var req struct {
		ID      int64  `json:"id"`
		Channel string `json:"channel"`
	}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	if req.ID <= 0 {
		response.WriteJSON(w, response.ErrDefault("参数错误"))
		return
	}

	channel := normalizeReleaseChannel(req.Channel)
	version, err := resolveLatestReleaseByChannelWithProxy(channel, h.githubProxyURLForFetch())
	if err != nil {
		response.WriteJSON(w, response.Err(-2, fmt.Sprintf("获取最新%s失败: %v", releaseChannelLabel(channel), err)))
		return
	}

	secret, err := h.repo.GetNodeSecret(req.ID)
	if err != nil {
		response.WriteJSON(w, response.ErrDefault("节点不存在"))
		return
	}
	panelAddr, err := h.repo.GetViteConfigValue("ip")
	if err != nil {
		if err == sql.ErrNoRows {
			response.WriteJSON(w, response.ErrDefault("请先前往网站配置中设置ip"))
			return
		}
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	// PORT-CLOSURE O-C-01（顺手收口）：面板地址在生成安装命令前须
	// 过格式校验——双端口值产出畸形 WS 地址、安装 loud 失败；此处
	// 直接拒绝并指向网站配置（单 host:port 为正常面板地址，放行）。
	if err := validatePanelInstallAddr(panelAddr); err != nil {
		response.WriteJSON(w, response.ErrDefault("网站配置中的面板地址格式无效："+err.Error()+"，请前往网站配置检查"))
		return
	}
	enabled, proxyURL := h.getGithubProxyConfig()

	// F-060：生成的命令此前对将以 root 执行的 install.sh 本体零校验
	// （curl -L 连 HTTP 错误都会把错误页存下来执行）。现在与全仓
	// fail-closed 标准对齐：同下 release 的 install.sh.sha256（流水线
	// 为每个脚本资产生成，内容为纯哈希），sha256sum 比对通过才 chmod
	// 执行；任一步失败整条 && 链中止。校验锚与被校验文件同源同版本，
	// 与 download_verified 的信任模型一致（防传输篡改/新旧混杂）。
	const verifyStep = ` && test "$(awk '{print $1}' ./install.sh.sha256)" = "$(sha256sum ./install.sh | awk '{print $1}')"`
	// FIX2-C #2：节点 addr 归一为显式 scheme（见 normalizeNodeInstallAddr）。
	agentAddr := normalizeNodeInstallAddr(panelAddr, panelRequestScheme(r))
	var cmd string
	if enabled {
		cmd = fmt.Sprintf("curl -fsSL %s/https://github.com/%s/releases/download/%s/install.sh -o ./install.sh && curl -fsSL %s/https://github.com/%s/releases/download/%s/install.sh.sha256 -o ./install.sh.sha256"+verifyStep+" && chmod +x ./install.sh && PROXY_ENABLED=true PROXY_URL=%s VERSION=%s ./install.sh -a %s -s %s",
			proxyURL, githubRepo, version, proxyURL, githubRepo, version, proxyURL, version, agentAddr, secret)
	} else {
		cmd = fmt.Sprintf("curl -fsSL https://github.com/%s/releases/download/%s/install.sh -o ./install.sh && curl -fsSL https://github.com/%s/releases/download/%s/install.sh.sha256 -o ./install.sh.sha256"+verifyStep+" && chmod +x ./install.sh && PROXY_ENABLED=false VERSION=%s ./install.sh -a %s -s %s",
			githubRepo, version, githubRepo, version, version, agentAddr, secret)
	}
	response.WriteJSON(w, response.OK(cmd))
}

func (h *Handler) nodeUpdateOrder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req struct {
		Nodes []struct {
			ID  int64 `json:"id"`
			Inx int   `json:"inx"`
		} `json:"nodes"`
	}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	for _, n := range req.Nodes {
		h.repo.UpdateNodeOrder(n.ID, n.Inx, time.Now().UnixMilli())
	}
	response.WriteJSON(w, response.OKEmpty())
}

func (h *Handler) nodeDismissExpiryReminder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req struct {
		ID int64 `json:"id"`
	}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	if req.ID <= 0 {
		response.WriteJSON(w, response.ErrDefault("节点ID不能为空"))
		return
	}
	if err := h.repo.UpdateNodeExpiryReminderDismissed(req.ID, 1); err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	response.WriteJSON(w, response.OKEmpty())
}

func (h *Handler) nodeBatchDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	ids := idsFromBody(r, w)
	if ids == nil {
		return
	}
	// A2-05（=A9-04 后端半）：逐条收集成败并以 batchOperationResult
	// 回执（与 forward/tunnel/inbound 批量接口同口径）。旧实现吞掉全部
	// 错误恒 OKEmpty，前端只能按选中数虚报全成功、失败节点刷新后
	// 「复活」。响应体新增字段，向后兼容。
	success := 0
	fail := 0
	failures := make([]batchFailureDetail, 0)
	// R2X-045：批量入口与单删共用 deleteNodeByID funnel，跨节点引用
	// 清单汇总回传（同一引用方跨多个被删节点只报一次）。
	// R3X-014：合并以引用方入站 ID 为键——旧实现以显示串为键，同名
	// 同节点的两个不同引用方显示串相同会被吞掉一条。
	var brokenReferrers []string
	seenReferrer := make(map[int64]struct{})
	for _, id := range ids {
		nodeName := ""
		if n, nErr := h.getNodeRecord(id); nErr == nil && n != nil {
			nodeName = n.Name
		} else {
			// 节点不存在：DeleteNodeCascade 删 0 行不报错，必须在这一层
			// 如实记失败，否则回执仍会把未删项报成功。
			fail++
			failures = appendBatchFailureReason(failures, id, nodeName, "节点不存在")
			continue
		}
		nodeReferrers, delErr := h.deleteNodeByID(id)
		// B4/F-056（X1-203 变体）: 批量删除此前连单删有的
		// nftablesManager.ReleaseNode 都不调，manager map 条目泄漏。
		if h.nftablesManager != nil {
			h.nftablesManager.ReleaseNode(id)
		}
		if delErr != nil {
			fail++
			failures = appendBatchFailure(failures, id, nodeName, delErr)
			continue
		}
		for _, referrer := range nodeReferrers {
			if _, dup := seenReferrer[referrer.ID]; dup {
				continue
			}
			seenReferrer[referrer.ID] = struct{}{}
			brokenReferrers = append(brokenReferrers, referrer.Display)
		}
		success++
	}
	response.WriteJSON(w, response.OK(batchOperationResult{SuccessCount: success, FailCount: fail, Failures: failures, BrokenReferrers: brokenReferrers}))
}

func (h *Handler) nodeCheckStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	items, err := h.repo.ListNodes()
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	response.WriteJSON(w, response.OK(items))
}

func (h *Handler) tunnelCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req map[string]interface{}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	// R2X-004：创建面接入 Idempotency-Key（机制见 idempotency.go）。
	{
		var finish func()
		var replayed bool
		w, finish, replayed = h.idemBegin(w, r, idemUserID(r), "tunnel:create")
		defer finish()
		if replayed {
			return
		}
	}
	if err := validateTunnelConnectIPConstraints(req); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	name := asString(req["name"])
	if name == "" {
		response.WriteJSON(w, response.ErrDefault("隧道名称不能为空"))
		return
	}
	nameDup, err := h.repo.TunnelNameExists(name)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if nameDup {
		response.WriteJSON(w, response.ErrDefault("隧道名称重复"))
		return
	}

	// FIX2-A（V2-07，与 tunnelUpdate 同形）：在场但不可解析的数值字段
	// fail-closed，不落库；须在 asInt64/asFloat 默认化之前校验，否则
	// 坏输入静默套硬默认、校验恒过（R12X-003 create 侧缺口）。
	// R13X-002：键表由 flow/trafficRatio 补齐为与 tunnelUpdate 同形的
	// 四键（type/flow/status + trafficRatio）；status 的数值域外
	// 枚举门（L4 O-3）不在本断言内，此处只拦不可解析输入。
	for _, key := range []string{"type", "flow", "status"} {
		if presentNumericFieldInvalid(req, key) {
			response.WriteJSON(w, response.ErrDefault(fmt.Sprintf("%s 参数无效", key)))
			return
		}
	}
	if presentFloatFieldInvalid(req, "trafficRatio") {
		response.WriteJSON(w, response.ErrDefault("trafficRatio 参数无效"))
		return
	}
	typeVal := asInt(req["type"], 1)
	flow := asInt64(req["flow"], 1)
	status := asInt(req["status"], 1)
	trafficRatio := asFloat(req["trafficRatio"], 1.0)
	// R11X-008/009：写入面补校验——TrafficRatio ∈ [0,100]、Flow ∈
	// {1,2}，域外值点名拒绝，不落库（合法值落库逐字不变）。
	if err := validateTunnelTrafficRatio(trafficRatio); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	if err := validateTunnelFlow(flow); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	// R31-L1-E01：显式 inIp 与系统派生值同口径——按 , 分项复用
	// stripShareHostPort 剥离 :port 后落库（整串 strip 对含逗号串
	// 无效）。空值仍走下方 buildTunnelInIP 派生；存量不改写。
	// PORT-CLOSURE F-07 扩展：显式 inIp 须过逐项格式校验，双端口
	// 即拒（此前单次剥离对双端口为 no-op，原值落库后消费侧畸形，
	// 与 PC-04/PC-05 同机制）；单端口剥离口径不变。
	inIP, err := stripAndValidateNodeAddressList(asString(req["inIp"]))
	if err != nil {
		response.WriteJSON(w, response.ErrDefault("隧道入口 IP 格式无效："+err.Error()))
		return
	}
	ipPreference := asString(req["ipPreference"])
	probeTarget, probeTargetConfigured, err := parseTunnelProbeTargetFromRequest(req)
	if err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	probeTargetHost := ""
	probeTargetPort := 0
	if probeTargetConfigured {
		probeTargetHost = probeTarget.Host
		probeTargetPort = probeTarget.Port
	}
	engine, err := normalizeTunnelEngine(asString(req["engine"]))
	if err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	now := time.Now().UnixMilli()
	inx := h.repo.NextIndex("tunnel")
	localDomain := h.federationLocalDomain()

	// B4/F-012: 创建流程重排为四段。此前整个创建共用一把「仅含出口
	// 节点」的端口锁 + 一个长事务，联邦 HTTP（fc.CreateTunnel 与
	// applyFederationRuntime，单次最长 10s）全在锁+事务内执行——
	// SQLite 单连接下远端慢响应让全站 DB 排队，且入口/链式节点根本
	// 不在锁内，违背 port_alloc_lock「锁内禁止网络调用」的自述约束。
	// 现在：1) 验证段（无锁、throwaway 事务 prepare+校验，算出联邦
	// CreateTunnel 参数后回滚）；2) 联邦 CreateTunnel 在锁/事务外；
	// 3) 提交段（锁覆盖入/出/链全部节点，事务内重新 prepare 定型端口
	// 后建隧道+链路，提交即释锁）；4) applyFederationRuntime 在锁外，
	// 成功后以短事务落链路（联邦分配口）与绑定，失败释放远端预留并
	// 删除刚建的隧道，保持「失败不留半成品」的原语义。
	validationTx := h.repo.BeginTx()
	if validationTx.Error != nil {
		response.WriteJSON(w, response.Err(-2, validationTx.Error.Error()))
		return
	}
	runtimeState, err := h.prepareTunnelCreateState(validationTx, req, typeVal, 0)
	if err != nil {
		validationTx.Rollback()
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	runtimeState.IPPreference = ipPreference
	if strings.TrimSpace(inIP) == "" {
		inIP = buildTunnelInIP(runtimeState.InNodes, runtimeState.Nodes, ipPreference)
	}
	entryNodeIDs := make([]int64, 0, len(runtimeState.InNodes))
	for _, inNode := range runtimeState.InNodes {
		if inNode.NodeID > 0 {
			entryNodeIDs = append(entryNodeIDs, inNode.NodeID)
		}
	}
	// A3-01：验证段内只收集 nftables 入口的 SSH 配置快照（纯 DB），
	// SSH 能力校验（网络调用）在验证事务回滚后、提交段持锁前执行，
	// 与下方联邦 HTTP 的出锁口径一致。
	nftPairs, err := h.collectNftablesSSHConfigsTx(validationTx, entryNodeIDs)
	if err != nil {
		validationTx.Rollback()
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	if engine == tunnelEngineRealm {
		if err := h.validateRealmTunnelTx(validationTx, typeVal, entryNodeIDs); err != nil {
			validationTx.Rollback()
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
	}
	if err := validateTunnelRuntimeBeforeApply(runtimeState); err != nil {
		validationTx.Rollback()
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}

	// 联邦 CreateTunnel 参数：只依赖请求与节点记录，在事务内算出、
	// 回滚后才真正发出 HTTP（R5 C-001 的 localDomain 复用保持不变）。
	type federationCreateCall struct {
		url, token, proto, addr string
		port                    int
	}
	var fcCall *federationCreateCall
	if len(runtimeState.InNodes) > 0 {
		firstNodeID := runtimeState.InNodes[0].NodeID
		isRemote, rUrl, rToken, _ := h.repo.GetNodeRemoteFieldsTx(validationTx, firstNodeID)
		if isRemote == 1 && typeVal == 1 {
			targetProto := "tcp"
			targetPort := 0
			targetAddr := ""
			if len(runtimeState.OutNodes) > 0 {
				outNode := runtimeState.OutNodes[0]
				outNodeRec := runtimeState.Nodes[outNode.NodeID]
				// R28-FIX-F01（sweep）：ServerIP 可含 :port；先剥离再
				// 拼 outNode.Port，否则联邦建链地址成 "IP:6366:port"
				// 双端口。processServerAddress 本身不剥 :port。
				targetAddr = processServerAddress(stripShareHostPort(outNodeRec.ServerIP))
				if outNode.Port > 0 {
					targetAddr = fmt.Sprintf("%s:%d", targetAddr, outNode.Port)
				}
			}
			inNodesRaw := asMapSlice(req["inNodeId"])
			if len(inNodesRaw) > 0 {
				targetPort = asInt(inNodesRaw[0]["port"], 0)
				targetProto = defaultString(asString(inNodesRaw[0]["protocol"]), "tcp")
			}
			if targetPort > 0 && targetAddr != "" {
				inNodeRec := runtimeState.Nodes[firstNodeID]
				if err := validateRemoteNodePort(inNodeRec, targetPort); err != nil {
					validationTx.Rollback()
					response.WriteJSON(w, response.ErrDefault(err.Error()))
					return
				}
				fcCall = &federationCreateCall{url: rUrl.String, token: rToken.String, proto: targetProto, addr: targetAddr, port: targetPort}
			}
		}
	}
	validationTx.Rollback()

	// A3-01：SSH 能力校验在此执行（无事务、无端口锁）。提交段内只
	// 保留 validateNftablesTunnelStateTx 的 DB 侧复检（模式与配置
	// 存在性），不再重复网络校验。
	if err := h.testNftablesNodeCapabilities(nftPairs); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}

	if fcCall != nil {
		fc := client.NewFederationClient()
		if _, err := fc.CreateTunnel(fcCall.url, fcCall.token, localDomain, fcCall.proto, fcCall.port, fcCall.addr); err != nil {
			response.WriteJSON(w, response.ErrDefault("Remote tunnel creation failed: "+err.Error()))
			return
		}
	}

	// 提交段：全节点锁内重新 prepare 定型端口（验证段的选口结果不
	// 跨段复用，避免锁外窗口被并发占用）。
	releasePortLocks := acquireSortedPortLocks(tunnelRequestNodeIDs(req))
	tx := h.repo.BeginTx()
	if tx.Error != nil {
		releasePortLocks()
		response.WriteJSON(w, response.Err(-2, tx.Error.Error()))
		return
	}
	runtimeState, err = h.prepareTunnelCreateState(tx, req, typeVal, 0)
	if err != nil {
		tx.Rollback()
		releasePortLocks()
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	runtimeState.IPPreference = ipPreference
	if strings.TrimSpace(inIP) == "" {
		inIP = buildTunnelInIP(runtimeState.InNodes, runtimeState.Nodes, ipPreference)
	}
	entryNodeIDs = entryNodeIDs[:0]
	for _, inNode := range runtimeState.InNodes {
		if inNode.NodeID > 0 {
			entryNodeIDs = append(entryNodeIDs, inNode.NodeID)
		}
	}
	if err := h.validateNftablesTunnelStateTx(tx, entryNodeIDs); err != nil {
		tx.Rollback()
		releasePortLocks()
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	if engine == tunnelEngineRealm {
		if err := h.validateRealmTunnelTx(tx, typeVal, entryNodeIDs); err != nil {
			tx.Rollback()
			releasePortLocks()
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
	}
	if err := validateTunnelRuntimeBeforeApply(runtimeState); err != nil {
		tx.Rollback()
		releasePortLocks()
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}

	var tunnelInIP sql.NullString
	if trimmed := strings.TrimSpace(inIP); trimmed != "" {
		tunnelInIP = sql.NullString{String: trimmed, Valid: true}
	}
	tunnelProtocol := "tls"
	if len(runtimeState.InNodes) > 0 && strings.TrimSpace(runtimeState.InNodes[0].Protocol) != "" {
		tunnelProtocol = strings.TrimSpace(runtimeState.InNodes[0].Protocol)
	}
	tunnel := model.Tunnel{
		Name:            name,
		TrafficRatio:    trafficRatio,
		Type:            typeVal,
		Protocol:        tunnelProtocol,
		Flow:            flow,
		CreatedTime:     now,
		UpdatedTime:     now,
		Status:          status,
		InIP:            tunnelInIP,
		Inx:             inx,
		IPPreference:    ipPreference,
		ProbeTargetHost: probeTargetHost,
		ProbeTargetPort: probeTargetPort,
		Engine:          engine,
	}
	// R2X-049：提交段落库走 InsertTunnelTx——事务内复检同名后才建行
	// （锁外预检与此处之间隔着联邦 HTTP，并发同名曾可双建）。
	if err := h.repo.InsertTunnelTx(tx, &tunnel); err != nil {
		tx.Rollback()
		releasePortLocks()
		if errors.Is(err, repo.ErrTunnelNameDuplicate) {
			response.WriteJSON(w, response.ErrDefault("隧道名称重复"))
			return
		}
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	tunnelID := tunnel.ID
	runtimeState.TunnelID = tunnelID
	applyTunnelPortsToRequest(req, runtimeState)
	if err := h.replaceTunnelChainsTx(tx, tunnelID, req); err != nil {
		tx.Rollback()
		releasePortLocks()
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if err := tx.Commit().Error; err != nil {
		tx.Rollback()
		releasePortLocks()
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	releasePortLocks()

	// 联邦运行时（HTTP）在锁/事务之外执行；成功后以短事务落链路
	// （含联邦分配口）与绑定。失败释放远端预留并删除刚建的隧道。
	federationBindings, federationReleaseRefs, err := h.applyFederationRuntime(runtimeState, localDomain)
	if err != nil {
		err = errors.Join(err, h.releaseFederationRuntimeRefs(federationReleaseRefs))
		_ = h.deleteTunnelByID(tunnelID)
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	if len(federationBindings) > 0 || len(federationReleaseRefs) > 0 {
		applyTunnelPortsToRequest(req, runtimeState)
		bindTx := h.repo.BeginTx()
		if bindTx.Error != nil {
			err := errors.Join(bindTx.Error, h.releaseFederationRuntimeRefs(federationReleaseRefs))
			_ = h.deleteTunnelByID(tunnelID)
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
		bindErr := func() error {
			defer bindTx.Rollback()
			if err := h.repo.DeleteChainTunnelsByTunnelTx(bindTx, tunnelID); err != nil {
				return err
			}
			if err := h.replaceTunnelChainsTx(bindTx, tunnelID, req); err != nil {
				return err
			}
			if err := h.repo.ReplaceFederationTunnelBindingsTx(bindTx, tunnelID, federationBindings); err != nil {
				return err
			}
			return bindTx.Commit().Error
		}()
		if bindErr != nil {
			err := errors.Join(bindErr, h.releaseFederationRuntimeRefs(federationReleaseRefs))
			_ = h.deleteTunnelByID(tunnelID)
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
	}
	if typeVal == 2 {
		createdChains, createdServices, applyErr := h.applyTunnelRuntime(runtimeState)
		if applyErr != nil {
			h.rollbackTunnelRuntime(createdChains, createdServices, tunnelID, tunnelProtocol)
			if cleanupErr := h.cleanupFederationRuntime(tunnelID); cleanupErr != nil {
				applyErr = errors.Join(applyErr, cleanupErr)
			} else {
				_ = h.deleteTunnelByID(tunnelID)
			}
			response.WriteJSON(w, response.ErrDefault(applyErr.Error()))
			return
		}
	}
	response.WriteJSON(w, response.OKEmpty())
}

func (h *Handler) cleanupTunnelRuntime(tunnelID int64) {
	tunnel, err := h.getTunnelRecord(tunnelID)
	if err != nil || tunnel.Type != 2 {
		return
	}
	chainRows, err := h.listChainNodesForTunnel(tunnelID)
	if err != nil {
		return
	}

	chainName := fmt.Sprintf("chains_%d", tunnelID)
	serviceNames := tunnelRuntimeServiceNames(tunnelID)

	for _, row := range chainRows {
		if row.ChainType == 1 {
			_, _ = h.sendNodeCommand(row.NodeID, "DeleteChains", map[string]interface{}{"chain": chainName}, false, true)
		} else if row.ChainType == 2 {
			_, _ = h.sendNodeCommand(row.NodeID, "DeleteChains", map[string]interface{}{"chain": chainName}, false, true)
			_, _ = h.sendNodeCommand(row.NodeID, "DeleteService", map[string]interface{}{"services": serviceNames}, false, true)
		} else if row.ChainType == 3 {
			_, _ = h.sendNodeCommand(row.NodeID, "DeleteService", map[string]interface{}{"services": serviceNames}, false, true)
		}
	}
}

func tunnelRuntimeServiceNames(tunnelID int64) []string {
	return []string{
		fmt.Sprintf("tunnel_%d", tunnelID),
		fmt.Sprintf("%d_tls", tunnelID),
		fmt.Sprintf("%d_kcp", tunnelID),
		fmt.Sprintf("%d_wss", tunnelID),
		fmt.Sprintf("%d_mtls", tunnelID),
		fmt.Sprintf("%d_mwss", tunnelID),
		fmt.Sprintf("%d_tcp", tunnelID),
		fmt.Sprintf("%d_mtcp", tunnelID),
	}
}

func tunnelRuntimeNeedsChain(row chainNodeRecord) bool {
	return row.ChainType == 1 || row.ChainType == 2
}

func tunnelRuntimeNeedsService(row chainNodeRecord) bool {
	return row.ChainType == 2 || row.ChainType == 3
}

func removedTunnelRuntimeNodeIDs(oldRows, newRows []chainNodeRecord, needsRuntime func(chainNodeRecord) bool) []int64 {
	if len(oldRows) == 0 || needsRuntime == nil {
		return nil
	}
	newRuntimeNodes := make(map[int64]struct{}, len(newRows))
	for _, row := range newRows {
		if row.NodeID <= 0 || !needsRuntime(row) {
			continue
		}
		newRuntimeNodes[row.NodeID] = struct{}{}
	}
	seen := make(map[int64]struct{}, len(oldRows))
	removed := make([]int64, 0)
	for _, row := range oldRows {
		if row.NodeID <= 0 || !needsRuntime(row) {
			continue
		}
		if _, ok := seen[row.NodeID]; ok {
			continue
		}
		seen[row.NodeID] = struct{}{}
		if _, stillNeeded := newRuntimeNodes[row.NodeID]; stillNeeded {
			continue
		}
		removed = append(removed, row.NodeID)
	}
	return removed
}

func (h *Handler) cleanupObsoleteTunnelRuntime(tunnelID int64, oldRows, newRows []chainNodeRecord) {
	if h == nil || tunnelID <= 0 || len(oldRows) == 0 {
		return
	}
	chainName := fmt.Sprintf("chains_%d", tunnelID)
	for _, nodeID := range removedTunnelRuntimeNodeIDs(oldRows, newRows, tunnelRuntimeNeedsChain) {
		_, _ = h.sendNodeCommand(nodeID, "DeleteChains", map[string]interface{}{"chain": chainName}, false, true)
	}
	serviceNames := tunnelRuntimeServiceNames(tunnelID)
	for _, nodeID := range removedTunnelRuntimeNodeIDs(oldRows, newRows, tunnelRuntimeNeedsService) {
		_, _ = h.sendNodeCommand(nodeID, "DeleteService", map[string]interface{}{"services": serviceNames}, false, true)
	}
}

func (h *Handler) tunnelGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	id := idFromBody(r, w)
	if id <= 0 {
		return
	}
	items, err := h.repo.ListTunnels()
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	h.attachBestExitStates(items)
	for _, it := range items {
		if asInt64(it["id"], 0) == id {
			response.WriteJSON(w, response.OK(it))
			return
		}
	}
	response.WriteJSON(w, response.ErrDefault("隧道不存在"))
}

// acquireSortedPortLocks takes the per-node port-allocation locks for
// the given node IDs in ascending order (deadlock-free) and returns the
// release function. B4/F-014.
func acquireSortedPortLocks(nodeIDs []int64) func() {
	ids := uniqueInt64s(nodeIDs)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	locks := make([]*sync.Mutex, 0, len(ids))
	for _, id := range ids {
		if id > 0 {
			locks = append(locks, repo.AcquirePortAllocLock(id))
		}
	}
	return func() {
		for i := len(locks) - 1; i >= 0; i-- {
			repo.ReleasePortAllocLock(locks[i])
		}
	}
}

// tunnelRequestNodeIDs collects every node ID referenced by a tunnel
// create/update request (entry, exit, and chain hops).
func tunnelRequestNodeIDs(req map[string]interface{}) []int64 {
	ids := make([]int64, 0)
	for _, item := range asMapSlice(req["inNodeId"]) {
		if id := asInt64(item["nodeId"], 0); id > 0 {
			ids = append(ids, id)
		}
	}
	for _, item := range asMapSlice(req["outNodeId"]) {
		if id := asInt64(item["nodeId"], 0); id > 0 {
			ids = append(ids, id)
		}
	}
	for _, hopRaw := range asAnySlice(req["chainNodes"]) {
		for _, item := range asMapSlice(hopRaw) {
			if id := asInt64(item["nodeId"], 0); id > 0 {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

func (h *Handler) tunnelUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req map[string]interface{}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	if err := validateTunnelConnectIPConstraints(req); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	id := asInt64(req["id"], 0)
	if id <= 0 {
		response.WriteJSON(w, response.ErrDefault("隧道ID不能为空"))
		return
	}
	// FIX2-A（V2-07）：在场但不可解析的数值字段 fail-closed，不落
	// 库（flow 旧口径静默套硬默认 1，type/status/trafficRatio 同族）。
	for _, key := range []string{"type", "flow", "status"} {
		if presentNumericFieldInvalid(req, key) {
			response.WriteJSON(w, response.ErrDefault(fmt.Sprintf("%s 参数无效", key)))
			return
		}
	}
	if presentFloatFieldInvalid(req, "trafficRatio") {
		response.WriteJSON(w, response.ErrDefault("trafficRatio 参数无效"))
		return
	}
	// R11X-008/009：写入面补校验（与 tunnelCreate 同口径）——仅校验
	// 请求中在场且非空的字段：部分更新缺席字段回填的是存量值
	// （A2-04 / R4X-004 的 presence 口径），存量域外行做无关编辑不得
	// 被连坐拒绝；在场域外值点名拒绝、不落库。
	if raw, ok := req["flow"]; ok && asString(raw) != "" {
		if err := validateTunnelFlow(asInt64(raw, 1)); err != nil {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
	}
	if raw, ok := req["trafficRatio"]; ok && asString(raw) != "" {
		if err := validateTunnelTrafficRatio(asFloat(raw, 1.0)); err != nil {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
	}
	typeVal := asInt(req["type"], 1)
	_, hasType := req["type"]
	ipPreference := asString(req["ipPreference"])
	_, hasIPPreference := req["ipPreference"]
	_, hasProbeTargetHost := req["probeTargetHost"]
	_, hasProbeTargetPort := req["probeTargetPort"]
	probeTargetFieldsPresent := hasProbeTargetHost || hasProbeTargetPort
	probeTargetHost := ""
	probeTargetPort := 0
	if probeTargetFieldsPresent {
		probeTarget, probeTargetConfigured, err := parseTunnelProbeTargetFromRequest(req)
		if err != nil {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
		if probeTargetConfigured {
			probeTargetHost = probeTarget.Host
			probeTargetPort = probeTarget.Port
		}
	}
	oldEntryNodeIDs, err := h.tunnelEntryNodeIDs(id)
	if err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	// R4X-004：旧值是下方 presence 回填的写回源，必须走不归一化的
	// 原始读取——归一化版会把存量 traffic_ratio=0（及 flow<=0）在
	// 读取层改成 1，回填后无条件写库，零计费被静默撤销。
	oldTunnel, err := h.getTunnelRecordRaw(id)
	if err != nil || oldTunnel == nil {
		response.WriteJSON(w, response.ErrDefault("隧道不存在"))
		return
	}
	if !probeTargetFieldsPresent && oldTunnel != nil {
		probeTargetHost = oldTunnel.ProbeTargetHost
		probeTargetPort = oldTunnel.ProbeTargetPort
	}
	engine := oldTunnel.Engine
	if raw, ok := req["engine"]; ok {
		parsedEngine, pErr := normalizeTunnelEngine(asString(raw))
		if pErr != nil {
			response.WriteJSON(w, response.ErrDefault(pErr.Error()))
			return
		}
		engine = parsedEngine
	}
	oldChainRows, err := h.listChainNodesForTunnel(id)
	if err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}

	// A2-04（BC-06）：真部分更新——标量字段未传保持现值，不再被硬默认
	// 覆写（旧口径：未传 name 清空、flow→1、trafficRatio→1.0、
	// status→1、type→1、ipPreference 清空）。链结构仍按请求重建
	// （inNodeId 等结构字段是本接口的全量语义，不在本条范围）。
	resolvedName := asString(req["name"])
	if _, ok := req["name"]; !ok {
		resolvedName, _ = h.repo.GetTunnelName(id)
	}
	resolvedFlow := asInt64(req["flow"], 1)
	if _, ok := req["flow"]; !ok {
		resolvedFlow = oldTunnel.Flow
	}
	resolvedRatio := asFloat(req["trafficRatio"], 1.0)
	if _, ok := req["trafficRatio"]; !ok {
		resolvedRatio = oldTunnel.TrafficRatio
	}
	resolvedStatus := asInt(req["status"], 1)
	if _, ok := req["status"]; !ok {
		resolvedStatus = oldTunnel.Status
	}
	if !hasType {
		typeVal = oldTunnel.Type
	}
	if !hasIPPreference {
		ipPreference = h.repo.GetTunnelIPPreference(id)
	}

	now := time.Now().UnixMilli()
	localDomain := h.federationLocalDomain()

	// B4/F-014: 隧道更新此前全程无 per-node 端口锁——验证事务与正式
	// 提交之间隔着联邦网络调用，与并发的转发/入站/隧道创建存在跨表
	// 「先查后写」窗口。现在按 A3 口径两段加锁：验证段持锁（旧+新全部
	// 涉及节点，排序获取），网络段锁外，提交段重新持锁并在锁内复检
	// 最终显式端口占用后再提交。errCode 复刻原各失败点的响应口径
	// （-2 = 系统错，0 = 默认业务错）。
	lockNodeIDs := append([]int64{}, oldEntryNodeIDs...)
	for _, row := range oldChainRows {
		lockNodeIDs = append(lockNodeIDs, row.NodeID)
	}
	lockNodeIDs = append(lockNodeIDs, tunnelRequestNodeIDs(req)...)

	var runtimeState *tunnelCreateState
	var inIp string
	var updateProtocol string
	var nftPairs []nftablesNodeSSHConfig
	vErr, vCode := func() (error, int) {
		release := acquireSortedPortLocks(lockNodeIDs)
		defer release()
		// Check the complete request and known database constraints before changing
		// any existing listener or remote binding.
		validationTx := h.repo.BeginTx()
		if validationTx.Error != nil {
			return validationTx.Error, -2
		}
		defer validationTx.Rollback()
		var err error
		runtimeState, err = h.prepareTunnelCreateState(validationTx, req, typeVal, id)
		if err != nil {
			return err, 0
		}
		runtimeState.TunnelID = id
		runtimeState.IPPreference = ipPreference
		entryNodeIDs := make([]int64, 0, len(runtimeState.InNodes))
		for _, inNode := range runtimeState.InNodes {
			if inNode.NodeID > 0 {
				entryNodeIDs = append(entryNodeIDs, inNode.NodeID)
			}
		}
		// A3-01：验证段持锁+持事务，只收集 SSH 配置快照（纯 DB）；
		// 能力校验（网络调用）在本段结束、锁与事务释放后执行。
		pairs, err := h.collectNftablesSSHConfigsTx(validationTx, entryNodeIDs)
		if err != nil {
			return err, 0
		}
		nftPairs = pairs
		// R35-FIX-F01（a）：任一新入口为 nftables 节点时，编辑后的
		// 入口集必须仍满足 validateNftablesForwardRequest 口径（直
		// 连隧道 + 单入口）——否则新入口写进 forward_port 后，
		// syncForwardServices 即被单入口门拒绝（此前该错误被 `_ =`
		// 吞掉），DB 与数据面分叉。realm 隧道由下方
		// validateRealmTunnelTx 另行拒绝 nft 入口。
		if len(pairs) > 0 {
			if typeVal != 1 {
				return errors.New("nftables 节点仅支持直连隧道"), 0
			}
			if len(entryNodeIDs) != 1 {
				return errors.New("nftables 节点仅支持单入口隧道"), 0
			}
		}
		if engine == tunnelEngineRealm {
			if err := h.validateRealmTunnelTx(validationTx, typeVal, entryNodeIDs); err != nil {
				return err, 0
			}
		}
		if err := h.validateTunnelEntryPortConflictsForNewEntriesTx(validationTx, id, oldEntryNodeIDs, entryNodeIDs); err != nil {
			return err, 0
		}
		if err := validateTunnelRuntimeBeforeApply(runtimeState); err != nil {
			return err, 0
		}
		inIp = buildTunnelInIP(runtimeState.InNodes, runtimeState.Nodes, ipPreference)
		// A9-05（BC-21）：与 create 对齐——请求显式给出 inIp 时保留自定义
		// 入口 IP，不再无条件被系统采集值覆盖（旧实现编辑即丢失自定义值，
		// 前端回填的旧值也因此恒被重算）。R31-L1-E01：显式值按 , 分项
		// 剥离 :port 后覆盖，与 buildTunnelInIP 的已剥离口径一致。
		if v := strings.TrimSpace(asString(req["inIp"])); v != "" {
			// PORT-CLOSURE F-07 扩展：显式 inIp 逐项校验，双端口即拒
			// （与 create 侧同口径；此前单次剥离对双端口为 no-op，
			// 原值覆盖 buildTunnelInIP 的已剥离结果）。
			stripped, err := stripAndValidateNodeAddressList(v)
			if err != nil {
				return fmt.Errorf("隧道入口 IP 格式无效：%s", err), 0
			}
			inIp = stripped
		}
		updateProtocol = "tls"
		if len(runtimeState.OutNodes) > 0 && strings.TrimSpace(runtimeState.OutNodes[0].Protocol) != "" {
			updateProtocol = strings.TrimSpace(runtimeState.OutNodes[0].Protocol)
		} else if len(runtimeState.InNodes) > 0 && strings.TrimSpace(runtimeState.InNodes[0].Protocol) != "" {
			updateProtocol = strings.TrimSpace(runtimeState.InNodes[0].Protocol)
		}
		if err := h.repo.UpdateTunnelTx(validationTx, id, resolvedName, typeVal,
			resolvedFlow, resolvedRatio, resolvedStatus,
			inIp, ipPreference, updateProtocol, probeTargetHost, probeTargetPort, now); err != nil {
			return err, -2
		}
		if err := h.validateTunnelChainReplacementTx(validationTx, runtimeState); err != nil {
			return err, -2
		}
		if err := validationTx.Rollback().Error; err != nil {
			return err, -2
		}
		return nil, 0
	}()
	if vErr != nil {
		if vCode == -2 {
			response.WriteJSON(w, response.Err(-2, vErr.Error()))
		} else {
			response.WriteJSON(w, response.ErrDefault(vErr.Error()))
		}
		return
	}
	// X-P2-4 ①（E-S1）：名下中转转发逐条复跑 onLanding 判定——新出口集
	// 让某个落地入站「出口为落地但非单出口」时拒绝保存（文案与
	// relayTarget 一致），否则转发继续拨回环地址静默错向。置于验证段
	// 之后、提交段之前：验证事务已回滚，此处走主句柄读 relays/inbounds
	// 不与事务锁冲突；runtimeState.OutNodes 即待提交的新出口集。
	var newExits []int64
	if typeVal == 2 && runtimeState != nil {
		newExits = make([]int64, 0, len(runtimeState.OutNodes))
		for _, outNode := range runtimeState.OutNodes {
			if outNode.NodeID > 0 {
				newExits = append(newExits, outNode.NodeID)
			}
		}
		if err := h.validateTunnelRelayExits(id, newExits); err != nil {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
	}
	// R2X-009：以待提交的隧道态（新类型 + 新出口集）对名下 Managed
	// relay 逐条预演 RemoteAddr 可计算性——出口集迁移（{L}↔{X}）与
	// 类型迁移（2→1）都会改变 relayTarget 口径，预演失败拒绝保存，
	// 不留「地址不变静默断流」的物化旧址。提交落库后在白名单刷新
	// 同位置以已落库态重算落库（见下方 resync）。
	if runtimeState != nil {
		prospective := &tunnelRecord{ID: id, Type: typeVal}
		if err := h.validateTunnelRelayRemoteAddrComputable(id, prospective, newExits); err != nil {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
	}
	// A3-01：SSH 能力校验在验证段的锁与事务都释放后执行（网络调用
	// 不进临界区）；失败时尚未触碰任何运行时/DB 状态，语义与原
	// 验证段内校验一致。
	if err := h.testNftablesNodeCapabilities(nftPairs); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	// R2X-010 ②：编辑是非删除操作——联邦清理失败中止时把本轮新置的
	// pending 绑定回滚为 active，避免重试环日后拆掉仍在用的隧道。
	if err := h.cleanupFederationRuntimePreserving(id); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	if oldTunnel.Type == 2 && typeVal != 2 {
		h.cleanupTunnelRuntime(id)
	}

	var federationBindings []repo.FederationTunnelBinding
	var federationReleaseRefs []federationRuntimeReleaseRef
	federationBindings, federationReleaseRefs, err = h.applyFederationRuntime(runtimeState, localDomain)
	if err != nil {
		err = errors.Join(err, h.releaseFederationRuntimeRefs(federationReleaseRefs))
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	applyTunnelPortsToRequest(req, runtimeState)

	// B4/F-014 提交段：重新持锁，锁内先复检最终显式端口（联邦下发后
	// 的 runtimeState 口径）的跨表占用，再执行正式写入并提交；锁在
	// 提交/回滚后即释放，后续运行时下发（网络）不持锁。
	cErr, cCode := func() (error, int) {
		release := acquireSortedPortLocks(lockNodeIDs)
		defer release()
		tx := h.repo.BeginTx()
		if tx.Error != nil {
			return tx.Error, -2
		}
		defer func() { tx.Rollback() }()

		finalPortNodes := append([]tunnelRuntimeNode{}, runtimeState.OutNodes...)
		for _, hop := range runtimeState.ChainHops {
			finalPortNodes = append(finalPortNodes, hop...)
		}
		for _, n := range finalPortNodes {
			if n.Port <= 0 {
				continue
			}
			node := runtimeState.Nodes[n.NodeID]
			if node == nil || node.IsRemote == 1 {
				continue
			}
			occupied, err := h.repo.NodePortOccupiedForTunnelTx(tx, n.NodeID, n.Port, id)
			if err != nil {
				return err, -2
			}
			if occupied {
				return fmt.Errorf("节点 %s 端口 %d 已被占用", node.Name, n.Port), 0
			}
		}

		if err := h.repo.UpdateTunnelTx(
			tx,
			id,
			resolvedName,
			typeVal,
			resolvedFlow,
			resolvedRatio,
			resolvedStatus,
			inIp,
			ipPreference,
			updateProtocol,
			probeTargetHost,
			probeTargetPort,
			now,
		); err != nil {
			return err, -2
		}
		if err := h.repo.SetTunnelEngineTx(tx, id, engine); err != nil {
			return err, -2
		}

		if err := h.repo.DeleteChainTunnelsByTunnelTx(tx, id); err != nil {
			return err, -2
		}
		if err := h.replaceTunnelChainsTx(tx, id, req); err != nil {
			return err, -2
		}
		if err := h.repo.ReplaceFederationTunnelBindingsTx(tx, id, federationBindings); err != nil {
			return err, -2
		}

		newEntryNodeIDs := make([]int64, 0, len(runtimeState.InNodes))
		for _, in := range runtimeState.InNodes {
			if in.NodeID > 0 {
				newEntryNodeIDs = append(newEntryNodeIDs, in.NodeID)
			}
		}
		if err := h.validateTunnelEntryPortConflictsForNewEntriesTx(tx, id, oldEntryNodeIDs, newEntryNodeIDs); err != nil {
			return err, 0
		}

		if err := tx.Commit().Error; err != nil {
			return err, -2
		}
		return nil, 0
	}()
	if cErr != nil {
		joined := errors.Join(cErr, h.releaseFederationRuntimeRefs(federationReleaseRefs))
		if cCode == -2 {
			response.WriteJSON(w, response.Err(-2, joined.Error()))
		} else {
			response.WriteJSON(w, response.ErrDefault(joined.Error()))
		}
		return
	}

	// X-P2-3（E-02）：隧道编辑改了入口/出口后，名下中转转发的拨号节点
	// 随之变化——按「隧道名下转发 → relay → inboundID」收集受影响入站
	// 逐个刷新白名单，否则新出口 IP 不在 allow 内（中转静默断）、旧
	// 出口 IP 残留放行。提交已落库，此处刷新与后续成功分支无关。
	h.refreshInboundWhitelists(h.inboundIDsForTunnel(id))

	// R2X-009：与白名单刷新同位置——以已落库的隧道态对名下 Managed
	// relay 逐条重算 RemoteAddr，变更则落库 + 重下发，失败汇入
	// warnings 随成功回执带出（不静默）。
	relayResyncWarnings := h.resyncManagedRelayRemoteAddrsForTunnel(id)
	respondTunnelUpdateOK := func() {
		if len(relayResyncWarnings) > 0 {
			response.WriteJSON(w, response.R{Code: 0, Msg: "已保存，但" + strings.Join(relayResyncWarnings, "；"), TS: now, Data: map[string]interface{}{"warnings": relayResyncWarnings}})
			return
		}
		response.WriteJSON(w, response.OKEmpty())
	}

	newEntryNodeIDs, _ := h.tunnelEntryNodeIDs(id)
	if !sameInt64Set(oldEntryNodeIDs, newEntryNodeIDs) {
		h.cleanupTunnelForwardRuntimesOnRemovedEntryNodes(id, oldEntryNodeIDs, newEntryNodeIDs)
		h.syncTunnelForwardsEntryPorts(id, newEntryNodeIDs)
	}

	if typeVal == 2 {
		applyRuntime := h.applyTunnelRuntime
		if oldTunnel != nil && oldTunnel.Type == 2 {
			applyRuntime = h.applyTunnelRuntimeUpsert
		}
		createdChains, createdServices, applyErr := applyRuntime(runtimeState)
		if applyErr != nil {
			updateProtocol := "tls"
			if len(runtimeState.InNodes) > 0 && strings.TrimSpace(runtimeState.InNodes[0].Protocol) != "" {
				updateProtocol = strings.TrimSpace(runtimeState.InNodes[0].Protocol)
			}
			if oldTunnel == nil || oldTunnel.Type != 2 {
				h.rollbackTunnelRuntime(createdChains, createdServices, id, updateProtocol)
			}
			applyErr = errors.Join(applyErr, h.cleanupFederationRuntimePreserving(id))
			if len(federationReleaseRefs) == 0 && shouldDeferTunnelRuntimeApplyError(applyErr) {
				respondTunnelUpdateOK()
				return
			}
			response.WriteJSON(w, response.ErrDefault(applyErr.Error()))
			return
		}
		newChainRows, _ := h.listChainNodesForTunnel(id)
		h.cleanupObsoleteTunnelRuntime(id, oldChainRows, newChainRows)
	}

	if engine != oldTunnel.Engine {
		// A4-003：切换的数据面硬失败必须让用户可见（与下方引擎未变分支
		// 返回 syncRealmNodes 错误的口径一致），不再恒报成功。
		if err := h.switchTunnelEngine(id, tunnelIsRealm(oldTunnel), oldEntryNodeIDs, newEntryNodeIDs); err != nil {
			response.WriteJSON(w, response.ErrDefault(fmt.Sprintf("引擎已切换，但数据面同步失败: %v", err)))
			return
		}
		respondTunnelUpdateOK()
		return
	}
	if engine == tunnelEngineRealm {
		// realm renders whole nodes: re-render the old and new entries
		// (forward ports, tunnel status).
		if _, err := h.syncRealmNodes(append(append([]int64{}, oldEntryNodeIDs...), newEntryNodeIDs...), nil); err != nil {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
		respondTunnelUpdateOK()
		return
	}

	oldType := 0
	if oldTunnel != nil {
		oldType = oldTunnel.Type
	}
	if tunnelForwardRuntimeNeedsSync(oldType, typeVal, oldEntryNodeIDs, newEntryNodeIDs) {
		forwards, fwdErr := h.listForwardsByTunnel(id)
		if fwdErr != nil {
			respondTunnelUpdateOK()
			return
		}
		// R35-FIX-F01：此前 `_ =` 吞掉 syncForwardServices 的错误——
		// 隧道已落库为多入口/nft 混合态而同步被单入口门拒绝时，回执
		// 仍报成功、DB 与数据面分叉。现在每条失败记日志并进回执
		// warnings（与 relayResyncWarnings 同通道），不静默。
		for i := range forwards {
			if err := h.syncForwardServices(&forwards[i], "UpdateService", true); err != nil {
				log.Printf("tunnel update: forward sync failed tunnel_id=%d forward_id=%d err=%v", id, forwards[i].ID, err)
				relayResyncWarnings = append(relayResyncWarnings, fmt.Sprintf("转发 %s 数据面同步失败：%v", forwards[i].Name, err))
			}
		}
	}

	respondTunnelUpdateOK()
}

func tunnelForwardRuntimeNeedsSync(oldType, newType int, oldEntryNodeIDs, newEntryNodeIDs []int64) bool {
	if oldType != newType {
		return true
	}
	return !sameInt64Set(oldEntryNodeIDs, newEntryNodeIDs)
}

func sameInt64Set(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) == 0 {
		return true
	}
	m := make(map[int64]int, len(a))
	for _, v := range a {
		m[v]++
	}
	for _, v := range b {
		c, ok := m[v]
		if !ok || c <= 0 {
			return false
		}
		if c == 1 {
			delete(m, v)
			continue
		}
		m[v] = c - 1
	}
	return len(m) == 0
}

func pickForwardPortFromRecords(ports []forwardPortRecord) int {
	min := 0
	for _, fp := range ports {
		if fp.Port <= 0 {
			continue
		}
		if min == 0 || fp.Port < min {
			min = fp.Port
		}
	}
	return min
}

func forwardPortNodeIDs(ports []forwardPortRecord) []int64 {
	ids := make([]int64, 0, len(ports))
	for _, fp := range ports {
		if fp.NodeID <= 0 {
			continue
		}
		ids = append(ids, fp.NodeID)
	}
	return uniqueInt64s(ids)
}

func (h *Handler) deleteForwardServicesOnNodeBatch(forward *forwardRecord, nodeID int64) error {
	if h == nil || forward == nil || nodeID <= 0 {
		return errors.New("invalid forward service cleanup context")
	}
	// A4-006：forward 所属（换隧道/迁移前的快照）隧道为 nft 模式时，该
	// 节点上没有 gost 服务可删——旧实现径直 DeleteService 落空，旧 DNAT
	// 与 nft_rule_binding 残留并继续计费。与 S-06 同款：对该节点做一次
	// 声明式 reconcile（plan 按当前 DB 构建，已不含本转发，规则收敛移除）。
	if nftMode, _, modeErr := h.tunnelUsesNftables(forward.TunnelID); modeErr == nil && nftMode {
		return h.reconcileNftablesNodeByRequest(nodeID)
	}
	if tunnel, err := h.getTunnelRecord(forward.TunnelID); err == nil && tunnelIsRealm(tunnel) {
		// The forward left a realm tunnel: re-rendering drops it there.
		// 引擎切换（realm.go switchTunnelEngine）走纯 gost 版
		// deleteForwardServicesOnNode——切换时隧道行已落库为新引擎，
		// 在这里拦截会把 gost 服务漏删、端口被占致新引擎监听失败。
		_, err := h.syncRealmNodes([]int64{nodeID}, nil)
		return err
	}
	bases, err := h.forwardServiceBaseCandidates(forward)
	if err != nil {
		return err
	}
	if len(bases) == 0 {
		return nil
	}

	names := make([]string, 0, len(bases)*3)
	seen := make(map[string]struct{}, len(bases)*3)
	appendName := func(name string) {
		if strings.TrimSpace(name) == "" {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	for _, base := range bases {
		appendName(base + "_tcp")
		appendName(base + "_udp")
		appendName(base)
	}
	if len(names) == 0 {
		return nil
	}

	payload := map[string]interface{}{"services": names}
	_, err = h.sendNodeCommand(nodeID, "DeleteService", payload, false, true)
	if err != nil {
		return err
	}
	// R19-X-03：服务已删后清该转发的 rule 级 climiter（与 controlForwardServices
	// 的 DeleteService 分支同口径）。nft/realm 分支在上方已提前返回，不到这里。
	h.deleteForwardConnLimiterOnNode(forward, nodeID)
	return nil
}

func uniqueInt64s(input []int64) []int64 {
	if len(input) <= 1 {
		return input
	}
	seen := make(map[int64]struct{}, len(input))
	out := make([]int64, 0, len(input))
	for _, v := range input {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func diffInt64s(base, subtract []int64) []int64 {
	if len(base) == 0 {
		return nil
	}
	seen := make(map[int64]struct{}, len(subtract))
	for _, v := range subtract {
		seen[v] = struct{}{}
	}
	out := make([]int64, 0, len(base))
	for _, v := range base {
		if _, ok := seen[v]; ok {
			continue
		}
		out = append(out, v)
	}
	return uniqueInt64s(out)
}

func (h *Handler) cleanupTunnelForwardRuntimesOnRemovedEntryNodes(tunnelID int64, oldEntryNodeIDs, newEntryNodeIDs []int64) {
	if h == nil || h.repo == nil || tunnelID <= 0 {
		return
	}

	removedNodeIDs := diffInt64s(oldEntryNodeIDs, newEntryNodeIDs)
	if len(removedNodeIDs) == 0 {
		return
	}

	forwards, err := h.listForwardsByTunnel(tunnelID)
	if err != nil || len(forwards) == 0 {
		return
	}

	// S-06: 检查是否为 nftables 模式
	nftMode, _, _ := h.tunnelUsesNftables(tunnelID)

	for i := range forwards {
		f := &forwards[i]
		if f == nil {
			continue
		}
		for _, nodeID := range removedNodeIDs {
			if nftMode {
				// S-06: nftables 模式下，对旧节点做 reconcile（声明式同步会移除多余规则）
				// 若节点离线，reconcile 会返回错误，调用方应记录而非静默
				if err := h.reconcileNftablesNodeByRequest(nodeID); err != nil {
					// 记录但不中断：离线节点的清理将在重连后通过对账完成
					// （S-04 的 nodeOfflineError 机制）
					continue
				}
			} else {
				// gost 模式：删除 service（离线错误会向上传播，由 S-04 机制处理）；
				// realm 隧道走 Batch 变体的重渲染清理。
				_ = h.deleteForwardServicesOnNodeBatch(f, nodeID)
			}
		}
	}
}

func (h *Handler) validateForwardPortAvailabilityTx(tx *gorm.DB, node *nodeRecord, port int, currentForwardID int64) error {
	if h == nil || h.repo == nil || tx == nil || node == nil || port <= 0 {
		return nil
	}
	occupied, err := h.repo.HasOtherForwardOnNodePortTx(tx, node.ID, port, currentForwardID)
	if err != nil {
		return err
	}
	if occupied {
		return fmt.Errorf("节点 %s 端口 %d 已被其他转发占用", node.Name, port)
	}
	return nil
}

func (h *Handler) validateTunnelEntryPortConflictsForNewEntriesTx(tx *gorm.DB, tunnelID int64, oldEntryNodeIDs, newEntryNodeIDs []int64) error {
	if h == nil || h.repo == nil || tx == nil || tunnelID <= 0 {
		return nil
	}

	addedNodeIDs := diffInt64s(newEntryNodeIDs, oldEntryNodeIDs)
	if len(addedNodeIDs) == 0 {
		return nil
	}

	forwards, err := h.repo.ListForwardsByTunnelTx(tx, tunnelID)
	if err != nil {
		return err
	}
	if len(forwards) == 0 {
		return nil
	}

	for i := range forwards {
		f := &forwards[i]
		if f == nil {
			continue
		}
		oldPorts, portsErr := h.repo.ListForwardPortsTx(tx, f.ID)
		if portsErr != nil {
			return portsErr
		}
		port := pickForwardPortFromRecords(oldPorts)
		if port <= 0 {
			continue
		}

		for _, nodeID := range addedNodeIDs {
			node, nodeErr := h.repo.GetNodeRecordTx(tx, nodeID)
			if nodeErr != nil {
				return nodeErr
			}

			if err := h.validateForwardPortAvailabilityTx(tx, node, port, f.ID); err != nil {
				return fmt.Errorf("转发 %s 入口端口冲突: %w", f.Name, err)
			}
		}
	}

	return nil
}

func (h *Handler) syncTunnelForwardsEntryPorts(tunnelID int64, entryNodeIDs []int64) {
	if h == nil || h.repo == nil || tunnelID <= 0 {
		return
	}
	entryNodeIDs = uniqueInt64s(entryNodeIDs)
	if len(entryNodeIDs) == 0 {
		return
	}

	forwards, err := h.listForwardsByTunnel(tunnelID)
	if err != nil || len(forwards) == 0 {
		return
	}

	allowInIP := len(entryNodeIDs) == 1
	for i := range forwards {
		f := &forwards[i]
		if f == nil {
			continue
		}
		oldPorts, err := h.listForwardPorts(f.ID)
		if err != nil {
			continue
		}
		referencePort := pickForwardPortFromRecords(oldPorts)
		if referencePort <= 0 {
			continue
		}

		// Build a map of existing node → port/inIP from old records.
		oldPortByNode := make(map[int64]forwardPortRecord)
		for _, fp := range oldPorts {
			if fp.NodeID > 0 {
				oldPortByNode[fp.NodeID] = fp
			}
		}

		entries := make([]forwardPortReplaceEntry, 0, len(entryNodeIDs))
		for _, nid := range entryNodeIDs {
			if existing, ok := oldPortByNode[nid]; ok && existing.Port > 0 {
				// Existing entry node: keep its current port.
				inIP := existing.InIP
				if !allowInIP {
					inIP = ""
				}
				entries = append(entries, forwardPortReplaceEntry{NodeID: nid, Port: existing.Port, InIP: inIP})
				continue
			}

			// New entry node: try to follow the reference port.
			port := h.resolvePortForNewEntryNode(nid, referencePort, f.ID)
			inIP := ""
			if allowInIP {
				// For single-entry tunnels, try to preserve inIP from old records.
				for _, fp := range oldPorts {
					if strings.TrimSpace(fp.InIP) != "" {
						inIP = fp.InIP
						break
					}
				}
			}
			entries = append(entries, forwardPortReplaceEntry{NodeID: nid, Port: port, InIP: inIP})
		}
		// B4/F-133: 此前 `_ =` 吞掉 ReplaceForwardPorts 的错误——新入口
		// 选口在锁外（resolvePortForNewEntryNode 仅建议性预检），与并发
		// 分配撞口时替换静默失败、转发留在旧入口集合上无人知晓。现在
		// ReplaceForwardPorts 已在自身锁内三表复检（F-013），失败必须
		// 留痕：记录 tunnel/forward 以便排查（本函数是提交后同步，无
		// 法再回滚隧道更新，但绝不静默）。
		if err := h.repo.ReplaceForwardPorts(f.ID, entries); err != nil {
			log.Printf("sync tunnel forwards entry ports failed tunnel_id=%d forward_id=%d err=%v", tunnelID, f.ID, err)
		}
	}
}

// resolvePortForNewEntryNode determines the port for a forward on a newly added
// entry node. It tries to reuse referencePort (from existing entries); if that
// port is out of range or already occupied, it picks a random available port
// for this specific node.
func (h *Handler) resolvePortForNewEntryNode(nodeID int64, referencePort int, forwardID int64) int {
	node, err := h.getNodeRecord(nodeID)
	if err != nil {
		return referencePort
	}

	// Check if referencePort is within the node's allowed range.
	if validateLocalNodePort(node, referencePort) == nil &&
		validateRemoteNodePort(node, referencePort) == nil {
		// In range — check availability.
		occupied, occErr := h.repo.HasOtherForwardOnNodePort(nodeID, referencePort, forwardID)
		if occErr == nil && !occupied {
			return referencePort
		}
	}

	// referencePort doesn't work for this node; pick a random one.
	newPort := h.pickRandomPortForNode(nodeID)
	if newPort > 0 {
		return newPort
	}
	// F-074 变体：无随机可用端口时仍提交 referencePort 由下游
	// ReplaceForwardPorts 的锁内权威复检裁决（F-013/F-133 已保证失败
	// 留痕），但回退本身必须显式记日志，不再静默——referencePort 是
	// 该转发既有条目的真实端口（非虚构值），与 pickTunnelPort 此前
	// 编造 10000 的情形不同，故只记日志、不改提交语义。
	log.Printf("resolvePortForNewEntryNode: no random port available node_id=%d forward_id=%d, falling back to reference port %d for authoritative recheck", nodeID, forwardID, referencePort)
	return referencePort
}

// pickRandomPortForNode picks a random available port from a single node's
// port range, excluding ports already occupied by other forwards or chains.
func (h *Handler) pickRandomPortForNode(nodeID int64) int {
	portRange, err := h.repo.GetNodePortRange(nodeID)
	if err != nil {
		return 0
	}
	if portRange == "" {
		portRange = "1000-65535"
	}

	nodePorts, err := parsePorts(portRange)
	if err != nil || len(nodePorts) == 0 {
		return 0
	}

	used, err := h.getUsedPorts(nodeID)
	if err != nil {
		return 0
	}

	var available []int
	for _, p := range nodePorts {
		if !used[p] {
			available = append(available, p)
		}
	}

	if len(available) == 0 {
		return 0
	}

	idx, _ := rand.Int(rand.Reader, big.NewInt(int64(len(available))))
	return available[idx.Int64()]
}

func (h *Handler) tunnelDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	id := idFromBody(r, w)
	if id <= 0 {
		return
	}
	// O3-001：删除前收集受影响的入站 ID（隧道删除会级联删除 NodeInboundRelay）。
	// 参考中转删除时的 h.refreshInboundWhitelist(rel.InboundID) 调用。
	// F-002：收集必须在转发数据面清理之前（清理会删转发行）。
	affectedInboundIDs := h.inboundIDsForTunnel(id)
	// F-002：先逐条清理名下转发的数据面（gost DeleteService / nft
	// pending 台账），清理未完成则中止删隧道，避免孤儿监听脱离计数。
	deletedForwards, err := h.cleanupTunnelForwardsDataPlane(id)
	if err != nil {
		// R5X-004(a)：清理逐条落库、中止时已有转发/relay 实际删除——
		// 失败返回前以删前收集集按现行 DB 补刷一次白名单（幂等），
		// 否则回执称「未删除」而已删转发的拨号地址无限驻留 allow。
		h.refreshInboundWhitelists(affectedInboundIDs)
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	// R2X-010 ①（BC-R2-01）：删除类与 X-P1-1 离线待清理对齐——联邦
	// 清理失败不中止删库，绑定留 pending 由重试环在对端恢复后收敛
	//（与 delete-with-forwards 的既有口径统一）。
	if err := h.cleanupFederationRuntime(id); err != nil {
		log.Printf("tunnel delete: federation cleanup deferred to retry loop tunnel_id=%d err=%v", id, err)
	}
	h.cleanupTunnelRuntime(id)
	if err := h.deleteTunnelByID(id); err != nil {
		// R5X-004(a)：转发已全删、删隧道行失败时同理补刷。
		// R19-X-01：注释自认「转发已全删」却只补刷白名单、不披露——
		// 此处补披露已删转发数与标识（deleted 清单为准），已删项
		// 不回滚。
		deleteErr := fmt.Errorf("隧道删除失败：%w；%s；隧道记录仍保留", err, h.formatTunnelDeleteFailureSummary(id, deletedForwards))
		h.refreshInboundWhitelists(affectedInboundIDs)
		response.WriteJSON(w, response.Err(-2, deleteErr.Error()))
		return
	}
	// O3-001：删除后刷新受影响入站的白名单。
	for _, inboundID := range affectedInboundIDs {
		h.refreshInboundWhitelist(inboundID)
	}
	response.WriteJSON(w, response.OKEmpty())
}

func (h *Handler) tunnelDiagnose(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	id := asInt64FromBodyKey(r, w, "tunnelId")
	if id <= 0 {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), diagnosisRequestTimeout)
	defer cancel()
	result, err := h.diagnoseTunnelRuntime(ctx, id)
	if err != nil {
		if strings.Contains(err.Error(), "不存在") || strings.Contains(err.Error(), "不完整") {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	response.WriteJSON(w, response.OK(result))
}

func (h *Handler) tunnelUpdateOrder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req struct {
		Tunnels []struct {
			ID  int64 `json:"id"`
			Inx int   `json:"inx"`
		} `json:"tunnels"`
	}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	for _, t := range req.Tunnels {
		h.repo.UpdateTunnelOrder(t.ID, t.Inx, time.Now().UnixMilli())
	}
	response.WriteJSON(w, response.OKEmpty())
}

func (h *Handler) tunnelBatchDelete(w http.ResponseWriter, r *http.Request) {
	ids := idsFromBody(r, w)
	if ids == nil {
		return
	}
	// O3-003：批量删除同样收集受影响的入站 ID（与 tunnelDelete 的 O3-001 同逻辑），
	// 删除完成后统一刷新白名单，避免残留已删中转机 IP。
	seenInbound := make(map[int64]struct{})
	for _, id := range ids {
		for _, inboundID := range h.inboundIDsForTunnel(id) {
			seenInbound[inboundID] = struct{}{}
		}
	}
	success := 0
	fail := 0
	failures := make([]batchFailureDetail, 0)
	for _, id := range ids {
		tunnelName, _ := h.repo.GetTunnelName(id)
		if _, err := h.getTunnelRecord(id); err != nil {
			fail++
			failures = appendBatchFailure(failures, id, tunnelName, err)
			continue
		}
		// F-002：与单条删隧道同一口径，先清名下转发的数据面。
		deletedForwards, err := h.cleanupTunnelForwardsDataPlane(id)
		if err != nil {
			fail++
			failures = appendBatchFailure(failures, id, tunnelName, err)
			continue
		}
		// R2X-010 ①：同单删口径，联邦清理失败不中止删库（绑定留
		// pending 由重试环收敛）。
		if err := h.cleanupFederationRuntime(id); err != nil {
			log.Printf("tunnel batch delete: federation cleanup deferred to retry loop tunnel_id=%d err=%v", id, err)
		}
		h.cleanupTunnelRuntime(id)
		if err := h.deleteTunnelByID(id); err != nil {
			// R19-X-01：与单删同型——转发已删、删隧道行失败时失败
			// 明细须披露已删转发数与标识（deleted 清单为准），
			// 已删项不回滚。
			fail++
			failures = appendBatchFailure(failures, id, tunnelName, fmt.Errorf("隧道删除失败：%w；%s；隧道记录仍保留", err, h.formatTunnelDeleteFailureSummary(id, deletedForwards)))
		} else {
			success++
		}
	}
	// O3-003：删除后刷新所有受影响入站的白名单。
	for inboundID := range seenInbound {
		h.refreshInboundWhitelist(inboundID)
	}
	response.WriteJSON(w, response.OK(batchOperationResult{SuccessCount: success, FailCount: fail, Failures: failures}))
}

func (h *Handler) reconstructTunnelState(tunnelID int64) (*tunnelCreateState, error) {
	tunnel, err := h.getTunnelRecord(tunnelID)
	if err != nil {
		return nil, err
	}

	chainRows, err := h.listChainNodesForTunnel(tunnelID)
	if err != nil {
		return nil, err
	}

	ipPreference := h.repo.GetTunnelIPPreference(tunnelID)

	state := &tunnelCreateState{
		TunnelID:     tunnelID,
		Type:         tunnel.Type,
		IPPreference: ipPreference,
		InNodes:      make([]tunnelRuntimeNode, 0),
		ChainHops:    make([][]tunnelRuntimeNode, 0),
		OutNodes:     make([]tunnelRuntimeNode, 0),
		Nodes:        make(map[int64]*nodeRecord),
		NodeIDList:   make([]int64, 0),
	}

	inNodes, chainHops, outNodes := splitChainNodeGroups(chainRows)

	for _, r := range inNodes {
		state.InNodes = append(state.InNodes, tunnelRuntimeNode{
			NodeID:    r.NodeID,
			Protocol:  r.Protocol,
			Strategy:  r.Strategy,
			ChainType: 1,
		})
		state.NodeIDList = append(state.NodeIDList, r.NodeID)
	}

	for _, r := range outNodes {
		state.OutNodes = append(state.OutNodes, tunnelRuntimeNode{
			NodeID:    r.NodeID,
			Protocol:  r.Protocol,
			Strategy:  r.Strategy,
			ChainType: 3,
			Port:      r.Port,
			ConnectIP: r.ConnectIP,
		})
		state.NodeIDList = append(state.NodeIDList, r.NodeID)
	}

	for _, hop := range chainHops {
		stateHop := make([]tunnelRuntimeNode, 0)
		for _, r := range hop {
			stateHop = append(stateHop, tunnelRuntimeNode{
				NodeID:    r.NodeID,
				Protocol:  r.Protocol,
				Strategy:  r.Strategy,
				ChainType: 2,
				Inx:       int(r.Inx),
				Port:      r.Port,
				ConnectIP: r.ConnectIP,
			})
			state.NodeIDList = append(state.NodeIDList, r.NodeID)
		}
		state.ChainHops = append(state.ChainHops, stateHop)
	}

	seen := make(map[int64]struct{})
	for _, id := range state.NodeIDList {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		node, err := h.getNodeRecord(id)
		if err != nil {
			return nil, err
		}
		state.Nodes[id] = node
	}

	return state, nil
}

func (h *Handler) redeployTunnelAndForwards(tunnelID int64) error {
	tunnel, err := h.getTunnelRecord(tunnelID)
	if err != nil {
		return err
	}

	if tunnel.Type == 2 {
		state, err := h.reconstructTunnelState(tunnelID)
		if err != nil {
			return err
		}
		if err := validateTunnelRuntimeBeforeApply(state); err != nil {
			return err
		}
		// R2X-010 ②：重部署是非删除操作，清理失败中止时回滚 pending。
		if err := h.cleanupFederationRuntimePreserving(tunnelID); err != nil {
			return err
		}
		h.cleanupTunnelRuntime(tunnelID)
		federationBindings, federationReleaseRefs, fedErr := h.applyFederationRuntime(state, h.federationLocalDomain())
		if fedErr != nil {
			return errors.Join(fedErr, h.releaseFederationRuntimeRefs(federationReleaseRefs))
		}
		tx := h.repo.BeginTx()
		if tx.Error != nil {
			return errors.Join(tx.Error, h.releaseFederationRuntimeRefs(federationReleaseRefs))
		}
		if replaceErr := h.repo.ReplaceFederationTunnelBindingsTx(tx, tunnelID, federationBindings); replaceErr != nil {
			tx.Rollback()
			return errors.Join(replaceErr, h.releaseFederationRuntimeRefs(federationReleaseRefs))
		}
		if commitErr := tx.Commit().Error; commitErr != nil {
			return errors.Join(commitErr, h.releaseFederationRuntimeRefs(federationReleaseRefs))
		}
		_, _, applyErr := h.applyTunnelRuntime(state)
		if applyErr != nil {
			applyErr = errors.Join(applyErr, h.cleanupFederationRuntimePreserving(tunnelID))
			return applyErr
		}
	}

	forwards, err := h.listForwardsByTunnel(tunnelID)
	if err != nil {
		return err
	}
	for i := range forwards {
		if err := h.syncForwardServices(&forwards[i], "UpdateService", true); err != nil {
			return err
		}
	}

	return nil
}

type batchFailureDetail struct {
	ID     int64  `json:"id"`
	Name   string `json:"name,omitempty"`
	Reason string `json:"reason"`
}

type batchOperationResult struct {
	SuccessCount int                  `json:"successCount"`
	FailCount    int                  `json:"failCount"`
	Failures     []batchFailureDetail `json:"failures,omitempty"`
	// R5-B3 D1-001 R4: 批量删除的逐条结果含“已转 pending”（节点不可达时）。
	PendingCount int                  `json:"pendingCount,omitempty"`
	Pending      []batchFailureDetail `json:"pending,omitempty"`
	// A4-002：批量同步路径的非致命告警（如 realm 限速不生效），与单条
	// create/update 的 data.warnings 同义；仅在非空时出现，向后兼容。
	Warnings []string `json:"warnings,omitempty"`
	// R2X-045：批量删节点打断的跨节点引用清单（与单删的
	// brokenReferrers 同名字段/同口径）；仅删节点批量入口填充。
	BrokenReferrers []string `json:"brokenReferrers,omitempty"`
}

func appendBatchFailure(failures []batchFailureDetail, id int64, name string, err error) []batchFailureDetail {
	reason := normalizeBatchFailureReason(errString(err))
	if reason == "" {
		reason = "未知错误"
	}
	return append(failures, batchFailureDetail{
		ID:     id,
		Name:   strings.TrimSpace(name),
		Reason: reason,
	})
}

func appendBatchFailureReason(failures []batchFailureDetail, id int64, name, reason string) []batchFailureDetail {
	normalized := normalizeBatchFailureReason(reason)
	if normalized == "" {
		normalized = "未知错误"
	}
	return append(failures, batchFailureDetail{
		ID:     id,
		Name:   strings.TrimSpace(name),
		Reason: normalized,
	})
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func normalizeBatchFailureReason(reason string) string {
	trimmed := strings.TrimSpace(reason)
	if trimmed == "" {
		return ""
	}
	if strings.EqualFold(trimmed, errForwardNotFound.Error()) {
		return "转发不存在"
	}
	return trimmed
}

func (h *Handler) tunnelBatchRedeploy(w http.ResponseWriter, r *http.Request) {
	ids := idsFromBody(r, w)
	if ids == nil {
		return
	}
	success := 0
	fail := 0
	failures := make([]batchFailureDetail, 0)
	for _, tunnelID := range ids {
		tunnel, tunnelErr := h.getTunnelRecord(tunnelID)
		tunnelName := ""
		if tunnelErr == nil && tunnel != nil {
			tunnelName, _ = h.repo.GetTunnelName(tunnelID)
		}
		if tunnelErr != nil {
			fail++
			failures = appendBatchFailure(failures, tunnelID, tunnelName, tunnelErr)
			continue
		}
		if nftMode, entryNodeIDs, modeErr := h.tunnelUsesNftables(tunnelID); modeErr != nil {
			fail++
			failures = appendBatchFailure(failures, tunnelID, tunnelName, modeErr)
			continue
		} else if nftMode {
			if len(entryNodeIDs) == 0 {
				fail++
				failures = appendBatchFailureReason(failures, tunnelID, tunnelName, "nftables 转发缺少入口节点")
				continue
			}
			// R35-FIX-F01：遗留/竞态混合态下记失败而不是 reconcile
			// 首节点后报成功（其余入口的服务不会被重推）。
			if err := h.rejectMixedNftForwardState(tunnelID, nftMode, entryNodeIDs); err != nil {
				fail++
				failures = appendBatchFailure(failures, tunnelID, tunnelName, err)
				continue
			}
			if reconcileErr := h.reconcileNftablesNodeByRequest(entryNodeIDs[0]); reconcileErr != nil {
				fail++
				failures = appendBatchFailure(failures, tunnelID, tunnelName, reconcileErr)
				continue
			}
			success++
			continue
		}
		if err := h.redeployTunnelAndForwards(tunnelID); err != nil {
			fail++
			failures = appendBatchFailure(failures, tunnelID, tunnelName, err)
			continue
		}
		success++
	}
	response.WriteJSON(w, response.OK(batchOperationResult{SuccessCount: success, FailCount: fail, Failures: failures}))
}

func (h *Handler) userTunnelAssign(w http.ResponseWriter, r *http.Request) {
	var req map[string]interface{}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	if err := h.upsertUserTunnel(req); err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	response.WriteJSON(w, response.OKEmpty())
}

func (h *Handler) userTunnelBatchAssign(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserID  int64 `json:"userId"`
		Tunnels []struct {
			TunnelID int64  `json:"tunnelId"`
			SpeedID  *int64 `json:"speedId"`
		} `json:"tunnels"`
	}
	if err := decodeJSON(r.Body, &req); err != nil || req.UserID <= 0 {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	// X-P3-22：整批单事务（对齐 SetUserSubscriptions 模式）——任一条目
	// DB 失败整批回滚，不再逐条顺序提交留下半批生效。数据面同步在提交后
	// 逐条执行；同步失败时整批按 upsertUserTunnel 的补偿语义回滚 DB 变更
	//（contract: limiter dispatch 失败须回滚），再尽力把恢复后的配置重下发。
	tx := h.repo.BeginTx()
	if tx == nil || tx.Error != nil {
		response.WriteJSON(w, response.Err(-2, "database unavailable"))
		return
	}
	defer func() { tx.Rollback() }()
	outcomes := make([]*userTunnelUpsertOutcome, 0, len(req.Tunnels))
	for _, t := range req.Tunnels {
		m := map[string]interface{}{"userId": req.UserID, "tunnelId": t.TunnelID}
		if t.SpeedID != nil {
			m["speedId"] = *t.SpeedID
		}
		outcome, err := h.upsertUserTunnelDB(tx, m)
		if err != nil {
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
		outcomes = append(outcomes, outcome)
	}
	if err := tx.Commit().Error; err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	for _, outcome := range outcomes {
		if err := h.syncUserTunnelForwards(outcome.userID, outcome.tunnelID); err != nil {
			var rollbackErrs []string
			for i := len(outcomes) - 1; i >= 0; i-- {
				if rbErr := h.rollbackUserTunnelUpsert(outcomes[i]); rbErr != nil {
					rollbackErrs = append(rollbackErrs, rbErr.Error())
				}
			}
			for _, o := range outcomes {
				_ = h.syncUserTunnelForwards(o.userID, o.tunnelID)
			}
			if len(rollbackErrs) > 0 {
				response.WriteJSON(w, response.Err(-2, fmt.Sprintf("下发失败且回滚失败: %v; %s", err, strings.Join(rollbackErrs, "; "))))
				return
			}
			response.WriteJSON(w, response.Err(-2, fmt.Sprintf("下发失败，已回滚: %v", err)))
			return
		}
	}
	response.WriteJSON(w, response.OKEmpty())
}

func (h *Handler) userTunnelRemove(w http.ResponseWriter, r *http.Request) {
	id := idFromBody(r, w)
	if id <= 0 {
		return
	}
	userID, tunnelID, lookupErr := h.repo.GetUserTunnelUserAndTunnel(id)
	if lookupErr != nil {
		// R2X-048（V-12 口径）：删除不存在的授权行幂等成功——repo 已把
		// 未找到归一为 sql.ErrNoRows；其余 lookup 错误仍报错。
		if errors.Is(lookupErr, sql.ErrNoRows) {
			response.WriteJSON(w, response.OKEmpty())
			return
		}
		response.WriteJSON(w, response.Err(-2, lookupErr.Error()))
		return
	}
	h.cleanupForwardsForUserTunnel(userID, tunnelID)
	if err := h.repo.DeleteUserTunnel(id); err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	response.WriteJSON(w, response.OKEmpty())
}

func (h *Handler) userTunnelUpdate(w http.ResponseWriter, r *http.Request) {
	var req map[string]interface{}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	id := asInt64(req["id"], 0)
	if id <= 0 {
		response.WriteJSON(w, response.ErrDefault("权限ID不能为空"))
		return
	}

	speedID := asAnyToInt64Ptr(req["speedId"])
	speedID, err := h.normalizeSpeedLimitReference(speedID)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}

	userID, tunnelID, utErr := h.repo.GetUserTunnelUserAndTunnel(id)
	if utErr != nil {
		response.WriteJSON(w, response.Err(-2, utErr.Error()))
		return
	}

	_, oldFlow, oldNum, oldExpTime, oldFlowReset, oldSpeedID, oldStatus, oldErr :=
		h.repo.GetExistingUserTunnel(userID, tunnelID)
	if oldErr != nil {
		response.WriteJSON(w, response.Err(-2, oldErr.Error()))
		return
	}
	oldTunnel, oldTunnelErr := h.repo.GetUserTunnelByID(id)
	if oldTunnelErr != nil {
		response.WriteJSON(w, response.Err(-2, oldTunnelErr.Error()))
		return
	}
	if oldTunnel == nil {
		response.WriteJSON(w, response.ErrDefault("隧道权限不存在"))
		return
	}
	// G3-131（BUG-A2-5）：真部分更新，与 upsertUserTunnel 同一约定——
	// 未传的字段保持原值，只有显式传入才覆盖；数值字段用 -1 哨兵区分
	// 「未传」与「传 0」，speedId 显式传 null 表示清除限速引用。
	// 旧实现对未传字段套硬编码默认值（num→0、expTime→+365 天、
	// flowResetTime→1、status→1、flow→parseTrafficLimit 缺省值），
	// 部分更新会静默重置既有配置。
	flow := oldTunnel.Flow
	flowMiB := oldTunnel.FlowMiB
	_, flowSupplied := req["flow"]
	_, flowMiBSupplied := req["flowMiB"]
	if flowSupplied || flowMiBSupplied {
		var flowErr error
		flow, flowMiB, flowErr = parseTrafficLimit(req, 0)
		if flowErr != nil {
			response.WriteJSON(w, response.ErrDefault(flowErr.Error()))
			return
		}
		if !flowMiBSupplied && oldTunnel.Flow == flow {
			flowMiB = oldTunnel.FlowMiB
		}
	}

	num := int(oldNum)
	if v := asInt(req["num"], -1); v >= 0 {
		num = v
	}
	expTime := oldExpTime
	if v := asInt64(req["expTime"], -1); v >= 0 {
		expTime = v
	}
	flowResetTime := oldFlowReset
	if v := asInt64(req["flowResetTime"], -1); v >= 0 {
		flowResetTime = v
	}
	status := oldStatus
	if v := asInt(req["status"], -1); v >= 0 {
		status = v
	}

	effectiveSpeedID := interface{}(oldSpeedID)
	if speedID != nil {
		effectiveSpeedID = nullableInt(speedID)
	} else if _, supplied := req["speedId"]; supplied {
		effectiveSpeedID = nil
	}

	if err := h.repo.UpdateUserTunnel(id,
		flow,
		num,
		expTime,
		flowResetTime,
		effectiveSpeedID,
		status,
		flowMiB,
	); err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}

	if syncErr := h.syncUserTunnelForwards(userID, tunnelID); syncErr != nil {
		rollbackErr := h.repo.UpdateUserTunnel(
			id,
			oldFlow,
			int(oldNum),
			oldExpTime,
			oldFlowReset,
			oldSpeedID,
			oldStatus,
			oldTunnel.FlowMiB,
		)
		if rollbackErr != nil {
			response.WriteJSON(w, response.Err(-2, fmt.Sprintf("下发失败且回滚失败: %v; 回滚错误: %v", syncErr, rollbackErr)))
			return
		}

		// R4X-011：同 upsertUserTunnel 单条路径——回滚成功后按回滚
		// 后 DB 对本条 UT 的转发集尽力重下发，纠正失败点之前已按
		// 新口径下发成功的转发；失败不覆写原错误回执。
		_ = h.syncUserTunnelForwards(userID, tunnelID)
		response.WriteJSON(w, response.Err(-2, fmt.Sprintf("下发失败，已回滚: %v", syncErr)))
		return
	}

	response.WriteJSON(w, response.OKEmpty())
}

func (h *Handler) forwardCreate(w http.ResponseWriter, r *http.Request) {
	var req map[string]interface{}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	userID, roleID, err := userRoleFromRequest(r)
	if err != nil {
		response.WriteJSON(w, response.Err(401, "无效的token或token已过期"))
		return
	}
	// X-P2-17（BC-13）：Idempotency-Key 回放/捕获（机制见 idempotency.go）。
	{
		var finish func()
		var replayed bool
		w, finish, replayed = h.idemBegin(w, r, userID, "forward:create")
		defer finish()
		if replayed {
			return
		}
	}
	tunnelID := asInt64(req["tunnelId"], 0)
	if tunnelID <= 0 {
		response.WriteJSON(w, response.ErrDefault("隧道ID不能为空"))
		return
	}
	if err := h.ensureTunnelPermission(userID, roleID, tunnelID); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	tunnel, err := h.getTunnelRecord(tunnelID)
	if err != nil {
		response.WriteJSON(w, response.ErrDefault("隧道不存在"))
		return
	}
	if tunnel.Status != 1 {
		response.WriteJSON(w, response.ErrDefault("隧道已禁用，无法创建转发"))
		return
	}
	if err := h.ensureUserTunnelForwardAllowed(userID, tunnelID, time.Now().UnixMilli()); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	name := asString(req["name"])
	remoteAddr := asString(req["remoteAddr"])
	if name == "" || remoteAddr == "" {
		response.WriteJSON(w, response.ErrDefault("转发名称和目标地址不能为空"))
		return
	}
	if roleID != 0 && !h.allowLocalRemoteAddr() {
		if err := IsSafeRemoteAddr(remoteAddr); err != nil {
			response.WriteJSON(w, response.Err(403, err.Error()))
			return
		}
	}
	if roleID != 0 {
		if speedIDVal, ok := req["speedId"]; ok && speedIDVal != nil {
			response.WriteJSON(w, response.Err(-1, "普通用户无法设置限速规则"))
			return
		}
	}
	speedID := asAnyToInt64Ptr(req["speedId"])
	speedID, err = h.normalizeSpeedLimitReference(speedID)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if roleID != 0 {
		if ipSpeedIDVal, ok := req["ipSpeedId"]; ok && ipSpeedIDVal != nil {
			response.WriteJSON(w, response.Err(-1, "普通用户无法设置每 IP 限速规则"))
			return
		}
	}
	ipSpeedID := asAnyToInt64Ptr(req["ipSpeedId"])
	ipSpeedID, err = h.normalizeSpeedLimitReference(ipSpeedID)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	port := asInt(req["inPort"], 0)
	if port <= 0 {
		picked, pickErr := h.pickTunnelPort(tunnelID)
		if pickErr != nil {
			response.WriteJSON(w, response.ErrDefault(pickErr.Error()))
			return
		}
		port = picked
	}
	// O2-001：端口范围校验，防止 inPort=99999 等非法值绕过前端直接写入 DB
	if port < 1 || port > 65535 {
		response.WriteJSON(w, response.Err(-1, "端口必须在 1-65535 范围内"))
		return
	}
	entryNodes, _ := h.tunnelEntryNodeIDs(tunnelID)
	isNftTunnel, _, err := h.tunnelUsesNftables(tunnelID)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if isNftTunnel {
		if err := h.validateNftablesForwardRequest(tunnel, remoteAddr, entryNodes); err != nil {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
		// R10X-007（F-001）：落库前先做 DB 侧 SSH 配置存在性校验并
		// 以正本措辞点名缺失。此前缺 node_ssh_config 行时要到落库后
		// 的同步阶段才失败，且回执透传仓储层 sql.ErrNoRows 字面量
		// （「sql: no rows in result set」），调用方无从自救。此处
		// 只查 DB（collectNftablesSSHConfigsTx 无网络调用），SSH
		// 能力校验仍由落库后的同步阶段承担，口径与 tunnelCreate 的
		// validateNftablesTunnelState 前置校验一致。
		if _, err := h.collectNftablesSSHConfigsTx(nil, entryNodes); err != nil {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
	}
	inIp := strings.TrimSpace(asString(req["inIp"]))
	// R30-FIX-F01：intake 剥离含 :port 的 inIp（与 buildTunnelInIP
	// 同口径复用 stripShareHostPort，R25 口径）。节点显式字段含
	// :port 时前端下拉会原样提交该值；不剥离则原值落库，下发层
	// “端口胜出”致 inPort 被静默丢弃、服务监听在嵌入端口上。
	// PORT-CLOSURE F-04：剥离后追加格式校验——双端口值单次剥离
	// 穿透（too many colons），此处直接拒绝并提示；一次收口同时
	// 覆盖 gost/realm/nft 三面。存量双端口 inIp 不改写。
	if inIp, err = stripAndValidateNodeAddress(inIp); err != nil {
		response.WriteJSON(w, response.ErrDefault("监听IP格式无效："+err.Error()))
		return
	}
	if inIp != "" && len(entryNodes) > 1 {
		response.WriteJSON(w, response.ErrDefault("多入口隧道的转发不支持自定义监听IP"))
		return
	}
	if err := h.validateForwardEntryNodes(entryNodes, port, 0); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	// A4-004（BC-26）：realm 不支持的负载策略明确拒绝，不再静默改写。
	if err := validateRealmForwardStrategy(tunnel, remoteAddr, defaultString(asString(req["strategy"]), "fifo")); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	now := time.Now().UnixMilli()
	inx := h.repo.NextIndex("forward")
	userName := h.repo.GetUsernameByID(userID)
	if userName == "" {
		userName = "user"
	}
	maxConn := asInt(req["maxConn"], 0)
	ipMaxConn := asInt(req["ipMaxConn"], 0)
	if ipMaxConn < 0 {
		ipMaxConn = 0
	}
	proxyProtocol := asInt(req["proxyProtocol"], 0)
	proxyProtocolReceive := asInt(req["proxyProtocolReceive"], 0)
	proxyProtocolSend := asInt(req["proxyProtocolSend"], proxyProtocol)

	forwardID, err := h.repo.CreateForwardTx(userID, userName, name, tunnelID, remoteAddr, defaultString(asString(req["strategy"]), "fifo"), now, inx, entryNodes, port, inIp, nullableInt(speedID), maxConn, ipMaxConn, nullableInt(ipSpeedID), proxyProtocol, proxyProtocolReceive, proxyProtocolSend)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	createdForward, err := h.getForwardRecord(forwardID)
	if err != nil {
		// E5-S1（同型补齐，不计关闭数）：与下方 sync 失败分支及孪生
		// createRelayForward 的取行失败分支同形——补一行补偿删除，
		// 避免「创建失败」回执下新建行残留。
		_ = h.deleteForwardByID(forwardID)
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	// A4-002：与 forwardUpdate 同形——同步告警（realm 限速/连接数不生效、
	// gost 绑 IP 回退、节点离线跳过）必须回传前端，原实现丢弃 warnings
	// 恒 OKEmpty，创建路径的告警分支在前端成死代码。
	createWarnings, err := h.syncForwardServicesWithWarnings(createdForward, "UpdateService", true)
	if err != nil {
		_ = h.deleteForwardByID(forwardID)
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	if len(createWarnings) > 0 {
		response.WriteJSON(w, response.OK(map[string]interface{}{"warnings": createWarnings}))
		return
	}
	response.WriteJSON(w, response.OKEmpty())
}

func (h *Handler) forwardUpdate(w http.ResponseWriter, r *http.Request) {
	var req map[string]interface{}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	id := asInt64(req["id"], 0)
	if id <= 0 {
		response.WriteJSON(w, response.ErrDefault("转发ID不能为空"))
		return
	}
	forward, actorUserID, actorRole, err := h.resolveForwardAccess(r, id)
	if err != nil {
		if errors.Is(err, errForwardNotFound) {
			response.WriteJSON(w, response.ErrDefault("转发不存在"))
			return
		}
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	oldPorts, err := h.listForwardPorts(id)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}

	tunnelID := asInt64(req["tunnelId"], forward.TunnelID)
	if tunnelID <= 0 {
		response.WriteJSON(w, response.ErrDefault("隧道ID不能为空"))
		return
	}
	if err := h.ensureTunnelPermission(actorUserID, actorRole, tunnelID); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	tunnel, err := h.getTunnelRecord(tunnelID)
	if err != nil {
		response.WriteJSON(w, response.ErrDefault("隧道不存在"))
		return
	}
	if tunnel.Status != 1 {
		response.WriteJSON(w, response.ErrDefault("隧道已禁用，无法更新转发"))
		return
	}

	name := strings.TrimSpace(asString(req["name"]))
	if name == "" {
		name = forward.Name
	}
	remoteAddr := strings.TrimSpace(asString(req["remoteAddr"]))
	if remoteAddr == "" {
		remoteAddr = forward.RemoteAddr
	}
	isNftTunnel, entryNodes, err := h.tunnelUsesNftables(tunnelID)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if isNftTunnel {
		if err := h.validateNftablesForwardRequest(tunnel, remoteAddr, entryNodes); err != nil {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
	}
	if actorRole != 0 && !h.allowLocalRemoteAddr() {
		if err := IsSafeRemoteAddr(remoteAddr); err != nil {
			response.WriteJSON(w, response.Err(403, err.Error()))
			return
		}
	}
	strategy := strings.TrimSpace(asString(req["strategy"]))
	if strategy == "" {
		strategy = forward.Strategy
	}
	// A4-004（BC-26）：编辑路径同建——合并后的策略/目标对 realm 隧道校验。
	if err := validateRealmForwardStrategy(tunnel, remoteAddr, strategy); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	rawSpeedID, hasSpeedID := req["speedId"]
	requestedSpeedID := asAnyToInt64Ptr(rawSpeedID)
	if actorRole != 0 && hasSpeedID && requestedSpeedID != nil && !sameSpeedLimitSelection(forward.SpeedID, requestedSpeedID) {
		response.WriteJSON(w, response.Err(-1, "普通用户无法修改限速规则"))
		return
	}
	speedID := requestedSpeedID
	speedID, err = h.normalizeSpeedLimitReference(speedID)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	newSpeedID := forward.SpeedID
	if speedID != nil {
		newSpeedID = sql.NullInt64{Int64: *speedID, Valid: true}
	} else if _, ok := req["speedId"]; ok {
		newSpeedID = sql.NullInt64{Valid: false}
	}
	rawIPSpeedID, hasIPSpeedID := req["ipSpeedId"]
	requestedIPSpeedID := asAnyToInt64Ptr(rawIPSpeedID)
	newIPSpeedID := forward.IPSpeedID
	if actorRole != 0 {
		if hasIPSpeedID && !sameSpeedLimitSelection(forward.IPSpeedID, requestedIPSpeedID) {
			response.WriteJSON(w, response.Err(-1, "普通用户无法修改每 IP 限速规则"))
			return
		}
	} else {
		ipSpeedID := requestedIPSpeedID
		ipSpeedID, err = h.normalizeSpeedLimitReference(ipSpeedID)
		if err != nil {
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
		if ipSpeedID != nil {
			newIPSpeedID = sql.NullInt64{Int64: *ipSpeedID, Valid: true}
		} else if hasIPSpeedID {
			newIPSpeedID = sql.NullInt64{Valid: false}
		}
	}

	port := asInt(req["inPort"], 0)
	if port <= 0 {
		minPort := h.repo.GetMinForwardPort(id)
		if minPort.Valid {
			port = int(minPort.Int64)
		}
		if port <= 0 {
			picked, pickErr := h.pickTunnelPort(tunnelID)
			if pickErr != nil {
				response.WriteJSON(w, response.ErrDefault(pickErr.Error()))
				return
			}
			port = picked
		}
	}
	// O2-001：端口范围校验，防止 inPort=99999 等非法值绕过前端直接写入 DB
	if port < 1 || port > 65535 {
		response.WriteJSON(w, response.Err(-1, "端口必须在 1-65535 范围内"))
		return
	}
	hasInIP := false
	inIp := ""
	if rawInIP, ok := req["inIp"]; ok {
		hasInIP = true
		inIp = asString(rawInIP)
		// R30-FIX-F01：与 forwardCreate 同口径，intake 剥离含 :port
		// 的 inIp（含取自 extraIPs 的下拉值）；hasInIP 时原值进
		// replaceForwardPorts，此处一次收敛。
		// PORT-CLOSURE F-04：剥离后追加格式校验，双端口值直接拒绝
		// （与 forwardCreate 同口径；存量双端口 inIp 不改写）。
		var err error
		if inIp, err = stripAndValidateNodeAddress(inIp); err != nil {
			response.WriteJSON(w, response.ErrDefault("监听IP格式无效："+err.Error()))
			return
		}
	}
	fwdEntryNodes, _ := h.tunnelEntryNodeIDs(tunnelID)
	if hasInIP && strings.TrimSpace(inIp) != "" && len(fwdEntryNodes) > 1 {
		response.WriteJSON(w, response.ErrDefault("多入口隧道的转发不支持自定义监听IP"))
		return
	}
	// When switching tunnels, entry nodes / service base may change. We must clean up old
	// listeners on nodes that will be removed, otherwise the old ports keep listening.
	tunnelChanged := tunnelID != forward.TunnelID
	// R3X-002（E3-01）：切隧道走共用漏斗——提交前以旧隧道侧状态收集
	// 该转发的中转入站（与批量换隧道 :4288/:4345 同口径），提交后
	// 刷新并重算 RemoteAddr（见函数尾）。不切隧道不收集、不重算。
	var tunnelSwitchInboundIDs []int64
	if tunnelChanged {
		tunnelSwitchInboundIDs = h.inboundIDsForForwards([]int64{id})
	}
	oldNodeIDs := forwardPortNodeIDs(oldPorts)
	newNodeIDs := uniqueInt64s(fwdEntryNodes)
	var removedNodeIDs []int64
	var keptNodeIDs []int64
	if tunnelChanged {
		removedNodeIDs = diffInt64s(oldNodeIDs, newNodeIDs)
		keptNodeIDs = diffInt64s(oldNodeIDs, removedNodeIDs)
	}
	if err := h.validateForwardEntryNodes(fwdEntryNodes, port, id); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	now := time.Now().UnixMilli()
	maxConn := asInt(req["maxConn"], forward.MaxConn)
	ipMaxConn := asInt(req["ipMaxConn"], forward.IPMaxConn)
	if ipMaxConn < 0 {
		ipMaxConn = 0
	}
	proxyProtocol := asInt(req["proxyProtocol"], forward.ProxyProtocol)
	proxyProtocolReceive := asInt(req["proxyProtocolReceive"], forward.ProxyProtocolReceive)
	proxyProtocolSend := asInt(req["proxyProtocolSend"], forward.ProxyProtocolSend)

	// GATE-F3（发版门项 5 观察定案）：切隧道是「先落库、后下发、失败
	// 补偿回滚」的非原子序列——落库到回滚之间 DB 处于已切换状态，
	// 而 allow 是现行 DB 的纯函数（buildKernelApplyPayload 现算），
	// 窗口内任何一次落地内核下发（漂移轮询、重连对账、其他操作触发
	// 的 applyKernel）都会把未提交口径的 allow 推给 agent；内核
	// config hash 又不涵盖 ports/allow，漂移轮询事后无法自纠。故
	// 失败回滚后须按回滚后 DB 对收集集补刷一次，与成功路径（函数尾
	// 漏斗收尾）对称。未切隧道的回滚不触发（allow 口径未变）。
	rollbackAndRefreshWhitelist := func() {
		h.rollbackForwardMutation(forward, oldPorts)
		if tunnelChanged {
			h.refreshInboundWhitelists(tunnelSwitchInboundIDs)
		}
	}
	if err := h.repo.UpdateForward(id, name, tunnelID, remoteAddr, strategy, now, newSpeedID, maxConn, ipMaxConn, newIPSpeedID, proxyProtocol, proxyProtocolReceive, proxyProtocolSend); err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if hasInIP {
		err = h.replaceForwardPorts(id, tunnelID, port, inIp)
	} else if tunnelID != forward.TunnelID {
		err = h.replaceForwardPorts(id, tunnelID, port, "")
	} else {
		err = h.replaceForwardPortsPreservingInIP(id, tunnelID, port, oldPorts)
	}
	if err != nil {
		rollbackAndRefreshWhitelist()
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	updatedForward, err := h.getForwardRecord(id)
	if err != nil {
		rollbackAndRefreshWhitelist()
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	warnings := make([]string, 0)
	if tunnelChanged && len(keptNodeIDs) > 0 {
		for _, nodeID := range keptNodeIDs {
			if delErr := h.deleteForwardServicesOnNodeBatch(forward, nodeID); delErr != nil {
				nodeLabel := fmt.Sprintf("%d", nodeID)
				if n, nErr := h.getNodeRecord(nodeID); nErr == nil && n != nil && strings.TrimSpace(n.Name) != "" {
					nodeLabel = strings.TrimSpace(n.Name)
				}
				warnings = append(warnings, fmt.Sprintf("节点 %s 清理旧转发监听失败: %v", nodeLabel, delErr))
			}
		}
		time.Sleep(tunnelServiceBindRetryDelay)
	}

	// R20-X-01 ①：把更新前快照 forward 传给同步，让失势的 rule climiter
	// 在服务推送成功后删除。forward 自 resolveForwardAccess 取行后未
	// 被改动，仍是更新前值。
	syncWarnings, err := h.syncForwardServicesWithWarnings(updatedForward, "UpdateService", true, forward)
	if err != nil {
		rollbackAndRefreshWhitelist()
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	warnings = append(warnings, syncWarnings...)

	// Best-effort cleanup for old entry nodes after a successful tunnel switch.
	// Avoid cleaning nodes that are still used by the updated forward.
	if tunnelChanged && len(removedNodeIDs) > 0 {
		for _, nodeID := range removedNodeIDs {
			if delErr := h.deleteForwardServicesOnNodeBatch(forward, nodeID); delErr != nil {
				nodeLabel := fmt.Sprintf("%d", nodeID)
				if n, nErr := h.getNodeRecord(nodeID); nErr == nil && n != nil && strings.TrimSpace(n.Name) != "" {
					nodeLabel = strings.TrimSpace(n.Name)
				}
				warnings = append(warnings, fmt.Sprintf("节点 %s 清理旧隧道残留服务失败: %v", nodeLabel, delErr))
			}
		}
	}
	// R3X-002（E3-01）：切隧道提交后的漏斗收尾——新旧两侧入站的
	// allow 均按现行 DB 重算（旧落地拨号地址收回、新落地加入），且
	// 该转发的 Managed relay 按新隧道口径重算 RemoteAddr（与批量
	// 换隧道的 recomputedRelayRemoteAddr 同一步骤，失败入 warnings
	// 不静默）。普通编辑（未切隧道）不触发。
	if tunnelChanged {
		h.refreshInboundWhitelists(tunnelSwitchInboundIDs)
		warnings = append(warnings, h.resyncManagedRelayRemoteAddrForForward(id)...)
	}
	if len(warnings) > 0 {
		response.WriteJSON(w, response.OK(map[string]interface{}{"warnings": warnings}))
		return
	}
	response.WriteJSON(w, response.OKEmpty())
}

func (h *Handler) forwardDelete(w http.ResponseWriter, r *http.Request) {
	id := idFromBody(r, w)
	if id <= 0 {
		return
	}
	forward, _, _, err := h.resolveForwardAccess(r, id)
	if err != nil {
		if errors.Is(err, errForwardNotFound) {
			// X-P3-28（L-11-3，V-12）：不存在幂等成功。
			response.WriteJSON(w, response.OKEmpty())
			return
		}
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	// X-P2-3：删库前按 forwardID 反查 relay 的 InboundID，删后刷新白名单
	// （R3 批 4 起经共用漏斗 inboundIDsForForwards 收集，见
	// kernel_protect.go 的漏斗说明）。
	affectedInboundIDs := h.inboundIDsForForwards([]int64{id})
	var nftNodeID int64
	if nftMode, entryNodeIDs, modeErr := h.tunnelUsesNftables(forward.TunnelID); modeErr != nil {
		response.WriteJSON(w, response.Err(-2, modeErr.Error()))
		return
	} else if nftMode {
		if len(entryNodeIDs) == 0 {
			response.WriteJSON(w, response.ErrDefault("nftables 转发缺少入口节点"))
			return
		}
		// R35-FIX-F01：遗留/竞态混合态下拒绝删除而不是报成功删库
		// （gost/realm 服务会残留监听）。
		if err := h.rejectMixedNftForwardState(forward.TunnelID, nftMode, entryNodeIDs); err != nil {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
		nftNodeID = entryNodeIDs[0]
	} else if err := h.controlForwardServices(forward, "DeleteService", true); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	if nftNodeID > 0 {
		// R5-B3 D1-001 + D6: 数据面优先。先标记 pending（plan 构建排除该
		// 转发），再 reconcile 使规则下线；成功后删库。reconcile 失败则
		// 留在 pending，由后台 job 退避重试（D7），不删库、不硬失败。
		now := time.Now()
		if err := h.repo.MarkForwardPendingDelete(id, nftNodeID, now); err != nil {
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
		if err := h.reconcileNftablesNodeByRequest(nftNodeID); err != nil {
			response.WriteJSON(w, response.Err(-2, "节点不可达，转发已加入待清理队列，后台将自动重试"))
			return
		}
		if err := h.deleteForwardByID(id); err != nil {
			// D6: DB 写失败但数据面已变更（规则已下线）。记日志，靠下一次
			// reconcile 收敛；方向是“可用”而非“静默计费”，可接受。
			log.Printf("nftables forward delete failed op=db_delete_after_reconcile forward_id=%d err=%v", id, err)
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
		h.refreshInboundWhitelists(affectedInboundIDs)
		response.WriteJSON(w, response.OKEmpty())
		return
	}
	// gost 路径：数据面已先行（controlForwardServices），再删库；检查 DB 错误。
	if err := h.deleteForwardByID(id); err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	h.refreshInboundWhitelists(affectedInboundIDs)
	response.WriteJSON(w, response.OKEmpty())
}

func (h *Handler) forwardForceDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	id := idFromBody(r, w)
	if id <= 0 {
		return
	}
	// R5-B3 D1-001 R5: force-delete 是管理员专属。
	operatorID, roleID, err := userRoleFromRequest(r)
	if err != nil {
		response.WriteJSON(w, response.Err(401, "无效的token或token已过期"))
		return
	}
	if roleID != 0 {
		response.WriteJSON(w, response.Err(403, "无权限"))
		return
	}
	// 绕过 ensureForwardAccessByActor 的 pending 过滤：force-delete 必须能
	// 找到 pending-delete 行。幂等口径见下方 X-P3-28（V-12）。
	forward, err := h.getForwardRecord(id)
	if err != nil {
		if errors.Is(err, errForwardNotFound) {
			// X-P3-28（L-11-3，V-12）：不存在幂等成功。
			response.WriteJSON(w, response.OKEmpty())
			return
		}
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if forward == nil {
		// X-P3-28（L-11-3，V-12）：不存在幂等成功。
		response.WriteJSON(w, response.OKEmpty())
		return
	}

	// X-P2-3：删库前按 forwardID 反查 relay 的 InboundID，删后刷新白名单。
	affectedInboundIDs := h.inboundIDsForForward(id)
	// 审计所需：节点与端口（删库前采集，供残留规则人工清理）。
	residueHint := h.buildForwardResidueHint(forward)
	oldPorts, _ := h.listForwardPorts(id)

	// DeleteForwardCascade 同步清理 forward / forward_port / nft_counter_state /
	// nft_rule_binding / nft_pending_delete（R5-B3 补充 A.3）。
	// R5-B3 D1-001 D7: 先删库（plan 不再含该转发），再 best-effort reconcile；
	// 节点不可达时规则残留，残留信息进审计日志供人工清理。
	if err := h.deleteForwardByID(id); err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	h.refreshInboundWhitelists(affectedInboundIDs)
	isRealm := false
	if tunnel, terr := h.getTunnelRecord(forward.TunnelID); terr == nil && tunnelIsRealm(tunnel) {
		// realm renders from the database: the forward drops out on the
		// next apply, now or when the node reconnects.
		_, _ = h.syncRealmNodes(forwardPortNodeIDs(oldPorts), nil)
		isRealm = true
	}
	nftMode := false
	if mode, entryNodeIDs, modeErr := h.tunnelUsesNftables(forward.TunnelID); modeErr == nil && mode {
		nftMode = true
		for _, nodeID := range entryNodeIDs {
			if err := h.reconcileNftablesNodeByRequest(nodeID); err != nil {
				log.Printf("audit op=force_delete_forward_reconcile_skipped forward_id=%d node_id=%d err=%v", id, nodeID, err)
			}
		}
	}
	if !isRealm && !nftMode {
		// R21-X-03 (F-03): gost 路径此前不发任何节点命令，服务仍监听、
		// limiter 仍被引用。与普通删转发同口径：逐节点 best-effort 下发
		// DeleteService + 按 ID 兜底的 DeleteCLimiters（经
		// deleteForwardServicesOnNodeBatch，与 R19-X-03 / R20-X-01 同漏斗）。
		// 节点离线时只记日志、不硬失败——force-delete 的语义即“强制”，
		// 残留待节点重连后面板清扫 / agent 自愈收敛。realm / nft 分支
		// 保持原样（两分支仍可同跑，本段只在两者皆否时执行）。
		for _, nodeID := range forwardPortNodeIDs(oldPorts) {
			if err := h.deleteForwardServicesOnNodeBatch(forward, nodeID); err != nil {
				log.Printf("audit op=force_delete_forward_cleanup_skipped forward_id=%d node_id=%d err=%v", id, nodeID, err)
			}
		}
	}
	log.Printf("audit op=force_delete_forward operator_id=%d forward_id=%d name=%q user_id=%d %s",
		operatorID, id, forward.Name, forward.UserID, residueHint)
	response.WriteJSON(w, response.OKEmpty())
}

// buildForwardResidueHint 生成 force-delete 审计中的残留规则提示：
// 节点名与端口，供节点不可达时人工清理。
func (h *Handler) buildForwardResidueHint(forward *forwardRecord) string {
	var sb strings.Builder
	if nftMode, entryNodeIDs, modeErr := h.tunnelUsesNftables(forward.TunnelID); modeErr == nil && nftMode && len(entryNodeIDs) > 0 {
		for _, nodeID := range entryNodeIDs {
			nodeLabel := fmt.Sprintf("%d", nodeID)
			if n, nErr := h.getNodeRecord(nodeID); nErr == nil && n != nil && strings.TrimSpace(n.Name) != "" {
				nodeLabel = strings.TrimSpace(n.Name)
			}
			fmt.Fprintf(&sb, "residue_node=%q ", nodeLabel)
		}
	}
	if ports, pErr := h.listForwardPorts(forward.ID); pErr == nil {
		for _, p := range ports {
			fmt.Fprintf(&sb, "residue_port=%d ", p.Port)
		}
	}
	sb.WriteString("residue_note=节点规则可能残留，需人工确认清理")
	return sb.String()
}

func (h *Handler) forwardPause(w http.ResponseWriter, r *http.Request) {
	id := idFromBody(r, w)
	if id <= 0 {
		return
	}
	forward, _, _, err := h.resolveForwardAccess(r, id)
	if err != nil {
		if errors.Is(err, errForwardNotFound) {
			response.WriteJSON(w, response.ErrDefault("转发不存在"))
			return
		}
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	// S-05: nftables 模式下暂停必须真正使规则失效。
	// R5-B3 D1-012 + D6: 数据面优先。按“已暂停”意图生成 plan（不含该转发）
	// 先 reconcile，成功后再写 status=0；失败则 DB 保持 active。
	if nftMode, entryNodeIDs, modeErr := h.tunnelUsesNftables(forward.TunnelID); modeErr != nil {
		response.WriteJSON(w, response.Err(-2, modeErr.Error()))
		return
	} else if nftMode {
		if len(entryNodeIDs) == 0 {
			response.WriteJSON(w, response.ErrDefault("nftables 转发缺少入口节点"))
			return
		}
		// R35-FIX-F01：遗留/竞态混合态下拒绝暂停而不是报成功（gost/
		// realm 服务会继续监听、继续计费）。
		if err := h.rejectMixedNftForwardState(forward.TunnelID, nftMode, entryNodeIDs); err != nil {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
		if err := h.syncNftablesNodeWithOverrides(entryNodeIDs[0], map[int64]bool{id: true}, nil); err != nil {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
		if err := h.repo.UpdateForwardStatus(id, model.ForwardStatusPaused, time.Now().UnixMilli()); err != nil {
			// D6: DB 写失败但数据面已变更（规则已下线）。记日志，靠下一次
			// reconcile 收敛；方向是“可用”而非“静默计费”，可接受。
			log.Printf("nftables forward pause failed op=db_write_after_reconcile forward_id=%d err=%v", id, err)
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
	} else {
		if err := h.controlForwardServices(forward, "PauseService", false); err != nil {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
		if err := h.repo.UpdateForwardStatus(id, model.ForwardStatusPaused, time.Now().UnixMilli()); err != nil {
			log.Printf("forward pause failed op=db_write_after_dplane forward_id=%d err=%v", id, err)
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
	}
	response.WriteJSON(w, response.OKEmpty())
}

func (h *Handler) forwardResetFlow(w http.ResponseWriter, r *http.Request) {
	id := idFromBody(r, w)
	if id <= 0 {
		return
	}
	if _, _, _, err := h.resolveForwardAccess(r, id); err != nil {
		if errors.Is(err, errForwardNotFound) {
			response.WriteJSON(w, response.ErrDefault("转发不存在"))
			return
		}
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if err := h.repo.ResetForwardFlow(id, time.Now().UnixMilli()); err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	response.WriteJSON(w, response.OKEmpty())
}

func (h *Handler) forwardResume(w http.ResponseWriter, r *http.Request) {
	id := idFromBody(r, w)
	if id <= 0 {
		return
	}
	forward, _, _, err := h.resolveForwardAccess(r, id)
	if err != nil {
		if errors.Is(err, errForwardNotFound) {
			response.WriteJSON(w, response.ErrDefault("转发不存在"))
			return
		}
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	now := time.Now().UnixMilli()
	if err := h.ensureUserTunnelForwardAllowed(forward.UserID, forward.TunnelID, now); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	// S-05: nftables 模式下恢复必须真正加回规则。
	// R5-B3 D1-012 + D6: 数据面优先。按“已恢复”意图生成 plan（含该转发）
	// 先 reconcile，成功后再写 status=1；失败则 DB 保持 paused，无黑洞。
	if nftMode, entryNodeIDs, modeErr := h.tunnelUsesNftables(forward.TunnelID); modeErr != nil {
		response.WriteJSON(w, response.Err(-2, modeErr.Error()))
		return
	} else if nftMode {
		if len(entryNodeIDs) == 0 {
			response.WriteJSON(w, response.ErrDefault("nftables 转发缺少入口节点"))
			return
		}
		// R35-FIX-F01：遗留/竞态混合态下拒绝恢复而不是报成功（nft
		// 规则与 gost/realm 服务会双份下发）。
		if err := h.rejectMixedNftForwardState(forward.TunnelID, nftMode, entryNodeIDs); err != nil {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
		if err := h.syncNftablesNodeWithOverrides(entryNodeIDs[0], nil, []*forwardRecord{forward}); err != nil {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
		if err := h.ActivateForwardIfNotPending(id, now); err != nil {
			log.Printf("nftables forward resume failed op=db_write_after_reconcile forward_id=%d err=%v", id, err)
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
	} else {
		if err := h.controlForwardServices(forward, "ResumeService", false); err != nil {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
		if err := h.ActivateForwardIfNotPending(id, now); err != nil {
			log.Printf("forward resume failed op=db_write_after_dplane forward_id=%d err=%v", id, err)
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
	}
	response.WriteJSON(w, response.OKEmpty())
}

func (h *Handler) forwardDiagnose(w http.ResponseWriter, r *http.Request) {
	id := asInt64FromBodyKey(r, w, "forwardId")
	if id <= 0 {
		return
	}
	forward, _, _, err := h.resolveForwardAccess(r, id)
	if err != nil {
		if errors.Is(err, errForwardNotFound) {
			response.WriteJSON(w, response.ErrDefault("转发不存在"))
			return
		}
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), diagnosisRequestTimeout)
	defer cancel()
	payload, err := h.diagnoseForwardRuntime(ctx, forward)
	if err != nil {
		if strings.Contains(err.Error(), "不存在") || strings.Contains(err.Error(), "不能为空") || strings.Contains(err.Error(), "错误") {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	response.WriteJSON(w, response.OK(payload))
}

func (h *Handler) forwardUpdateOrder(w http.ResponseWriter, r *http.Request) {
	// R5-B2 A-002: 补归属检查。此前无任何鉴权，用户 A 可改用户 B 的转发排序。
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	actorUserID, actorRole, err := userRoleFromRequest(r)
	if err != nil {
		response.WriteJSON(w, response.Err(401, "无效的token或token已过期"))
		return
	}
	var req struct {
		Forwards []struct {
			ID  int64 `json:"id"`
			Inx int   `json:"inx"`
		} `json:"forwards"`
	}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	for _, f := range req.Forwards {
		if _, accessErr := h.ensureForwardAccessByActor(actorUserID, actorRole, f.ID); accessErr != nil {
			if errors.Is(accessErr, errForwardNotFound) {
				response.WriteJSON(w, response.ErrDefault("转发不存在"))
			} else {
				response.WriteJSON(w, response.Err(-2, accessErr.Error()))
			}
			return
		}
		h.repo.UpdateForwardOrder(f.ID, f.Inx, time.Now().UnixMilli())
	}
	response.WriteJSON(w, response.OKEmpty())
}

// nftDeleteOutcome 是按节点聚合的 nft 删除结果。
type nftDeleteOutcome int

const (
	nftDeleteOK nftDeleteOutcome = iota
	nftDeletePending
	nftDeleteFailed
)

// reconcileGroupedNftDeletes 按节点聚合执行 nft 删除：每节点只 reconcile 一次
// （R5-B3 D1-001）。调用方须已将 forward 标记为 pending。reconcile 成功则删库，
// 失败则整组留在 pending 由后台 job 退避重试。返回每条 forward 的结果。
func (h *Handler) reconcileGroupedNftDeletes(groups map[int64][]int64) map[int64]nftDeleteOutcome {
	outcomes := make(map[int64]nftDeleteOutcome, len(groups))
	for nodeID, groupIDs := range groups {
		if err := h.reconcileNftablesNodeByRequest(nodeID); err != nil {
			for _, id := range groupIDs {
				outcomes[id] = nftDeletePending
			}
			continue
		}
		for _, id := range groupIDs {
			if err := h.deleteForwardByID(id); err != nil {
				log.Printf("nftables forward delete failed op=db_delete_after_reconcile forward_id=%d err=%v", id, err)
				outcomes[id] = nftDeleteFailed
				continue
			}
			outcomes[id] = nftDeleteOK
		}
	}
	return outcomes
}

func (h *Handler) forwardBatchDelete(w http.ResponseWriter, r *http.Request) {
	ids := idsFromBody(r, w)
	if ids == nil {
		return
	}
	actorUserID, actorRole, err := userRoleFromRequest(r)
	if err != nil {
		response.WriteJSON(w, response.Err(401, "无效的token或token已过期"))
		return
	}
	s := 0
	f := 0
	failures := make([]batchFailureDetail, 0)
	pending := make([]batchFailureDetail, 0)
	// X-P2-3：删库前按 forwardID 反查 relay 的 InboundID（并集），删后刷新
	// 白名单。刷新按当前 DB 重算：仅已删转发掉出 allow，pending/失败项
	// 的行仍在库、口径不变。
	var affectedInboundIDs []int64
	for _, id := range ids {
		affectedInboundIDs = append(affectedInboundIDs, h.inboundIDsForForward(id)...)
	}
	// R5-B3 D1-001: nft 删除按节点聚合，一节点只 reconcile 一次。
	nftGroups := make(map[int64][]int64)
	nftNames := make(map[int64]string)
	for _, id := range ids {
		forward, accessErr := h.ensureForwardAccessByActor(actorUserID, actorRole, id)
		if accessErr != nil {
			f++
			failures = appendBatchFailure(failures, id, "", accessErr)
			continue
		}
		if nftMode, entryNodeIDs, modeErr := h.tunnelUsesNftables(forward.TunnelID); modeErr != nil {
			f++
			failures = appendBatchFailure(failures, id, forward.Name, modeErr)
			continue
		} else if nftMode {
			if len(entryNodeIDs) == 0 {
				f++
				failures = appendBatchFailureReason(failures, id, forward.Name, "nftables 转发缺少入口节点")
				continue
			}
			// R35-FIX-F01：遗留/竞态混合态下记失败而不是先标记 pending
			// 再报成功（gost/realm 服务会残留监听）。
			if err := h.rejectMixedNftForwardState(forward.TunnelID, nftMode, entryNodeIDs); err != nil {
				f++
				failures = appendBatchFailure(failures, id, forward.Name, err)
				continue
			}
			// D6: 先标记 pending，再按节点聚合 reconcile。
			if err := h.repo.MarkForwardPendingDelete(id, entryNodeIDs[0], time.Now()); err != nil {
				f++
				failures = appendBatchFailure(failures, id, forward.Name, err)
				continue
			}
			nftGroups[entryNodeIDs[0]] = append(nftGroups[entryNodeIDs[0]], id)
			nftNames[id] = forward.Name
		} else if err := h.controlForwardServices(forward, "DeleteService", true); err != nil {
			f++
			failures = appendBatchFailure(failures, id, forward.Name, err)
			continue
		} else if err := h.deleteForwardByID(id); err != nil {
			f++
			failures = appendBatchFailure(failures, id, forward.Name, err)
			continue
		} else {
			s++
		}
	}
	// 每节点一次 reconcile；成功则删库，失败则整组留 pending（R4：成功项不回滚）。
	outcomes := h.reconcileGroupedNftDeletes(nftGroups)
	for _, groupIDs := range nftGroups {
		for _, id := range groupIDs {
			switch outcomes[id] {
			case nftDeleteOK:
				s++
			case nftDeletePending:
				pending = append(pending, batchFailureDetail{
					ID:     id,
					Name:   nftNames[id],
					Reason: "节点不可达，已加入待清理队列，后台将自动重试",
				})
			default:
				f++
				failures = appendBatchFailureReason(failures, id, nftNames[id], "删除数据库记录失败")
			}
		}
	}
	h.refreshInboundWhitelists(affectedInboundIDs)
	response.WriteJSON(w, response.OK(batchOperationResult{
		SuccessCount: s,
		FailCount:    f,
		Failures:     failures,
		PendingCount: len(pending),
		Pending:      pending,
	}))
}

func (h *Handler) forwardBatchPause(w http.ResponseWriter, r *http.Request) {
	h.batchForwardPauseResume(w, r, "pause")
}

func (h *Handler) forwardBatchResume(w http.ResponseWriter, r *http.Request) {
	h.batchForwardPauseResume(w, r, "resume")
}

// batchForwardPauseResume 是批量暂停/恢复的统一实现（R5-B3 D1-012）。
// nft 转发按 D6 数据面优先：按节点聚合，一节点只 reconcile 一次，成功后才写 DB。
// gost 转发保持原语义（先动数据面后写库），仅补齐 DB 错误检查。
func (h *Handler) batchForwardPauseResume(w http.ResponseWriter, r *http.Request, action string) {
	ids := idsFromBody(r, w)
	if ids == nil {
		return
	}
	actorUserID, actorRole, err := userRoleFromRequest(r)
	if err != nil {
		response.WriteJSON(w, response.Err(401, "无效的token或token已过期"))
		return
	}
	targetStatus := model.ForwardStatusPaused
	serviceCmd := "PauseService"
	if action == "resume" {
		targetStatus = model.ForwardStatusActive
		serviceCmd = "ResumeService"
	}
	s := 0
	f := 0
	now := time.Now().UnixMilli()
	failures := make([]batchFailureDetail, 0)
	type nftIntent struct {
		exclude map[int64]bool
		include []*forwardRecord
		names   map[int64]string
	}
	nftGroups := make(map[int64]*nftIntent)
	for _, id := range ids {
		forward, accessErr := h.ensureForwardAccessByActor(actorUserID, actorRole, id)
		if accessErr != nil {
			f++
			failures = appendBatchFailure(failures, id, "", accessErr)
			continue
		}
		if action == "resume" {
			if err := h.ensureUserTunnelForwardAllowed(forward.UserID, forward.TunnelID, now); err != nil {
				f++
				failures = appendBatchFailure(failures, id, forward.Name, err)
				continue
			}
		}
		nftMode, entryNodeIDs, modeErr := h.tunnelUsesNftables(forward.TunnelID)
		if modeErr != nil {
			f++
			failures = appendBatchFailure(failures, id, forward.Name, modeErr)
			continue
		}
		if nftMode {
			if len(entryNodeIDs) == 0 {
				f++
				failures = appendBatchFailureReason(failures, id, forward.Name, "nftables 转发缺少入口节点")
				continue
			}
			// R35-FIX-F01：遗留/竞态混合态下记失败而不是进 nft 分组
			// 后报成功（另一引擎的服务不会被暂停/恢复）。
			if err := h.rejectMixedNftForwardState(forward.TunnelID, nftMode, entryNodeIDs); err != nil {
				f++
				failures = appendBatchFailure(failures, id, forward.Name, err)
				continue
			}
			intent, ok := nftGroups[entryNodeIDs[0]]
			if !ok {
				intent = &nftIntent{exclude: make(map[int64]bool), names: make(map[int64]string)}
				nftGroups[entryNodeIDs[0]] = intent
			}
			if action == "pause" {
				intent.exclude[id] = true
			} else {
				intent.include = append(intent.include, forward)
			}
			intent.names[id] = forward.Name
			continue
		}
		if err := h.controlForwardServices(forward, serviceCmd, false); err != nil {
			f++
			failures = appendBatchFailure(failures, id, forward.Name, err)
			continue
		}
		var statusErr error
		if targetStatus == model.ForwardStatusActive {
			statusErr = h.ActivateForwardIfNotPending(id, now)
		} else {
			statusErr = h.repo.UpdateForwardStatus(id, targetStatus, now)
		}
		if statusErr != nil {
			log.Printf("forward batch %s failed op=db_write_after_dplane forward_id=%d err=%v", action, id, statusErr)
			f++
			failures = appendBatchFailure(failures, id, forward.Name, statusErr)
		} else {
			s++
		}
	}
	// nft：每节点一次 reconcile，成功后写 DB；失败则整组 DB 不动。
	for nodeID, intent := range nftGroups {
		if err := h.syncNftablesNodeWithOverrides(nodeID, intent.exclude, intent.include); err != nil {
			for id, name := range intent.names {
				f++
				failures = appendBatchFailureReason(failures, id, name, err.Error())
			}
			continue
		}
		for id, name := range intent.names {
			var statusErr error
			if targetStatus == model.ForwardStatusActive {
				statusErr = h.ActivateForwardIfNotPending(id, now)
			} else {
				statusErr = h.repo.UpdateForwardStatus(id, targetStatus, now)
			}
			if statusErr != nil {
				log.Printf("nftables forward batch %s failed op=db_write_after_reconcile forward_id=%d err=%v", action, id, statusErr)
				f++
				failures = appendBatchFailure(failures, id, name, statusErr)
				continue
			}
			s++
		}
	}
	response.WriteJSON(w, response.OK(batchOperationResult{SuccessCount: s, FailCount: f, Failures: failures}))
}

func (h *Handler) forwardBatchRedeploy(w http.ResponseWriter, r *http.Request) {
	ids := idsFromBody(r, w)
	if ids == nil {
		return
	}
	actorUserID, actorRole, err := userRoleFromRequest(r)
	if err != nil {
		response.WriteJSON(w, response.Err(401, "无效的token或token已过期"))
		return
	}
	s := 0
	f := 0
	failures := make([]batchFailureDetail, 0)
	batchWarnings := make([]string, 0)
	for _, id := range ids {
		forward, accessErr := h.ensureForwardAccessByActor(actorUserID, actorRole, id)
		if accessErr != nil {
			f++
			failures = appendBatchFailure(failures, id, "", accessErr)
			continue
		}
		if nftMode, entryNodeIDs, modeErr := h.tunnelUsesNftables(forward.TunnelID); modeErr != nil {
			f++
			failures = appendBatchFailure(failures, id, forward.Name, modeErr)
			continue
		} else if nftMode {
			if len(entryNodeIDs) == 0 {
				f++
				failures = appendBatchFailureReason(failures, id, forward.Name, "nftables 转发缺少入口节点")
				continue
			}
			// R35-FIX-F01：遗留/竞态混合态下记失败而不是 reconcile
			// 首节点后报成功（其余入口的服务不会被重推）。
			if err := h.rejectMixedNftForwardState(forward.TunnelID, nftMode, entryNodeIDs); err != nil {
				f++
				failures = appendBatchFailure(failures, id, forward.Name, err)
				continue
			}
			if err := h.reconcileNftablesNodeByRequest(entryNodeIDs[0]); err != nil {
				f++
				failures = appendBatchFailure(failures, id, forward.Name, err)
			} else {
				s++
			}
			continue
		}
		syncWarnings, err := h.syncForwardServicesWithWarnings(forward, "UpdateService", true)
		batchWarnings = append(batchWarnings, syncWarnings...)
		if err != nil {
			f++
			failures = appendBatchFailure(failures, id, forward.Name, err)
		} else {
			s++
		}
	}
	response.WriteJSON(w, response.OK(batchOperationResult{SuccessCount: s, FailCount: f, Failures: failures, Warnings: batchWarnings}))
}

func (h *Handler) forwardBatchChangeTunnel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ForwardIDs     []int64 `json:"forwardIds"`
		TargetTunnelID int64   `json:"targetTunnelId"`
	}
	if err := decodeJSON(r.Body, &req); err != nil || req.TargetTunnelID <= 0 {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	actorUserID, actorRole, err := userRoleFromRequest(r)
	if err != nil {
		response.WriteJSON(w, response.Err(401, "无效的token或token已过期"))
		return
	}
	if err := h.ensureTunnelPermission(actorUserID, actorRole, req.TargetTunnelID); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	targetTunnel, err := h.getTunnelRecord(req.TargetTunnelID)
	if err != nil {
		response.WriteJSON(w, response.ErrDefault("目标隧道不存在"))
		return
	}
	if targetTunnel.Status != 1 {
		response.WriteJSON(w, response.ErrDefault("目标隧道已禁用"))
		return
	}
	success := 0
	fail := 0
	failures := make([]batchFailureDetail, 0)
	batchWarnings := make([]string, 0)
	// R2X-008（E2-01）：逐 forward 在换隧道之前以 inboundIDsForForward
	// 收集受影响入站（换隧道后拨号节点集变化），成功后统一刷新白名单。
	var changedInboundIDs []int64
	// GATE-F3：已落库后下发失败而回滚的条目同样要刷新——回滚前窗口内
	// 的并发下发可能已把未提交口径的 allow 推给 agent（与 forwardUpdate
	// 同型），其收集集单列，循环后按最终 DB 一并补刷。
	var failedInboundIDs []int64
	for _, id := range req.ForwardIDs {
		if id <= 0 {
			continue
		}
		forward, accessErr := h.ensureForwardAccessByActor(actorUserID, actorRole, id)
		if accessErr != nil {
			fail++
			failures = appendBatchFailure(failures, id, "", accessErr)
			continue
		}
		fwdInboundIDs := h.inboundIDsForForward(id)
		if forward.TunnelID == req.TargetTunnelID {
			fail++
			failures = appendBatchFailureReason(failures, id, forward.Name, "规则已在目标隧道中")
			continue
		}
		oldPorts, listPortsErr := h.listForwardPorts(id)
		if listPortsErr != nil {
			fail++
			failures = appendBatchFailure(failures, id, forward.Name, listPortsErr)
			continue
		}
		if len(oldPorts) == 0 {
			fail++
			failures = appendBatchFailureReason(failures, id, forward.Name, "转发入口端口不存在")
			continue
		}
		oldNodeIDs := forwardPortNodeIDs(oldPorts)
		// B4/F-013: 此前先 UpdateForwardTunnel 落库再验口，验不过靠回滚
		// 恢复——写后验的窗口里转发短暂归属错误隧道且回滚本身可失败。
		// 改为先定口、先验口（目标入口节点的范围 + 三表占用，排除自身），
		// 全部通过后才执行换隧道与端口替换。
		port := h.repo.GetMinForwardPort(id)
		p := 0
		if port.Valid {
			p = int(port.Int64)
		}
		if p <= 0 {
			picked, pickErr := h.pickTunnelPort(req.TargetTunnelID)
			if pickErr != nil {
				fail++
				failures = appendBatchFailureReason(failures, id, forward.Name, pickErr.Error())
				continue
			}
			p = picked
		}
		// X-P3-15（L-04-1）：显式 1–65535 校验（与 create/update 的
		// O2-001 同口径）。存量非法端口在目标节点 PortRange 空规格
		// 放行时，旧实现原样写回、下发才失败；现校验环直接拒。
		if p < 1 || p > 65535 {
			fail++
			failures = appendBatchFailureReason(failures, id, forward.Name, "端口必须在 1-65535 范围内")
			continue
		}
		// X-P3-17（L-04-3）评估登记：本批不收敛为「整批单 tx」——
		// 批回执契约是逐条成败（部分成功合法且须逐条列明），且每条的
		// 运行时下发（网络调用）无法入 DB 事务，整批单 tx 只会把全部
		// 条目的端口锁与写事务拖过网络往返。收敛点落在逐条粒度：每条
		// 的 DB 写（换隧道+换端口）已是单 tx（见下方
		// ChangeForwardTunnelAndPorts），校验环即占用预取。
		// 「按用户批量建转发」同评估：仓内无此端点（forwardCreate 为
		// 单条且已经 CreateForwardTx 单 tx+锁内复检；批量分配见
		// userTunnelBatchAssign，X-P3-22 已整批单 tx），无可收敛对象。
		bctEntryNodes, bctErr := h.tunnelEntryNodeIDs(req.TargetTunnelID)
		if bctErr != nil {
			fail++
			failures = appendBatchFailure(failures, id, forward.Name, bctErr)
			continue
		}
		if len(bctEntryNodes) == 0 {
			fail++
			failures = appendBatchFailureReason(failures, id, forward.Name, "目标隧道没有入口节点")
			continue
		}
		newNodeIDs := uniqueInt64s(bctEntryNodes)
		removedNodeIDs := diffInt64s(oldNodeIDs, newNodeIDs)
		keptNodeIDs := diffInt64s(oldNodeIDs, removedNodeIDs)
		portRangeOk := true
		var portRangeErr error
		for _, nid := range bctEntryNodes {
			nd, ndErr := h.getNodeRecord(nid)
			if ndErr != nil {
				portRangeErr = ndErr
				portRangeOk = false
				break
			}
			if validateErr := validateRemoteNodePort(nd, p); validateErr != nil {
				portRangeOk = false
				portRangeErr = validateErr
				break
			}
			if validateErr := validateLocalNodePort(nd, p); validateErr != nil {
				portRangeOk = false
				portRangeErr = validateErr
				break
			}
			if validateErr := h.validateForwardPortAvailability(nd, p, id); validateErr != nil {
				portRangeOk = false
				portRangeErr = validateErr
				break
			}
		}
		if !portRangeOk {
			fail++
			failures = appendBatchFailure(failures, id, forward.Name, portRangeErr)
			continue
		}
		// X-P2-4 ②：托管中转转发按目标隧道出口集重算 RemoteAddr，不再
		// 只改端口沿用旧址；重算失败（如目标隧道出口集违反落地单出口
		// 规则）该条直接失败、不落库。
		newRemoteAddr, remoteAddrChanged, retargetErr := h.recomputedRelayRemoteAddr(forward, targetTunnel)
		if retargetErr != nil {
			fail++
			failures = appendBatchFailure(failures, id, forward.Name, retargetErr)
			continue
		}
		effectiveRemoteAddr := forward.RemoteAddr
		if remoteAddrChanged {
			effectiveRemoteAddr = newRemoteAddr
		}
		// A4-004（BC-26）：换入 realm 隧道同受策略校验（编辑路径之一）。
		if err := validateRealmForwardStrategy(targetTunnel, effectiveRemoteAddr, forward.Strategy); err != nil {
			fail++
			failures = appendBatchFailure(failures, id, forward.Name, err)
			continue
		}
		// X-P3-17（L-04-3）：换隧道+换端口收敛为 repo 单 tx（锁内占用
		// 复检）；失败整条回滚、无中间态，此段不再需要补偿恢复。
		// 补偿（rollbackForwardMutation）仅保留给提交后的取行/下发失败。
		bctEntries := make([]forwardPortReplaceEntry, len(bctEntryNodes))
		for i, nid := range bctEntryNodes {
			bctEntries[i] = forwardPortReplaceEntry{NodeID: nid, Port: p, InIP: ""}
		}
		if err := h.repo.ChangeForwardTunnelAndPorts(id, req.TargetTunnelID, effectiveRemoteAddr, bctEntries, time.Now().UnixMilli()); err != nil {
			fail++
			failures = appendBatchFailure(failures, id, forward.Name, err)
			continue
		}
		updatedForward, fetchErr := h.getForwardRecord(id)
		if fetchErr != nil {
			h.rollbackForwardMutation(forward, oldPorts)
			failedInboundIDs = append(failedInboundIDs, fwdInboundIDs...)
			fail++
			failures = appendBatchFailure(failures, id, forward.Name, fetchErr)
			continue
		}
		if len(keptNodeIDs) > 0 {
			for _, nodeID := range keptNodeIDs {
				_ = h.deleteForwardServicesOnNodeBatch(forward, nodeID)
			}
			time.Sleep(tunnelServiceBindRetryDelay)
		}
		// R20-X-01 ①：同 forwardUpdate，传更新前快照 forward。
		syncWarnings, err := h.syncForwardServicesWithWarnings(updatedForward, "UpdateService", true, forward)
		batchWarnings = append(batchWarnings, syncWarnings...)
		if err != nil {
			h.rollbackForwardMutation(forward, oldPorts)
			failedInboundIDs = append(failedInboundIDs, fwdInboundIDs...)
			fail++
			failures = appendBatchFailure(failures, id, forward.Name, err)
			continue
		}
		if len(removedNodeIDs) > 0 {
			for _, nodeID := range removedNodeIDs {
				_ = h.deleteForwardServicesOnNodeBatch(forward, nodeID)
			}
		}
		changedInboundIDs = append(changedInboundIDs, fwdInboundIDs...)
		success++
	}
	h.refreshInboundWhitelists(changedInboundIDs)
	h.refreshInboundWhitelists(failedInboundIDs)
	response.WriteJSON(w, response.OK(batchOperationResult{SuccessCount: success, FailCount: fail, Failures: failures, Warnings: batchWarnings}))
}

func (h *Handler) speedLimitCreate(w http.ResponseWriter, r *http.Request) {
	var req map[string]interface{}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	// X-P3-27（L-11-2）：与 X-P2-17 同一 Idempotency-Key 机制（创建面）。
	{
		var finish func()
		var replayed bool
		w, finish, replayed = h.idemBegin(w, r, idemUserID(r), "speed-limit:create")
		defer finish()
		if replayed {
			return
		}
	}

	name := asString(req["name"])
	if name == "" {
		response.WriteJSON(w, response.ErrDefault("名称不能为空"))
		return
	}

	speed := asInt(req["speed"], 100)

	now := time.Now().UnixMilli()
	_, err := h.repo.CreateSpeedLimit(name, speed, now, asInt(req["status"], 1))
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}

	response.WriteJSON(w, response.OKEmpty())
}

func (h *Handler) speedLimitUpdate(w http.ResponseWriter, r *http.Request) {
	var req map[string]interface{}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}

	id := asInt64(req["id"], 0)
	if id <= 0 {
		response.WriteJSON(w, response.ErrDefault("限速规则ID不能为空"))
		return
	}

	// R4X-019: presence 语义（对照 groupUpdate 正本）——先读存量行，
	// 缺席的 name/speed/status 回填旧值，不再被硬默认（speed→100、
	// status→1）静默改写；name 在场但为空仍是显式错误。创建面的
	// 100/1 是创建语义，不在此列。
	old, err := h.repo.GetSpeedLimit(id)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if old == nil {
		response.WriteJSON(w, response.ErrDefault("限速规则不存在"))
		return
	}

	name := old.Name
	if _, ok := req["name"]; ok {
		name = asString(req["name"])
	}
	if name == "" {
		response.WriteJSON(w, response.ErrDefault("名称不能为空"))
		return
	}

	speed := asInt(req["speed"], old.Speed)
	status := asInt(req["status"], old.Status)

	// R6X-017：引用集在写 DB 前枚举（与删除面同口径；引用关系不因
	// 本次改值/禁用而变，写后按此集重下发即按新 DB 口径现查现算）。
	affectedForwardIDs := h.speedLimitAffectedForwardIDs(id)

	if err := h.repo.UpdateSpeedLimit(id, name, speed, status, time.Now().UnixMilli()); err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}

	warnings := h.resyncForwardsForSpeedLimit(id, affectedForwardIDs)

	writeSpeedLimitResyncReceipt(w, warnings)
}

func (h *Handler) speedLimitDelete(w http.ResponseWriter, r *http.Request) {
	id := idFromBody(r, w)
	if id <= 0 {
		return
	}

	// R6X-017：删除前先枚举引用集（删后 forward/user_tunnel 行仍以
	// 悬空 id 指向该规则，此集即回退重下发的对象）。
	affectedForwardIDs := h.speedLimitAffectedForwardIDs(id)

	if err := h.repo.DeleteSpeedLimit(id); err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}

	warnings := h.resyncForwardsForSpeedLimit(id, affectedForwardIDs)

	writeSpeedLimitResyncReceipt(w, warnings)
}

// writeSpeedLimitResyncReceipt 以与 forwardUpdate 同构的回执应答
// 限速规则改/删：重下发 warnings 非空时 data={warnings:[...]}、
// 成功语义（code=0）不变；为空时维持原 OKEmpty 形态（无 BC）。
func writeSpeedLimitResyncReceipt(w http.ResponseWriter, warnings []string) {
	if len(warnings) > 0 {
		response.WriteJSON(w, response.OK(map[string]interface{}{"warnings": warnings}))
		return
	}
	response.WriteJSON(w, response.OKEmpty())
}

// speedLimitAffectedForwardIDs 枚举引用某限速规则的转发集（仓库层
// 引用集正本 SpeedLimitAffectedForwardIDs）；枚举失败只记日志、
// 返回空集——规则本体的写操作不因枚举失败而回滚（R6X-017 纪律：
// 重下发是尽力而为，失败记日志不回滚）。
func (h *Handler) speedLimitAffectedForwardIDs(id int64) []int64 {
	ids, err := h.repo.SpeedLimitAffectedForwardIDs(id)
	if err != nil {
		log.Printf("speed limit resync: enumerate referencing forwards failed speed_limit_id=%d err=%v", id, err)
		return nil
	}
	return ids
}

// resyncForwardsForSpeedLimit 按引用集逐条走既有 sync 通道
// （syncForwardServicesWithWarnings，含 gost/realm/nft 分发与暂停态
// 复位）重下发，使节点按当前 DB 口径（新值 / 禁用删除后回退 UT/隧道
// 级）现查现算 limiter。单条失败记日志续跑，不回滚已落库的规则变更。
//
// R7X-004：汇总各条转发的 warnings 回传调用方回执——nftables 隧道面
// 的「限速不生效」披露（R3X-019）此前经丢 warnings 的薄封装
// syncForwardServices 被整体丢弃、两 handler 恒 OKEmpty。相同文案
// 只保留一份（同一事实对一批转发只披露一次，防回执刷屏）；单条
// sync 失败仍只记日志（尽力而为纪律不变），其 warnings 若已产出
// （如 nft 面 reconcile 失败时随 err 返回）仍汇入回执。
func (h *Handler) resyncForwardsForSpeedLimit(speedLimitID int64, forwardIDs []int64) []string {
	var warnings []string
	seen := make(map[string]struct{})
	for _, forwardID := range forwardIDs {
		forward, err := h.getForwardRecord(forwardID)
		if err != nil || forward == nil {
			log.Printf("speed limit resync: load forward failed speed_limit_id=%d forward_id=%d err=%v", speedLimitID, forwardID, err)
			continue
		}
		syncWarnings, err := h.syncForwardServicesWithWarnings(forward, "UpdateService", true)
		for _, warning := range syncWarnings {
			if _, ok := seen[warning]; ok {
				continue
			}
			seen[warning] = struct{}{}
			warnings = append(warnings, warning)
		}
		if err != nil {
			log.Printf("speed limit resync: sync forward failed speed_limit_id=%d forward_id=%d err=%v", speedLimitID, forwardID, err)
		}
	}
	return warnings
}

func (h *Handler) groupTunnelCreate(w http.ResponseWriter, r *http.Request) {
	h.groupCreate(w, r, "tunnel_group")
}

func (h *Handler) groupTunnelUpdate(w http.ResponseWriter, r *http.Request) {
	h.groupUpdate(w, r, "tunnel_group")
}

func (h *Handler) groupTunnelDelete(w http.ResponseWriter, r *http.Request) {
	h.groupDelete(w, r, "tunnel_group")
}

func (h *Handler) groupUserCreate(w http.ResponseWriter, r *http.Request) {
	h.groupCreate(w, r, "user_group")
}

func (h *Handler) groupUserUpdate(w http.ResponseWriter, r *http.Request) {
	h.groupUpdate(w, r, "user_group")
}

func (h *Handler) groupUserDelete(w http.ResponseWriter, r *http.Request) {
	h.groupDelete(w, r, "user_group")
}

func (h *Handler) groupTunnelAssign(w http.ResponseWriter, r *http.Request) {
	var req struct {
		GroupID   int64   `json:"groupId"`
		TunnelIDs []int64 `json:"tunnelIds"`
	}
	if err := decodeJSON(r.Body, &req); err != nil || req.GroupID <= 0 {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	tx := h.repo.BeginTx()
	if tx.Error != nil {
		response.WriteJSON(w, response.Err(-2, tx.Error.Error()))
		return
	}
	defer func() { tx.Rollback() }()
	// X-P1-2②：替换前取旧成员差集，被移出隧道的组建授权在同一事务内撤销。
	previousTunnelIDs, err := h.repo.ListTunnelIDsByTunnelGroupTx(tx, req.GroupID)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if err := h.repo.ReplaceTunnelGroupMembersTx(tx, req.GroupID, req.TunnelIDs, time.Now().UnixMilli()); err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	revokedPairs, revokeErr := h.repo.RevokeGroupGrantsForRemovedTunnelsTx(tx, req.GroupID, previousTunnelIDs, req.TunnelIDs)
	if revokeErr != nil {
		response.WriteJSON(w, response.Err(-2, revokeErr.Error()))
		return
	}
	if err := tx.Commit().Error; err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	for _, pair := range revokedPairs {
		h.cleanupForwardsForUserTunnel(pair.UserID, pair.TunnelID)
	}
	_ = h.syncPermissionsByTunnelGroup(req.GroupID)
	response.WriteJSON(w, response.OKEmpty())
}

func (h *Handler) groupUserAssign(w http.ResponseWriter, r *http.Request) {
	var req struct {
		GroupID int64   `json:"groupId"`
		UserIDs []int64 `json:"userIds"`
	}
	if err := decodeJSON(r.Body, &req); err != nil || req.GroupID <= 0 {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	tx := h.repo.BeginTx()
	if tx.Error != nil {
		response.WriteJSON(w, response.Err(-2, tx.Error.Error()))
		return
	}
	defer func() { tx.Rollback() }()
	previousUserIDs, err := h.repo.ListUserIDsByUserGroupTx(tx, req.GroupID)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if err := h.repo.ReplaceUserGroupMembersTx(tx, req.GroupID, req.UserIDs, time.Now().UnixMilli()); err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	revokedPairs, revokeErr := h.repo.RevokeGroupGrantsForRemovedUsersTx(tx, req.GroupID, previousUserIDs, req.UserIDs)
	if revokeErr != nil {
		response.WriteJSON(w, response.Err(-2, revokeErr.Error()))
		return
	}
	if err := tx.Commit().Error; err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	for _, pair := range revokedPairs {
		h.cleanupForwardsForUserTunnel(pair.UserID, pair.TunnelID)
	}
	_ = h.syncPermissionsByUserGroup(req.GroupID)
	response.WriteJSON(w, response.OKEmpty())
}

func (h *Handler) groupPermissionAssign(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserGroupID   int64 `json:"userGroupId"`
		TunnelGroupID int64 `json:"tunnelGroupId"`
	}
	if err := decodeJSON(r.Body, &req); err != nil || req.UserGroupID <= 0 || req.TunnelGroupID <= 0 {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	// R2X-013: assigning to a disabled group is an explicit error, and
	// R2X-012: both groups must exist — both checked before any write.
	for _, g := range []struct {
		table string
		id    int64
		label string
	}{
		{"user_group", req.UserGroupID, "用户分组"},
		{"tunnel_group", req.TunnelGroupID, "隧道分组"},
	} {
		gStatus, gExists, gErr := h.repo.GetGroupStatus(g.table, g.id)
		if gErr != nil {
			response.WriteJSON(w, response.Err(-2, gErr.Error()))
			return
		}
		if !gExists {
			response.WriteJSON(w, response.ErrDefault(g.label+"不存在"))
			return
		}
		if gStatus != 1 {
			response.WriteJSON(w, response.ErrDefault(g.label+"已停用，无法分配权限"))
			return
		}
	}
	// R2X-036（G-S02 子项，校验/读取前置档）：展开所需的两份成员名单在
	// Insert 之前预读，读失败零落库；Insert 后用预读名单展开，不再重读。
	userIDs, err := h.repo.ListUserIDsByUserGroup(req.UserGroupID)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	tunnelIDs, err := h.repo.ListTunnelIDsByTunnelGroup(req.TunnelGroupID)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if err := h.repo.InsertGroupPermission(req.UserGroupID, req.TunnelGroupID, time.Now().UnixMilli()); err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	// X-P2-16：展开失败不再静默——部分失败在回执逐条列明，全失败报错。
	result, applyErr := h.applyGroupPermissionWithLists(req.UserGroupID, req.TunnelGroupID, userIDs, tunnelIDs)
	if applyErr != nil {
		response.WriteJSON(w, response.Err(-2, applyErr.Error()))
		return
	}
	if len(result.Failures) > 0 {
		if result.Succeeded == 0 {
			response.WriteJSON(w, response.Err(-2, fmt.Sprintf("权限分配失败: %v", result.Failures[0].Error)))
			return
		}
		response.WriteJSON(w, response.OK(map[string]interface{}{
			"message":  "权限分配部分失败",
			"failures": result.Failures,
		}))
		return
	}
	response.WriteJSON(w, response.OK("权限分配成功"))
}

func (h *Handler) groupPermissionRemove(w http.ResponseWriter, r *http.Request) {
	id := idFromBody(r, w)
	if id <= 0 {
		return
	}
	tx := h.repo.BeginTx()
	if tx.Error != nil {
		response.WriteJSON(w, response.Err(-2, tx.Error.Error()))
		return
	}
	defer func() { tx.Rollback() }()

	ug, tg, exists, err := h.repo.GetGroupPermissionPairByIDTx(tx, id)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}

	if err := h.repo.DeleteGroupPermissionByIDTx(tx, id); err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	var revokedPairs []repo.RevokedUserTunnelPair
	if exists {
		var revokeErr error
		revokedPairs, revokeErr = h.repo.RevokeGroupPermissionPairTx(tx, ug, tg)
		if revokeErr != nil {
			response.WriteJSON(w, response.Err(-2, revokeErr.Error()))
			return
		}
	}

	if err := tx.Commit().Error; err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	for _, pair := range revokedPairs {
		h.cleanupForwardsForUserTunnel(pair.UserID, pair.TunnelID)
	}
	response.WriteJSON(w, response.OKEmpty())
}

func (h *Handler) groupCreate(w http.ResponseWriter, r *http.Request, table string) {
	var req map[string]interface{}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	// R2X-004：创建面接入 Idempotency-Key（机制见 idempotency.go）。
	{
		groupScope := "user-group:create"
		if table == "tunnel_group" {
			groupScope = "tunnel-group:create"
		}
		var finish func()
		var replayed bool
		w, finish, replayed = h.idemBegin(w, r, idemUserID(r), groupScope)
		defer finish()
		if replayed {
			return
		}
	}
	name := asString(req["name"])
	if name == "" {
		response.WriteJSON(w, response.ErrDefault("分组名称不能为空"))
		return
	}
	// R4X-012: status 枚举 {0,1}（与 user 族同口径）——非法值直存会造出
	// 既非启用又非停用的「僵尸组」：sync/分配面按 !=1 不展开、撤销谓词
	// 按 ==0 不触发，两头不靠。
	if status := asInt(req["status"], 1); status != 0 && status != 1 {
		response.WriteJSON(w, response.ErrDefault("分组状态只能为 0 或 1"))
		return
	}
	now := time.Now().UnixMilli()
	if err := h.repo.GroupCreate(table, name, asInt(req["status"], 1), now); err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	response.WriteJSON(w, response.OKEmpty())
}

func (h *Handler) groupUpdate(w http.ResponseWriter, r *http.Request, table string) {
	var req map[string]interface{}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	id := asInt64(req["id"], 0)
	if id <= 0 {
		response.WriteJSON(w, response.ErrDefault("分组ID不能为空"))
		return
	}
	// R2X-013（BC-R2-02）：停用开始执法。先在事务内读旧行：结果状态为
	// 停用时在同一事务内按 grant 台账撤销该组组建授权（手工授权不动，
	// 级联候选收集复用删组同款逻辑），提交后清理被撤销授权的转发；
	// 0→1 时走既有 sync 按成员关系补发。
	// R3X-004（R2X-013 重开半）：撤销按「电平」而非「转换沿」触发——
	// 0→0 保存（停用组改名等）只要仍有台账同样收敛撤销，与 sync 侧的
	// applyGroupPermission 收敛互为双触点；无台账时撤销为空操作。
	// R3X-020：presence 语义（对照 userUpdate 正本）——req 缺 name/
	// status key 时以旧行现值填充，不再把缺省 status 当 1 启用停用组、
	// 不再把缺省 name 清空。
	tx := h.repo.BeginTx()
	if tx == nil || tx.Error != nil {
		response.WriteJSON(w, response.Err(-2, "database unavailable"))
		return
	}
	defer func() { tx.Rollback() }()
	oldName, oldStatus, exists, err := h.repo.GetGroupRowTx(tx, table, id)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if !exists {
		response.WriteJSON(w, response.ErrDefault("分组不存在"))
		return
	}
	newName := oldName
	if _, ok := req["name"]; ok {
		newName = asString(req["name"])
	}
	newStatus := oldStatus
	if _, ok := req["status"]; ok {
		newStatus = asInt(req["status"], oldStatus)
	}
	// R4X-012: presence 回填后仍须过 create 同口径校验——显式空名拒绝
	// （缺席已由旧行回填）、status 枚举 {0,1}；校验在落库与撤销之前，
	// 拒绝即整单不生效（存量授权不得被非法更新连带收敛）。
	if newName == "" {
		response.WriteJSON(w, response.ErrDefault("分组名称不能为空"))
		return
	}
	if newStatus != 0 && newStatus != 1 {
		response.WriteJSON(w, response.ErrDefault("分组状态只能为 0 或 1"))
		return
	}
	if err := h.repo.GroupUpdateTx(tx, table, id, newName, newStatus, time.Now().UnixMilli()); err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	var revokedPairs []repo.RevokedUserTunnelPair
	// R4X-012: 撤销谓词与全族对齐为 !=1（原 ==0）——存量非法 status
	// （校验上线前已落库）在此收敛撤销，sync 侧 !=1 收敛兜底同口径。
	if newStatus != 1 {
		var revokeErr error
		revokedPairs, revokeErr = h.repo.RevokeGroupGrantsByGroupTx(tx, table, id)
		if revokeErr != nil {
			response.WriteJSON(w, response.Err(-2, revokeErr.Error()))
			return
		}
	}
	if err := tx.Commit().Error; err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	for _, pair := range revokedPairs {
		h.cleanupForwardsForUserTunnel(pair.UserID, pair.TunnelID)
	}
	if oldStatus == 0 && newStatus == 1 {
		if table == "user_group" {
			_ = h.syncPermissionsByUserGroup(id)
		} else {
			_ = h.syncPermissionsByTunnelGroup(id)
		}
	}
	response.WriteJSON(w, response.OKEmpty())
}

func (h *Handler) groupDelete(w http.ResponseWriter, r *http.Request, table string) {
	id := idFromBody(r, w)
	if id <= 0 {
		return
	}
	// X-P1-2③：删组在删台账前先收集该组全部组建授权并撤销、清理转发，
	// 否则台账删除后残留授权无从追溯。
	revokedPairs, err := h.repo.GroupDeleteCascadeWithRevoke(table, id)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	for _, pair := range revokedPairs {
		h.cleanupForwardsForUserTunnel(pair.UserID, pair.TunnelID)
	}
	response.WriteJSON(w, response.OKEmpty())
}

// groupGrantFailure is one failed entry of a group-permission expansion
// (X-P2-16): the expansion no longer swallows per-entry failures.
type groupGrantFailure struct {
	UserID   int64  `json:"userId"`
	TunnelID int64  `json:"tunnelId"`
	Error    string `json:"error"`
}

type groupApplyResult struct {
	Succeeded int
	Failures  []groupGrantFailure
	// RevokedPairs carries user_tunnels revoked by the R3X-004 disabled-
	// side convergence below, so sync callers can clean their forwards.
	RevokedPairs []repo.RevokedUserTunnelPair
}

func (h *Handler) applyGroupPermission(userGroupID, tunnelGroupID int64) (groupApplyResult, error) {
	var result groupApplyResult
	// R2X-013: expansion skips a pair when either side's group is not
	// enabled (status != 1) — disabled groups never materialize grants,
	// including for pre-existing pairs synced after the fact.
	// R3X-004 (R2X-013 reopened half): skipping is not enough for stock
	// formed before the disable — when either side is disabled, converge
	// that group's ledger instead: revoke every grant recorded under it
	// (manual user_tunnels are untouched, as RevokeGroupGrantsByGroupTx
	// only orphans group-created rows) and report the revoked pairs so
	// the caller can clean up their forwards. This makes sync a real
	// retroactive-revocation touchpoint (BC-R2-02), not just a gate on
	// new grants.
	type groupRef struct {
		table string
		id    int64
	}
	var disabled []groupRef
	for _, g := range []groupRef{
		{"user_group", userGroupID},
		{"tunnel_group", tunnelGroupID},
	} {
		gStatus, gExists, gErr := h.repo.GetGroupStatus(g.table, g.id)
		if gErr != nil {
			return result, gErr
		}
		if !gExists {
			return result, nil
		}
		if gStatus != 1 {
			disabled = append(disabled, g)
		}
	}
	if len(disabled) > 0 {
		for _, g := range disabled {
			pairs, revokeErr := h.repo.RevokeGroupGrantsByGroup(g.table, g.id)
			if revokeErr != nil {
				return result, revokeErr
			}
			result.RevokedPairs = append(result.RevokedPairs, pairs...)
		}
		return result, nil
	}
	userIDs, err := h.repo.ListUserIDsByUserGroup(userGroupID)
	if err != nil {
		return result, err
	}
	tunnelIDs, err := h.repo.ListTunnelIDsByTunnelGroup(tunnelGroupID)
	if err != nil {
		return result, err
	}
	return h.applyGroupPermissionWithLists(userGroupID, tunnelGroupID, userIDs, tunnelIDs)
}

// applyGroupPermissionWithLists expands one group-permission pair from
// caller-supplied member lists (R2X-036: groupPermissionAssign pre-reads
// both lists before inserting the permission row, so a read failure can
// no longer strand a persisted pair with no expansion).
func (h *Handler) applyGroupPermissionWithLists(userGroupID, tunnelGroupID int64, userIDs, tunnelIDs []int64) (groupApplyResult, error) {
	var result groupApplyResult
	for _, uid := range userIDs {
		for _, tid := range tunnelIDs {
			utID, created, err := h.repo.EnsureUserTunnelGrant(uid, tid)
			if err != nil {
				result.Failures = append(result.Failures, groupGrantFailure{UserID: uid, TunnelID: tid, Error: err.Error()})
				continue
			}
			createdByGroup := 0
			if created {
				createdByGroup = 1
			}
			if err := h.repo.InsertGroupPermissionGrant(userGroupID, tunnelGroupID, utID, createdByGroup, time.Now().UnixMilli()); err != nil {
				result.Failures = append(result.Failures, groupGrantFailure{UserID: uid, TunnelID: tid, Error: err.Error()})
				continue
			}
			result.Succeeded++
		}
	}
	return result, nil
}

func (h *Handler) syncPermissionsByUserGroup(userGroupID int64) error {
	pairs, err := h.repo.ListGroupPermissionPairsByUserGroup(userGroupID)
	if err != nil {
		return err
	}
	for _, p := range pairs {
		result, applyErr := h.applyGroupPermission(p[0], p[1])
		if applyErr != nil {
			log.Printf("sync permissions by user group %d failed: %v", userGroupID, applyErr)
			continue
		}
		// R3X-004: pairs revoked by the disabled-side convergence lose
		// their forwards, exactly like the groupUpdate revoke path.
		for _, pair := range result.RevokedPairs {
			h.cleanupForwardsForUserTunnel(pair.UserID, pair.TunnelID)
		}
		for _, f := range result.Failures {
			log.Printf("sync permissions by user group %d: grant failed user=%d tunnel=%d err=%s", userGroupID, f.UserID, f.TunnelID, f.Error)
		}
	}
	return nil
}

func (h *Handler) syncPermissionsByTunnelGroup(tunnelGroupID int64) error {
	pairs, err := h.repo.ListGroupPermissionPairsByTunnelGroup(tunnelGroupID)
	if err != nil {
		return err
	}
	for _, p := range pairs {
		result, applyErr := h.applyGroupPermission(p[0], p[1])
		if applyErr != nil {
			log.Printf("sync permissions by tunnel group %d failed: %v", tunnelGroupID, applyErr)
			continue
		}
		// R3X-004: pairs revoked by the disabled-side convergence lose
		// their forwards, exactly like the groupUpdate revoke path.
		for _, pair := range result.RevokedPairs {
			h.cleanupForwardsForUserTunnel(pair.UserID, pair.TunnelID)
		}
		for _, f := range result.Failures {
			log.Printf("sync permissions by tunnel group %d: grant failed user=%d tunnel=%d err=%s", tunnelGroupID, f.UserID, f.TunnelID, f.Error)
		}
	}
	return nil
}

type tunnelRuntimeNode struct {
	NodeID    int64
	Protocol  string
	Strategy  string
	Inx       int
	ChainType int
	Port      int
	ConnectIP string
}

type tunnelCreateState struct {
	TunnelID     int64
	Type         int
	IPPreference string // "" = auto, "v4" = prefer IPv4, "v6" = prefer IPv6
	InNodes      []tunnelRuntimeNode
	ChainHops    [][]tunnelRuntimeNode
	OutNodes     []tunnelRuntimeNode
	Nodes        map[int64]*nodeRecord
	NodeIDList   []int64
}

func (h *Handler) prepareTunnelCreateState(tx *gorm.DB, req map[string]interface{}, tunnelType int, excludeTunnelID int64) (*tunnelCreateState, error) {
	state := &tunnelCreateState{
		Type:      tunnelType,
		InNodes:   make([]tunnelRuntimeNode, 0),
		ChainHops: make([][]tunnelRuntimeNode, 0),
		OutNodes:  make([]tunnelRuntimeNode, 0),
		Nodes:     make(map[int64]*nodeRecord),
	}
	nodeIDs := make([]int64, 0)

	for _, item := range asMapSlice(req["inNodeId"]) {
		nodeID := asInt64(item["nodeId"], 0)
		if nodeID <= 0 {
			continue
		}
		nodeIDs = append(nodeIDs, nodeID)
		state.InNodes = append(state.InNodes, tunnelRuntimeNode{
			NodeID:    nodeID,
			Protocol:  defaultString(asString(item["protocol"]), "tls"),
			Strategy:  defaultString(asString(item["strategy"]), "round"),
			ChainType: 1,
		})
	}
	if len(state.InNodes) == 0 {
		return nil, errors.New("入口不能为空")
	}

	if tunnelType == 2 {
		outNodesRaw := asMapSlice(req["outNodeId"])
		if len(outNodesRaw) == 0 {
			return nil, errors.New("出口不能为空")
		}

		allocated := map[int64]int{}
		for _, item := range outNodesRaw {
			nodeID := asInt64(item["nodeId"], 0)
			if nodeID <= 0 {
				continue
			}
			nodeIDs = append(nodeIDs, nodeID)
			port := asInt(item["port"], 0)
			if port <= 0 {
				isRemote, remoteErr := h.repo.IsRemoteNodeTx(tx, nodeID)
				if remoteErr != nil {
					return nil, remoteErr
				}
				if !isRemote {
					var err error
					if excludeTunnelID > 0 {
						port, err = h.repo.PickNodePortTx(tx, nodeID, allocated, excludeTunnelID)
					} else {
						port, err = h.repo.PickRandomNodePortTx(tx, nodeID, allocated, excludeTunnelID)
					}
					if err != nil {
						return nil, err
					}
				}
			}
			state.OutNodes = append(state.OutNodes, tunnelRuntimeNode{
				NodeID:    nodeID,
				Protocol:  defaultString(asString(item["protocol"]), "tls"),
				Strategy:  defaultString(asString(item["strategy"]), "round"),
				ChainType: 3,
				Port:      port,
				ConnectIP: asString(item["connectIp"]),
			})
		}
		if len(state.OutNodes) == 0 {
			return nil, errors.New("出口不能为空")
		}

		for hopIdx, hopRaw := range asAnySlice(req["chainNodes"]) {
			hop := make([]tunnelRuntimeNode, 0)
			for _, item := range asMapSlice(hopRaw) {
				nodeID := asInt64(item["nodeId"], 0)
				if nodeID <= 0 {
					continue
				}
				nodeIDs = append(nodeIDs, nodeID)
				port := asInt(item["port"], 0)
				if port <= 0 {
					isRemote, remoteErr := h.repo.IsRemoteNodeTx(tx, nodeID)
					if remoteErr != nil {
						return nil, remoteErr
					}
					if !isRemote {
						var err error
						if excludeTunnelID > 0 {
							port, err = h.repo.PickNodePortTx(tx, nodeID, allocated, excludeTunnelID)
						} else {
							port, err = h.repo.PickRandomNodePortTx(tx, nodeID, allocated, excludeTunnelID)
						}
						if err != nil {
							return nil, err
						}
					}
				}
				hop = append(hop, tunnelRuntimeNode{
					NodeID:    nodeID,
					Protocol:  defaultString(asString(item["protocol"]), "tls"),
					Strategy:  defaultString(asString(item["strategy"]), "round"),
					Inx:       hopIdx + 1,
					ChainType: 2,
					Port:      port,
					ConnectIP: asString(item["connectIp"]),
				})
			}
			if len(hop) > 0 {
				state.ChainHops = append(state.ChainHops, hop)
			}
		}
	}

	// When updating an existing tunnel (excludeTunnelID > 0), build a set of
	// node IDs that already belong to the tunnel so we can tolerate offline
	// nodes that the user is keeping or removing, while still rejecting newly
	// added offline nodes.
	existingNodeIDs := make(map[int64]struct{})
	if excludeTunnelID > 0 {
		var existIDs []int64
		var err error
		existIDs, err = h.repo.ListTunnelChainNodeIDsTx(tx, excludeTunnelID)
		if err != nil {
			return nil, err
		}
		for _, eid := range existIDs {
			existingNodeIDs[eid] = struct{}{}
		}
	}

	seen := make(map[int64]struct{}, len(nodeIDs))
	for _, nodeID := range nodeIDs {
		if _, ok := seen[nodeID]; ok {
			return nil, errors.New("节点重复")
		}
		seen[nodeID] = struct{}{}
		state.NodeIDList = append(state.NodeIDList, nodeID)
		node, err := h.repo.GetNodeRecordTx(tx, nodeID)
		if err != nil {
			if strings.Contains(err.Error(), "不存在") {
				return nil, errors.New("节点不存在")
			}
			return nil, err
		}
		if node == nil {
			return nil, errors.New("节点不存在")
		}
		if node.IsRemote != 1 && node.Status != 1 {
			// For tunnel updates, allow offline nodes that already belong to the
			// tunnel (user may be removing them). Only reject genuinely new offline nodes.
			_, isExisting := existingNodeIDs[nodeID]
			if excludeTunnelID <= 0 || !isExisting {
				return nil, errors.New("部分节点不在线")
			}
		}
		state.Nodes[nodeID] = node
	}

	for _, outNode := range state.OutNodes {
		if err := validateRemoteNodePort(state.Nodes[outNode.NodeID], outNode.Port); err != nil {
			return nil, err
		}
	}
	for _, hop := range state.ChainHops {
		for _, chainNode := range hop {
			if err := validateRemoteNodePort(state.Nodes[chainNode.NodeID], chainNode.Port); err != nil {
				return nil, err
			}
		}
	}

	// B4/F-015: 显式指定出口/链式端口此前只过 validateRemoteNodePort
	// （对本地节点恒 nil）——范围外、超 65535、与转发/入站/其他隧道
	// 链路撞口全部落库，运行时才 bind 失败。本地节点显式口补齐
	// 「有效范围 + 节点规格 + 跨表占用」三检（与转发 inPort 的
	// O2-001 口径一致）；自动分配的口由 Pick* 保证，不重复检。
	validateExplicitPort := func(n tunnelRuntimeNode) error {
		if n.Port <= 0 {
			return nil
		}
		if n.Port > 65535 {
			return fmt.Errorf("端口 %d 超出有效范围 1-65535", n.Port)
		}
		node := state.Nodes[n.NodeID]
		if node == nil || node.IsRemote == 1 {
			return nil
		}
		if err := validateLocalNodePort(node, n.Port); err != nil {
			return err
		}
		occupied, err := h.repo.NodePortOccupiedForTunnelTx(tx, n.NodeID, n.Port, excludeTunnelID)
		if err != nil {
			return err
		}
		if occupied {
			return fmt.Errorf("节点 %s 端口 %d 已被占用", node.Name, n.Port)
		}
		return nil
	}
	for _, outNode := range state.OutNodes {
		if err := validateExplicitPort(outNode); err != nil {
			return nil, err
		}
	}
	for _, hop := range state.ChainHops {
		for _, chainNode := range hop {
			if err := validateExplicitPort(chainNode); err != nil {
				return nil, err
			}
		}
	}

	return state, nil
}

func validateTunnelRuntimeBeforeApply(state *tunnelCreateState) error {
	if state.Type != 2 {
		return nil
	}
	for _, node := range state.Nodes {
		if node.IsRemote == 1 && (strings.TrimSpace(node.RemoteURL) == "" || strings.TrimSpace(node.RemoteToken) == "") {
			return fmt.Errorf("远程节点 %s 缺少共享配置", nodeDisplayName(node))
		}
	}
	groups := append([][]tunnelRuntimeNode{state.InNodes}, state.ChainHops...)
	groups = append(groups, state.OutNodes)
	for i := 0; i+1 < len(groups); i++ {
		for _, source := range groups[i] {
			for _, target := range groups[i+1] {
				node := state.Nodes[target.NodeID]
				if node == nil {
					return errors.New("节点不存在")
				}
				if _, err := selectTunnelDialHost(state.Nodes[source.NodeID], node, state.IPPreference, target.ConnectIP); err != nil {
					return err
				}
				if node.IsRemote != 1 && target.Port <= 0 {
					return errors.New("节点端口不能为空")
				}
			}
		}
	}
	return nil
}

// Exercise known chain write constraints inside the validation transaction.
// Remote ports that have not been reserved yet remain NULL until actual apply.
func (h *Handler) validateTunnelChainReplacementTx(tx *gorm.DB, state *tunnelCreateState) error {
	if err := h.repo.DeleteChainTunnelsByTunnelTx(tx, state.TunnelID); err != nil {
		return err
	}
	groups := append([][]tunnelRuntimeNode{state.InNodes, state.OutNodes}, state.ChainHops...)
	for groupIndex, group := range groups {
		for nodeIndex, node := range group {
			inx := nodeIndex + 1
			if node.ChainType == 2 {
				inx = groupIndex - 1
			}
			port := sql.NullInt64{Int64: int64(node.Port), Valid: node.Port > 0}
			if err := h.repo.CreateChainTunnelTx(tx, state.TunnelID, strconv.Itoa(node.ChainType),
				node.NodeID, port, node.Strategy, inx, node.Protocol, node.ConnectIP); err != nil {
				return err
			}
		}
	}
	return nil
}

func buildTunnelInIP(inNodes []tunnelRuntimeNode, nodes map[int64]*nodeRecord, ipPreference string) string {
	set := make(map[string]struct{})
	ordered := make([]string, 0)
	preferV6 := strings.TrimSpace(ipPreference) == "v6"
	for _, inNode := range inNodes {
		node := nodes[inNode.NodeID]
		if node == nil {
			continue
		}
		v4 := strings.TrimSpace(node.ServerIPv4)
		v6 := strings.TrimSpace(node.ServerIPv6)
		var addrs []string
		// R29-FIX-F01：显式 ServerIPv4/ServerIPv6 同回退分支口径剥离
		// :port（写侧只校验 serverIp，显式字段可原值落库含端口）。
		if preferV6 {
			if v6 != "" {
				addrs = append(addrs, stripShareHostPort(v6))
			}
			if v4 != "" {
				addrs = append(addrs, stripShareHostPort(v4))
			}
		} else {
			if v4 != "" {
				addrs = append(addrs, stripShareHostPort(v4))
			}
			if v6 != "" {
				addrs = append(addrs, stripShareHostPort(v6))
			}
		}
		// R28-FIX-F01（sweep）：ServerIP 回退分支可含 :port；剥离
		// 后进 tunnel.in_ip，否则列表/前端把面板端口当入口端口。
		if len(addrs) == 0 {
			if v := strings.TrimSpace(node.ServerIP); v != "" {
				addrs = append(addrs, stripShareHostPort(v))
			}
		}
		for _, a := range addrs {
			if _, ok := set[a]; !ok {
				set[a] = struct{}{}
				ordered = append(ordered, a)
			}
		}
	}
	return strings.Join(ordered, ",")
}

func validateTunnelConnectIPConstraints(req map[string]interface{}) error {
	// PORT-CLOSURE ConnectIP（顺手收口）：自定义连接 IP 只做格式
	// 校验、不剥离——存储 chain_tunnel.connect_ip 保持原值（R30
	// F-02 口径，消费点 selectTunnelDialHost 已剥离）；双端口值
	// 单次剥离穿透，此处直接拒绝。空值放行（走节点地址 pick 口径）。
	validateConnectIP := func(v string) error {
		if strings.TrimSpace(v) == "" {
			return nil
		}
		return IsValidNodeAddress(strings.TrimSpace(v))
	}
	outNodes := asMapSlice(req["outNodeId"])
	if len(outNodes) > 1 {
		for _, item := range outNodes {
			if strings.TrimSpace(asString(item["connectIp"])) != "" {
				return fmt.Errorf("多出口隧道不支持设置自定义连接IP")
			}
		}
	}
	for _, item := range outNodes {
		if err := validateConnectIP(asString(item["connectIp"])); err != nil {
			return fmt.Errorf("自定义连接IP格式无效：%v", err)
		}
	}

	for hopIdx, hopRaw := range asAnySlice(req["chainNodes"]) {
		hopNodes := asMapSlice(hopRaw)
		if len(hopNodes) <= 1 {
			continue
		}

		for _, item := range hopNodes {
			if strings.TrimSpace(asString(item["connectIp"])) != "" {
				return fmt.Errorf("转发链第%d跳有多个节点时不支持设置自定义连接IP", hopIdx+1)
			}
		}
	}
	for _, hopRaw := range asAnySlice(req["chainNodes"]) {
		for _, item := range asMapSlice(hopRaw) {
			if err := validateConnectIP(asString(item["connectIp"])); err != nil {
				return fmt.Errorf("自定义连接IP格式无效：%v", err)
			}
		}
	}

	return nil
}

func applyTunnelPortsToRequest(req map[string]interface{}, state *tunnelCreateState) {
	if req == nil || state == nil {
		return
	}
	outPorts := make(map[int64]int)
	for _, n := range state.OutNodes {
		outPorts[n.NodeID] = n.Port
	}
	for _, item := range asMapSlice(req["outNodeId"]) {
		nodeID := asInt64(item["nodeId"], 0)
		if port, ok := outPorts[nodeID]; ok && port > 0 {
			item["port"] = port
		}
	}

	chainPorts := make(map[int64]int)
	for _, hop := range state.ChainHops {
		for _, n := range hop {
			chainPorts[n.NodeID] = n.Port
		}
	}
	for _, hopRaw := range asAnySlice(req["chainNodes"]) {
		for _, item := range asMapSlice(hopRaw) {
			nodeID := asInt64(item["nodeId"], 0)
			if port, ok := chainPorts[nodeID]; ok && port > 0 {
				item["port"] = port
			}
		}
	}
}

type federationRuntimeReleaseRef struct {
	RemoteURL     string
	RemoteToken   string
	BindingID     string
	ReservationID string
	ResourceKey   string
}

func federationRuntimeResourceKey(tunnelID int64, nodeID int64, chainType int, hopInx int) string {
	return fmt.Sprintf("tunnel:%d:node:%d:type:%d:hop:%d", tunnelID, nodeID, chainType, hopInx)
}

func remoteShareIDFromConfig(raw string) int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	var cfg map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return 0
	}
	return asInt64(cfg["shareId"], 0)
}

func (h *Handler) federationLocalDomain() string {
	cfg, _ := h.repo.GetConfigByName("panel_domain")
	if cfg == nil {
		return ""
	}
	return strings.TrimSpace(cfg.Value)
}

// ensureRemoteNodeRelayAuth 是消费方凭据自愈（D3-04）：升级前导入
// 的远端节点 RemoteConfig 没有 relay 凭据，缺失时向提供方做一次
// Connect 刷新并合并写回 DB 与内存副本。best-effort：失败保留存量
// 值继续下发——目标服务若已强制鉴权，缺凭据的连接会在提供方被拒
// 并显形为隧道错误，不会静默放行。
func (h *Handler) ensureRemoteNodeRelayAuth(fc *client.FederationClient, node *nodeRecord, localDomain string) {
	if node == nil || node.IsRemote != 1 {
		return
	}
	if _, password := parseRemoteShareRelayAuth(node.RemoteConfig); password != "" {
		return
	}
	remoteURL := strings.TrimSpace(node.RemoteURL)
	remoteToken := strings.TrimSpace(node.RemoteToken)
	if remoteURL == "" || remoteToken == "" {
		return
	}
	info, err := fc.Connect(remoteURL, remoteToken, localDomain)
	if err != nil || info == nil || strings.TrimSpace(info.RelayPassword) == "" {
		return
	}
	// FIX2-B（V5-3）：版本提示记日志、对端版本随凭据自愈一并留痕。
	logFederationVersionWarning(info)
	cfg := map[string]interface{}{}
	if raw := strings.TrimSpace(node.RemoteConfig); raw != "" {
		_ = json.Unmarshal([]byte(raw), &cfg)
	}
	cfg["relayUsername"] = info.RelayUsername
	cfg["relayPassword"] = info.RelayPassword
	if strings.TrimSpace(info.PanelVersion) != "" {
		cfg["panelVersion"] = info.PanelVersion
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return
	}
	node.RemoteConfig = string(data)
	_ = h.repo.UpdateNodeRemoteConfig(node.ID, string(data))
}

func (h *Handler) applyFederationRuntime(state *tunnelCreateState, localDomain string) ([]repo.FederationTunnelBinding, []federationRuntimeReleaseRef, error) {
	bindings := make([]repo.FederationTunnelBinding, 0)
	releaseRefs := make([]federationRuntimeReleaseRef, 0)
	if h == nil || state == nil {
		return bindings, releaseRefs, nil
	}
	fc := client.NewFederationClient()
	now := time.Now().UnixMilli()

	// D3-04: 下发前为所有远端节点自愈 relay 凭据（每个节点至多一次
	// Connect），供下方 middle 目标与本地链构建取用。
	for _, node := range state.Nodes {
		h.ensureRemoteNodeRelayAuth(fc, node, localDomain)
	}

	for outIdx := range state.OutNodes {
		outNode := state.OutNodes[outIdx]
		node := state.Nodes[outNode.NodeID]
		if node == nil || node.IsRemote != 1 {
			continue
		}
		remoteURL := strings.TrimSpace(node.RemoteURL)
		remoteToken := strings.TrimSpace(node.RemoteToken)
		if remoteURL == "" || remoteToken == "" {
			return bindings, releaseRefs, fmt.Errorf("远程节点 %s 缺少共享配置", nodeDisplayName(node))
		}

		resourceKey := federationRuntimeResourceKey(state.TunnelID, outNode.NodeID, 3, 0)
		reserveReq := client.RuntimeReservePortRequest{
			ResourceKey:   resourceKey,
			Protocol:      defaultString(outNode.Protocol, "tls"),
			RequestedPort: outNode.Port,
		}
		reserveRes, err := fc.ReservePort(remoteURL, remoteToken, localDomain, reserveReq)
		if err != nil && reserveReq.RequestedPort > 0 {
			reserveReq.RequestedPort = 0
			reserveRes, err = fc.ReservePort(remoteURL, remoteToken, localDomain, reserveReq)
		}
		if err != nil {
			return bindings, releaseRefs, fmt.Errorf("远程节点 %s 端口分配失败: %w", nodeDisplayName(node), err)
		}

		releaseRefs = append(releaseRefs, federationRuntimeReleaseRef{
			RemoteURL: remoteURL, RemoteToken: remoteToken, BindingID: reserveRes.BindingID,
			ReservationID: reserveRes.ReservationID, ResourceKey: resourceKey,
		})

		state.OutNodes[outIdx].Port = reserveRes.AllocatedPort
		outNode = state.OutNodes[outIdx]

		applyReq := client.RuntimeApplyRoleRequest{
			ReservationID: reserveRes.ReservationID,
			ResourceKey:   resourceKey,
			Role:          "exit",
			Protocol:      defaultString(outNode.Protocol, "tls"),
			Strategy:      defaultString(outNode.Strategy, "round"),
		}
		applyRes, err := fc.ApplyRole(remoteURL, remoteToken, localDomain, applyReq)
		if err != nil {
			return bindings, releaseRefs, fmt.Errorf("远程节点 %s 运行时下发失败: %w", nodeDisplayName(node), err)
		}
		if applyRes.AllocatedPort > 0 {
			state.OutNodes[outIdx].Port = applyRes.AllocatedPort
			outNode = state.OutNodes[outIdx]
		}

		bindings = append(bindings, repo.FederationTunnelBinding{
			TunnelID:        state.TunnelID,
			NodeID:          outNode.NodeID,
			ChainType:       3,
			HopInx:          0,
			RemoteURL:       remoteURL,
			ResourceKey:     resourceKey,
			RemoteBindingID: defaultString(applyRes.BindingID, reserveRes.BindingID),
			AllocatedPort:   outNode.Port,
			Status:          1,
			CreatedTime:     now,
			UpdatedTime:     now,
		})
		releaseRefs[len(releaseRefs)-1].BindingID = defaultString(applyRes.BindingID, reserveRes.BindingID)
	}

	for hopIdx := len(state.ChainHops) - 1; hopIdx >= 0; hopIdx-- {
		for nodeIdx := range state.ChainHops[hopIdx] {
			chainNode := state.ChainHops[hopIdx][nodeIdx]
			node := state.Nodes[chainNode.NodeID]
			if node == nil || node.IsRemote != 1 {
				continue
			}
			remoteURL := strings.TrimSpace(node.RemoteURL)
			remoteToken := strings.TrimSpace(node.RemoteToken)
			if remoteURL == "" || remoteToken == "" {
				return bindings, releaseRefs, fmt.Errorf("远程节点 %s 缺少共享配置", nodeDisplayName(node))
			}

			resourceKey := federationRuntimeResourceKey(state.TunnelID, chainNode.NodeID, 2, hopIdx+1)
			reserveReq := client.RuntimeReservePortRequest{
				ResourceKey:   resourceKey,
				Protocol:      defaultString(chainNode.Protocol, "tls"),
				RequestedPort: chainNode.Port,
			}
			reserveRes, err := fc.ReservePort(remoteURL, remoteToken, localDomain, reserveReq)
			if err != nil && reserveReq.RequestedPort > 0 {
				reserveReq.RequestedPort = 0
				reserveRes, err = fc.ReservePort(remoteURL, remoteToken, localDomain, reserveReq)
			}
			if err != nil {
				return bindings, releaseRefs, fmt.Errorf("远程节点 %s 端口分配失败: %w", nodeDisplayName(node), err)
			}

			releaseRefs = append(releaseRefs, federationRuntimeReleaseRef{
				RemoteURL: remoteURL, RemoteToken: remoteToken, BindingID: reserveRes.BindingID,
				ReservationID: reserveRes.ReservationID, ResourceKey: resourceKey,
			})

			state.ChainHops[hopIdx][nodeIdx].Port = reserveRes.AllocatedPort
			chainNode = state.ChainHops[hopIdx][nodeIdx]

			nextTargets := state.OutNodes
			if hopIdx+1 < len(state.ChainHops) {
				nextTargets = state.ChainHops[hopIdx+1]
			} else {
				nextTargets = h.orderBestExitTargets(state.TunnelID, chainNode.NodeID, nextTargets)
			}
			applyTargets := make([]client.RuntimeTarget, 0, len(nextTargets))
			for _, target := range nextTargets {
				targetNode := state.Nodes[target.NodeID]
				if targetNode == nil {
					return bindings, releaseRefs, errors.New("节点不存在")
				}
				host, hostErr := selectTunnelDialHost(node, targetNode, state.IPPreference, target.ConnectIP)
				if hostErr != nil {
					return bindings, releaseRefs, hostErr
				}
				if target.Port <= 0 {
					return bindings, releaseRefs, errors.New("节点端口不能为空")
				}
				rt := client.RuntimeTarget{
					Host:     host,
					Port:     target.Port,
					Protocol: defaultString(target.Protocol, "tls"),
				}
				// D3-04: 目标是远端节点时附上其 share 凭据，供提供方
				// 为 middle 的 chain connector 写 auth。
				if targetNode.IsRemote == 1 {
					rt.RelayUsername, rt.RelayPassword = parseRemoteShareRelayAuth(targetNode.RemoteConfig)
				}
				applyTargets = append(applyTargets, rt)
			}

			applyReq := client.RuntimeApplyRoleRequest{
				ReservationID: reserveRes.ReservationID,
				ResourceKey:   resourceKey,
				Role:          "middle",
				Protocol:      defaultString(chainNode.Protocol, "tls"),
				Strategy:      runtimeStrategyForTargets(chainNode, nextTargets),
				Targets:       applyTargets,
			}
			applyRes, err := fc.ApplyRole(remoteURL, remoteToken, localDomain, applyReq)
			if err != nil {
				return bindings, releaseRefs, fmt.Errorf("远程节点 %s 运行时下发失败: %w", nodeDisplayName(node), err)
			}
			if applyRes.AllocatedPort > 0 {
				state.ChainHops[hopIdx][nodeIdx].Port = applyRes.AllocatedPort
				chainNode = state.ChainHops[hopIdx][nodeIdx]
			}

			bindings = append(bindings, repo.FederationTunnelBinding{
				TunnelID:        state.TunnelID,
				NodeID:          chainNode.NodeID,
				ChainType:       2,
				HopInx:          hopIdx + 1,
				RemoteURL:       remoteURL,
				ResourceKey:     resourceKey,
				RemoteBindingID: defaultString(applyRes.BindingID, reserveRes.BindingID),
				AllocatedPort:   chainNode.Port,
				Status:          1,
				CreatedTime:     now,
				UpdatedTime:     now,
			})
			releaseRefs[len(releaseRefs)-1].BindingID = defaultString(applyRes.BindingID, reserveRes.BindingID)
		}
	}

	return bindings, releaseRefs, nil
}

// releaseFederationRuntimeRefs is used after the caller has rolled back its
// transaction. Failed releases survive process restarts in a separate queue.
func (h *Handler) releaseFederationRuntimeRefs(refs []federationRuntimeReleaseRef) error {
	if h == nil || len(refs) == 0 {
		return nil
	}
	fc := client.NewFederationClient()
	localDomain := h.federationLocalDomain()
	var failures []error
	for i := len(refs) - 1; i >= 0; i-- {
		ref := refs[i]
		pending := &model.FederationPendingRelease{
			RemoteURL: ref.RemoteURL, RemoteToken: ref.RemoteToken,
			BindingID: ref.BindingID, ReservationID: ref.ReservationID, ResourceKey: ref.ResourceKey,
		}
		if err := h.repo.SavePendingFederationRelease(pending); err != nil {
			failures = append(failures, fmt.Errorf("保存共享运行时清理任务失败: %w", err))
			continue
		}
		req := client.RuntimeReleaseRoleRequest{
			BindingID: ref.BindingID, ReservationID: ref.ReservationID, ResourceKey: ref.ResourceKey,
		}
		if err := fc.ReleaseRole(ref.RemoteURL, ref.RemoteToken, localDomain, req); err != nil {
			failures = append(failures, err)
			continue
		}
		if err := h.repo.DeletePendingFederationRelease(pending.ID); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (h *Handler) cleanupFederationRuntime(tunnelID int64) error {
	if h == nil || tunnelID <= 0 {
		return nil
	}
	bindings, err := h.repo.ListFederationTunnelBindingsForCleanup(tunnelID)
	if err != nil {
		return err
	}
	var failures []error
	for _, b := range bindings {
		// Persist intent before sending the request: timeouts can occur after
		// the peer has already acted, and must remain safely retryable.
		if err := h.repo.MarkFederationTunnelBindingPendingRelease(b.ID); err != nil {
			failures = append(failures, err)
			continue
		}
		if err := h.releaseFederationTunnelBinding(b); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// cleanupFederationRuntimePreserving is cleanupFederationRuntime for
// non-delete operations (tunnelUpdate / redeploy) — R2X-010 ②: it
// snapshots each binding's status on entry, and when the cleanup fails
// (so the caller aborts and the tunnel stays alive) it rolls the
// bindings this round flipped from active (1) to pending (2) back to
// status=1, so the background retry loop cannot later release the
// runtime of a tunnel that was never actually deleted or replaced.
// Bindings already pending on entry keep their status: their pending
// state belongs to an earlier, committed delete intent.
func (h *Handler) cleanupFederationRuntimePreserving(tunnelID int64) error {
	if h == nil || tunnelID <= 0 {
		return nil
	}
	bindings, err := h.repo.ListFederationTunnelBindingsForCleanup(tunnelID)
	if err != nil {
		return err
	}
	original := make(map[int64]int, len(bindings))
	for _, b := range bindings {
		original[b.ID] = b.Status
	}
	if err := h.cleanupFederationRuntime(tunnelID); err != nil {
		for id, status := range original {
			if status != 1 {
				continue
			}
			if rerr := h.repo.SetFederationTunnelBindingStatus(id, 1); rerr != nil {
				log.Printf("federation cleanup rollback failed tunnel_id=%d binding_id=%d err=%v", tunnelID, id, rerr)
			}
		}
		return err
	}
	return nil
}

func (h *Handler) releaseFederationTunnelBinding(b repo.FederationTunnelBinding) error {
	node, err := h.repo.GetNodeByID(b.NodeID)
	if err != nil {
		return err
	}
	if node == nil {
		return fmt.Errorf("共享节点 %d 不存在，保留待清理绑定", b.NodeID)
	}
	remoteURL := strings.TrimSpace(b.RemoteURL)
	if remoteURL == "" {
		remoteURL = strings.TrimSpace(node.RemoteURL.String)
	}
	remoteToken := strings.TrimSpace(node.RemoteToken.String)
	if remoteURL == "" || remoteToken == "" {
		return fmt.Errorf("共享节点 %d 缺少连接配置，保留待清理绑定", b.NodeID)
	}
	req := client.RuntimeReleaseRoleRequest{
		BindingID: strings.TrimSpace(b.RemoteBindingID), ResourceKey: strings.TrimSpace(b.ResourceKey),
	}
	if err := client.NewFederationClient().ReleaseRole(remoteURL, remoteToken, h.federationLocalDomain(), req); err != nil {
		return fmt.Errorf("共享节点 %d 清理失败: %w", b.NodeID, err)
	}
	return h.repo.DeleteFederationTunnelBinding(b.ID)
}

// retryPendingFederationRuntimeCleanup never touches active bindings.
func (h *Handler) retryPendingFederationRuntimeCleanup() error {
	bindings, err := h.repo.ListPendingFederationTunnelBindings()
	if err != nil {
		return err
	}
	var failures []error
	for _, binding := range bindings {
		// R2X-010 ③（纵深防线）：释放前复核绑定所属隧道是否仍存在——
		// 仍存在说明该 pending 不是已提交的删除意图（例如旧口径下失败
		// 的删除/编辑尝试遗留），释放它会拆掉仍在用隧道的数据面，跳过
		// 并记日志。隧道 ID 自增不复用，此判据不会误伤真删除。
		if exists, err := h.repo.TunnelExists(binding.TunnelID); err != nil {
			failures = append(failures, err)
			continue
		} else if exists {
			log.Printf("federation cleanup retry: skip binding_id=%d tunnel_id=%d: tunnel still exists", binding.ID, binding.TunnelID)
			continue
		}
		if err := h.releaseFederationTunnelBinding(binding); err != nil {
			failures = append(failures, err)
		}
	}
	pending, err := h.repo.ListPendingFederationReleases()
	if err != nil {
		return errors.Join(append(failures, err)...)
	}
	fc := client.NewFederationClient()
	for _, item := range pending {
		req := client.RuntimeReleaseRoleRequest{
			BindingID: item.BindingID, ReservationID: item.ReservationID, ResourceKey: item.ResourceKey,
		}
		if err := fc.ReleaseRole(item.RemoteURL, item.RemoteToken, h.federationLocalDomain(), req); err != nil {
			failures = append(failures, err)
			continue
		}
		if err := h.repo.DeletePendingFederationRelease(item.ID); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (h *Handler) applyTunnelRuntime(state *tunnelCreateState) ([]int64, []int64, error) {
	return h.applyTunnelRuntimeWithMode(state, false)
}

func (h *Handler) applyTunnelRuntimeUpsert(state *tunnelCreateState) ([]int64, []int64, error) {
	return h.applyTunnelRuntimeWithMode(state, true)
}

func (h *Handler) applyTunnelRuntimeWithMode(state *tunnelCreateState, upsert bool) ([]int64, []int64, error) {
	if h == nil || state == nil {
		return nil, nil, errors.New("invalid tunnel runtime state")
	}
	createdChains := make([]int64, 0)
	createdServices := make([]int64, 0)
	if state.Type != 2 {
		return createdChains, createdServices, nil
	}

	for _, inNode := range state.InNodes {
		targets := state.OutNodes
		if len(state.ChainHops) > 0 {
			targets = state.ChainHops[0]
		} else {
			targets = h.orderBestExitTargets(state.TunnelID, inNode.NodeID, targets)
		}
		chainData, err := buildTunnelChainConfig(state.TunnelID, inNode.NodeID, targets, state.Nodes, state.IPPreference)
		if err != nil {
			return createdChains, createdServices, err
		}
		if err := h.applyTunnelChainOnNode(inNode.NodeID, chainData, upsert); err != nil {
			if shouldDeferTunnelRuntimeApplyError(err) {
				continue
			}
			return createdChains, createdServices, fmt.Errorf("入口节点 %s 下发转发链失败: %w", nodeDisplayName(state.Nodes[inNode.NodeID]), err)
		}
		createdChains = append(createdChains, inNode.NodeID)
	}

	for i, hop := range state.ChainHops {
		for _, chainNode := range hop {
			nextTargets := state.OutNodes
			if i+1 < len(state.ChainHops) {
				nextTargets = state.ChainHops[i+1]
			} else {
				nextTargets = h.orderBestExitTargets(state.TunnelID, chainNode.NodeID, nextTargets)
			}
			node := state.Nodes[chainNode.NodeID]
			if node != nil && (node.IsRemote == 1 || node.Status != 1) {
				continue
			}
			chainData, err := buildTunnelChainConfig(state.TunnelID, chainNode.NodeID, nextTargets, state.Nodes, state.IPPreference)
			if err != nil {
				return createdChains, createdServices, err
			}
			if err := h.applyTunnelChainOnNode(chainNode.NodeID, chainData, upsert); err != nil {
				if shouldDeferTunnelRuntimeApplyError(err) {
					continue
				}
				return createdChains, createdServices, fmt.Errorf("转发链节点 %s 下发转发链失败: %w", nodeDisplayName(state.Nodes[chainNode.NodeID]), err)
			}
			createdChains = append(createdChains, chainNode.NodeID)

			serviceData := buildTunnelChainServiceConfig(state.TunnelID, chainNode, state.Nodes[chainNode.NodeID], len(nextTargets))
			if err := h.addTunnelServiceOnNodeWithMode(chainNode.NodeID, state.TunnelID, serviceData, upsert); err != nil {
				if shouldDeferTunnelRuntimeApplyError(err) {
					continue
				}
				return createdChains, createdServices, fmt.Errorf("转发链节点 %s 下发服务失败: %w", nodeDisplayName(state.Nodes[chainNode.NodeID]), err)
			}
			createdServices = append(createdServices, chainNode.NodeID)
		}
	}

	for _, outNode := range state.OutNodes {
		node := state.Nodes[outNode.NodeID]
		if node != nil && (node.IsRemote == 1 || node.Status != 1) {
			continue
		}
		serviceData := buildTunnelChainServiceConfig(state.TunnelID, outNode, state.Nodes[outNode.NodeID], 1)
		if err := h.addTunnelServiceOnNodeWithMode(outNode.NodeID, state.TunnelID, serviceData, upsert); err != nil {
			if shouldDeferTunnelRuntimeApplyError(err) {
				continue
			}
			return createdChains, createdServices, fmt.Errorf("出口节点 %s 下发服务失败: %w", nodeDisplayName(state.Nodes[outNode.NodeID]), err)
		}
		createdServices = append(createdServices, outNode.NodeID)
	}

	return createdChains, createdServices, nil
}

func (h *Handler) applyTunnelChainOnNode(nodeID int64, chainData map[string]interface{}, upsert bool) error {
	if upsert {
		return h.upsertTunnelChainOnNode(nodeID, chainData)
	}
	_, err := h.sendNodeCommand(nodeID, "AddChains", chainData, true, false)
	return err
}

func (h *Handler) applyBestExitChainOrder(tunnelID, ownerNodeID int64, outNodes []chainNodeRecord, scores []bestExitCandidateScore, ipPreference string) error {
	if h == nil {
		log.Printf("best_exit: invalid chain update context tunnel=%d owner=%d", tunnelID, ownerNodeID)
		return errors.New("invalid best exit chain update context")
	}
	if tunnelID <= 0 || ownerNodeID <= 0 || len(outNodes) == 0 {
		log.Printf("best_exit: invalid chain update input tunnel=%d owner=%d exits=%d", tunnelID, ownerNodeID, len(outNodes))
		return fmt.Errorf("invalid best exit chain update input tunnel=%d owner=%d exits=%d", tunnelID, ownerNodeID, len(outNodes))
	}
	targets := chainRecordsToRuntimeTargets(outNodes)
	orderedIDs := make([]int64, 0, len(scores))
	for _, score := range scores {
		if score.ExitNodeID > 0 {
			orderedIDs = append(orderedIDs, score.ExitNodeID)
		}
	}
	targets = orderRuntimeTargetsByNodeID(targets, orderedIDs)
	nodes := make(map[int64]*nodeRecord, len(targets)+1)
	if owner, err := h.getNodeRecord(ownerNodeID); err == nil && owner != nil {
		nodes[ownerNodeID] = owner
	}
	for _, target := range targets {
		if node, err := h.getNodeRecord(target.NodeID); err == nil && node != nil {
			nodes[target.NodeID] = node
		}
	}
	owner := nodes[ownerNodeID]
	if owner != nil && owner.IsRemote == 1 {
		if err := h.applyRemoteBestExitChainOrder(tunnelID, ownerNodeID, owner, targets, nodes, ipPreference); err != nil {
			log.Printf("best_exit: update remote federation chain failed tunnel=%d owner=%d err=%v", tunnelID, ownerNodeID, err)
			return err
		}
		log.Printf("best_exit: updated remote federation chain tunnel=%d owner=%d best_exit=%d", tunnelID, ownerNodeID, targets[0].NodeID)
		return nil
	}
	chainData, err := buildTunnelChainConfig(tunnelID, ownerNodeID, targets, nodes, ipPreference)
	if err != nil {
		log.Printf("best_exit: build chain failed tunnel=%d owner=%d err=%v", tunnelID, ownerNodeID, err)
		return err
	}
	if err := h.applyTunnelChainOnNode(ownerNodeID, chainData, true); err != nil {
		log.Printf("best_exit: update chain failed tunnel=%d owner=%d err=%v", tunnelID, ownerNodeID, err)
		return err
	}
	log.Printf("best_exit: updated chain tunnel=%d owner=%d best_exit=%d", tunnelID, ownerNodeID, targets[0].NodeID)
	return nil
}

func (h *Handler) applyRemoteBestExitChainOrder(tunnelID, ownerNodeID int64, owner *nodeRecord, targets []tunnelRuntimeNode, nodes map[int64]*nodeRecord, ipPreference string) error {
	if h == nil || h.repo == nil || owner == nil {
		return errors.New("invalid remote best exit update context")
	}
	bindings, err := h.repo.ListActiveFederationTunnelBindingsByTunnel(tunnelID)
	if err != nil {
		return err
	}
	var binding *repo.FederationTunnelBinding
	for i := range bindings {
		if bindings[i].NodeID == ownerNodeID && bindings[i].ChainType == 2 && bindings[i].Status == 1 {
			binding = &bindings[i]
			break
		}
	}
	if binding == nil {
		return fmt.Errorf("active federation middle binding not found for tunnel=%d owner=%d", tunnelID, ownerNodeID)
	}

	remoteURL := strings.TrimSpace(owner.RemoteURL)
	if remoteURL == "" {
		remoteURL = strings.TrimSpace(binding.RemoteURL)
	}
	remoteToken := strings.TrimSpace(owner.RemoteToken)
	if remoteURL == "" || remoteToken == "" {
		return errors.New("远程节点缺少共享配置")
	}

	applyTargets := make([]client.RuntimeTarget, 0, len(targets))
	for _, target := range targets {
		targetNode := nodes[target.NodeID]
		if targetNode == nil {
			return errors.New("节点不存在")
		}
		host, hostErr := selectTunnelDialHost(owner, targetNode, ipPreference, target.ConnectIP)
		if hostErr != nil {
			return hostErr
		}
		if target.Port <= 0 {
			return errors.New("节点端口不能为空")
		}
		rt := client.RuntimeTarget{
			Host:     host,
			Port:     target.Port,
			Protocol: defaultString(target.Protocol, "tls"),
		}
		// D3-04: 与 applyFederationRuntime 同口径附目标 share 凭据。
		if targetNode.IsRemote == 1 {
			rt.RelayUsername, rt.RelayPassword = parseRemoteShareRelayAuth(targetNode.RemoteConfig)
		}
		applyTargets = append(applyTargets, rt)
	}

	ownerRuntimeNode := tunnelRuntimeNode{NodeID: ownerNodeID, Protocol: "tls", Strategy: "round", ChainType: 2}
	if chainRows, listErr := h.repo.ListChainNodesForTunnel(tunnelID); listErr == nil {
		for _, row := range chainRows {
			if row.NodeID == ownerNodeID && row.ChainType == 2 {
				ownerRuntimeNode = tunnelRuntimeNode{
					NodeID:    row.NodeID,
					Protocol:  row.Protocol,
					Strategy:  row.Strategy,
					Inx:       int(row.Inx),
					ChainType: row.ChainType,
					Port:      row.Port,
					ConnectIP: row.ConnectIP,
				}
				break
			}
		}
	}

	_, err = client.NewFederationClient().ApplyRole(remoteURL, remoteToken, h.federationLocalDomain(), client.RuntimeApplyRoleRequest{
		ResourceKey: strings.TrimSpace(binding.ResourceKey),
		Role:        "middle",
		Protocol:    defaultString(ownerRuntimeNode.Protocol, "tls"),
		Strategy:    runtimeStrategyForTargets(ownerRuntimeNode, targets),
		Targets:     applyTargets,
	})
	return err
}

func (h *Handler) upsertTunnelChainOnNode(nodeID int64, chainData map[string]interface{}) error {
	if h == nil {
		return errors.New("invalid tunnel chain context")
	}
	chainName := asString(chainData["name"])
	if strings.TrimSpace(chainName) == "" {
		return errors.New("转发链名称不能为空")
	}
	payload := map[string]interface{}{"chain": chainName, "data": chainData}
	_, err := h.sendNodeCommand(nodeID, "UpdateChains", payload, true, false)
	return err
}

func retryTunnelServiceAddWithCleanup(add func() error, cleanup func() error, wait time.Duration) error {
	if add == nil {
		return errors.New("invalid tunnel service add callback")
	}
	err := add()
	if err == nil || !isAddressAlreadyInUseError(err) {
		return err
	}
	if cleanup == nil {
		return err
	}
	if cleanupErr := cleanup(); cleanupErr != nil {
		return cleanupErr
	}
	if wait > 0 {
		time.Sleep(wait)
	}
	return add()
}

func (h *Handler) addTunnelServiceOnNode(nodeID, tunnelID int64, serviceData []map[string]interface{}) error {
	return h.addTunnelServiceOnNodeWithMode(nodeID, tunnelID, serviceData, false)
}

func (h *Handler) addTunnelServiceOnNodeWithMode(nodeID, tunnelID int64, serviceData []map[string]interface{}, upsert bool) error {
	if h == nil {
		return errors.New("invalid tunnel service context")
	}
	serviceName := fmt.Sprintf("%d_tls", tunnelID)
	if len(serviceData) > 0 {
		if name, ok := serviceData[0]["name"].(string); ok && strings.TrimSpace(name) != "" {
			serviceName = strings.TrimSpace(name)
		}
	}
	command := "AddService"
	if upsert {
		command = "UpdateService"
	}
	return retryTunnelServiceAddWithCleanup(
		func() error {
			_, err := h.sendNodeCommand(nodeID, command, serviceData, true, false)
			return err
		},
		func() error {
			_, err := h.sendNodeCommand(nodeID, "DeleteService", map[string]interface{}{"services": []string{serviceName}}, false, true)
			return err
		},
		tunnelServiceBindRetryDelay,
	)
}

func (h *Handler) rollbackTunnelRuntime(chainNodeIDs, serviceNodeIDs []int64, tunnelID int64, protocol string) {
	if h == nil || tunnelID <= 0 {
		return
	}
	if protocol == "" {
		protocol = "tls"
	}
	seenServices := make(map[int64]struct{})
	serviceNames := tunnelRuntimeServiceNames(tunnelID)
	for i := len(serviceNodeIDs) - 1; i >= 0; i-- {
		nodeID := serviceNodeIDs[i]
		if _, ok := seenServices[nodeID]; ok {
			continue
		}
		seenServices[nodeID] = struct{}{}
		_, _ = h.sendNodeCommand(nodeID, "DeleteService", map[string]interface{}{"services": serviceNames}, false, true)
	}

	seenChains := make(map[int64]struct{})
	chainName := fmt.Sprintf("chains_%d", tunnelID)
	for i := len(chainNodeIDs) - 1; i >= 0; i-- {
		nodeID := chainNodeIDs[i]
		if _, ok := seenChains[nodeID]; ok {
			continue
		}
		seenChains[nodeID] = struct{}{}
		_, _ = h.sendNodeCommand(nodeID, "DeleteChains", map[string]interface{}{"chain": chainName}, false, true)
	}
}

func shouldDeferTunnelRuntimeApplyError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	if msg == "" {
		return false
	}
	if strings.Contains(msg, "节点不在线") {
		return true
	}
	if strings.Contains(msg, "等待节点响应超时") || strings.Contains(msg, "timeout") || strings.Contains(msg, "超时") {
		return true
	}
	return false
}

// isNodeOfflineOrTimeoutError returns true when the error indicates a node
// is unreachable (offline or timed out), matching the same patterns used by
// shouldDeferTunnelRuntimeApplyError.
func isNodeOfflineOrTimeoutError(err error) bool {
	return shouldDeferTunnelRuntimeApplyError(err)
}

// isNodeUnreachableError 判断错误是否属于「节点离线/不可达」类（R1 X-P1-1）：
// 结构化的 nodeOfflineError、既有离线/超时口径，以及传输层不可达特征
// （SSH 拨号失败、连接拒绝、无路由等）。节点在线但命令真实失败（ agent
// 明确回失败、远端返回业务错误等）不匹配，调用方据此区分待清理与硬失败。
func isNodeUnreachableError(err error) bool {
	if err == nil {
		return false
	}
	if IsNodeOfflineError(err) {
		return true
	}
	if isNodeOfflineOrTimeoutError(err) {
		return true
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	for _, pattern := range []string{
		"节点不可达",
		"unable to connect",
		"connection refused",
		"no route to host",
		"network is unreachable",
		"connection reset",
	} {
		if strings.Contains(msg, pattern) {
			return true
		}
	}
	return false
}

func buildTunnelChainConfig(tunnelID int64, fromNodeID int64, targets []tunnelRuntimeNode, nodes map[int64]*nodeRecord, ipPreference string) (map[string]interface{}, error) {
	fromNode := nodes[fromNodeID]
	if fromNode == nil {
		return nil, errors.New("节点不存在")
	}
	if len(targets) == 0 {
		return nil, errors.New("转发链目标不能为空")
	}
	nodeItems := make([]map[string]interface{}, 0, len(targets))
	for idx, target := range targets {
		targetNode := nodes[target.NodeID]
		if targetNode == nil {
			return nil, errors.New("节点不存在")
		}
		host, err := selectTunnelDialHost(fromNode, targetNode, ipPreference, target.ConnectIP)
		if err != nil {
			return nil, err
		}
		port := target.Port
		if port <= 0 {
			return nil, errors.New("节点端口不能为空")
		}
		protocol := defaultString(target.Protocol, "tls")
		connector := map[string]interface{}{
			"type": "relay",
		}
		// D3-04: 目标是远端节点（其角色服务带鉴权）时写入该 share
		// 的 relay 凭据；本地节点目标不带。
		if targetNode.IsRemote == 1 {
			if auth := relayAuthConfig(parseRemoteShareRelayAuth(targetNode.RemoteConfig)); auth != nil {
				connector["auth"] = auth
			}
		}
		connectorMetadata := map[string]interface{}{}
		if isTCPTunnelProtocol(protocol) {
			connectorMetadata["nodelay"] = true
			connectorMetadata["mux.keepaliveInterval"] = "15s"
			connectorMetadata["mux.keepaliveTimeout"] = "45s"
			connectorMetadata["mux.maxFrameSize"] = 32768
			connectorMetadata["mux.maxStreamBuffer"] = 2097152
		}
		if isKCPTunnelProtocol(protocol) {
			connectorMetadata["connectTimeout"] = "30s"
			connectorMetadata["mux.keepaliveInterval"] = "15s"
			connectorMetadata["mux.keepaliveTimeout"] = "45s"
			connectorMetadata["mux.maxFrameSize"] = 32768
			connectorMetadata["mux.maxStreamBuffer"] = 2097152
		}
		if len(connectorMetadata) > 0 {
			connector["metadata"] = connectorMetadata
		}
		nodeItems = append(nodeItems, map[string]interface{}{
			"name":      fmt.Sprintf("node_%d", idx+1),
			"addr":      processServerAddress(fmt.Sprintf("%s:%d", host, port)),
			"connector": connector,
			"dialer":    buildTunnelDialerConfig(protocol),
		})
	}

	strategy := runtimeTunnelStrategy(defaultString(strings.TrimSpace(targets[0].Strategy), "round"))
	hop := map[string]interface{}{
		"name": fmt.Sprintf("hop_%d", tunnelID),
		"selector": map[string]interface{}{
			"strategy":    strategy,
			"maxFails":    1,
			"failTimeout": int64(600000000000),
		},
		"nodes": nodeItems,
	}
	if strings.TrimSpace(fromNode.InterfaceName) != "" {
		hop["interface"] = fromNode.InterfaceName
	}

	return map[string]interface{}{
		"name": fmt.Sprintf("chains_%d", tunnelID),
		"hops": []map[string]interface{}{hop},
	}, nil
}

func (h *Handler) orderBestExitTargets(tunnelID, ownerNodeID int64, targets []tunnelRuntimeNode) []tunnelRuntimeNode {
	if len(targets) <= 1 || !isBestTunnelStrategy(targets[0].Strategy) {
		return append([]tunnelRuntimeNode(nil), targets...)
	}
	if h == nil || h.bestExit == nil {
		return append([]tunnelRuntimeNode(nil), targets...)
	}
	return h.bestExit.orderTargets(bestExitOwnerKey{TunnelID: tunnelID, OwnerNodeID: ownerNodeID}, targets)
}

func runtimeStrategyForTargets(owner tunnelRuntimeNode, targets []tunnelRuntimeNode) string {
	strategy := defaultString(owner.Strategy, "round")
	if len(targets) > 0 {
		strategy = defaultString(targets[0].Strategy, strategy)
	}
	return runtimeTunnelStrategy(strategy)
}

func buildTunnelChainServiceConfig(tunnelID int64, chainNode tunnelRuntimeNode, node *nodeRecord, nextHopCandidateCount int) []map[string]interface{} {
	if node == nil {
		return nil
	}
	protocol := defaultString(chainNode.Protocol, "tls")
	handlerCfg := map[string]interface{}{
		"type": "relay",
	}
	handlerMetadata := map[string]interface{}{}
	if isTCPTunnelProtocol(protocol) {
		handlerMetadata["nodelay"] = true
		handlerMetadata["mux.keepaliveInterval"] = "15s"
		handlerMetadata["mux.keepaliveTimeout"] = "45s"
		handlerMetadata["mux.maxFrameSize"] = 32768
		handlerMetadata["mux.maxStreamBuffer"] = 2097152
	}
	if isKCPTunnelProtocol(protocol) {
		handlerMetadata["connectTimeout"] = "30s"
		handlerMetadata["mux.keepaliveInterval"] = "15s"
		handlerMetadata["mux.keepaliveTimeout"] = "45s"
		handlerMetadata["mux.maxFrameSize"] = 32768
		handlerMetadata["mux.maxStreamBuffer"] = 2097152
	}
	if len(handlerMetadata) > 0 {
		handlerCfg["metadata"] = handlerMetadata
	}
	if nextHopCandidateCount > 1 {
		handlerCfg["retries"] = nextHopCandidateCount - 1
	}
	service := map[string]interface{}{
		"name": fmt.Sprintf("tunnel_%d", tunnelID),
		// 监听地址恒用节点自身的 TCPListenAddr：chainNode.ConnectIP 是拨号
		// 语义（上一跳拨它到达本节点，见 buildTunnelChainConfig /
		// selectTunnelDialHost），不是本节点的本地地址。CNIX/NAT 线路入口
		// IP 在目标机上不可绑，拿它当监听地址 agent 必报
		// "cannot assign requested address"。
		// R32-FIX-F01：监听地址先剥离 :port 再与链端口拼接（须用
		// JoinHostPort：裸 IPv6 直拼会被 processServerAddress 误读，
		// 见 realm.go 同注释）。
		"addr":     processServerAddress(net.JoinHostPort(strings.Trim(stripListenAddrPort(node.TCPListenAddr), "[]"), strconv.Itoa(chainNode.Port))),
		"handler":  handlerCfg,
		"listener": buildTunnelListenerConfig(protocol),
	}
	if chainNode.ChainType == 2 {
		service["handler"].(map[string]interface{})["chain"] = fmt.Sprintf("chains_%d", tunnelID)
	}
	if chainNode.ChainType == 3 && strings.TrimSpace(node.InterfaceName) != "" {
		service["metadata"] = map[string]interface{}{"interface": node.InterfaceName}
	}
	return []map[string]interface{}{service}
}

func selectTunnelDialHost(fromNode, toNode *nodeRecord, ipPreference string, connectIp string) (string, error) {
	if fromNode == nil || toNode == nil {
		return "", errors.New("节点不存在")
	}
	if strings.TrimSpace(connectIp) != "" {
		// R30-FIX-F02（sweep）：connectIp 与 forward inIp 同源（下拉
		// 取节点原始字段），可含 :port；须剥离后作拨号 host，否则
		// 下游再拼 ":port" 即产出 "[IP:port]:port" 畸形地址（链配
		// 置、联邦 RuntimeTarget、延迟/诊断探针同受）。存储
		// chain_tunnel.connect_ip 保持原值，收敛在消费点。
		return stripShareHostPort(connectIp), nil
	}
	fromV4 := nodeSupportsV4(fromNode)
	fromV6 := nodeSupportsV6(fromNode)
	toV4 := nodeSupportsV4(toNode)
	toV6 := nodeSupportsV6(toNode)

	switch strings.TrimSpace(ipPreference) {
	case "v6":
		if fromV6 && toV6 {
			if host := pickNodeAddressV6(toNode); host != "" {
				return host, nil
			}
		}
		if fromV4 && toV4 {
			if host := pickNodeAddressV4(toNode); host != "" {
				return host, nil
			}
		}
	case "v4":
		if fromV4 && toV4 {
			if host := pickNodeAddressV4(toNode); host != "" {
				return host, nil
			}
		}
		if fromV6 && toV6 {
			if host := pickNodeAddressV6(toNode); host != "" {
				return host, nil
			}
		}
	default:
		// 同版本优先
		if fromV4 && toV4 {
			if host := pickNodeAddressV4(toNode); host != "" {
				return host, nil
			}
		}
		if fromV6 && toV6 {
			if host := pickNodeAddressV6(toNode); host != "" {
				return host, nil
			}
		}
		// 跨版本支持：v6入v4出 / v4入v6出
		if fromV6 && toV4 {
			if host := pickNodeAddressV4(toNode); host != "" {
				return host, nil
			}
		}
		if fromV4 && toV6 {
			if host := pickNodeAddressV6(toNode); host != "" {
				return host, nil
			}
		}
	}
	return "", fmt.Errorf("节点链路不兼容：%s(v4=%t,v6=%t) -> %s(v4=%t,v6=%t)", nodeDisplayName(fromNode), fromV4, fromV6, nodeDisplayName(toNode), toV4, toV6)
}

func nodeDisplayName(node *nodeRecord) string {
	if node == nil {
		return "node"
	}
	if strings.TrimSpace(node.Name) != "" {
		return strings.TrimSpace(node.Name)
	}
	return fmt.Sprintf("node_%d", node.ID)
}

func isTCPTunnelProtocol(protocol string) bool {
	p := strings.ToLower(strings.TrimSpace(defaultString(protocol, "tls")))
	return p == "tls" || p == "mtls" || p == "mtcp"
}

func isKCPTunnelProtocol(protocol string) bool {
	return strings.EqualFold(strings.TrimSpace(protocol), "kcp")
}

func isTLSTunnelProtocol(protocol string) bool {
	return strings.EqualFold(strings.TrimSpace(defaultString(protocol, "tls")), "tls")
}

func buildTunnelDialerConfig(protocol string) map[string]interface{} {
	dialer := map[string]interface{}{
		"type": protocol,
	}
	if isKCPTunnelProtocol(protocol) {
		dialer["metadata"] = map[string]interface{}{
			"kcp.keepalive":   10,
			"kcp.tcp":         false,
			"kcp.mode":        "fast3",
			"kcp.sndwnd":      4096,
			"kcp.rcvwnd":      4096,
			"kcp.mtu":         1350,
			"kcp.sockbuf":     4194304,
			"kcp.smuxbuf":     4194304,
			"kcp.streambuf":   2097152,
			"kcp.datashard":   10,
			"kcp.parityshard": 3,
			"kcp.nocomp":      true,
			"kcp.nc":          1,
		}
	}
	return dialer
}

func buildTunnelListenerConfig(protocol string) map[string]interface{} {
	listener := map[string]interface{}{
		"type": protocol,
	}
	if isKCPTunnelProtocol(protocol) {
		listener["metadata"] = map[string]interface{}{
			"kcp.keepalive":   10,
			"kcp.tcp":         false,
			"kcp.mode":        "fast3",
			"kcp.sndwnd":      4096,
			"kcp.rcvwnd":      4096,
			"kcp.mtu":         1350,
			"kcp.sockbuf":     4194304,
			"kcp.smuxbuf":     4194304,
			"kcp.streambuf":   2097152,
			"kcp.datashard":   10,
			"kcp.parityshard": 3,
			"kcp.nocomp":      true,
			"kcp.nc":          1,
		}
	}
	return listener
}

func nodeSupportsV4(node *nodeRecord) bool {
	if node == nil {
		return false
	}
	if strings.TrimSpace(node.ServerIPv4) != "" {
		return true
	}
	if strings.TrimSpace(node.ServerIPv6) != "" {
		return false
	}
	// R28-FIX-F01（sweep E-04 同根）：legacy ServerIP 可含 :port；
	// 须先剥离再 ParseIP，否则 "IP:port" 解析失败、双栈误判全真，
	// "[v6]:port" 去括号后仍带 ":port" 同样误判。
	legacy := stripShareHostPort(node.ServerIP)
	if legacy == "" {
		return false
	}
	if ip := net.ParseIP(legacy); ip != nil {
		return ip.To4() != nil
	}
	return true
}

func nodeSupportsV6(node *nodeRecord) bool {
	if node == nil {
		return false
	}
	if strings.TrimSpace(node.ServerIPv6) != "" {
		return true
	}
	if strings.TrimSpace(node.ServerIPv4) != "" {
		return false
	}
	legacy := stripShareHostPort(node.ServerIP)
	if legacy == "" {
		return false
	}
	if ip := net.ParseIP(legacy); ip != nil {
		return ip.To4() == nil
	}
	return true
}

func pickNodeAddressV4(node *nodeRecord) string {
	if node == nil {
		return ""
	}
	// R28-FIX-F01（sweep E-04）：ServerIP 回退分支可含 :port；调用
	// 方再拼 "host:port" 作拨号地址，原值进即双端口。显式 v4 值
	// 同口径剥离（裸 IP 时为 no-op）。
	if v := strings.TrimSpace(node.ServerIPv4); v != "" {
		return stripShareHostPort(v)
	}
	return stripShareHostPort(node.ServerIP)
}

func pickNodeAddressV6(node *nodeRecord) string {
	if node == nil {
		return ""
	}
	if v := strings.TrimSpace(node.ServerIPv6); v != "" {
		return stripShareHostPort(v)
	}
	return stripShareHostPort(node.ServerIP)
}

func (h *Handler) replaceTunnelChainsTx(tx *gorm.DB, tunnelID int64, req map[string]interface{}) error {
	allocated := map[int64]int{}
	inNodes := asMapSlice(req["inNodeId"])
	for i, n := range inNodes {
		nodeID := asInt64(n["nodeId"], 0)
		if nodeID <= 0 {
			continue
		}
		if err := h.repo.CreateChainTunnelTx(
			tx,
			tunnelID,
			"1",
			nodeID,
			sql.NullInt64{},
			defaultString(asString(n["strategy"]), "round"),
			i+1,
			defaultString(asString(n["protocol"]), "tls"),
			"",
		); err != nil {
			return err
		}
	}
	for i, n := range asMapSlice(req["outNodeId"]) {
		nodeID := asInt64(n["nodeId"], 0)
		if nodeID <= 0 {
			continue
		}
		port := asInt(n["port"], 0)
		if port <= 0 {
			var pickErr error
			port, pickErr = h.repo.PickRandomNodePortTx(tx, nodeID, allocated, 0)
			if pickErr != nil {
				return pickErr
			}
		}
		connectIp := asString(n["connectIp"])
		if err := h.repo.CreateChainTunnelTx(
			tx,
			tunnelID,
			"3",
			nodeID,
			sql.NullInt64{Int64: int64(port), Valid: true},
			defaultString(asString(n["strategy"]), "round"),
			i+1,
			defaultString(asString(n["protocol"]), "tls"),
			connectIp,
		); err != nil {
			return err
		}
	}
	chainNodes := asAnySlice(req["chainNodes"])
	for i, grp := range chainNodes {
		for _, n := range asMapSlice(grp) {
			nodeID := asInt64(n["nodeId"], 0)
			if nodeID <= 0 {
				continue
			}
			port := asInt(n["port"], 0)
			if port <= 0 {
				var pickErr error
				port, pickErr = h.repo.PickRandomNodePortTx(tx, nodeID, allocated, 0)
				if pickErr != nil {
					return pickErr
				}
			}
			connectIp := asString(n["connectIp"])
			if err := h.repo.CreateChainTunnelTx(
				tx,
				tunnelID,
				"2",
				nodeID,
				sql.NullInt64{Int64: int64(port), Valid: true},
				defaultString(asString(n["strategy"]), "round"),
				i+1,
				defaultString(asString(n["protocol"]), "tls"),
				connectIp,
			); err != nil {
				return err
			}
		}
	}
	return nil
}

// deleteNodeByID removes a node. B4/F-056: 此前只有 DB 级联，注释声称
// 的「调用方 pending 流程」全仓不存在——节点上驻留的转发服务、nft 规则
// 与内核在 DB 删除后成为无人收敛的孤儿。现在删库前做数据面清理尝试：
//   - 在该节点上有入口的转发：逐条删除该节点上的服务
//     （deleteForwardServicesOnNodeBatch，覆盖隧道入口在其他节点时
//     本节点上的跨节点残留）；
//   - nftables 节点：尝试 clearNftablesNode（清节点 nft 表 + 绑定）。
//
// 与删隧道/删用户不同，这里失败只记录不中止：删除「已经死掉的节点」
// 本身就是删节点的主要场景，不能因节点不可达而永远删不掉；节点自身
// 的数据面随节点下线消亡，未清成功的残留以日志形式留痕（节点 ID +
// 转发 ID），不静默丢失。
// deleteNodeByID 的返回值是 R2X-045 的跨节点引用清单：删除前按该节点
// 全部入站汇 chainReferrers、排除同节点引用方，随删除结果回传给调用
// 方（单删/批删/创建补偿三入口共用此 funnel）。删除语义不变。
// R3X-014：清单元素为 (引用方入站 ID, 显示串) 结构，供批量入口按 ID
// 合并；单删入口在响应层投影回 []string，响应契约不变。
func (h *Handler) deleteNodeByID(id int64) ([]nodeCrossReferrer, error) {
	var brokenReferrers []nodeCrossReferrer
	// R3X-017（E3-03）：删节点此前从不刷新白名单——走共用漏斗，级联
	// 删前收集受影响的他入站（经 ChainTunnel 引用本节点 + 以本节点
	// 入站为链式出站目标，见 inboundIDsAffectedByNodeDelete），级联
	// 删后统一刷新；单删与 nodeBatchDelete 共用本 funnel 同步受益。
	var affectedInboundIDs []int64
	if h != nil && h.repo != nil {
		// R3X-015：跨节点引用守卫查询失败 fail-closed——清单不可信
		// 时中止删除并上抛（单删/批删/创建补偿三入口同受此约束），
		// 不得记录日志后照删。
		refs, guardErr := h.nodeCrossReferrers(id)
		if guardErr != nil {
			return nil, guardErr
		}
		brokenReferrers = refs
		// 同 R3X-015 口径：受影响入站收集失败同样中止删除。
		affected, collectErr := h.inboundIDsAffectedByNodeDelete(id)
		if collectErr != nil {
			return nil, collectErr
		}
		affectedInboundIDs = affected
	}
	if h != nil && h.repo != nil {
		if forwardIDs, err := h.repo.ListForwardIDsByNode(id); err == nil {
			for _, forwardID := range forwardIDs {
				fwd, err := h.repo.GetForwardRecord(forwardID)
				if err != nil || fwd == nil {
					continue
				}
				if delErr := h.deleteForwardServicesOnNodeBatch(fwd, id); delErr != nil {
					log.Printf("node delete: forward service cleanup failed node_id=%d forward_id=%d err=%v (continuing; node may be offline)", id, forwardID, delErr)
				}
			}
		} else {
			log.Printf("node delete: list forwards failed node_id=%d err=%v (continuing)", id, err)
		}
		if usesNft, err := h.nodeUsesNftables(id); err == nil && usesNft {
			if clearErr := h.clearNftablesNode(id); clearErr != nil {
				log.Printf("node delete: nftables clear failed node_id=%d err=%v (continuing; node may be offline)", id, clearErr)
			}
		}
	}
	if err := h.repo.DeleteNodeCascade(id); err != nil {
		return nil, err
	}
	// port_alloc_lock 设计要求节点删除时清理锁表条目，此前无调用方。
	repo.CleanupPortAllocLock(id)
	// D2-05：删节点同时踢掉其 agent 的 WS 存量会话，否则幽灵会话
	// 会无限存活、继续上报并被广播（secret 已随行删除也拦不住）。
	if h.wsServer != nil {
		h.wsServer.DisconnectNode(id)
	}
	// R3X-017：级联删后统一刷新（漏斗收尾，见函数头收集段）。
	h.refreshInboundWhitelists(affectedInboundIDs)
	return brokenReferrers, nil
}

func (h *Handler) deleteTunnelByID(id int64) error {
	if h == nil || h.repo == nil {
		return errors.New("repository not initialized")
	}

	tunnelName, _ := h.repo.GetTunnelName(id)
	tunnel, _ := h.repo.GetTunnelRecord(id)
	entryNodeIDs, _ := h.tunnelEntryNodeIDs(id)
	// X-P3-23（L-09-5）：尾步 peer runtime 释放标记纳入级联主事务——
	// 释放失败则整个删除回滚，不再出现「隧道已删、runtime 未释放、
	// 靠后台收敛」的不一致中间态。share 信息须在删前解析（隧道名
	// 随行删除即不可读）。
	shareID, port, isPeerShare := parsePeerShareInfoFromFederationTunnelName(tunnelName)
	if isPeerShare {
		if err := h.repo.DeleteTunnelCascadeWithPeerRelease(id, shareID, port, time.Now().UnixMilli()); err != nil {
			return err
		}
	} else if err := h.repo.DeleteTunnelCascade(id); err != nil {
		return err
	}
	if tunnelIsRealm(tunnel) {
		// 级联删除后 realm 渲染源已无此隧道：让入口节点收敛到无端点。
		_, _ = h.syncRealmNodes(entryNodeIDs, nil)
	}
	return nil
}

func (h *Handler) deleteForwardByID(id int64) error {
	return h.repo.DeleteForwardCascade(id)
}

func (h *Handler) batchForwardDelete(ids []int64) (int, int) {
	s := 0
	f := 0
	for _, id := range ids {
		if err := h.deleteForwardByID(id); err != nil {
			f++
		} else {
			s++
		}
	}
	return s, f
}

func (h *Handler) batchForwardStatus(ids []int64, status int) (int, int) {
	return h.repo.BatchUpdateForwardStatus(ids, status)
}

func (h *Handler) tunnelEntryNodeIDs(tunnelID int64) ([]int64, error) {
	return h.repo.TunnelEntryNodeIDs(tunnelID)
}

// pickTunnelPort 在隧道的全部入口节点上挑一个公共可用端口。
// F-074: 此前无公共可用端口（及无入口节点/查询失败）时静默回退虚构的
// 10000，下游再逐节点报"超出范围/已被占用"，掩盖真实原因、误导排障，
// 甚至在 10000 恰好可用时歪打正着。现在区分失败原因并显式返回错误，
// 由各调用方按自身上下文报错，绝不编造端口。
func (h *Handler) pickTunnelPort(tunnelID int64) (int, error) {
	entryNodes, err := h.tunnelEntryNodeIDs(tunnelID)
	if err != nil {
		return 0, err
	}
	if len(entryNodes) == 0 {
		return 0, errors.New("隧道没有入口节点")
	}

	var commonAvailable []int
	firstNode := true
	evaluated := 0

	for _, nodeID := range entryNodes {
		portRange, err := h.repo.GetNodePortRange(nodeID)
		if err != nil {
			continue
		}
		if portRange == "" {
			portRange = "1000-65535"
		}

		nodePorts, err := parsePorts(portRange)
		if err != nil {
			continue
		}

		used, err := h.getUsedPorts(nodeID)
		if err != nil {
			continue
		}
		evaluated++

		var available []int
		for _, p := range nodePorts {
			if !used[p] {
				available = append(available, p)
			}
		}

		if firstNode {
			commonAvailable = available
			firstNode = false
		} else {
			set := make(map[int]bool)
			for _, p := range available {
				set[p] = true
			}
			var newCommon []int
			for _, p := range commonAvailable {
				if set[p] {
					newCommon = append(newCommon, p)
				}
			}
			commonAvailable = newCommon
		}

		if len(commonAvailable) == 0 {
			break
		}
	}

	if evaluated == 0 {
		return 0, errors.New("无法获取入口节点的端口范围或占用情况")
	}
	if len(commonAvailable) == 0 {
		return 0, errors.New("入口节点间无公共可用端口（端口范围无交集，或交集端口已被占用）")
	}
	idx, _ := rand.Int(rand.Reader, big.NewInt(int64(len(commonAvailable))))
	return commonAvailable[idx.Int64()], nil
}

func (h *Handler) getUsedPorts(nodeID int64) (map[int]bool, error) {
	return h.repo.GetUsedPortsOnNodeAsMap(nodeID)
}

func parsePorts(portRange string) ([]int, error) {
	var ports []int
	parts := strings.Split(portRange, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "-") {
			rangeParts := strings.Split(part, "-")
			if len(rangeParts) != 2 {
				return nil, errors.New("invalid port range format")
			}
			start, err1 := strconv.Atoi(strings.TrimSpace(rangeParts[0]))
			end, err2 := strconv.Atoi(strings.TrimSpace(rangeParts[1]))
			if err1 != nil || err2 != nil || start > end {
				return nil, errors.New("invalid port range values")
			}
			for i := start; i <= end; i++ {
				ports = append(ports, i)
			}
		} else {
			p, err := strconv.Atoi(part)
			if err != nil {
				return nil, errors.New("invalid port value")
			}
			ports = append(ports, p)
		}
	}
	return ports, nil
}

type forwardPortReplaceEntry = struct {
	NodeID int64
	Port   int
	InIP   string
}

func buildForwardPortEntriesWithPreservedInIP(entryNodeIDs []int64, oldPorts []forwardPortRecord, port int) []forwardPortReplaceEntry {
	preservedByNode := make(map[int64]string)
	for _, fp := range oldPorts {
		current, exists := preservedByNode[fp.NodeID]
		if !exists {
			preservedByNode[fp.NodeID] = fp.InIP
			continue
		}
		if strings.TrimSpace(current) == "" && strings.TrimSpace(fp.InIP) != "" {
			preservedByNode[fp.NodeID] = fp.InIP
		}
	}

	entries := make([]forwardPortReplaceEntry, 0, len(entryNodeIDs))
	for _, nid := range entryNodeIDs {
		entries = append(entries, forwardPortReplaceEntry{
			NodeID: nid,
			Port:   port,
			InIP:   preservedByNode[nid],
		})
	}

	return entries
}

func (h *Handler) replaceForwardPorts(forwardID, tunnelID int64, port int, inIp string) error {
	entryNodes, err := h.tunnelEntryNodeIDs(tunnelID)
	if err != nil {
		return err
	}
	entries := make([]forwardPortReplaceEntry, len(entryNodes))
	for i, nid := range entryNodes {
		entries[i] = forwardPortReplaceEntry{NodeID: nid, Port: port, InIP: inIp}
	}
	return h.repo.ReplaceForwardPorts(forwardID, entries)
}

func (h *Handler) replaceForwardPortsPreservingInIP(forwardID, tunnelID int64, port int, oldPorts []forwardPortRecord) error {
	entryNodes, err := h.tunnelEntryNodeIDs(tunnelID)
	if err != nil {
		return err
	}
	entries := buildForwardPortEntriesWithPreservedInIP(entryNodes, oldPorts, port)
	return h.repo.ReplaceForwardPorts(forwardID, entries)
}

func (h *Handler) replaceForwardPortsWithRecords(forwardID int64, ports []forwardPortRecord) error {
	entries := make([]forwardPortReplaceEntry, len(ports))
	for i, fp := range ports {
		entries[i] = forwardPortReplaceEntry{NodeID: fp.NodeID, Port: fp.Port, InIP: fp.InIP}
	}
	return h.repo.ReplaceForwardPorts(forwardID, entries)
}

func (h *Handler) rollbackForwardMutation(oldForward *forwardRecord, oldPorts []forwardPortRecord) {
	if h == nil || oldForward == nil || h.repo == nil {
		return
	}

	h.repo.RollbackForwardFields(
		oldForward.ID, oldForward.UserID, oldForward.UserName, oldForward.Name,
		oldForward.TunnelID, oldForward.RemoteAddr, oldForward.Strategy, oldForward.Status,
		oldForward.SpeedID, oldForward.MaxConn, oldForward.IPMaxConn, oldForward.IPSpeedID, oldForward.ProxyProtocol, oldForward.ProxyProtocolReceive, oldForward.ProxyProtocolSend,
		time.Now().UnixMilli(),
	)

	if err := h.replaceForwardPortsWithRecords(oldForward.ID, oldPorts); err != nil {
		return
	}

	_ = h.syncForwardServices(oldForward, "UpdateService", true)
}

// userTunnelUpsertOutcome describes the DB-side effect of one
// upsertUserTunnelDB call so callers can run the data-plane sync (and its
// compensation) outside any transaction.
type userTunnelUpsertOutcome struct {
	userID   int64
	tunnelID int64
	inserted bool
	// Previous field values (update path only), for sync-failure rollback.
	existingID    int64
	prevSpeedID   sql.NullInt64
	prevFlow      int64
	prevFlowMiB   int64
	prevNum       int64
	prevExpTime   int64
	prevFlowReset int64
	prevStatus    int
}

// rollbackUserTunnelUpsert undoes the DB change of one upsert outcome
// (inserted rows are deleted, updated rows restored to their previous
// values) — the compensation applied when the data-plane sync fails.
func (h *Handler) rollbackUserTunnelUpsert(outcome *userTunnelUpsertOutcome) error {
	if outcome.inserted {
		insertedID, _, _, _, _, _, _, lookupErr := h.repo.GetExistingUserTunnel(outcome.userID, outcome.tunnelID)
		if lookupErr != nil {
			return fmt.Errorf("回滚查询错误: %w", lookupErr)
		}
		if err := h.repo.DeleteUserTunnel(insertedID); err != nil {
			return fmt.Errorf("回滚删除错误: %w", err)
		}
		return nil
	}
	if err := h.repo.UpdateUserTunnelFields(
		outcome.existingID,
		outcome.prevSpeedID,
		outcome.prevFlow,
		int(outcome.prevNum),
		outcome.prevExpTime,
		outcome.prevFlowReset,
		outcome.prevStatus,
		outcome.prevFlowMiB,
	); err != nil {
		return fmt.Errorf("回滚错误: %w", err)
	}
	return nil
}

func (h *Handler) upsertUserTunnel(req map[string]interface{}) error {
	outcome, err := h.upsertUserTunnelDB(nil, req)
	if err != nil {
		return err
	}
	if syncErr := h.syncUserTunnelForwards(outcome.userID, outcome.tunnelID); syncErr != nil {
		if rollbackErr := h.rollbackUserTunnelUpsert(outcome); rollbackErr != nil {
			return fmt.Errorf("下发失败且回滚失败: %v; %w", syncErr, rollbackErr)
		}
		// R4X-011（verdicts §3）：逐条下发是首错返回——失败点之前
		// 的转发已按新 UT 口径下发成功，DB 回滚后必须按回滚后 DB
		// 对本条 UT 的转发集尽力重下发（与批量路径整批重跑正本
		// 逐字同形），否则这批转发在 agent 侧保持新口径、与回滚后
		// DB 分叉。重下发失败不覆写原错误回执（同批量 `_ =` 口径）。
		_ = h.syncUserTunnelForwards(outcome.userID, outcome.tunnelID)
		return fmt.Errorf("下发失败，已回滚: %w", syncErr)
	}
	return nil
}

// upsertUserTunnelDB performs only the DB portion of a user-tunnel
// assignment. With tx == nil it writes directly (single assignment); with a
// tx it participates in the caller's transaction (X-P3-22 batch assignment).
// Data-plane sync is deliberately left to the caller so it never runs
// inside a transaction.
func (h *Handler) upsertUserTunnelDB(tx *gorm.DB, req map[string]interface{}) (*userTunnelUpsertOutcome, error) {
	userID := asInt64(req["userId"], 0)
	tunnelID := asInt64(req["tunnelId"], 0)
	if userID <= 0 || tunnelID <= 0 {
		return nil, fmt.Errorf("userId or tunnelId missing")
	}

	// R2X-012: user and tunnel must both exist — a phantom user_tunnel row
	// silently becomes a live grant the moment the referenced ID is later
	// created (sequential IDs make that a near-future event, not theory).
	if tx != nil {
		if err := h.repo.ValidateIDsExistTx(tx, "user", []int64{userID}); err != nil {
			return nil, err
		}
		if err := h.repo.ValidateIDsExistTx(tx, "tunnel", []int64{tunnelID}); err != nil {
			return nil, err
		}
	} else {
		if err := h.repo.ValidateIDsExist("user", []int64{userID}); err != nil {
			return nil, err
		}
		if err := h.repo.ValidateIDsExist("tunnel", []int64{tunnelID}); err != nil {
			return nil, err
		}
	}

	getExisting := func() (int64, int64, int64, int64, int64, sql.NullInt64, int, error) {
		if tx != nil {
			return h.repo.GetExistingUserTunnelTx(tx, userID, tunnelID)
		}
		return h.repo.GetExistingUserTunnel(userID, tunnelID)
	}
	existingID, currentFlow, currentNum, currentExpTime, currentFlowReset, currentSpeedID, currentStatus, lookupErr := getExisting()

	speedID := asAnyToInt64Ptr(req["speedId"])
	var err error
	if tx != nil {
		speedID, err = h.normalizeSpeedLimitReferenceTx(tx, speedID)
	} else {
		speedID, err = h.normalizeSpeedLimitReference(speedID)
	}
	if err != nil {
		return nil, err
	}

	reqFlow := asInt64(req["flow"], -1)
	var reqFlowMiB int64
	if _, hasFlowMiB := req["flowMiB"]; hasFlowMiB {
		var flowErr error
		reqFlow, reqFlowMiB, flowErr = parseTrafficLimit(req, 0)
		if flowErr != nil {
			return nil, flowErr
		}
	}
	reqNum := asInt(req["num"], -1)
	reqExpTime := asInt64(req["expTime"], -1)
	reqFlowReset := asInt64(req["flowResetTime"], -1)
	reqStatus := asInt(req["status"], -1)

	if lookupErr == sql.ErrNoRows {
		if reqFlow < 0 || reqNum < 0 || reqExpTime < 0 || reqFlowReset < 0 {
			var uFlow, uExp, uReset int64
			var uNum int
			var uErr error
			if tx != nil {
				uFlow, uNum, uExp, uReset, uErr = h.repo.GetUserDefaultsForTunnelTx(tx, userID)
			} else {
				uFlow, uNum, uExp, uReset, uErr = h.repo.GetUserDefaultsForTunnel(userID)
			}
			// R2X-012: a defaults lookup failure is now an explicit
			// error — user existence was validated above, so a failure
			// here is a real read error, never a phantom user to paper
			// over with hard-coded defaults.
			if uErr != nil {
				return nil, uErr
			}
			if reqFlow < 0 {
				reqFlow = uFlow
				var user *model.User
				var userErr error
				if tx != nil {
					user, userErr = h.repo.GetUserByIDTx(tx, userID)
				} else {
					user, userErr = h.repo.GetUserByID(userID)
				}
				if userErr == nil && user != nil {
					reqFlowMiB = user.FlowMiB
				}
			}
			if reqNum < 0 {
				reqNum = uNum
			}
			if reqExpTime < 0 {
				reqExpTime = uExp
			}
			if reqFlowReset < 0 {
				reqFlowReset = uReset
			}
		}
		if reqFlow < 0 {
			reqFlow = 0
		}
		if reqNum < 0 {
			reqNum = 0
		}
		if reqExpTime < 0 {
			reqExpTime = time.Now().Add(365 * 24 * time.Hour).UnixMilli()
		}
		if reqFlowReset < 0 {
			reqFlowReset = 1
		}
		if reqStatus < 0 {
			reqStatus = 1
		}

		if tx != nil {
			if err := h.repo.InsertUserTunnelTx(tx, userID, tunnelID, nullableInt(speedID), reqNum, reqFlow, reqFlowReset, reqExpTime, reqStatus, reqFlowMiB); err != nil {
				return nil, err
			}
		} else if err := h.repo.InsertUserTunnel(userID, tunnelID, nullableInt(speedID), reqNum, reqFlow, reqFlowReset, reqExpTime, reqStatus, reqFlowMiB); err != nil {
			return nil, err
		}

		return &userTunnelUpsertOutcome{userID: userID, tunnelID: tunnelID, inserted: true}, nil
	}
	if lookupErr != nil {
		return nil, lookupErr
	}

	newFlow := currentFlow
	var oldTunnel *model.UserTunnel
	if tx != nil {
		oldTunnel, err = h.repo.GetUserTunnelByIDTx(tx, existingID)
	} else {
		oldTunnel, err = h.repo.GetUserTunnelByID(existingID)
	}
	if err != nil {
		return nil, err
	}
	if oldTunnel == nil {
		return nil, fmt.Errorf("隧道权限不存在")
	}
	newFlowMiB := oldTunnel.FlowMiB
	if reqFlow >= 0 {
		newFlow = reqFlow
		newFlowMiB = reqFlowMiB
		if _, supplied := req["flowMiB"]; !supplied && reqFlow == currentFlow {
			newFlowMiB = oldTunnel.FlowMiB
		}
	}

	newNum := int(currentNum)
	if reqNum >= 0 {
		newNum = reqNum
	}

	newExpTime := currentExpTime
	if reqExpTime >= 0 {
		newExpTime = reqExpTime
	}

	newFlowReset := currentFlowReset
	if reqFlowReset >= 0 {
		newFlowReset = reqFlowReset
	}

	newStatus := currentStatus
	if reqStatus >= 0 {
		newStatus = reqStatus
	}

	newSpeedID := currentSpeedID
	if speedID != nil {
		newSpeedID = sql.NullInt64{Int64: *speedID, Valid: true}
	} else if _, ok := req["speedId"]; ok {
		newSpeedID = sql.NullInt64{Valid: false}
	}

	if tx != nil {
		if err := h.repo.UpdateUserTunnelFieldsTx(tx, existingID, newSpeedID, newFlow, newNum, newExpTime, newFlowReset, newStatus, newFlowMiB); err != nil {
			return nil, err
		}
	} else if err := h.repo.UpdateUserTunnelFields(existingID, newSpeedID, newFlow, newNum, newExpTime, newFlowReset, newStatus, newFlowMiB); err != nil {
		return nil, err
	}

	return &userTunnelUpsertOutcome{
		userID:        userID,
		tunnelID:      tunnelID,
		existingID:    existingID,
		prevSpeedID:   currentSpeedID,
		prevFlow:      currentFlow,
		prevFlowMiB:   oldTunnel.FlowMiB,
		prevNum:       currentNum,
		prevExpTime:   currentExpTime,
		prevFlowReset: currentFlowReset,
		prevStatus:    currentStatus,
	}, nil
}

func (h *Handler) syncUserTunnelForwards(userID, tunnelID int64) error {
	forwards, err := h.listForwardsByTunnel(tunnelID)
	if err != nil {
		return err
	}
	for i := range forwards {
		f := &forwards[i]
		if f.UserID == userID {
			if err := h.syncForwardServices(f, "UpdateService", true); err != nil {
				return err
			}
		}
	}

	return nil
}

func (h *Handler) syncUserMaxConnForwards(userID int64) ([]string, error) {
	forwards, err := h.listActiveForwardsByUser(userID)
	if err != nil {
		return nil, err
	}
	warnings := make([]string, 0)
	for i := range forwards {
		f := &forwards[i]
		if f.MaxConn > 0 {
			continue
		}
		syncWarnings, syncErr := h.syncForwardServicesWithWarnings(f, "UpdateService", true)
		warnings = append(warnings, syncWarnings...)
		if syncErr != nil {
			return warnings, syncErr
		}
	}
	return warnings, nil
}

// cleanupForwardsForUserTunnel deletes all forwarding rules belonging to a
// specific user+tunnel pair. It notifies nodes to remove the runtime services
// first, then deletes the DB records. This is best-effort: individual failures
// do not abort the overall cleanup so that remaining forwards are still cleaned.
//
// R5-B3 D1-001 矩阵 #7 / 补充 C: pending-delete 行不硬删（已在 pending 批次中，
// 由后台 job 按节点聚合清理，不产生孤儿）；nft 模式的 active 行标记 pending 后
// 按节点聚合尝试一次清理，失败留给 job 退避重试。
func (h *Handler) cleanupForwardsForUserTunnel(userID, tunnelID int64) {
	if userID <= 0 || tunnelID <= 0 {
		return
	}
	forwards, err := h.repo.ListForwardsByUserAndTunnel(userID, tunnelID)
	if err != nil || len(forwards) == 0 {
		return
	}
	// R3X-016（E3-02）：本漏斗删转发此前从不刷新白名单——走共用漏斗，
	// 删前按待删转发集收集受影响入站（已在 pending 的行不在本批终删
	// 范围，排除收集、其物化处置随 jobs 终删面），删后统一刷新一次。
	// nft 新标 pending 的行若在本批 reconcile 中终删，其入站刷新后
	// 按现行 DB 重算掉出 allow；reconcile 失败留 pending 的行仍在库，
	// 重算口径不变、不提前收回（N-04）。
	var deletableIDs []int64
	for i := range forwards {
		if !isForwardPendingDelete(&forwards[i]) {
			deletableIDs = append(deletableIDs, forwards[i].ID)
		}
	}
	affectedInboundIDs := h.inboundIDsForForwards(deletableIDs)
	nftGroups := make(map[int64][]int64)
	for i := range forwards {
		f := &forwards[i]
		if isForwardPendingDelete(f) {
			// R5-B5 补充 C 修正：已有 pending 行不跳过，合并进同节点清理批次，
			// 统一重试时间表（避免分裂）。pending 记录中有 node_id。
			if pending, err := h.repo.GetNftPendingDelete(f.ID); err == nil && pending != nil {
				// 加入空批次触发该节点的 reconcile（pending 行会被 plan 排除，
				// 但 reconcile 会尝试清理残留）。
				if _, ok := nftGroups[pending.NodeID]; !ok {
					nftGroups[pending.NodeID] = []int64{}
				}
			}
			continue
		}
		if f.Status != model.ForwardStatusActive {
			_ = h.deleteForwardByID(f.ID)
			continue
		}
		if nftMode, entryNodeIDs, modeErr := h.tunnelUsesNftables(f.TunnelID); modeErr == nil && nftMode && len(entryNodeIDs) > 0 {
			// R35-FIX-F01：遗留/竞态混合态下跳过、不删库（gost/realm
			// 服务不会被触碰）；行保留待管理员先编辑隧道去掉 nft/
			// 多余入口后再清理，记日志不静默。
			if err := h.rejectMixedNftForwardState(f.TunnelID, nftMode, entryNodeIDs); err != nil {
				log.Printf("cleanup forwards for user tunnel: mixed nft state skipped forward_id=%d err=%v", f.ID, err)
				continue
			}
			if markErr := h.repo.MarkForwardPendingDelete(f.ID, entryNodeIDs[0], time.Now()); markErr == nil {
				nftGroups[entryNodeIDs[0]] = append(nftGroups[entryNodeIDs[0]], f.ID)
				continue
			}
		}
		_ = h.controlForwardServices(f, "DeleteService", true)
		_ = h.deleteForwardByID(f.ID)
	}
	// best-effort：按节点聚合尝试一次，失败的组留在 pending 由 job 重试。
	_ = h.reconcileGroupedNftDeletes(nftGroups)
	// R3X-016：删后统一刷新（漏斗收尾，见上方收集段）。
	h.refreshInboundWhitelists(affectedInboundIDs)
}

func (h *Handler) normalizeSpeedLimitReference(speedID *int64) (*int64, error) {
	if speedID == nil {
		return nil, nil
	}

	exists, err := h.repo.SpeedLimitExists(*speedID)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}

	return speedID, nil
}

func (h *Handler) normalizeSpeedLimitReferenceTx(tx *gorm.DB, speedID *int64) (*int64, error) {
	if speedID == nil {
		return nil, nil
	}
	exists, err := h.repo.SpeedLimitExistsTx(tx, *speedID)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}
	return speedID, nil
}

func sameSpeedLimitSelection(current sql.NullInt64, requested *int64) bool {
	if requested == nil {
		return !current.Valid
	}

	return current.Valid && current.Int64 == *requested
}

func asAnySlice(v interface{}) []interface{} {
	if v == nil {
		return nil
	}
	if arr, ok := v.([]interface{}); ok {
		return arr
	}
	return nil
}

func asMapSlice(v interface{}) []map[string]interface{} {
	arr := asAnySlice(v)
	if arr == nil {
		return nil
	}
	out := make([]map[string]interface{}, 0, len(arr))
	for _, it := range arr {
		if m, ok := it.(map[string]interface{}); ok {
			out = append(out, m)
		}
	}
	return out
}

func asMap(v interface{}) map[string]interface{} {
	if m, ok := v.(map[string]interface{}); ok && m != nil {
		return m
	}
	return map[string]interface{}{}
}

func asString(v interface{}) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(t)
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int, int32, int64:
		return fmt.Sprintf("%v", t)
	default:
		b, _ := json.Marshal(t)
		return strings.Trim(string(b), "\"")
	}
}

// presentNumericFieldInvalid 是 FIX2-A（V2-07）的 fail-closed 守
// 卫：部分更新中字段「在场、非空、但不可解析为整数」时返回 true，
// 调用方必须报错且不落库。旧实现对这类输入静默套用硬默认
// （userUpdate.num→10、tunnelUpdate.flow→1、nodeUpdate.expiryTime
// →0 清空到期），与「未传保旧」的部分更新口径不一致且无任何提
// 示。空值（缺失/null/空串）不算在场有值，沿用各字段既有缺省语
// 义；合法数值（含 JSON 数字、数字字符串）不受影响。
func presentNumericFieldInvalid(req map[string]interface{}, key string) bool {
	raw, ok := req[key]
	if !ok {
		return false
	}
	s := asString(raw)
	if s == "" {
		return false
	}
	_, err := strconv.ParseInt(s, 10, 64)
	return err != nil
}

// presentFloatFieldInvalid 同上，针对浮点字段（如 trafficRatio）。
func presentFloatFieldInvalid(req map[string]interface{}, key string) bool {
	raw, ok := req[key]
	if !ok {
		return false
	}
	s := asString(raw)
	if s == "" {
		return false
	}
	_, err := strconv.ParseFloat(s, 64)
	return err != nil
}

func asInt(v interface{}, def int) int {
	s := asString(v)
	if s == "" {
		return def
	}
	i, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return i
}

func asInt64(v interface{}, def int64) int64 {
	s := asString(v)
	if s == "" {
		return def
	}
	i, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return def
	}
	return i
}

func asFloat(v interface{}, def float64) float64 {
	s := asString(v)
	if s == "" {
		return def
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return def
	}
	return f
}

func asAnyToInt64Ptr(v interface{}) *int64 {
	s := asString(v)
	if s == "" || strings.EqualFold(s, "null") {
		return nil
	}
	i, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return nil
	}
	return &i
}

func idFromBody(r *http.Request, w http.ResponseWriter) int64 {
	return asInt64FromBodyKey(r, w, "id")
}

func asInt64FromBodyKey(r *http.Request, w http.ResponseWriter, key string) int64 {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return 0
	}
	var req map[string]interface{}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return 0
	}
	id := asInt64(req[key], 0)
	if id <= 0 {
		response.WriteJSON(w, response.ErrDefault("参数错误"))
		return 0
	}
	return id
}

func idsFromBody(r *http.Request, w http.ResponseWriter) []int64 {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return nil
	}
	var req map[string]interface{}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return nil
	}
	arr := asAnySlice(req["ids"])
	if len(arr) == 0 {
		response.WriteJSON(w, response.ErrDefault("ids不能为空"))
		return nil
	}
	ids := make([]int64, 0, len(arr))
	for _, x := range arr {
		id := asInt64(x, 0)
		if id > 0 {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func nullableText(s string) interface{} {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

func nullableUnixMilli(v int64) interface{} {
	if v <= 0 {
		return nil
	}
	return v
}

func normalizeNodeRenewalCycle(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "month", "quarter", "year":
		return strings.ToLower(strings.TrimSpace(v))
	default:
		return ""
	}
}

func defaultNodeForwardMode(mode string) string {
	switch strings.TrimSpace(strings.ToLower(mode)) {
	case "nftables":
		return "nftables"
	default:
		return "agent"
	}
}

func nullableInt(v *int64) interface{} {
	if v == nil {
		return nil
	}
	return *v
}

func defaultString(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

// cryptoRandRead 是凭据随机源的测试 seam（生产恒为 crypto/rand.Read）。
var cryptoRandRead = rand.Read

// randomToken 生成 n 字节随机凭据的 hex 编码。X-P3-14（L-03-3）：
// 随机源失败 fail-closed 返回错误——旧实现降级为时间戳 hex（可
// 预测）落库为节点 secret/联邦凭据，属安全缺陷；调用方必须报错。
func randomToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := cryptoRandRead(buf); err != nil {
		return "", fmt.Errorf("生成随机凭据失败: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func asInt64Slice(v interface{}) []int64 {
	arr := asAnySlice(v)
	if len(arr) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(arr))
	for _, x := range arr {
		if id := asInt64(x, 0); id > 0 {
			ids = append(ids, id)
		}
	}
	return ids
}

func validateLocalNodePort(node *nodeRecord, port int) error {
	if node == nil || node.IsRemote == 1 || port <= 0 {
		return nil
	}
	portRange := strings.TrimSpace(node.PortRange)
	if portRange == "" {
		return nil
	}
	// B3 口径判定（W10 复审 A1）：节点端口规格是「可分配端口池」——自动
	// 分配（parsePortRangeSpec）从中取口，本函数也只被转发/中转/隧道入口
	// 路径调用；内核入站监听端口是管理员显式配置、自始只做占用冲突校验
	// （inboundPortConflict），不在此池口径内。故报错文案明示约束范围，
	// 不给入站新增拦截（存量入站多在池外，新增拦截会在编辑时误伤）。
	// B4/F-016: 端口规格是逗号多段的集合语义（与自动分配
	// parsePortRangeSpec 一致）。此前压成 min-max 区间校验，段间空隙
	// 端口被误放行——校验通过但自动分配永不选、agent 按规格不认。
	ports, err := parsePorts(portRange)
	if err != nil || len(ports) == 0 {
		return nil // 规格不可解析时保持既有宽松行为，不新设拒绝面
	}
	for _, p := range ports {
		if p == port {
			return nil
		}
	}
	return fmt.Errorf("端口 %d 不在节点 %s 允许范围 %s 内（该范围仅约束转发/中转/隧道入口端口）", port, node.Name, portRange)
}

// validateForwardEntryNodes 逐入口节点做转发端口校验（远程范围/
// 本地规格/占用）。X-P3-16（L-04-2，BC-14，裁定 V-11）：节点记录
// 读取失败 fail-closed 直接报错——create/update 旧实现 continue
// 跳过校验仍落库，与 batch 的整条失败口径分叉，现统一。
func (h *Handler) validateForwardEntryNodes(entryNodes []int64, port int, currentForwardID int64) error {
	for _, nodeID := range entryNodes {
		node, nodeErr := h.getNodeRecord(nodeID)
		if nodeErr != nil {
			return nodeErr
		}
		if err := validateRemoteNodePort(node, port); err != nil {
			return err
		}
		if err := validateLocalNodePort(node, port); err != nil {
			return err
		}
		if err := h.validateForwardPortAvailability(node, port, currentForwardID); err != nil {
			return err
		}
	}
	return nil
}

func (h *Handler) validateForwardPortAvailability(node *nodeRecord, port int, currentForwardID int64) error {
	if h == nil || h.repo == nil || node == nil || port <= 0 {
		return nil
	}
	// R5-B3 纠正 C / 补充 B: 拿冲突行详情以区分 pending 占用；本函数在 tx 之外
	// 调用（预校验），用非 tx 版本安全。tx 内调用方必须用 Tx 版本。
	conflict, err := h.repo.FindOtherForwardOnNodePort(node.ID, port, currentForwardID)
	if err != nil {
		return err
	}
	if conflict == nil {
		return nil
	}
	if conflict.Status == model.ForwardStatusPendingDelete {
		return fmt.Errorf("节点 %s 端口 %d 正等待节点清理（转发 %s），请稍后重试或联系管理员",
			node.Name, port, conflict.Name)
	}
	return fmt.Errorf("节点 %s 端口 %d 已被其他转发占用", node.Name, port)
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
			start, err1 := strconv.Atoi(strings.TrimSpace(r[0]))
			end, err2 := strconv.Atoi(strings.TrimSpace(r[1]))
			if err1 != nil || err2 != nil || start <= 0 || end <= 0 {
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
		p, err := strconv.Atoi(part)
		if err != nil || p <= 0 {
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
