package repo

import (
	"crypto/rand"
	"database/sql"
	"log"
	"os"
	"path/filepath"
	"strings"

	"go-backend/internal/security"
	"go-backend/internal/store/model"

	"gorm.io/gorm"
)

// ─── 初始管理员口令（R1-12-01） ─────────────────────────────
//
// 旧实现以公开字面量 admin@user 同作种子管理员的用户名与口令：
// 新装面板在运维首次登录前，任意第三方可用默认口令抢先登录并经
// 强制改密流程设定攻击者口令、完整接管。2.0.0 起新播种不再产生
// 该字面量：
//   - 环境变量 FLMX_ADMIN_PASSWORD 已设置时用它播种（不落文件）；
//   - 否则生成随机初始口令（24 字符、密码学安全随机源），明文只
//     落一次性文件（0600），启动日志点名实际落点路径；落点按
//     「数据目录 → 可执行文件同级目录 → 系统临时目录」成链尝试
//     （FIX-CI-1：单一落点不可写不得中止播种——PG 契约 CI 的落点
//     推导出不可建的 /app、PG 裸二进制未设 DB_PATH、数据卷只读
//     等形态下旧实现永久无管理员；全链皆败时照常建号、登记内存
//     候选并置待重试标，落盘在后续启动探针处重试）；
//   - 初始口令在本进程内登记为「初始口令候选」，handler 的单一
//     裁决点 isStoredDefaultPassword 与旧字面量同口径核对它——
//     初始口令未改前登录只发受限 token、必须先改密；
//   - 管理员真实改密（本人改密/管理员重置的仓储写点）成功后清除
//     候选并删除一次性文件，初始口令就此失效。
//
// 作用域只有「播种时刻」：管理员行已存在的部署（含已播种未认领
// 的存量）不重播、不改存量哈希。启动探针只在存量管理员的存储
// 哈希仍与一次性文件/环境值吻合时才恢复候选登记（跨重启保持
// 强制改密），吻合失败的文件按陈旧文件删除。

// InitialAdminPasswordEnv 是播种时读取的初始管理员口令环境变量。
const InitialAdminPasswordEnv = "FLMX_ADMIN_PASSWORD"

// initialAdminPasswordFileName 是一次性初始口令文件的基名，
// 落在面板数据目录（SQLite 库同级目录；PG 模式为 DB_PATH 同级
// 目录，两种模式下后端均挂载 /app/data）。
const initialAdminPasswordFileName = "initial-admin-password.txt"

// initialAdminPasswordLength 是生成初始口令的长度（字符数）。
// 字母表 56 字符，每字符约 5.8 bit，总熵约 139 bit。
const initialAdminPasswordLength = 24

var (
	// initialAdminDataDir 是数据目录，由 Open/OpenPostgres 在
	// seedData 之前设置；直接调用 seedData 的测试未设置时为空，
	// 文件面自动关闭、仅内存候选生效。
	initialAdminDataDir string
	// initialAdminCandidate 是本进程登记的初始口令候选（明文，
	// 仅内存）；空串表示无候选。
	initialAdminCandidate string
	// initialAdminCandidateFile 是候选对应的一次性文件路径，
	// 环境来源的候选为空串。
	initialAdminCandidateFile string
	// initialAdminPendingWrite 表示生成来源的候选已登记但尚未
	// 落盘（落点链全败），欠一次重试落盘；仅生成来源置位，
	// 环境来源不落文件、恒不置位。
	initialAdminPendingWrite bool
)

// initialAdminFallbackDirs 给出数据目录之后的一次性文件后备
// 落点（可执行文件同级目录、系统临时目录）。变量形态供测试
// 替身，生产恒为此实现。
var initialAdminFallbackDirs = func() []string {
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		if dir := filepath.Dir(exe); dir != "" {
			dirs = append(dirs, dir)
		}
	}
	return append(dirs, os.TempDir())
}

// initialAdminPasswordFileDirs 是落点链（去重、保序）：配置的
// 数据目录在前、后备落点在后。数据目录未配置（直接调用
// seedData 的测试形态）时返回空——文件面整体关闭、仅内存
// 候选生效，与旧语义一致。
func initialAdminPasswordFileDirs() []string {
	if initialAdminDataDir == "" {
		return nil
	}
	seen := map[string]bool{}
	var dirs []string
	for _, dir := range append([]string{initialAdminDataDir}, initialAdminFallbackDirs()...) {
		dir = filepath.Clean(dir)
		if dir == "" || seen[dir] {
			continue
		}
		seen[dir] = true
		dirs = append(dirs, dir)
	}
	return dirs
}

// InitialAdminPasswordCandidate 返回本进程登记的初始管理员口令
// 候选（无候选时为空串）。供 handler 的默认口令裁决点同口径核对。
func InitialAdminPasswordCandidate() string {
	return initialAdminCandidate
}

func setInitialAdminDataDir(dir string) {
	initialAdminDataDir = dir
}

