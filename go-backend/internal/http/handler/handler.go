package handler

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go-backend/internal/auth"
	"go-backend/internal/health"
	"go-backend/internal/http/middleware"
	"go-backend/internal/http/response"
	"go-backend/internal/metrics"
	"go-backend/internal/monitoring"
	runtimenft "go-backend/internal/runtime/nftables"
	"go-backend/internal/security"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
	"go-backend/internal/ws"
)

type Handler struct {
	repo        *repo.Repository
	jwtSecret   string
	wsServer    *ws.Server
	metrics     *metrics.IngestionService
	healthCheck *health.Checker
	// R5-B5 D14: 下沉到 manager 层的 per-node 串行化包装。
	// 所有触碰节点 nft 状态的方法必须经此，不依赖调用方自觉。
	nftablesManager *serializedNftManager

	// R5-B5 D14: per-node single-flight 已下沉到 serializedNftManager，
	// 此处不再保留 handler 层锁。
	// R5-B3 D1-003/D9: 异步自动 reconcile（R2：采集循环只投递，不在事务内执行）。
	nftAutoMu      sync.Mutex
	nftAutoPending map[int64]bool
	nftAutoQueue   chan int64
	nftAutoFail    map[int64]*nftAutoFailState
	// R5-B3 D1-003/D9(b): 最近一次 apply 失败的节点（节点恢复后触发 reconcile）。
	nftApplyFailedMu sync.Mutex
	nftApplyFailed   map[int64]bool

	peerResourceMu sync.Mutex
	captchaMu      sync.Mutex
	captchaTokens  map[string]int64
	// R10X-008（A-01）：captcha verify 端点独立 IP 限流桶（与登录
	// 桶分开计量，形态对齐 loginRateLimiter：窗口计数 + 上限淘汰）。
	captchaVerifyMu      sync.Mutex
	captchaVerifyBuckets map[string]*loginAttemptBucket
	passkeyMu            sync.Mutex
	passkeyPending       map[string]passkeyCeremony

	jobsMu      sync.Mutex
	jobsCtx     context.Context
	jobsCancel  context.CancelFunc
	jobsStarted bool
	jobsWG      sync.WaitGroup

	// FIX-D（D6-07）：通知派发有界队列 + worker 池状态（notify.go）。
	notifyMu      sync.Mutex
	notifyQueue   chan notifyJob
	notifyCancel  context.CancelFunc
	notifyRunning bool
	notifyWG      sync.WaitGroup
	// 通知配置读取缓存（短 TTL + 配置写路径显式失效），消除每条通知
	// 一次 vite_config 全表扫描。
	notifyCfgMu   sync.Mutex
	notifyCfgVals map[string]string
	notifyCfgAt   time.Time

	// R2X-018：日报待发原文缓存（notify_reports.go）。首次生成后
	// 同日重试逐字复用，snapshot 与 daily_last 仅整条投递成功时推进。
	dailyMu      sync.Mutex
	dailyPending *dailyReportPending

	upgradeMu              sync.Mutex
	systemUpgradeMu        sync.Mutex
	pendingUpgradeRedeploy map[int64]struct{}
	// FIX2-A（V2-04）：节点离线期间切走 realm 的待结算标记（按节点）。
	// 离线切换时 settle 轮询够不到节点，重连后必须先结算 realm 计数
	// 再允许重下发，否则增量随 agent 弃 realm 计数永久漏计。
	pendingRealmSettle map[int64]struct{}
	// FIX2-A（V2-01/V2-02）：配额执法 per-user 串行化锁。
	quotaEnforceMu    sync.Mutex
	quotaEnforceLocks map[int64]*sync.Mutex
	// FIX2-A（V2-08）：转发计数入账 per-node 串行化锁（基线读→差分
	// →落库整段），防 settle/周期/重连多路轮询以同一旧基线重复差分。
	nodeAccountingMu            sync.Mutex
	nodeAccountingLocks         map[int64]*sync.Mutex
	nodeOnlineRedeployAt        map[int64]time.Time
	nodeOnlineRedeployQueued    map[int64]struct{}
	nodeOnlineRedeploying       map[int64]struct{}
	nodeLocalRuntimeRetryQueued map[int64]struct{}

	qualityProber  *tunnelQualityProber
	externalProbes *externalProbeStore
	// 移植自 kure29/flyx：节点延迟测试点测量结果缓存。
	nodeLatency *nodeLatencyStore
	// 移植自 kure29/flyx：节点回程路由追踪结果缓存。
	nodeRoutes *nodeRouteStore
	// 移植自 kure29/flyx：延迟连续劣化告警的连击计数（node_latency_history.go）。
	latencyAlerts *latencyAlertTracker
	relayLatency  *relayLatencyStore
	notifier      *notifier
	bestExit      *bestExitManager

	// 移植自 kure29/flyx 3.3.30/3.3.33（DA-6/DB-5）：每节点 TcpPing
	// 并发槽位（队列与执行共用超时预算，见 node_ping_slots.go）。
	pingSlots *nodePingSlots
	// 移植自 kure29/flyx 3.3.33（DB-3）：节点重连后自动重测队列
	// （见 node_reconnect_probe.go），由 StartBackgroundJobs 创建。
	reconnectProbes *nodeReconnectProbeQueue
	// R1-B-04：内核在节点保持在线时单独重启的检测基线（agent 状态
	// 报告的 Restarts/StartedAt 上次值），见 noteKernelStart。
	kernelStartsMu sync.Mutex
	kernelStarts   map[kernelStartKey]kernelStartMark
}

const monitorTunnelQualityEnabledConfigKey = "monitor_tunnel_quality_enabled"
const allowLocalRemoteAddrConfigKey = "allow_local_remote_addr"

type loginRequest struct {
	Username  string `json:"username"`
	Password  string `json:"password"`
	CaptchaID string `json:"captchaId"`
}

type captchaVerifyRequest struct {
	ID   string `json:"id"`
	Data string `json:"data"`
}

type nameRequest struct {
	Name string `json:"name"`
}

type configSingleRequest struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type changePasswordRequest struct {
	NewUsername     string `json:"newUsername"`
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
	ConfirmPassword string `json:"confirmPassword"`
}

type flowItem struct {
	N string `json:"n"`
	U int64  `json:"u"`
	D int64  `json:"d"`
}

const (
	pngDataURLPrefix          = "data:image/png;base64,"
	maxBrandAssetDataURLBytes = 1024 * 1024
)

func New(repo *repo.Repository, jwtSecret string) *Handler {
	h := &Handler{
		repo:                     repo,
		jwtSecret:                jwtSecret,
		wsServer:                 ws.NewServer(repo, jwtSecret),
		metrics:                  metrics.NewIngestionService(repo),
		healthCheck:              nil,
		nftablesManager:          newSerializedNftManager(runtimenft.NewManager(nil)),
		nftAutoPending:           make(map[int64]bool),
		nftAutoQueue:             make(chan int64, 64),
		nftAutoFail:              make(map[int64]*nftAutoFailState),
		nftApplyFailed:           make(map[int64]bool),
		captchaTokens:            make(map[string]int64),
		captchaVerifyBuckets:     make(map[string]*loginAttemptBucket),
		passkeyPending:           make(map[string]passkeyCeremony),
		pendingUpgradeRedeploy:   make(map[int64]struct{}),
		pendingRealmSettle:       make(map[int64]struct{}),
		quotaEnforceLocks:        make(map[int64]*sync.Mutex),
		nodeAccountingLocks:      make(map[int64]*sync.Mutex),
		nodeOnlineRedeployAt:     make(map[int64]time.Time),
		nodeOnlineRedeployQueued: make(map[int64]struct{}),
		nodeOnlineRedeploying:    make(map[int64]struct{}),
		bestExit:                 newBestExitManager(),
		externalProbes:           newExternalProbeStore(),
		nodeLatency:              newNodeLatencyStore(),
		nodeRoutes:               newNodeRouteStore(),
		latencyAlerts:            newLatencyAlertTracker(),
		relayLatency:             newRelayLatencyStore(),
		notifier:                 newNotifier(),
		pingSlots:                newNodePingSlots(),
	}
	h.healthCheck = health.NewChecker(repo, h.wsServer)
	h.qualityProber = newTunnelQualityProber(h)
	h.wsServer.SetNodeOnlineHook(h.onNodeOnline)
	h.wsServer.SetNodeOfflineHook(h.notifyNodeOffline)
	h.wsServer.SetNodeMetricHook(func(nodeID int64, info ws.SystemInfo) {
		metricInfo := metrics.SystemInfo{
			Uptime:           info.Uptime,
			BytesReceived:    info.BytesReceived,
			BytesTransmitted: info.BytesTransmitted,
			BootID:           info.BootID,
			CPUUsage:         info.CPUUsage,
			MemoryUsage:      info.MemoryUsage,
			DiskUsage:        info.DiskUsage,
			Load1:            info.Load1,
			Load5:            info.Load5,
			Load15:           info.Load15,
			TCPConns:         info.TCPConns,
			UDPConns:         info.UDPConns,
			NetInSpeed:       info.NetInSpeed,
			NetOutSpeed:      info.NetOutSpeed,
		}
		h.metrics.RecordNodeMetric(nodeID, metricInfo)
	})
	h.wsServer.SetUserAuthStateLookup(h.GetUserAuthState)
	return h
}

func (h *Handler) WebSocketHandler() http.Handler {
	return h.wsServer
}

