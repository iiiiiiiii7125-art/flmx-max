package ws

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strconv"
	"sync"
	"time"

	"go-backend/internal/auth"
)

// WSTicketTTL 是浏览器实时通道票据的有效期（R-WS-TICKET）：浏览器
// 在每次（重）连前经 POST /api/v1/system-info/ticket 以登录态 JWT
// 换票，WS 握手只认票据、拒绝查询参数里的裸 JWT。票据须短期
// （规格 ≤60s，取 30s）、单次使用、绑定用户与签发时的吊销纪元。
const WSTicketTTL = 30 * time.Second

// wsTicketMaxOutstanding 是未消费票据的在册上限（与 D1-02 认领集
// 同形态的进程内有界集合，防滥发耗尽内存）。
const wsTicketMaxOutstanding = 8192

// wsTicket 是一张已签发票据的绑定信息：消费时与用户当前状态复核
// （状态/角色/吊销纪元），任一漂移即拒。
type wsTicket struct {
	userID    int64
	roleID    int
	user      string
	iatMs     int64 // 签发所用 JWT 的 IatMs（供既有会话校验链复用）
	expUnix   int64 // 签发所用 JWT 的 Exp（秒）
	epoch     int64 // 签发时该用户的吊销纪元 RevocationWatermark()
	expiresAt time.Time
}

// wsTicketStore 是进程内票据表：不落库（进程重启即全失效，浏览器
// 重连时重新取票即可）；签发时顺带清理过期项，消费即删除（单次
// 使用——包括校验失败的消费尝试，防止对同一票据反复试探）。
type wsTicketStore struct {
	mu   sync.Mutex
	byID map[string]wsTicket
	now  func() time.Time
}

func newWSTicketStore() *wsTicketStore {
	return &wsTicketStore{byID: make(map[string]wsTicket), now: time.Now}
}

func (st *wsTicketStore) issue(t wsTicket) (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	id := base64.RawURLEncoding.EncodeToString(raw[:])

	st.mu.Lock()
	defer st.mu.Unlock()
	now := st.now()
	for k, v := range st.byID {
		if !now.Before(v.expiresAt) {
			delete(st.byID, k)
		}
	}
	if len(st.byID) >= wsTicketMaxOutstanding {
		return "", errors.New("too many outstanding ws tickets")
	}
	t.expiresAt = now.Add(WSTicketTTL)
	st.byID[id] = t
	return id, nil
}

// IssueWSTicket 为一次浏览器 WS 握手签发票据（R-WS-TICKET）。调用方
// （HTTP handler）已完成 JWT 全量复核，传入 claims 字段与该用户当前
// 的吊销纪元；票据与这些绑定一同入册，消费时逐项复核。
func (s *Server) IssueWSTicket(userID int64, roleID int, user string, iatMs, expUnix, epoch int64) (string, error) {
	if s == nil || s.tickets == nil {
		return "", errors.New("ws ticket store not initialized")
	}
	return s.tickets.issue(wsTicket{
		userID:  userID,
		roleID:  roleID,
		user:    user,
		iatMs:   iatMs,
		expUnix: expUnix,
		epoch:   epoch,
	})
}

// consumeWSTicketClaims 消费一张票据并复核其绑定：用户状态/角色在
// 签发后漂移、或吊销纪元被改密/登出（E1-02）推进，均使未消费票据
// 作废；签发所用 JWT 本身已过期同理。通过后返回与该 JWT 等价的
// claims，供 ServeHTTP 的既有 admin/monitor 分发链原样使用。
func (s *Server) consumeWSTicketClaims(id string) (auth.Claims, bool) {
	if s == nil || s.tickets == nil {
		return auth.Claims{}, false
	}
	t, ok := s.tickets.consume(id)
	if !ok {
		return auth.Claims{}, false
	}
	if t.expUnix <= time.Now().Unix() {
		return auth.Claims{}, false
	}
	if s.getUserAuthState != nil {
		state, err := s.getUserAuthState(t.userID)
		if err != nil || state == nil || state.Status != 1 || state.RoleID != t.roleID || state.RevocationWatermark() != t.epoch {
			return auth.Claims{}, false
		}
	}
	return auth.Claims{
		Sub:    strconv.FormatInt(t.userID, 10),
		IatMs:  t.iatMs,
		Exp:    t.expUnix,
		User:   t.user,
		Name:   t.user,
		RoleID: t.roleID,
	}, true
}

// consume 取出并烧毁一张票据：不存在、已消费或已过期均返回 false。
func (st *wsTicketStore) consume(id string) (wsTicket, bool) {
	if id == "" {
		return wsTicket{}, false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	t, ok := st.byID[id]
	if !ok {
		return wsTicket{}, false
	}
	delete(st.byID, id)
	if !st.now().Before(t.expiresAt) {
		return wsTicket{}, false
	}
	return t, true
}
