package handler

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go-backend/internal/config"
	"go-backend/internal/http/response"
	"go-backend/internal/store/repo"

	"gopkg.in/yaml.v3"
)

const (
	panelDeployDirEnv        = "PANEL_DEPLOY_DIR"
	panelBackendContainerEnv = "PANEL_BACKEND_CONTAINER"
	// env 透传丢失时的回退恒用 FLMX 标准名（部署目录
	// /opt/flmx-panel、后端容器 flmx-panel-backend，见
	// newSystemUpgradeExecutor）。存量旧名部署（/opt/flvx-panel、
	// flux-panel-backend）的随版 Compose 恒把两键写进后端容器环
	// 境，env 在场时逐字沿用、再由镜像归属断言把关；只有手写
	// Compose 把两键一并丢掉时才落到回退，此时宁可报不可升级，
	// 也绝不凭旧名去探同机另一套旧谱系部署（F4-02）。
	defaultPanelDeployDir      = "/opt/flmx-panel"
	defaultPanelBackendName    = "flmx-panel-backend"
	dockerSocketPath           = "/var/run/docker.sock"
	systemUpgradeMessage       = "升级 helper 已启动，面板服务将短暂重启"
	systemUpgradeConflictError = "已有面板升级任务执行中"
	// U1-003：升级状态文件名（落盘于部署目录，跨进程/重启可查）。
	systemUpgradeStatusFileName = ".flmx_upgrade_status.json"
	// 改名前的状态文件名：读取时回退，保证升级跨越改名版本时状态不丢。
	legacySystemUpgradeStatusFileName = ".flvx_upgrade_status.json"
	// U1-003：状态文件显示进行中但开始时间早于此时长，视为残留
	// （helper 被强杀等异常情况），允许新升级覆盖，避免永久锁死。
	systemUpgradeStaleAfter = 30 * time.Minute
	// B05-005：helper 存活心跳文件（落盘于部署目录）。helper 运行期
	// 每 30s 刷新其 mtime；心跳新鲜（< systemUpgradeHeartbeatFresh）
	// 即证明 helper 仍在执行，残留判定不得仅凭开始时间一刀切——
	// 慢链路拉镜像可合法超过 systemUpgradeStaleAfter，此前第二轮
	// 升级会被放行并覆盖第一轮的 .upgrade.bak。
	systemUpgradeHeartbeatFileName = ".flmx_upgrade_heartbeat"
	systemUpgradeHeartbeatFresh    = 3 * time.Minute
	// B05-005：helper 内 docker compose pull 的单段上限（秒）。pull
	// 无界时慢链路可无限拖长 helper；超时按拉取失败走回滚终态。
	// 其余段均有界（健康等待 300s×2、up 短），合法总时长由心跳
	// 存活证据覆盖，不再依赖与残留阈值的大小关系。helperScript
	// 中的字面量与本常量由回归测试同值钉住。
	systemUpgradePullTimeoutSeconds = 1200
)

var safeBackendContainerPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
var enableIPv6ComposePattern = regexp.MustCompile(`(?im)^\s*enable_ipv6\s*:\s*['"]?true['"]?\s*(?:#.*)?$`)
var systemUpgradeReleaseBaseURL = githubHTMLBase
var systemUpgradeAPIBaseURL = githubAPIBase
var systemUpgradeHTTPGet = func(client *http.Client, url string) (*http.Response, error) {
	return client.Get(url)
}

type systemUpgradeExecutor struct {
	deployDir        string
	backendContainer string
	// U1-003：本次升级的目标版本与开始时间，透传给 helper 容器的环境变量，
	// helper 在结束/回滚时据此回写升级状态文件。
	upgradeVersion   string
	upgradeStartedAt string
	// R8-003：本次升级的第二锚降级点名（双锚通过时为空），随升级状态
	// 一并落盘并透传 helper 回写，使状态轮询面也能透出单锚/双锚区别
	// （此前只进发起回执的 message、前端又不读，UI 无从区分）。
	upgradeAnchorNote string
	// expectedSQLiteVolume 是升级前记录的后端数据卷名（.env 原有
	// FLMX_SQLITE_VOLUME 值或钉键实读值，见 pinLegacyResourceNames）：
	// 透传 helper 在重建后校验新后端 /app/data 实际挂载卷与之一致，
	// 不一致即回滚。为空表示升级前无从记录（无旧后端等），helper
	// 跳过校验。
	expectedSQLiteVolume string
	// expectedPostgresVolume 是 PG 模式下升级前记录的 PG 数据卷
	// 名（来源同 expectedSQLiteVolume）：透传 helper 在重建后校
	// 验新 PG 容器 /var/lib/postgresql/data 实际挂载卷与之一致，
	// 不一致即回滚（修复批 D 补齐：此前 PG 卷在重建后无任何实读
	// 比对，正确性全押钉键一步）。SQLite 模式恒为空、helper 跳
	// 过校验。
	expectedPostgresVolume string
	// removeLegacyPGContainer 是改名后待 helper 在成功前删除的旧
	// 名 PG 容器（仅 SQLite 模式且 PG 键被改名时非空，见
	// applyIdentityRename 的模式分流裁定）。
	removeLegacyPGContainer string
	// recreatePostgres 表示本次升级改名了 PG 容器键且部署为 PG
	// 模式：helper 的 pull/up 服务集须含 postgres，使被改名容器
	// 随本次升级按新名重建（脚本面 start_panel_after_update 在
	// PG 型先 up postgres 同形态）。
	recreatePostgres bool
	// snapshotPath 是升级前 DB 快照的部署内相对路径
	// （backups/panel-update-<版本>-<时间戳>，仅 SQLite 模式）：
	// 透传 helper 在停旧后端后、重建前落盘，状态文件与发起回执
	// 据此点名。为空表示本次无快照（PG 模式切出，见
	// pgSnapshotDeferred）。
	snapshotPath string
	// pgSnapshotDeferred 表示 PG 模式的 DB 快照切出未落（见
	// prepareUpgradeSnapshot 的切出登记）：helper 仅在日志中点
	// 名跳过，状态/回执不报快照路径。
	pgSnapshotDeferred bool
	// composeProject is the Compose project the running containers belong
	// to. It need not match the deploy directory's name (e.g. installed
	// from /root, deployed in /opt/flvx-panel); without it Compose would
	// try to create a second flux-panel-backend and hit a name conflict.
	composeProject string
	// hostDeployDir is the host directory mounted at deployDir. Compose
	// resolves relative bind mounts (./) against the directory it runs in
	// as a host path, so the helper must run in the host path itself;
	// running in deployDir would remount an empty /opt/flvx-panel.
	hostDeployDir string
}

var hostPathPattern = regexp.MustCompile(`^/[A-Za-z0-9._/@+-]*$`)

// detectHostDeployDir returns the host source of the backend container's
// deploy directory mount, or "" when it cannot be determined.
func (e *systemUpgradeExecutor) detectHostDeployDir(ctx context.Context) string {
	if validateBackendContainerName(e.backendContainer) != nil || !hostPathPattern.MatchString(e.deployDir) {
		return ""
	}
	tmpl := fmt.Sprintf(`{{range .Mounts}}{{if eq .Destination %q}}{{.Source}}{{end}}{{end}}`, e.deployDir)
	out, err := exec.CommandContext(ctx, "docker", "inspect", "-f", tmpl, e.backendContainer).Output()
	if err != nil {
		return ""
	}
	src := strings.TrimSpace(string(out))
	if !hostPathPattern.MatchString(src) {
		return ""
	}
	return src
}

var composeProjectPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// detectComposeProject reads the Compose project label of the backend
// container, the same way panel_install.sh does.
func (e *systemUpgradeExecutor) detectComposeProject(ctx context.Context) string {
	return e.dockerInspectComposeProject(ctx, e.backendContainer)
}

type systemUpgradeCapabilityData struct {
	Capable          bool     `json:"capable"`
	Reasons          []string `json:"reasons"`
	DeployDir        string   `json:"deployDir"`
	BackendContainer string   `json:"backendContainer"`
}

type systemUpgradeReleaseData struct {
	Version     string `json:"version"`
	Name        string `json:"name"`
	PublishedAt string `json:"publishedAt"`
	Prerelease  bool   `json:"prerelease"`
	Channel     string `json:"channel"`
}

type systemUpgradeVersionData struct {
	CurrentVersion string                      `json:"currentVersion"`
	LatestVersion  string                      `json:"latestVersion"`
	HasUpdate      bool                        `json:"hasUpdate"`
	Channel        string                      `json:"channel"`
	Reason         string                      `json:"reason,omitempty"`
	Capability     systemUpgradeCapabilityData `json:"capability"`
	// R5-B4C: 待执行的迁移（结构化，预检失败时非空）。
	PendingMigrations *repo.PendingMigrationStatus `json:"pendingMigrations,omitempty"`
	// 修正 A：需要人工补建管理员。
	AdminBootstrapRequired bool `json:"adminBootstrapRequired,omitempty"`
}

type systemUpgradeCheckData struct {
	CurrentVersion string                      `json:"currentVersion"`
	LatestVersion  string                      `json:"latestVersion"`
	HasUpdate      bool                        `json:"hasUpdate"`
	Channel        string                      `json:"channel"`
	Capability     systemUpgradeCapabilityData `json:"capability"`
	Releases       []systemUpgradeReleaseData  `json:"releases"`
	// B05-003：版本列表拉取失败但重定向兜底成功时的降级说明（与
	// /system/version 的同名字段同口径）；正常路径为空。
	Reason string `json:"reason,omitempty"`
}

type systemUpgradeRunData struct {
	Version         string `json:"version"`
	Channel         string `json:"channel"`
	ComposeAsset    string `json:"composeAsset"`
	HelperContainer string `json:"helperContainer"`
	BackendImageID  string `json:"backendImageId"`
	Message         string `json:"message"`
	// SnapshotPath 点名本次升级的 DB 快照相对路径（SQLite 模式；
	// helper 在重建前落盘于此）。PG 模式快照切出时为空。
	SnapshotPath string `json:"snapshotPath,omitempty"`
	// PgSnapshotDeferred（B7-03 披露半）：PG 模式下升级前 DB 快
	// 照未落盘（切出待后续批次），回执必须显式点名，不得以
	// snapshotPath 为空让运维误以为已有快照兜底。与状态面的同
	// 名字段同口径；SQLite 模式恒 false 不出现。
	PgSnapshotDeferred bool `json:"pgSnapshotDeferred,omitempty"`
}

// systemUpgradeStatusData 是落盘于部署目录的升级状态（U1-003/U1-007）。
// 同步段由 handler 写入 inProgress=true；helper 在成功/回滚时回写终态，
// 使面板重启后仍能回答「是否正在升级 / 上次升级结果」。
type systemUpgradeStatusData struct {
	InProgress bool   `json:"inProgress"`
	Version    string `json:"version,omitempty"`
	StartedAt  string `json:"startedAt,omitempty"`
	Result     string `json:"result,omitempty"`
	FinishedAt string `json:"finishedAt,omitempty"`
	// R8-003：发起本次升级时的第二锚降级点名（单锚降级时非空）；由发起
	// 路径写入、helper 终态回写时原样保留，状态端点据此透出。
	AnchorNote string `json:"anchorNote,omitempty"`
	// SnapshotPath 点名本次升级的 DB 快照相对路径（SQLite 模式）；
	// 由发起路径写入、helper 终态回写时原样保留（与 AnchorNote
	// 同形态）。无快照（PG 模式切出）时为空。
	SnapshotPath string `json:"snapshotPath,omitempty"`
	// PgSnapshotDeferred（B7-03 披露半）：PG 模式快照切出的显式
	// 标记，与回执面同名字段同口径；helper 终态回写时经
	// FLMX_UPGRADE_PG_SNAPSHOT_DEFERRED 环境变量原样保留。
	PgSnapshotDeferred bool `json:"pgSnapshotDeferred,omitempty"`
}

func (e *systemUpgradeExecutor) statusPath() string {
	return filepath.Join(e.deployDir, systemUpgradeStatusFileName)
}

// readUpgradeStatus 读取落盘的升级状态；文件不存在或损坏时返回零值
// （inProgress=false），不视为错误。新名文件缺失时回退读改名前的旧名
// 文件（跨改名版本升级中的状态由旧 helper 写在旧名下）。
// 注意：损坏态在此被折叠为零值——需要区分「无状态」与「文件存在但
// 不可解析」的判定面一律走 assessUpgradeStatus（B05-002），不得依据
// 本函数的零值直接放行。
func (e *systemUpgradeExecutor) readUpgradeStatus() systemUpgradeStatusData {
	st, _ := e.readUpgradeStatusDetailed()
	return st
}

// readUpgradeStatusDetailed 在 readUpgradeStatus 基础上报告 corrupt：
// 状态文件（新名或旧名回退）存在但 JSON 解析失败时为 true。
func (e *systemUpgradeExecutor) readUpgradeStatusDetailed() (st systemUpgradeStatusData, corrupt bool) {
	data, err := os.ReadFile(e.statusPath())
	if err != nil {
		data, err = os.ReadFile(filepath.Join(e.deployDir, legacySystemUpgradeStatusFileName))
	}
	if err != nil {
		return st, false
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return systemUpgradeStatusData{}, true
	}
	return st, false
}

// writeUpgradeStatus 以 0600 权限原子落盘升级状态（B05-002：临时文件
// + Rename 覆盖，读方不会再读到截断中的撕裂内容）。
func (e *systemUpgradeExecutor) writeUpgradeStatus(st systemUpgradeStatusData) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return writeFileAtomicWithMode(e.statusPath(), append(data, '\n'), 0o600)
}

// upgradeStatusAssessment 是升级状态的统一判定结果（B05-002/B14-004）：
// 发起拦截面与状态展示面共用同一判定，杜绝两面口径分裂。
type upgradeStatusAssessment struct {
	// Raw 为落盘原样状态（折算前；损坏时为零值）。
	Raw systemUpgradeStatusData
	// Status 为可对外展示的状态：残留进行中已折算为
	// inProgress=false + result="interrupted"（只读折算，不落盘改写）。
	Status systemUpgradeStatusData
	// Blocking 为 true 时新升级必须被拦截。
	Blocking bool
	// Corrupt 表示状态文件存在但不可解析（撕裂读/文件系统损坏）。
	Corrupt bool
}

// assessUpgradeStatus 是升级状态的唯一判定入口（发起路径与状态端点
// 共用同一函数，三面同口径）：
//   - 完好状态：Blocking 沿 upgradeInProgressBlocking（心跳新鲜度 +
//     30 分钟残留规则）；进行中但不拦截者，展示折算为已中断。
//   - 损坏状态（B05-002）：内容不可解析、无从得知是否进行中，结合
//     心跳新鲜度保守处置——心跳新鲜证明 helper 仍在运行，按进行中
//     拦截且展示同为进行中；心跳缺失/过期按残留放行、展示无进行中。
func (e *systemUpgradeExecutor) assessUpgradeStatus(now time.Time) upgradeStatusAssessment {
	st, corrupt := e.readUpgradeStatusDetailed()
	if corrupt {
		blocking := false
		if hb, ok := e.upgradeHeartbeat(); ok {
			blocking = now.Sub(hb) <= systemUpgradeHeartbeatFresh
		}
		return upgradeStatusAssessment{
			Raw:      st,
			Status:   systemUpgradeStatusData{InProgress: blocking},
			Blocking: blocking,
			Corrupt:  true,
		}
	}
	a := upgradeStatusAssessment{Raw: st, Status: st}
	if !st.InProgress {
		return a
	}
	a.Blocking = e.upgradeInProgressBlocking(st, now)
	if !a.Blocking {
		a.Status.InProgress = false
		a.Status.Result = "interrupted"
	}
	return a
}

// upgradeStatusStale 判断进行中状态是否为残留（开始时间解析失败或距今
// 超过 systemUpgradeStaleAfter）。helper 异常退出时状态文件可能永久停在
// inProgress=true，残留状态不拦截新升级，避免面板升级被永久锁死。
func upgradeStatusStale(st systemUpgradeStatusData, now time.Time) bool {
	started, err := time.Parse(time.RFC3339, st.StartedAt)
	if err != nil {
		return true
	}
	return now.Sub(started) > systemUpgradeStaleAfter
}

// upgradeHeartbeat 返回 helper 心跳文件的最近刷新时间；文件不存在
// （旧版 helper 写入的进行中状态没有心跳）时 ok=false。
func (e *systemUpgradeExecutor) upgradeHeartbeat() (time.Time, bool) {
	info, err := os.Stat(filepath.Join(e.deployDir, systemUpgradeHeartbeatFileName))
	if err != nil {
		return time.Time{}, false
	}
	return info.ModTime(), true
}

// touchUpgradeHeartbeat 由同步段在写下进行中状态后调用：helper 容器
// 启动到脚本首次心跳之间有秒级间隙，先由 handler 预触一次心跳，
// 避免这段间隙里并发请求把刚启动的升级误判为残留。helper 启动失败
// 时同步段会立即回写终态（inProgress=false），预触无副作用。
func (e *systemUpgradeExecutor) touchUpgradeHeartbeat() {
	path := filepath.Join(e.deployDir, systemUpgradeHeartbeatFileName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return
	}
	_ = f.Close()
	now := time.Now()
	_ = os.Chtimes(path, now, now)
}

// upgradeInProgressBlocking 判断落盘的进行中状态是否应拦截新升级
// （B05-005）：helper 心跳文件存在时以心跳新鲜度为准——心跳新鲜
// 证明 helper 仍在执行，无论开始多久都拦截；心跳过期证明 helper
// 已死（每 30s 一拍，3 分钟 ≈ 6 拍未达），立即视为残留放行，不再
// 干等 30 分钟。心跳文件不存在（跨版本升级中、状态由旧 helper
// 写入）时回退 U1-003 的纯时间规则。
func (e *systemUpgradeExecutor) upgradeInProgressBlocking(st systemUpgradeStatusData, now time.Time) bool {
	if !st.InProgress {
		return false
	}
	if hb, ok := e.upgradeHeartbeat(); ok {
		return now.Sub(hb) <= systemUpgradeHeartbeatFresh
	}
	return !upgradeStatusStale(st, now)
}

type systemUpgradeRequest struct {
	Version string `json:"version"`
	Channel string `json:"channel"`
}

