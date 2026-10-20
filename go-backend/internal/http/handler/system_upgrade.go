package handler

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
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

	"go-backend/internal/http/response"
	"go-backend/internal/store/repo"
)

const (
	panelDeployDirEnv          = "PANEL_DEPLOY_DIR"
	panelBackendContainerEnv   = "PANEL_BACKEND_CONTAINER"
	defaultPanelDeployDir      = "/opt/flvx-panel"
	defaultPanelBackendName    = "flux-panel-backend"
	dockerSocketPath           = "/var/run/docker.sock"
	systemUpgradeMessage       = "升级 helper 已启动，面板服务将短暂重启"
	systemUpgradeConflictError = "已有面板升级任务执行中"
	// U1-003：升级状态文件名（落盘于部署目录，跨进程/重启可查）。
	systemUpgradeStatusFileName = ".flvx_upgrade_status.json"
	// U1-003：状态文件显示进行中但开始时间早于此时长，视为残留
	// （helper 被强杀等异常情况），允许新升级覆盖，避免永久锁死。
	systemUpgradeStaleAfter = 30 * time.Minute
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
	if validateBackendContainerName(e.backendContainer) != nil {
		return ""
	}
	out, err := exec.CommandContext(ctx, "docker", "inspect", "-f",
		`{{index .Config.Labels "com.docker.compose.project"}}`, e.backendContainer).Output()
	if err != nil {
		return ""
	}
	project := strings.TrimSpace(string(out))
	if !composeProjectPattern.MatchString(project) {
		return ""
	}
	return project
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
}

type systemUpgradeRunData struct {
	Version         string `json:"version"`
	Channel         string `json:"channel"`
	ComposeAsset    string `json:"composeAsset"`
	HelperContainer string `json:"helperContainer"`
	BackendImageID  string `json:"backendImageId"`
	Message         string `json:"message"`
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
}

func (e *systemUpgradeExecutor) statusPath() string {
	return filepath.Join(e.deployDir, systemUpgradeStatusFileName)
}

// readUpgradeStatus 读取落盘的升级状态；文件不存在或损坏时返回零值
// （inProgress=false），不视为错误。
func (e *systemUpgradeExecutor) readUpgradeStatus() systemUpgradeStatusData {
	var st systemUpgradeStatusData
	data, err := os.ReadFile(e.statusPath())
	if err != nil {
		return st
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return systemUpgradeStatusData{}
	}
	return st
}

// writeUpgradeStatus 以 0600 权限落盘升级状态。
func (e *systemUpgradeExecutor) writeUpgradeStatus(st systemUpgradeStatusData) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return writeFileWithMode(e.statusPath(), append(data, '\n'), 0o600)
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

type systemUpgradeRequest struct {
	Version string `json:"version"`
	Channel string `json:"channel"`
}

func newSystemUpgradeExecutor() *systemUpgradeExecutor {
	deployDir := strings.TrimSpace(os.Getenv(panelDeployDirEnv))
	if deployDir == "" {
		deployDir = defaultPanelDeployDir
	}
	backendContainer := strings.TrimSpace(os.Getenv(panelBackendContainerEnv))
	if backendContainer == "" {
		backendContainer = defaultPanelBackendName
	}
	return &systemUpgradeExecutor{deployDir: deployDir, backendContainer: backendContainer}
}

func currentPanelVersion() string {
	version := strings.TrimSpace(os.Getenv("FLUX_VERSION"))
	if version == "" {
		return "dev"
	}
	return version
}

