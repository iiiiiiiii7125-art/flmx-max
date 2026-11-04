package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	_ "net/http/pprof"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/go-gost/core/logger"
	xlogger "github.com/go-gost/x/logger"
	"github.com/go-gost/x/service"
	"github.com/go-gost/x/socket"
	"github.com/judwhite/go-svc"
)

type stringList []string

func (l *stringList) String() string {
	return fmt.Sprintf("%s", *l)
}
func (l *stringList) Set(value string) error {
	*l = append(*l, value)
	return nil
}

var (
	cfgFile      string
	outputFormat string
	services     stringList
	nodes        stringList
	debug        bool
	trace        bool
	apiAddr      string
	metricsAddr  string
)

func init() {
	log.SetFlags(log.LstdFlags | log.Lshortfile | log.Lmicroseconds)

	args := strings.Join(os.Args[1:], "  ")

	if strings.Contains(args, " -- ") {
		var (
			wg  sync.WaitGroup
			ret int
		)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		for wid, wargs := range strings.Split(" "+args+" ", " -- ") {
			wg.Add(1)
			go func(wid int, wargs string) {
				defer wg.Done()
				defer cancel()
				worker(wid, strings.Split(wargs, "  "), &ctx, &ret)
			}(wid, strings.TrimSpace(wargs))
		}

		wg.Wait()

		os.Exit(ret)
	}
}

func worker(id int, args []string, ctx *context.Context, ret *int) {
	cmd := exec.CommandContext(*ctx, os.Args[0], args...)

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), fmt.Sprintf("_GOST_ID=%d", id))

	if err := cmd.Run(); err != nil {
		log.Fatal(err)
	}
	if cmd.ProcessState.Exited() {
		*ret = cmd.ProcessState.ExitCode()
	}
}

var printVersion bool

func init() {
	// Registration only — parsing lives in main (parseCommandLine).
	// The flag set, defaults and usages are exactly the ones the
	// agent has always taken (pinned by TestAgentFlagSetAndDefaults).
	flag.Var(&services, "L", "service list")
	flag.Var(&nodes, "F", "chain node list")
	flag.StringVar(&cfgFile, "C", "", "configuration file")
	flag.BoolVar(&printVersion, "V", false, "print version")
	flag.StringVar(&outputFormat, "O", "", "output format, one of yaml|json format")
	flag.BoolVar(&debug, "D", false, "debug mode")
	flag.BoolVar(&trace, "DD", false, "trace mode")
	flag.StringVar(&apiAddr, "api", "", "api service address")
	flag.StringVar(&metricsAddr, "metrics", "", "metrics service address")
}

// parseCommandLine parses the flags registered in init and reports
// whether -V asked for the version banner, in which case the
// caller exits before doing anything else.
//
// Parsing runs from main, not from the init above (R2-013 / BD-03):
// flag.Parse in init kills any test binary for this package — the
// -test.* flags are not registered yet at init time — which left
// the root package with no program-level tests at all. Registering
// in init is harmless; parsing is the part that had to move. The
// worker multiplexing in the other init is unaffected: forked
// workers land here in their own main and parse their sub-args.
func parseCommandLine() bool {
	flag.Parse()

	if printVersion {
		fmt.Fprintf(os.Stdout, "gost %s (%s %s/%s)\n",
			version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
		return true
	}
	return false
}

// R17-C (R17-X-01): startup failures must stay diagnosable even when the
// service manager discards stdout/stderr (the install.sh unit used to set
// StandardOutput=null/StandardError=null, so a poisoned gost.json killed
// the agent with no retrievable reason). Append one timestamped line to
// agent-startup.log in the executable's directory — the same directory as
// agent-upgrade.log — before exiting. Failures of this write itself must
// never mask the startup error, so they only go to stderr.
func logStartupFailure(stage string, err error) {
	exe, exeErr := os.Executable()
	dir := "."
	if exeErr == nil {
		dir = filepath.Dir(exe)
	}
	path := filepath.Join(dir, "agent-startup.log")
	f, ferr := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if ferr != nil {
		fmt.Fprintf(os.Stderr, "agent: cannot write startup log %s: %v (startup error: %v)\n", path, ferr, err)
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s agent startup failed [%s]: %v\n", time.Now().Format(time.RFC3339), stage, err)
}

func main() {
	if parseCommandLine() {
		return
	}

	// Core dump hygiene (coredump.go): never produce core files, and
	// sweep any stockpile left in the data directories by earlier
	// crashes before doing anything else.
	sweepCoreDumps()

	// 已被面板卸载（容器形态留有标记）：配置已清除，先于配置加载
	// 拦截，空转等待人工删除容器。
	if socket.AgentUninstalled() {
		idleAfterUninstall()
		return
	}

	// 加载配置文件
	config, err := LoadConfig("config.json")
	if err != nil {
		logStartupFailure("load-config", err)
		fmt.Printf("❌ 配置加载失败: %v\n", err)
		fmt.Println("请确保当前目录存在 config.json 文件")
		os.Exit(1)
	}

	fmt.Printf("✅ 配置加载成功 - addr: %s\n", config.Addr)

	// 设置运行时配置持久化路径
	socket.SetConfigPersistPath("gost.json")
	// 启用持久化将在 program.Start() 后开启，避免启动加载阶段触发冗余写入

	log := xlogger.NewLogger()
	logger.SetDefault(log)

	distro := socket.DetectDistro()
	fullVersion := fmt.Sprintf("%s (%s/%s)", version, distro, runtime.GOARCH)
	service.SetHTTPReportURL(config.Addr, config.Secret)
	// 遗留接线（F-005 历史口径）：seq 路径仍按 config.json 同目录注入，
	// 但 seq 状态的生产消费端已死——生产防漏计由下方 spool v2 承担，
	// seq 文件生产中从不被写入或读取；保留接线仅因该族尚未整体删除
	// （见 x/service/traffic_seq.go 文件头登记）。
	if abs, err := filepath.Abs("config.json"); err == nil {
		dir := filepath.Dir(abs)
		service.SetSeqFilePath(filepath.Join(dir, "flmx_agent_seq.json"))
		// 可靠上报 spool 与 seq 文件同级落盘，按「归一化面板地址
		// ＋节点 secret」身份隔离选文件（TrafficSpoolIdentity：同
		// 面板改地址写法不换文件，换面板/换 secret 才换；遗留的
		// 其他身份 spool 会被点名告警不自动处置）。初始化失败
		// （存量 spool 损坏等）拒绝启动：静默换新 spool 会无痕
		// 丢掉未确认流量。
		if err := service.ConfigureTrafficSpool(dir, service.TrafficSpoolIdentity(config.Addr, config.Secret)); err != nil {
			fmt.Printf("❌ 流量 spool 初始化失败，拒绝启动: %v\n", err)
			os.Exit(1)
		}
	}

	p := &program{
		startReporter: func() reporter {
			return socket.StartWebSocketReporterWithConfig(config.Addr, config.Secret, config.Http, config.Tls, config.Socks, fullVersion)
		},
	}
	if err := svc.Run(p); err != nil {
		logStartupFailure("start", err)
		logger.Default().Fatal(err)
	}
}

// GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o gost
// upx --best --lzma gost