func newSystemUpgradeExecutor() *systemUpgradeExecutor {
	deployDir := strings.TrimSpace(os.Getenv(panelDeployDirEnv))
	if deployDir == "" {
		// 未显式指定时恒用 FLMX 标准部署目录，不再回退探测旧名
		// 目录 /opt/flvx-panel（F4-02）：env 丢失＋同机存量旧谱系
		// 部署时，旧回退链会把升级动作指向他方部署，只剩镜像归
		// 属断言兜底；标准名目录不存在时后续能力检查会如实报出
		// 不可升级，不会误动他人部署。
		deployDir = defaultPanelDeployDir
	}
	backendContainer := strings.TrimSpace(os.Getenv(panelBackendContainerEnv))
	if backendContainer == "" {
		backendContainer = defaultPanelBackendName
	}
	return &systemUpgradeExecutor{deployDir: deployDir, backendContainer: backendContainer}
}

func currentPanelVersion() string {
	// 改名过渡：新键 FLMX_VERSION 优先、旧键 FLUX_VERSION 回退。
	// 升级写面双写两键同值（见 updateEnvVersion），两键长期并存，
	// 读面恒以新键优先、旧键只作存量回退。
	version := strings.TrimSpace(os.Getenv("FLMX_VERSION"))
	if version == "" {
		version = strings.TrimSpace(os.Getenv("FLUX_VERSION"))
	}
	if version == "" {
		return "dev"
	}
	return version
}

// parseVersionParts 把版本号解析成数字段序列（严格语义，供 compareVersions）。
// 剥可选的 "v" 前缀后，每段必须是非负整数；含字母/连字符后缀、空段、
// 超长溢出段等不可解析形态一律返回错误。
func parseVersionParts(v string) ([]int, error) {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	if v == "" {
		return nil, fmt.Errorf("version is empty")
	}
	raw := strings.Split(v, ".")
	parts := make([]int, len(raw))
	for i, s := range raw {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil || n < 0 {
			return nil, fmt.Errorf("version segment %q is not a non-negative integer", s)
		}
		parts[i] = n
	}
	return parts, nil
}

// extractLeadingSemver 从「上报型版本串」中提取首段语义版本（R4-N1）。
// node.version 存的是 agent 握手上报的 fullVersion，形如
// "1.3.0 (debian/amd64)"（go-gost/main.go 口径：版本 + 空格 + 括号内
// 的发行版/架构）；联邦对端自述的面板版本同属这一类。这类被比较的
// 存量/上报值不能直接套 compareVersions 的严格语义——后缀不是版本
// 的一部分：先取首个空白前的段，再对该段做严格解析。无空白分隔的
// 粘连后缀（如 "1.3.2-beta1"）、无数字段、空串一律提取失败，调用
// 方维持 fail-closed。用户输入的目标版本不走本函数（各入口已有
// 格式校验 + compareVersions 严格把关）。"dev" 原样透传，保留
// compareVersions 的 dev 口径；各调用方对 dev 的既有豁免（联邦警
// 告）/门控（bypass fail-closed）语义不经本函数改变。
func extractLeadingSemver(v string) (string, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", false
	}
	if v == "dev" {
		return "dev", true
	}
	head := v
	if i := strings.IndexByte(v, ' '); i >= 0 {
		head = v[:i]
	}
	head = strings.TrimPrefix(head, "v")
	if _, err := parseVersionParts(head); err != nil {
		return "", false
	}
	return head, true
}

// compareWithReportedVersion 比较目标版本与上报型版本串
// （node.version / agent 版本 / 对端面板版本）：上报侧先经
// extractLeadingSemver 归一，提取失败返回错误、调用方 fail-closed；
// 目标侧保持 compareVersions 的严格语义。
func compareWithReportedVersion(target, reported string) (int, error) {
	rep, ok := extractLeadingSemver(reported)
	if !ok {
		return 0, fmt.Errorf("reported version %q has no leading semantic version", reported)
	}
	return compareVersions(target, rep)
}

// compareVersions 比较两个版本号（如 "1.0.7"）。
// 返回 -1 表示 v1 < v2，0 表示相等，1 表示 v1 > v2。
// O9-001：用于升级前防止静默降级。
// A11-132-06：旧实现用 Atoi 且吞掉错误，解析失败的段按 0 计——
// "1.3.2-beta1" 会被当成 1.3.0、溢出段被当成 0、"v" 前缀段被当成 0，
// 防降级与 HasUpdate 都可被构造版本号误判。现改为严格语义：任一侧
// 不可解析即返回错误，调用方必须 fail-closed（拒绝升级 / 不报有更新），
// 不再返回一个貌似可信的大小关系。
func compareVersions(v1, v2 string) (int, error) {
	// "dev" 视为最低版本，允许升级到任何正式版本
	if v1 == "dev" && v2 == "dev" {
		return 0, nil
	}
	if v1 == "dev" {
		return -1, nil
	}
	if v2 == "dev" {
		return 1, nil
	}
	parts1, err := parseVersionParts(v1)
	if err != nil {
		return 0, fmt.Errorf("invalid version %q: %w", v1, err)
	}
	parts2, err := parseVersionParts(v2)
	if err != nil {
		return 0, fmt.Errorf("invalid version %q: %w", v2, err)
	}
	maxLen := len(parts1)
	if len(parts2) > maxLen {
		maxLen = len(parts2)
	}
	for i := 0; i < maxLen; i++ {
		var n1, n2 int
		if i < len(parts1) {
			n1 = parts1[i]
		}
		if i < len(parts2) {
			n2 = parts2[i]
		}
		if n1 < n2 {
			return -1, nil
		}
		if n1 > n2 {
			return 1, nil
		}
	}
	return 0, nil
}

func validateBackendContainerName(value string) error {
	if value == "" {
		return fmt.Errorf("backend container name is empty")
	}
	if !safeBackendContainerPattern.MatchString(value) {
		return fmt.Errorf("unsafe backend container name: %s", value)
	}
	return nil
}

func validateUpgradeVersion(value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("upgrade version is empty")
	}
	// G-001：收紧为版本号字符集，防止路径注入（与工作流版本号校验一致）
	// 旧逻辑仅拒绝控制字符，允许 "/" 等路径字符
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '.' || r == '-' || r == '_') {
			return fmt.Errorf("unsafe upgrade version: invalid character %q", r)
		}
	}
	// O9-007：拒绝全由 "." 组成的版本（如 "."、".."、"..."），避免路径/URL 语义歧义
	if strings.Trim(value, ".") == "" {
		return fmt.Errorf("unsafe upgrade version: no version digits")
	}
	return nil
}

func (e *systemUpgradeExecutor) composePath() string {
	return filepath.Join(e.deployDir, "docker-compose.yml")
}
func (e *systemUpgradeExecutor) envPath() string { return filepath.Join(e.deployDir, ".env") }

func (e *systemUpgradeExecutor) capability(ctx context.Context) systemUpgradeCapabilityData {
	reasons := make([]string, 0)
	if !filepath.IsAbs(e.deployDir) {
		reasons = append(reasons, "部署目录必须是绝对路径")
	}
	if err := validateBackendContainerName(e.backendContainer); err != nil {
		reasons = append(reasons, err.Error())
	}
	if out, err := exec.CommandContext(ctx, "docker", "--version").CombinedOutput(); err != nil {
		reasons = append(reasons, fmt.Sprintf("docker CLI不可用: %v: %s", err, strings.TrimSpace(string(out))))
	}
	if info, err := os.Stat(dockerSocketPath); err != nil {
		reasons = append(reasons, "docker socket不可用: "+err.Error())
	} else if info.IsDir() {
		reasons = append(reasons, "docker socket路径不是文件")
	}
	if info, err := os.Stat(e.composePath()); err != nil {
		reasons = append(reasons, "部署docker-compose.yml不可用: "+err.Error())
		hint := "后端容器挂载的部署目录里没有 docker-compose.yml（通常是旧版面板内升级把挂载弄丢了）。请在服务器上进入存放 docker-compose.yml 和 .env 的目录（常见为 /root 或安装脚本所在目录），执行 docker compose up -d --force-recreate backend frontend，或在该目录重新运行安装脚本选择“更新面板”"
		if src := e.detectHostDeployDir(ctx); src != "" {
			hint += "。当前挂载的宿主机目录：" + src
		}
		reasons = append(reasons, hint)
	} else if info.IsDir() {
		reasons = append(reasons, "部署docker-compose.yml不是文件")
	}
	if info, err := os.Stat(e.envPath()); err != nil {
		reasons = append(reasons, "部署.env不可用: "+err.Error())
	} else if info.IsDir() {
		reasons = append(reasons, "部署.env不是文件")
	}
	if out, err := exec.CommandContext(ctx, "docker", "compose", "version").CombinedOutput(); err != nil {
		reasons = append(reasons, fmt.Sprintf("docker compose不可用: %v: %s", err, strings.TrimSpace(string(out))))
	}
	if _, err := e.currentBackendImage(ctx); err != nil {
		reasons = append(reasons, err.Error())
	} else if err := e.verifyBackendImageOwnership(ctx); err != nil {
		// R2-006（AI-08）：镜像 ID 可读只证明同名容器存在，不证明
		// 它跑的是本面板的镜像——归属断言不过同样不可升级。
		reasons = append(reasons, err.Error())
	}

	return systemUpgradeCapabilityData{
		Capable:          len(reasons) == 0,
		Reasons:          reasons,
		DeployDir:        e.deployDir,
		BackendContainer: e.backendContainer,
	}
}

func (e *systemUpgradeExecutor) selectComposeAsset(current []byte) string {
	if enableIPv6ComposePattern.Match(current) {
		return "docker-compose-v6.yml"
	}
	return "docker-compose-v4.yml"
}

func (e *systemUpgradeExecutor) helperScript() string {
	return `set -eu
LOGFILE="$PANEL_DEPLOY_DIR/upgrade.log"
STATUS_FILE="$PANEL_DEPLOY_DIR/.flmx_upgrade_status.json"
HEARTBEAT_FILE="$PANEL_DEPLOY_DIR/.flmx_upgrade_heartbeat"
log() { echo "[$(date '+%Y-%m-%d %H:%M:%S')] $*" | tee -a "$LOGFILE"; }

# 本次升级的服务集（词拆分是有意的，勿加引号）：缺省 backend
# frontend；PG 模式且本次改名了 PG 容器键时，面板侧透传含
# postgres 的服务集，被改名容器随本次升级按新名重建；回滚重建
# 用同一服务集，恢复旧 .env 后按旧名重建。
UPGRADE_SERVICES="${FLMX_UPGRADE_SERVICES:-backend frontend}"

# U1-003：回写升级状态文件（与后端 systemUpgradeStatusData 结构体对应）。
# $1=inProgress(true/false)，$2=result。version/startedAt 由环境变量传入。
write_status() {
  # B05-002：先写同目录临时文件再 mv 原子覆盖，避免后端在回写瞬间
  # 读到截断中的撕裂内容（旧实现 printf > 直接截断目标文件）。
  # R8-003：透传的第二锚降级点名（已由面板侧 JSON 转义）原样保留进
  # 终态，未透传时不写该字段。
  anchor_field=""
  if [ -n "${FLMX_UPGRADE_ANCHOR_NOTE:-}" ]; then
    anchor_field=", \"anchorNote\": \"${FLMX_UPGRADE_ANCHOR_NOTE}\""
  fi
  # DB 快照相对路径同形态保留进终态，但仅在快照真正落盘后
  # （SNAPSHOT_TAKEN 由快照段成功后置位）才写：pull 失败等快照
  # 前的回滚终态若虚报路径，运维会去找一份不存在的备份。路径
  # 由面板侧以版本＋时间戳派生，字符集受限（字母数字/.-/），
  # 无 JSON 转义需求。
  snapshot_field=""
  if [ -n "${SNAPSHOT_TAKEN:-}" ]; then
    snapshot_field=", \"snapshotPath\": \"${FLMX_UPGRADE_SNAPSHOT_DIR}\""
  fi
  # B7-03：PG 快照切出标记同形态保留进终态（面板侧透传 env 为
  # 1 时写 pgSnapshotDeferred: true），否则终态会把发起态的披
  # 露冲掉、运维在结果面又看不到「本次无升级前快照」。
  deferred_field=""
  if [ "${FLMX_UPGRADE_PG_SNAPSHOT_DEFERRED:-}" = "1" ]; then
    deferred_field=", \"pgSnapshotDeferred\": true"
  fi
  printf '{"inProgress": %s, "version": "%s", "startedAt": "%s", "result": "%s", "finishedAt": "%s"%s%s%s}\n' \
    "$1" "${FLMX_UPGRADE_VERSION:-}" "${FLMX_UPGRADE_STARTED_AT:-}" "$2" "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "$anchor_field" "$snapshot_field" "$deferred_field" > "$STATUS_FILE.tmp"
  chmod 600 "$STATUS_FILE.tmp" 2>/dev/null || true
  mv -f "$STATUS_FILE.tmp" "$STATUS_FILE"
}

# O9-002：失败时自动恢复备份文件
# 旧逻辑：pull/up 失败时直接 exit 1，已替换的 compose.yml/.env 不恢复，面板无法启动
# F-028（A7-004）：只恢复文件不算回滚——容器仍按新定义运行（或已退出），
# 终态却写 rolled_back 造成「假回滚成功」。$1=recreate（默认）时恢复文件后
# 用旧定义重建容器并复跑健康检查，成功才写 rolled_back；重建或健康失败
# 写 rollback_failed 并保留 .bak 告警人工介入。pull 阶段失败时新容器
# 从未创建，调用方传 files 只恢复文件、不做无谓重建。
rollback() {
  log "升级失败，正在回滚..."
  if [ -f docker-compose.yml.upgrade.bak ]; then
    cp docker-compose.yml.upgrade.bak docker-compose.yml
    log "已恢复 docker-compose.yml"
  fi
  if [ -f .env.upgrade.bak ]; then
    cp .env.upgrade.bak .env
    log "已恢复 .env"
  fi
  if [ "${1:-recreate}" = "files" ]; then
    # U1-003：回滚终态回写状态文件，.bak 保留供人工排查（不删除）。
    write_status "false" "rolled_back"
    log "回滚完成（仅文件恢复，容器未重建），请检查 upgrade.log"
    return 0
  fi
  log "用恢复后的配置重建旧容器..."
  if docker compose up -d --force-recreate --remove-orphans $UPGRADE_SERVICES >> "$LOGFILE" 2>&1 && wait_backend_healthy; then
    write_status "false" "rolled_back"
    log "回滚完成：旧容器已按恢复后的配置重建并通过健康检查（.bak 保留）"
  else
    write_status "false" "rollback_failed"
    log "错误: 回滚重建旧容器或健康检查失败，请立即手动处理（.bak 已保留）"
  fi
}

# U1-002：健康校验。up 返回 0 只代表容器已创建，不能证明新后端进程健康：
# 连续 30s 内 compose ps 状态为 running 且镜像自带健康探针
# （http://localhost:6365/flow/test，与镜像 healthcheck 同一锚点）通过，才算升级成功。
# 最长等 5 分钟，超时视为升级失败并回滚。
wait_backend_healthy() {
  health_deadline=$(( $(date +%s) + 300 ))
  stable_since=0
  while [ "$(date +%s)" -lt "$health_deadline" ]; do
    state=$(docker compose ps backend --format '{{.State}}' 2>/dev/null | head -n 1)
    if [ "$state" = "running" ]; then
      container=$(docker compose ps backend --format '{{.Name}}' 2>/dev/null | head -n 1)
      probe_ok=1
      if [ -n "$container" ]; then
        if docker exec "$container" wget --no-verbose --tries=1 --spider http://localhost:6365/flow/test >/dev/null 2>&1; then
          probe_ok=0
        fi
      fi
      if [ "$probe_ok" -eq 0 ]; then
        if [ "$stable_since" -eq 0 ]; then
          stable_since=$(date +%s)
        fi
        if [ $(( $(date +%s) - stable_since )) -ge 30 ]; then
          log "新后端健康检查通过（持续 running 且进程探针成功）"
          return 0
        fi
      else
        stable_since=0
      fi
    else
      stable_since=0
    fi
    sleep 5
  done
  log "错误: 后端健康检查超时（5 分钟内未稳定）"
  return 1
}

cd "$PANEL_DEPLOY_DIR"

# B05-005：存活心跳——运行期每 30s 刷新心跳文件 mtime，面板的残留
# 判定据此识别 helper 仍在执行（慢链路合法超过 30 分钟不再被误判
# 残留、放行第二轮覆盖 .upgrade.bak）。EXIT trap 收尾心跳循环。
# B05-001（R2-K2）：心跳循环绑定主脚本 PID 并逐拍校验其存活——主
# 脚本被 SIGKILL 时 EXIT trap 不执行，心跳子进程若被 init 收养就会
# 成为孤儿、永远刷新心跳，面板据此无限期误判「升级中」。父死即自
# 退后心跳自然过期，残留判定按既有新鲜度规则放行。容器形态下主
# 脚本是命名空间 PID 1，$$ 恒为 1、校验恒真，命名空间拆除时循环
# 随之消亡，原有连带回收行为不变。
touch "$HEARTBEAT_FILE" 2>/dev/null || true
HB_PARENT=$$
( while :; do
    sleep 30
    kill -0 "$HB_PARENT" 2>/dev/null || exit 0
    touch "$HEARTBEAT_FILE" 2>/dev/null || exit 0
  done ) &
HB_PID=$!
trap 'kill "$HB_PID" 2>/dev/null || true' EXIT

echo "" > "$LOGFILE"
log "开始面板升级"
log "工作目录: $(pwd)"
log "Compose 项目: ${COMPOSE_PROJECT_NAME:-（按目录名）}"

if [ ! -f docker-compose.yml ]; then
  log "错误: docker-compose.yml 不存在"
  write_status "false" "aborted"
  exit 1
fi
if [ ! -f .env ]; then
  log "错误: .env 不存在"
  write_status "false" "aborted"
  exit 1
fi

log "拉取新镜像..."
# B05-005：pull 单段上限 1200s（与面板 systemUpgradePullTimeoutSeconds
# 同值，回归测试钉住）；无界 pull 在慢链路上可无限拖长本 helper，
# 超时按拉取失败走回滚终态（files 恢复，容器未动）。
if ! timeout 1200 docker compose pull $UPGRADE_SERVICES >> "$LOGFILE" 2>&1; then
  log "错误: 拉取镜像失败"
  rollback files
  exit 1
fi

log "等待旧容器释放资源..."
sleep 3

# DB 快照（1.9.9，SQLite 模式，面板侧以 FLMX_UPGRADE_SNAPSHOT_DIR
# 透传部署内相对路径时启用）：停旧后端后、重建前，把数据卷整目
# 录复制到部署目录的快照目录 sqlite/ 子目录（整目录口径与脚本
# 面 backup_sqlite_for_update 一致：含 gost.db 与 -wal/-shm、剔
# 除可弃的 github-cache），随后按 mtime 保留最近 3 份
# panel-update-*（脚本面 BACKUP_RETENTION_KEEP=3 同口径，两面
# 备份同目录同 glob 一并轮换）。任一步失败＝中止升级：此时新容
# 器尚未创建，rollback 按旧文件重建被停的旧后端收口（快照失败
# 不得静默跳过后照常升级）。快照只备不自动恢复，恢复由人工按
# 状态文件点名的路径执行。源目录经 --volumes-from 与后端同挂
# 载、恒为 /app/data（FLMX_UPGRADE_DATA_DIR 仅供测试覆盖）。
if [ -n "${FLMX_UPGRADE_PG_SNAPSHOT_DEFERRED:-}" ]; then
  log "PostgreSQL 模式：DB 快照未随面板内升级落地（切出登记项），本次升级不创建 DB 快照"
fi
if [ -n "${FLMX_UPGRADE_SNAPSHOT_DIR:-}" ]; then
  SNAPSHOT_DIR="$PANEL_DEPLOY_DIR/$FLMX_UPGRADE_SNAPSHOT_DIR"
  DATA_DIR="${FLMX_UPGRADE_DATA_DIR:-/app/data}"
  log "创建升级前 DB 快照: $FLMX_UPGRADE_SNAPSHOT_DIR"
  if ! docker compose stop backend >> "$LOGFILE" 2>&1; then
    log "错误: 停止旧后端以创建 DB 快照失败，中止升级"
    rollback
    exit 1
  fi
  snapshot_ok=1
  mkdir -p "$SNAPSHOT_DIR/sqlite" || snapshot_ok=0
  if [ "$snapshot_ok" -eq 1 ] && ! cp -a "$DATA_DIR/." "$SNAPSHOT_DIR/sqlite/" >> "$LOGFILE" 2>&1; then
    # cp -a 在属主保留受限的环境（卷内文件属主为奇异 uid 映射
    # 等）会因 chown 失败报错，而文件内容其实已复制到位。快照
    # 以内容完整为准：降级为仅内容复制重试一次（恢复时由操作
    # 者以 root 拷回数据卷，与脚本面 docker cp 的差异仅在属主
    # 一维，降级在日志中点名）；两次都失败才判快照失败。
    log "警告: cp -a 快照复制失败，降级为仅内容复制重试（cp 输出见 upgrade.log）"
    rm -rf "$SNAPSHOT_DIR/sqlite"
    mkdir -p "$SNAPSHOT_DIR/sqlite" && cp -r "$DATA_DIR/." "$SNAPSHOT_DIR/sqlite/" >> "$LOGFILE" 2>&1 || snapshot_ok=0
  fi
  if [ "$snapshot_ok" -eq 1 ]; then
    rm -rf "$SNAPSHOT_DIR/sqlite/github-cache" || snapshot_ok=0
  fi
  if [ "$snapshot_ok" -ne 1 ]; then
    log "错误: DB 快照复制失败，中止升级"
    rollback
    exit 1
  fi
  ls -dt "$PANEL_DEPLOY_DIR"/backups/panel-update-* 2>/dev/null | tail -n +4 | while IFS= read -r old_backup; do
    rm -rf "$old_backup" 2>/dev/null || true
  done
  SNAPSHOT_TAKEN=1
  log "DB 快照完成: $FLMX_UPGRADE_SNAPSHOT_DIR/sqlite"
fi

log "重启服务（force-recreate）..."
if ! docker compose up -d --force-recreate --remove-orphans $UPGRADE_SERVICES >> "$LOGFILE" 2>&1; then
  log "错误: 重启服务失败"
  rollback
  exit 1
fi

if ! wait_backend_healthy; then
  log "错误: 新后端未通过健康检查"
  rollback
  exit 1
fi

# 数据卷校验（1.9.9）：健康只证明新进程能跑，不证明它挂的是原来的
# 数据卷——无键存量若被新模板缺省卷名接管，后端会对着空卷按新装
# 播种且健康检查照样通过（1.9.8 生产事故）。面板侧升级前已把数据
# 卷名钉进 .env 并经 FLMX_EXPECTED_SQLITE_VOLUME 透传期望值；此处
# 实读新后端 /app/data 的挂载卷名比对，不一致（或读不到）即按挂错
# 卷处理：走既有 rollback 恢复旧文件与旧容器，绝不以成功收场。
# 期望值为空（升级前无旧后端可记录）时跳过。
if [ -n "${FLMX_EXPECTED_SQLITE_VOLUME:-}" ]; then
  new_backend=$(docker compose ps backend --format '{{.Name}}' 2>/dev/null | head -n 1 || true)
  actual_volume=""
  if [ -n "$new_backend" ]; then
    actual_volume=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/app/data"}}{{.Name}}{{end}}{{end}}' "$new_backend" 2>/dev/null || true)
  fi
  if [ "$actual_volume" != "$FLMX_EXPECTED_SQLITE_VOLUME" ]; then
    log "错误: 新后端 /app/data 实际挂载卷「${actual_volume:-（空）}」与升级前记录的数据卷「$FLMX_EXPECTED_SQLITE_VOLUME」不一致，疑似挂到空卷，执行回滚"
    rollback
    exit 1
  fi
  log "数据卷校验通过: /app/data 挂载卷与升级前一致（$actual_volume）"
fi

# PG 数据卷校验（1.9.9 修复批 D，PG 模式，面板侧以
# FLMX_EXPECTED_POSTGRES_VOLUME 透传期望值时启用）：PG 模式下真
# 数据在 PG 卷里，/app/data 校验与之无关——此前 PG 卷重建后无任
# 何实读比对，钉键一步若漏（自定义卷名＋无键＋PG 容器定位不到
# 的子形）新 PG 会落模板缺省空卷、后端播种后健康通过（1.9.8 事
# 故在 PG 面的同构形态）。此处实读 PG 容器
# /var/lib/postgresql/data 的挂载卷名比对，不一致（或读不到）即
# 按挂错卷走 rollback，绝不以成功收场。期望值为空（SQLite 模
# 式或升级前无从记录）时跳过。PG 容器未经本次重建时按 -a 取现
# 存容器读其挂载（校验的是卷身份而非存活）。
if [ -n "${FLMX_EXPECTED_POSTGRES_VOLUME:-}" ]; then
  new_pg=$(docker compose ps -a postgres --format '{{.Name}}' 2>/dev/null | head -n 1 || true)
  actual_pg_volume=""
  if [ -n "$new_pg" ]; then
    actual_pg_volume=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/var/lib/postgresql/data"}}{{.Name}}{{end}}{{end}}' "$new_pg" 2>/dev/null || true)
  fi
  if [ "$actual_pg_volume" != "$FLMX_EXPECTED_POSTGRES_VOLUME" ]; then
    log "错误: PG 容器 /var/lib/postgresql/data 实际挂载卷「${actual_pg_volume:-（空）}」与升级前记录的数据卷「$FLMX_EXPECTED_POSTGRES_VOLUME」不一致，疑似挂到空卷，执行回滚"
    rollback
    exit 1
  fi
  log "PG 数据卷校验通过: /var/lib/postgresql/data 挂载卷与升级前一致（$actual_pg_volume）"
fi

# 改名收尾（1.9.9，SQLite 模式，面板侧透传旧 PG 容器名时启用）：
# PG 键已在同步段改写为现行名，旧 PG 容器随本次升级删除——脚本
# 面改名流在 SQLite 模式下停删 PG 容器且不重建（菜单 4 迁移按
# 需拉起，RR4-F7-02 已注记无流程依赖），终态须一致。删除点置
# 于健康＋卷名校验全过之后：此前任一步失败走 rollback 时，旧
# 容器与恢复的旧 .env 仍成对在场、不留半改名态；删除本身失败
# 同样走 rollback。容器已不在场视为已达成目标状态，不报错。
if [ -n "${FLMX_UPGRADE_REMOVE_PG_CONTAINER:-}" ]; then
  if docker inspect "$FLMX_UPGRADE_REMOVE_PG_CONTAINER" >/dev/null 2>&1; then
    log "删除改名前的旧 PG 容器: $FLMX_UPGRADE_REMOVE_PG_CONTAINER"
    if ! docker rm -f "$FLMX_UPGRADE_REMOVE_PG_CONTAINER" >> "$LOGFILE" 2>&1; then
      log "错误: 删除旧 PG 容器失败"
      rollback
      exit 1
    fi
  fi
fi

log "升级完成"
# U1-002：只有健康检查通过才清理备份文件；升级终态写入状态文件
write_status "false" "success"
rm -f docker-compose.yml.upgrade.bak .env.upgrade.bak
`
}