func (h *Handler) GetUserAuthState(userID int64) (*auth.UserAuthState, error) {
	return h.repo.GetUserAuthState(userID)
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/user/login", h.login)
	mux.HandleFunc("/api/v1/user/passkey/status", h.passkeyStatus)
	mux.HandleFunc("/api/v1/user/passkey/login/begin", h.passkeyLoginBegin)
	mux.HandleFunc("/api/v1/user/passkey/login/finish", h.passkeyLoginFinish)
	mux.HandleFunc("/api/v1/user/passkey/register/begin", h.passkeyRegisterBegin)
	mux.HandleFunc("/api/v1/user/passkey/register/finish", h.passkeyRegisterFinish)
	mux.HandleFunc("/api/v1/user/passkey/list", h.passkeyList)
	mux.HandleFunc("/api/v1/user/passkey/delete", h.passkeyDelete)
	mux.HandleFunc("/api/v1/user/list", h.userList)
	mux.HandleFunc("/api/v1/user/create", h.userCreate)
	mux.HandleFunc("/api/v1/user/update", h.userUpdate)
	mux.HandleFunc("/api/v1/user/delete", h.userDelete)
	mux.HandleFunc("/api/v1/user/reset", h.userResetFlow)
	mux.HandleFunc("/api/v1/user/quota/reset", h.userQuotaReset)
	mux.HandleFunc("/api/v1/user/groups", h.userGroups)
	mux.HandleFunc("/api/v1/public/config/get", h.getPublicConfigByName)
	mux.HandleFunc("/api/v1/config/get", h.getConfigByName)
	mux.HandleFunc("/api/v1/config/list", h.getConfigs)
	mux.HandleFunc("/api/v1/config/update", h.updateConfigs)
	mux.HandleFunc("/api/v1/config/update-single", h.updateSingleConfig)
	mux.HandleFunc("/api/v1/system/storage", h.storageSummary)
	mux.HandleFunc("/api/v1/system/version", h.systemVersion)
	mux.HandleFunc("/api/v1/system/check-updates", h.systemCheckUpdates)
	mux.HandleFunc("/api/v1/system/upgrade", h.systemUpgrade)
	mux.HandleFunc("/api/v1/system/upgrade/status", h.systemUpgradeStatus)
	mux.HandleFunc("/api/v1/backup/export", h.backupExport)
	mux.HandleFunc("/api/v1/backup/gist/status", h.gistBackupStatus)
	mux.HandleFunc("/api/v1/backup/gist/save", h.gistBackupSave)
	mux.HandleFunc("/api/v1/backup/gist/upload", h.gistBackupUpload)
	mux.HandleFunc("/api/v1/backup/gist/versions", h.gistBackupVersions)
	mux.HandleFunc("/api/v1/backup/gist/restore", h.gistBackupRestore)
	mux.HandleFunc("/api/v1/backup/import", h.backupImport)
	mux.HandleFunc("/api/v1/backup/restore", h.backupImport)
	mux.HandleFunc("/api/v1/api/v1/backup/export", h.backupExport)
	mux.HandleFunc("/api/v1/api/v1/backup/import", h.backupImport)
	mux.HandleFunc("/api/v1/api/v1/backup/restore", h.backupImport)
	mux.HandleFunc("/api/v1/captcha/check", h.checkCaptcha)
	mux.HandleFunc("/api/v1/captcha/verify", h.captchaVerify)
	mux.HandleFunc("/api/v1/user/package", h.userPackage)
	mux.HandleFunc("/api/v1/user/subscription/traffic", h.userSubscriptionTraffic)
	mux.HandleFunc("/api/v1/user/updatePassword", h.updatePassword)
	mux.HandleFunc("/api/v1/node/list", h.nodeList)
	mux.HandleFunc("/api/v1/node/create", h.nodeCreate)
	mux.HandleFunc("/api/v1/node/update", h.nodeUpdate)
	mux.HandleFunc("/api/v1/node/delete", h.nodeDelete)
	mux.HandleFunc("/api/v1/node/install", h.nodeInstall)
	mux.HandleFunc("/api/v1/node/update-order", h.nodeUpdateOrder)
	mux.HandleFunc("/api/v1/node/dismiss-expiry-reminder", h.nodeDismissExpiryReminder)
	mux.HandleFunc("/api/v1/node/batch-delete", h.nodeBatchDelete)
	mux.HandleFunc("/api/v1/node/check-status", h.nodeCheckStatus)
	mux.HandleFunc("/api/v1/node/upgrade", h.nodeUpgrade)
	mux.HandleFunc("/api/v1/node/batch-upgrade", h.nodeBatchUpgrade)
	mux.HandleFunc("/api/v1/node/rollback", h.nodeRollback)
	// 移植自 kure29/flyx：节点延迟测试。
	mux.HandleFunc("/api/v1/node/latency/probe", h.nodeLatencyProbe)
	mux.HandleFunc("/api/v1/node/latency/targets", h.nodeLatencyTargetsSet)
	mux.HandleFunc("/api/v1/node/latency/history", h.nodeLatencyHistory)
	// 移植自 kure29/flyx：节点网络调优。
	mux.HandleFunc("/api/v1/node/tune", h.nodeTune)
	// 移植自 kure29/flyx：回程路由追踪。
	mux.HandleFunc("/api/v1/node/route", h.nodeRouteGet)
	mux.HandleFunc("/api/v1/node/route/probe", h.nodeRouteProbe)
	// 移植自 kure29/flyx：GitHub 镜像代理。
	mux.HandleFunc(ghMirrorPath, h.githubMirror)
	mux.HandleFunc("/api/v1/node/releases", h.listReleases)
	mux.HandleFunc("/api/v1/node/nftables/test", h.nodeNftablesTest)
	mux.HandleFunc("/api/v1/node/nftables/reconcile", h.nodeNftablesReconcile)
	mux.HandleFunc("/api/v1/node/nftables/clear", h.nodeNftablesClear)
	mux.HandleFunc("/api/v1/node/nftables/pending-deletes", h.nftPendingDeleteList)
	mux.HandleFunc("/api/v1/kernel/capabilities", h.kernelCapabilities)
	mux.HandleFunc("/api/v1/kernel/list", h.kernelList)
	mux.HandleFunc("/api/v1/kernel/install", h.kernelInstall)
	mux.HandleFunc("/api/v1/kernel/versions", h.kernelVersions)
	mux.HandleFunc("/api/v1/kernel/control", h.kernelControl)
	mux.HandleFunc("/api/v1/kernel/logs", h.kernelLogs)
	mux.HandleFunc("/api/v1/inbound/list", h.inboundList)
	mux.HandleFunc("/api/v1/inbound/create", h.inboundCreate)
	mux.HandleFunc("/api/v1/inbound/update", h.inboundUpdate)
	mux.HandleFunc("/api/v1/inbound/delete", h.inboundDelete)
	mux.HandleFunc("/api/v1/inbound/batch-delete", h.inboundBatchDelete)
	mux.HandleFunc("/api/v1/inbound/probe", h.inboundProbe)
	mux.HandleFunc("/api/v1/inbound/batch-toggle", h.inboundBatchToggle)
	mux.HandleFunc("/api/v1/notify/test", h.notifyTest)
	mux.HandleFunc("/api/v1/subscription/list", h.subscriptionList)
	mux.HandleFunc("/api/v1/subscription/save", h.subscriptionSave)
	mux.HandleFunc("/api/v1/subscription/delete", h.subscriptionDelete)
	mux.HandleFunc("/api/v1/subscription/reset", h.subscriptionReset)
	mux.HandleFunc("/api/v1/subscription/reset-traffic", h.subscriptionResetTraffic)
	mux.HandleFunc("/api/v1/subscription/traffic", h.subscriptionTraffic)
	mux.HandleFunc("/api/v1/subscription/source/list", h.remoteSourceList)
	mux.HandleFunc("/api/v1/subscription/source/save", h.remoteSourceSave)
	mux.HandleFunc("/api/v1/subscription/source/refresh", h.remoteSourceRefresh)
	mux.HandleFunc("/api/v1/subscription/source/delete", h.remoteSourceDelete)
	mux.HandleFunc("/api/v1/subscription/preview", h.subscriptionPreview)
	mux.HandleFunc("/api/v1/inbound/toggle", h.inboundToggle)
	mux.HandleFunc("/api/v1/inbound/split", h.inboundSplitSave)
	mux.HandleFunc("/api/v1/inbound/reset-traffic", h.inboundResetTraffic)
	mux.HandleFunc("/api/v1/inbound/regenerate", h.inboundRegenerate)
	mux.HandleFunc("/api/v1/inbound/share", h.inboundShare)
	mux.HandleFunc("/api/v1/inbound/import", h.inboundImport)
	mux.HandleFunc("/api/v1/inbound/external/update", h.inboundExternalUpdate)
	mux.HandleFunc("/api/v1/inbound/sub/info", h.inboundSubInfo)
	mux.HandleFunc("/api/v1/inbound/relay/list", h.inboundRelayList)
	mux.HandleFunc("/api/v1/inbound/relay/create", h.inboundRelayCreate)
	mux.HandleFunc("/api/v1/inbound/relay/delete", h.inboundRelayDelete)
	mux.HandleFunc("/api/v1/inbound/relay/probe", h.inboundRelayProbe)
	mux.HandleFunc("/api/v1/user-inbound/list", h.getUserInbounds)
	mux.HandleFunc("/api/v1/user-inbound/set", h.setUserInbounds)
	mux.HandleFunc("/api/v1/user-inbound/mine", h.getMyInbounds)
	mux.HandleFunc("/api/v1/user-subscription/list", h.userSubscriptionList)
	mux.HandleFunc("/api/v1/user-subscription/set", h.userSubscriptionSet)
	mux.HandleFunc("/api/v1/user-subscription/mine", h.userSubscriptionMine)
	mux.HandleFunc("/api/v1/open_api/sub/", h.openSubscription)
	mux.HandleFunc("/api/v1/tunnel/list", h.tunnelList)
	mux.HandleFunc("/api/v1/tunnel/create", h.tunnelCreate)
	mux.HandleFunc("/api/v1/tunnel/get", h.tunnelGet)
	mux.HandleFunc("/api/v1/tunnel/update", h.tunnelUpdate)
	mux.HandleFunc("/api/v1/tunnel/delete", h.tunnelDelete)
	mux.HandleFunc("/api/v1/tunnel/delete-preview", h.tunnelDeletePreview)
	mux.HandleFunc("/api/v1/tunnel/delete-with-forwards", h.tunnelDeleteWithForwards)
	mux.HandleFunc("/api/v1/tunnel/batch-delete-preview", h.tunnelBatchDeletePreview)
	mux.HandleFunc("/api/v1/tunnel/batch-delete-with-forwards", h.tunnelBatchDeleteWithForwards)
	mux.HandleFunc("/api/v1/tunnel/diagnose", h.tunnelDiagnose)
	mux.HandleFunc("/api/v1/tunnel/diagnose/stream", h.tunnelDiagnoseStream)
	mux.HandleFunc("/api/v1/tunnel/update-order", h.tunnelUpdateOrder)
	mux.HandleFunc("/api/v1/tunnel/batch-delete", h.tunnelBatchDelete)
	mux.HandleFunc("/api/v1/tunnel/batch-redeploy", h.tunnelBatchRedeploy)
	mux.HandleFunc("/api/v1/tunnel/user/assign", h.userTunnelAssign)
	mux.HandleFunc("/api/v1/tunnel/user/batch-assign", h.userTunnelBatchAssign)
	mux.HandleFunc("/api/v1/tunnel/user/remove", h.userTunnelRemove)
	mux.HandleFunc("/api/v1/tunnel/user/update", h.userTunnelUpdate)
	mux.HandleFunc("/api/v1/forward/list", h.forwardList)
	mux.HandleFunc("/api/v1/forward/create", h.forwardCreate)
	mux.HandleFunc("/api/v1/forward/update", h.forwardUpdate)
	mux.HandleFunc("/api/v1/forward/delete", h.forwardDelete)
	mux.HandleFunc("/api/v1/forward/force-delete", h.forwardForceDelete)
	mux.HandleFunc("/api/v1/forward/pause", h.forwardPause)
	mux.HandleFunc("/api/v1/forward/resume", h.forwardResume)
	mux.HandleFunc("/api/v1/forward/reset-flow", h.forwardResetFlow)
	mux.HandleFunc("/api/v1/forward/diagnose", h.forwardDiagnose)
	mux.HandleFunc("/api/v1/forward/diagnose/stream", h.forwardDiagnoseStream)
	mux.HandleFunc("/api/v1/forward/update-order", h.forwardUpdateOrder)
	mux.HandleFunc("/api/v1/forward/batch-delete", h.forwardBatchDelete)
	mux.HandleFunc("/api/v1/forward/batch-pause", h.forwardBatchPause)
	mux.HandleFunc("/api/v1/forward/batch-resume", h.forwardBatchResume)
	mux.HandleFunc("/api/v1/forward/batch-redeploy", h.forwardBatchRedeploy)
	mux.HandleFunc("/api/v1/forward/batch-change-tunnel", h.forwardBatchChangeTunnel)
	mux.HandleFunc("/api/v1/speed-limit/list", h.speedLimitList)
	mux.HandleFunc("/api/v1/speed-limit/create", h.speedLimitCreate)
	mux.HandleFunc("/api/v1/speed-limit/update", h.speedLimitUpdate)
	mux.HandleFunc("/api/v1/speed-limit/delete", h.speedLimitDelete)
	mux.HandleFunc("/api/v1/tunnel/user/tunnel", h.userTunnelVisibleList)
	mux.HandleFunc("/api/v1/tunnel/user/list", h.userTunnelList)
	mux.HandleFunc("/api/v1/group/tunnel/list", h.tunnelGroupList)
	mux.HandleFunc("/api/v1/group/tunnel/create", h.groupTunnelCreate)
	mux.HandleFunc("/api/v1/group/tunnel/update", h.groupTunnelUpdate)
	mux.HandleFunc("/api/v1/group/tunnel/delete", h.groupTunnelDelete)
	mux.HandleFunc("/api/v1/group/tunnel/assign", h.groupTunnelAssign)
	mux.HandleFunc("/api/v1/group/user/list", h.userGroupList)
	mux.HandleFunc("/api/v1/group/user/create", h.groupUserCreate)
	mux.HandleFunc("/api/v1/group/user/update", h.groupUserUpdate)
	mux.HandleFunc("/api/v1/group/user/delete", h.groupUserDelete)
	mux.HandleFunc("/api/v1/group/user/assign", h.groupUserAssign)
	mux.HandleFunc("/api/v1/group/permission/list", h.groupPermissionList)
	mux.HandleFunc("/api/v1/group/permission/assign", h.groupPermissionAssign)
	mux.HandleFunc("/api/v1/group/permission/remove", h.groupPermissionRemove)
	mux.HandleFunc("/api/v1/open_api/sub_store", h.openAPISubStore)
	mux.HandleFunc("/api/v1/federation/share/list", h.federationShareList)
	mux.HandleFunc("/api/v1/federation/share/create", h.federationShareCreate)
	mux.HandleFunc("/api/v1/federation/share/update", h.federationShareUpdate)
	mux.HandleFunc("/api/v1/federation/share/delete", h.federationShareDelete)
	mux.HandleFunc("/api/v1/federation/share/reset-flow", h.federationShareResetFlow)
	mux.HandleFunc("/api/v1/federation/share/remote-usage/list", h.federationRemoteUsageList)
	mux.HandleFunc("/api/v1/federation/connect", h.authPeer(h.federationConnect))
	mux.HandleFunc("/api/v1/federation/tunnel/create", h.authPeer(h.federationTunnelCreate))
	mux.HandleFunc("/api/v1/federation/runtime/reserve-port", h.authPeer(h.federationRuntimeReservePort))
	mux.HandleFunc("/api/v1/federation/runtime/apply-role", h.authPeer(h.federationRuntimeApplyRole))
	mux.HandleFunc("/api/v1/federation/runtime/release-role", h.authPeer(h.federationRuntimeReleaseRole))
	mux.HandleFunc("/api/v1/federation/runtime/diagnose", h.authPeer(h.federationRuntimeDiagnose))
	mux.HandleFunc("/api/v1/federation/runtime/command", h.authPeer(h.federationRuntimeCommand))
	mux.HandleFunc("/api/v1/federation/node/import", h.nodeImport)
	mux.HandleFunc("/api/v1/announcement/get", h.getAnnouncement)
	mux.HandleFunc("/api/v1/announcement/update", h.updateAnnouncement)

	mux.HandleFunc("/api/v1/node/traffic/month", h.nodeTrafficMonth)
	mux.HandleFunc("/api/v1/monitor/access", h.monitorAccessHandler)
	mux.HandleFunc("/api/v1/monitor/nodes/", h.monitorNodeMetricsHandler)
	mux.HandleFunc("/api/v1/monitor/nodes", h.monitorNodeListHandler)
	mux.HandleFunc("/api/v1/monitor/tunnels", h.monitorTunnelListHandler)
	mux.HandleFunc("/api/v1/monitor/tunnels/quality", h.monitorTunnelQualityHandler)
	mux.HandleFunc("/api/v1/monitor/tunnels/", h.monitorTunnelMetrics)
	mux.HandleFunc("/api/v1/monitor/services", h.monitorServiceListHandler)
	mux.HandleFunc("/api/v1/monitor/services/create", h.monitorServiceCreate)
	mux.HandleFunc("/api/v1/monitor/services/update", h.monitorServiceUpdate)
	mux.HandleFunc("/api/v1/monitor/services/delete", h.monitorServiceDelete)
	mux.HandleFunc("/api/v1/monitor/services/run", h.monitorServiceRun)
	mux.HandleFunc("/api/v1/monitor/services/latest-results", h.monitorServiceLatestResultsHandler)
	mux.HandleFunc("/api/v1/monitor/services/limits", h.monitorServiceLimitsHandler)
	mux.HandleFunc("/api/v1/monitor/services/", h.monitorServiceResultsHandler)
	mux.HandleFunc("/api/v1/monitor/permission/list", h.monitorPermissionList)
	mux.HandleFunc("/api/v1/monitor/permission/assign", h.monitorPermissionAssign)
	mux.HandleFunc("/api/v1/monitor/permission/remove", h.monitorPermissionRemove)

	mux.HandleFunc("/flow/test", h.flowTest)
	mux.HandleFunc("/flow/config", h.flowConfig)
	mux.HandleFunc("/flow/upload", h.flowUpload)
	mux.HandleFunc("/error", h.errorPage)
}