// defaultInitialAdminDataDir 推导 PG 模式下的数据目录：DB_PATH
// 同级目录（与 config.FromEnv 的 DB_PATH 缺省 /app/data/gost.db
// 同口径；handler ghMirrorDir 已有同式先例）。
func defaultInitialAdminDataDir() string {
	if p := strings.TrimSpace(os.Getenv("DB_PATH")); p != "" {
		return filepath.Dir(p)
	}
	return "/app/data"
}

// resolveInitialAdminPassword 返回环境指定的初始口令；
// 环境值为空白时视为未指定。
func resolveInitialAdminPassword() (string, bool) {
	if v := os.Getenv(InitialAdminPasswordEnv); strings.TrimSpace(v) != "" {
		return v, true
	}
	return "", false
}

// initialAdminPasswordAlphabet 去除了易混字符（I/O/l/o/0/1）。
const initialAdminPasswordAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789"

func generateInitialAdminPassword() (string, error) {
	out := make([]byte, 0, initialAdminPasswordLength)
	buf := make([]byte, initialAdminPasswordLength*2)
	// 拒绝采样：只取 < 224（56*4）的字节后取模，消除取模偏差。
	for len(out) < initialAdminPasswordLength {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for _, b := range buf {
			if b >= 224 {
				continue
			}
			out = append(out, initialAdminPasswordAlphabet[int(b)%len(initialAdminPasswordAlphabet)])
			if len(out) == initialAdminPasswordLength {
				break
			}
		}
	}
	return string(out), nil
}

// tryWriteInitialAdminPasswordFile 把初始口令明文以 0600 原子
// 落盘到指定目录（tmp+rename，与导入前自动备份快照同式），
// 返回完整路径。
func tryWriteInitialAdminPasswordFile(dir, pwd string) (string, error) {
	path := filepath.Join(dir, initialAdminPasswordFileName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(pwd+"\n"), 0o600); err != nil {
		return "", err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return path, nil
}

// persistInitialAdminPasswordFile 按落点链逐点尝试落盘，首个
// 成功点即落点；返回落点完整路径与逐点失败记录（供日志点名）。
// 落点链为空（数据目录未配置）时直接返回失败记录一条。
func persistInitialAdminPasswordFile(pwd string) (string, []string) {
	dirs := initialAdminPasswordFileDirs()
	if len(dirs) == 0 {
		return "", []string{"no data directory configured for initial admin password file"}
	}
	var attempts []string
	for _, dir := range dirs {
		path, err := tryWriteInitialAdminPasswordFile(dir, pwd)
		if err == nil {
			return path, attempts
		}
		attempts = append(attempts, filepath.Join(dir, initialAdminPasswordFileName)+": "+err.Error())
	}
	return "", attempts
}

func registerInitialAdminCandidate(pwd, file string) {
	initialAdminCandidate = pwd
	initialAdminCandidateFile = file
}

// clearInitialAdminPassword 清除初始口令候选并尽力删除一次性
// 文件。管理员真实改密的仓储写点在成功后调用（仅 id=1）。
// 有登记落点时删该文件；无登记落点时按落点链清扫同名文件
// （FIX-CI-1：后备落点的文件同样须在改密后失效）。
func clearInitialAdminPassword() {
	had := initialAdminCandidate != ""
	initialAdminCandidate = ""
	initialAdminPendingWrite = false
	file := initialAdminCandidateFile
	initialAdminCandidateFile = ""
	var files []string
	if file != "" {
		files = []string{file}
	} else {
		for _, dir := range initialAdminPasswordFileDirs() {
			files = append(files, filepath.Join(dir, initialAdminPasswordFileName))
		}
	}
	for _, f := range files {
		if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
			log.Printf("INFO: remove initial admin password file %s failed: %v", f, err)
		} else if err == nil && had {
			log.Printf("INFO: initial admin password file %s removed after admin password change", f)
		}
	}
}