func (e *systemUpgradeExecutor) buildHelperRunArgs(imageID, helperName string) ([]string, error) {
	if err := validateBackendContainerName(e.backendContainer); err != nil {
		return nil, err
	}
	args := []string{
		"run", "-d", "--rm", "--name", helperName,
		"--volumes-from", e.backendContainer,
		"-v", dockerSocketPath + ":" + dockerSocketPath,
	}
	if hostPathPattern.MatchString(e.hostDeployDir) && e.hostDeployDir != e.deployDir {
		args = append(args,
			"-v", e.hostDeployDir+":"+e.hostDeployDir,
			"-e", panelDeployDirEnv+"="+e.hostDeployDir)
	} else {
		args = append(args, "-e", panelDeployDirEnv+"="+e.deployDir)
	}
	if composeProjectPattern.MatchString(e.composeProject) {
		args = append(args, "-e", "COMPOSE_PROJECT_NAME="+e.composeProject)
	}
	// U1-003：透传目标版本/开始时间，helper 回写升级状态文件时用。
	if e.upgradeVersion != "" {
		args = append(args,
			"-e", "FLMX_UPGRADE_VERSION="+e.upgradeVersion,
			"-e", "FLMX_UPGRADE_STARTED_AT="+e.upgradeStartedAt)
	}
	// R8-003：透传第二锚降级点名（JSON 字符串转义后的内容、不含外层
	// 引号），helper 回写终态时原样保留进状态文件；为空则不透传，
	// helper 侧缺省不写 anchorNote 字段。
	if e.upgradeAnchorNote != "" {
		if raw, err := json.Marshal(e.upgradeAnchorNote); err == nil && len(raw) >= 2 {
			args = append(args,
				"-e", "FLMX_UPGRADE_ANCHOR_NOTE="+string(raw[1:len(raw)-1]))
		}
	}
	// 钉卷校验期望值：升级前记录的数据卷名透传 helper，重建后实读
	// 新后端挂载卷比对（见 helperScript 数据卷校验段）；为空不透传、
	// helper 跳过校验。
	if e.expectedSQLiteVolume != "" {
		args = append(args,
			"-e", expectedSQLiteVolumeEnvKey+"="+e.expectedSQLiteVolume)
	}
	// PG 数据卷校验期望值（PG 模式）：同上，helper 健康门后实读
	// PG 容器挂载卷比对；为空不透传、helper 跳过校验。
	if e.expectedPostgresVolume != "" {
		args = append(args,
			"-e", expectedPostgresVolumeEnvKey+"="+e.expectedPostgresVolume)
	}
	// 改名/快照安排（见 applyIdentityRename / prepareUpgradeSnapshot）：
	// 均仅在非缺省时透传，helper 侧缺失即按缺省行为（服务集
	// backend frontend、不删旧 PG 容器、无 DB 快照）。
	if e.recreatePostgres {
		args = append(args,
			"-e", upgradeServicesEnvKey+"=backend frontend postgres")
	}
	if e.removeLegacyPGContainer != "" {
		args = append(args,
			"-e", removePGContainerEnvKey+"="+e.removeLegacyPGContainer)
	}
	if e.snapshotPath != "" {
		args = append(args,
			"-e", snapshotDirEnvKey+"="+e.snapshotPath)
	}
	if e.pgSnapshotDeferred {
		args = append(args,
			"-e", pgSnapshotDeferredEnvKey+"=1")
	}
	return append(args,
		"--entrypoint", "/bin/sh", imageID,
		"-c", e.helperScript(),
	), nil
}

func (e *systemUpgradeExecutor) updateEnvVersion(envPath, version string) error {
	if err := validateUpgradeVersion(version); err != nil {
		return err
	}
	mode, err := fileModeOrDefault(envPath, 0o600)
	if err != nil {
		return err
	}
	mode = capEnvFileMode(mode)
	data, err := os.ReadFile(envPath)
	if err != nil {
		return err
	}
	// 版本键双写（1.9.9 起，与脚本面 activate_panel_update_files 同
	// 口径，兑现改名裁定的「双写两版过渡」）：FLMX_VERSION 与
	// FLUX_VERSION 同值、各自就地更新、缺失补写，不再删行收敛为
	// 单键——旧实现把 .env 收敛为只有 FLMX_VERSION，只认
	// FLUX_VERSION 的带外旧消费者（旧脚本/旧面板的版本读面）在经
	// 面板内升级后会读空（parity 矩阵 D5）。按键名精确匹配
	// （U1-009：HasPrefix 会误伤 FLUX_VERSION_EXTRA= 等同前缀键），
	// 细节见 applyEnvKeyUpdates。
	content := applyEnvKeyUpdates(data, []envKeyValue{
		{envKeyFLMXVersion, version},
		{envKeyFLUXVersion, version},
	})
	return writeFileAtomicWithMode(envPath, content, mode)
}

// —— 存量资源名钉键（1.9.9，与 panel_install.sh 的
// panel_pin_missing_resource_keys 同口径、补齐面板内升级面）——
//
// 1.9.8 生产事故：无身份键的存量部署（.env 无 FLMX_SQLITE_VOLUME
// 等键）经面板内升级换上新 compose 模板后，模板卷名表达式
// ${FLMX_SQLITE_VOLUME:-flmx-sqlite-data} 落缺省新卷，后端挂空卷按
// 新装播种，真库卷被晾在一边。脚本更新流早有钉键防线（动容器前
// inspect 旧后端实读卷名钉进 .env），面板内升级面（本文件）此前
// 只有 preserveComposeNetworks 保网络段、updateEnvVersion 改版本
// 键，对卷名零处理——双面同能力未对齐的缺口在此补齐：升级动任何
// 文件之前，把缺失的资源名键按旧容器的现行有效值钉进 .env，已有
// 键原则上逐字保留；两卷键（FLMX_SQLITE_VOLUME /
// FLMX_POSTGRES_VOLUME）与在跑旧容器实读冲突时以实读为准改写
// 并点名（修复批 D，见 planLegacyResourcePins）。
const (
	envKeyFLMXVersion    = "FLMX_VERSION"
	envKeyFLUXVersion    = "FLUX_VERSION"
	envKeySQLiteVolume   = "FLMX_SQLITE_VOLUME"
	envKeyPostgresVolume = "FLMX_POSTGRES_VOLUME"
	envKeyNetwork        = "FLMX_NETWORK"
	envKeyComposeProject = "COMPOSE_PROJECT_NAME"
	// envKeyPanelPostgresContainer 是部署 .env 中 PG 容器名的身份
	// 键（脚本 write_new_panel_identity 落盘同名键）：定位旧 PG 容
	// 器时优先取部署自带声明，其次按 Compose 项目+服务标签查找。
	envKeyPanelPostgresContainer = "PANEL_POSTGRES_CONTAINER"
	// expectedSQLiteVolumeEnvKey 是透传 helper 的期望数据卷名键：
	// helper 重建后实读新后端挂载卷与之比对，不一致即回滚。
	expectedSQLiteVolumeEnvKey = "FLMX_EXPECTED_SQLITE_VOLUME"
	// expectedPostgresVolumeEnvKey 是透传 helper 的期望 PG 数据
	// 卷名键（PG 模式）：helper 健康门后实读 PG 容器挂载卷与之
	// 比对，不一致即回滚。
	expectedPostgresVolumeEnvKey = "FLMX_EXPECTED_POSTGRES_VOLUME"
	// legacyPostgresDataVolume 是脚本面同口径的历史缺省 PG 卷名：
	// PG 容器不在场但该卷存在时钉它（脚本面板迁移/存量识别同名）。
	legacyPostgresDataVolume = "postgres_data"
	// legacySQLiteDataVolume 是历史缺省 SQLite 卷名（脚本面同口
	// 径）：事故态门以「该卷存在且含 gost.db」为真库证据。
	legacySQLiteDataVolume = "sqlite_data"
	// newSQLiteDataVolume / newPostgresDataVolume 是现行模板的缺
	// 省卷名（模板表达式 ${FLMX_SQLITE_VOLUME:-flmx-sqlite-data}
	// 等的回退值，未计实例后缀；事故态门比对时经
	// panelInstanceSuffix 补后缀，与脚本面同式）。
	newSQLiteDataVolume   = "flmx-sqlite-data"
	newPostgresDataVolume = "flmx-postgres-data"
)

// errDataVolumeUndetermined 是 fail-closed 哨兵：旧后端在场但其实
// 读数据卷名为空、.env 又无键——中止升级且不做任何更改。
var errDataVolumeUndetermined = fmt.Errorf("无法确定现有数据卷名")

// errPGVolumeUndetermined 是 PG 模式的 fail-closed 哨兵（修复批
// D）：PG 模式、.env 无 FLMX_POSTGRES_VOLUME、实读不到 PG 卷名、
// 历史缺省卷 postgres_data 也不存在——与 SQLite 卷同标准中止升
// 级且不做任何更改（否则新模板缺省 flmx-postgres-data 会接管成
// 空卷，是 1.9.8 事故在 PG 面的同构复现）。
var errPGVolumeUndetermined = fmt.Errorf("无法确定现有 PostgreSQL 数据卷名")

// errAccidentStateVolume 是事故态门哨兵（R1-01-M1）：.env 缺卷
// 键、在跑容器实读恰为模板新缺省卷名、而旧名卷仍含真实数据
// ——该部署是 1.9.8 式事故后未救援的形态，此时按实读钉键会把
// 空卷名钉进 .env、把事故态正式化（此后自检亦以错卷为期望）。
// 故中止升级并点名两卷与人工钉法，由人确认数据所在再钉。与脚
// 本面 panel_pin_missing_resource_keys 的事故态门同谓词；批 D
// 只在脚本面×SQLite 一格立门，本哨兵补齐其余三格中的 Go 面两格
// （脚本面 PG 格在脚本侧同批补齐）。
var errAccidentStateVolume = fmt.Errorf("疑似事故态：实读卷为模板新缺省卷而旧名卷仍含真实数据")