// dummyBcryptHash 是合法 bcrypt 格式（cost 10）的占位哈希：
// 用户不存在的鉴权路径用它跑一次 VerifyPassword，使耗时与真实
// 验证相近，消除用户名枚举时序差（K-001；login 与 sub_store 共用）。
const dummyBcryptHash = "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"

// isStoredDefaultPassword 判断存储的口令哈希是否为种子默认口令（M-05）。
// 仅用于管理员账号；登录已做过一次 bcrypt，这里是通行密钥等无密码登录路径的补充检查。
func isStoredDefaultPassword(roleID int, pwdHash string) bool {
	if roleID != 0 {
		return false
	}
	for _, def := range []string{"admin@user", "admin_user"} {
		if ok, _ := security.VerifyPassword(pwdHash, def); ok {
			return true
		}
	}
	return false
}

// tokenIssueTime 计算 token 签发时刻，保证 IatMs 严格大于
// PasswordChangedAt（F-068）。中间件/WS 以 IatMs <= PasswordChangedAt
// 判 token 失效（M-05 有意口径，不得放宽）：改密/吊销与重新登录落在
// 同一毫秒时，按 time.Now() 签发的新 token 会出生即失效。签发侧把
// 时刻单调推到 PasswordChangedAt+1，与 legacy rehash 的既有做法一致。
func tokenIssueTime(passwordChangedAt int64) time.Time {
	now := time.Now()
	if passwordChangedAt >= now.UnixMilli() {
		return time.UnixMilli(passwordChangedAt + 1)
	}
	return now
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}

	// M-04: 登录限流（每 IP 每分钟 10 次），防暴力破解
	if !h.checkLoginRateLimit(w, r) {
		return
	}

	var req loginRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}

	if strings.TrimSpace(req.Username) == "" {
		response.WriteJSON(w, response.ErrDefault("用户名不能为空"))
		return
	}
	// R5-B5 D11-D13: 用户名桶只作为"升级验证"信号，永不硬拒。
	// 触发后：验证码启用则强制 captcha（下方已有）；未启用则收紧该 IP 阈值。
	// 无论用户名是否存在都计数，避免用行为差异做用户名枚举。
	captchaEnabledForSignal, _ := h.captchaEnabled()
	clientIPStr := ""
	if ip := loginClientIP(r); ip != nil {
		clientIPStr = ip.String()
	} else {
		clientIPStr = r.RemoteAddr
	}
	h.checkLoginUsernameRateLimit(req.Username, clientIPStr, captchaEnabledForSignal)
	if strings.TrimSpace(req.Password) == "" {
		response.WriteJSON(w, response.ErrDefault("密码不能为空"))
		return
	}

	captchaEnabled, err := h.captchaEnabled()
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	// R5-B2 D2: 已删除 X-FLVX-API-Client 自声明头旁路；验证码开启时无例外。
	if captchaEnabled {
		captchaID := strings.TrimSpace(req.CaptchaID)
		if captchaID == "" {
			response.WriteJSON(w, response.ErrDefault("验证码校验失败"))
			return
		}

		if !h.consumeCaptchaToken(captchaID) {
			secretCfg, err := h.repo.GetConfigByName("cloudflare_secret_key")
			if err != nil || secretCfg == nil || strings.TrimSpace(secretCfg.Value) == "" {
				response.WriteJSON(w, response.ErrDefault("验证码校验失败"))
				return
			}

			if !h.verifyCloudflareTurnstile(captchaID, strings.TrimSpace(secretCfg.Value)) {
				response.WriteJSON(w, response.ErrDefault("验证码校验失败"))
				return
			}
		}
	}

	user, err := h.repo.GetUserByUsername(req.Username)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if user == nil {
		// K-001：用户不存在时也跑一次 dummy bcrypt，消除时序差，防止用户名枚举
		// 旧逻辑：不存在用户立即返回（2-5ms），存在用户走 bcrypt（80-150ms），时序差可被利用
		_, _ = security.VerifyPassword(dummyBcryptHash, req.Password)
		response.WriteJSON(w, response.ErrDefault("账号或密码错误"))
		return
	}
	passwordMatched, passwordWasLegacy := security.VerifyPassword(user.Pwd, req.Password)
	if !passwordMatched {
		response.WriteJSON(w, response.ErrDefault("账号或密码错误"))
		return
	}
	// R6X-014：停用判据与全族 fail-closed 口径对齐（Status != 1 即拒绝），
	// 存量 status∉{0,1} 的僵尸行不得再取得 token。
	if user.Status != 1 {
		response.WriteJSON(w, response.ErrDefault("账号被停用"))
		return
	}
	issueAt := tokenIssueTime(user.PasswordChangedAt)
	if passwordWasLegacy {
		updatedAt := time.Now().UnixMilli()
		hashedPassword, err := security.HashPassword(req.Password)
		if err != nil {
			log.Printf("legacy password rehash skipped user_id=%d path=login err=%v", user.ID, err)
		} else if err := h.repo.UpdateUserPassword(user.ID, hashedPassword, updatedAt); err != nil {
			log.Printf("legacy password rehash update skipped user_id=%d path=login err=%v", user.ID, err)
		} else {
			issueAt = time.UnixMilli(updatedAt + 1)
		}
	}

	// M-05: 管理员用种子默认口令登录时签发受限 token（仅允许改密），
	// 服务端强制改密。A1-D2：判定口径与 passkey 路径（R-002）统一为
	// 「存储口令是否仍为种子默认口令」——登录已成功验证提交口令与存储
	// 哈希一致，故该口径与「提交口令命中默认值」等价，且能覆盖口令被
	// 其它路径重置为默认的情况。下方响应标志直接复用同一结果：
	// JWT claim、响应标志、passkey 三方一致，不再按用户名现算
	// （旧现算使已改密但保留 admin@user 用户名的管理员被永久误报）。
	pwdChangeRequired := isStoredDefaultPassword(user.RoleID, user.Pwd)
	var token string
	if pwdChangeRequired {
		token, err = auth.GenerateTokenAtWithPwdChangeRequired(user.ID, user.User, user.RoleID, h.jwtSecret, issueAt)
	} else {
		token, err = auth.GenerateTokenAt(user.ID, user.User, user.RoleID, h.jwtSecret, issueAt)
	}
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}

	// R5-B2 A-001: 登录成功，清除该用户名的限流状态（含退避计数）。
	h.noteLoginUsernameSuccess(req.Username)
	response.WriteJSON(w, response.OK(map[string]interface{}{
		"token":                 token,
		"name":                  user.User,
		"role_id":               user.RoleID,
		"requirePasswordChange": pwdChangeRequired,
	}))
}