// compareVersions 比较两个版本号（如 "1.0.7"）。
// 返回 -1 表示 v1 < v2，0 表示相等，1 表示 v1 > v2。
// O9-001：用于升级前防止静默降级。
func compareVersions(v1, v2 string) int {
	// "dev" 视为最低版本，允许升级到任何正式版本
	if v1 == "dev" && v2 == "dev" {
		return 0
	}
	if v1 == "dev" {
		return -1
	}
	if v2 == "dev" {
		return 1
	}
	parts1 := strings.Split(v1, ".")
	parts2 := strings.Split(v2, ".")
	maxLen := len(parts1)
	if len(parts2) > maxLen {
		maxLen = len(parts2)
	}
	for i := 0; i < maxLen; i++ {
		var n1, n2 int
		if i < len(parts1) {
			n1, _ = strconv.Atoi(strings.TrimSpace(parts1[i]))
		}
		if i < len(parts2) {
			n2, _ = strconv.Atoi(strings.TrimSpace(parts2[i]))
		}
		if n1 < n2 {
			return -1
		}
		if n1 > n2 {
			return 1
		}
	}
	return 0
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
STATUS_FILE="$PANEL_DEPLOY_DIR/.flvx_upgrade_status.json"
log() { echo "[$(date '+%Y-%m-%d %H:%M:%S')] $*" | tee -a "$LOGFILE"; }

# U1-003：回写升级状态文件（与后端 systemUpgradeStatusData 结构体对应）。
# $1=inProgress(true/false)，$2=result。version/startedAt 由环境变量传入。
write_status() {
  printf '{"inProgress": %s, "version": "%s", "startedAt": "%s", "result": "%s", "finishedAt": "%s"}\n' \
    "$1" "${FLUX_UPGRADE_VERSION:-}" "${FLUX_UPGRADE_STARTED_AT:-}" "$2" "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" > "$STATUS_FILE"
  chmod 600 "$STATUS_FILE" 2>/dev/null || true
}

# O9-002：失败时自动恢复备份文件
# 旧逻辑：pull/up 失败时直接 exit 1，已替换的 compose.yml/.env 不恢复，面板无法启动
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
  # U1-003：回滚终态回写状态文件，.bak 保留供人工排查（不删除）。
  write_status "false" "rolled_back"
  log "回滚完成，请检查 upgrade.log 并手动处理"
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
if ! docker compose pull backend frontend >> "$LOGFILE" 2>&1; then
  log "错误: 拉取镜像失败"
  rollback
  exit 1
fi

log "等待旧容器释放资源..."
sleep 3

log "重启服务（force-recreate）..."
if ! docker compose up -d --force-recreate --remove-orphans backend frontend >> "$LOGFILE" 2>&1; then
  log "错误: 重启服务失败"
  rollback
  exit 1
fi

if ! wait_backend_healthy; then
  log "错误: 新后端未通过健康检查"
  rollback
  exit 1
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
			"-e", "FLUX_UPGRADE_VERSION="+e.upgradeVersion,
			"-e", "FLUX_UPGRADE_STARTED_AT="+e.upgradeStartedAt)
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
	data, err := os.ReadFile(envPath)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	replaced := false
	for i, line := range lines {
		// U1-009：按键名精确匹配（旧逻辑 HasPrefix 会误伤 FLUX_VERSION_EXTRA= 等同前缀变量）。
		key := line
		if idx := strings.Index(line, "="); idx >= 0 {
			key = line[:idx]
		}
		if strings.TrimSpace(key) == "FLUX_VERSION" {
			lines[i] = "FLUX_VERSION=" + version
			replaced = true
		}
	}
	if !replaced {
		trimmed := strings.TrimRight(strings.Join(lines, "\n"), "\n")
		if trimmed == "" {
			trimmed = "FLUX_VERSION=" + version
		} else {
			trimmed += "\nFLUX_VERSION=" + version
		}
		return writeFileWithMode(envPath, []byte(trimmed+"\n"), mode)
	}
	content := strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n"
	return writeFileWithMode(envPath, []byte(content), mode)
}

func (e *systemUpgradeExecutor) backupFile(path string) (string, error) {
	mode, err := fileModeOrDefault(path, 0o600)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	backupPath := path + ".upgrade.bak"
	if err := writeFileWithMode(backupPath, data, mode); err != nil {
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
	data, err := os.ReadFile(backupPath)
	if err != nil {
		return err
	}
	return writeFileWithMode(path, data, mode)
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

func (e *systemUpgradeExecutor) replaceCompose(path string, data []byte) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return fmt.Errorf("compose asset is empty")
	}
	mode, err := fileModeOrDefault(path, 0o644)
	if err != nil {
		return err
	}
	return writeFileWithMode(path, data, mode)
}

func (h *Handler) buildSystemUpgradeDownloadURL(version, filename string) string {
	enabled, proxyURL := h.getGithubProxyConfig()
	base := fmt.Sprintf("%s/%s/releases/download/%s/%s", strings.TrimRight(systemUpgradeReleaseBaseURL, "/"), githubRepo, version, filename)
	if enabled {
		return fmt.Sprintf("%s/%s", proxyURL, base)
	}
	return base
}