// dockerResourceNamePattern 是 Docker 卷/网络名的合法形态（名称必
// 以字母数字开头，其后为字母数字与 _ . -）：inspect 实读值不合此
// 式视为未读到，不钉入 .env。
var dockerResourceNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

// envKeyValue 是一条待钉入 .env 的键值。
type envKeyValue struct {
	key   string
	value string
}

// parseEnvFileValues 解析 .env 内容为键值表。值语义与 Docker
// Compose 消费该文件时的解析（compose-go 的 dotenv）对齐（修复
// 批 D，RA-1 L-4）：成对引号（单/双）剥除、双引号内转义展开、
// 未引号值的行尾注释（值首或空白后的 # 起）剥除、重复键以
// 后现为准。实证依据：compose-go v2 dotenv.Parse 实测——重复
// 键取后值、"v"/'v' 剥引号、`v # c` 剥注释、`abc#def` 的 # 保
// 留、"a\nb" 展开为真换行。旧实现取原文＋首现：手写 .env 的
// 带引号值（如 FLMX_SQLITE_VOLUME="sqlite_data"、"" 空引号、
// 值后行尾注释）会被当成含引号/注释的真值，规划与 Compose 的
// 运行时解析分歧，升级后卷名门必然误判、健康升级被假回滚。
// 两点有意不跟：不做 $ 变量展开（规划面只需卷名等字面值，与
// Compose 的插值是两个层面）；引号未闭合时 Compose 整场报
// 错，此处宽容回退原文，不让一行怪值污染其余键的规划。
func parseEnvFileValues(data []byte) map[string]string {
	values := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		trimmedLine := strings.TrimSpace(line)
		if trimmedLine == "" || strings.HasPrefix(trimmedLine, "#") {
			continue
		}
		key, rawValue, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		values[key] = parseEnvFileValue(rawValue)
	}
	return values
}

// parseEnvFileValue 解析单个 .env 值（口径见 parseEnvFileValues）。
func parseEnvFileValue(raw string) string {
	v := strings.TrimSpace(raw)
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') {
		quote := v[0]
		for i := 1; i < len(v); i++ {
			if quote == '"' && v[i] == '\\' {
				i++
				continue
			}
			if v[i] == quote {
				inner := v[1:i]
				if quote == '"' {
					return expandEnvDoubleQuotedEscapes(inner)
				}
				return inner
			}
		}
		return v
	}
	for i := 0; i < len(v); i++ {
		if v[i] == '#' && (i == 0 || v[i-1] == ' ' || v[i-1] == '\t') {
			return strings.TrimSpace(v[:i])
		}
	}
	return v
}

// expandEnvDoubleQuotedEscapes 展开双引号值的反斜杠转义（与
// compose-go dotenv 同集：\n \r \t \\ \" \$；未知转义保留原文
// 两字符，不臆造语义）。
func expandEnvDoubleQuotedEscapes(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case '\\':
			b.WriteByte('\\')
		case '"':
			b.WriteByte('"')
		case '$':
			b.WriteByte('$')
		default:
			b.WriteByte('\\')
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// applyEnvPins 把 pins 钉进 .env 内容并返回新内容（纯函数）：同名
// 键的空值行就地补值（与脚本 upsert 对空键行的效果一致）、缺失键
// 追加到文件尾；已有非空值行与其余一切行（注释、顺序）逐字保留。
// 调用方（规划段）保证 pins 只含 .env 缺失或为空的键。
func applyEnvPins(data []byte, pins []envKeyValue) []byte {
	if len(pins) == 0 {
		return data
	}
	pinByKey := make(map[string]string, len(pins))
	for _, pin := range pins {
		pinByKey[pin.key] = pin.value
	}
	seen := make(map[string]bool, len(pins))
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		key, value, ok := strings.Cut(strings.TrimSuffix(line, "\r"), "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		pin, wanted := pinByKey[key]
		if !wanted {
			continue
		}
		// 键已在文件中出现即标记 seen，防追加段再补一行同名键；
		// 只有空值行就地补值，非空行逐字保留。
		seen[key] = true
		if strings.TrimSpace(value) != "" {
			continue
		}
		lines[i] = key + "=" + pin
	}
	out := strings.TrimRight(strings.Join(lines, "\n"), "\n")
	for _, pin := range pins {
		if seen[pin.key] {
			continue
		}
		if out != "" {
			out += "\n"
		}
		out += pin.key + "=" + pin.value
		seen[pin.key] = true
	}
	return []byte(out + "\n")
}

// applyEnvKeyUpdates 把 updates 逐键 upsert 进 .env 内容并返回新
// 内容（纯函数，与脚本 upsert_env_var 的 awk 语义一致）：同名键
// 的既有行全部就地改写为新值（键名按首个 = 前的原文精确匹配、
// 键侧空白容忍并归一），缺失键按 updates 序追加到文件尾；其余
// 一切行（注释、顺序、其他键值）逐字保留，输出恒以单个换行结
// 尾。与 applyEnvPins 的分工：pins 只补缺/补空、绝不覆盖非空
// 值；updates 是真改写（版本键双写、容器改名键共用）。
func applyEnvKeyUpdates(data []byte, updates []envKeyValue) []byte {
	if len(updates) == 0 {
		return data
	}
	want := make(map[string]string, len(updates))
	for _, u := range updates {
		want[u.key] = u.value
	}
	seen := make(map[string]bool, len(updates))
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		key, _, ok := strings.Cut(strings.TrimSuffix(line, "\r"), "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value, wanted := want[key]
		if !wanted {
			continue
		}
		seen[key] = true
		lines[i] = key + "=" + value
	}
	out := strings.TrimRight(strings.Join(lines, "\n"), "\n")
	for _, u := range updates {
		if seen[u.key] {
			continue
		}
		if out != "" {
			out += "\n"
		}
		out += u.key + "=" + u.value
		seen[u.key] = true
	}
	return []byte(out + "\n")
}

// resourceNameInspect 是升级前对旧容器的一次实读结果（采集段填
// 报、规划段消费；纯数据，便于单测构造）。
type resourceNameInspect struct {
	// backendName 是旧后端容器名（fail-closed 点名用）。
	backendName string
	// backendExists 为旧后端容器存在且可 inspect。
	backendExists bool
	// sqliteVolume 是旧后端 /app/data 挂载的卷名；bind 挂载、
	// 无此挂载或 inspect 失败时为空。
	sqliteVolume string
	// pgVolume 是旧 PG 容器 /var/lib/postgresql/data 的卷名；
	// PG 容器不在场或读不到时为空。
	pgVolume string
	// postgresDataVolumeExists 为历史缺省名 PG 卷是否存在。
	postgresDataVolumeExists bool
	// sqliteLegacyVolumeHasDB 为旧名卷 sqlite_data 存在且含
	// gost.db（事故态门的真库证据；探测不到按无证据）。
	sqliteLegacyVolumeHasDB bool
	// postgresLegacyVolumeHasData 为旧名卷 postgres_data 存在且
	// 含 PG_VERSION（事故态门 PG 格的真库证据；同上）。
	postgresLegacyVolumeHasData bool
	// pgContainerFound 为旧 PG 容器已定位成功：卷实读为空时据此
	// 区分「容器不在场」与「在场但挂载读空（bind 等）」，供
	// R1-01-L5 的 PG bind 中止判定。
	pgContainerFound bool
	// pgMountInspectOK 为旧 PG 容器的挂载 inspect 命令本身成功
	// 执行：与 pgContainerFound 合用，把「在场且确无命名卷挂载
	// （bind）」与「inspect 失败读不到」分开——后者不进 L5 中止
	// 格，维持键保留的既有口径。
	pgMountInspectOK bool
	// network 是旧后端所连的唯一网络名；未连或多网络时为空。
	network string
	// composeProject 是旧后端的 Compose 项目标签；读不到为空。
	composeProject string
}

// resourcePinPlan 是钉键规划结果（纯数据）。
type resourcePinPlan struct {
	// pins 是待补缺/补空的键（固定序：SQLite 卷、PG 卷、网络、
	// 项目名），经 applyEnvPins 落盘、绝不覆盖已有非空值。
	pins []envKeyValue
	// corrections 是与在跑旧容器实读冲突的两卷键改写（实读为
	// 准），经 applyEnvKeyUpdates 落盘并在日志中逐键点名（键名、
	// 旧值、新值）。仅 FLMX_SQLITE_VOLUME / FLMX_POSTGRES_VOLUME
	// 两键有此处置：.env 里的卷名只是声明，在跑容器的实际挂载
	// 才是现行事实；网络名/项目名无等价的实读真值语义，维持只
	// 补缺。
	corrections []envKeyValue
	// expectedSQLiteVolume / expectedPostgresVolume 是升级后校验
	// 用的期望数据卷名（.env 原值优先、其次实读钉值/改写值），
	// 透传 helper 在重建后实读新容器挂载比对。
	expectedSQLiteVolume   string
	expectedPostgresVolume string
}

// planLegacyResourcePins 是钉键的纯规划段：给定 .env 现值与旧容器
// 实读，产出 resourcePinPlan。已有非空键原则上逐字保留、不进
// pins；两卷键是例外——键值与在跑旧容器的实际挂载不一致时以实
// 读为准进 corrections（RA-1 L-2 / RA-2 S-01 收口：旧口径下钉键
// 与升级后校验门的期望值同源于错键，错键会被自检「验证通过」，
// 且事故态部署会被实读钉入错卷而正式化；migrate 路径在 E4 已立
// 「键读冲突以实读为准」口径，本面补齐）。实读不到（无旧容器或
// 挂载读空）时无从证伪键值，保留原样。
//
// fail-closed：.env 缺 FLMX_SQLITE_VOLUME、旧后端在场、但其实读
// 卷名为空 → 返回 errDataVolumeUndetermined 缠绕错误，调用方必须
// 在动任何文件之前中止。与脚本面的口径差异（有意为之）：脚本面
// 实读为空时回退旧缺省名 sqlite_data——其回退值恰是历史缺省卷
// 名，对标准名存量无害；但面板内升级面同样面对自定义卷名部署，
// 实读失败时回退缺省名等于把错卷钉死、重演 1.9.8 事故的变体，
// 故此处不回退、只中止。PG 卷的 postgres_data 回退与脚本同口
// 径保留：该键对 SQLite 部署惰性无害，且脚本的存量识别亦以此
// 名为锚，语义是「历史缺省卷在场则认领」而非猜测现行卷名。
// PG 卷 fail-closed（修复批 D，RA-3 §1）：PG 模式下缺 PG 卷键、
// 实读为空且 postgres_data 也不存在时同标准中止
// （errPGVolumeUndetermined）——此子形下不钉不拦，重建 PG 会落
// 模板缺省空卷播种，事故在 PG 面同构复现；SQLite 模式下 PG 卷
// 键惰性，维持不钉不拦。
// 事故态门（R1-01-M1）：两卷共同谓词——缺键＋实读恰为模板新缺
// 省卷名（带实例后缀）＋旧名卷含真实数据证据（SQLite 卷含
// gost.db / PG 卷含 PG_VERSION）→ 返回 errAccidentStateVolume
// 中止：该部署是事故后未救援形态，钉实读值会把事故态正式化。
// 实读为空时的键信任（R1-01-L5，仅 PG 格）：PG 模式、PG 卷键
// 已有值、旧 PG 容器已定位且其挂载 inspect 成功但卷名为空
// （bind 挂载）时，键值无从核对——此形态下脚本面逐字信键、重
// 建落错键空卷还报成功，本面 helper 以键为期望比对未重建的旧
// PG 容器恒回滚；统一为动文件前 fail-closed 中止并点名（脚本
// 面同谓词同处置）。缺键时维持 postgres_data 回退认领的既有
// 口径；SQLite 卷键在实读为空时逐字保留的口径亦维持既有裁定
// （TestPlanLegacyResourcePinsFailClosedOnUnreadableVolume 钉
// 住），不在本格翻动。
// 旧后端不存在（全新安装形态）时不钉卷、不报错、期望值为空。
func planLegacyResourcePins(env map[string]string, insp resourceNameInspect) (resourcePinPlan, error) {
	plan := resourcePinPlan{pins: make([]envKeyValue, 0, 4)}
	expected := env[envKeySQLiteVolume]
	switch {
	case expected == "":
		switch {
		case insp.backendExists && insp.sqliteVolume == "":
			return plan, fmt.Errorf("%w：部署 .env 缺少 %s，旧后端容器 %s 的 /app/data 挂载实读为空（可能是 bind 挂载、无此挂载或 inspect 失败）",
				errDataVolumeUndetermined, envKeySQLiteVolume, insp.backendName)
		case insp.backendExists:
			// 事故态门（R1-01-M1，脚本面同谓词）：无键＋实读恰
			// 为模板新缺省卷＋旧名卷含 gost.db → 钉键会把事故
			// 态正式化，中止并点名人工钉法。
			if insp.sqliteVolume == newSQLiteDataVolume+panelInstanceSuffix(env) && insp.sqliteLegacyVolumeHasDB {
				return plan, fmt.Errorf("%w：部署 .env 缺少 %s，在跑后端实际挂载的数据卷是模板新缺省卷 %s，但旧名卷 %s 存在且含有 gost.db——该部署可能正处在「挂新空卷、真库在旧卷」的事故形态，此时钉键会把事故态正式化。请人工确认数据所在卷后，在 .env 中显式钉入正确卷名再重试升级：数据在旧卷 → %s=%s；数据确在现行卷 → %s=%s",
					errAccidentStateVolume, envKeySQLiteVolume, insp.sqliteVolume, legacySQLiteDataVolume,
					envKeySQLiteVolume, legacySQLiteDataVolume, envKeySQLiteVolume, insp.sqliteVolume)
			}
			plan.pins = append(plan.pins, envKeyValue{envKeySQLiteVolume, insp.sqliteVolume})
			expected = insp.sqliteVolume
		}
	case insp.backendExists && insp.sqliteVolume != "" && insp.sqliteVolume != expected:
		plan.corrections = append(plan.corrections, envKeyValue{envKeySQLiteVolume, insp.sqliteVolume})
		expected = insp.sqliteVolume
	}
	plan.expectedSQLiteVolume = expected

	pgExpected := env[envKeyPostgresVolume]

	// R1-01-L5（PG bind 两面收敛）：PG 模式、PG 卷键已有值、旧
	// PG 容器已定位且其挂载 inspect 成功但卷名为空（bind 挂载）
	// ——键值无从与实读核对。此形态下脚本面旧行为是逐字信键、
	// 重建落错键命名的空卷、后端播种而更新报成功；本面旧行为是
	// helper 以错键为期望比对未重建的旧 PG 容器（实读为空）恒
	// 判不一致回滚。统一为动任何文件之前 fail-closed 中止并点
	// 名（脚本面 panel_pin_missing_resource_keys 同谓词同处
	// 置）。inspect 本身失败（读不到）不在此格：键无从证伪、按
	// 既有口径保留，升级后校验门兜底。SQLite 模式下 PG 容器惰
	// 性、数据不在 PG 卷，不拦。
	if isPostgresMode(env) && pgExpected != "" && insp.pgContainerFound && insp.pgMountInspectOK && insp.pgVolume == "" {
		return plan, fmt.Errorf("%w：部署为 PostgreSQL 模式且 .env 已有 %s=%s，但旧 PG 容器的 /var/lib/postgresql/data 挂载不是命名卷（实读卷名为空，疑似 bind 挂载），键值无从与实际挂载核对；bind 挂载不在随版模板形态内，请先把 PG 数据改为命名卷挂载或人工核对该键后再重试升级",
			errPGVolumeUndetermined, envKeyPostgresVolume, pgExpected)
	}
	switch {
	case pgExpected == "":
		// 事故态门（R1-01-M1，脚本面同谓词）：无键＋实读恰为模
		// 板新缺省卷＋旧名卷含 PG_VERSION → 中止并点名人工钉法。
		if insp.pgVolume == newPostgresDataVolume+panelInstanceSuffix(env) && insp.postgresLegacyVolumeHasData {
			return plan, fmt.Errorf("%w：部署 .env 缺少 %s，在跑 PostgreSQL 容器实际挂载的数据卷是模板新缺省卷 %s，但旧名卷 %s 存在且含有 PG_VERSION（真实 PostgreSQL 数据目录）——该部署可能正处在「挂新空卷、真库在旧卷」的事故形态，此时钉键会把事故态正式化。请人工确认数据所在卷后，在 .env 中显式钉入正确卷名再重试升级：数据在旧卷 → %s=%s；数据确在现行卷 → %s=%s",
				errAccidentStateVolume, envKeyPostgresVolume, insp.pgVolume, legacyPostgresDataVolume,
				envKeyPostgresVolume, legacyPostgresDataVolume, envKeyPostgresVolume, insp.pgVolume)
		}
		pgVolume := insp.pgVolume
		if pgVolume == "" && insp.postgresDataVolumeExists {
			pgVolume = legacyPostgresDataVolume
		}
		if pgVolume != "" {
			plan.pins = append(plan.pins, envKeyValue{envKeyPostgresVolume, pgVolume})
			pgExpected = pgVolume
		} else if isPostgresMode(env) {
			return plan, fmt.Errorf("%w：部署为 PostgreSQL 模式但 .env 缺少 %s，旧 PG 容器未能定位或其实读卷名为空，且历史缺省卷 %s 不存在",
				errPGVolumeUndetermined, envKeyPostgresVolume, legacyPostgresDataVolume)
		}
	case insp.pgVolume != "" && insp.pgVolume != pgExpected:
		plan.corrections = append(plan.corrections, envKeyValue{envKeyPostgresVolume, insp.pgVolume})
		pgExpected = insp.pgVolume
	}
	plan.expectedPostgresVolume = pgExpected

	if env[envKeyNetwork] == "" && insp.network != "" {
		plan.pins = append(plan.pins, envKeyValue{envKeyNetwork, insp.network})
	}
	if env[envKeyComposeProject] == "" && insp.composeProject != "" {
		plan.pins = append(plan.pins, envKeyValue{envKeyComposeProject, insp.composeProject})
	}
	return plan, nil
}

// upgradeDataVolumeMismatch 是 helper 数据卷校验段的判定谓词（与
// helperScript 内 shell 比对同式，由测试双向钉住）：期望值非空且
// 实际挂载卷与之不同（含实际为空）即判不一致、触发回滚。
func upgradeDataVolumeMismatch(expected, actual string) bool {
	return expected != "" && actual != expected
}

// dockerInspectMountVolumeName 实读容器在 destination 处的挂载卷
// 名（与脚本面 panel_pin_missing_resource_keys 的 inspect 模板同
// 式）：bind 挂载该字段为空、inspect 失败返回空串。
func (e *systemUpgradeExecutor) dockerInspectMountVolumeName(ctx context.Context, container, destination string) string {
	name, _ := e.dockerInspectMountVolumeNameOK(ctx, container, destination)
	return name
}

// dockerInspectMountVolumeNameOK 同上，但额外报告 inspect 命令本
// 身是否成功执行：ok=false 表示读不到（容器不在场/inspect 失
// 败），ok=true 且名为空表示容器在场但该 destination 确无命名
// 卷挂载（bind 等）——R1-01-L5 的 bind 中止格只认后者。
func (e *systemUpgradeExecutor) dockerInspectMountVolumeNameOK(ctx context.Context, container, destination string) (string, bool) {
	tmpl := fmt.Sprintf(`{{range .Mounts}}{{if eq .Destination %q}}{{.Name}}{{end}}{{end}}`, destination)
	out, err := exec.CommandContext(ctx, "docker", "inspect", "-f", tmpl, container).Output()
	if err != nil {
		return "", false
	}
	name := strings.TrimSpace(string(out))
	if !dockerResourceNamePattern.MatchString(name) {
		return "", true
	}
	return name, true
}

// dockerContainerExists 报告容器是否存在且可 inspect。
func (e *systemUpgradeExecutor) dockerContainerExists(ctx context.Context, container string) bool {
	if validateBackendContainerName(container) != nil {
		return false
	}
	return exec.CommandContext(ctx, "docker", "inspect", "-f", "{{.Id}}", container).Run() == nil
}

// inspectLegacyResourceNames 采集规划段所需的旧容器实读（本文件既
// 有 docker 执行通道：exec.CommandContext + inspect 模板，与
// detectHostDeployDir/detectComposeProject 同形态）。任一单项读
// 不到只影响该项钉键，唯独 SQLite 卷在规划段 fail-closed。
func (e *systemUpgradeExecutor) inspectLegacyResourceNames(ctx context.Context, env map[string]string) resourceNameInspect {
	insp := resourceNameInspect{backendName: e.backendContainer}
	if !e.dockerContainerExists(ctx, e.backendContainer) {
		return insp
	}
	insp.backendExists = true
	insp.sqliteVolume = e.dockerInspectMountVolumeName(ctx, e.backendContainer, "/app/data")
	insp.composeProject = e.detectComposeProject(ctx)

	// 旧后端所连网络：恰一个时钉它；多网络时实读串无法无歧义归
	// 一，跳过该键（脚本面同模板会把多网络名拼接成废串，此处不
	// 沿袭该形态）。
	networksOut, err := exec.CommandContext(ctx, "docker", "inspect", "-f",
		`{{range $k, $v := .NetworkSettings.Networks}}{{$k}}{{"\n"}}{{end}}`, e.backendContainer).Output()
	if err == nil {
		names := make([]string, 0, 2)
		for _, line := range strings.Split(string(networksOut), "\n") {
			if name := strings.TrimSpace(line); name != "" {
				names = append(names, name)
			}
		}
		if len(names) == 1 && dockerResourceNamePattern.MatchString(names[0]) {
			insp.network = names[0]
		}
	}

	// 旧 PG 容器定位与卷实读（定位口径见 locatePostgresContainer）。
	if pgContainer := e.locatePostgresContainer(ctx, env, insp.composeProject); pgContainer != "" {
		insp.pgContainerFound = true
		insp.pgVolume, insp.pgMountInspectOK = e.dockerInspectMountVolumeNameOK(ctx, pgContainer, "/var/lib/postgresql/data")
	}
	if insp.pgVolume == "" {
		insp.postgresDataVolumeExists =
			exec.CommandContext(ctx, "docker", "volume", "inspect", legacyPostgresDataVolume).Run() == nil
	}

	// 事故态门的真库证据探测：仅在门的前件（缺键＋实读恰为模板
	// 新缺省名）成立时才花一次性探针容器，常态升级零额外开销。
	suffix := panelInstanceSuffix(env)
	if env[envKeySQLiteVolume] == "" && insp.backendExists && insp.sqliteVolume == newSQLiteDataVolume+suffix {
		insp.sqliteLegacyVolumeHasDB = e.dockerVolumeFileExists(ctx, legacySQLiteDataVolume, "gost.db")
	}
	if env[envKeyPostgresVolume] == "" && insp.pgVolume == newPostgresDataVolume+suffix {
		insp.postgresLegacyVolumeHasData = e.dockerVolumeFileExists(ctx, legacyPostgresDataVolume, "PG_VERSION")
	}
	return insp
}

// dockerVolumeFileExists 探测命名卷内是否存在指定文件（事故态门
// 的真库证据，与脚本面 panel_volume_file_exists 同语义：探测不
// 到一律按「无证据」返回 false，不臆断卷内有数据）。面板进程自
// 身没有宿主机卷挂载点视图，故借后端镜像起一次性只读探针容
// 器挂卷查看；卷不存在、镜像读不到或探针运行失败均返回 false
// ——探针是证据通道而非裁决面，其失败只使门退回未立门时的行
// 为，绝不反向误拦正常升级。relPath 只由内部常量传入
// （gost.db / PG_VERSION），不接受外部输入。
func (e *systemUpgradeExecutor) dockerVolumeFileExists(ctx context.Context, volume, relPath string) bool {
	if exec.CommandContext(ctx, "docker", "volume", "inspect", volume).Run() != nil {
		return false
	}
	imageID, err := e.currentBackendImage(ctx)
	if err != nil || imageID == "" {
		return false
	}
	return exec.CommandContext(ctx, "docker", "run", "--rm", "--network", "none",
		"-v", volume+":/flmx-volume-probe:ro",
		"--entrypoint", "/bin/sh", imageID,
		"-c", "test -f /flmx-volume-probe/"+relPath).Run() == nil
}

// pinLegacyResourceNames 在升级动任何文件之前执行钉键：读部署
// .env → 实读旧容器 → 纯规划 → 有钉键/改写才原子改写 .env（mode
// 帽与 updateEnvVersion 同口径；钉入走 applyEnvPins 只补缺、卷键
// 冲突改写走 applyEnvKeyUpdates，两段合成同一份内容一次原子
// 写）。返回升级后校验用的期望数据卷名（SQLite 恒返回；PG 仅 PG
// 模式返回，SQLite 模式下 PG 卷不被本次升级触碰、不下发校验）。
// fail-closed 时 .env 一字未动，调用方直接中止升级。
func (e *systemUpgradeExecutor) pinLegacyResourceNames(ctx context.Context) (string, string, error) {
	data, err := os.ReadFile(e.envPath())
	if err != nil {
		return "", "", fmt.Errorf("读取部署 .env 失败: %v", err)
	}
	env := parseEnvFileValues(data)
	insp := e.inspectLegacyResourceNames(ctx, env)
	plan, err := planLegacyResourcePins(env, insp)
	if err != nil {
		return "", "", err
	}
	expectedPG := plan.expectedPostgresVolume
	if !isPostgresMode(env) {
		expectedPG = ""
	}
	if len(plan.pins) == 0 && len(plan.corrections) == 0 {
		return plan.expectedSQLiteVolume, expectedPG, nil
	}
	mode, err := fileModeOrDefault(e.envPath(), 0o600)
	if err != nil {
		return "", "", err
	}
	content := applyEnvPins(data, plan.pins)
	content = applyEnvKeyUpdates(content, plan.corrections)
	if err := writeFileAtomicWithMode(e.envPath(), content, capEnvFileMode(mode)); err != nil {
		return "", "", fmt.Errorf("钉入存量资源名失败: %v", err)
	}
	if len(plan.pins) > 0 {
		pairs := make([]string, 0, len(plan.pins))
		for _, pin := range plan.pins {
			pairs = append(pairs, pin.key+"="+pin.value)
		}
		log.Printf("systemUpgrade: 已将存量资源名钉入 .env（升级前实读旧容器）: %s", strings.Join(pairs, ", "))
	}
	for _, correction := range plan.corrections {
		log.Printf("systemUpgrade: 警告: .env 的 %s=%s 与在跑旧容器的实际挂载卷名不一致，已按实读改写为 %s（卷键以实读为准）",
			correction.key, env[correction.key], correction.value)
	}
	return plan.expectedSQLiteVolume, expectedPG, nil
}

// locatePostgresContainer 定位本部署的旧 PG 容器名（钉键采集与
// 容器改名共用）：优先部署 .env 自带的 PANEL_POSTGRES_CONTAINER
// 声明，但声明只作线索、不无条件信任（RA-1 L-1）：声明名容器须
// 真实存在，且其实读 Compose 项目标签与本部署项目名（由后端容
// 器标签锚定）不得可证地不一致——声明陈旧且其名被同机另一部署
// 的 PG 容器复用时，项目标签必然对不上，此时声明作废。声明名
// 不存在或被作废，一律回落到按 Compose 项目 + service=postgres
// 标签在本项目内查找（残差③硬化：旧实现声明名不存在即直接判
// 定位失败，孤儿 PG 容器自此永远够不到）。两路皆空返回空串。
func (e *systemUpgradeExecutor) locatePostgresContainer(ctx context.Context, env map[string]string, composeProject string) string {
	declared := strings.TrimSpace(env[envKeyPanelPostgresContainer])
	if validateBackendContainerName(declared) != nil {
		declared = ""
	}
	if declared != "" && e.dockerContainerExists(ctx, declared) &&
		composeProjectMatches(e.dockerInspectComposeProject(ctx, declared), composeProject) {
		return declared
	}
	return e.findPostgresContainerByProjectLabel(ctx, composeProject)
}

// composeProjectMatches 判容器项目标签是否可证地不属于本部署：
// 任一侧读不到项目名时无从证伪，按相符处理（常态下部署项目名
// 由后端容器标签读得、恒非空）；两侧俱在且不同才判不符。
func composeProjectMatches(containerProject, deploymentProject string) bool {
	if containerProject == "" || deploymentProject == "" {
		return true
	}
	return containerProject == deploymentProject
}

// dockerInspectComposeProject 实读任意容器的 Compose 项目标签
// （detectComposeProject 的泛化，模板/形态口径一致）：读不到或
// 不合形态返回空串。
func (e *systemUpgradeExecutor) dockerInspectComposeProject(ctx context.Context, container string) string {
	if validateBackendContainerName(container) != nil {
		return ""
	}
	out, err := exec.CommandContext(ctx, "docker", "inspect", "-f",
		`{{index .Config.Labels "com.docker.compose.project"}}`, container).Output()
	if err != nil {
		return ""
	}
	project := strings.TrimSpace(string(out))
	if !composeProjectPattern.MatchString(project) {
		return ""
	}
	return project
}

// findPostgresContainerByProjectLabel 按 Compose 项目 +
// service=postgres 标签在本项目内查找 PG 容器：首个形态合法且
// 真实存在的名字定格（旧实现在首个非空行即停，首行不合形态便
// 整体放弃，RA-1 I-5 一并收口）。
func (e *systemUpgradeExecutor) findPostgresContainerByProjectLabel(ctx context.Context, composeProject string) string {
	if composeProject == "" {
		return ""
	}
	out, err := exec.CommandContext(ctx, "docker", "ps", "-a",
		"--filter", "label=com.docker.compose.project="+composeProject,
		"--filter", "label=com.docker.compose.service=postgres",
		"--format", "{{.Names}}").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if name := strings.TrimSpace(line); name != "" && validateBackendContainerName(name) == nil &&
			e.dockerContainerExists(ctx, name) {
			return name
		}
	}
	return ""
}