func (h *Handler) getConfigByName(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}

	var req nameRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("配置名称不能为空"))
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		response.WriteJSON(w, response.ErrDefault("配置名称不能为空"))
		return
	}
	configName := strings.ToLower(strings.TrimSpace(req.Name))
	if configName == "license_key" || configName == "license_machine_id" || configName == "machine_fingerprint" {
		response.WriteJSON(w, response.Err(403, "禁止访问系统授权凭据"))
		return
	}
	// R10X-014（A-02）：读路径统一过 repo.PolicyForConfig 单一裁决
	// 点。此前执行路径走名单制（仅 IsSensitiveConfigKey 列名键被
	// 挡），未列名的新键任意已登录用户可读，与 PolicyForConfig
	// 「未知键 → 敏感」的口径矛盾且该函数零生产调用。接线后：
	// sensitive（含未分类）非管理员 403；public 匿名可读；
	// authenticated 须已登录。现行在用键已逐键补分类（见
	// config_policy.go authenticatedConfigKeys），读取行为不变。
	if repo.PolicyForConfig(configName) == repo.ConfigAccessSensitive && !isAdminRequest(r) {
		response.WriteJSON(w, response.Err(403, "禁止访问敏感配置"))
		return
	}

	if _, ok := r.Context().Value(middleware.ClaimsContextKey).(auth.Claims); !ok && repo.PolicyForConfig(configName) != repo.ConfigAccessPublic {
		response.WriteJSON(w, response.Err(401, "未登录或token已过期"))
		return
	}

	// R14-F01（R14-X-01 / A3-01 / A8-01）：Gist 令牌与密码即使对管理
	// 员也不经 config/get 下发，与 getConfigs（config/list）侧 delete
	// + gist/status 仅 tokenHint 的设计对齐。置于敏感/登录守卫之后，
	// 故只收紧此前唯一漏防的管理员读路径；非管理员与匿名仍走既有
	// 403「禁止访问敏感配置」/ 401 口径。边界：只动这两键，其余
	// sensitive 键（如 jwt_secret）管理员可读是既有行为，不动。
	if configName == gistConfigToken || configName == gistConfigPassword {
		response.WriteJSON(w, response.Err(403, "禁止访问 Gist 凭据，请经 gist/status 查看"))
		return
	}

	cfg, err := h.repo.GetConfigByName(configName)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if cfg == nil {
		response.WriteJSON(w, response.ErrDefault("配置不存在"))
		return
	}

	response.WriteJSON(w, response.OK(cfg))
}

func (h *Handler) getConfigs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}

	cfgMap, err := h.repo.ListConfigs()
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	ctxClaims := r.Context().Value(middleware.ClaimsContextKey)
	// O11-P3-3：变量更名为 isAuthenticated（旧名 isAdmin 实为"断言成功"，
	// 命名误导易被后人改错；逻辑不变，非 admin 仍走敏感配置过滤）。
	// R10X-014（A-02）：FilterSensitiveConfigs 已统一过
	// PolicyForConfig（仅 public/authenticated 下发），本处调用
	// 形态不变，过滤口径随策略单点收口。
	claims, isAuthenticated := ctxClaims.(auth.Claims)
	if !isAuthenticated || claims.RoleID != 0 {
		cfgMap = repo.FilterSensitiveConfigs(cfgMap)
	}
	delete(cfgMap, "license_key")
	delete(cfgMap, "license_machine_id")
	delete(cfgMap, "machine_fingerprint")
	// PORT-3342/B1：Gist 令牌与密码即使对管理员也不经 config/list
	// 下发（与上游一致）；读取走专用的 gist/status 端点（仅 tokenHint）。
	delete(cfgMap, gistConfigToken)
	delete(cfgMap, gistConfigPassword)
	response.WriteJSON(w, response.OK(cfgMap))
}

func (h *Handler) userList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}

	var req struct {
		Current int    `json:"current"`
		Size    int    `json:"size"`
		Keyword string `json:"keyword"`
	}
	if err := decodeJSON(r.Body, &req); err != nil && err != io.EOF {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}

	users, err := h.repo.ListUsers()
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}

	keyword := strings.ToLower(strings.TrimSpace(req.Keyword))
	if keyword != "" {
		filtered := make([]map[string]interface{}, 0, len(users))
		for _, item := range users {
			username := strings.ToLower(strings.TrimSpace(fmt.Sprint(item["user"])))
			displayName := strings.ToLower(strings.TrimSpace(fmt.Sprint(item["name"])))
			if strings.Contains(username, keyword) || strings.Contains(displayName, keyword) {
				filtered = append(filtered, item)
			}
		}
		users = filtered
	}

	// O5-003：正确使用 current/size 分页。旧逻辑解析了参数却完全忽略、全量返回。
	// 未传分页参数（current/size 为 0）时保持旧全量行为兼容。
	if req.Current > 0 && req.Size > 0 {
		// R16-X-02：(Current-1)*Size 与 start+Size 均可回绕 int64（溢出形
		// 见 audit-out-132/r16/R16-L3-sentinel-a2.md:196-198），回绕为负后
		// users[start:end] panic；乘前判溢出、超界即空页，不改变正常分页语义。
		start := len(users)
		if req.Current-1 <= math.MaxInt64/req.Size {
			start = (req.Current - 1) * req.Size
			if start > len(users) {
				start = len(users)
			}
		}
		end := len(users)
		if req.Size <= len(users)-start {
			end = start + req.Size
		}
		users = users[start:end]
	}

	response.WriteJSON(w, response.OK(users))
}

func (h *Handler) nodeList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}

	items, err := h.repo.ListNodes()
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}

	h.syncRemoteNodeStatuses(items)
	h.attachNodeLatency(items)

	response.WriteJSON(w, response.OK(items))
}

func (h *Handler) tunnelList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}

	items, err := h.repo.ListTunnels()
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	h.attachBestExitStates(items)
	response.WriteJSON(w, response.OK(items))
}

func (h *Handler) forwardList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}

	userID, roleID, err := userRoleFromRequest(r)
	if err != nil {
		response.WriteJSON(w, response.Err(401, "无效的token或token已过期"))
		return
	}

	items, err := h.repo.ListForwards()
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if roleID != 0 {
		filtered := make([]map[string]interface{}, 0, len(items))
		for _, item := range items {
			if asInt64(item["userId"], 0) == userID {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	response.WriteJSON(w, response.OK(items))
}

func (h *Handler) speedLimitList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}

	items, err := h.repo.ListSpeedLimits()
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	response.WriteJSON(w, response.OK(items))
}

// R5-B2 D1: sub_store 保留 + 90 天弃用窗口（Sunset: 2027-01-17）。
// POST JSON body 为正式用法；GET 保留但限流 + Deprecation/Sunset 头。
// 凭据曾长期走 URL query（进 access_log/代理日志），限流器独立于 login，
// 键 = 真实客户端 IP + 用户名，避免攻击者耗尽 login 桶锁死正常登录。
var subStoreLimiter = &loginRateLimiter{buckets: make(map[string]*loginAttemptBucket)}

const subStoreSunsetHTTPDate = "Sun, 17 Jan 2027 00:00:00 GMT"