// seedInitialAdmin 补建 id=1 管理员（seedData 在「id=1 缺失且
// 用户名未被占用」时调用）。初始口令来源见文件头注：环境指定
// 优先，否则随机生成并经落点链落一次性文件再建行。落盘全链
// 失败不中止播种（FIX-CI-1）：照常建行、登记内存候选并置待
// 重试标，ERROR 日志点名逐点失败与恢复办法——落点推导错误是
// 永久性失败，旧「不建、下次启动重试」口径对此永不收敛（CI
// PG 契约与 PG 裸二进制首启砖化同根）。生成/哈希/落库失败仍
// 按既有口径中止并置 adminBootstrapRequired。
func seedInitialAdmin(db *gorm.DB) {
	initialPwd, fromEnv := resolveInitialAdminPassword()
	generated := false
	if !fromEnv {
		var err error
		initialPwd, err = generateInitialAdminPassword()
		if err != nil {
			adminBootstrapRequired = true
			log.Printf("ERROR: seed admin initial password generation failed: %v. 面板缺少管理员账号，需人工处理。", err)
			return
		}
		generated = true
	}
	var initialFile string
	persisted := !generated
	if generated {
		var attempts []string
		initialFile, attempts = persistInitialAdminPasswordFile(initialPwd)
		persisted = initialFile != ""
		if !persisted {
			log.Printf("ERROR: cannot write initial admin password file to any landing (%s); the admin account is still created and its initial password is registered in memory only — it cannot be read from disk. Make the panel data directory writable (or set %s) and restart to retry the write; if this process exits first, delete the seeded admin row (id=1) and restart. 初始管理员口令未落盘、管理员已建号，需人工处理。", strings.Join(attempts, "; "), InitialAdminPasswordEnv)
		}
	}
	adminPwd, err := security.HashPassword(initialPwd)
	if err != nil {
		// 与占用分支同标准：不得静默跳过。ERROR 日志 + API 状态可见。
		adminBootstrapRequired = true
		log.Printf("ERROR: seed admin password hash failed: %v. 面板缺少管理员账号，需人工处理。", err)
		if initialFile != "" {
			_ = os.Remove(initialFile)
		}
		return
	}
	adminUser := model.User{
		ID: 1, User: "admin@user", Pwd: adminPwd,
		RoleID: 0, ExpTime: 2727251700000, Flow: 99999, InFlow: 0, OutFlow: 0,
		FlowResetTime: 1, FlowLastResetTime: 1748914865000, Num: 99999, CreatedTime: 1748914865000,
		UpdatedTime:       sql.NullInt64{Int64: 1754011744252, Valid: true},
		Status:            1,
		PasswordChangedAt: 1748914865000,
	}
	if err := db.Create(&adminUser).Error; err != nil {
		// C1-02：与生成/落盘/哈希三失败支同标准——落库失败同样
		// 意味着面板无管理员，必须置位可观测标志，不得只记一行
		// 普通日志静默返回（否则状态面恒报无事可做）。
		adminBootstrapRequired = true
		log.Printf("ERROR: seed admin failed: %v. 面板缺少管理员账号，需人工处理。", mapUniqueViolation(err, "username"))
		if initialFile != "" {
			_ = os.Remove(initialFile)
		}
		return
	}
	registerInitialAdminCandidate(initialPwd, initialFile)
	if generated && !persisted {
		initialAdminPendingWrite = true
	}
	// INV-2（事故专项审计路 3）：播种成功必须可观测——旧
	// 实现补建成功零日志，已初始化部署被指向空库（卷漂
	// 移等）时在空库上静默播种、日志面无迹可循。此处只
	// 加日志、不改播种语义：点名触发谓词（当前打开的库
	// 中 id=1 的 user 计数为 0），事故定位时据此可知本
	// 进程面对的库里当时没有管理员。
	log.Printf("INFO: seeded default admin user %q (id=1): trigger predicate was user count for id=1 == 0 in the opened database; if this deployment was already initialized, the opened database is probably not the original one (check the mounted data volume)", adminUser.User)
	// R1-12-01：初始口令的取得路径必须在启动日志中点名（只记
	// 路径/来源，绝不记录口令本身）。
	if fromEnv {
		log.Printf("INFO: initial admin password for user %q was taken from the %s environment variable (not written to disk); it is valid for the first login only and must be changed there", adminUser.User, InitialAdminPasswordEnv)
	} else if persisted {
		log.Printf("INFO: initial admin password for user %q was generated and written to the one-time file %s (mode 0600, deleted automatically after the first successful password change); read it there for the first login", adminUser.User, initialFile)
	}
}

// probeInitialAdminPassword 在启动时恢复初始口令候选登记：
// 本进程已登记时候选跳过恢复，但若欠落盘（FIX-CI-1 待重试标）
// 先重试落点链落盘；未登记时按落点链逐点查一次性文件，内容与
// id=1 管理员的存储哈希吻合时登记之（改密跨重启仍被强制），
// 不吻合的文件是陈旧残留（口令已由其它路径变更），删除并留
// INFO 日志；文件皆不存在时回退核对环境指定值（环境播种路径
// 的跨重启恢复）。
func probeInitialAdminPassword(db *gorm.DB) {
	if initialAdminCandidate != "" {
		if initialAdminPendingWrite {
			if path, _ := persistInitialAdminPasswordFile(initialAdminCandidate); path != "" {
				initialAdminCandidateFile = path
				initialAdminPendingWrite = false
				log.Printf("INFO: initial admin password file write retry succeeded: %s (mode 0600, deleted automatically after the first successful password change)", path)
			}
		}
		return
	}
	var admin model.User
	if err := db.Where("id = ?", 1).First(&admin).Error; err != nil {
		return
	}
	for _, dir := range initialAdminPasswordFileDirs() {
		path := filepath.Join(dir, initialAdminPasswordFileName)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		pwd := strings.TrimSpace(string(data))
		if pwd == "" {
			continue
		}
		if ok, _ := security.VerifyPassword(admin.Pwd, pwd); ok {
			registerInitialAdminCandidate(pwd, path)
			return
		}
		if err := os.Remove(path); err == nil {
			log.Printf("INFO: removed stale initial admin password file %s: stored admin password no longer matches it", path)
		}
	}
	if pwd, fromEnv := resolveInitialAdminPassword(); fromEnv {
		if ok, _ := security.VerifyPassword(admin.Pwd, pwd); ok {
			registerInitialAdminCandidate(pwd, "")
		}
	}
}
