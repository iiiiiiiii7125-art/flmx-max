package handler

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go-backend/internal/http/client"
	"go-backend/internal/http/response"
	"go-backend/internal/kernel"
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
	if passwordTooShort(pwd) {
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

	// B02-001（FIX2-A 同族补齐）：与 userUpdate 同口径——在场但不可
	// 解析的数值字段 fail-closed 报错、不落库。此前 create 面无此
	// 守卫：配额传 "abc"/1.5 时 asInt64 静默回 0，而配额只在 >0 时
	// 才落行，等于「传了配额却不限额」且界面显示成功。
	for _, key := range []string{"flow", "num", "expTime", "flowResetTime", "status", "maxConn", "dailyQuotaGB", "monthlyQuotaGB"} {
		if presentNumericFieldInvalid(req, key) {
			response.WriteJSON(w, response.ErrDefault(fmt.Sprintf("%s 参数无效", key)))
			return
		}
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

	// O5-004：CreateUser + 配额配置两步包在同一个事务里，任一步失败整体回滚。
	// 旧逻辑两步各自提交，后续失败会留下半成品账号（重试时用户名已被占用）。
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
	if err := tx.Commit().Error; err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
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

	// R2X-036（校验前置档）：配额解析全部前移到首写之前。
	// 旧实现主行先落库、配额段后失败，报错与落库事实不符
	// （改名已生效却回「保存失败」）。配额单侧补齐的只读查询同理前移，
	// 其读取失败也不再发生在主行写入之后。
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
	} else if passwordTooShort(pwd) {
		// B02-002：改密与 create 共用最小长度口径（≥6 位，此前 update
		// 面无此校验，可把密码改成 3 位且正常登录）。
		response.WriteJSON(w, response.ErrDefault("密码至少6位"))
		return
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

	// B01-002：启用→停用构成一次会话吊销。标准停用走
	// UpdateUserWithoutPassword 不推进 password_changed_at，旧 token
	// 只被 Status 门暂停、重新启用后自动复活。此处在主行落库成功后
	// 同步 RevokeUserSessions，使停用前签发的 token 在中间件
	// IatMs<=PasswordChangedAt 判据下永久失效；启用不回退该时间戳，
	// 与改密/删 passkey 的既有吊销口径一致。带 pwd 的更新已由
	// UpdateUserWithPassword 推进同一时间戳，重复推进无副作用。
	if oldUser.Status == 1 && status == 0 {
		if err := h.repo.RevokeUserSessions(id, now); err != nil {
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
	}

	// G-UPDATE-01：与同函数 syncUserMaxConnForwards（:415-419）同口径——
	// 隧道限额同步失败即上抛，不再只记日志回成功（否则用户行是新值、
	// 全部 user_tunnel 行仍是旧限额，且前端无任何信号）。
	if err := h.repo.PropagateUserFlowToTunnels(id, flow, num, expTime, flowResetTime, flowMiB); err != nil {
		log.Printf("propagate user flow to tunnels failed user_id=%d err=%v", id, err)
		response.WriteJSON(w, response.ErrDefault(fmt.Sprintf("隧道限额同步失败: %v", err)))
		return
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

	// R2-G-01：重置前按 type 校验存在（与 userQuotaReset 的 G-QUOTA-01
	// 同口径），不存在即报错，不再幻影回成功。type=1 查用户，
	// type=2 查 user_tunnel 行。
	if typeVal == 1 {
		u, err := h.repo.GetUserByID(id)
		if err != nil {
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
		if u == nil {
			response.WriteJSON(w, response.ErrDefault("用户不存在"))
			return
		}
		// B2-02：重置落库失败必须如实报错，不再恒回成功
		// （与上方存在性检查的 -2 口径一致）。
		if err := h.repo.ResetUserFlowByUser(id, time.Now().UnixMilli()); err != nil {
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
	} else {
		if _, _, err := h.repo.GetUserTunnelUserAndTunnel(id); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				response.WriteJSON(w, response.ErrDefault("隧道不存在"))
			} else {
				response.WriteJSON(w, response.Err(-2, err.Error()))
			}
			return
		}
		if err := h.repo.ResetUserFlowByUserTunnel(id); err != nil {
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
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
//
// R2 B04-001：校验顺序改「先验原始值、后剥离」。旧序先用
// net.SplitHostPort 剥离再验剥离产物——SplitHostPort 不验端口数值性，
// "http://evil.com" 剥成 "http"、"example.com:abc" 剥成 "example.com"
// 后 IsValidNodeAddress 的 scheme 拒绝与端口数值/范围校验（B04-005）
// 在主写路径上不可达，残缺地址静默落库。先以原始值过同一校验器
// （host:port 合法形态本就放行），再剥离、再验剥离产物兜底。
func stripAndValidateNodeAddress(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", nil
	}
	if err := IsValidNodeAddress(trimmed); err != nil {
		return "", err
	}
	stripped := stripShareHostPort(trimmed)
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
	// R2 B04-001：同 stripAndValidateNodeAddress——先验原始值，
	// 端口段数值/范围非法（":abc"/":99999"）不得被剥离静默吞掉。
	if err := IsValidNodeAddress(value); err != nil {
		return "", err
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
	// B04-003（R3 K4）：与 nodeUpdate 同口径——在场但不可解析的数值
	// 字段 fail-closed 拒绝（FIX2-A 门此前唯独漏了创建面，expiryTime
	// 传 "abc" 被静默套 0 = 到期时间丢失且回成功）。
	for _, key := range []string{"http", "tls", "socks", "expiryTime"} {
		if presentNumericFieldInvalid(req, key) {
			response.WriteJSON(w, response.ErrDefault(fmt.Sprintf("%s 参数无效", key)))
			return
		}
	}
	name := asString(req["name"])
	// R30-FIX-F04（写侧归一化，用户 2026-10-26 拍板）：入库前剥离
	// :port 再存。":port-only" 等退化值归一化为空后由下方的空值检查
	// 拒绝。
	// R2 B04-001：归一走 stripAndValidateNodeAddress（先验原始值后
	// 剥离）——旧序「先剥离再校验」使 IsValidNodeAddress 的端口数值
	// 校验不可达，"example.com:abc"、"http://evil.com" 被静默剥成
	// 残缺地址落库。
	serverIP, sErr := stripAndValidateNodeAddress(asString(req["serverIp"]))
	if sErr != nil {
		response.WriteJSON(w, response.ErrDefault(sErr.Error()))
		return
	}
	if name == "" || serverIP == "" {
		response.WriteJSON(w, response.ErrDefault("节点名称和地址不能为空"))
		return
	}
	// R2 K1 枚举复扫（B03-001 同族）：名称按列宽 varchar(100)、网卡名
	// 按 varchar(200) 校验——此前无门，PG 吐原始错误、SQLite 静默存入。
	if len([]rune(name)) > 100 {
		response.WriteJSON(w, response.ErrDefault("节点名称长度不能超过100个字符"))
		return
	}
	if len([]rune(asString(req["interfaceName"]))) > 200 {
		response.WriteJSON(w, response.ErrDefault("网卡名称长度不能超过200个字符"))
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
	// R2 K1 枚举复扫（B04-001 同族）：extraIPs 与 V4/V6 同口径逐项
	// 先验后剥（此前只剥离不校验，坏端口段被静默吞掉）。
	extraIPs, err := stripAndValidateNodeAddressList(asString(req["extraIPs"]))
	if err != nil {
		response.WriteJSON(w, response.ErrDefault("节点附加 IP 地址格式无效："+err.Error()))
		return
	}
	// B04-003：port 写侧按 parsePortRangeSpec 同口径校验（不可解析/
	// 越界/反序拒绝），不再原样落库。
	portSpec := defaultString(asString(req["port"]), "1000-65535")
	if err := validateNodePortSpec(portSpec); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
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
		portSpec,
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
		// PORT-CLOSURE B-NEW-01 + R2 K1 枚举复扫：extraIPs 已在上方
		// 逐项先验后剥（extraIPs 变量），校验口径与 V4/V6 一致。
		nullableText(extraIPs),
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
		if _, _, delErr := h.deleteNodeByID(nodeID); delErr != nil {
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
		// 的字段保持存量原值。
		// R2 B04-001：同 nodeCreate——先验原始值后剥离（下方对合并值
		// 的 IsValidNodeAddress 仍保留，覆盖未提交的存量保值路径）。
		v, vErr := stripAndValidateNodeAddress(asString(req["serverIp"]))
		if vErr != nil {
			response.WriteJSON(w, response.ErrDefault(vErr.Error()))
			return
		}
		// B04-002（R3 K4）：显式提交的 serverIp 归一后为空与
		// nodeCreate 同口径拒绝（空串按 strip 定义返回 ("", nil)，
		// 旧口径直接落库空地址、节点随即不可拨）。
		if v == "" {
			response.WriteJSON(w, response.ErrDefault("节点名称和地址不能为空"))
			return
		}
		serverIP = v
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
	protocolChanged := newHTTP != currentHTTP || newTLS != currentTLS || newSocks != currentSocks
	// R4B03-002（裁定方案①）：协议三开关是面板权威字段——本地节点
	// 离线时旧口径跳过下发仍落库回成功，而节点重连握手会以 agent
	// 本地值无条件覆回（UpdateNodeOnline），修改无声丢失。与前端
	// 既定口径一致：本地节点不在线且三开关有变更时整单拒绝并点名，
	// 不落库；其他字段的离线修改不受此门影响。远程节点无本地握手
	// 覆写面，不在本机制范围内，保持原口径。
	if protocolChanged && !usesNftablesRuntime && currentNode.IsRemote != 1 && currentStatus != 1 {
		response.WriteJSON(w, response.ErrDefault("节点未在线，等待节点上线后再设置"))
		return
	}
	if currentStatus == 1 && !usesNftablesRuntime && protocolChanged {
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
	// R2 K1 枚举复扫（B04-001 同族）：剥离改走先验后剥的 List 变体
	// （提交值已在下方预检报错，此处结果与其一致）。
	resolveExtraIPs := func(key string, current interface{}) interface{} {
		if _, ok := req[key]; ok {
			stripped, _ := stripAndValidateNodeAddressList(asString(req[key]))
			return nullableText(stripped)
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
	// R2 K1 枚举复扫（B04-001 同族）：extraIPs 仅提交时逐项先验后剥，
	// 与 nodeCreate 同口径。
	if _, ok := req["extraIPs"]; ok {
		if _, err := stripAndValidateNodeAddressList(asString(req["extraIPs"])); err != nil {
			response.WriteJSON(w, response.ErrDefault("节点附加 IP 地址格式无效："+err.Error()))
			return
		}
	}
	nodeName := currentNode.Name
	if _, ok := req["name"]; ok {
		nodeName = asString(req["name"])
		// B04-002（R3 K4）：显式提交空名称与 nodeCreate 同口径拒绝
		//（未提交时保持现值，A2-04 语义不变）。
		if nodeName == "" {
			response.WriteJSON(w, response.ErrDefault("节点名称和地址不能为空"))
			return
		}
	}
	// R2 K1 枚举复扫（B03-001 同族）：显式提交的名称/网卡名按列宽
	// 校验（presence 口径，存量值不连坐）。
	if _, ok := req["name"]; ok && len([]rune(nodeName)) > 100 {
		response.WriteJSON(w, response.ErrDefault("节点名称长度不能超过100个字符"))
		return
	}
	if _, ok := req["interfaceName"]; ok && len([]rune(asString(req["interfaceName"]))) > 200 {
		response.WriteJSON(w, response.ErrDefault("网卡名称长度不能超过200个字符"))
		return
	}
	nodePort := currentNode.Port
	if _, ok := req["port"]; ok {
		nodePort = defaultString(asString(req["port"]), "1000-65535")
	}
	if strings.TrimSpace(nodePort) == "" {
		nodePort = "1000-65535"
	}
	// B04-003：仅校验本次提交的 port（存量非法值做无关编辑不连坐，
	// 与 R11X-008/009 的 presence 口径一致），口径同 nodeCreate。
	if _, ok := req["port"]; ok {
		if err := validateNodePortSpec(nodePort); err != nil {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
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
	// R6-21：SSH 配置的解析与校验前置到字段落库之前。旧顺序先
	// UpdateNode 落全部字段、再 persistNodeSSHConfig，其确定性
	// 校验失败（配置不完整/端口无效等）回错时字段已生效、回执
	// 却不披露，属两段写非原子。校验只依赖请求与已存 SSH 配置
	// （均可先行读取），故先 resolve 求解并校验、整单拒绝零落
	// 库；落库后 apply 只剩单次仓库写，其失败只能是仓库错误。
	// 与 kernelControl 的 B11-002 回执披露口径不同：那里的第二段
	// 是不可预检的远程下发，这里的第二段是本地确定性校验，可
	// 前置消除而非仅披露。
	sshPlan, err := h.resolveNodeSSHConfig(id, req, forwardMode, true)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
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
	if err := h.applyNodeSSHConfig(id, sshPlan, now); err != nil {
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

// nodeSSHConfigPlan is the resolved outcome of the SSH config
// validation phase: everything persistNodeSSHConfig would write,
// computed and fully validated before anything is persisted.
// R6-21 split the function into resolve/apply because nodeUpdate
// used to persist the node fields first and only then discover a
// deterministic SSH validation failure — the update reported an
// error while the fields had already landed, with a receipt that
// disclosed neither fact. The validation depends only on the
// request and the stored SSH config (both readable up front), so
// nodeUpdate now resolves first and rejects the whole update
// before any write; apply then performs the single write, whose
// only remaining failure mode is a repository error.
type nodeSSHConfigPlan struct {
	delete bool
	noop   bool
	input  repo.NftSSHConfigInput
}

func (h *Handler) resolveNodeSSHConfig(nodeID int64, req map[string]interface{}, forwardMode string, preserveSecrets bool) (*nodeSSHConfigPlan, error) {
	if h == nil || h.repo == nil {
		return nil, errors.New("handler not initialized")
	}
	if nodeID <= 0 {
		return nil, errors.New("节点ID不能为空")
	}
	if forwardMode != "nftables" {
		return &nodeSSHConfigPlan{delete: true}, nil
	}
	// V2-06：部分更新（preserveSecrets=true）未带 sshConfig 整段时
	// 整段不动——旧实现仍会用请求值重建并 upsert，其中 port 被
	// asInt(...,22) 填成 22 后保值分支（port<=0 才保值）永不触发，
	// 任何不带完整 sshConfig 的 nodeUpdate 都把非 22 端口的 nft
	// 节点 SSH 配置静默改回 22，数据面随之失联。
	if preserveSecrets {
		if _, ok := req["sshConfig"]; !ok {
			return &nodeSSHConfigPlan{noop: true}, nil
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
			return nil, errors.New("nftables 节点 SSH 端口无效")
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
			return nil, err
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
		return nil, errors.New("nftables 节点 SSH 配置不完整")
	}
	// 复扫 R-04：SSH host 是拨号目标主机名（runner 以
	// JoinHostPort(host, sshPort) 拼地址），显式填值此前无任何
	// 主机名校验；空标签在任何 DNS 下都不可解析，写面即拒。
	// serverIp 派生分支已由节点地址门覆盖，此处复核无害。
	if err := validateNoEmptyLabelAddr(host); err != nil {
		return nil, fmt.Errorf("nftables 节点 SSH 主机名格式无效：%w", err)
	}
	if port <= 0 || port > 65535 {
		return nil, errors.New("nftables 节点 SSH 端口无效")
	}

	authType = strings.ToLower(authType)
	switch authType {
	case "password":
		if strings.TrimSpace(password) == "" {
			return nil, errors.New("nftables 节点 SSH 密码不能为空")
		}
		privateKey = ""
	case "private_key", "":
		authType = "private_key"
		if strings.TrimSpace(privateKey) == "" {
			return nil, errors.New("nftables 节点 SSH 私钥不能为空")
		}
		password = ""
	default:
		return nil, errors.New("nftables 节点 SSH 认证方式无效")
	}

	switch strings.ToLower(sudoMode) {
	case "", "none":
		sudoMode = "none"
	case "sudo", "sudo_su":
	default:
		return nil, errors.New("nftables 节点 sudo 模式无效")
	}

	return &nodeSSHConfigPlan{input: repo.NftSSHConfigInput{
		Host:       host,
		Port:       port,
		Username:   username,
		AuthType:   authType,
		Password:   password,
		PrivateKey: privateKey,
		Passphrase: passphrase,
		SudoMode:   sudoMode,
	}}, nil
}

func (h *Handler) applyNodeSSHConfig(nodeID int64, plan *nodeSSHConfigPlan, now int64) error {
	if plan == nil || plan.noop {
		return nil
	}
	if plan.delete {
		return h.repo.DeleteNodeSSHConfig(nodeID)
	}
	return h.repo.UpsertNodeSSHConfig(nodeID, plan.input, now)
}

func (h *Handler) persistNodeSSHConfig(nodeID int64, req map[string]interface{}, forwardMode string, now int64, preserveSecrets bool) error {
	plan, err := h.resolveNodeSSHConfig(nodeID, req, forwardMode, preserveSecrets)
	if err != nil {
		return err
	}
	return h.applyNodeSSHConfig(nodeID, plan, now)
}

func (h *Handler) nodeDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	// 请求体在此一次解码：除 id 外还要读 uninstall 标记（旧客户端不带
	// 该字段时 asBool 回落 false，行为与此前完全一致）。
	var req map[string]interface{}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	id := asInt64(req["id"], 0)
	if id <= 0 {
		response.WriteJSON(w, response.ErrDefault("参数错误"))
		return
	}
	// 漏斗三段：守卫校验 → 连带卸载 → 级联删除。守卫 fail-closed，
	// 必须先于卸载步：卸载含不可逆的在线自卸载下发与待卸载登记，
	// 若守卫随后中止删除，会留下「记录还在、agent 已卸载」。
	guard, guardErr := h.checkNodeDeleteGuards(id)
	if guardErr != nil {
		response.WriteJSON(w, response.Err(-2, guardErr.Error()))
		return
	}
	// R4-F2-02：守卫已占用的在途登记在本入口兜底释放——正常路径由
	// 级联段先行释放并清标、本 defer no-op；连带卸载步 panic 时级联
	// 段从未进入，靠本 defer 在展开期释放，避免登记泄漏至进程重启。
	defer guard.releaseUpgradeOp(h, id)
	// 连带卸载必须赶在删记录之前：记录删除后 secret 与在线会话都不
	// 复存在，既下发不了命令也登记不了待卸载。卸载步的失败只进
	// uninstallWarnings，不中止删除（见 node_delete_uninstall.go）。
	uninstallState := ""
	var uninstallWarnings []string
	if asBool(req["uninstall"], false) {
		uninstallState, uninstallWarnings = h.prepareNodeAgentUninstall(id)
	}
	// R2X-045：删除语义不变（尽力而为，N-05），但删除打断的跨节点引用
	// 清单随响应回传，不再静默。
	brokenEntries, soleRoleWarnings, err := h.deleteNodeByIDAfterGuards(id, guard)
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
	if soleRoleWarnings == nil {
		soleRoleWarnings = []string{}
	}
	deleteResp := map[string]interface{}{
		"brokenReferrers":     brokenReferrers,
		"brokenReferrerCount": len(brokenReferrers),
		// L4-03：唯一入口/出口点名（新增字段，向后兼容）。
		"soleRoleWarnings": soleRoleWarnings,
	}
	// 连带卸载三态回执（新增字段，仅请求带 uninstall 时出现，向后兼容）。
	if uninstallState != "" {
		deleteResp["uninstall"] = uninstallState
	}
	if len(uninstallWarnings) > 0 {
		deleteResp["uninstallWarnings"] = uninstallWarnings
	}
	response.WriteJSON(w, response.OK(deleteResp))
}

// panelRequestScheme 推断本次管理请求所见的面板实际服务方式
// （"http"/"https"）：直接对端命中本机代理链谓词（回环/私网/
// 链路本地，见 federation.go 的 isTrustedProxyIP）时采信
// X-Forwarded-Proto，其次连接自身 TLS。
//
// 信任口径明示（R2-02-01）：本面与 C4-01 起的登录限流口径
// （internal/clientip：仅回环＋FLMX_TRUSTED_PROXIES 显式名单）
// 不是同一口径，是刻意保留的独立面。本谓词的唯一生产消费是
// 生成节点安装命令时为裸面板地址补 ws/wss（见本文件
// normalizeNodeInstallAddr 的调用点）并据此附明文警告——消费
// 点经管理员鉴权、产物只是链接 scheme，不参与鉴权/限流/
// Cookie 属性/联邦信任等任何安全决策；且默认 Caddy→nginx
// 拓扑下后端对端即私网前端容器，若按登录口径收窄，XFP 恒不
// 被采信、HTTPS 面板的新装命令会被误补成 ws://。
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

// nodeInstallPlaintextWarning returns the explicit operator warning
// carried in the nodeInstall response envelope when the generated
// agent address is plaintext ws:// (R7-J-01, non-BC part): the node
// secret channel would run in plaintext, and a TLS-only panel (Caddy /
// nginx terminating HTTPS) can never be reached this way — the
// 2026-10-29 incident shape, which previously surfaced nowhere. The
// generated command string itself is unchanged; only the warning is
// new. wss:// (and anything else) yields no warning.
func nodeInstallPlaintextWarning(agentAddr string) string {
	if !strings.HasPrefix(agentAddr, "ws://") {
		return ""
	}
	return "警告：本次安装命令的节点连接地址为明文 " + agentAddr + "，节点密钥将经明文通道发送，且明文地址无法连接仅提供 HTTPS 的面板（如经 Caddy/nginx 反向代理终止 TLS）。如面板经 HTTPS 对外，请先在「网站配置」把面板后端地址改为 https:// 开头的地址再重新生成安装命令；确认该面板就是明文部署时可忽略本警告。"
}

// panelMirrorPrefixFromAgentAddr 从待安装节点的 agent 连接地址
// 派生面板源候选前缀（面板源分发一期③）：normalizeNodeInstallAddr
// 的逆映射——wss → https、ws → http，再拼面板 GitHub 镜像端点
// 路径（ghMirrorPath）。agent 地址恒带显式 scheme（归一化的产
// 物）；无 scheme 或派生不出时返回空串，安装命令不带面板候选、
// 与旧形态逐字一致。派生源是生成命令时已过 validatePanelInstallAddr
// 字符集校验的面板地址，与其余候选同样以引号形式进入命令。
func panelMirrorPrefixFromAgentAddr(agentAddr string) string {
	var base string
	switch {
	case strings.HasPrefix(agentAddr, "wss://"):
		base = "https://" + strings.TrimPrefix(agentAddr, "wss://")
	case strings.HasPrefix(agentAddr, "ws://"):
		base = "http://" + strings.TrimPrefix(agentAddr, "ws://")
	default:
		return ""
	}
	return base + strings.TrimSuffix(ghMirrorPath, "/")
}

// buildNodeInstallCommand 生成节点安装命令（1.7.7 KU5 重构）。
//
// 首跳（install.sh 本体的获取）不再依赖面板的 GitHub 加速配置——
// 该设置已整体移除，且命令在节点机上执行、面板无法替节点测速。
// 命令内置与全产品统一的候选清单（githubFetchProxyCandidates +
// 直连，github_routes.go），按「直连优先、其后代理清单原序」的顺
// 序逐个试取：每个候选先取 install.sh.sha256 并校验内容为 64 位
// 十六进制哈希（真实目标判活，防代理落地页假活），再从同一候选
// 取 install.sh 本体；首个两件齐备的候选即停（获胜候选记入 w），
// 随后过 F-060 的 sha256sum 比对（第一锚）与 R7-002 的跨线路复核
// （第二锚）才 chmod 执行。全部候选失败则整条命令失败（ok 恒为
// 0、test 中止），不执行任何已下载内容。
//
// 面板源一期③：候选表最前面 prepend 面板源（由 agentAddr 派生，
// 见 panelMirrorPrefixFromAgentAddr）——新节点此刻唯一已证可达
// 的目的地就是面板本身，国内机经面板拉 install.sh 不再先撞
// GitHub 直连；面板候选与其余候选受同一套双锚校验约束，镜
// 像回源失败时端点回 502、命令自动顺延下一候选，不劣化现状。
//
// 脚本本体内的二进制下载由 install.sh 内置测速直选负责（同候选
// 清单），命令不再注入 PROXY_ENABLED / PROXY_URL；运维显式传入
// 环境变量 PROXY_URL 时脚本仍将其作手动覆盖（见 install.sh）。
//
// F-060：生成的命令此前对将以 root 执行的 install.sh 本体零校验
// （curl -L 连 HTTP 错误都会把错误页存下来执行）。当时与全仓
// fail-closed 标准对齐：同下 release 的 install.sh.sha256（流水线
// 为每个脚本资产生成，内容为纯哈希），sha256sum 比对通过才 chmod
// 执行；任一步失败整条 && 链中止。
//
// R7-002（L8-F-02 同族残留收口）：F-060 的第一锚与被校验文件同源
// 同次取得，单个作恶候选即可给出自洽的（假脚本，假 .sha256）对
// ——这正是产品其余下载面已升级掉的旧信任模型（panel_install.sh
// 的 corroborate_download_anchor、面板自升级的
// corroborateReleaseAssetChecksum、agent 侧 corroborate 均为
// 「同源第一锚＋异路第二锚」）。生成命令内同步补第二锚，与四面
// 同语义：定版后从候选表内另一候选重取 install.sh.sha256，与落
// 盘本体的实际哈希比对——异路给出不同的合法哈希即硬中止（不
// 执行、不换路重试）；异路内容非合法哈希只废该路、继续问下一
// 路；全部异路都给不出合法校验值时按获胜候选分层（FIX-R2-9，
// 与脚本侧 R2-01-01 同律）：获胜者为直连或面板源（第一方）才
// 降级单锚继续、并在命令输出中点名「跨线路锚复核已降级」；第
// 三方代理获胜则 fail-closed——点名中止、删除已下载脚本、整
// 条命令失败（单个代理自给的自洽对不构成验证）。复核整轮共用 20s 总界、逐路
// --max-time 取剩余量与 10s 的较小者（与四面总界口径一致）。
// nodeSecretCharset 是节点 secret 的生成契约字符集（randomToken
// (16) 的 32 位小写 hex）。B3-01：备份恢复等旁路曾把任意字符串
// 写进 node.secret，而安装命令构造此前把 secret 无引号插值进
// 以 root 执行的 shell 命令——含空格/引号/命令替换的存量 secret
// 一键变命令注入。生成面双堵：nodeInstall 先以本字符集校验存
// 量 secret（非法点名拒绝，见 validateNodeSecretForInstall），
// 构造器再对全部插值走 shellQuoteForInstall 安全引用（纵深：
// 即便某值绕过校验，引用形态也保证它只是一个参数）。
var nodeSecretCharset = regexp.MustCompile(`^[0-9a-f]{32}$`)

func validateNodeSecretForInstall(secret string) error {
	if !nodeSecretCharset.MatchString(secret) {
		return fmt.Errorf("节点 secret 格式异常（应为 32 位小写十六进制），拒绝生成安装命令：该值可能经备份恢复等旁路写入，请先重置节点 secret")
	}
	return nil
}

// shellQuoteForInstall 把一个值安全地包成 POSIX sh 单引号参数：
// 内嵌单引号以 POSIX 标准序列转义，值中的空格/$/反引号/分号
// 在单引号内全部惰性化。
func shellQuoteForInstall(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func buildNodeInstallCommand(version, agentAddr, secret string) string {
	const verifyStep = ` && test "$(awk '{print $1}' ./install.sh.sha256)" = "$(sha256sum ./install.sh | awk '{print $1}')"`
	routes := make([]string, 0, len(githubFetchProxyCandidates)+2)
	panelPrefix := panelMirrorPrefixFromAgentAddr(agentAddr)
	if panelPrefix != "" {
		routes = append(routes, panelPrefix)
	}
	routes = append(routes, "")
	routes = append(routes, githubFetchProxyCandidates...)
	quoted := make([]string, 0, len(routes))
	for _, prefix := range routes {
		quoted = append(quoted, `"`+prefix+`"`)
	}
	routeList := strings.Join(quoted, " ")
	base := "https://github.com/" + githubRepo + "/releases/download/" + version
	// R7-002 第二锚（shell 段，语义见函数头注）：a = 落盘本体实际
	// 哈希（第一锚已证其等于获胜候选的校验值）；anchor 三态——
	// 0=尚无异路给出合法校验值、1=异路同值复核通过、2=异路异值。
	// 组末以 [ "$anchor" != 2 ] 收口：异值令整条 && 链中止。
	// FIX-R2-9：anchor 停在 0 时按获胜候选 $w 分层——$w 为空（直
	// 连）或等于 $pp（面板源，第一方）才点名降级继续；第三方代
	// 理获胜则点名中止、删已下载脚本并置 anchor=2 断链。
	corroborateStep := ` && { a=$(sha256sum ./install.sh | awk '{print $1}'); anchor=0; dl=$(( $(date +%s) + 20 )); ` +
		`for q in ` + routeList + `; do [ "$q" = "$w" ] && continue; r=$(( dl - $(date +%s) )); [ "$r" -gt 0 ] || break; m=10; [ "$r" -lt 10 ] && m=$r; ` +
		`curl -fsSL --connect-timeout 5 --max-time "$m" "${q:+$q/}` + base + `/install.sh.sha256" -o ./install.sh.sha256.alt 2>/dev/null || continue; ` +
		`g=$(awk '{print $1}' ./install.sh.sha256.alt | tr 'A-F' 'a-f'); case "$g" in ''|*[!0-9a-f]*) continue;; esac; [ "${#g}" = 64 ] || continue; ` +
		`if [ "$g" != "$a" ]; then echo "❌ 跨线路锚复核失败：install.sh 的校验值经两条不同线路获取的结果不一致，已中止安装（R7-002 第二锚）" >&2; anchor=2; else echo "✅ 跨线路锚复核通过（第二线路: ${q:-直连}）"; anchor=1; fi; break; done; ` +
		`rm -f ./install.sh.sha256.alt; if [ "$anchor" = 0 ]; then if [ -z "$w" ] || [ "$w" = "$pp" ]; then echo "⚠️ 跨线路锚复核已降级：其他线路均未给出校验值，按单锚继续（R7-002 第二锚）"; else echo "❌ 跨线路锚复核失败：获胜线路为代理（$w）且其他线路均未给出校验值，无法复核校验锚，已中止安装并删除已下载脚本（R7-002 第二锚，FIX-R2-9 分层）" >&2; rm -f ./install.sh ./install.sh.sha256; anchor=2; fi; fi; [ "$anchor" != 2 ]; }`
	return `ok=0; w=""; pp="` + panelPrefix + `"; for p in ` + routeList + `; do u="${p:+$p/}` + base + `"; ` +
		`if curl -fsSL --connect-timeout 5 --max-time 30 "$u/install.sh.sha256" -o ./install.sh.sha256 && grep -Eq '^[0-9a-f]{64}' ./install.sh.sha256 && curl -fsSL --connect-timeout 5 --max-time 60 "$u/install.sh" -o ./install.sh; then ok=1; w="$p"; break; fi; done; test "$ok" = 1` +
		verifyStep +
		corroborateStep +
		` && chmod +x ./install.sh && VERSION=` + version + ` ./install.sh -a ` + shellQuoteForInstall(agentAddr) + ` -s ` + shellQuoteForInstall(secret)
}

// ---------------------------------------------------------------------
// 节点容器安装形态（Docker / Docker Compose）
//
// 与脚本形态并列的可选安装方式：agent 镜像由发版流水线随面板镜像
// 一同构建发布（flmx-agent），安装命令把与脚本形态同格式的
// config.json 写到我方自有数据目录后，以 host 网络 + NET_ADMIN
// 拉起容器。资源命名：容器 flmx-agent、数据目录 /etc/flmx_agent、
// Compose 落位 /opt/docker/flmx-agent。与脚本形态的关系分两代：
// 对其 legacy 落位（/etc/flux_agent、flux_agent 服务）零共用；其
// 现行落位自全量改名后同以 /etc/flmx_agent 为安装目录基名（服务
// 名 flmx_agent），数据目录基名与本形态重合，两形态靠容器与系统
// 服务的运行面区分（脚本形态的存量迁移与双识别口径见 install.sh）。
// 其他面板的 agent 与本面板各形态仍互不接管、互不误删。
//
// 多面板同机时再进一步：本面板的实例键（环境变量 FLMX_INSTANCE，
// 由面板安装脚本写入后端运行环境）非空时，容器名、数据目录与
// Compose 落位统一加后缀 -<实例键>（见 agentContainerName 等派生
// 函数）——否则两个面板生成的命令会同名同目录，后装的覆写先装
// 的 config 并 rm 重建其容器、把节点劫持到自己名下。实例键为空
// （单面板默认）时派生结果与本节的历史字面量逐字一致。
const (
	nodeInstallModeScript        = "script"
	nodeInstallModeDocker        = "docker"
	nodeInstallModeDockerCompose = "docker-compose"

	agentImageName = "flmx-agent"
)

// 容器形态资源基名（无实例后缀的形态）；实际资源名由下方派生
// 函数按实例键拼接。
const (
	agentContainerBaseName = "flmx-agent"
	agentDataBaseDir       = "/etc/flmx_agent"
	agentComposeBaseDir    = "/opt/docker/flmx-agent"
)

// agentContainerDataDir 是数据卷在容器内的挂载点，恒为无后缀的
// 镜像锚点：Dockerfile 的 WORKDIR、entrypoint 的 cd 目标与 ENV
// FLMX_KERNEL_DIR 三处都钉在 /etc/flmx_agent，agent 的全部状态
// 根（config.json、gost.json、seq/spool、内核根、调优根、自卸载
// 锚）都由容器内工作目录与该 ENV 派生。实例后缀只落在宿主机一
// 侧：生成命令把宿主机数据目录（可带后缀）挂到这个固定锚点，
// 而不是同路径挂载——同路径挂载在后缀形态下会把卷挂到无人查看
// 的目录，agent 在镜像层的空目录里读不到 config.json 而崩溃循
// 环、状态根全落卷外（C-F2-01）。默认实例下宿主机目录与锚点
// 同值，生成命令与历史逐字一致。
const agentContainerDataDir = agentDataBaseDir

// panelInstanceEnv 是面板实例键的环境变量名：同机多面板部署时，
// 安装脚本为每个面板实例写不同的值进各自后端的运行环境。
const panelInstanceEnv = "FLMX_INSTANCE"

// panelInstanceID 返回本面板的实例键。口径：小写字母/数字开头，
// 其后可含小写字母、数字、连字符与下划线，全长 ≤32——该值将拼进
// 容器名与宿主机路径，必须先过白名单校验，杜绝路径穿越与命令
// 注入。未设置或不合规一律按默认实例（空）处理。
func panelInstanceID() string {
	id := strings.TrimSpace(os.Getenv(panelInstanceEnv))
	if !validPanelInstanceID(id) {
		return ""
	}
	return id
}

func validPanelInstanceID(id string) bool {
	if id == "" || len(id) > 32 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z' || c >= '0' && c <= '9':
		case (c == '-' || c == '_') && i > 0:
		default:
			return false
		}
	}
	return true
}

// agentInstanceSuffix 是容器形态资源的实例后缀：默认实例为空串
// （资源名与历史逐字一致），多面板实例为 "-<实例键>"。
func agentInstanceSuffix() string {
	if id := panelInstanceID(); id != "" {
		return "-" + id
	}
	return ""
}

func agentContainerName() string { return agentContainerBaseName + agentInstanceSuffix() }
func agentDataDir() string       { return agentDataBaseDir + agentInstanceSuffix() }
func agentComposeDir() string    { return agentComposeBaseDir + agentInstanceSuffix() }

// normalizeNodeInstallMode 归一安装方式；空值按脚本形态（与历史
// 请求完全一致）。未知值返回 ok=false，handler 拒绝生成命令。
func normalizeNodeInstallMode(mode string) (string, bool) {
	switch strings.TrimSpace(mode) {
	case "", nodeInstallModeScript:
		return nodeInstallModeScript, true
	case nodeInstallModeDocker:
		return nodeInstallModeDocker, true
	case nodeInstallModeDockerCompose:
		return nodeInstallModeDockerCompose, true
	}
	return "", false
}

// agentImageRef 是容器形态使用的 agent 镜像引用：与面板镜像同仓
// （ghcr.io/<owner>/flmx-agent:<version>），owner 取自 githubRepo。
func agentImageRef(version string) string {
	owner := githubRepo
	if i := strings.Index(owner, "/"); i >= 0 {
		owner = owner[:i]
	}
	return "ghcr.io/" + strings.ToLower(owner) + "/" + agentImageName + ":" + version
}

// agentConfigWriteStep 生成把 config.json 写入宿主机数据目录的命令
// 段：格式与 install.sh write_flmx_agent_config 的默认形态一致
// （addr/secret 两字段），umask 077 + chmod 600 保证 secret 不经
// 宽松权限落盘。addr 在 handler 侧已过格式校验、secret 为库内
// 取值，与脚本形态同一插值口径。
//
// 同一步播种 gost.json（与 install.sh 的初始 {} 对齐）：agent 的
// 配置加载要求 gost.json 存在，缺失即 Fatal 退出——脚本形态靠
// install.sh 播种，容器形态若无人播种则新节点容器无限重启、永
// 不上线。已存在则绝不覆盖：gost.json 承载节点运行状态，而重跑
// 本命令正是升级/重装路径。镜像 entrypoint 另有缺失播种兜底，
// 两面同口径。
func agentConfigWriteStep(agentAddr, secret string) string {
	dataDir := agentDataDir()
	return `mkdir -p ` + dataDir +
		` && (umask 077; printf '{\n  "addr": "%s",\n  "secret": "%s"\n}\n' ` + shellQuoteForInstall(agentAddr) + ` ` + shellQuoteForInstall(secret) + ` > ` + dataDir + `/config.json)` +
		` && chmod 600 ` + dataDir + `/config.json` +
		` && { [ -f ` + dataDir + `/gost.json ] || (umask 077; printf '{}\n' > ` + dataDir + `/gost.json); }`
}

// buildNodeDockerInstallCommand 生成 Docker 形态安装命令：写配置 →
// 移除同名旧容器（仅我方容器名，重跑即重建，兼作升级/重装路径）→
// docker run。privileged 时附加 --privileged（服务器调优需要：
// sysctl 写入与 modprobe 在非特权容器内不可用）。
func buildNodeDockerInstallCommand(version, agentAddr, secret string, privileged bool) string {
	priv := ""
	if privileged {
		priv = " --privileged"
	}
	containerName := agentContainerName()
	dataDir := agentDataDir()
	return agentConfigWriteStep(agentAddr, secret) +
		` && { docker rm -f ` + containerName + ` >/dev/null 2>&1 || true; }` +
		` && docker run -d --name ` + containerName +
		` --network host --cap-add NET_ADMIN` + priv +
		` --restart unless-stopped` +
		` -v ` + dataDir + `:` + agentContainerDataDir +
		` ` + agentImageRef(version)
}

// buildNodeDockerComposeInstallCommand 生成 Compose 形态安装命令：
// 写配置 → 把 compose 文件写到固定落位（文件内无 secret，secret 只
// 在数据目录的 config.json 中）→ docker compose up -d。改调优开关
// 时重新获取命令重跑即可，只重写这一个 compose 文件。
func buildNodeDockerComposeInstallCommand(version, agentAddr, secret string, privileged bool) string {
	priv := ""
	if privileged {
		priv = "\n    privileged: true"
	}
	containerName := agentContainerName()
	dataDir := agentDataDir()
	composeDir := agentComposeDir()
	compose := `services:
  ` + containerName + `:
    image: ` + agentImageRef(version) + `
    container_name: ` + containerName + `
    network_mode: host
    cap_add:
      - NET_ADMIN` + priv + `
    restart: unless-stopped
    volumes:
      - ` + dataDir + `:` + agentContainerDataDir + `
`
	return agentConfigWriteStep(agentAddr, secret) +
		` && mkdir -p ` + composeDir +
		` && cat > ` + composeDir + `/docker-compose.yaml <<'FLMX_AGENT_COMPOSE_EOF' && docker compose -f ` + composeDir + `/docker-compose.yaml up -d
` + compose + `FLMX_AGENT_COMPOSE_EOF
`
}

func (h *Handler) nodeInstall(w http.ResponseWriter, r *http.Request) {
	// C059：版本发现两段链（API 60s + 重定向兜底 60s）后端界 120s，
	// 此前独缺 extendWriteDeadline 而与全局 WriteTimeout 120s 恰等，
	// 且被登记表误分类为 DB/local（class）+ discovery-prelude-legacy
	// 命名例外逃过三层对账——与 C080（/system/version）同型，现按
	// 同法收口：武装放宽、改登 strict、摘除例外。
	extendWriteDeadline(w, nodeInstallWriteDeadline)
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}

	var req struct {
		ID      int64  `json:"id"`
		Channel string `json:"channel"`
		// Mode 选择安装形态：空/script 为脚本形态（历史行为不变），
		// docker / docker-compose 为容器形态；Privileged 仅容器形态
		// 消费（服务器调优需要特权容器）。
		Mode       string `json:"mode"`
		Privileged bool   `json:"privileged"`
	}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	if req.ID <= 0 {
		response.WriteJSON(w, response.ErrDefault("参数错误"))
		return
	}
	mode, modeOK := normalizeNodeInstallMode(req.Mode)
	if !modeOK {
		response.WriteJSON(w, response.ErrDefault("安装方式无效"))
		return
	}

	channel := normalizeReleaseChannel(req.Channel)
	version, err := resolveLatestReleaseByChannel(channel)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, fmt.Sprintf("获取最新%s失败: %v", releaseChannelLabel(channel), err)))
		return
	}

	// C-01（纵深第二道）：version 随后无引号拼入以 root 执行的安装
	// 命令（VERSION=%s），与 nodeUpgrade/nodeBatchUpgrade 同口径
	// 严格校验；第一道在 fetchLatestTagByRedirect。
	if !isValidAgentVersion(version) {
		response.WriteJSON(w, response.ErrDefault("版本号格式无效"))
		return
	}

	// 面板源二期：异步预热本版本安装资产到镜像缓存（纯后台、零
	// 等待，不改变本 handler 的时间界，登记表 C059 注已登记）；
	// 命令生成与节点机执行之间隔着人工粘贴执行的时间差，预热
	// 届时已落地。失败只记日志，命令的候选回退不依赖它。
	preheatInstallReleaseAssets(version)

	secret, err := h.repo.GetNodeSecret(req.ID)
	if err != nil {
		response.WriteJSON(w, response.ErrDefault("节点不存在"))
		return
	}
	// B3-01：存量 secret 先过字符集门再进命令构造（非法点名拒
	// 绝，不让旁路写进的任意字符串抵达 root shell）。
	if err := validateNodeSecretForInstall(secret); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
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
	// FIX2-C #2：节点 addr 归一为显式 scheme（见 normalizeNodeInstallAddr）。
	agentAddr := normalizeNodeInstallAddr(panelAddr, panelRequestScheme(r))
	var cmd string
	switch mode {
	case nodeInstallModeDocker:
		cmd = buildNodeDockerInstallCommand(version, agentAddr, secret, req.Privileged)
	case nodeInstallModeDockerCompose:
		cmd = buildNodeDockerComposeInstallCommand(version, agentAddr, secret, req.Privileged)
	default:
		cmd = buildNodeInstallCommand(version, agentAddr, secret)
	}
	// R7-J-01 (non-BC part): a plaintext ws:// result must not leave
	// silently — carry the explicit warning in the envelope. The
	// command string above is generated exactly as before.
	if warning := nodeInstallPlaintextWarning(agentAddr); warning != "" {
		response.WriteJSON(w, response.OKWithWarning(cmd, warning))
		return
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
	// C1-04：逐条 UPDATE＋逐条回执的批量面，条目数同口径计数帽
	// （见 maxBatchIDs；本面不去重，按提交条数计）。
	if !checkBatchIDsCap(w, len(req.Nodes)) {
		return
	}
	// B04-002（handler 半）：逐条聚合回执，与 nodeBatchDelete /
	// tunnelUpdateOrder 同口径。repo 的 UpdateNodeOrder 已改返回
	// (RowsAffected, error)（C04 立契约），此处消费之：0 行=节点不
	// 存在、err=写库失败，均计 failures 并带 id+原因；部分失败不再
	// 报全量成功。此前 handler 丢弃返回值恒 OKEmpty，前端只能按提交
	// 数虚报成功、排错位刷新后才暴露。
	success := 0
	fail := 0
	failures := make([]batchFailureDetail, 0)
	now := time.Now().UnixMilli()
	for _, n := range req.Nodes {
		rows, err := h.repo.UpdateNodeOrder(n.ID, n.Inx, now)
		if err != nil {
			fail++
			failures = appendBatchFailureReason(failures, n.ID, "", "更新节点排序失败："+err.Error())
			continue
		}
		if rows == 0 {
			fail++
			failures = appendBatchFailureReason(failures, n.ID, "", "节点不存在")
			continue
		}
		success++
	}
	response.WriteJSON(w, response.OK(batchOperationResult{SuccessCount: success, FailCount: fail, Failures: failures}))
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
	// 请求体在此一次解码（与 idsFromBody 同口径：ids 为空报错、过滤
	// <=0、升序），另读 uninstall 标记供连带卸载步使用。
	var batchReq map[string]interface{}
	if err := decodeJSON(r.Body, &batchReq); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	rawIDs := asAnySlice(batchReq["ids"])
	if len(rawIDs) == 0 {
		response.WriteJSON(w, response.ErrDefault("ids不能为空"))
		return
	}
	ids := make([]int64, 0, len(rawIDs))
	for _, x := range rawIDs {
		if id := asInt64(x, 0); id > 0 {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	// R2-040（B-node-05）：去重——同一 ID 第二次迭代时节点已删，
	// 会被记成「节点不存在」假失败，与同批成功计数自相矛盾。
	ids = uniqueInt64s(ids)
	// R10-08：本面自有解析不走 idsFromBody，去重后同口径计数帽
	// （见 maxBatchIDs）。
	if !checkBatchIDsCap(w, len(ids)) {
		return
	}
	uninstall := asBool(batchReq["uninstall"], false)
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
	// L4-03：唯一入口/出口点名同 funnel 汇总（同文案只报一次）。
	var soleRoleWarnings []string
	seenSoleRole := make(map[string]struct{})
	// 连带卸载三态聚合（仅请求带 uninstall 时填充）。
	uninstalledCount, uninstallPendingCount, uninstallUnsupportedCount := 0, 0, 0
	var uninstallWarnings []string
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
		// 守卫校验先行（与单删同一三段口径）：守卫失败即记失败并
		// 跳过该节点——不得先执行连带卸载、再发现删不掉。
		guard, guardErr := h.checkNodeDeleteGuards(id)
		if guardErr != nil {
			fail++
			failures = appendBatchFailure(failures, id, nodeName, guardErr)
			continue
		}
		// R4-F2-02：同单删入口的兜底释放（循环内 defer 安全：正常
		// 路径级联段已释放并清标、defer 恒 no-op；仅连带步 panic
		// 展开时对未清标的本节点生效）。
		defer guard.releaseUpgradeOp(h, id)
		// 连带卸载步（同单删：赶在删记录前下发/登记，失败只进
		// warnings 不中止删除）。三态先暂存，删除成功后才计入聚合
		// 计数（见级联成功后的 switch）。
		var uninstallState string
		if uninstall {
			state, warns := h.prepareNodeAgentUninstall(id)
			uninstallState = state
			uninstallWarnings = append(uninstallWarnings, warns...)
		}
		nodeReferrers, nodeSoleWarnings, delErr := h.deleteNodeByIDAfterGuards(id, guard)
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
		// 卸载计数在删除成功后才累加：旧实现删除前累加，删除失败
		// 的节点会同时计入 fail 与卸载计数，回执自相矛盾。
		switch uninstallState {
		case nodeUninstallDone:
			uninstalledCount++
		case nodeUninstallPending:
			uninstallPendingCount++
		case nodeUninstallUnsupported:
			uninstallUnsupportedCount++
		}
		for _, referrer := range nodeReferrers {
			if _, dup := seenReferrer[referrer.ID]; dup {
				continue
			}
			seenReferrer[referrer.ID] = struct{}{}
			brokenReferrers = append(brokenReferrers, referrer.Display)
		}
		for _, warning := range nodeSoleWarnings {
			if _, dup := seenSoleRole[warning]; dup {
				continue
			}
			seenSoleRole[warning] = struct{}{}
			soleRoleWarnings = append(soleRoleWarnings, warning)
		}
		success++
	}
	response.WriteJSON(w, response.OK(batchOperationResult{SuccessCount: success, FailCount: fail, Failures: failures, BrokenReferrers: brokenReferrers, SoleRoleWarnings: soleRoleWarnings, UninstalledCount: uninstalledCount, UninstallPendingCount: uninstallPendingCount, UninstallUnsupportedCount: uninstallUnsupportedCount, UninstallWarnings: uninstallWarnings}))
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
	// R8-T-07 (T-GATE completion): same composite legs as tunnel
	// update; without relief the global 120s write cap would exactly
	// equal the frontend budget raised by batch T2.
	extendWriteDeadline(w, tunnelCreateWriteDeadline)
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
	// R2 K1 枚举复扫（B03-001 同族）：名称按列宽 varchar(100) 校验，
	// 与分组/限速规则同口径——此前无门，PG 吐原始错误、SQLite 静默存入。
	if len([]rune(name)) > 100 {
		response.WriteJSON(w, response.ErrDefault("隧道名称长度不能超过100个字符"))
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
	// B06-001（Round 2 K9）：Type ∈ {1,2}、Status ∈ {0,1} 同口径立门
	// （此前两字段无合法集校验、域外值原样落库回吐）。
	if err := validateTunnelType(typeVal); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	if err := validateTunnelStatus(status); err != nil {
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
				// R35-FIX-F02：stripShareHostPort 剥不动无括号 IPv6
				// host:port（"2001:db8::1:8080"），须用 stripHostPortOnce
				//（PC-03 读法）；剥离后仍带端口/畸形者直接拒绝，
				// 不把畸形地址发给对端面板。
				fedHost := stripHostPortOnce(outNodeRec.ServerIP)
				if fedHost != "" && (!isBareDialHost(fedHost) || stripHostPortOnce(fedHost) != fedHost) {
					validationTx.Rollback()
					response.WriteJSON(w, response.ErrDefault("出站节点地址不正确"))
					return
				}
				targetAddr = processServerAddress(fedHost)
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
		// R8-T-06: CreateTunnel is a runtime face; see
		// federationRuntimeClientTimeout.
		fc := client.NewFederationClientWithTimeout(federationRuntimeClientTimeout)
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
	// R8-T-07: same composite legs as tunnel create; the global
	// 120s write cap was an exact-equality bound here.
	extendWriteDeadline(w, tunnelUpdateWriteDeadline)
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
	// B06-001（Round 2 K9）：Type ∈ {1,2}、Status ∈ {0,1} 同口径立门，
	// presence 语义与 flow/trafficRatio 一致（仅校验在场非空字段，
	// 缺席字段的存量回填值不连坐）。
	if raw, ok := req["type"]; ok && asString(raw) != "" {
		if err := validateTunnelType(asInt(raw, 1)); err != nil {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
	}
	if raw, ok := req["status"]; ok && asString(raw) != "" {
		if err := validateTunnelStatus(asInt(raw, 1)); err != nil {
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
	// R2 K1 枚举复扫（B03-001 同族）：显式提交的改名按列宽 100 校验
	// （presence 口径，未提交的存量名不连坐）。
	if _, ok := req["name"]; ok && len([]rune(resolvedName)) > 100 {
		response.WriteJSON(w, response.ErrDefault("隧道名称长度不能超过100个字符"))
		return
	}
	// B06-001：改名补与 create 同口径的重名校验——create 有 handler
	// 预检 + InsertTunnelTx 事务内复检，update 此前全程无校验且
	// tunnel 表无唯一索引，可改出同名隧道。仅显式提交且与现名不同
	// 才查（同名即自身不算重复）；提交段落库前再复检一次（见下方
	// 提交段），把竞态窗口收敛到与 create 同级。
	currentTunnelName, _ := h.repo.GetTunnelName(id)
	_, tunnelNameSubmitted := req["name"]
	tunnelRenamed := tunnelNameSubmitted && resolvedName != currentTunnelName
	if tunnelRenamed {
		nameDup, dupErr := h.repo.TunnelNameExists(resolvedName)
		if dupErr != nil {
			response.WriteJSON(w, response.Err(-2, dupErr.Error()))
			return
		}
		if nameDup {
			response.WriteJSON(w, response.ErrDefault("隧道名称重复"))
			return
		}
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
		// R2-F4-01：切入 realm 是 A4-004 策略门的第四写面——隧道下既有
		// 转发逐条过门，命中点名整单拒绝（本段未落库）。realm→gost 与
		// 引擎未变的编辑不查：gost 全策略可渲染；已在 realm 的转发入仓
		// 时已过其余三面。switchTunnelEngine 入口另有同谓词兜底。
		if engine == tunnelEngineRealm && oldTunnel.Engine != tunnelEngineRealm {
			switchForwards, err := h.repo.ListForwardsByTunnelTx(validationTx, id)
			if err != nil {
				return err, -2
			}
			if err := validateRealmEngineSwitchForwards(switchForwards); err != nil {
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
	// P01-002：验证段 prepare 若把非入口段口换掉（现口被占/越界），
	// 换口事实必须随回执显形——与 relayResyncWarnings 同通道进
	// data.warnings（前端已有逐条 toast），不再静默漂移。
	var portChangeWarnings []string
	if runtimeState != nil {
		for _, ch := range runtimeState.PortChanges {
			nodeName := ""
			if n := runtimeState.Nodes[ch.NodeID]; n != nil {
				nodeName = n.Name
			}
			segment := "出口"
			if ch.ChainType == 2 {
				segment = "链路"
			}
			if ch.RoleChanged {
				// F2-T1-02：成因是段角色变更（原段口不跨段沿用，
				// 与占用/范围无关）——按真实成因点名，不再沿用
				// 「已被占用或不在范围内」的错误后缀误导排查。
				fromSegment := "出口"
				if ch.FromChainType == 2 {
					fromSegment = "链路"
				}
				portChangeWarnings = append(portChangeWarnings, fmt.Sprintf("节点 %s 由%s段改任%s段，端口由 %d 变更为 %d（改任另一段不沿用原段口）", nodeName, fromSegment, segment, ch.OldPort, ch.NewPort))
				continue
			}
			portChangeWarnings = append(portChangeWarnings, fmt.Sprintf("节点 %s %s端口由 %d 变更为 %d（原端口已被占用或不在节点端口范围内）", nodeName, segment, ch.OldPort, ch.NewPort))
		}
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
		// L9 观察 1：type-1 游离段口不在 OutNodes/ChainHops 里，从
		// SegmentPorts 补入提交段复检（与验证段同口径，堵两段之间
		// 被并发占用的窗口；重复条目排除本隧道后恒通过，无副作用）。
		for key, port := range runtimeState.SegmentPorts {
			finalPortNodes = append(finalPortNodes, tunnelRuntimeNode{NodeID: key.nodeID, Port: port})
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

		// B06-001：提交段复检改名重名（与 create 的 InsertTunnelTx
		// 事务内复检同形，防验证段之后并发改入同名）。F1：复检必须
		// 走本事务句柄（TunnelNameExistsTx）——池版走 r.db 另取连接，
		// SQLite 单连接下事务占住唯一连接会自死锁。
		// X01-001：与 InsertTunnelTx 同族——PG 下两并发改名事务的
		// Count 复检可双双读到 0，复检前先按目标名取事务级咨询锁
		// 串行化（LockTunnelNameTx，SQLite 下为空操作）。
		if tunnelRenamed {
			if lockErr := h.repo.LockTunnelNameTx(tx, resolvedName); lockErr != nil {
				return lockErr, -2
			}
			nameDup, dupErr := h.repo.TunnelNameExistsTx(tx, resolvedName)
			if dupErr != nil {
				return dupErr, -2
			}
			if nameDup {
				return errors.New("隧道名称重复"), 0
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
	// R8-019：入口转发端口同步（下方入口集变更分支执行）的失败与
	// 其他后置失败同口径进回执 warnings，不静默。此闭包只在同步
	// 执行之后被调用，读取的是届时值。
	var entryPortSyncWarnings []string
	// B4-03：被移除入口节点的转发服务清理失败同口径进 warnings，
	// 闭包在同步执行之后调用、读取届时值（与 entryPortSyncWarnings
	// 同形态）。
	var entryCleanupWarnings []string
	respondTunnelUpdateOK := func() {
		warnings := append(append([]string{}, portChangeWarnings...), relayResyncWarnings...)
		warnings = append(warnings, entryPortSyncWarnings...)
		warnings = append(warnings, entryCleanupWarnings...)
		if len(warnings) > 0 {
			response.WriteJSON(w, response.R{Code: 0, Msg: "已保存，但" + strings.Join(warnings, "；"), TS: now, Data: map[string]interface{}{"warnings": warnings}})
			return
		}
		response.WriteJSON(w, response.OKEmpty())
	}

	newEntryNodeIDs, _ := h.tunnelEntryNodeIDs(id)
	if !sameInt64Set(oldEntryNodeIDs, newEntryNodeIDs) {
		entryCleanupWarnings = h.cleanupTunnelForwardRuntimesOnRemovedEntryNodes(id, oldEntryNodeIDs, newEntryNodeIDs)
		entryPortSyncWarnings = h.syncTunnelForwardsEntryPorts(id, newEntryNodeIDs)
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

// cleanupTunnelForwardRuntimesOnRemovedEntryNodes 清理入口集变更
// 后被移除节点上的转发运行时。B4-03：此前返回 void 且逐层吞错
// ——listForwardsByTunnel 失败、nft reconcile 失败、gost 批量删
// 除失败全部静默，而 tunnelUpdate 回执恒成功，残留转发服务在
// 旧入口节点上继续监听旧端口（转发已改走新入口、无人再对账该
// 节点）。现失败逐项汇成 warnings 由调用方随回执带出（与
// syncTunnelForwardsEntryPorts 同口径）；当时清不掉的由重连对
// 账的成员判定补扫（deleteOrphanedForwardService 的 B4-03 扩
// 展：转发仍存在但节点已不在其隧道入口集时同样删除），不永久
// 残留。
func (h *Handler) cleanupTunnelForwardRuntimesOnRemovedEntryNodes(tunnelID int64, oldEntryNodeIDs, newEntryNodeIDs []int64) []string {
	if h == nil || h.repo == nil || tunnelID <= 0 {
		return nil
	}

	removedNodeIDs := diffInt64s(oldEntryNodeIDs, newEntryNodeIDs)
	if len(removedNodeIDs) == 0 {
		return nil
	}

	forwards, err := h.listForwardsByTunnel(tunnelID)
	if err != nil {
		return []string{fmt.Sprintf("入口节点变更后清理旧节点转发服务失败：读取隧道转发列表出错（%v），残留服务将在节点重连对账时清除", err)}
	}
	if len(forwards) == 0 {
		return nil
	}

	// S-06: 检查是否为 nftables 模式
	nftMode, _, _ := h.tunnelUsesNftables(tunnelID)

	var warnings []string
	for i := range forwards {
		f := &forwards[i]
		if f == nil {
			continue
		}
		for _, nodeID := range removedNodeIDs {
			if nftMode {
				// S-06: nftables 模式下，对旧节点做 reconcile（声明式同步会移除多余规则）
				// 若节点离线，reconcile 会返回错误：点名进回执
				// warnings（B4-03），重连后由对账补扫，不静默。
				if err := h.reconcileNftablesNodeByRequest(nodeID); err != nil {
					warnings = append(warnings, fmt.Sprintf("节点 %d 的 nftables 规则清理失败（%v），将在节点重连对账时补扫", nodeID, err))
					continue
				}
			} else {
				// gost 模式：删除 service（离线错误由 S-04 机制在
				// 命令面处理）；realm 隧道走 Batch 变体的重渲染
				// 清理。失败点名进 warnings（B4-03）。
				if err := h.deleteForwardServicesOnNodeBatch(f, nodeID); err != nil {
					warnings = append(warnings, fmt.Sprintf("节点 %d 上转发 %d 的旧服务清理失败（%v），将在节点重连对账时补扫", nodeID, f.ID, err))
				}
			}
		}
	}
	return warnings
}

// forwardPortConflictMessage 按统一四源谓词给出的占用来源措辞
// （L9 观察 2）：入站/链路段/联邦预留占用不得误报为「已被其他转发
// 占用」——归因错误会让排障找错对象。conflict 为 nil（来源不可知）
// 时退回不点名的「已被占用」。
func forwardPortConflictMessage(node *nodeRecord, port int, conflict *repo.ForwardPortConflict) string {
	if conflict != nil {
		name := strings.TrimSpace(conflict.Name)
		switch conflict.Source {
		case repo.PortSourceInbound:
			if name != "" {
				return fmt.Sprintf("节点 %s 端口 %d 已被入站 %s 占用", node.Name, port, name)
			}
			return fmt.Sprintf("节点 %s 端口 %d 已被入站占用", node.Name, port)
		case repo.PortSourceChain:
			if name != "" {
				return fmt.Sprintf("节点 %s 端口 %d 已被隧道 %s 的链路段占用", node.Name, port, name)
			}
			return fmt.Sprintf("节点 %s 端口 %d 已被隧道链路段占用", node.Name, port)
		case repo.PortSourcePeer:
			return fmt.Sprintf("节点 %s 端口 %d 已被联邦共享预留占用", node.Name, port)
		}
	}
	return fmt.Sprintf("节点 %s 端口 %d 已被其他转发占用", node.Name, port)
}

func (h *Handler) validateForwardPortAvailabilityTx(tx *gorm.DB, node *nodeRecord, port int, currentForwardID int64) error {
	if h == nil || h.repo == nil || tx == nil || node == nil || port <= 0 {
		return nil
	}
	conflict, err := h.repo.FindOtherForwardOnNodePortTx(tx, node.ID, port, currentForwardID)
	if err != nil {
		return err
	}
	if conflict == nil {
		return nil
	}
	if conflict.Source == repo.PortSourceForward && conflict.Status == model.ForwardStatusPendingDelete {
		return fmt.Errorf("节点 %s 端口 %d 正等待节点清理（转发 %s），请稍后重试或联系管理员",
			node.Name, port, conflict.Name)
	}
	return errors.New(forwardPortConflictMessage(node, port, conflict))
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

// syncTunnelForwardsEntryPorts re-syncs the tunnel's forwards onto the
// (new) entry node set after a tunnel update commits. It returns one
// warning per forward whose sync failed (R8-019): the caller folds them
// into the update receipt so a "saved" response never silently hides a
// forward that has no listener on the new entry set — the same
// non-silent rule the other post-commit steps in tunnelUpdate follow.
func (h *Handler) syncTunnelForwardsEntryPorts(tunnelID int64, entryNodeIDs []int64) []string {
	if h == nil || h.repo == nil || tunnelID <= 0 {
		return nil
	}
	entryNodeIDs = uniqueInt64s(entryNodeIDs)
	if len(entryNodeIDs) == 0 {
		return nil
	}

	forwards, err := h.listForwardsByTunnel(tunnelID)
	if err != nil {
		log.Printf("sync tunnel forwards entry ports failed tunnel_id=%d stage=list forwards err=%v", tunnelID, err)
		return []string{fmt.Sprintf("隧道名下转发入口端口同步失败：%v", err)}
	}
	if len(forwards) == 0 {
		return nil
	}
	var warnings []string

	allowInIP := len(entryNodeIDs) == 1
	for i := range forwards {
		f := &forwards[i]
		if f == nil {
			continue
		}
		oldPorts, err := h.listForwardPorts(f.ID)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("转发 %s 入口端口同步失败：%v", f.Name, err))
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
		// R8-019：留痕之外还要让管理员在更新回执里看见——日志加返回值
		// warnings 双通道（回执组装见 tunnelUpdate 的
		// entryPortSyncWarnings），「成功回执 ≠ 已同步」就此消除。
		if err := h.repo.ReplaceForwardPorts(f.ID, entries); err != nil {
			log.Printf("sync tunnel forwards entry ports failed tunnel_id=%d forward_id=%d err=%v", tunnelID, f.ID, err)
			warnings = append(warnings, fmt.Sprintf("转发 %s 入口端口同步失败：%v", f.Name, err))
		}
	}
	return warnings
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

// 隧道删除的两种回执口径（B06-003 口径登记，勿再当不一致立案）：
//   - 单删（本函数 tunnel/delete）：对不存在的隧道返回 code=0 幂等
//     成功——这是 X-P3-28 的既定语义（单删清单 user/forward/inbound/
//     subscription + 对齐原点 node/tunnel/speedLimit 一律如此，由
//     TestR1B8_XP328 锁定），目的是重复删除/删后重试不报错。
//   - 复合操作（delete-with-forwards、delete-preview、
//     batch-delete、batch-delete-preview）：逐条给出存在性回执
//     （不存在即报错或计入 failures），因为这类请求携带迁移/预览
//     等附加语义，调用方需要逐条对账。
//
// 两者并存是有意设计：单删只承诺「删完即无此隧道」，复合操作承诺
// 「逐条可对账」。用户侧说明见 doc/guide/relay.md「删除隧道与节点
// 离线时的语义」。
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
	// R8-T-05: outer write layer rises to ~150s; the 120s request
	// ctx below stays the designed work cap.
	extendWriteDeadline(w, diagnosisWriteDeadline)
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
	// C1-04：逐条 UPDATE＋逐条回执的批量面，条目数同口径计数帽
	// （见 maxBatchIDs；本面不去重，按提交条数计）。
	if !checkBatchIDsCap(w, len(req.Tunnels)) {
		return
	}
	// B06-004：逐条回执（batchOperationResult 口径，与 tunnel
	// batch-delete 一致）——不存在的 id（0 行更新）与真实 DB 错误
	// 计入 failures，不再无条件报全成功。repo.UpdateTunnelOrder 已
	// 改为返回受影响行数与错误，返回值不得丢弃。
	success := 0
	fail := 0
	failures := make([]batchFailureDetail, 0)
	now := time.Now().UnixMilli()
	for _, t := range req.Tunnels {
		rows, err := h.repo.UpdateTunnelOrder(t.ID, t.Inx, now)
		if err != nil {
			fail++
			name, _ := h.repo.GetTunnelName(t.ID)
			failures = appendBatchFailure(failures, t.ID, name, err)
			continue
		}
		if rows == 0 {
			fail++
			failures = appendBatchFailureReason(failures, t.ID, "", "隧道不存在")
			continue
		}
		success++
	}
	response.WriteJSON(w, response.OK(batchOperationResult{SuccessCount: success, FailCount: fail, Failures: failures}))
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
	// L4-03：批量删节点的「唯一入口/出口」点名清单（与单删的
	// soleRoleWarnings 同名字段/同口径）；仅删节点批量入口填充。
	SoleRoleWarnings []string `json:"soleRoleWarnings,omitempty"`
	// 删节点连带卸载 agent 的三态聚合计数与告警（与单删的 uninstall /
	// uninstallWarnings 同口径）；仅批量删节点入口在请求带 uninstall
	// 时填充，omitempty 向后兼容。
	UninstalledCount          int      `json:"uninstalledCount,omitempty"`
	UninstallPendingCount     int      `json:"uninstallPendingCount,omitempty"`
	UninstallUnsupportedCount int      `json:"uninstallUnsupportedCount,omitempty"`
	UninstallWarnings         []string `json:"uninstallWarnings,omitempty"`
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
	if !requirePost(w, r) {
		return
	}
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
	if !requirePost(w, r) {
		return
	}
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
	// R10-08：本面逐条 upsert＋逐条同步（与 idsFromBody 族同为逐条
	// 工作面），条目数同口径计数帽（见 maxBatchIDs；本面不去重，
	// 按提交条数计）。
	if !checkBatchIDsCap(w, len(req.Tunnels)) {
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
	if !requirePost(w, r) {
		return
	}
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
		// R2 B09-001：GB 键 flow 补 FIX2-A 同族守卫——在场但不可解析
		// （"abc"/1.5）时 fail-closed 报错。parseTrafficLimit 的 GB 路径
		// 经 asInt64 解析失败静默回 0，而 flow=0 是「不限额」语义，
		// 缺守卫等于把配额无声取消且回执成功（flowMiB 键已有
		// parseTrafficLimit 内部 ParseInt 校验，不在此列）。
		if presentNumericFieldInvalid(req, "flow") {
			response.WriteJSON(w, response.ErrDefault("flow 参数无效"))
			return
		}
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
	// R7-G-02：status / flowResetTime 合法集校验，与 user 面同口径
	// （O5-007/O5-008，校验对象为 presence 回填后的合并值，与
	// userUpdate / groupUpdate 的既有口径一致）——越界直拒不落库，
	// 不再出现 user 面拒绝、user_tunnel 面落库的口径分裂。
	if status != 0 && status != 1 {
		response.WriteJSON(w, response.ErrDefault("状态值无效，仅允许 0 或 1"))
		return
	}
	if flowResetTime < 0 || flowResetTime > 31 {
		response.WriteJSON(w, response.ErrDefault("流量重置日无效，仅允许 0-31（0 表示不自动重置）"))
		return
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
	if !requirePost(w, r) {
		return
	}
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
		// RR4-C1-01：forward 族为双受众面——助手业务文案（权限/
		// 配额/校验）原样回，仓储错误（repoError 标记）收口为通用
		// 文案；本函数同族各点同口径，下文不再逐点注。
		if respondInternalIfRepoError(w, "forwardCreate: ensureTunnelPermission failed", err) {
			return
		}
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
	// F2-05：预检→提交全程持 per-user 配额执法锁（与用户配额执
	// 法面共用同一把锁），串行化同用户的创建/恢复/执法交错；提
	// 交段另有 CreateForwardTx 事务内复检兜底（权威面）。锁在
	// CreateForwardTx 返回后立即释放（其后是数据面同步，不再
	// 涉及条数判定），defer 仅作早退兜底。
	unlockQuota := h.lockUserQuotaEnforcement(userID)
	quotaReleased := false
	releaseQuota := func() {
		if !quotaReleased {
			quotaReleased = true
			unlockQuota()
		}
	}
	defer releaseQuota()
	if err := h.ensureUserTunnelForwardAllowed(userID, tunnelID, time.Now().UnixMilli()); err != nil {
		if respondInternalIfRepoError(w, "forwardCreate: ensureUserTunnelForwardAllowed failed", err) {
			return
		}
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	name := asString(req["name"])
	remoteAddr := asString(req["remoteAddr"])
	if name == "" || remoteAddr == "" {
		response.WriteJSON(w, response.ErrDefault("转发名称和目标地址不能为空"))
		return
	}
	// R2 K1 枚举复扫（B03-001 同族）：名称按列宽 varchar(100) 校验。
	if len([]rune(name)) > 100 {
		response.WriteJSON(w, response.ErrDefault("转发名称长度不能超过100个字符"))
		return
	}
	// 终审 L1-F-06：管理员面同样补语法层空标签判——只判语法、不做 DNS；
	// 不调 IsSafeRemoteAddr，避免改变管理员转发到内网地址的行为。
	if err := validateNoEmptyLabelAddr(remoteAddr); err != nil {
		response.WriteJSON(w, response.ErrDefault("转发目标地址主机名格式无效："+err.Error()))
		return
	}
	// R7-04（G-F3-01）：零有效目标（纯分隔符/纯空白，段集为空）属
	// 格式无效，与 netguard 豁免无关——不绑进下方 roleID/开关的段
	// 校验条件，管理员面与开关开启时同样在此拒绝。
	if err := validateRemoteAddrHasTargets(remoteAddr); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	// R10-09（I-F2-02）：段数帽同为格式层判定，管理员面同样在此
	// 拒绝（豁免的只是下方 netguard 段校验）。
	if err := validateRemoteAddrSegmentCount(remoteAddr); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	// F2-F1-02：端口格式同为格式层判定，管理员面同样在此拒绝——
	// 端口段在场却非 1–65535 数字端口的目标不得落库（判定点
	// validateRemoteAddrPorts，与消费面 parseTargetAddress 同口径）。
	if err := validateRemoteAddrPorts(remoteAddr); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
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
		if respondInternalIfRepoError(w, "forwardCreate: normalizeSpeedLimitReference failed", err) {
			return
		}
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
		if respondInternalIfRepoError(w, "forwardCreate: normalizeSpeedLimitReference failed", err) {
			return
		}
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	port := asInt(req["inPort"], 0)
	if port <= 0 {
		picked, pickErr := h.pickTunnelPort(tunnelID)
		if pickErr != nil {
			if respondInternalIfRepoError(w, "forwardCreate: pickTunnelPort failed", pickErr) {
				return
			}
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
		respondInternalError(w, "forwardCreate: tunnelUsesNftables failed", err)
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
			if respondInternalIfRepoError(w, "forwardCreate: collectNftablesSSHConfigsTx failed", err) {
				return
			}
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
		if respondInternalIfRepoError(w, "forwardCreate: validateForwardEntryNodes failed", err) {
			return
		}
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	strategy := strings.TrimSpace(defaultString(asString(req["strategy"]), "fifo"))
	// R4-F4-02：策略合法集校验（合法集为 kernel 单点判，update/恢复
	// 面共用）；校验对修剪后的最终落库值做，存 " fifo " 一类带空白
	// 值在此归一，避免声明与 agent 解析口径错位。
	if err := validateForwardStrategy(strategy); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	// A4-004（BC-26）：realm 不支持的负载策略明确拒绝，不再静默改写。
	if err := validateRealmForwardStrategy(tunnel, remoteAddr, strategy); err != nil {
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
	// B09-002：maxConn 与用户面 O5-006 同口径拒绝负值（此前只钳
	// ipMaxConn，maxConn 负值原样落库、消费面 MaxConn>0 门控下静
	// 默等价无限制，与填写意图相反且无提示）。
	if maxConn < 0 {
		response.WriteJSON(w, response.ErrDefault("连接数限制不能小于0"))
		return
	}
	ipMaxConn := asInt(req["ipMaxConn"], 0)
	if ipMaxConn < 0 {
		ipMaxConn = 0
	}
	proxyProtocol := asInt(req["proxyProtocol"], 0)
	proxyProtocolReceive := asInt(req["proxyProtocolReceive"], 0)
	proxyProtocolSend := asInt(req["proxyProtocolSend"], proxyProtocol)

	forwardID, err := h.repo.CreateForwardTx(userID, userName, name, tunnelID, remoteAddr, strategy, now, inx, entryNodes, port, inIp, nullableInt(speedID), maxConn, ipMaxConn, nullableInt(ipSpeedID), proxyProtocol, proxyProtocolReceive, proxyProtocolSend)
	releaseQuota()
	if err != nil {
		// F2-05：事务内条数复检命中——与锁外预检同文案回执。
		if errors.Is(err, repo.ErrForwardNumExceeded) || errors.Is(err, repo.ErrUserTunnelForwardNumExceeded) {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
		// 端口占用冲突是仓储层已映射的友好错误（repo.ErrPortInUse），
		// 原样回；其余事务错误（含 DB 约束原文）收口。
		if errors.Is(err, repo.ErrPortInUse) {
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
		respondInternalError(w, "forwardCreate: CreateForwardTx failed", err)
		return
	}
	createdForward, err := h.getForwardRecord(forwardID)
	if err != nil {
		// E5-S1（同型补齐，不计关闭数）：与下方 sync 失败分支及孪生
		// createRelayForward 的取行失败分支同形——补一行补偿删除，
		// 避免「创建失败」回执下新建行残留。
		_ = h.deleteForwardByID(forwardID)
		respondInternalError(w, "forwardCreate: getForwardRecord failed", err)
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
	if !requirePost(w, r) {
		return
	}
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
		if respondInternalIfRepoError(w, "forwardUpdate: resolveForwardAccess failed", err) {
			return
		}
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	oldPorts, err := h.listForwardPorts(id)
	if err != nil {
		respondInternalError(w, "forwardUpdate: listForwardPorts failed", err)
		return
	}

	tunnelID := asInt64(req["tunnelId"], forward.TunnelID)
	if tunnelID <= 0 {
		response.WriteJSON(w, response.ErrDefault("隧道ID不能为空"))
		return
	}
	if err := h.ensureTunnelPermission(actorUserID, actorRole, tunnelID); err != nil {
		if respondInternalIfRepoError(w, "forwardUpdate: ensureTunnelPermission failed", err) {
			return
		}
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
	// R2 K1 枚举复扫（B03-001 同族）：显式提交的改名按列宽 100 校验
	// （空名回填的存量名不连坐，故须在回填前判）。
	if _, ok := req["name"]; ok && name != "" && len([]rune(name)) > 100 {
		response.WriteJSON(w, response.ErrDefault("转发名称长度不能超过100个字符"))
		return
	}
	if name == "" {
		name = forward.Name
	}
	remoteAddr := strings.TrimSpace(asString(req["remoteAddr"]))
	if remoteAddr == "" {
		remoteAddr = forward.RemoteAddr
	}
	// 终审 L1-F-06：与创建面同口径，管理员面同样补语法层空标签判。
	if err := validateNoEmptyLabelAddr(remoteAddr); err != nil {
		response.WriteJSON(w, response.ErrDefault("转发目标地址主机名格式无效："+err.Error()))
		return
	}
	// R7-04（G-F3-01）：与创建面同口径，零有效目标（段集为空）属
	// 格式无效、与 netguard 豁免无关，管理员面同样拒绝。此处判的
	// 是回填后的合并值：提交纯分隔符直接命中；存量若已是零目标
	// （修复前落库的坏行），任何编辑也须先补正目标。
	if err := validateRemoteAddrHasTargets(remoteAddr); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	// R10-09（I-F2-02）：段数帽与创建面同口径、同为格式层判定，
	// 对回填后的合并值判定（存量超帽行任何编辑须先削减目标，
	// 与上方零目标口径一致）。
	if err := validateRemoteAddrSegmentCount(remoteAddr); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	// F2-F1-02：端口格式与创建面同口径，对回填后的合并值判定
	// （存量坏端口行任何编辑须先补正端口，与上方段数帽口径一致）。
	if err := validateRemoteAddrPorts(remoteAddr); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	isNftTunnel, entryNodes, err := h.tunnelUsesNftables(tunnelID)
	if err != nil {
		respondInternalError(w, "forwardUpdate: tunnelUsesNftables failed", err)
		return
	}
	if isNftTunnel {
		if err := h.validateNftablesForwardRequest(tunnel, remoteAddr, entryNodes); err != nil {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
	}
	// R7-05（G-F3-02）：netguard 段校验只在目标实际变更时复检。
	// 此前对回填后的合并值无条件复检，存量内网目标规则在开关关闭
	// 后被所有者冻结编辑（仅改名亦 403），与 forward.md「已保存的
	// 规则不受开关变化影响」冲突。变更判定走 remoteAddrTargetsChanged
	// 的段集口径：缺席回填、逐字相同或仅顺序/空段差的重提均视为
	// 未变更、不复检；提交新目标（改公网/改内网）照常全检。
	if actorRole != 0 && !h.allowLocalRemoteAddr() && remoteAddrTargetsChanged(remoteAddr, forward.RemoteAddr) {
		if err := IsSafeRemoteAddr(remoteAddr); err != nil {
			response.WriteJSON(w, response.Err(403, err.Error()))
			return
		}
	}
	strategy := strings.TrimSpace(asString(req["strategy"]))
	if strategy == "" {
		strategy = strings.TrimSpace(forward.Strategy)
	}
	// R4-F4-02：策略合法集校验，对回填后的合并值无条件判定（与零
	// 目标/段数帽同口径：存量坏策略行任何编辑须先补正）。
	if err := validateForwardStrategy(strategy); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
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
		if respondInternalIfRepoError(w, "forwardUpdate: normalizeSpeedLimitReference failed", err) {
			return
		}
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
			if respondInternalIfRepoError(w, "forwardUpdate: normalizeSpeedLimitReference failed", err) {
				return
			}
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
				if respondInternalIfRepoError(w, "forwardUpdate: pickTunnelPort failed", pickErr) {
					return
				}
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
	// R2-005 / L4-01（2026-11-03 拍板锁死）：入口端口创建后永不换口
	// （2026-10-30 定案）。此前同隧道三条 replace 路径与切隧道分支
	// 共用同一 port 解析、显式提交的 inPort 被全量接受替换，与定案
	// 及 resolveTunnelSegmentPort 头注口径不符。
	//
	// R4-01（LEDGER4）：锁改集合语义。逐行比对下异口转发（同一
	// forward 在不同入口节点的行口不同）的任何单值至多与一行相
	// 等、其余行必然触发拒绝——前端编辑恒提交列表口，异口转发经
	// 界面完全不可编辑，且回执引用首个不一致行的端口（非提交
	// 值）。现口径：显式提交的 inPort 命中存量行口集任一成员 ⇒
	// 非变更、放行；提交值不带逐节点含义，下游落库口径与未提交
	// inPort 时一致（保留节点逐节点保口、新行用规范口），故此处
	// 把 port 归一到规范口。未命中行口集 ⇒ 真变更企图，拒绝且
	// 文案引用提交值本身。无存量行时无可变更对象，沿用提交值
	// （与旧逐行比对的 vacuous 放行一致）。未提交 inPort（回填
	// 规范口）照常放行，切隧道沿用存量口、中间/出口段在隧道更
	// 新面按既有口径换口均不受影响。
	if rawInPort, submitted := req["inPort"]; submitted {
		if explicitPort := asInt(rawInPort, 0); explicitPort > 0 && len(oldPorts) > 0 {
			inRowSet := false
			for _, fp := range oldPorts {
				if fp.Port > 0 && fp.Port == explicitPort {
					inRowSet = true
					break
				}
			}
			if !inRowSet {
				response.WriteJSON(w, response.ErrDefault(fmt.Sprintf("入口端口不可变更：转发创建后入口端口固定，提交的入口端口 %d 不是该转发任一入口节点现行端口，如需更换端口请删除该转发后重新创建", explicitPort)))
				return
			}
			if canonical := pickForwardPortFromRecords(oldPorts); canonical > 0 {
				port = canonical
			}
		}
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
	// C-F3-01：占用/范围校验按逐节点实际落库口径走——已有行的入口
	// 节点按其现口校验（异口转发不再被规范口的范围/占用误拒），无行
	// 的新节点按规范口 port 校验。
	oldPortByNode := forwardPortByNode(oldPorts)
	if err := h.validateForwardEntryNodesWithPortFunc(fwdEntryNodes, func(nodeID int64) int {
		return forwardEntryPort(oldPortByNode, nodeID, port)
	}, id); err != nil {
		if respondInternalIfRepoError(w, "forwardUpdate: validateForwardEntryNodes failed", err) {
			return
		}
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	now := time.Now().UnixMilli()
	maxConn := asInt(req["maxConn"], forward.MaxConn)
	// B09-002：同 forwardCreate——maxConn 负值与 O5-006 同口径拒绝
	// （仅在本次显式提交时判定，存量负值做无关编辑不连坐）。
	if _, ok := req["maxConn"]; ok && maxConn < 0 {
		response.WriteJSON(w, response.ErrDefault("连接数限制不能小于0"))
		return
	}
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
		respondInternalError(w, "forwardUpdate: UpdateForward failed", err)
		return
	}
	if hasInIP {
		err = h.replaceForwardPorts(id, tunnelID, port, inIp, oldPorts)
	} else if tunnelID != forward.TunnelID {
		err = h.replaceForwardPorts(id, tunnelID, port, "", oldPorts)
	} else {
		err = h.replaceForwardPortsPreservingInIP(id, tunnelID, port, oldPorts)
	}
	if err != nil {
		rollbackAndRefreshWhitelist()
		// 与 CreateForwardTx 同口径：端口占用冲突回友好原文，
		// 其余仓储错误收口。
		if errors.Is(err, repo.ErrPortInUse) {
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
		respondInternalError(w, "forwardUpdate: replaceForwardPorts failed", err)
		return
	}
	updatedForward, err := h.getForwardRecord(id)
	if err != nil {
		rollbackAndRefreshWhitelist()
		respondInternalError(w, "forwardUpdate: getForwardRecord failed", err)
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
		if respondInternalIfRepoError(w, "forwardDelete: resolveForwardAccess failed", err) {
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
		respondInternalError(w, "forwardDelete: tunnelUsesNftables failed", modeErr)
		return
	} else if nftMode {
		if len(entryNodeIDs) == 0 {
			response.WriteJSON(w, response.ErrDefault("nftables 转发缺少入口节点"))
			return
		}
		// R35-FIX-F01：遗留/竞态混合态下拒绝删除而不是报成功删库
		// （gost/realm 服务会残留监听）。
		if err := h.rejectMixedNftForwardState(forward.TunnelID, nftMode, entryNodeIDs); err != nil {
			if respondInternalIfRepoError(w, "forwardDelete: rejectMixedNftForwardState failed", err) {
				return
			}
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
			respondInternalError(w, "forwardDelete: MarkForwardPendingDelete failed", err)
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
			respondInternalError(w, "forwardDelete: deleteForwardByID failed", err)
			return
		}
		h.refreshInboundWhitelists(affectedInboundIDs)
		response.WriteJSON(w, response.OKEmpty())
		return
	}
	// gost 路径：数据面已先行（controlForwardServices），再删库；检查 DB 错误。
	if err := h.deleteForwardByID(id); err != nil {
		respondInternalError(w, "forwardDelete: deleteForwardByID failed", err)
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
		if respondInternalIfRepoError(w, "forwardPause: resolveForwardAccess failed", err) {
			return
		}
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	// S-05: nftables 模式下暂停必须真正使规则失效。
	// R5-B3 D1-012 + D6: 数据面优先。按“已暂停”意图生成 plan（不含该转发）
	// 先 reconcile，成功后再写 status=0；失败则 DB 保持 active。
	if nftMode, entryNodeIDs, modeErr := h.tunnelUsesNftables(forward.TunnelID); modeErr != nil {
		respondInternalError(w, "forwardPause: tunnelUsesNftables failed", modeErr)
		return
	} else if nftMode {
		if len(entryNodeIDs) == 0 {
			response.WriteJSON(w, response.ErrDefault("nftables 转发缺少入口节点"))
			return
		}
		// R35-FIX-F01：遗留/竞态混合态下拒绝暂停而不是报成功（gost/
		// realm 服务会继续监听、继续计费）。
		if err := h.rejectMixedNftForwardState(forward.TunnelID, nftMode, entryNodeIDs); err != nil {
			if respondInternalIfRepoError(w, "forwardPause: rejectMixedNftForwardState failed", err) {
				return
			}
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
			respondInternalError(w, "forwardPause: UpdateForwardStatus failed", err)
			return
		}
	} else {
		if err := h.controlForwardServices(forward, "PauseService", false); err != nil {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
		if err := h.repo.UpdateForwardStatus(id, model.ForwardStatusPaused, time.Now().UnixMilli()); err != nil {
			log.Printf("forward pause failed op=db_write_after_dplane forward_id=%d err=%v", id, err)
			respondInternalError(w, "forwardPause: UpdateForwardStatus failed", err)
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
		if respondInternalIfRepoError(w, "forwardResetFlow: resolveForwardAccess failed", err) {
			return
		}
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if err := h.repo.ResetForwardFlow(id, time.Now().UnixMilli()); err != nil {
		respondInternalError(w, "forwardResetFlow: ResetForwardFlow failed", err)
		return
	}
	response.WriteJSON(w, response.OKEmpty())
}

// ensureTunnelEnabledForForward 是「使转发由暂停/停止转为运行」各入口
// 共用的隧道禁用门（R3 B08-001）：隧道不存在/已禁用即拒绝，与
// forwardCreate 的「隧道已禁用」同口径。此前恢复/批量恢复/批量重部署/
// 配额释放四面都漏了这道门，禁用隧道上的暂停转发可被重新激活且三引擎
// 后果分裂（gost 真下发、realm 被 override 强行恢复、nft 规则不生成但
// DB 假活）。四面共用本函数，不再各自漏检（闭合枚举见
// lanes/K6/r3/enumeration.md）。
func (h *Handler) ensureTunnelEnabledForForward(tunnelID int64) error {
	tunnel, err := h.getTunnelRecord(tunnelID)
	if err != nil {
		return errors.New("隧道不存在")
	}
	if tunnel.Status != 1 {
		return errors.New("隧道已禁用")
	}
	return nil
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
		if respondInternalIfRepoError(w, "forwardResume: resolveForwardAccess failed", err) {
			return
		}
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if err := h.ensureTunnelEnabledForForward(forward.TunnelID); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	now := time.Now().UnixMilli()
	// F2-05：恢复与创建共用条数竞态面——预检与状态激活之间持
	// per-user 配额执法锁串行化（defer 至函数尾，覆盖
	// ActivateForwardIfNotPending 的落库点）。
	unlockQuota := h.lockUserQuotaEnforcement(forward.UserID)
	defer unlockQuota()
	if err := h.ensureUserTunnelForwardAllowed(forward.UserID, forward.TunnelID, now); err != nil {
		if respondInternalIfRepoError(w, "forwardResume: ensureUserTunnelForwardAllowed failed", err) {
			return
		}
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	// S-05: nftables 模式下恢复必须真正加回规则。
	// R5-B3 D1-012 + D6: 数据面优先。按“已恢复”意图生成 plan（含该转发）
	// 先 reconcile，成功后再写 status=1；失败则 DB 保持 paused，无黑洞。
	if nftMode, entryNodeIDs, modeErr := h.tunnelUsesNftables(forward.TunnelID); modeErr != nil {
		respondInternalError(w, "forwardResume: tunnelUsesNftables failed", modeErr)
		return
	} else if nftMode {
		if len(entryNodeIDs) == 0 {
			response.WriteJSON(w, response.ErrDefault("nftables 转发缺少入口节点"))
			return
		}
		// R35-FIX-F01：遗留/竞态混合态下拒绝恢复而不是报成功（nft
		// 规则与 gost/realm 服务会双份下发）。
		if err := h.rejectMixedNftForwardState(forward.TunnelID, nftMode, entryNodeIDs); err != nil {
			if respondInternalIfRepoError(w, "forwardResume: rejectMixedNftForwardState failed", err) {
				return
			}
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
		if err := h.syncNftablesNodeWithOverrides(entryNodeIDs[0], nil, []*forwardRecord{forward}); err != nil {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
		if err := h.ActivateForwardIfNotPending(id, now); err != nil {
			log.Printf("nftables forward resume failed op=db_write_after_reconcile forward_id=%d err=%v", id, err)
			if respondInternalIfRepoError(w, "forwardResume: ActivateForwardIfNotPending failed", err) {
				return
			}
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
			if respondInternalIfRepoError(w, "forwardResume: ActivateForwardIfNotPending failed", err) {
				return
			}
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
	}
	response.WriteJSON(w, response.OKEmpty())
}

func (h *Handler) forwardDiagnose(w http.ResponseWriter, r *http.Request) {
	// R8-T-05: see tunnelDiagnose.
	extendWriteDeadline(w, diagnosisWriteDeadline)
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
		if respondInternalIfRepoError(w, "forwardDiagnose: resolveForwardAccess failed", err) {
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
		if respondInternalIfRepoError(w, "forwardDiagnose: diagnoseForwardRuntime failed", err) {
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
	// C1-04：本面两段循环逐条归属校验＋逐条 UPDATE（任意登录用户
	// 可达、工作量翻倍），条目数同口径计数帽（见 maxBatchIDs；本
	// 面不去重，按提交条数计）。
	if !checkBatchIDsCap(w, len(req.Forwards)) {
		return
	}
	// B08-002：两段式——先全量校验所有条目的归属/存在性，全部通过
	// 后才逐条落库。旧实现单循环内「校验一条、写一条」，中途某条
	// 不存在即报错返回，但排在它前面的条目排序已落库，前端按失败
	// 处理与实际状态不一致。落库阶段逐条检查 UpdateForwardOrder
	// 的返回 error（旧实现连同 repo 层一并吞错），写失败即报错；
	// 此阶段的失败只剩真实 DB 错误，不再有「不存在」形态。
	for _, f := range req.Forwards {
		if _, accessErr := h.ensureForwardAccessByActor(actorUserID, actorRole, f.ID); accessErr != nil {
			if errors.Is(accessErr, errForwardNotFound) {
				response.WriteJSON(w, response.ErrDefault("转发不存在"))
			} else if respondInternalIfRepoError(w, "forwardUpdateOrder: ensureForwardAccessByActor failed", accessErr) {
				// 已按通用文案回执。
			} else {
				response.WriteJSON(w, response.Err(-2, accessErr.Error()))
			}
			return
		}
	}
	now := time.Now().UnixMilli()
	for _, f := range req.Forwards {
		if err := h.repo.UpdateForwardOrder(f.ID, f.Inx, now); err != nil {
			respondInternalError(w, "forwardUpdateOrder: UpdateForwardOrder failed", err)
			return
		}
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
	ids, invalidIDs := idsFromBodyWithInvalid(r, w)
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
	// B08-003：id<=0 与不存在 id 同口径计入 failures，不再静默吞。
	for _, badID := range invalidIDs {
		f++
		failures = appendBatchFailureReason(failures, badID, "", "无效的转发ID")
	}
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
			failures = appendBatchFailure(failures, id, "", batchFailureErr(accessErr))
			continue
		}
		if nftMode, entryNodeIDs, modeErr := h.tunnelUsesNftables(forward.TunnelID); modeErr != nil {
			f++
			failures = appendBatchFailure(failures, id, forward.Name, batchFailureErr(markRepoError(modeErr)))
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
				failures = appendBatchFailure(failures, id, forward.Name, batchFailureErr(err))
				continue
			}
			// D6: 先标记 pending，再按节点聚合 reconcile。
			if err := h.repo.MarkForwardPendingDelete(id, entryNodeIDs[0], time.Now()); err != nil {
				f++
				failures = appendBatchFailure(failures, id, forward.Name, batchFailureErr(markRepoError(err)))
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
			// C1-01：删除链的仓储/基础设施失败不进回执原文；仅
			// 「删除处理中」哨兵是用户可见的业务原因。
			reasonErr := err
			if !errors.Is(err, errForwardPendingDelete) {
				reasonErr = errors.New("内部错误，请稍后重试")
			}
			failures = appendBatchFailure(failures, id, forward.Name, reasonErr)
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
	ids, invalidIDs := idsFromBodyWithInvalid(r, w)
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
	// B08-003：id<=0 与不存在 id 同口径计入 failures，不再静默吞。
	for _, badID := range invalidIDs {
		f++
		failures = appendBatchFailureReason(failures, badID, "", "无效的转发ID")
	}
	type nftIntent struct {
		exclude map[int64]bool
		include []*forwardRecord
		names   map[int64]string
	}
	nftGroups := make(map[int64]*nftIntent)
	// R3 B08-002：nft 意图项的 status 写入推迟到分组 reconcile 之后，
	// 批前 DB 计数会被同批多条共享。恢复判定通过的 nft 意图项按
	// （用户）与（用户，隧道）两维度预留名额，后续条目的 Num 判定
	// 把预留计入；gost 条目即时写库、计数实时可见，不占预留。
	type numReservationKey struct{ userID, tunnelID int64 }
	reservedByUser := make(map[int64]int64)
	reservedByUserTunnel := make(map[numReservationKey]int64)
	for _, id := range ids {
		forward, accessErr := h.ensureForwardAccessByActor(actorUserID, actorRole, id)
		if accessErr != nil {
			f++
			failures = appendBatchFailure(failures, id, "", batchFailureErr(accessErr))
			continue
		}
		resKey := numReservationKey{userID: forward.UserID, tunnelID: forward.TunnelID}
		if action == "resume" {
			// R3 B08-001：与单条恢复共用隧道禁用门，逐条记失败。
			if err := h.ensureTunnelEnabledForForward(forward.TunnelID); err != nil {
				f++
				failures = appendBatchFailure(failures, id, forward.Name, err)
				continue
			}
			if err := h.ensureUserTunnelForwardAllowedWithReserved(forward.UserID, forward.TunnelID, now, reservedByUser[forward.UserID], reservedByUserTunnel[resKey]); err != nil {
				f++
				failures = appendBatchFailure(failures, id, forward.Name, batchFailureErr(err))
				continue
			}
		}
		nftMode, entryNodeIDs, modeErr := h.tunnelUsesNftables(forward.TunnelID)
		if modeErr != nil {
			f++
			failures = appendBatchFailure(failures, id, forward.Name, batchFailureErr(markRepoError(modeErr)))
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
				failures = appendBatchFailure(failures, id, forward.Name, batchFailureErr(err))
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
				// R3 B08-002：意图项已通过 Num 判定但尚未写库，
				// 预留名额供同批后续条目计数（见循环前注释）。
				reservedByUser[forward.UserID]++
				reservedByUserTunnel[resKey]++
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
			statusErr = markRepoError(h.repo.UpdateForwardStatus(id, targetStatus, now))
		}
		if statusErr != nil {
			log.Printf("forward batch %s failed op=db_write_after_dplane forward_id=%d err=%v", action, id, statusErr)
			f++
			failures = appendBatchFailure(failures, id, forward.Name, batchFailureErr(statusErr))
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
				statusErr = markRepoError(h.repo.UpdateForwardStatus(id, targetStatus, now))
			}
			if statusErr != nil {
				log.Printf("nftables forward batch %s failed op=db_write_after_reconcile forward_id=%d err=%v", action, id, statusErr)
				f++
				failures = appendBatchFailure(failures, id, name, batchFailureErr(statusErr))
				continue
			}
			s++
		}
	}
	response.WriteJSON(w, response.OK(batchOperationResult{SuccessCount: s, FailCount: f, Failures: failures}))
}

func (h *Handler) forwardBatchRedeploy(w http.ResponseWriter, r *http.Request) {
	ids, invalidIDs := idsFromBodyWithInvalid(r, w)
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
	// B08-003：id<=0 与不存在 id 同口径计入 failures，不再静默吞。
	for _, badID := range invalidIDs {
		f++
		failures = appendBatchFailureReason(failures, badID, "", "无效的转发ID")
	}
	for _, id := range ids {
		forward, accessErr := h.ensureForwardAccessByActor(actorUserID, actorRole, id)
		if accessErr != nil {
			f++
			failures = appendBatchFailure(failures, id, "", batchFailureErr(accessErr))
			continue
		}
		// R3 B08-001：重部署会把转发服务重新下发到节点，与恢复同属
		// 「转入运行」面，同步前共用隧道禁用门，逐条记失败。
		if err := h.ensureTunnelEnabledForForward(forward.TunnelID); err != nil {
			f++
			failures = appendBatchFailure(failures, id, forward.Name, err)
			continue
		}
		if nftMode, entryNodeIDs, modeErr := h.tunnelUsesNftables(forward.TunnelID); modeErr != nil {
			f++
			failures = appendBatchFailure(failures, id, forward.Name, batchFailureErr(markRepoError(modeErr)))
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
				failures = appendBatchFailure(failures, id, forward.Name, batchFailureErr(err))
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
	if !requirePost(w, r) {
		return
	}
	var req struct {
		ForwardIDs     []int64 `json:"forwardIds"`
		TargetTunnelID int64   `json:"targetTunnelId"`
	}
	if err := decodeJSON(r.Body, &req); err != nil || req.TargetTunnelID <= 0 {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	// C6-01：ForwardIDs 的 valid 条目先去重（与 idsFromBodyWithInvalid
	// 同口径）——重复 id 第二条必然命中下方「规则已在目标隧道中」
	// 记一条与实际副作用相反的假失败；invalid 条目仍逐条计失败。
	req.ForwardIDs = dedupeValidBatchIDs(req.ForwardIDs)
	// R10-08：本面自有字段不走 idsFromBody 系，去重后同口径计数
	// 帽（见 maxBatchIDs；invalid 条目逐条计失败、同属工作量，
	// 与 idsFromBodyWithInvalid 的合计口径一致）。
	if !checkBatchIDsCap(w, len(req.ForwardIDs)) {
		return
	}
	actorUserID, actorRole, err := userRoleFromRequest(r)
	if err != nil {
		response.WriteJSON(w, response.Err(401, "无效的token或token已过期"))
		return
	}
	if err := h.ensureTunnelPermission(actorUserID, actorRole, req.TargetTunnelID); err != nil {
		if respondInternalIfRepoError(w, "forwardBatchChangeTunnel: ensureTunnelPermission failed", err) {
			return
		}
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
			// B08-003：非法 id 与不存在 id 同口径计入 failures，
			// 不再静默跳过（回执须能对账）。
			fail++
			failures = appendBatchFailureReason(failures, id, "", "无效的转发ID")
			continue
		}
		forward, accessErr := h.ensureForwardAccessByActor(actorUserID, actorRole, id)
		if accessErr != nil {
			fail++
			failures = appendBatchFailure(failures, id, "", batchFailureErr(accessErr))
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
			failures = appendBatchFailure(failures, id, forward.Name, batchFailureErr(markRepoError(listPortsErr)))
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
				failures = appendBatchFailure(failures, id, forward.Name, batchFailureErr(pickErr))
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
			failures = appendBatchFailure(failures, id, forward.Name, batchFailureErr(markRepoError(bctErr)))
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
		// C-F3-01：保留节点（同属新旧隧道入口集）逐节点沿用现口，
		// 规范口 p 只用于真正新增的节点行；校验同样逐节点按实际
		// 落库口径走，不再以单值口对全部入口节点一刀切。
		oldPortByNode := forwardPortByNode(oldPorts)
		portRangeOk := true
		var portRangeErr error
		for _, nid := range bctEntryNodes {
			nd, ndErr := h.getNodeRecord(nid)
			if ndErr != nil {
				portRangeErr = ndErr
				portRangeOk = false
				break
			}
			nodePort := forwardEntryPort(oldPortByNode, nid, p)
			if validateErr := validateRemoteNodePort(nd, nodePort); validateErr != nil {
				portRangeOk = false
				portRangeErr = validateErr
				break
			}
			if validateErr := validateLocalNodePort(nd, nodePort); validateErr != nil {
				portRangeOk = false
				portRangeErr = validateErr
				break
			}
			if validateErr := h.validateForwardPortAvailability(nd, nodePort, id); validateErr != nil {
				portRangeOk = false
				portRangeErr = validateErr
				break
			}
		}
		if !portRangeOk {
			fail++
			failures = appendBatchFailure(failures, id, forward.Name, batchFailureErr(portRangeErr))
			continue
		}
		// X-P2-4 ②：托管中转转发按目标隧道出口集重算 RemoteAddr，不再
		// 只改端口沿用旧址；重算失败（如目标隧道出口集违反落地单出口
		// 规则）该条直接失败、不落库。
		newRemoteAddr, remoteAddrChanged, retargetErr := h.recomputedRelayRemoteAddr(forward, targetTunnel)
		if retargetErr != nil {
			fail++
			failures = appendBatchFailure(failures, id, forward.Name, batchFailureErr(retargetErr))
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
			bctEntries[i] = forwardPortReplaceEntry{NodeID: nid, Port: forwardEntryPort(oldPortByNode, nid, p), InIP: ""}
		}
		if err := h.repo.ChangeForwardTunnelAndPorts(id, req.TargetTunnelID, effectiveRemoteAddr, bctEntries, time.Now().UnixMilli()); err != nil {
			fail++
			failures = appendBatchFailure(failures, id, forward.Name, batchFailureErr(markRepoError(err)))
			continue
		}
		updatedForward, fetchErr := h.getForwardRecord(id)
		if fetchErr != nil {
			h.rollbackForwardMutation(forward, oldPorts)
			failedInboundIDs = append(failedInboundIDs, fwdInboundIDs...)
			fail++
			failures = appendBatchFailure(failures, id, forward.Name, batchFailureErr(fetchErr))
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
	if !requirePost(w, r) {
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
	// R2 B09-004：名称按列宽 varchar(100) 校验（O5-002 同型），
	// 堵住 PG 下超长名吐原始落库错误、SQLite 静默存入的分裂。
	if len([]rune(name)) > 100 {
		response.WriteJSON(w, response.ErrDefault("名称长度不能超过100个字符"))
		return
	}

	// B09-001：写侧合法集校验——在场不可解析先 fail-closed（FIX2-A
	// 同族），再验 speed≥1、status∈{0,1}，非法不落库。
	for _, key := range []string{"speed", "status"} {
		if presentNumericFieldInvalid(req, key) {
			response.WriteJSON(w, response.ErrDefault(fmt.Sprintf("%s 参数无效", key)))
			return
		}
	}
	speed := asInt(req["speed"], 100)
	status := asInt(req["status"], 1)
	if err := validateSpeedLimitSpeed(speed); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	if err := validateSpeedLimitStatus(status); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}

	now := time.Now().UnixMilli()
	_, err := h.repo.CreateSpeedLimit(name, speed, now, status)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}

	response.WriteJSON(w, response.OKEmpty())
}

func (h *Handler) speedLimitUpdate(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
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
	// R2 B09-004：同 create 按列宽 100 校验——仅验本次在场字段
	// （presence 口径，与下方 speed/status 一致，存量超长名做无关
	// 编辑不连坐）。
	if _, ok := req["name"]; ok && len([]rune(name)) > 100 {
		response.WriteJSON(w, response.ErrDefault("名称长度不能超过100个字符"))
		return
	}

	speed := asInt(req["speed"], old.Speed)
	status := asInt(req["status"], old.Status)

	// B09-001：与 create 同口径的合法集校验——仅验本次在场字段
	// （presence 口径：存量非法值做无关编辑不连坐，与 R11X-008/009
	// 一致）；在场不可解析先 fail-closed（FIX2-A 同族）。
	for _, key := range []string{"speed", "status"} {
		if presentNumericFieldInvalid(req, key) {
			response.WriteJSON(w, response.ErrDefault(fmt.Sprintf("%s 参数无效", key)))
			return
		}
	}
	if _, ok := req["speed"]; ok {
		if err := validateSpeedLimitSpeed(speed); err != nil {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
	}
	if _, ok := req["status"]; ok {
		if err := validateSpeedLimitStatus(status); err != nil {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
	}

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

type tunnelRuntimeNode struct {
	NodeID    int64
	Protocol  string
	Strategy  string
	Inx       int
	ChainType int
	Port      int
	ConnectIP string
}

// tunnelPortChange describes one non-entry segment port that an update
// prepare had to move. Two distinct causes, told apart by RoleChanged
// because the receipt warning must name the real one (F2-T1-02):
//   - RoleChanged=false: the persisted port was no longer usable
//     (occupied by someone else, or outside the node's current port
//     spec) and was re-picked within the same segment role;
//   - RoleChanged=true: the node kept a persisted port in this tunnel
//     but in a different segment role (FromChainType); segment ports
//     are not carried across roles, so a fresh port was picked even
//     if the old one was free and in spec.
type tunnelPortChange struct {
	NodeID        int64
	ChainType     int // 2 = chain hop, 3 = exit
	OldPort       int
	NewPort       int
	RoleChanged   bool
	FromChainType int // previous segment role; only set when RoleChanged
}

type tunnelCreateState struct {
	TunnelID     int64
	Type         int
	IPPreference string // "" = auto, "v4" = prefer IPv4, "v6" = prefer IPv6
	// TunnelProtocol 是隧道行自身协议（归一化小写），由
	// applyTunnelRuntimeWithMode 在下发前统一装配（K1-05）：段
	// 协议为空时生成面回落到它，与探针面 chainSegmentProtocol
	// 同源。"" = 未装配（非 apply 路径的零值 state），段回落退化
	// 为段行自身协议。
	TunnelProtocol string
	InNodes      []tunnelRuntimeNode
	ChainHops    [][]tunnelRuntimeNode
	OutNodes     []tunnelRuntimeNode
	Nodes        map[int64]*nodeRecord
	NodeIDList   []int64
	// PortChanges records non-entry port re-picks made during an update
	// prepare (P01-002), so tunnelUpdate can surface them in the
	// receipt's warnings instead of drifting ports silently.
	PortChanges []tunnelPortChange
	// SegmentPorts records the port prepare resolved for every
	// non-entry segment slot present in the request, keyed by
	// (chainType, inx, nodeID) — the same identity
	// replaceTunnelChainsTx persists. applyTunnelPortsToRequest
	// writes them back into the request so the persisted rows carry
	// exactly prepare's ports for every tunnel type, including
	// type-1 tunnels whose segment rows prepare builds no runtime
	// topology for (L9 观察 1).
	SegmentPorts map[tunnelChainPortKey]int
}

// tunnelChainPortKey identifies one non-entry segment slot of a tunnel
// by (chainType, inx, nodeID) — the same identity replaceTunnelChainsTx
// persists (inx = 1-based position within the segment list).
type tunnelChainPortKey struct {
	chainType int
	inx       int
	nodeID    int64
}

// tunnelChainNodePort is a tunnel's persisted non-entry port for one
// node, indexed by node alone (R3 B06-001): a node appears at most once
// among a tunnel's non-entry segments, so the node identifies its
// persisted port unambiguously even when its position (inx) moved.
type tunnelChainNodePort struct {
	chainType int
	port      int
}

// existingTunnelChainPorts loads the tunnel's persisted non-entry ports
// (chain hops + exits) for update-time reuse decisions (P01-002). It
// returns two indexes over the same rows: by exact slot
// (chainType, inx, nodeID) and by node (R3 B06-001 — position shifts
// must still find the node's own port).
func (h *Handler) existingTunnelChainPorts(tx *gorm.DB, tunnelID int64) (map[tunnelChainPortKey]int, map[int64]tunnelChainNodePort, error) {
	rows, err := h.repo.ListChainNodesForTunnelTx(tx, tunnelID)
	if err != nil {
		return nil, nil, err
	}
	ports := make(map[tunnelChainPortKey]int, len(rows))
	byNode := make(map[int64]tunnelChainNodePort, len(rows))
	for _, row := range rows {
		if row.Port > 0 && (row.ChainType == 2 || row.ChainType == 3) {
			ports[tunnelChainPortKey{chainType: row.ChainType, inx: int(row.Inx), nodeID: row.NodeID}] = row.Port
			byNode[row.NodeID] = tunnelChainNodePort{chainType: row.ChainType, port: row.Port}
		}
	}
	return ports, byNode, nil
}

// resolveTunnelSegmentPort decides the port for one non-entry segment
// node during prepareTunnelCreateState when the request carries no
// explicit port:
//   - create (excludeTunnelID == 0): random pick, as before;
//   - update: reuse the node's persisted port while it is still legal —
//     inside the node's port spec and unoccupied under the unified
//     four-source predicate (excluding this tunnel). A rename-only edit
//     must not silently drift ports (P01-002). Only when the old port
//     is taken, out of spec, or the node has no persisted port does it
//     re-pick — with the same random policy as create (the old update
//     path used the sequential minimum, which drifted ports on every
//     edit and piled them at the bottom of the range). A re-pick that
//     moves the port is recorded in state.PortChanges so the update
//     receipt can warn about it.
//
// R3 B06-001: reuse identity is the node, not the slot. The exact-slot
// index (chainType, inx, nodeID) is tried first; on a miss the by-node
// index (chainType, nodeID) still finds the node's own persisted port
// after position shifts (hop reorder, prepend, deleting an earlier
// segment), so a position-only edit keeps the port. Any update-time
// re-pick of a node that had a persisted port in this tunnel — whatever
// the reason — is recorded in state.PortChanges; only a genuinely new
// segment (node had no persisted port here) picks silently, as create
// does. Entry segments never pass through this function and keep their
// create-time immutability (P01 口径）.
func (h *Handler) resolveTunnelSegmentPort(tx *gorm.DB, state *tunnelCreateState, nodeID int64, chainType int, inx int, allocated map[int64]int, excludeTunnelID int64, existing map[tunnelChainPortKey]int, existingByNode map[int64]tunnelChainNodePort) (int, error) {
	if excludeTunnelID > 0 {
		if oldPort, ok := existing[tunnelChainPortKey{chainType: chainType, inx: inx, nodeID: nodeID}]; ok && oldPort > 0 {
			return h.reuseOrRepickTunnelSegmentPort(tx, state, nodeID, chainType, oldPort, allocated, excludeTunnelID)
		}
		if nodePort, ok := existingByNode[nodeID]; ok && nodePort.port > 0 {
			if nodePort.chainType == chainType {
				// Same node, same segment role, new position: the
				// persisted port is still this node's port.
				return h.reuseOrRepickTunnelSegmentPort(tx, state, nodeID, chainType, nodePort.port, allocated, excludeTunnelID)
			}
			// The node kept a persisted port in this tunnel but in a
			// different segment role: not reused, and the re-pick must
			// not be silent (P01 口径：未沿用存量口的重挑一律回执点名）。
			port, err := h.repo.PickRandomNodePortTx(tx, nodeID, allocated, excludeTunnelID)
			if err != nil {
				return 0, err
			}
			if port != nodePort.port {
				state.PortChanges = append(state.PortChanges, tunnelPortChange{
					NodeID: nodeID, ChainType: chainType, OldPort: nodePort.port, NewPort: port,
					RoleChanged: true, FromChainType: nodePort.chainType,
				})
			}
			return port, nil
		}
	}
	return h.repo.PickRandomNodePortTx(tx, nodeID, allocated, excludeTunnelID)
}

// reuseOrRepickTunnelSegmentPort keeps oldPort while it is still legal
// for the node (in spec, unoccupied under the unified four-source
// predicate excluding this tunnel); otherwise it re-picks with the
// create-time random policy and records the move in state.PortChanges
// so the update receipt warns instead of drifting silently (P01-002).
func (h *Handler) reuseOrRepickTunnelSegmentPort(tx *gorm.DB, state *tunnelCreateState, nodeID int64, chainType int, oldPort int, allocated map[int64]int, excludeTunnelID int64) (int, error) {
	reusable, err := h.repo.TunnelChainPortReusableTx(tx, nodeID, oldPort, excludeTunnelID)
	if err != nil {
		return 0, err
	}
	if reusable {
		allocated[nodeID] = oldPort
		return oldPort, nil
	}
	port, err := h.repo.PickRandomNodePortTx(tx, nodeID, allocated, excludeTunnelID)
	if err != nil {
		return 0, err
	}
	if port != oldPort {
		state.PortChanges = append(state.PortChanges, tunnelPortChange{
			NodeID: nodeID, ChainType: chainType, OldPort: oldPort, NewPort: port,
		})
	}
	return port, nil
}

// detachedSegmentPortConflictError 为游离显式段口的占用拒绝生成
// 点名来源的文案（RV3-02，与 L9 观察 2 的 forwardPortConflictMessage
// 同口径：入站/链路段/联邦预留占用不得误报为「已被其他转发占用」）。
// 来源判定走统一谓词 NodePortOccupantTx，排除集与触发拒绝的
// NodePortOccupiedForTunnelTx 完全一致（本隧道链路行不算占用），
// 保证点名的来源就是触发拒绝的来源。
func (h *Handler) detachedSegmentPortConflictError(tx *gorm.DB, nodeID int64, node *nodeRecord, port int, excludeTunnelID int64) error {
	nodeName := ""
	if node != nil {
		nodeName = strings.TrimSpace(node.Name)
	}
	if nodeName == "" {
		nodeName = strconv.FormatInt(nodeID, 10)
	}
	var excl repo.PortExclusions
	if excludeTunnelID > 0 {
		excl.TunnelIDs = []int64{excludeTunnelID}
	}
	source, err := h.repo.NodePortOccupantTx(tx, nodeID, port, excl)
	if err != nil {
		return err
	}
	switch source {
	case repo.PortSourceInbound:
		return fmt.Errorf("节点 %s 端口 %d 已被入站占用", nodeName, port)
	case repo.PortSourceChain:
		return fmt.Errorf("节点 %s 端口 %d 已被隧道链路段占用", nodeName, port)
	case repo.PortSourcePeer:
		return fmt.Errorf("节点 %s 端口 %d 已被联邦共享预留占用", nodeName, port)
	case repo.PortSourceForward:
		return fmt.Errorf("节点 %s 端口 %d 已被其他转发占用", nodeName, port)
	}
	return fmt.Errorf("节点 %s 端口 %d 已被占用", nodeName, port)
}

// prepareDetachedSegmentPorts resolves ports for the non-entry
// segment rows of a non-type-2 tunnel request (L9 观察 1). These rows
// never enter the runtime topology (state.OutNodes/ChainHops stay
// empty for type 1), but replaceTunnelChainsTx persists them for
// every tunnel type — so their ports must go through the same
// discipline as type-2 segments:
//   - omitted port on update: reuse the persisted port while legal,
//     re-pick only when unusable, and record the move in
//     state.PortChanges so the receipt warns (P01-002);
//   - explicit port: range (1-65535) and four-source occupancy are
//     checked here — the same position type-2 explicit ports get
//     in validateExplicitPort (B4/F-015), minus the node-spec
//     (port pool) check. The spec check stays exempt: the pool
//     does not constrain these detached rows, and out-of-pool
//     segment ports are legitimate stock (pinned by the C001
//     latent-guard and R2B2 idempotency tests — their 29000/29001
//     requests are in-range and unoccupied, so the two restored
//     checks do not touch them). The occupancy check is the only
//     gate on the create path: tunnelUpdate re-checks occupancy
//     in its commit segment, tunnelCreate has no such recheck,
//     and replaceTunnelChainsTx persists explicit ports verbatim
//     (the chain_tunnel (node_id, port) unique index only catches
//     chain-vs-chain collisions, as a raw DB error);
//   - the resolved port lands in state.SegmentPorts so
//     applyTunnelPortsToRequest writes it back and
//     replaceTunnelChainsTx persists prepare's port instead of
//     silently re-picking its own random one.
//
// Node records are loaded best-effort (for the receipt's node
// names in port-change warnings); an unreadable record keeps the
// historical tolerance for these detached rows instead of failing
// the whole save, but an omitted port still resolves through the
// shared segment-port machinery.
func (h *Handler) prepareDetachedSegmentPorts(tx *gorm.DB, req map[string]interface{}, state *tunnelCreateState, excludeTunnelID int64) error {
	outRaw := asMapSlice(req["outNodeId"])
	chainRaw := asAnySlice(req["chainNodes"])
	if len(outRaw) == 0 && len(chainRaw) == 0 {
		return nil
	}
	existingPorts := map[tunnelChainPortKey]int{}
	existingPortsByNode := map[int64]tunnelChainNodePort{}
	if excludeTunnelID > 0 {
		var err error
		existingPorts, existingPortsByNode, err = h.existingTunnelChainPorts(tx, excludeTunnelID)
		if err != nil {
			return err
		}
	}
	allocated := map[int64]int{}
	resolve := func(item map[string]interface{}, chainType int, inx int) error {
		nodeID := asInt64(item["nodeId"], 0)
		if nodeID <= 0 {
			return nil
		}
		// R1-03-M1：游离段行的 strategy 不经 state 定型、由
		// replaceTunnelChainsTx 从 req 原文落库——在此与 type-2
		// 段同口径过链路策略合法集门（该函数落库前另有同判兜底）。
		if _, err := normalizeChainStrategy(chainType, asString(item["strategy"])); err != nil {
			return err
		}
		if _, ok := state.Nodes[nodeID]; !ok {
			if node, nErr := h.repo.GetNodeRecordTx(tx, nodeID); nErr == nil && node != nil {
				state.Nodes[nodeID] = node
			}
		}
		port := asInt(item["port"], 0)
		if port > 0 {
			// RV3-02：显式口恢复范围＋四源占用两检（规格/端口池检
			// 保持豁免，见函数头注）。范围检与 validateExplicitPort
			// 同位、无条件；占用检按节点远端性分流（远端节点的口
			// 不在本地端口空间，与 validateExplicitPort 对远端跳
			// 过占用检同口径），远端判定走 IsRemoteNodeTx 而非尽力
			// 而为的节点记录缓存——记录读不到时占用检照常执行，
			// 不重蹈「无检落库」。
			if port > 65535 {
				return fmt.Errorf("端口 %d 超出有效范围 1-65535", port)
			}
			isRemote, remoteErr := h.repo.IsRemoteNodeTx(tx, nodeID)
			if remoteErr != nil {
				return remoteErr
			}
			if !isRemote {
				occupied, err := h.repo.NodePortOccupiedForTunnelTx(tx, nodeID, port, excludeTunnelID)
				if err != nil {
					return err
				}
				if occupied {
					return h.detachedSegmentPortConflictError(tx, nodeID, state.Nodes[nodeID], port, excludeTunnelID)
				}
			}
			state.SegmentPorts[tunnelChainPortKey{chainType: chainType, inx: inx, nodeID: nodeID}] = port
			return nil
		}
		isRemote, remoteErr := h.repo.IsRemoteNodeTx(tx, nodeID)
		if remoteErr != nil {
			return remoteErr
		}
		if isRemote {
			return nil
		}
		resolved, err := h.resolveTunnelSegmentPort(tx, state, nodeID, chainType, inx, allocated, excludeTunnelID, existingPorts, existingPortsByNode)
		if err != nil {
			return err
		}
		state.SegmentPorts[tunnelChainPortKey{chainType: chainType, inx: inx, nodeID: nodeID}] = resolved
		return nil
	}
	for idx, item := range outRaw {
		if err := resolve(item, 3, idx+1); err != nil {
			return err
		}
	}
	for hopIdx, hopRaw := range chainRaw {
		for _, item := range asMapSlice(hopRaw) {
			if err := resolve(item, 2, hopIdx+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func (h *Handler) prepareTunnelCreateState(tx *gorm.DB, req map[string]interface{}, tunnelType int, excludeTunnelID int64) (*tunnelCreateState, error) {
	state := &tunnelCreateState{
		Type:         tunnelType,
		InNodes:      make([]tunnelRuntimeNode, 0),
		ChainHops:    make([][]tunnelRuntimeNode, 0),
		OutNodes:     make([]tunnelRuntimeNode, 0),
		Nodes:        make(map[int64]*nodeRecord),
		SegmentPorts: make(map[tunnelChainPortKey]int),
	}
	nodeIDs := make([]int64, 0)

	for _, item := range asMapSlice(req["inNodeId"]) {
		nodeID := asInt64(item["nodeId"], 0)
		if nodeID <= 0 {
			continue
		}
		strategy, err := normalizeChainStrategy(1, asString(item["strategy"]))
		if err != nil {
			return nil, err
		}
		nodeIDs = append(nodeIDs, nodeID)
		state.InNodes = append(state.InNodes, tunnelRuntimeNode{
			NodeID:    nodeID,
			Protocol:  defaultString(asString(item["protocol"]), "tls"),
			Strategy:  strategy,
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

		// P01-002：更新时先载入本隧道现存的非入口段口，供缺省口走
		// 「沿用现口、不可用才重挑」口径（见 resolveTunnelSegmentPort）。
		existingPorts := map[tunnelChainPortKey]int{}
		existingPortsByNode := map[int64]tunnelChainNodePort{}
		if excludeTunnelID > 0 {
			var err error
			existingPorts, existingPortsByNode, err = h.existingTunnelChainPorts(tx, excludeTunnelID)
			if err != nil {
				return nil, err
			}
		}

		allocated := map[int64]int{}
		for idx, item := range outNodesRaw {
			nodeID := asInt64(item["nodeId"], 0)
			if nodeID <= 0 {
				continue
			}
			nodeIDs = append(nodeIDs, nodeID)
			strategy, err := normalizeChainStrategy(3, asString(item["strategy"]))
			if err != nil {
				return nil, err
			}
			port := asInt(item["port"], 0)
			if port <= 0 {
				isRemote, remoteErr := h.repo.IsRemoteNodeTx(tx, nodeID)
				if remoteErr != nil {
					return nil, remoteErr
				}
				if !isRemote {
					var err error
					port, err = h.resolveTunnelSegmentPort(tx, state, nodeID, 3, idx+1, allocated, excludeTunnelID, existingPorts, existingPortsByNode)
					if err != nil {
						return nil, err
					}
				}
			}
			if port > 0 {
				state.SegmentPorts[tunnelChainPortKey{chainType: 3, inx: idx + 1, nodeID: nodeID}] = port
			}
			state.OutNodes = append(state.OutNodes, tunnelRuntimeNode{
				NodeID:    nodeID,
				Protocol:  defaultString(asString(item["protocol"]), "tls"),
				Strategy:  strategy,
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
				strategy, err := normalizeChainStrategy(2, asString(item["strategy"]))
				if err != nil {
					return nil, err
				}
				port := asInt(item["port"], 0)
				if port <= 0 {
					isRemote, remoteErr := h.repo.IsRemoteNodeTx(tx, nodeID)
					if remoteErr != nil {
						return nil, remoteErr
					}
					if !isRemote {
						var err error
						port, err = h.resolveTunnelSegmentPort(tx, state, nodeID, 2, hopIdx+1, allocated, excludeTunnelID, existingPorts, existingPortsByNode)
						if err != nil {
							return nil, err
						}
					}
				}
				if port > 0 {
					state.SegmentPorts[tunnelChainPortKey{chainType: 2, inx: hopIdx + 1, nodeID: nodeID}] = port
				}
				hop = append(hop, tunnelRuntimeNode{
					NodeID:    nodeID,
					Protocol:  defaultString(asString(item["protocol"]), "tls"),
					Strategy:  strategy,
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
	} else {
		// L9 观察 1：非 type-2 隧道的 outNodeId/chainNodes 行不进运行
		// 时拓扑，但 replaceTunnelChainsTx 照样落库。此前 prepare 不
		// 解析这些行的端口：更新时省略 port 的行在落库兜底被随机重
		// 挑（与 P01-002「缺省沿用现口、重挑必回执警示」口径不符，
		// 且无任何 PortChanges 记录）。在此解析（缺省沿用/重挑
		// ＋记录，显式口过范围＋四源占用两检、规格检豁免），
		// 结果经 SegmentPorts 回写请求后落库。
		if err := h.prepareDetachedSegmentPorts(tx, req, state, excludeTunnelID); err != nil {
			return nil, err
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

	// F-UDP-4：段协议同时充当链服务的 listener 类型与上一跳的
	// dialer 类型，非法值会直达 agent 解析（注册表内 sshd/tun 等
	// 设计外类型可被实例化）。创建/更新共用本入口，在此按合法集
	// （peerTunnelTransportTypes）统一校验并小写归一，与联邦面
	// reserve/apply-role 同口径。
	normalizeNodeProtocols := func(nodes []tunnelRuntimeNode) error {
		for i := range nodes {
			if !isAllowedTunnelProtocol(nodes[i].Protocol) {
				return fmt.Errorf("不支持的隧道协议 %q", strings.TrimSpace(nodes[i].Protocol))
			}
			nodes[i].Protocol = normalizeTunnelProtocol(nodes[i].Protocol)
		}
		return nil
	}
	if err := normalizeNodeProtocols(state.InNodes); err != nil {
		return nil, err
	}
	if err := normalizeNodeProtocols(state.OutNodes); err != nil {
		return nil, err
	}
	for _, hop := range state.ChainHops {
		if err := normalizeNodeProtocols(hop); err != nil {
			return nil, err
		}
	}

	return state, nil
}

func validateTunnelRuntimeBeforeApply(state *tunnelCreateState) error {
	if state.Type != 2 {
		return nil
	}
	// B4-01 残面：存量状态重放面补门。请求写面（create/update）
	// 的 strategy 都过 normalizeChainStrategy，但 redeploy 类路径
	// 经 reconstructTunnelState 把 chain_tunnel 存量策略原文装入
	// state、此前在本验证点零判——恢复导入等旁路写进的非法策略
	// （banana/大小写异形/跳行 best）会被原样重放下发，agent 侧
	// 静默降级轮询。现与写面同判据 fail-closed 点名：存量非法
	// 策略须先经编辑面修正， redeploy 不放行。
	strategyGroups := [][]tunnelRuntimeNode{state.InNodes, state.OutNodes}
	strategyGroups = append(strategyGroups, state.ChainHops...)
	for _, group := range strategyGroups {
		for _, n := range group {
			if !kernel.ChainStrategyLegal(n.ChainType, defaultString(strings.TrimSpace(n.Strategy), "round")) {
				return fmt.Errorf("节点 %d 的存量链路策略 %q 不合法（合法值为 %s），请先修正隧道配置后再部署",
					n.NodeID, strings.TrimSpace(n.Strategy), kernel.ChainStrategyLegalValues(n.ChainType))
			}
		}
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

	// L9 观察 1：按 (chainType, inx, nodeID) 精确回写 prepare 解析的
	// 全部段口。按节点回写覆盖不到 type-1 的游离段行（不在
	// state.OutNodes/ChainHops 里），省略 port 的行到
	// replaceTunnelChainsTx 时仍是 0、被随机重挑且无回执警示；
	// 精确键与落库迭代同序（outNodeId→3、chainNodes 组序→2），
	// 回写值与 prepare 定型值逐行一致。
	if len(state.SegmentPorts) > 0 {
		for idx, item := range asMapSlice(req["outNodeId"]) {
			nodeID := asInt64(item["nodeId"], 0)
			if port, ok := state.SegmentPorts[tunnelChainPortKey{chainType: 3, inx: idx + 1, nodeID: nodeID}]; ok && port > 0 {
				item["port"] = port
			}
		}
		for hopIdx, hopRaw := range asAnySlice(req["chainNodes"]) {
			for _, item := range asMapSlice(hopRaw) {
				nodeID := asInt64(item["nodeId"], 0)
				if port, ok := state.SegmentPorts[tunnelChainPortKey{chainType: 2, inx: hopIdx + 1, nodeID: nodeID}]; ok && port > 0 {
					item["port"] = port
				}
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

// ensureRemoteNodeRelayAuth 是消费方凭据自愈（D3-04）：下发前向
// 提供方做一次 Connect，按现行凭据合并刷新 RemoteConfig 并写回 DB
// 与内存副本。D3-04 初版只在本地密码为空（升级前导入的存量节点）时
// 才刷新；R2-F3-01：提供方从旧备份恢复到空库后凭据被重新补发，
// 消费方持旧的非空凭据时旧实现永不刷新、数据面无限期被 relay 鉴权
// 拒绝而控制面无感——现改为每次下发前都比对刷新（与用量同步的无
// 条件覆写同口径，经 mergeRemoteConnectCredentials 合并落盘），刷
// 新后本次下发即以新凭据继续，构成一次重试。best-effort：Connect
// 失败记日志并保留存量值继续下发——提供方不可达会在后续运行时调
// 用处显形为隧道错误，凭据失效另由节点状态同步面同口径自愈并以
// syncError 披露；目标服务若已强制鉴权，旧凭据会在提供方被拒，不
// 会静默放行。
func (h *Handler) ensureRemoteNodeRelayAuth(fc *client.FederationClient, node *nodeRecord, localDomain string) {
	if node == nil || node.IsRemote != 1 {
		return
	}
	remoteURL := strings.TrimSpace(node.RemoteURL)
	remoteToken := strings.TrimSpace(node.RemoteToken)
	if remoteURL == "" || remoteToken == "" {
		return
	}
	info, err := fc.Connect(remoteURL, remoteToken, localDomain)
	if err != nil {
		log.Printf("federation credential self-heal: connect remote node %d (%s) before tunnel apply: %v", node.ID, nodeDisplayName(node), err)
		return
	}
	if info == nil {
		return
	}
	// FIX2-B（V5-3）：版本提示记日志、对端版本随凭据自愈一并留痕。
	logFederationVersionWarning(info)
	if updated, changed := h.mergeRemoteConnectCredentials(node.ID, node.RemoteConfig, info); changed {
		node.RemoteConfig = updated
	}
}

func (h *Handler) applyFederationRuntime(state *tunnelCreateState, localDomain string) ([]repo.FederationTunnelBinding, []federationRuntimeReleaseRef, error) {
	bindings := make([]repo.FederationTunnelBinding, 0)
	releaseRefs := make([]federationRuntimeReleaseRef, 0)
	if h == nil || state == nil {
		return bindings, releaseRefs, nil
	}
	// R8-T-06: this client drives Connect/ReservePort/ApplyRole —
	// runtime faces whose peer work is sequential 6s node commands
	// under a shared mutex; give it the explicit runtime budget
	// instead of the 10s default.
	fc := client.NewFederationClientWithTimeout(federationRuntimeClientTimeout)
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
			Protocol:      effectiveSegmentProtocol(outNode.Protocol, state.TunnelProtocol),
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
			Protocol:      effectiveSegmentProtocol(outNode.Protocol, state.TunnelProtocol),
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
				Protocol:      effectiveSegmentProtocol(chainNode.Protocol, state.TunnelProtocol),
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
					Protocol: effectiveSegmentProtocol(target.Protocol, state.TunnelProtocol),
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
				Protocol:      effectiveSegmentProtocol(chainNode.Protocol, state.TunnelProtocol),
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
	// K1-05：把隧道行协议写入 state，段协议回落（生成面）与探针
	// 面自此同源——state 各装配点（创建/更新/reconstruct）不再各
	// 自查库。隧道行协议是创建/更新面的权威字段（tunnel.protocol
	// 有 default 'tls' 且过 normalize），此处以库中现值为准。
	if tr, terr := h.getTunnelRecord(state.TunnelID); terr == nil && tr != nil {
		state.TunnelProtocol = normalizeTunnelProtocol(tr.Protocol)
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
		chainData, err := h.buildTunnelChainConfig(state.TunnelID, inNode.NodeID, targets, state.Nodes, state.IPPreference, state.TunnelProtocol)
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
			chainData, err := h.buildTunnelChainConfig(state.TunnelID, chainNode.NodeID, nextTargets, state.Nodes, state.IPPreference, state.TunnelProtocol)
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

			serviceData := h.buildTunnelChainServiceConfig(state.TunnelID, chainNode, state.Nodes[chainNode.NodeID], len(nextTargets), state.TunnelProtocol)
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
		serviceData := h.buildTunnelChainServiceConfig(state.TunnelID, outNode, state.Nodes[outNode.NodeID], 1, state.TunnelProtocol)
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
	chainData, err := h.buildTunnelChainConfig(tunnelID, ownerNodeID, targets, nodes, ipPreference)
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
			Protocol: effectiveSegmentProtocol(target.Protocol, h.tunnelProtocolOf(tunnelID)),
		}
		// D3-04: 与 applyFederationRuntime 同口径附目标 share 凭据。
		if targetNode.IsRemote == 1 {
			rt.RelayUsername, rt.RelayPassword = parseRemoteShareRelayAuth(targetNode.RemoteConfig)
		}
		applyTargets = append(applyTargets, rt)
	}

	ownerRuntimeNode := tunnelRuntimeNode{NodeID: ownerNodeID, Protocol: "", Strategy: "round", ChainType: 2}
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

	// R8-T-06: ApplyRole is a runtime face; see
	// federationRuntimeClientTimeout.
	_, err = client.NewFederationClientWithTimeout(federationRuntimeClientTimeout).ApplyRole(remoteURL, remoteToken, h.federationLocalDomain(), client.RuntimeApplyRoleRequest{
		ResourceKey: strings.TrimSpace(binding.ResourceKey),
		Role:        "middle",
		Protocol:    effectiveSegmentProtocol(ownerRuntimeNode.Protocol, h.tunnelProtocolOf(tunnelID)),
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

// tunnelRelayMetadata 是 relay connector/handler 在给定隧道协议下的
// metadata 全集。本地链 builder（buildTunnelChainConfig /
// buildTunnelChainServiceConfig）与联邦 builder
// （buildFederationMiddleChainConfig / buildFederationServiceConfig）
// 共用同一构造，防止两套生成面参数漂移——F-UDP-7：联邦面曾缺
// mux.maxFrameSize / mux.maxStreamBuffer，其中 MaxStreamBuffer 有效
// 值 64KB（库默认） vs 本地 2MB，联邦端作接收方时单流在途窗被压小。
func tunnelRelayMetadata(protocol string) map[string]interface{} {
	metadata := map[string]interface{}{}
	if isTCPTunnelProtocol(protocol) {
		metadata["nodelay"] = true
		metadata["mux.keepaliveInterval"] = "15s"
		metadata["mux.keepaliveTimeout"] = "45s"
		metadata["mux.maxFrameSize"] = 32768
		metadata["mux.maxStreamBuffer"] = 2097152
	}
	if isKCPTunnelProtocol(protocol) {
		metadata["connectTimeout"] = "30s"
		metadata["mux.keepaliveInterval"] = "15s"
		metadata["mux.keepaliveTimeout"] = "45s"
		metadata["mux.maxFrameSize"] = 32768
		metadata["mux.maxStreamBuffer"] = 2097152
	}
	return metadata
}

// effectiveSegmentProtocol 是段协议的生成面统一口径（K1-05 收口）：
// 段行 protocol 优先、空回落隧道协议——与质量探针面
// chainSegmentProtocol（control_plane.go，自述为统一准绳）逐字
// 同义。此前 runtime 生成面与诊断组装面多处把空段回落字面量
// "tls"，与探针面分歧：存量空 protocol 行在隧道协议为 kcp 时会
// 生成 TLS 腿、探针面却按 UDP 段记「未测」。所有生成/组装面的
// 段协议回落一律经此函数（tunnelProtocol 由调用方按隧道解析，
// 见 tunnelKCPKey 与 applyTunnelRuntime/applyFederationRuntime
// 的 state 装配）。
func effectiveSegmentProtocol(segmentProtocol, tunnelProtocol string) string {
	return chainSegmentProtocol(segmentProtocol, tunnelProtocol)
}

// tunnelProtocolOf 返回隧道行自身的协议（归一化小写）。优先用
// state 在 apply 入口装配的 TunnelProtocol（调用方经 state 形参
// 传入当前 state 的值），未装配时回落查库；无 Handler、无 repo
// 或读失败时返回 ""，段回落随之退化为段行自身协议——存量段行
// protocol 恒非空（F-UDP-4 后创建/更新面强制必填），只有隧道与
// 段双空的极端存量行才退化到 builder 的非 KCP 分支。
func (h *Handler) tunnelProtocolOf(tunnelID int64, stateProtocol ...string) string {
	if len(stateProtocol) > 0 && strings.TrimSpace(stateProtocol[0]) != "" {
		return normalizeTunnelProtocol(stateProtocol[0])
	}
	if h == nil || h.repo == nil || tunnelID <= 0 {
		return ""
	}
	tr, err := h.repo.GetTunnelRecord(tunnelID)
	if err != nil || tr == nil {
		return ""
	}
	return normalizeTunnelProtocol(tr.Protocol)
}

// tunnelKCPKey 派生一条本地链隧道的 KCP 段密钥（K1-01）：仅当
// 隧道协议或任一段协议为 kcp 时才需要；非 KCP 隧道返回 "" 且不
// 报错。KCP 隧道而密钥派生失败（jwtSecret 缺失等）返回错误——
// 宁可整条隧道下发失败，也不生成回落公开默认密钥的 KCP 配置。
func (h *Handler) tunnelKCPKey(tunnelID int64, tunnelProtocol string, segmentProtocols ...string) (string, error) {
	needs := isKCPTunnelProtocol(tunnelProtocol)
	for _, p := range segmentProtocols {
		if isKCPTunnelProtocol(effectiveSegmentProtocol(p, tunnelProtocol)) {
			needs = true
			break
		}
	}
	if !needs {
		return "", nil
	}
	key := KCPKeyFor(h.jwtSecret, tunnelID)
	if key == "" {
		return "", errors.New("KCP 段密钥派生失败：面板密钥不可用")
	}
	return key, nil
}

func (h *Handler) buildTunnelChainConfig(tunnelID int64, fromNodeID int64, targets []tunnelRuntimeNode, nodes map[int64]*nodeRecord, ipPreference string, stateProtocol ...string) (map[string]interface{}, error) {
	fromNode := nodes[fromNodeID]
	if fromNode == nil {
		return nil, errors.New("节点不存在")
	}
	if len(targets) == 0 {
		return nil, errors.New("转发链目标不能为空")
	}
	tunnelProtocol := h.tunnelProtocolOf(tunnelID, stateProtocol...)
	protocols := make([]string, 0, len(targets))
	for _, target := range targets {
		protocols = append(protocols, target.Protocol)
	}
	kcpKey, err := h.tunnelKCPKey(tunnelID, tunnelProtocol, protocols...)
	if err != nil {
		return nil, err
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
		protocol := effectiveSegmentProtocol(target.Protocol, tunnelProtocol)
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
		if connectorMetadata := tunnelRelayMetadata(protocol); len(connectorMetadata) > 0 {
			connector["metadata"] = connectorMetadata
		}
		nodeItems = append(nodeItems, map[string]interface{}{
			"name":      fmt.Sprintf("node_%d", idx+1),
			"addr":      processServerAddress(fmt.Sprintf("%s:%d", host, port)),
			"connector": connector,
			"dialer":    buildTunnelDialerConfig(protocol, kcpKey),
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

func (h *Handler) buildTunnelChainServiceConfig(tunnelID int64, chainNode tunnelRuntimeNode, node *nodeRecord, nextHopCandidateCount int, stateProtocol ...string) []map[string]interface{} {
	if node == nil {
		return nil
	}
	// K1-05：段协议回落与生成/探针面统一（空→隧道协议），不再
	// 回落字面量 "tls"；K1-01：KCP 段密钥按隧道派生、与拨号面
	// 同源。密钥派生失败时产出空配置（整条服务缺席），由上层
	// apply 的服务数校验报错——绝不下发无钥 KCP 配置。
	tunnelProtocol := h.tunnelProtocolOf(tunnelID, stateProtocol...)
	protocol := effectiveSegmentProtocol(chainNode.Protocol, tunnelProtocol)
	kcpKey, err := h.tunnelKCPKey(tunnelID, tunnelProtocol, chainNode.Protocol)
	if err != nil {
		log.Printf("tunnel %d chain service config skipped: %v", tunnelID, err)
		return nil
	}
	handlerCfg := map[string]interface{}{
		"type": "relay",
	}
	handlerMetadata := tunnelRelayMetadata(protocol)
	// KU10: relay BIND 是逐跳门控、逐跳传递的命令——x 的 relay
	// handler 对 BIND 在每一跳 handler 都先查 enableBind，未开即
	// 回 "relay: BIND is disabled" 拒绝、根本不进 chain 转发
	// （Phase F 收尾复验真机定性：本地多跳链 middle 未开 bind
	// 时第一跳即 0/6，middle 日志实锤）。本 builder 产出的服务
	// 只有 ChainType 2（中段）与 ChainType 3（出口）两种，全部
	// 位于 BIND 逐跳路径上，故无条件开 bind opt-in；与联邦
	// builder 同口径（buildFederationServiceConfig）。KU9 的
	// 终端-only 口径（有 chain 即 forward、不查开关）已被真机
	// 证伪，勿回退。
	handlerMetadata["bind"] = true
	if len(handlerMetadata) > 0 {
		handlerCfg["metadata"] = handlerMetadata
	}
	if nextHopCandidateCount > 1 {
		handlerCfg["retries"] = nextHopCandidateCount - 1
	}
	service := map[string]interface{}{
		"name": fmt.Sprintf("tunnel_%d", tunnelID),
		// 监听地址用节点自身的监听地址（按协议分取，见
		// nodeListenAddrForProtocol）：chainNode.ConnectIP 是拨号
		// 语义（上一跳拨它到达本节点，见 buildTunnelChainConfig /
		// selectTunnelDialHost），不是本节点的本地地址。CNIX/NAT 线路入口
		// IP 在目标机上不可绑，拿它当监听地址 agent 必报
		// "cannot assign requested address"。
		// R32-FIX-F01：监听地址先剥离 :port 再与链端口拼接（须用
		// JoinHostPort：裸 IPv6 直拼会被 processServerAddress 误读，
		// 见 realm.go 同注释）。
		"addr":     processServerAddress(net.JoinHostPort(strings.Trim(stripListenAddrPort(nodeListenAddrForProtocol(node, protocol)), "[]"), strconv.Itoa(chainNode.Port))),
		"handler":  handlerCfg,
		"listener": buildTunnelListenerConfig(protocol, kcpKey),
	}
	if chainNode.ChainType == 2 {
		service["handler"].(map[string]interface{})["chain"] = fmt.Sprintf("chains_%d", tunnelID)
	}
	if chainNode.ChainType == 3 && strings.TrimSpace(node.InterfaceName) != "" {
		service["metadata"] = map[string]interface{}{"interface": node.InterfaceName}
	}
	return []map[string]interface{}{service}
}

// selectTunnelDialHost returns the bare host a tunnel hop dials to reach
// toNode. R35-FIX-F02：返回值须为单个裸 host——单次剥离对双端口值
// 是 no-op，残值原值穿透后下游再拼端口即成 "[IP:port]:port" 畸形
// 地址；此处统一校验，畸形值直接报错（裸 IPv6 主机原值放行，
// 主机名/单端口值不受影响；空值沿旧口径由调用方处置）。
func selectTunnelDialHost(fromNode, toNode *nodeRecord, ipPreference string, connectIp string) (string, error) {
	host, err := selectTunnelDialHostRaw(fromNode, toNode, ipPreference, connectIp)
	if err != nil || host == "" {
		return host, err
	}
	if !isBareDialHost(host) {
		return "", fmt.Errorf("节点「%s」的地址不正确", nodeDisplayName(toNode))
	}
	return host, nil
}

func selectTunnelDialHostRaw(fromNode, toNode *nodeRecord, ipPreference string, connectIp string) (string, error) {
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

// kcpCryptAlgorithm 是 KCP 段下发的加密算法（K1-01 修复）：
// kcp-go 的 BlockCrypt 支持集里 aes-256 是最稳妥的强档——其
// pbkdf2(key, salt) 派生恰好输出 32 字节，aes/aes-128/aes-192
// 会把派生结果截断到 16/24 字节，aes-256 全量使用。
const kcpCryptAlgorithm = "aes-256"

// KCPKeyFor 按作用域派生 KCP 段的加密密钥（K1-01 修复）：与
// SSUserPSK 同构的免存储确定性派生——HMAC-SHA256(面板 jwtSecret,
// 域分隔串 + scopeID)，两端（本地链的拨号/监听两侧、联邦的消费
// 方与提供方）用同一 (secret, scopeID) 必然算出同一把钥匙，无需
// 任何新增存储或跨面板协议字段。scopeID 在本地链为隧道 ID、在
// 联邦角色面为 share ID（PeerShareRuntime 行自带 ShareID，提供
// 方本地可达；消费方面板用自己库里的同一 share 行 ID）。密钥对
// 同一作用域恒定：隧道/份额重下发不换钥；jwtSecret 轮换（或面板
// 数据整体迁移）才会换钥，那时全网 KCP 腿须同批重下发。空 secret
// 或非法 scopeID 返回 ""，调用方必须拒绝生成 KCP 配置——绝不回
// 落到 kcp-go 的公开默认密钥（回落即漏洞保留）。
func KCPKeyFor(secret string, scopeID int64) string {
	if strings.TrimSpace(secret) == "" || scopeID <= 0 {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "flmx-kcp-key|%d", scopeID)
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// tunnelKCPMetadata 是 KCP 段 metadata 的单一真源（K1-02 收敛）：
// 此前 dialer/listener 两份逐字重复的 13 键字面量合并于此，两面
// 共用同一构造，改参只改一处。参数值维持现行缺省（fast3 / wnd
// 4096 / FEC 10+3），本批只收敛真源不改默认——FEC 等参数的产品
// 可配面尚无既有配置口径可挂，待产品定版时在此函数接入。
// kcpKey 为空时不写 kcp.key/kcp.crypt 两键（非 KCP 作用域的防御
// 分支）；KCP 作用域的调用方须先经 KCPKeyFor 校验非空再传入。
func tunnelKCPMetadata(kcpKey string) map[string]interface{} {
	metadata := map[string]interface{}{
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
	if strings.TrimSpace(kcpKey) != "" {
		metadata["kcp.key"] = kcpKey
		metadata["kcp.crypt"] = kcpCryptAlgorithm
	}
	return metadata
}

func buildTunnelDialerConfig(protocol, kcpKey string) map[string]interface{} {
	dialer := map[string]interface{}{
		"type": protocol,
	}
	if isKCPTunnelProtocol(protocol) {
		dialer["metadata"] = tunnelKCPMetadata(kcpKey)
	}
	return dialer
}

func buildTunnelListenerConfig(protocol, kcpKey string) map[string]interface{} {
	listener := map[string]interface{}{
		"type": protocol,
	}
	if strings.EqualFold(strings.TrimSpace(protocol), "udp") {
		// F-UDP-1: relay 是会话型协议，落在 UDP listener 上必须显式
		// keepAlive——缺省 false 时 x/listener/udp 的会话 conn 首写后
		// 自毁，后续帧被监听侧新建会话当 Request 头解析、报 bad
		// version（专项定案，自引入即不合）。与
		// buildForwardServiceConfigs 的 _udp 口径逐字一致
		// （keepAlive:true + ttl 30s）。本地链中段/出口与联邦
		// middle/exit 共用本 builder，一处收口覆盖全部会话型形态。
		listener["metadata"] = map[string]interface{}{
			"keepAlive": true,
			"ttl":       "30s",
		}
	}
	if isKCPTunnelProtocol(protocol) {
		listener["metadata"] = tunnelKCPMetadata(kcpKey)
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
		// R1-03-M1：落库前同判兜底——prepare 已对 state 定型值
		// 过门，本函数对 req 原文（游离段行不经 state）再过一遍
		// 链路策略合法集，集外值不得原文落库。
		strategy, err := normalizeChainStrategy(1, asString(n["strategy"]))
		if err != nil {
			return err
		}
		if err := h.repo.CreateChainTunnelTx(
			tx,
			tunnelID,
			"1",
			nodeID,
			sql.NullInt64{},
			strategy,
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
		portVal := sql.NullInt64{Int64: int64(port), Valid: true}
		if port <= 0 {
			// E-01：与 prepareTunnelCreateState（:5578）同口径——远端
			// 节点端口留 NULL，待联邦层预留后由 bindTx 写回；此前无
			// 条件 PickRandomNodePortTx 会把未预留的随机端口写进链行，
			// 无端口段分享更会误报"节点端口已满"。
			isRemote, remoteErr := h.repo.IsRemoteNodeTx(tx, nodeID)
			if remoteErr != nil {
				return remoteErr
			}
			if isRemote {
				portVal = sql.NullInt64{}
			} else {
				var pickErr error
				port, pickErr = h.repo.PickRandomNodePortTx(tx, nodeID, allocated, 0)
				if pickErr != nil {
					return pickErr
				}
				portVal = sql.NullInt64{Int64: int64(port), Valid: true}
			}
		}
		connectIp := asString(n["connectIp"])
		strategy, serr := normalizeChainStrategy(3, asString(n["strategy"]))
		if serr != nil {
			return serr
		}
		if err := h.repo.CreateChainTunnelTx(
			tx,
			tunnelID,
			"3",
			nodeID,
			portVal,
			strategy,
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
			portVal := sql.NullInt64{Int64: int64(port), Valid: true}
			if port <= 0 {
				// E-01：与 prepareTunnelCreateState（:5617）同口径——
				// 远端节点端口留 NULL（见出口分支注释）。
				isRemote, remoteErr := h.repo.IsRemoteNodeTx(tx, nodeID)
				if remoteErr != nil {
					return remoteErr
				}
				if isRemote {
					portVal = sql.NullInt64{}
				} else {
					var pickErr error
					port, pickErr = h.repo.PickRandomNodePortTx(tx, nodeID, allocated, 0)
					if pickErr != nil {
						return pickErr
					}
					portVal = sql.NullInt64{Int64: int64(port), Valid: true}
				}
			}
			connectIp := asString(n["connectIp"])
			strategy, serr := normalizeChainStrategy(2, asString(n["strategy"]))
			if serr != nil {
				return serr
			}
			if err := h.repo.CreateChainTunnelTx(
				tx,
				tunnelID,
				"2",
				nodeID,
				portVal,
				strategy,
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
// L4-03：第二返回值是「唯一入口/出口」点名清单——本节点是某隧道
// 唯一入口或唯一出口时，级联删其全部链路段行会让该隧道结构残缺，
// 此前无任何回执点名（brokenReferrers 只覆盖跨节点入站引用），现
// 在删除前查明、随回执显式披露（删除语义不变，仍尽力而为）。
// 漏斗分三段：守卫校验（checkNodeDeleteGuards，fail-closed）→
// 调用方的连带动作（如连带卸载，见 nodeDelete/nodeBatchDelete）→
// 级联段（deleteNodeByIDAfterGuards）。本函数是无连带动作时的组合
// 入口（创建补偿删除用），语义与分段前一致。
func (h *Handler) deleteNodeByID(id int64) ([]nodeCrossReferrer, []string, error) {
	guard, err := h.checkNodeDeleteGuards(id)
	if err != nil {
		return nil, nil, err
	}
	defer guard.releaseUpgradeOp(h, id)
	return h.deleteNodeByIDAfterGuards(id, guard)
}

// nodeDeleteGuard 是删节点漏斗第一段（守卫校验）的产物：跨节点
// 引用清单、唯一角色点名、级联删后要刷新的受影响入站集合。
// holdsUpgradeOp 标记守卫段已在 L2-10 在途登记里以「删除」占用本
// 节点（R9-06），由级联段 deleteNodeByIDAfterGuards 收尾释放。
type nodeDeleteGuard struct {
	brokenReferrers    []nodeCrossReferrer
	soleRoleWarnings   []string
	affectedInboundIDs []int64
	holdsUpgradeOp     bool
}

// releaseUpgradeOp 释放守卫段占用的在途登记（R4-F2-02）。释放权
// 归一：级联段入口即以 defer 调用本方法收尾，三个漏斗入口在守卫
// 成功后亦各自 defer 本方法兜底——此前释放只在级联段内 defer，
// 守卫与级联段之间的连带卸载步（prepareNodeAgentUninstall）若
// panic，HTTP recover 只救请求、级联段的 defer 从未注册，登记无
// TTL 永不释放，该节点升级/回退/再删恒被误拒至面板重启。
// 本方法以 holdsUpgradeOp 为唯一所有权标记：先到者释放并清标，
// 后到者 no-op——故入口的兜底 defer 不会误放级联段释放之后由
// 其他操作新占用的同节点登记（releaseNodeUpgradeOp 本身按节点
// 无条件删除、没有身份校验，所有权必须在本层收口）。
func (g *nodeDeleteGuard) releaseUpgradeOp(h *Handler, id int64) {
	if g == nil || !g.holdsUpgradeOp {
		return
	}
	g.holdsUpgradeOp = false
	h.releaseNodeUpgradeOp(id)
}

// checkNodeDeleteGuards 是漏斗的守卫校验段：任一守卫查询失败即
// fail-closed 返回错误、整次删除中止。单删/批删入口必须先取守卫、
// 全过之后才许执行连带卸载等不可逆的附带动作（在线 agent 自卸载
// 下发、待卸载登记），最后才进 deleteNodeByIDAfterGuards 的级联
// 段——顺序颠倒时守卫中止会留下「节点记录还在、agent 已不可逆
// 自卸载」的撕裂状态。
// R3X-017（E3-03）：删节点此前从不刷新白名单——级联删前在此收集
// 受影响的他入站（经 ChainTunnel 引用本节点 + 以本节点入站为链式
// 出站目标，见 inboundIDsAffectedByNodeDelete），级联删后由
// AfterGuards 统一刷新；单删与 nodeBatchDelete 共用本漏斗同步受益。
func (h *Handler) checkNodeDeleteGuards(id int64) (*nodeDeleteGuard, error) {
	guard, err := h.collectNodeDeleteGuard(id)
	if err != nil {
		return nil, err
	}
	// R9-06（I-F2-01）：守卫全过后、任何不可逆附带动作（连带卸载
	// 下发/待卸载登记）之前，删除漏斗接入 L2-10 同节点在途登记：
	// 升级/回退在途时删除在此被拒并点名在途操作（与升级/回滚三
	// 入口同文案），不再放行连带卸载与 agent 侧在途变更并发；同
	// 时以「删除」占用登记至级联段结束，删除进行期间升级类入口
	// 同理由登记被拒并得知在途的是删除。占用由 deleteNodeByID-
	// AfterGuards 收尾释放（三入口都必经级联段）。
	if existing, ok := h.tryAcquireNodeUpgradeOp(id, "删除"); !ok {
		return nil, fmt.Errorf("该节点已有%s操作正在进行，请等待其完成后再试", existing)
	}
	guard.holdsUpgradeOp = true
	return guard, nil
}

// collectNodeDeleteGuard 是守卫段的纯收集部分（引用清单/受影响集
// 合/唯一角色点名）：不碰在途登记，供守卫段与级联段落库前复检
// （R9-08）共用。
func (h *Handler) collectNodeDeleteGuard(id int64) (*nodeDeleteGuard, error) {
	guard := &nodeDeleteGuard{}
	if h != nil && h.repo != nil {
		// R3X-015：跨节点引用守卫查询失败 fail-closed——清单不可信
		// 时中止删除并上抛（单删/批删/创建补偿三入口同受此约束），
		// 不得记录日志后照删。
		// X04-001：整轮守卫共用一份全表快照——旧实现跨节点守卫按
		// 目标逐个重载全表、受影响收集再按全表逐行经 chainDialer-
		// NodeIDs 重载，删一个节点即 O(目标数+全表行数) 次全表扫描。
		targets, targetErr := h.repo.ListNodeInboundsByNode(id)
		if targetErr != nil {
			return nil, fmt.Errorf("查询节点 %d 的入站失败: %w", id, targetErr)
		}
		ix, ixErr := h.loadChainRefIndex()
		if ixErr != nil {
			if len(targets) > 0 {
				// 与逐目标 chainReferrers 首败的失败形态一致。
				return nil, fmt.Errorf("查询入站 %d 的链式引用方失败: %w", targets[0].ID, ixErr)
			}
			return nil, ixErr
		}
		guard.brokenReferrers = h.nodeCrossReferrersIx(targets, id, ix)
		// 同 R3X-015 口径：受影响入站收集失败同样中止删除。
		affected, collectErr := h.inboundIDsAffectedByNodeDeleteIx(id, ix)
		if collectErr != nil {
			return nil, collectErr
		}
		guard.affectedInboundIDs = affected
		// L4-03：唯一入口/出口点名收集，同守卫口径 fail-closed。
		soleRoles, soleErr := h.repo.ListSoleSegmentTunnelsForNode(id)
		if soleErr != nil {
			return nil, soleErr
		}
		if len(soleRoles) > 0 {
			nodeName := fmt.Sprintf("节点 %d", id)
			if n, nErr := h.repo.GetNodeRecord(id); nErr == nil && n != nil && strings.TrimSpace(n.Name) != "" {
				nodeName = "节点 " + n.Name
			}
			for _, sr := range soleRoles {
				role := "入口"
				if sr.ChainType == 3 {
					role = "出口"
				}
				tunnelName := strings.TrimSpace(sr.TunnelName)
				if tunnelName == "" {
					tunnelName = fmt.Sprintf("隧道 %d", sr.TunnelID)
				}
				guard.soleRoleWarnings = append(guard.soleRoleWarnings,
					fmt.Sprintf("隧道 %s 将失去唯一%s（%s 被删除后该隧道无%s节点）", tunnelName, role, nodeName, role))
			}
		}
	}
	return guard, nil
}

// deleteNodeByIDAfterGuards 是漏斗后段：数据面尽力清理 + DB 级联
// + 收尾（端口锁表、WS 会话、白名单刷新）。guard 须来自
// checkNodeDeleteGuards 且校验已全过；传 nil 时按空守卫处理（仅
// 内部兜底，正常入口不走）。
func (h *Handler) deleteNodeByIDAfterGuards(id int64, guard *nodeDeleteGuard) ([]nodeCrossReferrer, []string, error) {
	if guard == nil {
		guard = &nodeDeleteGuard{}
	}
	// R9-06：守卫段以「删除」占用的在途登记在本段收尾释放——单删/
	// 批删/创建补偿三入口都必经本段，无一遗漏。经 guard 的所有权
	// 标记释放（R4-F2-02）：本 defer 先行释放并清标，入口处兜底
	// defer 随后 no-op；仅当本段根本没被进入（连带步 panic）时才
	// 由入口兜底释放。
	defer guard.releaseUpgradeOp(h, id)
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
	// R9-08（I-F2-03）：落库前按最终引用集复检——守卫收集与级联删
	// 除之间隔着调用方的连带卸载步（在线回执等待最长 15s），期间
	// 新保存的跨节点引用不在守卫清单内，照旧删除会静默打断它且
	// 回执不点名，与 nodes.md 承诺不符。删除语义不变（尽力而为）：
	// 新引用并入回执清单、其入站并入下方白名单刷新集、唯一角色
	// 点名同理并入。复检查询失败不中止：不可逆的连带卸载可能已
	// 执行，此刻中止只会留下「agent 已卸载、记录还在」的撕裂态，
	// 按守卫时清单继续并记日志（守卫段本身仍 fail-closed）。
	if h != nil && h.repo != nil {
		if fresh, ferr := h.collectNodeDeleteGuard(id); ferr == nil {
			mergeNodeDeleteGuard(guard, fresh)
		} else {
			log.Printf("node delete: referrer recheck failed node_id=%d err=%v (continuing with guard-time snapshot)", id, ferr)
		}
	}
	if err := h.repo.DeleteNodeCascade(id); err != nil {
		return nil, nil, err
	}
	// port_alloc_lock 设计要求节点删除时清理锁表条目，此前无调用方。
	repo.CleanupPortAllocLock(id)
	// D2-05：删节点同时踢掉其 agent 的 WS 存量会话，否则幽灵会话
	// 会无限存活、继续上报并被广播（secret 已随行删除也拦不住）。
	if h.wsServer != nil {
		h.wsServer.DisconnectNode(id)
	}
	// R3X-017：级联删后统一刷新（漏斗收尾，见守卫段收集）。
	h.refreshInboundWhitelists(guard.affectedInboundIDs)
	return guard.brokenReferrers, guard.soleRoleWarnings, nil
}

// mergeNodeDeleteGuard 把落库前复检（R9-08）的新鲜守卫并入守卫时
// 守卫：跨节点引用按引用方入站 ID 去重并入、受影响入站并入、唯
// 一角色点名按文案去重并入。holdsUpgradeOp 不并（登记占用只属
// 守卫段那一份）。
func mergeNodeDeleteGuard(dst, fresh *nodeDeleteGuard) {
	if dst == nil || fresh == nil {
		return
	}
	seenRef := make(map[int64]struct{}, len(dst.brokenReferrers))
	for _, ref := range dst.brokenReferrers {
		seenRef[ref.ID] = struct{}{}
	}
	for _, ref := range fresh.brokenReferrers {
		if _, dup := seenRef[ref.ID]; dup {
			continue
		}
		seenRef[ref.ID] = struct{}{}
		dst.brokenReferrers = append(dst.brokenReferrers, ref)
	}
	seenInbound := make(map[int64]struct{}, len(dst.affectedInboundIDs))
	for _, inboundID := range dst.affectedInboundIDs {
		seenInbound[inboundID] = struct{}{}
	}
	for _, inboundID := range fresh.affectedInboundIDs {
		if _, dup := seenInbound[inboundID]; dup {
			continue
		}
		seenInbound[inboundID] = struct{}{}
		dst.affectedInboundIDs = append(dst.affectedInboundIDs, inboundID)
	}
	seenSole := make(map[string]struct{}, len(dst.soleRoleWarnings))
	for _, warning := range dst.soleRoleWarnings {
		seenSole[warning] = struct{}{}
	}
	for _, warning := range fresh.soleRoleWarnings {
		if _, dup := seenSole[warning]; dup {
			continue
		}
		seenSole[warning] = struct{}{}
		dst.soleRoleWarnings = append(dst.soleRoleWarnings, warning)
	}
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
		// C1-01：本函数的其余失败均为自著业务文案或记日志跳过，唯
		// 此处是仓储直通，标记后由各调用面按内部错误收口。
		return 0, markRepoError(err)
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
			log.Printf("pickTunnelPort: skipped entry node node_id=%d stage=port_range err=%v", nodeID, err)
			continue
		}
		if portRange == "" {
			portRange = "1000-65535"
		}

		nodePorts, err := parsePorts(portRange)
		if err != nil {
			log.Printf("pickTunnelPort: skipped entry node node_id=%d stage=parse_ports err=%v", nodeID, err)
			continue
		}

		used, err := h.getUsedPorts(nodeID)
		if err != nil {
			log.Printf("pickTunnelPort: skipped entry node node_id=%d stage=used_ports err=%v", nodeID, err)
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
			// E-01（F-PORT-01 未守住）：与 parsePortRangeSpec 同口径，
			// 区间钳到 1–65535——此前 "1-99999" 会把 >65535 的端口发
			// 给 pickTunnelPort/pickRandomPortForNode，随机挑中后由
			// 调用方以“端口必须在 1-65535 范围内”挡回（窄范围 100%
			// 失败）；全段越界的段直接丢弃（不报错，调用方按空集处理）。
			if start > 65535 || end < 1 {
				continue
			}
			if start < 1 {
				start = 1
			}
			if end > 65535 {
				end = 65535
			}
			for i := start; i <= end; i++ {
				ports = append(ports, i)
			}
		} else {
			p, err := strconv.Atoi(part)
			if err != nil {
				return nil, errors.New("invalid port value")
			}
			// E-01：单值越界同样丢弃（与 parsePortRangeSpec 同口径）。
			if p < 1 || p > 65535 {
				continue
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

// forwardPortByNode 索引转发当前 forward_port 行的 node→port，与
// syncTunnelForwardsEntryPorts 的 oldPortByNode 同口径（同节点多行
// 时后行覆盖；行序为 ListForwardPorts 的 id ASC）。
func forwardPortByNode(oldPorts []forwardPortRecord) map[int64]int {
	byNode := make(map[int64]int, len(oldPorts))
	for _, fp := range oldPorts {
		if fp.NodeID > 0 && fp.Port > 0 {
			byNode[fp.NodeID] = fp.Port
		}
	}
	return byNode
}

// forwardEntryPort 是 C-F3-01 的逐节点定口：已有行的入口节点沿用
// 该节点现口（2026-10-30 定案「入口端口创建后永不换口」），无行的
// 新节点才用规范口 fallback（MIN 口／迁移目标口）。异口行（同
// forward 不同入口节点不同口）的产生面是隧道新增入口节点时规范
// 口超新节点规格随机落口与备份导入逐行保真——任何以规范口单值
// 重建全行集的更新路径都会把非 min 行静默改口，故全部重建面必须
// 经此函数逐节点定口。
func forwardEntryPort(portByNode map[int64]int, nodeID int64, fallbackPort int) int {
	if port, ok := portByNode[nodeID]; ok {
		return port
	}
	return fallbackPort
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

	portByNode := forwardPortByNode(oldPorts)
	entries := make([]forwardPortReplaceEntry, 0, len(entryNodeIDs))
	for _, nid := range entryNodeIDs {
		entries = append(entries, forwardPortReplaceEntry{
			NodeID: nid,
			Port:   forwardEntryPort(portByNode, nid, port),
			InIP:   preservedByNode[nid],
		})
	}

	return entries
}

func (h *Handler) replaceForwardPorts(forwardID, tunnelID int64, port int, inIp string, oldPorts []forwardPortRecord) error {
	entryNodes, err := h.tunnelEntryNodeIDs(tunnelID)
	if err != nil {
		return err
	}
	portByNode := forwardPortByNode(oldPorts)
	entries := make([]forwardPortReplaceEntry, len(entryNodes))
	for i, nid := range entryNodes {
		entries[i] = forwardPortReplaceEntry{NodeID: nid, Port: forwardEntryPort(portByNode, nid, port), InIP: inIp}
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

	// R2 B09-001：同 userTunnelUpdate——显式在场的 flow 不可解析时
	// 直拒，不得被 asInt64/parseTrafficLimit 吞成 0 绕过 -1 哨兵的
	// 用户默认回填（assign {flow:"abc", flowMiB:0} 曾静默落 0=不限额，
	// 而非用户默认上限）。
	if presentNumericFieldInvalid(req, "flow") {
		return nil, fmt.Errorf("flow 参数无效")
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

	// R7-G-02：status / flowResetTime 合法集校验，与 user 面同口径
	// （O5-007/O5-008）。-1 为「未传」哨兵（走存量/用户默认回填，
	// 来源均已过门）；显式传入的越界值在此直拒，assign 与
	// batch-assign 共用本落点，一并覆盖。
	if reqStatus > 1 {
		return nil, fmt.Errorf("状态值无效，仅允许 0 或 1")
	}
	if reqFlowReset > 31 {
		return nil, fmt.Errorf("流量重置日无效，仅允许 0-31（0 表示不自动重置）")
	}

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
	// R3 B09-002（分诊 §5.2 方案 ①）：枚举该用户全部转发，不限
	// status=1。此前只重下发启用中的转发，暂停中的转发被跳过后，
	// 其节点上的 user_conn_limit 停留在旧值、恢复路径又不补下发，
	// 与 DB 永久分叉且无告警。syncForwardServicesWithWarnings 内部
	// 对 status!=1 的转发在推送后复位暂停（与限速规则/用户隧道两条
	// 重下发面同形），故全量枚举不改变暂停态语义。
	forwards, err := h.repo.ListForwardsByUser(userID)
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

// normalizeSpeedLimitReference validates an explicitly passed speed-limit
// reference. R35-F-05 (R35-L-02): nil stays nil (no reference requested)
// and a non-positive id is treated as no reference (not a real rule id);
// a positive id must resolve to an existing, enabled rule. A missing rule
// and a disabled rule are distinct, explicit errors — previously both were
// silently folded to nil, so editing a forward after its rule was disabled
// quietly dropped the limit (and re-enabling the rule could not restore
// it). O2-005 only requires a disabled rule not to take effect; it does
// not license silently discarding the reference. The status=1 effect
// predicate itself (speedLimitExists / GetSpeedLimitSpeed) is untouched.
func (h *Handler) normalizeSpeedLimitReference(speedID *int64) (*int64, error) {
	if speedID == nil || *speedID <= 0 {
		return nil, nil
	}
	exists, err := h.repo.SpeedLimitExists(*speedID)
	if err != nil {
		return nil, markRepoError(err)
	}
	if exists {
		return speedID, nil
	}
	return nil, h.speedLimitReferenceError(*speedID)
}

func (h *Handler) normalizeSpeedLimitReferenceTx(tx *gorm.DB, speedID *int64) (*int64, error) {
	if speedID == nil || *speedID <= 0 {
		return nil, nil
	}
	exists, err := h.repo.SpeedLimitExistsTx(tx, *speedID)
	if err != nil {
		return nil, err
	}
	if exists {
		return speedID, nil
	}
	return nil, h.speedLimitReferenceErrorTx(tx, *speedID)
}

// speedLimitReferenceError distinguishes the two ways a reference can
// fail the status=1 gate: the rule does not exist at all, or it exists
// but is disabled. The row is read without the status predicate so the
// error can name the actual state (GetSpeedLimit cannot serve this — it
// filters status=1 and cannot tell the two apart).
func (h *Handler) speedLimitReferenceError(id int64) error {
	row, err := h.repo.GetSpeedLimit(id)
	if err != nil {
		return markRepoError(err)
	}
	return speedLimitReferenceErrorForRow(id, row)
}

func (h *Handler) speedLimitReferenceErrorTx(tx *gorm.DB, id int64) error {
	row, err := h.repo.GetSpeedLimitTx(tx, id)
	if err != nil {
		return err
	}
	return speedLimitReferenceErrorForRow(id, row)
}

func speedLimitReferenceErrorForRow(id int64, row *model.SpeedLimit) error {
	if row == nil {
		return fmt.Errorf("限速规则不存在（id=%d）", id)
	}
	return fmt.Errorf("限速规则已停用（id=%d）", id)
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

// validateSpeedLimitSpeed / validateSpeedLimitStatus 是 B09-001 的
// 限速合法集（create/update/import 三载体同口径）：speed 必须 ≥1、
// status ∈{0,1}。speed<=0 的规则此前可创建、可被引用，但下发面以
// speed>0 为门控静默丢弃——限速实际不存在且无任何警告；写侧拒绝
// 与 parseTrafficLimit/validateTunnelTrafficRatio 同格点名字段。
func validateSpeedLimitSpeed(speed int) error {
	if speed < 1 {
		return fmt.Errorf("限速 speed 必须大于等于 1，当前 %d", speed)
	}
	return nil
}

func validateSpeedLimitStatus(status int) error {
	if status != 0 && status != 1 {
		return fmt.Errorf("限速 status 只能为 0 或 1，当前 %d", status)
	}
	return nil
}

// validateNodePortSpec 是 B04-003 的节点 port 写侧校验：接受集与
// 消费面 parsePortRangeSpec 同口径（逗号多段、单值或 start-end 区
// 间、端口 1-65535），但写侧对不可解析、越界、反序一律拒绝而非
// 静默丢弃/交换/钳制——非法值原样落库会在下游 fail-soft 后远离
// 输入处爆发。
func validateNodePortSpec(spec string) error {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return errors.New("端口范围不能为空")
	}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return fmt.Errorf("端口范围格式无效: %q", spec)
		}
		if strings.Contains(part, "-") {
			r := strings.SplitN(part, "-", 2)
			start, err1 := strconv.Atoi(strings.TrimSpace(r[0]))
			end, err2 := strconv.Atoi(strings.TrimSpace(r[1]))
			if err1 != nil || err2 != nil {
				return fmt.Errorf("端口范围格式无效: %q", part)
			}
			if start < 1 || start > 65535 || end < 1 || end > 65535 {
				return fmt.Errorf("端口必须在 1-65535 范围内: %q", part)
			}
			if end < start {
				return fmt.Errorf("端口范围起始不能大于结束: %q", part)
			}
			continue
		}
		p, err := strconv.Atoi(part)
		if err != nil {
			return fmt.Errorf("端口范围格式无效: %q", part)
		}
		if p < 1 || p > 65535 {
			return fmt.Errorf("端口必须在 1-65535 范围内: %q", part)
		}
	}
	return nil
}

// isUniqueConstraintErr 识别两方言的唯一约束冲突短语（与 repo
// FIX132-C 的 isUniqueConstraintViolation 同短语集），供 handler
// 把 DB 原文错误转译为人话文案，不泄漏表名/驱动错误码（B03-002）。
func isUniqueConstraintErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "duplicate key value")
}

// passwordTooShort 是 create/update 共用的密码最小长度口径
// （B02-002：此前只有 userCreate 校验 ≥6 位，userUpdate 改密无
// 下限，两面共用本 helper 防再分叉）。
func passwordTooShort(pwd string) bool {
	return len([]rune(pwd)) < 6
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

// requirePost 是写面的方法守卫（B03-002 同族口径）：仅接受 POST，
// 非 POST 回「请求失败」且不执行任何写操作。已守卫面多为内联同形
// 校验，本助手供新增/补守卫面单点复用；返回 false 时回执已写出，
// 调用方直接 return。
func requirePost(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return false
	}
	return true
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

// maxBatchIDs 是批量端点单次请求的 ID 条数上限（R10-08/I-F2-01）。
// 此前只有 node/batch-upgrade 有帽（maxBatchUpgradeNodes=50，资源
// 理由的在仓先例），其余批量路由只受 10MB 请求体兜底（约 120 万个
// 短 ID/请求），而各 handler 逐条执行真实工作（级联删除、数据面
// 清理/重部署、逐条权限校验）并逐条累积失败明细——5 万个不存在
// ID 的 forward 批删实测 58s＋2.1MB 回执，普通用户可达。帽落在
// 各解析点的去重之后（重复条目不占额度），与批量升级的 50 帽
// 并存（彼更严、先行判定、原样保留）。
const maxBatchIDs = 1000

// checkBatchIDsCap 在批量入口完成其 ID 列表归一（去重/过滤）后
// 校验条数；超限时写出点名拒绝回执并返回 false，调用方直接
// return。n 传归一后的条数（idsFromBody 系为去重后 valid 数；
// idsFromBodyWithInvalid 系为去重后 valid＋invalid 总数——invalid
// 条目同样逐条生成失败明细，计入工作量）。
func checkBatchIDsCap(w http.ResponseWriter, n int) bool {
	if n > maxBatchIDs {
		response.WriteJSON(w, response.ErrDefault(fmt.Sprintf("单次批量操作最多 %d 条，请分批提交", maxBatchIDs)))
		return false
	}
	return true
}

// parseIDsFromBody 解析批量请求体的 ids 数组（原始值，含 <=0
// 条目）；失败时已写出错误回执，ok=false，调用方直接 return。
func parseIDsFromBody(r *http.Request, w http.ResponseWriter) (ids []int64, ok bool) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return nil, false
	}
	var req map[string]interface{}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return nil, false
	}
	arr := asAnySlice(req["ids"])
	if len(arr) == 0 {
		response.WriteJSON(w, response.ErrDefault("ids不能为空"))
		return nil, false
	}
	ids = make([]int64, 0, len(arr))
	for _, x := range arr {
		ids = append(ids, asInt64(x, 0))
	}
	return ids, true
}

func idsFromBody(r *http.Request, w http.ResponseWriter) []int64 {
	raw, ok := parseIDsFromBody(r, w)
	if !ok {
		return nil
	}
	ids := make([]int64, 0, len(raw))
	for _, id := range raw {
		if id > 0 {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	// R2-040：去重（与 nodeBatchUpgrade 的 uniqueInt64s 口径对齐）。
	// 全部调用面都是对 id 集合的批处理（入站批删/隧道批删/隧道批量
	// 重部署），重复 id 无语义：不去重时删除面第二次迭代必然撞上
	// 已删行、记一条虚假失败，重部署面则重复执行并重复计数。
	ids = uniqueInt64s(ids)
	// R10-08：去重后计数帽（见 maxBatchIDs）。超限时已写出拒绝
	// 回执，返回 nil 与解析失败同口径，调用方直接 return。
	if !checkBatchIDsCap(w, len(ids)) {
		return nil
	}
	return ids
}

// idsFromBodyWithInvalid 与 idsFromBody 同源解析，但把 id<=0 的条目
// 单列返回（B08-003）：forward 批量入口据此把非法 id 与不存在 id
// 同口径计入 failures 回执，而不是在计数前就静默过滤掉（旧行为
// 下 {"ids":[0]} 会得到 successCount=0/failCount=0 的全成功回执）。
// valid 保持升序（与 idsFromBody 一致）；invalid 按请求顺序保留。
// 解析失败时已写出错误回执，valid 返回 nil，调用方直接 return。
//
// C6-01：valid 段排序后同样过 uniqueInt64s（与 idsFromBody 的
// R2-040 口径一致）——重复 valid id 指向同一实体、不是两次独立
// 的客户端错误，不去重时删除面第二次迭代必然撞上已删行记虚假
// 失败、暂停/重部署面重复执行并重复计数、nft 恢复面配额预留按
// 条目重复自增挤占同批相异条目。invalid 段一字不动：B08-003 的
// 逐条计数契约只覆盖该段。
func idsFromBodyWithInvalid(r *http.Request, w http.ResponseWriter) (valid []int64, invalid []int64) {
	raw, ok := parseIDsFromBody(r, w)
	if !ok {
		return nil, nil
	}
	valid = make([]int64, 0, len(raw))
	invalid = make([]int64, 0)
	for _, id := range raw {
		if id > 0 {
			valid = append(valid, id)
		} else {
			invalid = append(invalid, id)
		}
	}
	sort.Slice(valid, func(i, j int) bool { return valid[i] < valid[j] })
	valid = uniqueInt64s(valid)
	// R10-08：计数帽（见 maxBatchIDs）——valid 去重后与 invalid
	// 合计计数（invalid 逐条生成失败明细、同属逐条工作量）。
	// 超限时已写出拒绝回执，返回 (nil, nil) 与解析失败同口径。
	if !checkBatchIDsCap(w, len(valid)+len(invalid)) {
		return nil, nil
	}
	return valid, invalid
}

// dedupeValidBatchIDs 对自有 id 列表字段的批量请求（如
// forwardBatchChangeTunnel 的 ForwardIDs）做 valid 段去重，保留
// 首次出现顺序；invalid 条目（id<=0）原样保留、仍由调用方逐条计
// 失败（B08-003）。与 idsFromBodyWithInvalid 的 C6-01 口径一致：
// 重复 valid id 不是两次独立的客户端错误。
func dedupeValidBatchIDs(ids []int64) []int64 {
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id > 0 {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
		}
		out = append(out, id)
	}
	return out
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

// nonPositiveRawIDs 返回原始 JSON 数组中的非正整数元素（B03-001）。
// asInt64Slice 的语义是静默过滤 id<=0（只读拼装面适用）；写面在
// 过滤前须以本助手对原始值做 fail-closed 检查，否则 [-5] 会被等价
// 处理为「空集/未指定」。
func nonPositiveRawIDs(v interface{}) []int64 {
	bad := make([]int64, 0)
	for _, x := range asAnySlice(v) {
		if id := asInt64(x, 0); id <= 0 {
			bad = append(bad, id)
		}
	}
	return bad
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
	if err != nil {
		return nil // 规格不可解析时保持既有宽松行为，不新设拒绝面
	}
	if len(ports) == 0 {
		// E-01：parsePorts 钳制后，可解析但全段越界的规格（如
		// "70000-80000"）返回空集、无错误；此前同一规格下合法端口
		// 均不在集合中而被拒，空集不能反转为放行，仍按“不在允许范围”
		// 拒绝。无数字的垃圾规格（如 ","）仍保持既有宽松行为。
		if strings.ContainsAny(portRange, "0123456789") {
			return fmt.Errorf("端口 %d 不在节点 %s 允许范围 %s 内（该范围仅约束转发/中转/隧道入口端口）", port, node.Name, portRange)
		}
		return nil
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
	return h.validateForwardEntryNodesWithPortFunc(entryNodes, func(int64) int { return port }, currentForwardID)
}

// validateForwardEntryNodesWithPortFunc 是 validateForwardEntryNodes
// 的逐节点定口变体（C-F3-01）：每个入口节点按 portForNode 给出的
// 该节点实际落库端口做远程范围/本地规格/占用校验，使异口转发的
// 保留节点按现口受检、新节点按规范口受检，不再以单值口对全节点
// 一刀切（单值口在非 min 节点越规格或被占时，未换口的编辑会被误
// 拒且无自愈路径）。
func (h *Handler) validateForwardEntryNodesWithPortFunc(entryNodes []int64, portForNode func(nodeID int64) int, currentForwardID int64) error {
	for _, nodeID := range entryNodes {
		node, nodeErr := h.getNodeRecord(nodeID)
		if nodeErr != nil {
			return nodeErr
		}
		port := portForNode(nodeID)
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
		return markRepoError(err)
	}
	if conflict == nil {
		return nil
	}
	if conflict.Status == model.ForwardStatusPendingDelete {
		return fmt.Errorf("节点 %s 端口 %d 正等待节点清理（转发 %s），请稍后重试或联系管理员",
			node.Name, port, conflict.Name)
	}
	// L9 观察 2：按占用来源措辞，入站/链路段/联邦预留不再误报转发占用。
	return errors.New(forwardPortConflictMessage(node, port, conflict))
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
			// E-01：与 repo 的 parsePortRangeMinMax 同口径，上界钳到
			// 65535（此前孪生实现未钳制；目前零调用方，仍先钳住，
			// 防止后人接线后重蹈 E-01）。
			if start > 65535 {
				continue
			}
			if end > 65535 {
				end = 65535
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
		if err != nil || p <= 0 || p > 65535 {
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