func (h *Handler) openAPISubStore(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.repo == nil {
		response.WriteJSON(w, response.Err(-2, "database unavailable"))
		return
	}

	var username, password, tunnel string
	switch r.Method {
	case http.MethodPost:
		// 正式用法：凭据走 POST body，不进 URL/query 日志
		var req struct {
			User     string `json:"user"`
			Password string `json:"password"`
			Tunnel   string `json:"tunnel"`
		}
		if err := decodeJSON(r.Body, &req); err != nil {
			response.WriteJSON(w, response.ErrDefault("请求参数错误"))
			return
		}
		username = strings.TrimSpace(req.User)
		password = req.Password
		tunnel = strings.TrimSpace(req.Tunnel)
	case http.MethodGet:
		// 弃用窗口：仍可用，但限流 + 标记弃用；窗口结束后删除
		w.Header().Set("Deprecation", "true")
		w.Header().Set("Sunset", subStoreSunsetHTTPDate)
		q := r.URL.Query()
		username = strings.TrimSpace(q.Get("user"))
		password = q.Get("pwd")
		tunnel = strings.TrimSpace(q.Get("tunnel"))
	default:
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	if tunnel == "" {
		tunnel = "-1"
	}

	if username == "" {
		response.WriteJSON(w, response.ErrDefault("用户不能为空"))
		return
	}
	if password == "" {
		response.WriteJSON(w, response.ErrDefault("密码不能为空"))
		return
	}

	// 独立限流（不与 login 共用桶）：键 = 真实客户端 IP + 用户名
	clientIP := loginClientIP(r)
	ipStr := ""
	if clientIP != nil {
		ipStr = clientIP.String()
	} else if parsed := parseIPLiteral(r.RemoteAddr); parsed != nil {
		ipStr = parsed.String()
	} else {
		ipStr = r.RemoteAddr
	}
	if !subStoreLimiter.allow(ipStr+"\x00"+strings.ToLower(username), time.Now()) {
		w.Header().Set("Retry-After", "60")
		response.WriteJSON(w, response.Err(429, "请求过于频繁，请稍后再试"))
		return
	}

	authOK := false
	defer func() {
		if !authOK {
			// 失败审计：只记用户名、解析后 IP、时间戳；
			// 绝不记录密码、原始 query 或完整 URL。
			log.Printf("sub_store auth failed user=%q ip=%s at=%d", username, ipStr, time.Now().UnixMilli())
		}
	}()

	user, err := h.repo.GetUserByUsername(username)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if user == nil {
		// A1 P3：与 login 的 K-001 同口径——用户不存在时也跑一次
		// dummy bcrypt，消除用户名枚举时序侧信道（旧实现立即返回）。
		_, _ = security.VerifyPassword(dummyBcryptHash, password)
		response.WriteJSON(w, response.ErrDefault("鉴权失败"))
		return
	}
	passwordMatched, passwordWasLegacy := security.VerifyPassword(user.Pwd, password)
	if !passwordMatched {
		response.WriteJSON(w, response.ErrDefault("鉴权失败"))
		return
	}
	// R6X-014：同 login——停用判据改 Status != 1 fail-closed，
	// 存量 status∉{0,1} 的僵尸行不得再取得本人用量。
	if user.Status != 1 {
		response.WriteJSON(w, response.ErrDefault("账号被停用"))
		return
	}
	if passwordWasLegacy {
		hashedPassword, err := security.HashPassword(password)
		if err != nil {
			log.Printf("legacy password rehash skipped user_id=%d path=sub_store err=%v", user.ID, err)
		} else if err := h.repo.UpdateUserPassword(user.ID, hashedPassword, time.Now().UnixMilli()); err != nil {
			log.Printf("legacy password rehash update skipped user_id=%d path=sub_store err=%v", user.ID, err)
		}
	}
	authOK = true

	// R10X-005（T1）：total 必须经 flowLimitBytes(Flow, FlowMiB) 按字节
	// 精确下发，与执法面同值——旧读法 Flow×1GiB 忽略 FlowMiB，非整 GiB
	// 限额被向上取整、(0,MiB>0) 备份可达对 total 误报 0。
	// (Flow,FlowMiB)=(0,0) 时 total=0 保留：与分享订阅面 TrafficLimit
	// 0=不限量同读法（kernel_relay.go 订阅头注记），执法面亦以 0=不限额
	// 读之（R9X-001），三面一致。
	headerValue := ""

	if tunnel == "-1" {
		headerValue = buildSubscriptionHeader(user.OutFlow, user.InFlow, flowLimitBytes(user.Flow, user.FlowMiB), user.ExpTime/1000)
	} else {
		tunnelID, parseErr := strconv.ParseInt(tunnel, 10, 64)
		if parseErr != nil || tunnelID <= 0 {
			response.WriteJSON(w, response.ErrDefault("隧道不存在"))
			return
		}

		ut, err := h.repo.GetUserTunnelByID(tunnelID)
		if err != nil {
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
		if ut == nil {
			response.WriteJSON(w, response.ErrDefault("隧道不存在"))
			return
		}
		if ut.UserID != user.ID {
			response.WriteJSON(w, response.ErrDefault("隧道不存在"))
			return
		}

		headerValue = buildSubscriptionHeader(ut.OutFlow, ut.InFlow, flowLimitBytes(ut.Flow, ut.FlowMiB), ut.ExpTime/1000)
	}

	w.Header().Set("subscription-userinfo", headerValue)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(headerValue))
}

func (h *Handler) errorPage(w http.ResponseWriter, _ *http.Request) {
	// 后端直出的唯一 HTML 面：与前端入口文档统一 no-cache 口径，
	// 别让浏览器启发式缓存把旧错误页留住（面板 UI 本身不经后端出站）。
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Type", "text/html; charset=UTF-8")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte("<!DOCTYPE html><html lang='zh-CN'><head><meta charset='UTF-8'><meta name='viewport' content='width=device-width, initial-scale=1.0'><title>错误 404</title></head><body><div style='min-height:100vh;display:flex;align-items:center;justify-content:center;flex-direction:column;font-family:-apple-system,BlinkMacSystemFont,Segoe UI,Arial,sans-serif;'><div style='font-size:6rem;color:#333;font-weight:300;'>404</div><div style='font-size:1.2rem;color:#666;'>你推开了后端的大门，却发现里面只有寂寞。</div></div></body></html>"))
}

func buildSubscriptionHeader(upload, download, total, expire int64) string {
	return fmt.Sprintf("upload=%d; download=%d; total=%d; expire=%d", download, upload, total, expire)
}

func (h *Handler) userTunnelVisibleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}

	userID, roleID, err := userRoleFromRequest(r)
	if err != nil {
		response.WriteJSON(w, response.Err(401, "无效的token或token已过期"))
		return
	}

	items := make([]map[string]interface{}, 0)
	if roleID == 0 {
		items, err = h.repo.ListEnabledTunnelSummaries()
	} else {
		items, err = h.repo.ListUserAccessibleTunnels(userID)
	}
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	response.WriteJSON(w, response.OK(items))
}

func (h *Handler) userTunnelList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}

	var req struct {
		UserID int64 `json:"userId"`
	}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	if req.UserID <= 0 {
		response.WriteJSON(w, response.OK([]interface{}{}))
		return
	}

	tunnels, err := h.repo.GetUserPackageTunnels(req.UserID)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}

	out := make([]map[string]interface{}, 0, len(tunnels))
	for _, t := range tunnels {
		item := map[string]interface{}{
			"id":             t.ID,
			"userId":         t.UserID,
			"tunnelId":       t.TunnelID,
			"tunnelName":     t.TunnelName,
			"status":         t.Status,
			"flow":           t.Flow,
			"flowMiB":        t.FlowMiB,
			"num":            t.Num,
			"expTime":        t.ExpTime,
			"flowResetTime":  t.FlowResetTime,
			"inFlow":         t.InFlow,
			"outFlow":        t.OutFlow,
			"tunnelFlow":     t.TunnelFlow,
			"speedId":        nil,
			"speedLimitName": nil,
		}
		if t.SpeedID.Valid {
			item["speedId"] = t.SpeedID.Int64
		}
		if t.SpeedLimit.Valid {
			item["speedLimitName"] = t.SpeedLimit.String
		}
		out = append(out, item)
	}
	response.WriteJSON(w, response.OK(out))
}

func (h *Handler) tunnelGroupList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}

	items, err := h.repo.ListTunnelGroups()
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	response.WriteJSON(w, response.OK(items))
}

func (h *Handler) userGroupList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}

	items, err := h.repo.ListUserGroups()
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	response.WriteJSON(w, response.OK(items))
}

func (h *Handler) groupPermissionList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}

	items, err := h.repo.ListGroupPermissions()
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	response.WriteJSON(w, response.OK(items))
}

func (h *Handler) checkCaptcha(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}

	enabled, err := h.captchaEnabled()
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if enabled {
		response.WriteJSON(w, response.OK(1))
		return
	}
	response.WriteJSON(w, response.OK(0))
}

func (h *Handler) captchaVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}

	// R10X-008（A-01）②：端点未认证可达，此前无任何限流——接入
	// 独立 IP 限流桶，超限直接 429，不再进入校验与铸令牌路径。
	if !h.checkCaptchaVerifyRateLimit(w, r) {
		return
	}

	var req captchaVerifyRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		h.writeCaptchaVerifyResult(w, false, "")
		return
	}
	id := strings.TrimSpace(req.ID)
	data := strings.TrimSpace(req.Data)
	if id == "" || data == "" {
		h.writeCaptchaVerifyResult(w, false, "")
		return
	}

	secretCfg, err := h.repo.GetConfigByName("cloudflare_secret_key")
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if secretCfg == nil || strings.TrimSpace(secretCfg.Value) == "" {
		// R10X-008（A-01）③ / BC-R10-07：secret 未配置时关闭本地
		// data=="ok" 回退分支。此前默认部署形态下任意请求体
		// data=="ok" 即判定通过并铸令牌入池，而 login 侧在该形态
		// 下根本不消费验证码（captchaEnabled 要求 site+secret
		// 双全），该端点的唯一实际效果是写入无界状态。现返回
		// 未启用语义且不铸令牌；前端在该形态下本就无调用方
		// （verifyCaptcha 无现行调用点，登录页只在 check 返回
		// 启用后走 Turnstile 组件直连 login 消费路径）。
		response.WriteJSON(w, response.Err(-1, "验证码未启用"))
		return
	}
	if !h.verifyCloudflareTurnstile(data, strings.TrimSpace(secretCfg.Value)) {
		h.writeCaptchaVerifyResult(w, false, "")
		return
	}

	h.markCaptchaToken(id)
	h.writeCaptchaVerifyResult(w, true, id)
}

// captcha verify 端点 IP 限流参数：独立小桶（不与登录桶共用，
// 避免验证码流量挤占登录预算、或登录爆破连带锁死验证码）。
const (
	captchaVerifyRateLimitWindow      = time.Minute
	captchaVerifyRateLimitMaxAttempts = 30
	captchaVerifyRateLimitMaxBuckets  = 10000
)

// checkCaptchaVerifyRateLimit 形态对齐 checkLoginRateLimit：
// 命中时直接写 429 并返回 false。桶表有界（上限 + 过期清理 +
// LRU 淘汰最旧），与登录限流器同一淘汰口径（A-08）。
func (h *Handler) checkCaptchaVerifyRateLimit(w http.ResponseWriter, r *http.Request) bool {
	if h.allowCaptchaVerify(r) {
		return true
	}
	w.Header().Set("Retry-After", "60")
	response.WriteJSON(w, response.Err(429, "请求过于频繁，请稍后再试"))
	return false
}

func (h *Handler) allowCaptchaVerify(r *http.Request) bool {
	ip := loginClientIP(r)
	key := ""
	if ip != nil {
		key = ip.String()
	} else if parsed := parseIPLiteral(r.RemoteAddr); parsed != nil {
		key = parsed.String()
	} else {
		key = r.RemoteAddr
	}
	now := time.Now()
	h.captchaVerifyMu.Lock()
	defer h.captchaVerifyMu.Unlock()
	if h.captchaVerifyBuckets == nil {
		h.captchaVerifyBuckets = make(map[string]*loginAttemptBucket)
	}
	b, ok := h.captchaVerifyBuckets[key]
	if !ok {
		h.evictCaptchaVerifyBucketsLocked(now)
		h.captchaVerifyBuckets[key] = &loginAttemptBucket{count: 1, windowStart: now, lastSeen: now}
		return true
	}
	b.lastSeen = now
	if now.Sub(b.windowStart) >= captchaVerifyRateLimitWindow {
		b.count = 1
		b.windowStart = now
		return true
	}
	b.count++
	return b.count <= captchaVerifyRateLimitMaxAttempts
}

func (h *Handler) evictCaptchaVerifyBucketsLocked(now time.Time) {
	if len(h.captchaVerifyBuckets) < captchaVerifyRateLimitMaxBuckets {
		return
	}
	for k, v := range h.captchaVerifyBuckets {
		if now.Sub(v.windowStart) >= captchaVerifyRateLimitWindow {
			delete(h.captchaVerifyBuckets, k)
		}
	}
	if len(h.captchaVerifyBuckets) < captchaVerifyRateLimitMaxBuckets {
		return
	}
	var oldestKey string
	var oldest time.Time
	first := true
	for k, v := range h.captchaVerifyBuckets {
		if first || v.lastSeen.Before(oldest) {
			oldestKey, oldest, first = k, v.lastSeen, false
		}
	}
	if oldestKey != "" {
		delete(h.captchaVerifyBuckets, oldestKey)
	}
}

func (h *Handler) flowTest(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("test"))
}

func (h *Handler) flowConfig(w http.ResponseWriter, r *http.Request) {
	secret := r.URL.Query().Get("secret")
	node, err := h.repo.GetNodeBySecret(secret)
	if err != nil || node == nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
		return
	}

	rawData, err := readAndDecryptFlowBody(r.Body, secret)
	if err == nil && strings.TrimSpace(rawData) != "" {
		h.cleanNodeConfigs(node.ID, rawData)
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok"))
}

// parseFlowSeqFromHeaders 解析上报 seq 头：新头 X-Flmx-Seq 优先、
// 旧头 X-Flvx-Seq 次之。X-P3-19（L-06-1＝F-O01）：新头存在但解析
// 失败时继续尝试旧头（与头注释口径对齐）——旧实现无条件 break，
// 垃圾新头会把合法旧头 seq 一并丢弃、回落 legacy 失去重放防护。
func parseFlowSeqFromHeaders(r *http.Request) *int64 {
	if r == nil {
		return nil
	}
	for _, hdr := range []string{"X-Flmx-Seq", "X-Flvx-Seq"} {
		if hv := r.Header.Get(hdr); hv != "" {
			if v, err := strconv.ParseInt(strings.TrimSpace(hv), 10, 64); err == nil {
				return &v
			}
		}
	}
	return nil
}