// —— 存量容器改名（1.9.9，补齐面板内升级面与脚本面
// apply_panel_identity_rename 的对齐，parity 矩阵 D1）——
//
// 基线形态：Go 面无改名逻辑，.env 容器键为旧缺省名的存量经面板
// 内升级后键与容器名永不改名，与改名裁定「存量部署在升级流内
// 默认执行容器改名（PANEL_IDENTITY_RENAME=0 可退出）」未齐。本
// 段在钉卷之后、备份 .env 之后、换 compose 之前把键改写到位：
// 后端/前端容器由 helper 按新 compose 的 container_name 随
// force-recreate 换名（compose 按服务标签认容器，改名是重建的
// 自然结果，无需显式停删）；PG 容器不在 helper 缺省重建集内，
// 其处置按 DB 模式分流，见 applyIdentityRename 注。
const (
	envKeyPanelBackendContainer  = "PANEL_BACKEND_CONTAINER"
	envKeyPanelFrontendContainer = "PANEL_FRONTEND_CONTAINER"
	envKeyIdentityRename         = "PANEL_IDENTITY_RENAME"
	envKeyInstance               = "FLMX_INSTANCE"
	envKeyDBType                 = "DB_TYPE"

	// 旧/新缺省容器名与脚本面 LEGACY_/NEW_PANEL_*_CONTAINER 常
	// 量逐字同值（前端旧名是 vite-frontend，非 flux- 谱系）。
	legacyPanelBackendContainer  = "flux-panel-backend"
	legacyPanelPostgresContainer = "flux-panel-postgres"
	legacyPanelFrontendContainer = "vite-frontend"
	newPanelBackendContainer     = "flmx-panel-backend"
	newPanelPostgresContainer    = "flmx-panel-postgres"
	newPanelFrontendContainer    = "flmx-panel-frontend"

	// 透传 helper 的改名/快照环境键（仅非缺省时透传，helper 侧
	// 缺失即按缺省行为，与 expectedSQLiteVolumeEnvKey 同形态）。
	removePGContainerEnvKey  = "FLMX_UPGRADE_REMOVE_PG_CONTAINER"
	upgradeServicesEnvKey    = "FLMX_UPGRADE_SERVICES"
	snapshotDirEnvKey        = "FLMX_UPGRADE_SNAPSHOT_DIR"
	pgSnapshotDeferredEnvKey = "FLMX_UPGRADE_PG_SNAPSHOT_DEFERRED"
	// upgradeDataDirEnvKey 仅供测试覆盖快照源目录；生产恒不设、
	// helper 缺省 /app/data。
	upgradeDataDirEnvKey = "FLMX_UPGRADE_DATA_DIR"
)

// panelInstanceIDPattern 是实例 id 白名单（与脚本
// panel_instance_id_is_valid 逐字同契约：小写字母或数字开头、
// 仅含小写字母/数字/-/_、全长不超过 32）。
var panelInstanceIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// panelInstanceSuffix 派生容器/卷名派生的实例后缀（与脚本
// panel_instance_suffix 同源）：实例 id 取进程环境 FLMX_INSTANCE
// 优先、部署 .env 次之（脚本 panel_instance_id 同序），过白名单
// 后拼为 "-<id>"；缺失或不合法时按默认实例处理、返回空串。
func panelInstanceSuffix(env map[string]string) string {
	id := strings.TrimSpace(os.Getenv(envKeyInstance))
	if id == "" {
		id = strings.TrimSpace(env[envKeyInstance])
	}
	if id == "" || !panelInstanceIDPattern.MatchString(id) {
		return ""
	}
	return "-" + id
}

// identityRenameEnabled 读改名开关：部署 .env 的
// PANEL_IDENTITY_RENAME 优先、进程环境兜底；仅值 "0" 关闭，其
// 余（含缺失与空值）开启。取值来源与判定口径与脚本面
// panel_identity_rename_enabled 逐字同序（批 D 已对齐：脚本面同
// 读 .env、键在即赢、缺键才回退进程环境）。
func identityRenameEnabled(env map[string]string) bool {
	flag, ok := env[envKeyIdentityRename]
	if !ok {
		flag = os.Getenv(envKeyIdentityRename)
	}
	return flag != "0"
}

// isPostgresMode 按部署 .env 的 DB_TYPE 判部署模式：走 config 的
// 单点归一（R1-01-M2），别名 postgresql 与 postgres 等价。旧实现
// 只认 postgres 字面量——后端与脚本都接受别名，唯独执行器不认，
// 该拼写的 PG 部署在此整格失去 PG 保护（helper PG 卷校验永不启
// 用、PG fail-closed 不触发、改名误置 removeLegacyPGContainer 删
// 在跑 PG 容器、快照按 SQLite 安排）。
func isPostgresMode(env map[string]string) bool {
	return config.DBTypeIsPostgres(env[envKeyDBType])
}

// identityRenameOutcome 是改名规划结果（纯数据）。
type identityRenameOutcome struct {
	// writes 是待 upsert 的容器键（固定序：后端、前端、PG）。
	writes []envKeyValue
	// pgRenamed 表示 PG 键被改名，helper 侧须处置旧 PG 容器。
	pgRenamed bool
	// oldPGContainer 是改名前 PG 容器的实际名（定位到才非空）。
	oldPGContainer string
}

// planIdentityRename 是改名的纯规划段（触发谓词——每键何时改
// 写——与脚本 apply_panel_identity_rename 逐字同口径；此同口径
// 仅限谓词，不含改名开关的取值来源，见 identityRenameEnabled
// 的 L-3 注）：每个容器键在「缺失或为空」或「等于旧缺省名」时
// 改写为现行名＋实例后缀；自定义名与已是现行名的键逐字保留、
// 不进 writes。
func planIdentityRename(env map[string]string, suffix, oldPGContainer string) identityRenameOutcome {
	out := identityRenameOutcome{}
	rules := []struct{ key, legacy, current string }{
		{envKeyPanelBackendContainer, legacyPanelBackendContainer, newPanelBackendContainer + suffix},
		{envKeyPanelFrontendContainer, legacyPanelFrontendContainer, newPanelFrontendContainer + suffix},
		{envKeyPanelPostgresContainer, legacyPanelPostgresContainer, newPanelPostgresContainer + suffix},
	}
	for _, r := range rules {
		if v := env[r.key]; v != "" && v != r.legacy {
			continue
		}
		out.writes = append(out.writes, envKeyValue{r.key, r.current})
		if r.key == envKeyPanelPostgresContainer {
			out.pgRenamed = true
			out.oldPGContainer = oldPGContainer
		}
	}
	return out
}

