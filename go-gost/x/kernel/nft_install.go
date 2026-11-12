package kernel

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"
)

// nftables 自动安装：含源白名单的内核下发前，agent 自检 nft 是否
// 可用；缺失时按本机发行版自动安装，装不上才明错 fail-closed，
// 不再把“手动装 nft”留给用户。
//
// 触发面唯一：nftCounters.sync 在 ports 含白名单且 nft 缺失时调用
// 本文件入口；无白名单时计数是 best-effort，不装（沿用旧行为）。
// 安装只在 nft 缺失时发生一次——装好后 LookPath 即命中，后续同步
// 不再尝试；失败时下一次下发会重试（面板 drift 轮询会重推）。

// nftInstallTimeout 是单次安装尝试的总时限（update + install）。
// 与面板的内核下发超时（5 分钟）同量级：慢速安装不应跑赢面板正
// 在等的那次下发。
const nftInstallTimeout = 5 * time.Minute

// osReleasePath 是发行版身份来源（/etc/os-release），测试可指向
// 临时文件。
var osReleasePath = "/etc/os-release"

// installRunner 执行一条包管理器命令。变量形态与 nftRunner 同款，
// 供测试注入假命令——测试永不触碰真实包管理器。
var installRunner = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	// Stdin 为 nil 时进程读 /dev/null：包管理器不得因交互提问挂住。
	cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return out, fmt.Errorf("%s %s：%s", name, strings.Join(args, " "), truncateNftErr(msg))
	}
	return out, nil
}

// packageManagerLookPath 定位包管理器（dnf 有无决定 RHEL 系走
// dnf 还是 yum）。变量形态供测试模拟“有 dnf / 无 dnf”。
var packageManagerLookPath = exec.LookPath

// distroID 读 /etc/os-release 的 ID（如 "ubuntu"、"debian"、
// "centos"、"alpine"）；读不到返回 ""。
func distroID() string {
	b, err := os.ReadFile(osReleasePath)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if strings.TrimSpace(k) != "ID" {
			continue
		}
		return strings.ToLower(strings.Trim(strings.TrimSpace(v), `"'`))
	}
	return ""
}

// detectDistroFamily 把发行版映射到包管理器族："apt"（Debian 系）、
// "dnf"（RHEL 系）、"apk"（Alpine）。ID 与 ID_LIKE 都认（派生版只
// 写 ID_LIKE，如 Ubuntu 派生版）；三族之外返回 ""。
func detectDistroFamily() string {
	b, err := os.ReadFile(osReleasePath)
	if err != nil {
		return ""
	}
	var id string
	var like []string
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.ToLower(strings.Trim(strings.TrimSpace(v), `"'`))
		switch strings.TrimSpace(k) {
		case "ID":
			id = v
		case "ID_LIKE":
			like = strings.Fields(v)
		}
	}
	match := func(want ...string) bool {
		for _, w := range want {
			if id == w {
				return true
			}
			for _, l := range like {
				if l == w {
					return true
				}
			}
		}
		return false
	}
	switch {
	case match("debian"):
		return "apt"
	case match("rhel", "fedora", "centos"):
		return "dnf"
	case match("alpine"):
		return "apk"
	default:
		return ""
	}
}

// runNftInstall 按族执行安装。update 类步骤失败只记日志、继续装
// （索引陈旧也可能装得上，失败原因以 install 本身的报错为准）；
// install 失败直接返回，错误里带命令与 stderr 尾，供面板原样展示。
func runNftInstall(ctx context.Context, family string) error {
	switch family {
	case "apt":
		if _, err := installRunner(ctx, "apt-get", "update"); err != nil {
			log.Printf("WARN: nftables 自动安装：apt-get update 失败（%v），继续尝试安装", err)
		}
		_, err := installRunner(ctx, "apt-get", "install", "-y", "nftables")
		return err
	case "dnf":
		mgr := "dnf"
		if _, err := packageManagerLookPath("dnf"); err != nil {
			if _, err := packageManagerLookPath("yum"); err != nil {
				return fmt.Errorf("未找到 dnf/yum，无法安装 nftables")
			}
			mgr = "yum"
		}
		_, err := installRunner(ctx, mgr, "install", "-y", "nftables")
		return err
	case "apk":
		if _, err := installRunner(ctx, "apk", "update"); err != nil {
			log.Printf("WARN: nftables 自动安装：apk update 失败（%v），继续尝试安装", err)
		}
		_, err := installRunner(ctx, "apk", "add", "nftables")
		return err
	default:
		return fmt.Errorf("未知的包管理器族 %q", family)
	}
}

// nftEnsureInstalled 在 nft 缺失时尝试自动安装。返回值 installed
// 表示本次调用真实装上了包（供状态上报区分“已安装”与“已自动安
// 装”）；nft 已在时返回 (false, nil)，不触碰包管理器。安装失败
// 的错误必含 "nftables" 与失败原因，面板原样展示。
func nftEnsureInstalled(ctx context.Context) (installed bool, err error) {
	if nftAvailable() == nil {
		return false, nil
	}
	family := detectDistroFamily()
	if family == "" {
		id := distroID()
		if id == "" {
			id = "未知（无法读取 /etc/os-release）"
		}
		return false, fmt.Errorf("nftables 自动安装失败：未知发行版 %q，无法确定包管理器", id)
	}
	if err := runNftInstall(ctx, family); err != nil {
		return false, fmt.Errorf("nftables 自动安装失败：%v", err)
	}
	if nftAvailable() != nil {
		return false, fmt.Errorf("nftables 自动安装失败：包管理器已退出，但 nft 命令仍不可用")
	}
	return true, nil
}