func (h *Handler) flowUpload(w http.ResponseWriter, r *http.Request) {
	secret := r.URL.Query().Get("secret")
	node, _ := h.repo.GetNodeBySecret(secret)
	if node == nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
		return
	}

	raw, err := readAndDecryptFlowBody(r.Body, secret)
	if err == nil && strings.TrimSpace(raw) != "" {
		// R5-B6 E-001: seq 优先从 HTTP 头 X-Flmx-Seq 读取（改名后新头），
		// 回退旧头 X-Flvx-Seq，其次从 body 的 {seq, items} 对象读取，
		// 最后兼容纯数组（legacy）。
		seq := parseFlowSeqFromHeaders(r)
		var items []flowItem
		// 先尝试新格式：{seq, items}。
		var batch struct {
			Seq   *int64     `json:"seq"`
			Items []flowItem `json:"items"`
		}
		if json.Unmarshal([]byte(raw), &batch) == nil && batch.Items != nil {
			if seq == nil {
				seq = batch.Seq
			}
			items = batch.Items
		} else if json.Unmarshal([]byte(raw), &items) == nil {
			// 旧格式：纯数组，seq 可能来自 header，否则为 nil（legacy）。
		} else {
			items = nil
		}
		if items != nil {
			now := time.Now()
			// X-P3-31（V-08）：seq 改为「先只读校验、入账同 tx 推进」。
			// 旧实现 CheckAndUpdateSeq 在入账前先行消费 seq：metas 失
			// 败置空仍回 ok、入账两段分开写一段失败另一段继续——seq
			// 已推进使同 seq 重报被判重放，分叉/漏计不可恢复。
			isReplay, isLegacy, seqErr := h.repo.CheckSeq(node.ID, seq)
			if seqErr != nil {
				// 校验失败整批拒收（非 ok）：agent 不清零、可重报。
				log.Printf("flow upload seq check failed node_id=%d err=%v", node.ID, seqErr)
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				_, _ = w.Write([]byte("error"))
				return
			}
			if isReplay {
				// 已入账批次的重放：回 ok 终止 agent 重试，不重复入账。
				log.Printf("flow upload replay rejected node_id=%d seq=%d", node.ID, *seq)
			} else {
				if isLegacy {
					log.Printf("flow upload legacy (no seq) node_id=%d", node.ID)
				}
				forwardIDs := collectFlowUploadForwardIDs(items)
				metas, metaErr := h.repo.GetFlowUploadForwardMetas(forwardIDs)
				if metaErr != nil {
					// V-08 ①：metas 失败整批拒收返非 ok——不入账、
					// seq 未推进，同 seq 重报可入账。
					log.Printf("flow upload metadata lookup failed node_id=%d err=%v", node.ID, metaErr)
					w.Header().Set("Content-Type", "text/plain; charset=utf-8")
					_, _ = w.Write([]byte("error"))
					return
				}
				batch := h.buildNodeFlowUploadBatch(node.ID, items, metas)
				batch.seqAdvance = &repo.FlowSeqAdvance{NodeID: node.ID, Seq: seq}
				if applyErr := h.applyFlowUploadBatch(node.ID, batch, now); applyErr != nil {
					if errors.Is(applyErr, repo.ErrSeqReplay) {
						// R2X-011：并发同 seq 已由另一入账消费——
						// 本批整 tx 回滚未入账，按既有重放分支回
						// ok 终止 agent 重试，不重复入账。
						log.Printf("flow upload replay (seq race) node_id=%d seq=%d", node.ID, *seq)
					} else {
						// 入账单事务失败：整批未入账、seq 未推进，返非 ok。
						w.Header().Set("Content-Type", "text/plain; charset=utf-8")
						_, _ = w.Write([]byte("error"))
						return
					}
				} else {
					h.recordTunnelMetricsFromForwardBatch(node.ID, batch.forwardTraffic, metas, now.UnixMilli())
				}
			}
		}
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok"))
}

func (h *Handler) updateConfigs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}

	var payload map[string]string
	if err := decodeJSON(r.Body, &payload); err != nil {
		response.WriteJSON(w, response.ErrDefault("配置数据不能为空"))
		return
	}
	if len(payload) == 0 {
		response.WriteJSON(w, response.ErrDefault("配置数据不能为空"))
		return
	}

	now := time.Now().UnixMilli()

	// O11-P3-2：先完成全部校验（敏感/系统管理/合法性），全部通过后再一次性
	// 事务写入。旧逻辑逐个 UpsertConfig 且无事务：中途失败会留下部分写入。
	// G3-131（A3 F-1）：空值不再整批拒绝。可清空键（凭据/URL/可选文本，
	// 见 repo.IsClearableConfigKey）传空 = 删除该键、读取回落默认——
	// Bot Token/Webhook 等凭据由此可以经 UI 撤下；必填键传空仍拒绝，
	// 且错误信息带上键名以便定位。
	validated := make(map[string]string, len(payload))
	cleared := make([]string, 0, len(payload))
	hooks := make([]string, 0, len(payload))
	prevPools := make(map[string][]latencyTarget, len(payload))
	for k, v := range payload {
		key := strings.TrimSpace(k)
		if key == "" {
			continue
		}
		if repo.IsSensitiveConfigKey(key) && !isAdminRequest(r) {
			response.WriteJSON(w, response.Err(403, "禁止访问敏感配置"))
			return
		}
		if repo.IsSystemManagedConfigKey(key) {
			response.WriteJSON(w, response.ErrDefault("该配置由系统管理"))
			return
		}

		value, err := normalizeAndValidateConfigValue(key, v)
		if err != nil {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}

		if value == "" {
			if !repo.IsClearableConfigKey(key) {
				response.WriteJSON(w, response.ErrDefault(fmt.Sprintf("配置值不能为空：%s", key)))
				return
			}
			cleared = append(cleared, key)
			hooks = append(hooks, key)
			prevPools[key] = h.latencyPoolBefore(key)
			continue
		}
		validated[key] = value
		hooks = append(hooks, key)
		prevPools[key] = h.latencyPoolBefore(key)
	}

	if err := h.repo.ApplyConfigBatch(validated, cleared, now); err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	h.invalidateNotifyConfigCache()
	for _, key := range hooks {
		h.notifyTunnelQualityConfigChanged(key)
		h.onLatencyConfigChanged(key, prevPools[key])
	}

	response.WriteJSON(w, response.OKEmpty())
}

func (h *Handler) updateSingleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}

	var req configSingleRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("配置名称不能为空"))
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		response.WriteJSON(w, response.ErrDefault("配置名称不能为空"))
		return
	}
	if repo.IsSensitiveConfigKey(name) && !isAdminRequest(r) {
		response.WriteJSON(w, response.Err(403, "禁止访问敏感配置"))
		return
	}
	if repo.IsSystemManagedConfigKey(name) {
		response.WriteJSON(w, response.ErrDefault("该配置由系统管理"))
		return
	}

	value, err := normalizeAndValidateConfigValue(name, req.Value)
	if err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}

	// G3-131（A3 F-1）：与 updateConfigs 同一清空语义——可清空键传空 =
	// 删除该键回落默认；必填键传空拒绝并在错误中带上键名。
	if value == "" {
		if !repo.IsClearableConfigKey(name) {
			response.WriteJSON(w, response.ErrDefault(fmt.Sprintf("配置值不能为空：%s", name)))
			return
		}
		prevPool := h.latencyPoolBefore(name)
		if err := h.repo.DeleteConfig(name); err != nil {
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
		h.invalidateNotifyConfigCache()
		h.notifyTunnelQualityConfigChanged(name)
		h.onLatencyConfigChanged(name, prevPool)

		response.WriteJSON(w, response.OKEmpty())
		return
	}

	prevPool := h.latencyPoolBefore(name)
	if err := h.repo.UpsertConfig(name, value, time.Now().UnixMilli()); err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	h.invalidateNotifyConfigCache()
	h.notifyTunnelQualityConfigChanged(name)
	h.onLatencyConfigChanged(name, prevPool)

	response.WriteJSON(w, response.OKEmpty())
}

func isAdminRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	claims, ok := r.Context().Value(middleware.ClaimsContextKey).(auth.Claims)
	return ok && claims.RoleID == 0
}

func normalizeAndValidateConfigValue(key, value string) (string, error) {
	switch strings.TrimSpace(key) {
	case "app_logo", "app_favicon":
		normalized := strings.TrimSpace(value)
		if normalized == "" {
			return "", nil
		}

		if !strings.HasPrefix(normalized, pngDataURLPrefix) {
			return "", fmt.Errorf("品牌图片必须通过上传生成 PNG 数据")
		}

		if len(normalized) > maxBrandAssetDataURLBytes {
			return "", fmt.Errorf("品牌图片过大，请上传更小图片")
		}

		payload := strings.TrimSpace(strings.TrimPrefix(normalized, pngDataURLPrefix))
		if payload == "" {
			return "", fmt.Errorf("品牌图片数据不能为空")
		}

		if _, err := base64.StdEncoding.DecodeString(payload); err != nil {
			return "", fmt.Errorf("品牌图片数据格式无效")
		}

		return pngDataURLPrefix + payload, nil
	case latencyTargetsConfigKey:
		// A2-01=A7-01：此键此前走 default 原样放行，非法值保存成功后
		// latencyPool 解析失败恒空——全部延迟/回程测量静默停摆，且
		// onLatencyConfigChanged 以空池同步会清掉每个节点的自选测试点。
		// normalizeLatencyTargetsConfig 本为此写就，现正式接线（空串
		// 返回 ""，与既有可清空语义兼容）。
		return normalizeLatencyTargetsConfig(value)
	case monitorTunnelQualityEnabledConfigKey:
		normalized := strings.TrimSpace(strings.ToLower(value))
		switch normalized {
		case "true", "false":
			return normalized, nil
		default:
			return "", fmt.Errorf("隧道质量检测开关配置值无效")
		}
	case monitoring.ConfigMonitorRetentionDays:
		return monitoring.NormalizeMonitoringRetentionDays(value)
	case monitoring.ConfigTunnelQualityProbeIntervalSec:
		return monitoring.NormalizeTunnelQualityProbeIntervalSeconds(value)
	default:
		return value, nil
	}
}

func (h *Handler) notifyTunnelQualityConfigChanged(key string) {
	if h == nil || h.qualityProber == nil {
		return
	}
	switch strings.TrimSpace(key) {
	// FIX2-B（V4-O1）：retention 键补入——probeConfig 三键合读，漏此
	// 键时保留期新值只能等 5s TTL 兜底，与另两键的即时口径不一致。
	case monitorTunnelQualityEnabledConfigKey, monitoring.ConfigTunnelQualityProbeIntervalSec, monitoring.ConfigMonitorRetentionDays:
		h.qualityProber.NotifyConfigChanged()
	}
}

func (h *Handler) isTunnelQualityMonitoringEnabled() bool {
	if h == nil || h.repo == nil {
		return true
	}

	cfg, err := h.repo.GetConfigByName(monitorTunnelQualityEnabledConfigKey)
	if err != nil || cfg == nil {
		return true
	}

	return strings.TrimSpace(strings.ToLower(cfg.Value)) != "false"
}