// applyIdentityRename 执行存量容器改名的 .env 键改写，并定格
// helper 侧的 PG 处置（recreatePostgres / removeLegacyPGContainer）。
// 调用点在 handler 同步段：钉卷之后、.env 备份之后、换 compose
// 之前。排在备份之后是有意的：helper 回滚恢复的 .env.upgrade.bak
// 含改名前的身份键，改名后健康/卷名校验失败时 .env 键随文件一
// 并回到旧值、旧名容器按旧定义重建（钉卷键在备份之前写入，不
// 在回滚之列——钉入的都是存量资源的现行事实，回滚保留无害且
// 必要）。
//
// PG 容器处置的模式分流（选与脚本面最终状态一致者）：
//   - PG 模式取「重建」：脚本面 start_panel_after_update 在 PG
//     型部署先 up postgres 再重建后端/前端，改名后的 PG 容器随
//     升级按新名重建。Go 面置 recreatePostgres，helper 的 pull/up
//     服务集含 postgres，达同一终态。
//   - SQLite 模式取 (a)「改键＋删旧容器」：脚本面改名流在
//     SQLite 模式下停删旧 PG 容器且不重建（RR4-F7-02 已注记：
//     无流程依赖，菜单 4 迁移按需拉起），终态为「键是新名、旧
//     PG 容器不在场」。若只改键不删容器（备选 (b) 的反面形态），
//     旧 PG 容器以旧名长存、与 .env 声明分家，且与脚本面终态不
//     一致；若连键也不改 (b)，则 D1 在 SQLite 模式留一格不对
//     齐。故置 removeLegacyPGContainer，helper 在健康＋卷名校
//     验全过后、写 success 前删除之（删除点晚于脚本面，是有意
//     偏差：此前任一步失败走回滚时，旧容器与恢复后的旧键仍成
//     对在场，不留半改名态；成功终态与脚本面一致）。旧 PG 容
//     器定位不到（无从证明其存在）时只改键并记录日志。
func (e *systemUpgradeExecutor) applyIdentityRename(ctx context.Context) error {
	data, err := os.ReadFile(e.envPath())
	if err != nil {
		return fmt.Errorf("读取部署 .env 失败: %v", err)
	}
	env := parseEnvFileValues(data)
	if !identityRenameEnabled(env) {
		return nil
	}
	suffix := panelInstanceSuffix(env)
	oldPG := ""
	if v := env[envKeyPanelPostgresContainer]; v == "" || v == legacyPanelPostgresContainer {
		oldPG = e.locatePostgresContainer(ctx, env, e.detectComposeProject(ctx))
	}
	outcome := planIdentityRename(env, suffix, oldPG)
	if len(outcome.writes) == 0 {
		return nil
	}
	mode, err := fileModeOrDefault(e.envPath(), 0o600)
	if err != nil {
		return err
	}
	if err := writeFileAtomicWithMode(e.envPath(), applyEnvKeyUpdates(data, outcome.writes), capEnvFileMode(mode)); err != nil {
		return fmt.Errorf("写入改名后的容器身份键失败: %v", err)
	}
	pairs := make([]string, 0, len(outcome.writes))
	for _, w := range outcome.writes {
		pairs = append(pairs, w.key+"="+w.value)
	}
	log.Printf("systemUpgrade: 存量容器改名键已写入 .env: %s", strings.Join(pairs, ", "))
	if outcome.pgRenamed {
		switch {
		case isPostgresMode(env):
			e.recreatePostgres = true
		case outcome.oldPGContainer != "":
			e.removeLegacyPGContainer = outcome.oldPGContainer
		default:
			log.Printf("systemUpgrade: PG 容器键已改名，但旧 PG 容器未能定位（SQLite 模式），如其仍在运行请手动删除旧名容器")
		}
	}
	return nil
}

// prepareUpgradeSnapshot 在启动 helper 前定格本次升级的 DB 快照
// 安排（仅 SQLite 模式，路 3 INV-7 对 Go 面的补齐）：快照目录
// 名由目标版本与当前 UTC 时刻派生（backups/panel-update-<版本>-
// <时间戳>，与脚本面 create_panel_update_backup 的
// panel-update-<时间戳> 同族命名；K=3 按 mtime 轮换亦同口径，
// 由 helper 落盘时执行），经环境变量透传 helper 在停旧后端后、
// 重建前整目录复制数据卷；状态文件与发起回执以相对路径点名。
// 快照只备不自动恢复（恢复由人工按点名路径执行，与脚本面回滚
// 不恢复 DB 备份的口径一致）。
//
// PG 模式切出登记（本批不落，只落 SQLite 子项）：脚本面 PG 快
// 照是宿主机上对在跑 PG 容器直接 pg_dump；面板内 helper 若同
// 做，须在 shell 内再解析 PG 容器定位与 POSTGRES_* 凭据/
// DATABASE_URL 形态并处理转储一致性，工具链与失败面显著大于
// SQLite 的整目录复制；且 PG 模式下数据卷不被本次升级写
// （helper 至多重建 PG 容器；卷名由钉键保障——PG 模式无键且
// 实读不到卷名时升级前 fail-closed 中止、重建后 helper 实读
// PG 卷挂载比对，见 planLegacyResourcePins 与 helper 的 PG 数
// 据卷校验段），风险敞口与 SQLite 面不对等。故 PG 子项以
// pgSnapshotDeferred 标记＋helper 日志点名切出，待后续批次按
// 同一 backups/ 口径补齐。
func (e *systemUpgradeExecutor) prepareUpgradeSnapshot(version string) {
	data, err := os.ReadFile(e.envPath())
	if err != nil {
		// .env 此刻已由前序步骤反复读写成功，读失败极罕见；按
		// SQLite 缺省处理（config 的 DB_TYPE 缺省同为 sqlite）：
		// 安排照常下发，快照真失败时在 helper 内以回滚终态暴
		// 露，不会静默跳过。
		data = nil
	}
	if isPostgresMode(parseEnvFileValues(data)) {
		e.pgSnapshotDeferred = true
		return
	}
	name := "panel-update-" + version + "-" + time.Now().UTC().Format("20060102-150405")
	e.snapshotPath = "backups/" + name
}

func (e *systemUpgradeExecutor) backupFile(path string) (string, error) {
	mode, err := fileModeOrDefault(path, 0o600)
	if err != nil {
		return "", err
	}
	mode = capEnvFileMode(mode)
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	backupPath := path + ".upgrade.bak"
	if err := writeFileAtomicWithMode(backupPath, data, mode); err != nil {
		return "", err
	}
	return backupPath, nil
}

func (e *systemUpgradeExecutor) restoreBackup(path string) error {
	backupPath := path + ".upgrade.bak"
	mode, err := fileModeOrDefault(backupPath, 0o600)
	if err != nil {
		return err
	}
	mode = capEnvFileMode(mode)
	data, err := os.ReadFile(backupPath)
	if err != nil {
		return err
	}
	return writeFileAtomicWithMode(path, data, mode)
}

func (e *systemUpgradeExecutor) restoreUpgradeBackups(paths ...string) error {
	var errs []string
	for _, path := range paths {
		if err := e.restoreBackup(path); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", path, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// expectedComposeServices 是面板自升级 compose 资产必须包含的
// 核心服务名（随版发布的 docker-compose-v4/v6.yml 两模板共有：
// backend + frontend；postgres 两模板亦有，但属可替换的数据面，
// 不作硬性要求，避免模板将来去掉内置库时误杀合法资产）。
var expectedComposeServices = []string{"backend", "frontend"}

// validateComposeAsset 是 replaceCompose 的落盘前内容校验段
// （L8-F-06 之外的 L8-F-02 修复，G3b 负责本段）：下载来的 compose
// 此前除网络段保留外整份原文落盘、零内容校验——同源锚一旦被绕
// 过（或第二锚降级时单锚失陷），任意内容都会被 helper 以宿主
// docker 权限执行。这里只做最小结构校验：YAML 可解析、根为
// mapping、含 services 且包含预期服务名；不合即中止、旧 compose
// 保持不动。深层语义（镜像钉死等）不在本门范围，资产完整性由
// 下载段的双锚校验和负责。
func validateComposeAsset(data []byte) error {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("compose 资产不是合法 YAML: %v", err)
	}
	root := mappingRoot(&doc)
	if root == nil {
		return fmt.Errorf("compose 资产结构非法：根节点不是 mapping")
	}
	services := yamlMappingChild(root, "services")
	if services == nil || services.Kind != yaml.MappingNode || len(services.Content) == 0 {
		return fmt.Errorf("compose 资产缺少 services 定义")
	}
	for _, name := range expectedComposeServices {
		if yamlMappingChild(services, name) == nil {
			return fmt.Errorf("compose 资产缺少预期服务 %q", name)
		}
	}
	return nil
}

func (e *systemUpgradeExecutor) replaceCompose(path string, data []byte) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return fmt.Errorf("compose asset is empty")
	}
	// L8-F-02 内容校验段（G3b）：落盘前先过最小结构校验，不合即
	// 中止保旧 compose；校验对象是下载原文，网络段保留合并发生
	// 在其后（preserveComposeNetworks 只动 networks 段）。
	if err := validateComposeAsset(data); err != nil {
		return err
	}
	mode, err := fileModeOrDefault(path, 0o644)
	if err != nil {
		return err
	}
	// FIX2-C #1（V6-01）：整份覆写前把存量 compose 的网络名与子网
	// 搬进候选（见 preserveComposeNetworks）。存量文件读不到（首装
	// 等）时按候选原样写。
	// R6-L8-F-01：同一覆写点再保 FLMX_TRUSTED_PROXY_HOPS 传递行
	// （见 ensureComposeTrustedProxyHops）——存量显式值优先搬入，
	// 两侧皆无则再生模板标准行，防自升级把跳数配置冲回缺失。
	if existing, readErr := os.ReadFile(path); readErr == nil {
		data = preserveComposeNetworks(existing, data)
		data = ensureComposeTrustedProxyHops(existing, data)
	} else {
		data = ensureComposeTrustedProxyHops(nil, data)
	}
	return writeFileAtomicWithMode(path, data, mode)
}

// preserveComposeNetworks 把存量 compose 的网络设置（networks.<name>
// 的 name 与 ipam.config 整体）搬进待写入的新 compose，返回结果。
//
// 背景（V6-01）：FIX-G 把模板子网由 172.80 改为 172.30 只应作用于
// 全新安装；升级整份覆写存量 compose 会连在用子网一起换掉，等于
// 升级即重建面板网络（容器 IP 全变、后端可信代理判定随子网是否
// RFC1918 静默翻转）。规则：候选与存量同名的网络，以存量为准——
// 有 ipam.config 就整段搬（含 gateway 等附带项）；存量网络没有
// ipam.config（docker 自动分配子网）则删掉候选的 config，不强加
// 模板子网。存量无 networks 段、任一侧解析失败、或候选无 networks
// 段时，候选原样返回（不比现状更糟）。
func preserveComposeNetworks(existing, candidate []byte) []byte {
	var existingDoc, candidateDoc yaml.Node
	if err := yaml.Unmarshal(existing, &existingDoc); err != nil {
		return candidate
	}
	if err := yaml.Unmarshal(candidate, &candidateDoc); err != nil {
		return candidate
	}
	existingNetworks := yamlMappingChild(mappingRoot(&existingDoc), "networks")
	candidateNetworks := yamlMappingChild(mappingRoot(&candidateDoc), "networks")
	if existingNetworks == nil || candidateNetworks == nil {
		return candidate
	}

	changed := false
	for i := 0; i+1 < len(existingNetworks.Content); i += 2 {
		name := existingNetworks.Content[i].Value
		existingNet := existingNetworks.Content[i+1]
		candidateNet := yamlMappingChild(candidateNetworks, name)
		if candidateNet == nil || candidateNet.Kind != yaml.MappingNode {
			continue
		}
		if existingNet.Kind != yaml.MappingNode {
			continue
		}
		if nameNode := yamlMappingChild(existingNet, "name"); nameNode != nil && nameNode.Value != "" {
			setYAMLMappingValue(candidateNet, "name", nameNode)
			changed = true
		}
		existingIPAM := yamlMappingChild(existingNet, "ipam")
		existingConfig := (*yaml.Node)(nil)
		if existingIPAM != nil {
			existingConfig = yamlMappingChild(existingIPAM, "config")
		}
		candidateIPAM := yamlMappingChild(candidateNet, "ipam")
		switch {
		case existingConfig != nil:
			if candidateIPAM == nil {
				candidateIPAM = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
				setYAMLMappingValue(candidateNet, "ipam", candidateIPAM)
			}
			setYAMLMappingValue(candidateIPAM, "config", cloneYAMLNode(existingConfig))
			changed = true
		case candidateIPAM != nil && yamlMappingChild(candidateIPAM, "config") != nil:
			// 存量无显式子网：删候选 config，维持自动分配语义。
			removeYAMLMappingKey(candidateIPAM, "config")
			changed = true
		}
	}
	if !changed {
		return candidate
	}
	out, err := yaml.Marshal(&candidateDoc)
	if err != nil {
		return candidate
	}
	return out
}

// trustedProxyHopsEnvKey / trustedProxyHopsComposeLine 是
// R6-L8-F-01 的标准传递行，与随版 compose 模板（docker-compose-
// v4/v6.yml）逐字一致：默认拓扑（Caddy → 前端 nginx → 后端）两跳
// 即 2，部署可在 .env 以同名变量覆盖。
const trustedProxyHopsEnvKey = "FLMX_TRUSTED_PROXY_HOPS"
const trustedProxyHopsComposeValue = "${FLMX_TRUSTED_PROXY_HOPS:-2}"

// ensureComposeTrustedProxyHops 保证待写入的新 compose 的 backend
// environment 带 FLMX_TRUSTED_PROXY_HOPS（R6-L8-F-01）。
//
// 背景：该变量缺失时后端按代码默认 1 跳解析 XFF，而脚本默认拓扑
// 恒为两跳——登录限流键收敛到前端网关条目、全站共用一桶，未认证
// 者可持续锁死全站登录。自升级整份覆写 compose 会把部署手工补的
// 传递行冲掉，故在此与 preserveComposeNetworks 同点兜底：
//   - 候选已带该键（新版模板）→ 原样返回（含字节不变）；
//   - 候选缺、存量带显式值 → 把存量的条目整条搬入（值优先于默认）；
//   - 两侧皆缺 → 注入模板标准行 ${FLMX_TRUSTED_PROXY_HOPS:-2}。
//
// environment 的映射与列表（- KEY=value）两种写法都处理；任一侧
// 解析失败、或候选无 services.backend.environment 结构时，候选
// 原样返回（不比现状更糟）。
func ensureComposeTrustedProxyHops(existing, candidate []byte) []byte {
	var candidateDoc yaml.Node
	if err := yaml.Unmarshal(candidate, &candidateDoc); err != nil {
		return candidate
	}
	candidateEnv := composeBackendEnvironment(&candidateDoc)
	if candidateEnv == nil {
		return candidate
	}
	if composeEnvHasKey(candidateEnv, trustedProxyHopsEnvKey) {
		return candidate
	}

	// 候选缺键：优先搬存量的显式条目（映射值节点或列表条目原文）。
	var existingValue *yaml.Node
	var existingItem string
	if len(existing) > 0 {
		var existingDoc yaml.Node
		if err := yaml.Unmarshal(existing, &existingDoc); err == nil {
			if existingEnv := composeBackendEnvironment(&existingDoc); existingEnv != nil {
				existingValue = yamlMappingChild(existingEnv, trustedProxyHopsEnvKey)
				existingItem = composeEnvListItem(existingEnv, trustedProxyHopsEnvKey)
			}
		}
	}

	switch candidateEnv.Kind {
	case yaml.MappingNode:
		value := existingValue
		if value == nil {
			if existingItem != "" {
				// 存量是列表写法 KEY=value：取 = 右侧作映射值。
				if _, v, ok := strings.Cut(existingItem, "="); ok {
					value = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}
				}
			}
		}
		if value == nil {
			value = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: trustedProxyHopsComposeValue}
		} else {
			value = cloneYAMLNode(value)
		}
		setYAMLMappingValue(candidateEnv, trustedProxyHopsEnvKey, value)
	case yaml.SequenceNode:
		item := existingItem
		if item == "" && existingValue != nil {
			item = trustedProxyHopsEnvKey + "=" + existingValue.Value
		}
		if item == "" {
			item = trustedProxyHopsEnvKey + "=" + trustedProxyHopsComposeValue
		}
		candidateEnv.Content = append(candidateEnv.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: item})
	default:
		return candidate
	}
	out, err := yaml.Marshal(&candidateDoc)
	if err != nil {
		return candidate
	}
	return out
}

// composeBackendEnvironment 定位 services.backend.environment 节点
// （无此结构返回 nil）。
func composeBackendEnvironment(doc *yaml.Node) *yaml.Node {
	services := yamlMappingChild(mappingRoot(doc), "services")
	backend := yamlMappingChild(services, "backend")
	return yamlMappingChild(backend, "environment")
}

// composeEnvHasKey 报告 environment（映射或列表写法）是否已设 key。
func composeEnvHasKey(env *yaml.Node, key string) bool {
	switch env.Kind {
	case yaml.MappingNode:
		return yamlMappingChild(env, key) != nil
	case yaml.SequenceNode:
		return composeEnvListItem(env, key) != ""
	}
	return false
}

// composeEnvListItem 在列表写法 environment 中取 key 的整条条目
// （"KEY=value" 原文；无则 ""）。
func composeEnvListItem(env *yaml.Node, key string) string {
	if env == nil || env.Kind != yaml.SequenceNode {
		return ""
	}
	for _, item := range env.Content {
		if item.Kind != yaml.ScalarNode {
			continue
		}
		if k, _, ok := strings.Cut(item.Value, "="); ok && k == key {
			return item.Value
		}
	}
	return ""
}

// mappingRoot 返回 YAML 文档的根 mapping（非 mapping 时 nil）。
func mappingRoot(doc *yaml.Node) *yaml.Node {
	if doc == nil || len(doc.Content) == 0 {
		return nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil
	}
	return root
}

// yamlMappingChild 在 mapping 节点中按 key 取值节点（无则 nil）。
func yamlMappingChild(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

// setYAMLMappingValue 设置 mapping 的 key（存在则替换值节点，不存在
// 则追加）。value 节点会被直接挂上（调用方需传克隆或新节点）。
func setYAMLMappingValue(mapping *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content[i+1] = value
			return
		}
	}
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
}

func removeYAMLMappingKey(mapping *yaml.Node, key string) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content = append(mapping.Content[:i], mapping.Content[i+2:]...)
			return
		}
	}
}

func cloneYAMLNode(n *yaml.Node) *yaml.Node {
	if n == nil {
		return nil
	}
	c := *n
	if n.Content != nil {
		c.Content = make([]*yaml.Node, len(n.Content))
		for i, child := range n.Content {
			c.Content[i] = cloneYAMLNode(child)
		}
	}
	if n.Alias != nil {
		c.Alias = cloneYAMLNode(n.Alias)
	}
	return &c
}