func (h *Handler) fetchSystemUpgradeReleases(perPage int) ([]githubRelease, error) {
	if perPage <= 0 {
		perPage = 20
	}
	// 与 fetchGitHubReleases 共用 10 分钟缓存（版本列表不需要实时，
	// 页脚检查更新每次打开页面都会调这里，是 API 配额的主要消耗方）。
	cacheKey := "sysapi:" + strings.TrimRight(systemUpgradeAPIBaseURL, "/")
	if releases, ok := cachedReleases(cacheKey); ok {
		return sliceReleases(releases, perPage), nil
	}

	// U1-006：超时 15s → 30s（经代理多一跳 + 50 条 release JSON，慢代理下 15s 偏紧）。
	client := &http.Client{Timeout: 30 * time.Second}
	apiURL := fmt.Sprintf("%s/repos/%s/releases?per_page=%d", strings.TrimRight(systemUpgradeAPIBaseURL, "/"), githubRepo, perPage)

	// U1-001：代理开启时先试代理路径，失败降级直连。
	// gh-proxy 类服务不一定转发 api.github.com（实测 ghfast.top 返回 403
	// "Invalid input."），此前全链路无 fallback，版本发现直接整体失效。
	paths := []string{apiURL}
	if enabled, proxyURL := h.getGithubProxyConfig(); enabled {
		paths = []string{fmt.Sprintf("%s/%s", proxyURL, apiURL), apiURL}
	}

	var lastErr error
	for i, url := range paths {
		releases, err := h.fetchSystemUpgradeReleasePage(client, url)
		if err == nil {
			if i > 0 {
				log.Printf("system_upgrade: 代理版本API失败，已降级直连 api.github.com")
			}
			storeReleasesCache(cacheKey, releases)
			return sliceReleases(releases, perPage), nil
		}
		log.Printf("system_upgrade: 版本API请求失败（尝试 %d/%d）url=%s: %v", i+1, len(paths), url, err)
		lastErr = err
	}
	return nil, fmt.Errorf("请求GitHub API失败（代理/直连均已尝试）: %v", lastErr)
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
	// U1-006：解码体积上限 4MB，防异常代理返回超大 body。
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 4<<20))
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
		if normalizedChannel == releaseChannelStable {
			if tag, ferr := resolveLatestTagByRedirect(); ferr == nil {
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
		if releaseChannelFromTag(tag) == normalizedChannel {
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

func writeFileWithMode(path string, data []byte, mode os.FileMode) error {
	if err := os.WriteFile(path, data, mode); err != nil {
		return err
	}
	return os.Chmod(path, mode)
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

func (h *Handler) downloadReleaseAsset(version, filename string) ([]byte, error) {
	url := h.buildSystemUpgradeDownloadURL(version, filename)
	// R5-B5 D16: 经 SecureFetch（SSRF 防护 + 大小限制 + 超时）。
	ctx, cancel := context.WithTimeout(context.Background(), secureFetchTotalTimeout)
	defer cancel()
	body, err := SecureFetch(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("下载%s失败: %v", filename, err)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, fmt.Errorf("下载%s内容为空", filename)
	}
	// R5-B5 D16: 校验和缺失即 fail-closed（与 H-01 对 gost 二进制的标准一致）。
	shaURL := url + ".sha256"
	shaBody, err := SecureFetch(ctx, shaURL)
	if err != nil {
		return nil, fmt.Errorf("下载%s的校验文件失败（fail-closed）: %v", filename, err)
	}
	expected := strings.TrimSpace(strings.Split(string(shaBody), " ")[0])
	actual := fmt.Sprintf("%x", sha256.Sum256(body))
	if !hmac.Equal([]byte(expected), []byte(actual)) {
		return nil, fmt.Errorf("%s校验失败：期望 %s，实际 %s", filename, expected, actual)
	}
	return body, nil
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
		itemChannel := releaseChannelFromTag(tag)
		if itemChannel != channel {
			continue
		}
		items = append(items, systemUpgradeReleaseData{
			Version:     tag,
			Name:        r.Name,
			PublishedAt: r.PublishedAt,
			Prerelease:  itemChannel == releaseChannelDev,
			Channel:     itemChannel,
		})
	}
	return items
}

func decodeSystemUpgradeRequest(r *http.Request, req *systemUpgradeRequest) error {
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
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
	data := systemUpgradeVersionData{
		CurrentVersion: current,
		LatestVersion:  latest,
		// O9-004：语义版本比较（旧逻辑字符串相等：1.0.10 与 1.0.9 会误判、降级也会误报"有更新"）
		HasUpdate:  latest != "" && compareVersions(latest, current) > 0,
		Channel:    channel,
		Capability: capability,
	}
	if lookupErr != nil {
		data.LatestVersion = ""
		data.HasUpdate = false
		data.Reason = lookupErr.Error()
	}
	return data
}

func (h *Handler) systemVersion(w http.ResponseWriter, r *http.Request) {
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
		response.WriteJSON(w, response.Err(-2, fmt.Sprintf("获取版本列表失败: %v", err)))
		return
	}
	releases := releasesForChannel(githubReleases, channel)
	latest := ""
	if len(releases) > 0 {
		latest = releases[0].Version
	}
	response.WriteJSON(w, response.OK(systemUpgradeCheckData{
		CurrentVersion: current,
		LatestVersion:  latest,
		// O9-004：语义版本比较（旧逻辑字符串相等：1.0.10 与 1.0.9 会误判、降级也会误报"有更新"）
		HasUpdate:  latest != "" && compareVersions(latest, current) > 0,
		Channel:    channel,
		Capability: capability,
		Releases:   releases,
	}))
}

func (h *Handler) systemUpgrade(w http.ResponseWriter, r *http.Request) {
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
	if st := exec.readUpgradeStatus(); st.InProgress && !upgradeStatusStale(st, time.Now()) {
		response.WriteJSON(w, response.ErrDefault(fmt.Sprintf("%s（目标版本 %s，开始于 %s）", systemUpgradeConflictError, st.Version, st.StartedAt)))
		return
	} else if st.InProgress {
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
	if cmp := compareVersions(strings.TrimPrefix(version, "v"), strings.TrimPrefix(currentVer, "v")); cmp < 0 {
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

	composePath := exec.composePath()
	envPath := exec.envPath()
	composeData, err := os.ReadFile(composePath)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, "读取compose失败: "+err.Error()))
		return
	}
	composeAsset := exec.selectComposeAsset(composeData)
	newCompose, err := h.downloadReleaseAsset(version, composeAsset)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, wrapUpgradeDownloadError(err).Error()))
		return
	}
	if _, err := exec.backupFile(composePath); err != nil {
		response.WriteJSON(w, response.Err(-2, "备份compose失败: "+err.Error()))
		return
	}
	if _, err := exec.backupFile(envPath); err != nil {
		response.WriteJSON(w, response.Err(-2, "备份.env失败: "+err.Error()))
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
	if err := exec.writeUpgradeStatus(systemUpgradeStatusData{
		InProgress: true,
		Version:    version,
		StartedAt:  exec.upgradeStartedAt,
	}); err != nil {
		if restoreErr := exec.restoreUpgradeBackups(composePath, envPath); restoreErr != nil {
			err = fmt.Errorf("%v; 回滚失败: %v", err, restoreErr)
		}
		response.WriteJSON(w, response.Err(-2, "写入升级状态失败: "+err.Error()))
		return
	}
	helperName := fmt.Sprintf("flvx-upgrade-helper-%d", time.Now().Unix())
	helperContainer, err := exec.startHelper(r.Context(), imageID, helperName)
	if err != nil {
		// U1-003：helper 未启动成功，立即解除落盘的「升级中」状态，避免永久拦截后续升级。
		_ = exec.writeUpgradeStatus(systemUpgradeStatusData{
			InProgress: false,
			Version:    version,
			StartedAt:  exec.upgradeStartedAt,
			Result:     "helper_start_failed",
			FinishedAt: time.Now().UTC().Format(time.RFC3339),
		})
		if restoreErr := exec.restoreUpgradeBackups(composePath, envPath); restoreErr != nil {
			err = fmt.Errorf("%v; 回滚失败: %v", err, restoreErr)
		}
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}

	response.WriteJSON(w, response.OK(systemUpgradeRunData{
		Version:         version,
		Channel:         channel,
		ComposeAsset:    composeAsset,
		HelperContainer: helperContainer,
		BackendImageID:  imageID,
		Message:         systemUpgradeMessage,
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
		return fmt.Errorf("%v（提示：GitHub 代理地址解析为内网/回环地址，被安全下载策略拒绝；如确为可信自建代理，请在面板部署环境设置 FLVX_FETCH_ALLOW_PRIVATE_NETWORKS=true 或 FLVX_FETCH_PRIVATE_ALLOWLIST 后重试）", err)
	}
	return err
}

// systemUpgradeStatus 返回落盘的升级状态（U1-007）：是否正在升级、目标版本、
// 上次升级结果。无状态文件时返回 {inProgress:false}。
func (h *Handler) systemUpgradeStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	exec := newSystemUpgradeExecutor()
	response.WriteJSON(w, response.OK(exec.readUpgradeStatus()))
}