func (h *Handler) allowLocalRemoteAddr() bool {
	if h == nil || h.repo == nil {
		return false
	}

	cfg, err := h.repo.GetConfigByName(allowLocalRemoteAddrConfigKey)
	if err != nil || cfg == nil {
		return false
	}

	return strings.TrimSpace(strings.ToLower(cfg.Value)) == "true"
}

func (h *Handler) userPackage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}

	claims, ok := r.Context().Value(middleware.ClaimsContextKey).(auth.Claims)
	if !ok {
		response.WriteJSON(w, response.Err(401, "无效的token或token已过期"))
		return
	}

	userID, err := parseUserID(claims.Sub)
	if err != nil {
		response.WriteJSON(w, response.Err(401, "无效的token或token已过期"))
		return
	}

	user, err := h.repo.GetUserByID(userID)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if user == nil {
		response.WriteJSON(w, response.ErrDefault("用户不存在"))
		return
	}

	tunnels, err := h.repo.GetUserPackageTunnels(userID)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}

	forwards, err := h.repo.GetUserPackageForwards(userID)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}

	stats, err := h.repo.GetStatisticsFlows(userID, 24)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}

	sort.Slice(stats, func(i, j int) bool { return stats[i].ID < stats[j].ID })

	tunnelOut := make([]map[string]interface{}, 0, len(tunnels))
	for _, t := range tunnels {
		item := map[string]interface{}{
			"id":             t.ID,
			"userId":         t.UserID,
			"tunnelId":       t.TunnelID,
			"tunnelName":     t.TunnelName,
			"tunnelFlow":     t.TunnelFlow,
			"flow":           t.Flow,
			"flowMiB":        t.FlowMiB,
			"inFlow":         t.InFlow,
			"outFlow":        t.OutFlow,
			"num":            t.Num,
			"flowResetTime":  t.FlowResetTime,
			"expTime":        t.ExpTime,
			"speedId":        nil,
			"speedLimitName": nil,
			"speed":          nil,
		}
		if t.SpeedID.Valid {
			item["speedId"] = t.SpeedID.Int64
		}
		if t.SpeedLimit.Valid {
			item["speedLimitName"] = t.SpeedLimit.String
		}
		if t.Speed.Valid {
			item["speed"] = t.Speed.Int64
		}
		tunnelOut = append(tunnelOut, item)
	}

	forwardOut := make([]map[string]interface{}, 0, len(forwards))
	for _, f := range forwards {
		item := map[string]interface{}{
			"id":          f.ID,
			"name":        f.Name,
			"tunnelId":    f.TunnelID,
			"tunnelName":  f.TunnelName,
			"inIp":        f.InIP,
			"inPort":      nil,
			"remoteAddr":  f.RemoteAddr,
			"inFlow":      f.InFlow,
			"outFlow":     f.OutFlow,
			"status":      f.Status,
			"createdTime": f.CreatedAt,
		}
		if f.InPort.Valid {
			item["inPort"] = f.InPort.Int64
		}
		forwardOut = append(forwardOut, item)
	}

	payload := map[string]interface{}{
		"userInfo": map[string]interface{}{
			"id":            user.ID,
			"name":          user.User,
			"user":          user.User,
			"status":        user.Status,
			"flow":          user.Flow,
			"flowMiB":       user.FlowMiB,
			"inFlow":        user.InFlow,
			"outFlow":       user.OutFlow,
			"num":           user.Num,
			"expTime":       user.ExpTime,
			"flowResetTime": user.FlowResetTime,
			"createdTime":   user.CreatedTime,
			"updatedTime":   nullableNullInt64(user.UpdatedTime),
		},
		"tunnelPermissions": tunnelOut,
		"forwards":          forwardOut,
		"statisticsFlows":   stats,
	}

	response.WriteJSON(w, response.OK(payload))
}

func (h *Handler) updatePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}

	claims, ok := r.Context().Value(middleware.ClaimsContextKey).(auth.Claims)
	if !ok {
		response.WriteJSON(w, response.Err(401, "无效的token或token已过期"))
		return
	}

	userID, err := parseUserID(claims.Sub)
	if err != nil {
		response.WriteJSON(w, response.Err(401, "无效的token或token已过期"))
		return
	}

	var req changePasswordRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("修改账号密码时发生错误"))
		return
	}

	if strings.TrimSpace(req.NewUsername) == "" {
		response.WriteJSON(w, response.ErrDefault("新用户名不能为空"))
		return
	}
	if strings.TrimSpace(req.CurrentPassword) == "" {
		response.WriteJSON(w, response.ErrDefault("当前密码不能为空"))
		return
	}
	if strings.TrimSpace(req.NewPassword) == "" {
		response.WriteJSON(w, response.ErrDefault("新密码不能为空"))
		return
	}
	if strings.TrimSpace(req.ConfirmPassword) == "" {
		response.WriteJSON(w, response.ErrDefault("确认密码不能为空"))
		return
	}
	if req.NewPassword != req.ConfirmPassword {
		response.WriteJSON(w, response.ErrDefault("新密码和确认密码不匹配"))
		return
	}
	if len([]rune(req.NewPassword)) < 6 {
		response.WriteJSON(w, response.ErrDefault("新密码至少6位"))
		return
	}

	user, err := h.repo.GetUserByID(userID)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if user == nil {
		response.WriteJSON(w, response.ErrDefault("用户不存在"))
		return
	}

	passwordMatched, _ := security.VerifyPassword(user.Pwd, req.CurrentPassword)
	if !passwordMatched {
		response.WriteJSON(w, response.ErrDefault("当前密码错误"))
		return
	}

	exists, err := h.repo.UsernameExistsExceptID(req.NewUsername, userID)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if exists {
		response.WriteJSON(w, response.ErrDefault("用户名已存在"))
		return
	}

	hashedPassword, err := security.HashPassword(req.NewPassword)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if err := h.repo.UpdateUserNameAndPassword(userID, req.NewUsername, hashedPassword, time.Now().UnixMilli()); err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}

	response.WriteJSON(w, response.OKEmpty())
}

func (h *Handler) captchaEnabled() (bool, error) {
	cfg, err := h.repo.GetConfigByName("captcha_enabled")
	if err != nil {
		return false, err
	}
	if cfg == nil || !strings.EqualFold(strings.TrimSpace(cfg.Value), "true") {
		return false, nil
	}

	siteCfg, err := h.repo.GetConfigByName("cloudflare_site_key")
	if err != nil {
		return false, err
	}
	if siteCfg == nil || strings.TrimSpace(siteCfg.Value) == "" {
		return false, nil
	}

	secretCfg, err := h.repo.GetConfigByName("cloudflare_secret_key")
	if err != nil {
		return false, err
	}
	if secretCfg == nil || strings.TrimSpace(secretCfg.Value) == "" {
		return false, nil
	}

	return true, nil
}

// captchaTokenPoolMax 是 captcha 令牌池容量上限（R10X-008，
// A-01）：与同仓未认证可写内存表的上限口径对齐——passkey pending
// 池 1000（passkey.go putPasskeyCeremony）、登录双桶 10000、
// gh 镜像 host 表 10000，三表均「上限 + 淘汰」；本池此前无上限，
// 是全集枚举中唯一漏网（A 域口径）。令牌 TTL 统一 5 分钟、仅
// 登录路径单次消费，1000 与 passkey 池同量级即足够，满时淘汰
// 最旧（过期时刻最小者；TTL 统一故等价于最早铸造者）。
const captchaTokenPoolMax = 1000

func (h *Handler) markCaptchaToken(token string) {
	if h == nil {
		return
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return
	}
	now := time.Now().UnixMilli()
	exp := now + int64(5*time.Minute/time.Millisecond)

	h.captchaMu.Lock()
	defer h.captchaMu.Unlock()
	if h.captchaTokens == nil {
		h.captchaTokens = make(map[string]int64)
	}
	for k, v := range h.captchaTokens {
		if v <= now {
			delete(h.captchaTokens, k)
		}
	}
	if _, exists := h.captchaTokens[token]; !exists {
		for len(h.captchaTokens) >= captchaTokenPoolMax {
			var oldestKey string
			var oldestExp int64
			first := true
			for k, v := range h.captchaTokens {
				if first || v < oldestExp {
					oldestKey, oldestExp, first = k, v, false
				}
			}
			if oldestKey == "" {
				break
			}
			delete(h.captchaTokens, oldestKey)
		}
	}
	h.captchaTokens[token] = exp
}

func (h *Handler) consumeCaptchaToken(token string) bool {
	if h == nil {
		return false
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}
	now := time.Now().UnixMilli()

	h.captchaMu.Lock()
	defer h.captchaMu.Unlock()
	if h.captchaTokens == nil {
		return false
	}
	for k, v := range h.captchaTokens {
		if v <= now {
			delete(h.captchaTokens, k)
		}
	}
	exp, ok := h.captchaTokens[token]
	if !ok {
		return false
	}
	delete(h.captchaTokens, token)
	return exp > now
}

// R5-B2 D4/B-004: 统一信封 {code,msg,data,ts}，code 0 = 成功。
// breaking change：旧裸形状 {success,data:{validToken}} 已移除，
// 迁移时把对 success 字段的判断改为 code == 0。
func (h *Handler) writeCaptchaVerifyResult(w http.ResponseWriter, success bool, token string) {
	if success {
		response.WriteJSON(w, response.OK(map[string]interface{}{"validToken": token}))
		return
	}
	response.WriteJSON(w, response.Err(-1, "验证码校验失败"))
}

func decodeJSON(body io.ReadCloser, out interface{}) error {
	return decodeJSONWithLimit(body, out, 10<<20)
}

// decodeJSONWithLimit 与 decodeJSON 相同，但允许调用方指定 body 上限。
// 备份导入需要更大的上限（面板备份可达数十 MB），而普通 API 保持 10MB 防 DoS。
func decodeJSONWithLimit(body io.ReadCloser, out interface{}, limit int64) error {
	defer body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(nil, body, limit))
	decoder.DisallowUnknownFields()
	return decoder.Decode(out)
}

func parseUserID(sub string) (int64, error) {
	id, err := strconv.ParseInt(sub, 10, 64)
	if err != nil || id <= 0 {
		return 0, strconv.ErrSyntax
	}
	return id, nil
}

func userIDFromRequest(r *http.Request) (int64, error) {
	claims, ok := r.Context().Value(middleware.ClaimsContextKey).(auth.Claims)
	if !ok {
		return 0, strconv.ErrSyntax
	}
	return parseUserID(claims.Sub)
}

func userRoleFromRequest(r *http.Request) (int64, int, error) {
	claims, ok := r.Context().Value(middleware.ClaimsContextKey).(auth.Claims)
	if !ok {
		return 0, 0, strconv.ErrSyntax
	}
	userID, err := parseUserID(claims.Sub)
	if err != nil {
		return 0, 0, err
	}
	return userID, claims.RoleID, nil
}

func nullableNullInt64(v sql.NullInt64) interface{} {
	if v.Valid {
		return v.Int64
	}
	return nil
}

// flowCryptoCache caches AES crypto instances by secret to avoid per-request SHA256+GCM init.
var flowCryptoCache sync.Map