// systemUpgradeDownloadURLs 是面板自升级资产的候选 URL 全表
// （1.7.7 KU5）：按面板侧测速直选的当前路线序（gitHubRouteOrder：
// 直连判活居首、代理排名殿后、直连判死殿后兜底）对同一资产拼全
// 候选，downloadReleaseAsset 逐个尝试。原 buildSystemUpgrade
// DownloadURL 只产出面板配置代理（或直连）一条 URL——设置移除后
// 不再有单点配置，F-139 的「代理坏了无路可走」由全候选根除。
func (h *Handler) systemUpgradeDownloadURLs(version, filename string) []string {
	base := fmt.Sprintf("%s/%s/releases/download/%s/%s", strings.TrimRight(systemUpgradeReleaseBaseURL, "/"), githubRepo, version, filename)
	order := gitHubRouteOrder()
	urls := make([]string, 0, len(order))
	for _, prefix := range order {
		urls = append(urls, prefixURL(prefix, base))
	}
	return urls
}

func (h *Handler) fetchSystemUpgradeReleases(perPage int) ([]githubRelease, error) {
	if perPage <= 0 {
		perPage = 20
	}
	// B05-004：与 fetchGitHubReleases 共用同一缓存键与同一
	// 条目（releasesCacheKey in upgrade.go）——旧实现两路同表异键
	// （"sysapi:"/"api:"）永不互相命中，同一份列表冷缓存时向 GitHub
	// 请求两遍，未认证配额双倍消耗。现两路都全量拉取
	// releasesFetchPerPage 条存缓存、读取时各自 slice，本注释与实现
	// 自此一致（生产中 systemUpgradeAPIBaseURL 与 githubAPIBase 同值）。
	cacheKey := releasesCacheKey(systemUpgradeAPIBaseURL)
	if releases, ok := cachedReleases(cacheKey); ok {
		return sliceReleases(releases, perPage), nil
	}

	apiURL := fmt.Sprintf("%s/repos/%s/releases?per_page=%d", strings.TrimRight(systemUpgradeAPIBaseURL, "/"), githubRepo, releasesFetchPerPage)

	// U1-001 的「代理先行失败降级直连」在 1.7.7 升级为内置候选测速
	// 直选（KU5，github_routes.go）：直连优先短路、失败按代理探测
	// 排名续切。gh-proxy 类服务不转发 api.github.com 时该候选自行
	// 失败落到下一名，全链路不再系于单点配置。
	// U1-006/C-02 口径不变：单次尝试客户端走共用加固助手（钉死拨
	// 号 + 逐跳重定向复检），超时按段预算剩余量收紧（上限 30s）。
	var releases []githubRelease
	err := forEachGitHubRoute(githubFetchStageBudget, func(prefix string, attemptTimeout time.Duration) error {
		client := githubVersionFetchClientWithTimeout(attemptTimeout)
		rs, err := h.fetchSystemUpgradeReleasePage(client, prefixURL(prefix, apiURL))
		if err != nil {
			log.Printf("system_upgrade: 版本API请求失败（路线 %s）: %v", gitHubRouteLabel(prefix), err)
			return err
		}
		releases = rs
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("请求GitHub API失败（全部候选路线均已尝试）: %v", err)
	}
	storeReleasesCache(cacheKey, releases)
	return sliceReleases(releases, perPage), nil
}

func (h *Handler) fetchSystemUpgradeReleasePage(client *http.Client, url string) ([]githubRelease, error) {
	resp, err := systemUpgradeHTTPGet(client, url)
	if err != nil {
		return nil, fmt.Errorf("请求GitHub API失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		if resp.StatusCode == http.StatusForbidden && strings.Contains(string(body), "rate limit") {
			return nil, fmt.Errorf("GitHub API 已限流（未认证额度 60 次/小时/IP），请稍后再试")
		}
		return nil, fmt.Errorf("GitHub API返回 %d: %s", resp.StatusCode, string(body))
	}

	var releases []githubRelease
	// U1-006：解码体积兜底，防异常代理返回超大 body；上限取 256MiB 仅作兜底，
	// 正常 releases 负载远低于此，避免重演内核版本列表被小上限截断的故障。
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 256<<20))
	if err := decoder.Decode(&releases); err != nil {
		return nil, fmt.Errorf("解析GitHub API响应失败: %v", err)
	}
	return releases, nil
}

func (h *Handler) resolveSystemUpgradeLatestReleaseByChannel(channel string) (string, error) {
	normalizedChannel := normalizeReleaseChannel(channel)
	releases, err := h.fetchSystemUpgradeReleases(50)
	if err != nil {
		// API 不可用（限流等）时稳定版回退到重定向解析，不依赖 API 配额。
		// F-029：重定向解析与节点侧同口径（1.7.7 起同为内置直选）。
		// J-03：兜底亦须过渠道过滤——稳定渠道只接受纯数字 tag，
		// 命中 beta 时继续报 API 错误（与节点侧同口径）。
		if normalizedChannel == releaseChannelStable {
			if tag, ferr := resolveLatestTagByRedirect(); ferr == nil && releaseChannelFromTag(tag) == normalizedChannel {
				return tag, nil
			}
		}
		return "", err
	}

	for _, r := range releases {
		if r.Draft {
			continue
		}
		tag := strings.TrimSpace(r.TagName)
		if tag == "" {
			continue
		}
		if releaseMatchesChannel(r, normalizedChannel) {
			return tag, nil
		}
	}

	return "", fmt.Errorf("未找到%s版本号", releaseChannelLabel(normalizedChannel))
}

func fileModeOrDefault(path string, fallback os.FileMode) (os.FileMode, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fallback, nil
		}
		return 0, err
	}
	return info.Mode().Perm(), nil
}

// capEnvFileMode 把 .env 族写点（updateEnvVersion 的 .env 改写、
// backupFile 的 .upgrade.bak、restoreBackup 的恢复写）继承来的
// 文件 mode 封顶到 0600（I-C3-03(b)）：.env 与其备份含
// JWT_SECRET/数据库口令，此前自升级改写只继承现行 mode、永不收
// 紧——运维手工建的 0644 .env 经改写后原样保留全局可读，无任何
// 一面纠正。口径：(继承值 &^ 077) | 0600，即清组/其他位并保属主
// 读写；继承值不存在时 fileModeOrDefault 的回落已是 0600、本函
// 数对其为恒等。三写点共用 backupFile/restoreBackup 的 compose
// 备份/恢复同受此帽（compose 同为部署配置面，0600 对其属主读
// 写无影响）。
func capEnvFileMode(mode os.FileMode) os.FileMode {
	return (mode &^ 0o077) | 0o600
}