func getOrCreateFlowCrypto(secret string) *security.AESCrypto {
	if v, ok := flowCryptoCache.Load(secret); ok {
		return v.(*security.AESCrypto)
	}
	c, err := security.NewAESCrypto(secret)
	if err != nil {
		return nil
	}
	flowCryptoCache.Store(secret, c)
	return c
}

func readAndDecryptFlowBody(body io.ReadCloser, secret string) (string, error) {
	defer body.Close()
	raw, err := io.ReadAll(body)
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return "", nil
	}

	var wrap struct {
		Encrypted bool   `json:"encrypted"`
		Data      string `json:"data"`
		Timestamp int64  `json:"timestamp"`
	}
	if err := json.Unmarshal(raw, &wrap); err != nil || !wrap.Encrypted || strings.TrimSpace(wrap.Data) == "" {
		return text, nil
	}

	crypto := getOrCreateFlowCrypto(secret)
	if crypto == nil {
		return text, nil
	}
	plain, err := crypto.Decrypt(wrap.Data)
	if err != nil {
		return text, nil
	}
	return string(plain), nil
}

// R6X-013（R6-C-02）：Turnstile 校验出站的专用 client 与端点。
// 旧实现走 http.PostForm（DefaultClient：Timeout=0 永不超时、默认
// 跟随 ≤10 跳重定向），且位于登录/验证码路径——上游挂死时调用
// goroutine 无限期阻塞于 Decode、可累积。此处显式 10s 超时（与
// 联邦面同量级）+ 不跟随重定向（校验端点无需重定向，3xx 响应体
// 无 success 字段、按校验失败处理）。client 与 URL 均为包级 var
// 作为注入缝，测试可替换为 httptest 端点/短超时 client；正常路径
// 行为不变。
var (
	turnstileSiteVerifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"
	turnstileClient        = &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
)

func (h *Handler) verifyCloudflareTurnstile(token, secretKey string) bool {
	if token == "" || secretKey == "" {
		return false
	}
	resp, err := turnstileClient.PostForm(turnstileSiteVerifyURL, url.Values{
		"secret":   {secretKey},
		"response": {token},
	})
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var body struct {
		Success bool `json:"success"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return false
	}
	return body.Success
}

type backupExportRequest struct {
	Types []string `json:"types"`
}

func (h *Handler) backupExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}

	var req backupExportRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}

	var backup interface{}
	var err error

	// X-P3-25（L-10-1）：导出绑定请求 ctx，断连即止。
	if len(req.Types) == 0 {
		backup, err = h.repo.ExportAllContext(r.Context())
	} else {
		backup, err = h.repo.ExportPartialContext(r.Context(), req.Types)
	}

	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}

	w.Header().Set("Content-Disposition", "attachment; filename=backup.json")
	w.Header().Set("Content-Type", "application/json")
	// AUDIT-R22 (LAD22-001): 先 Encode 到 buffer，成功后再发 headers+body。
	// 直接 Encode 到 w 时若中途失败（如客户端断开），HTTP 200 已发出，
	// 错误信封会追加到截断的残缺流后，客户端收到既非合法备份也非合法信封的损坏响应。
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(backup); err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if _, err := w.Write(buf.Bytes()); err != nil {
		// headers+body 已发出，此时只能记录日志，无法再返回错误信封。
		log.Printf("backupExport: write response failed: %v", err)
		return
	}
}

type backupImportRequest struct {
	Types []string `json:"types"`
	repo.BackupData
}

func (h *Handler) backupImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}

	var req backupImportRequest
	// AUDIT-R22 (R5R22-001): 备份导入用 100MB 上限。面板备份（含数万用户/分配）
	// 可达数十 MB，10MB 的通用上限会误拒合法备份。nginx 侧已放行 50m（SCR-002），
	// 此处 100MB 与面板规模对齐；仍为有界值，防内存耗尽 DoS。
	if err := decodeJSONWithLimit(r.Body, &req, 100<<20); err != nil {
		// J-008：错误码统一为 -2（原混用 500 与 -2）
		response.WriteJSON(w, response.Err(-2, "请求参数错误"))
		return
	}

	if len(req.Types) == 0 {
		// J-008：错误码统一为 -2
		response.WriteJSON(w, response.Err(-2, "请选择要导入的数据类型"))
		return
	}

	autoBackup, err := h.repo.ExportAllContext(r.Context())
	if err != nil {
		// O7-010：导入前自动备份失败时只记录警告、继续导入流程。
		// 旧逻辑直接中断恢复——但数据库损坏时正是备份失败的时候，
		// 此时最需要从完好的外部备份恢复。
		log.Printf("backupImport: pre-import auto backup failed, continuing: %v", err)
	}

	// O7-009：版本兼容性检查（repo.Import 同样校验，此处提前给中文提示）。
	if req.BackupData.Version != "1.0" {
		// J-008：错误码统一为 -2
		response.WriteJSON(w, response.Err(-2, "备份版本不支持（仅支持 1.0）"))
		return
	}

	// X-P3-21（L-08-3）：导入整单事务维持（语义有意），事务绑定请求
	// ctx——导入期间其余请求经 SQLite busy_timeout（DSN 5000ms）/
	// PG 语句超时走正常超时错误，导入本身断连可取消，不再无限排队。
	result, err := h.repo.ImportContext(r.Context(), &req.BackupData, req.Types)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, fmt.Sprintf("导入失败: %v", err)))
		return
	}

	h.runBackupImportTailHooks(result, &req.BackupData, req.Types, autoBackup)
	response.WriteJSON(w, response.OK(result))
}

// runBackupImportTailHooks is the post-import tail-hook chain shared
// verbatim by backupImport (file import) and gistBackupRestore
// (PORT-3342/G-recovery). It must stay a pure move of the former
// backupImport tail: metering reconciliation, configs cache
// invalidation, kernel reconcile, revoked-authorization cleanup,
// forward repush (backup_import_repush.go, FINAL-P2-1 seven-segment
// predicate, untouched by this batch), and the AutoBackup receipt.
// Gist restore must never call Import and stop here, and must never
// call only reconcileNodeKernels: every hook below runs.
func (h *Handler) runBackupImportTailHooks(result *model.ImportResult, backup *model.BackupData, types []string, autoBackup *model.BackupData) {
	// R6X-010：恢复也是 origin 状态的写路径——导入成功后同步触发
	// 一次计量对账，使恢复出的 metering origin 的伴生行立即物化，
	// 与入站保存/删除路径同口径。此前 backupImport 不在触发点全集
	// 内，伴生最早要等订阅同步循环的节流顺带（≤~90s）才出现，窗
	// 口内恢复的 SS 流量静默不计量。对账幂等自愈，任何成功导入都
	// 触发（部分导入同样可能改写 origin/订阅需求）。
	h.reconcileSSMetering()

	// FIX2-B（V4-N1）：configs 段写库后与 UI 两写入口同口径失效配置
	// 缓存。此前导入绕过全部失效钩子：notify_enabled 与 prober 三键
	// 只能等 5s TTL 兜底（notify_events 因派发时现读已对冲，不在此列）。
	if slices.Contains(types, "configs") && len(backup.Configs) > 0 {
		h.invalidateNotifyConfigCache()
		for key := range backup.Configs {
			h.notifyTunnelQualityConfigChanged(key)
		}
	}

	// R4X-009（E4-02）：reconcile 触发条件由只认 inbounds 段放宽至
	// 任一物化输入集段。nodes（节点地址）/ tunnels（Type 与入口·
	// 出口集）/ forwards（隧道归属与存废，经 ForwardExists 决定
	// relay 行是否计入 allow）同样改写白名单/RemoteAddr 输入集，
	// 未选 inbounds 的部分导入旧口径不触发任何重推、物化与导入
	// 后 DB 脱节驻留。其余段逐段核过均不改写输入集（授权/分配/
	// 分组/权限段的撤销子面经下方 RevokedUserTunnels 漏斗自带
	// 刷新、configs 段经上方缓存钩子），全集表见 R4-FIX-B5 §2。
	// 计数以实际落库行计（被存在性校验跳过的行 DB 未变、不触发）；
	// relays 子段旧条件在 relays-only 备份下同漏、本批一并补齐。
	// 单个 if、单个 goroutine：全量导入多段同时命中也只触发一轮。
	materializedImport := result.NodesImported > 0 || result.TunnelsImported > 0 ||
		result.ForwardsImported > 0 || result.InboundsImported > 0 ||
		backup.Inbounds != nil &&
			(len(backup.Inbounds.Kernels) > 0 || len(backup.Inbounds.Relays) > 0)
	if materializedImport {
		// Restored kernels have no applied config yet: push them to the
		// nodes that are online; the rest get it when they reconnect.
		go func() {
			nodeIDs, err := h.repo.ListKernelNodeIDs()
			if err != nil {
				return
			}
			for _, nodeID := range nodeIDs {
				h.reconcileNodeKernels(nodeID)
			}
		}()
	}

	// R3X-005 facet (a)：导入在分组段成员替换 / 停用收敛中撤销的
	// 组建授权经回执 revokedUserTunnels 回传，逐对清理其转发（与
	// 运行时全部撤销路径的 cleanupForwardsForUserTunnel 同口径），
	// 不留「授权已撤、转发仍跑」的静默残留。
	for _, pair := range result.RevokedUserTunnels {
		h.cleanupForwardsForUserTunnel(pair.UserID, pair.TunnelID)
	}

	// R13X-001（P2）：导入成功后对受影响节点集补一次转发重布（见
	// backup_import_repush.go）。放在撤销清理之后，使重推下发的是
	// 导入终态；离线节点不在此列（重连路径行为不变）。重推失败只
	// 披露进回执，不改写导入成功结论。
	if outcome := h.repushForwardRuntimeAfterImport(result, backup); len(outcome.attempted) > 0 || len(outcome.warnings) > 0 {
		result.RepushNodeIDs = outcome.attempted
		result.RepushFailedNodeIDs = outcome.failed
		result.ImportWarnings = append(result.ImportWarnings, outcome.warnings...)
	}

	result.AutoBackup = autoBackup
}

func (h *Handler) getAnnouncement(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}

	ann, err := h.repo.GetAnnouncement()
	if err != nil {
		response.WriteJSON(w, response.Err(-1, fmt.Sprintf("获取公告失败: %v", err)))
		return
	}

	if ann == nil {
		response.WriteJSON(w, response.OK(map[string]interface{}{
			"content":     "",
			"enabled":     0,
			"update_time": 0,
		}))
		return
	}

	updateTime := ann.CreatedTime
	if ann.UpdatedTime.Valid {
		updateTime = ann.UpdatedTime.Int64
	}

	response.WriteJSON(w, response.OK(map[string]interface{}{
		"content":     ann.Content,
		"enabled":     ann.Enabled,
		"update_time": updateTime,
	}))
}

func (h *Handler) updateAnnouncement(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}

	var req struct {
		Content string `json:"content"`
		Enabled int    `json:"enabled"`
	}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}

	now := time.Now().UnixMilli()
	if err := h.repo.UpsertAnnouncement(req.Content, req.Enabled, now); err != nil {
		response.WriteJSON(w, response.Err(-1, fmt.Sprintf("更新公告失败: %v", err)))
		return
	}

	response.WriteJSON(w, response.OKEmpty())
}