// writeFileAtomicWithMode 以「同目录临时文件 + Rename 覆盖」原子落盘：
// 并发读者只会看到旧内容或新内容，不会读到截断写中途的撕裂态。用于
// 升级状态文件这类写方回写瞬间读方正在判定的跨进程文件（B05-002），
// 以及自升级对 .env / docker-compose.yml 的整份覆写与 .upgrade.bak
// 备份/恢复——整机断电落在写中途时，现行文件保持旧内容完好，不再
// 依赖人工拿 .upgrade.bak 还原（I-C3-02）。本文件的部署文件写点
// 一律走此路，原非原子直写 helper 已删除，勿再引入。
func writeFileAtomicWithMode(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

func (e *systemUpgradeExecutor) currentBackendImage(ctx context.Context) (string, error) {
	if err := validateBackendContainerName(e.backendContainer); err != nil {
		return "", err
	}
	out, err := exec.CommandContext(ctx, "docker", "inspect", "-f", "{{.Image}}", e.backendContainer).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("inspect backend image failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	imageID := strings.TrimSpace(string(out))
	if imageID == "" {
		return "", fmt.Errorf("backend image id is empty")
	}
	return imageID, nil
}

// panelImageRefPrefix 是本项目全部发布镜像的注册表前缀：
// ghcr.io/<仓库 owner>/，owner 取自 githubRepo（与 agentImageRef
// 同派生）。与 panel_install.sh 的 panel_image_prefix 同口径——
// 脚本侧共存守卫以此为归属根：名字会撞，镜像归属不会。面板内
// 升级的身份断言必须与之用同一根，否则两侧对同一部署的归属
// 判断会分家。
func panelImageRefPrefix() string {
	owner := githubRepo
	if i := strings.Index(owner, "/"); i >= 0 {
		owner = owner[:i]
	}
	return "ghcr.io/" + strings.ToLower(owner) + "/"
}

// panelImageRefOwned 判定一个镜像引用是否在本项目发布前缀下
// （前缀级判定：同前缀的旧名镜像同样算自家，与脚本侧一致）。
func panelImageRefOwned(ref string) bool {
	ref = strings.TrimSpace(ref)
	return ref != "" && strings.HasPrefix(ref, panelImageRefPrefix())
}

// backendImageRef 返回后端容器创建时使用的镜像引用
// （.Config.Image，如 ghcr.io/<owner>/flmx-panel-backend:1.9.7；
// 改名前的 flmx-flux-panel-backend 同前缀、归属判定同样通过）。
// 与 currentBackendImage 返回的解析后镜像 ID 不同：归属断言必须
// 看引用本身——镜像 ID 只证明某个镜像存在，无法区分同机同名的
// 他方部署。
func (e *systemUpgradeExecutor) backendImageRef(ctx context.Context) (string, error) {
	if err := validateBackendContainerName(e.backendContainer); err != nil {
		return "", err
	}
	out, err := exec.CommandContext(ctx, "docker", "inspect", "-f", "{{.Config.Image}}", e.backendContainer).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("inspect backend image ref failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	ref := strings.TrimSpace(string(out))
	if ref == "" {
		return "", fmt.Errorf("backend image ref is empty")
	}
	return ref, nil
}

// verifyBackendImageOwnership 是 R2-006（AI-08）的镜像归属断言：
// 所 inspect 的容器必须确实在跑本项目发布的镜像。容器名来自
// env 显式指定时逐字沿用（含存量旧名 flux-panel-backend），同机
// 他方旧装的同名容器只凭名字与镜像 ID 会被误认成本面板后端——
// helper 随后将以他方镜像在本方部署目录上执行升级脚本。引用不
// 在本仓前缀下即拒绝，并指引脚本更新路径（脚本侧有自己的归属
// 守卫，是这类部署完成升级的正道）。inspect 失败与现行
// currentBackendImage 同口径 fail-closed。env 透传丢失时构造器
// 只回退 FLMX 标准名（F4-02），不再凭旧名撞同机旧谱系部署。
func (e *systemUpgradeExecutor) verifyBackendImageOwnership(ctx context.Context) error {
	ref, err := e.backendImageRef(ctx)
	if err != nil {
		return err
	}
	if !panelImageRefOwned(ref) {
		return fmt.Errorf("后端容器镜像 %s 不属于本面板（发布前缀 %s），已拒绝面板内升级：该容器可能属于同机其他面板或自备镜像部署。请在服务器上进入部署目录运行安装脚本并选择“更新面板”完成升级", ref, panelImageRefPrefix())
	}
	return nil
}

func (e *systemUpgradeExecutor) startHelper(ctx context.Context, imageID, helperName string) (string, error) {
	args, err := e.buildHelperRunArgs(imageID, helperName)
	if err != nil {
		return "", err
	}
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("start helper failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	containerID := strings.TrimSpace(string(out))
	if containerID == "" {
		containerID = helperName
	}
	return containerID, nil
}

// downloadReleaseAsset 下载并校验一个 release 资产，返回资产
// 本体与锚复核说明（单锚降级时非空、须由调用方点名进升级回执
// 与日志；双锚通过时为空）。
func (h *Handler) downloadReleaseAsset(version, filename string) ([]byte, string, error) {
	// F-139：资产下载必须有多路线尝试（旧实现只产出面板配置代理的
	// 单条 URL，代理对该资产不可用时无直连重试）。1.7.7（KU5）候
	// 选 = systemUpgradeDownloadURLs 的全表（直选路线序，10 条）：
	// 资产与 .sha256 必须同源同次取得——每个候选内部独立完成下载
	// +校验，不跨源混搭；全候选共用 githubAssetDownloadBudget 总
	// 预算，单候选沿 SecureFetch 的 60s 腿界（取两者较小者）。
	// L8-F-02：同源校验只是第一锚——获胜候选定版后必须再经
	// corroborateReleaseAssetChecksum 从异路复核同一校验值，不一
	// 致硬失败中止（不换路重试），无异路可复核时降级单锚并把降
	// 级说明交回调用方点名。
	ctx, cancel := context.WithTimeout(context.Background(), githubAssetDownloadBudget)
	defer cancel()
	urls := h.systemUpgradeDownloadURLs(version, filename)
	var lastErr error
	for i, url := range urls {
		attemptCtx, attemptCancel := context.WithTimeout(ctx, secureFetchTotalTimeout)
		body, want, err := h.downloadReleaseAssetAttempt(attemptCtx, url, filename)
		attemptCancel()
		if err == nil {
			note, cerr := h.corroborateReleaseAssetChecksum(ctx, urls, i, filename, want)
			if cerr != nil {
				return nil, "", cerr
			}
			return body, note, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	return nil, "", lastErr
}

// systemUpgradeAnchorTimeout 是跨线路锚复核一轮的总界
// （L8-F-02）：与 agent 侧 anchorCorroborateTimeout 同值同口径
// ——复核对象只是小校验文件，逐候选尝试共用此界；且复核整体仍
// 在 githubAssetDownloadBudget 总预算内完成，不新增端点时限
// （T004 的 240s 界不变）。
var systemUpgradeAnchorTimeout = 20 * time.Second

// corroborateReleaseAssetChecksum 是面板自升级的第二锚
// （L8-F-02，与 agent 侧 corroborateUpgradeChecksum 同口径）：
// 获胜候选的 .sha256 与资产同源同次取得，单个镜像失陷即可自洽
// 篡改（资产与校验一起换）。这里从候选集内与获胜线路不同的另
// 一条线路重取同一资产的 .sha256 比对：任一异路给出不同哈希即
// 硬失败中止；异路给出的内容不是合法 SHA256 时只废该路、继续
// 问下一路（与 agent 腿内口径一致）；全部异路都给不出合法校验
// 值（或候选集只有一条线路）时降级为单锚继续，并返回必须点名
// 进升级回执/日志的降级说明。返回空串表示复核通过。
func (h *Handler) corroborateReleaseAssetChecksum(ctx context.Context, urls []string, winner int, filename, want string) (string, error) {
	actx, cancel := context.WithTimeout(ctx, systemUpgradeAnchorTimeout)
	defer cancel()
	var lastErr error
	for i, url := range urls {
		if i == winner {
			continue
		}
		shaBody, err := SecureFetch(actx, url+".sha256")
		if err != nil {
			lastErr = err
			continue
		}
		got := parseReleaseChecksum(shaBody)
		if got == "" {
			lastErr = fmt.Errorf("%s 的校验文件内容不是合法 SHA256", filename)
			continue
		}
		if !strings.EqualFold(got, want) {
			return "", fmt.Errorf("校验文件经两条不同线路获取的结果不一致，已中止升级（L8-F-02 第二锚）: %s != %s", got, want)
		}
		return "", nil
	}
	note := "跨线路锚复核已降级：升级校验锚复核不可用（其他线路均未给出校验值），按单锚继续"
	if lastErr != nil {
		note = fmt.Sprintf("%s（%v）", note, lastErr)
	}
	return note, nil
}

func (h *Handler) downloadReleaseAssetFrom(url, filename string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), secureFetchTotalTimeout)
	defer cancel()
	return h.downloadReleaseAssetFromCtx(ctx, url, filename)
}

func (h *Handler) downloadReleaseAssetFromCtx(ctx context.Context, url, filename string) ([]byte, error) {
	body, _, err := h.downloadReleaseAssetAttempt(ctx, url, filename)
	return body, err
}

// parseReleaseChecksum 解析 .sha256 校验文件的期望哈希（B7-04）：
// 首字段为空白（含制表）分隔的哈希本体，大小写与 sha256sum 的
// "*" 二进制标记都容忍——这些都是校验文件的合法书写形态，此
// 前单腿用 Split(" ")[0]＋大小写敏感比对，制表分隔或大写哈希
// 会把格式问题误报成「校验失败」中止升级。内容不是合法
// SHA256 时返回 ""，调用方 fail-closed；格式放宽不放宽真值：
// 比对仍是完整 64 位十六进制的相等判定。
func parseReleaseChecksum(shaBody []byte) string {
	fields := strings.Fields(string(shaBody))
	if len(fields) == 0 || !ghMirrorSHA256Hex.MatchString(fields[0]) {
		return ""
	}
	return strings.ToLower(fields[0])
}

// downloadReleaseAssetAttempt 是单候选下载腿：资产与 .sha256
// 同源同次取得并比对（R5-B5 D16 口径不变），额外回传该腿采信
// 的期望哈希，供 downloadReleaseAsset 做跨线路第二锚复核
// （L8-F-02）。
func (h *Handler) downloadReleaseAssetAttempt(ctx context.Context, url, filename string) ([]byte, string, error) {
	// R5-B5 D16: 经 SecureFetch（SSRF 防护 + 大小限制 + 超时）。
	body, err := SecureFetch(ctx, url)
	if err != nil {
		return nil, "", fmt.Errorf("下载%s失败: %v", filename, err)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, "", fmt.Errorf("下载%s内容为空", filename)
	}
	// R5-B5 D16: 校验和缺失即 fail-closed（与 H-01 对 gost 二进制的标准一致）。
	shaURL := url + ".sha256"
	shaBody, err := SecureFetch(ctx, shaURL)
	if err != nil {
		return nil, "", fmt.Errorf("下载%s的校验文件失败（fail-closed）: %v", filename, err)
	}
	expected := parseReleaseChecksum(shaBody)
	if expected == "" {
		return nil, "", fmt.Errorf("%s的校验文件内容不是合法 SHA256（fail-closed）", filename)
	}
	actual := fmt.Sprintf("%x", sha256.Sum256(body))
	if !hmac.Equal([]byte(expected), []byte(actual)) {
		return nil, "", fmt.Errorf("%s校验失败：期望 %s，实际 %s", filename, expected, actual)
	}
	return body, expected, nil
}

func releasesForChannel(releases []githubRelease, channel string) []systemUpgradeReleaseData {
	channel = normalizeReleaseChannel(channel)
	items := make([]systemUpgradeReleaseData, 0, len(releases))
	for _, r := range releases {
		if r.Draft {
			continue
		}
		tag := strings.TrimSpace(r.TagName)
		if tag == "" {
			continue
		}
		if !releaseMatchesChannel(r, channel) {
			continue
		}
		itemChannel := releaseChannelFromTag(tag)
		items = append(items, systemUpgradeReleaseData{
			Version:     tag,
			Name:        r.Name,
			PublishedAt: r.PublishedAt,
			Prerelease:  r.Prerelease || itemChannel == releaseChannelDev,
			Channel:     itemChannel,
		})
	}
	return items
}

func decodeSystemUpgradeRequest(r *http.Request, req *systemUpgradeRequest) error {
	defer r.Body.Close()
	// B05-001（R3-K9）：ReadAll 前以 MaxBytesReader 包裹与 decodeJSON
	// 同一 10MB 上限（旧实现无上限读入）；DisallowUnknownFields 语义保留。
	body, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, 10<<20))
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	return decoder.Decode(req)
}

func systemUpgradeVersionResponse(current, channel, latest string, lookupErr error, capability systemUpgradeCapabilityData) systemUpgradeVersionData {
	// O9-004：语义版本比较（旧逻辑字符串相等：1.0.10 与 1.0.9 会误判、降级也会误报"有更新"）
	// A11-132-06：版本不可比（解析失败）时 fail-closed，不报有更新。
	hasUpdate := false
	if latest != "" {
		if cmp, err := compareVersions(latest, current); err == nil && cmp > 0 {
			hasUpdate = true
		}
	}
	data := systemUpgradeVersionData{
		CurrentVersion: current,
		LatestVersion:  latest,
		HasUpdate:      hasUpdate,
		Channel:        channel,
		Capability:     capability,
	}
	if lookupErr != nil {
		data.LatestVersion = ""
		data.HasUpdate = false
		data.Reason = lookupErr.Error()
	}
	return data
}

func (h *Handler) systemVersion(w http.ResponseWriter, r *http.Request) {
	// B05-001：版本发现两段链（API 60s + 重定向兜底 60s）后端界
	// 120s，此前独缺 extendWriteDeadline 而与全局 WriteTimeout 120s
	// 恰等、且被登记表误分类为 DB/local（C080）逃过三层对账。
	extendWriteDeadline(w, systemVersionWriteDeadline)
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}

	channel := releaseChannelStable
	current := currentPanelVersion()
	exec := newSystemUpgradeExecutor()
	capability := exec.capability(r.Context())
	latest, err := h.resolveSystemUpgradeLatestReleaseByChannel(channel)
	data := systemUpgradeVersionResponse(current, channel, latest, err, capability)
	// R5-B4C: 暴露待执行迁移状态（结构化）。
	if pm := h.repo.GetPendingMigrationStatus(); pm.Required {
		data.PendingMigrations = pm
	}
	// 修正 A：管理员补建状态。
	data.AdminBootstrapRequired = repo.IsAdminBootstrapRequired()
	response.WriteJSON(w, response.OK(data))
}

func (h *Handler) systemCheckUpdates(w http.ResponseWriter, r *http.Request) {
	// B05-003：补重定向兜底后本接口与 /system/version 同为发现两段
	// 链（界 120s），同享 systemVersionWriteDeadline 放宽（T009 行）。
	extendWriteDeadline(w, systemVersionWriteDeadline)
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}

	var req systemUpgradeRequest
	if err := decodeSystemUpgradeRequest(r, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	channel := normalizeReleaseChannel(req.Channel)
	current := currentPanelVersion()
	exec := newSystemUpgradeExecutor()
	capability := exec.capability(r.Context())

	githubReleases, err := h.fetchSystemUpgradeReleases(50)
	if err != nil {
		// B05-003：与 /system/version（resolveSystemUpgradeLatest
		// ReleaseByChannel）口径对齐——API 不可用（限流等）时稳定
		// 渠道回退重定向解析：最新版照常给出，列表留空并在 Reason
		// 中标注降级；dev 渠道与兜底失败保留原错误。J-03：兜底结果
		// 仍须过渠道过滤（beta tag 不冒充稳定版）。
		if channel == releaseChannelStable {
			if tag, ferr := resolveLatestTagByRedirect(); ferr == nil && releaseChannelFromTag(tag) == channel {
				hasUpdate := false
				if cmp, cerr := compareVersions(tag, current); cerr == nil && cmp > 0 {
					hasUpdate = true
				}
				response.WriteJSON(w, response.OK(systemUpgradeCheckData{
					CurrentVersion: current,
					LatestVersion:  tag,
					HasUpdate:      hasUpdate,
					Channel:        channel,
					Capability:     capability,
					Releases:       []systemUpgradeReleaseData{},
					Reason:         fmt.Sprintf("版本列表获取失败（%v），已降级为仅解析最新版本号，版本列表暂不可用", err),
				}))
				return
			}
		}
		response.WriteJSON(w, response.Err(-2, fmt.Sprintf("获取版本列表失败: %v", err)))
		return
	}
	releases := releasesForChannel(githubReleases, channel)
	latest := ""
	if len(releases) > 0 {
		latest = releases[0].Version
	}
	// O9-004：语义版本比较（旧逻辑字符串相等：1.0.10 与 1.0.9 会误判、降级也会误报"有更新"）
	// A11-132-06：版本不可比（解析失败）时 fail-closed，不报有更新。
	hasUpdate := false
	if latest != "" {
		if cmp, err := compareVersions(latest, current); err == nil && cmp > 0 {
			hasUpdate = true
		}
	}
	response.WriteJSON(w, response.OK(systemUpgradeCheckData{
		CurrentVersion: current,
		LatestVersion:  latest,
		HasUpdate:      hasUpdate,
		Channel:        channel,
		Capability:     capability,
		Releases:       releases,
	}))
}

func (h *Handler) systemUpgrade(w http.ResponseWriter, r *http.Request) {
	// R8-T-04: the synchronous prelude (release resolution plus
	// asset download) runs up to ~210s worst case.
	extendWriteDeadline(w, systemUpgradeWriteDeadline)
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	if !h.systemUpgradeMu.TryLock() {
		response.WriteJSON(w, response.ErrDefault(systemUpgradeConflictError))
		return
	}
	defer h.systemUpgradeMu.Unlock()

	// U1-003：落盘状态检查。systemUpgradeMu 只覆盖同步段，helper 异步执行数分钟，
	// 期间必须靠状态文件拦截二次升级（否则 .bak 会被中间态覆盖污染）。
	exec := newSystemUpgradeExecutor()
	// B05-002/B14-004：拦截判定与状态端点共用 assessUpgradeStatus 同一
	// 判定（含状态文件损坏时的心跳保守口径），不再各行其是。
	assessment := exec.assessUpgradeStatus(time.Now())
	st := assessment.Raw
	switch {
	case assessment.Corrupt && assessment.Blocking:
		log.Printf("system_upgrade: 升级状态文件损坏但 helper 心跳新鲜，保守按升级进行中拦截")
		response.WriteJSON(w, response.ErrDefault(systemUpgradeConflictError+"（升级状态文件损坏，helper 心跳新鲜，保守拦截）"))
		return
	case assessment.Corrupt:
		log.Printf("system_upgrade: 升级状态文件损坏且心跳缺失/过期，视为残留，允许新升级")
	case assessment.Blocking:
		response.WriteJSON(w, response.ErrDefault(fmt.Sprintf("%s（目标版本 %s，开始于 %s）", systemUpgradeConflictError, st.Version, st.StartedAt)))
		return
	case st.InProgress:
		log.Printf("system_upgrade: 检测到残留的升级中状态（版本 %s，开始于 %s），视为已中断，允许新升级", st.Version, st.StartedAt)
	}

	var req systemUpgradeRequest
	if err := decodeSystemUpgradeRequest(r, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	channel := normalizeReleaseChannel(req.Channel)
	capability := exec.capability(r.Context())
	if !capability.Capable {
		response.WriteJSON(w, response.ErrDefault("当前环境不支持面板自升级: "+strings.Join(capability.Reasons, "; ")))
		return
	}
	version := strings.TrimSpace(req.Version)
	if version == "" {
		var err error
		version, err = h.resolveSystemUpgradeLatestReleaseByChannel(channel)
		if err != nil {
			response.WriteJSON(w, response.Err(-2, fmt.Sprintf("获取最新%s失败: %v", releaseChannelLabel(channel), err)))
			return
		}
	}
	// O9-003：version 确定后立即校验格式（之前只在下载后的 updateEnvVersion 中校验，
	// 属于下载后才触发，防御纵深缺失；现提前到下载前）
	if err := validateUpgradeVersion(version); err != nil {
		response.WriteJSON(w, response.ErrDefault("版本号不合法: "+err.Error()))
		return
	}
	// O9-001：升级前比较目标版本与当前版本，防止静默降级
	// 旧逻辑：接受任意版本号直接下载升级，允许降级到旧版本
	// U1-005：比较前剥离 "v" 前缀（与节点侧 upgrade.go 一致），避免手填 v1.0.10 被误判降级
	currentVer := currentPanelVersion()
	// A11-132-06：不可比版本 fail-closed 拒绝（旧实现把解析失败的段按 0
	// 计，构造版本号可绕过这道防降级比较）。
	cmp, cmpErr := compareVersions(strings.TrimPrefix(version, "v"), strings.TrimPrefix(currentVer, "v"))
	if cmpErr != nil {
		log.Printf("systemUpgrade: 版本不可比较，拒绝升级：%v", cmpErr)
		response.WriteJSON(w, response.ErrDefault(fmt.Sprintf("版本号 %s 或当前版本 %s 无法比较，已拒绝升级: %v", version, currentVer, cmpErr)))
		return
	}
	if cmp < 0 {
		log.Printf("systemUpgrade: 拒绝降级请求，目标版本 %s 低于当前版本 %s", version, currentVer)
		response.WriteJSON(w, response.ErrDefault(fmt.Sprintf("目标版本 %s 低于当前版本 %s，不允许降级。如确需降级请手动操作。", version, currentVer)))
		return
	} else if cmp == 0 {
		log.Printf("systemUpgrade: 目标版本 %s 与当前版本相同", version)
	}
	imageID, err := exec.currentBackendImage(r.Context())
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}

	// 存量资源名钉键（1.9.9）：赶在备份/换 compose/改 .env 之前，
	// 把 .env 缺失的卷/网络/项目名按旧容器现行值钉死，防新模板
	// 缺省卷名让后端挂空卷（1.9.8 事故，见 pinLegacyResourceNames）。
	// 期望数据卷名随后透传 helper 做重建后校验。fail-closed 中止
	// 时文件一字未动（钉键写点尚未发生、备份亦未做）。
	expectedVolume, expectedPGVolume, err := exec.pinLegacyResourceNames(r.Context())
	if err != nil {
		msg := "升级已中止（未做任何更改）: " + err.Error()
		if errors.Is(err, errDataVolumeUndetermined) {
			msg += "。为防升级后挂到空卷，请在部署 .env 中显式设置 FLMX_SQLITE_VOLUME 为现有数据卷名后重试，或改用安装脚本的“更新面板”完成升级"
		}
		if errors.Is(err, errPGVolumeUndetermined) {
			msg += "。为防升级后挂到空卷，请在部署 .env 中显式设置 FLMX_POSTGRES_VOLUME 为现有 PostgreSQL 数据卷名后重试，或改用安装脚本的“更新面板”完成升级"
		}
		log.Printf("systemUpgrade: %s", msg)
		response.WriteJSON(w, response.Err(-2, msg))
		return
	}
	exec.expectedSQLiteVolume = expectedVolume
	exec.expectedPostgresVolume = expectedPGVolume

	composePath := exec.composePath()
	envPath := exec.envPath()
	composeData, err := os.ReadFile(composePath)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, "读取compose失败: "+err.Error()))
		return
	}
	composeAsset := exec.selectComposeAsset(composeData)
	newCompose, anchorNote, err := h.downloadReleaseAsset(version, composeAsset)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, wrapUpgradeDownloadError(err).Error()))
		return
	}
	// L8-F-02：单锚降级必须在升级回执与日志里点名（与 agent 侧
	// RV2-03 同口径：降级不能只剩面板本地不可见的痕迹）。
	if anchorNote != "" {
		log.Printf("systemUpgrade: %s（资产 %s，目标版本 %s）", anchorNote, composeAsset, version)
	}
	if _, err := exec.backupFile(composePath); err != nil {
		response.WriteJSON(w, response.Err(-2, "备份compose失败: "+err.Error()))
		return
	}
	if _, err := exec.backupFile(envPath); err != nil {
		response.WriteJSON(w, response.Err(-2, "备份.env失败: "+err.Error()))
		return
	}
	// 存量容器改名（1.9.9，parity 矩阵 D1）：钉卷之后、.env 备份
	// 之后、换 compose 之前把旧缺省容器键改写为现行名（细节与
	// PG 模式分流裁定见 applyIdentityRename）。备份在改名之前是
	// 有意的：升级失败回滚恢复的 .env 含改名前的身份键。写失败
	// 按同步段既有口径恢复备份并中止（容器未动）。
	if err := exec.applyIdentityRename(r.Context()); err != nil {
		if restoreErr := exec.restoreUpgradeBackups(composePath, envPath); restoreErr != nil {
			err = fmt.Errorf("%v; 回滚失败: %v", err, restoreErr)
		}
		response.WriteJSON(w, response.Err(-2, "存量容器改名失败: "+err.Error()))
		return
	}
	if err := exec.replaceCompose(composePath, newCompose); err != nil {
		if restoreErr := exec.restoreUpgradeBackups(composePath, envPath); restoreErr != nil {
			err = fmt.Errorf("%v; 回滚失败: %v", err, restoreErr)
		}
		response.WriteJSON(w, response.Err(-2, "替换compose失败: "+err.Error()))
		return
	}
	if err := exec.updateEnvVersion(envPath, version); err != nil {
		if restoreErr := exec.restoreUpgradeBackups(composePath, envPath); restoreErr != nil {
			err = fmt.Errorf("%v; 回滚失败: %v", err, restoreErr)
		}
		response.WriteJSON(w, response.Err(-2, "更新版本配置失败: "+err.Error()))
		return
	}
	exec.composeProject = exec.detectComposeProject(r.Context())
	exec.hostDeployDir = exec.detectHostDeployDir(r.Context())
	// U1-003：启动 helper 前落盘「升级中」状态，并透传给 helper 由其回写终态。
	exec.upgradeVersion = version
	exec.upgradeStartedAt = time.Now().UTC().Format(time.RFC3339)
	// R8-003：降级点名随状态落盘并透传 helper，状态轮询面同步透出。
	exec.upgradeAnchorNote = anchorNote
	// DB 快照安排定格（SQLite 模式点名快照相对路径，状态与回执
	// 据此透出；PG 模式切出标记，见 prepareUpgradeSnapshot）。
	exec.prepareUpgradeSnapshot(version)
	if err := exec.writeUpgradeStatus(systemUpgradeStatusData{
		InProgress:         true,
		Version:            version,
		StartedAt:          exec.upgradeStartedAt,
		AnchorNote:         anchorNote,
		SnapshotPath:       exec.snapshotPath,
		PgSnapshotDeferred: exec.pgSnapshotDeferred,
	}); err != nil {
		if restoreErr := exec.restoreUpgradeBackups(composePath, envPath); restoreErr != nil {
			err = fmt.Errorf("%v; 回滚失败: %v", err, restoreErr)
		}
		response.WriteJSON(w, response.Err(-2, "写入升级状态失败: "+err.Error()))
		return
	}
	// B05-005：预触心跳，覆盖 helper 启动到首次心跳的间隙。
	exec.touchUpgradeHeartbeat()
	helperName := fmt.Sprintf("flmx-upgrade-helper-%d", time.Now().Unix())
	helperContainer, err := exec.startHelper(r.Context(), imageID, helperName)
	if err != nil {
		// U1-003：helper 未启动成功，立即解除落盘的「升级中」状态，避免永久拦截后续升级。
		_ = exec.writeUpgradeStatus(systemUpgradeStatusData{
			InProgress:         false,
			Version:            version,
			StartedAt:          exec.upgradeStartedAt,
			Result:             "helper_start_failed",
			FinishedAt:         time.Now().UTC().Format(time.RFC3339),
			AnchorNote:         anchorNote,
			SnapshotPath:       exec.snapshotPath,
			PgSnapshotDeferred: exec.pgSnapshotDeferred,
		})
		if restoreErr := exec.restoreUpgradeBackups(composePath, envPath); restoreErr != nil {
			err = fmt.Errorf("%v; 回滚失败: %v", err, restoreErr)
		}
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}

	message := systemUpgradeMessage
	if anchorNote != "" {
		message += "（" + anchorNote + "）"
	}
	response.WriteJSON(w, response.OK(systemUpgradeRunData{
		Version:            version,
		Channel:            channel,
		ComposeAsset:       composeAsset,
		HelperContainer:    helperContainer,
		BackendImageID:     imageID,
		Message:            message,
		SnapshotPath:       exec.snapshotPath,
		PgSnapshotDeferred: exec.pgSnapshotDeferred,
	}))
}

// wrapUpgradeDownloadError 包装升级下载错误（U1-008）。
// 代理地址解析到内网/回环时 SecureFetch 默认按 SSRF 策略拒绝，错误原文
// （private network not allowlisted）对运维无指引，这里追加可操作说明。
func wrapUpgradeDownloadError(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "private network not allowlisted") {
		return fmt.Errorf("%v（提示：GitHub 代理地址解析为内网/回环地址，被安全下载策略拒绝；如确为可信自建代理，请在面板部署环境设置 FLMX_FETCH_ALLOW_PRIVATE_NETWORKS=true 或 FLMX_FETCH_PRIVATE_ALLOWLIST 后重试）", err)
	}
	return err
}

// systemUpgradeStatus 返回落盘的升级状态（U1-007）：是否正在升级、目标版本、
// 上次升级结果。无状态文件时返回 {inProgress:false}。
// B14-004：返回前套用与发起路径同一判定（assessUpgradeStatus）做只读
// 折算——残留进行中状态折算为 inProgress=false + result="interrupted"，
// 不再原样回吐与「可否再发起升级」分裂的口径。
func (h *Handler) systemUpgradeStatus(w http.ResponseWriter, r *http.Request) {
	// R8-075：状态查询是只读幂等面——GET 与 POST 同形受理，POST 携带
	// 的 body（如探测工具/脚本惯用的 `{}`）直接忽略，不再因 method 或
	// body 形态返回 -1，与发布产物真机实证的契约不一致收口。
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	if r.Body != nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 1<<20))
		_ = r.Body.Close()
	}
	exec := newSystemUpgradeExecutor()
	response.WriteJSON(w, response.OK(exec.assessUpgradeStatus(time.Now()).Status))
}
