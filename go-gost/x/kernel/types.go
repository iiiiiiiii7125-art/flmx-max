// Package kernel runs third-party proxy kernels (Xray, sing-box, ...)
// next to the agent. The panel renders every configuration file; this package
// only installs binaries, writes files, supervises processes and counts
// per-inbound traffic with nftables.
package kernel

import (
	"fmt"
	"net"
	"path/filepath"
	"regexp"
	"strings"
)

// Download describes how to fetch one binary. Integrity is mandatory: the
// digest comes from SHA256 (pinned, keyed by the mapped arch), a checksum file,
// or the GitHub release asset digest; without one the install is refused.
type Download struct {
	URL         string            `json:"url"`
	ChecksumURL string            `json:"checksumUrl"`
	Archive     string            `json:"archive"` // zip | tar.gz | raw
	BinName     string            `json:"binName"`
	ArchMap     map[string]string `json:"archMap"`
	SHA256      map[string]string `json:"sha256,omitempty"`
	GitHub      *GitHubRelease    `json:"github,omitempty"`
}

// GitHubRelease lets the agent resolve a release itself when the panel could
// not (URL still holds {VERSION}, or no digest for this arch).
type GitHubRelease struct {
	Repo  string `json:"repo"`  // owner/name
	Tag   string `json:"tag"`   // "" = latest
	Asset string `json:"asset"` // file name template with {VERSION} and {ARCH}
	Proxy string `json:"proxy"` // retired (KU4, 1.7.7): parsed for wire compatibility but ignored — download routing is probe-selected locally from built-in candidates
}

// ExtraBinary is a helper program a kernel needs next to its main binary.
type ExtraBinary struct {
	Name     string    `json:"name"`
	Version  string    `json:"version"`
	Download *Download `json:"download"`
}

// ProcessSpec is one supervised process of a kernel. Bin is "" for the main
// binary or the Name of an extra binary.
type ProcessSpec struct {
	Name string   `json:"name"`
	Bin  string   `json:"bin"`
	Args []string `json:"args"`
}

type File struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

type Port struct {
	InboundID int64  `json:"inboundId"`
	Port      int    `json:"port"`
	Network   string `json:"network"` // "tcp", "udp" or "tcp,udp"
	// Bind is the inbound's listen address (FIX-031): the nft counter
	// rules for this port match only traffic to/from it, so packets to
	// the same port on another local address are not attributed to
	// this inbound. Empty or a wildcard ("0.0.0.0", "::") means the
	// inbound listens everywhere and the rules stay unconditioned.
	Bind string `json:"bind,omitempty"`
	// Allow, when non-empty, restricts the port to these source CIDRs;
	// every other packet to it is dropped. Absent or empty means no
	// restriction (both ends of the guard chain key on len > 0).
	Allow []string `json:"allow,omitempty"`
	// HopFrom..HopTo (UDP) are redirected to Port: Hysteria2 port hopping.
	HopFrom int `json:"hopFrom,omitempty"`
	HopTo   int `json:"hopTo,omitempty"`
}

// ConnlogEntry is one kernel inbound the panel marks as a direct
// connection-log entry: the inbound itself is where end clients
// enter this node (direct inbound, or the source of a chain), so
// the kernel log collector (internal/connlog) may record its
// connections under Tag. Inbounds absent from ApplyRequest's list —
// relay landings, metering companions — are not collection sources.
type ConnlogEntry struct {
	InboundID int64  `json:"inboundId"`
	Tag       string `json:"tag"`
}

// ApplyRequest is the KernelApply command body.
type ApplyRequest struct {
	Type     string    `json:"type"`
	Version  string    `json:"version"`
	Download *Download `json:"download"`
	Files    []File    `json:"files"`
	Args     []string  `json:"args"`
	// Processes, when set, replaces Args with several processes (a
	// per-inbound kernel runs one process per inbound).
	Processes []ProcessSpec `json:"processes,omitempty"`
	Extras    []ExtraBinary `json:"extras,omitempty"`
	Hash      string        `json:"hash"`
	Enabled   bool          `json:"enabled"`
	Ports     []Port        `json:"ports"`
	// UserMarks are subscription users whose connections the kernel marks
	// with userMark(id); their traffic is counted per user.
	UserMarks []int64 `json:"userMarks,omitempty"`
	// UserMarkBase is the socket-mark base user marks are minted with
	// (F2-DBL-01, per-agent: 0x47000000 | agentByte<<16, agentByte =
	// the first byte of SHA-256("flmx:user-mark-v2:"+node secret)
	// since E-01 — the 0x47 tag keeps the per-agent space disjoint
	// from the legacy base, F-R4-01). The panel renders the same
	// base into the kernel config and sends it here so the agent's
	// nft rules match the config's marks. 0 means the panel did not
	// send one — the legacy base (0x46560000) stays in force.
	UserMarkBase int64 `json:"userMarkBase,omitempty"`
	// GeoData are data files kept current next to the main binary (Xray's
	// geosite.dat / geoip.dat).
	GeoData []GeoFile `json:"geoData,omitempty"`
	// ConnlogEntries are the inbounds marked as direct connection-log
	// entries (see ConnlogEntry): the panel's topology qualification
	// for kernel collection. The panel always sends the key — an
	// empty list clears any previously applied marks.
	ConnlogEntries []ConnlogEntry `json:"connlogEntries,omitempty"`
	// ConnLogInbounds is the per-inbound opt-in list for connection log
	// collection (design §4.5): the ids of the direct inbounds rendered
	// into this kernel whose connection events the agent collects from
	// the kernel's logs. The panel (unit U5) fills it; empty means
	// collection is off for this kernel. It is a rendering-level fact,
	// so it rides the Apply request rather than a side channel.
	// Collection is effective only for ids present in BOTH lists
	// (opt-in ∩ topology qualification), resolved in applyConnLog.
	ConnLogInbounds []int64 `json:"connLogInbounds,omitempty"`
}

type State struct {
	Type       string   `json:"type"`
	Running    bool     `json:"running"`
	Enabled    bool     `json:"enabled"`
	Pid        int      `json:"pid"`
	Version    string   `json:"version"`
	ConfigHash string   `json:"configHash"`
	Error      string   `json:"error"`
	Restarts   int      `json:"restarts"`
	StartedAt  int64    `json:"startedAt"`
	Logs       []string `json:"logs,omitempty"`
}

type Counter struct {
	InboundID int64  `json:"inboundId"`
	Proto     string `json:"proto"`
	Dir       string `json:"dir"` // "in" = client upload, "out" = client download
	// Packets is the packet count behind Bytes (FIX-029: the panel
	// needs it to strip per-packet headers off realm nft counters).
	// Zero on agents that predate the field; the panel treats zero
	// packets with nonzero bytes as "no packet data" and passes bytes
	// through unconverted.
	Packets int64 `json:"packets,omitempty"`
	Bytes   int64 `json:"bytes"`
}

// UserCounter is one user's traffic in one direction ("up" = the user's
// upload, "down" = download) since the counters' epoch.
type UserCounter struct {
	ID    int64  `json:"id"`
	Dir   string `json:"dir"`
	Bytes int64  `json:"bytes"`
}

type Status struct {
	Kernels      []State       `json:"kernels"`
	Counters     []Counter     `json:"counters"`
	Users        []UserCounter `json:"users"`
	Epoch        string        `json:"epoch"`
	NftAvailable bool          `json:"nftAvailable"`
	NftError     string        `json:"nftError,omitempty"`
	// NftReadError 承载最近一次 nft 计数读取失败的原因（R3A-NEW-1：
	// 表被删/不可读时 read() 出错）。与 NftError（自动安装失败）
	// 分开：读错必须随每次上报带给面板置 failing＋告警，而安装
	// 失败是另一回事；空表示最近一次读取成功（或无端口可读）。
	NftReadError string `json:"nftReadError,omitempty"`
	// NftAutoInstalled 表示本 agent 进程曾为白名单自动装上
	// nftables（面板据此在节点页展示“已自动安装”，区别于装机
	// 自带的“已安装”）。NftError 只承载最近一次自动安装失败
	// 的原因——nft 单纯缺失（未尝试安装，如无白名单节点）不进
	// 此字段，面板不得把它读成“安装失败”。
	NftAutoInstalled bool `json:"nftAutoInstalled,omitempty"`
	// MissingLogsFor names the kernel type a logs query asked for
	// that has no instance on this node (F2-23). Kernels lists
	// only installed kernels, so without this field "no such
	// kernel" and "kernel present but no logs yet" are
	// indistinguishable downstream. Only ever set when the caller
	// asked for one type's logs (logsFor != ""); the periodic poll
	// asks for none and never sees it.
	MissingLogsFor string `json:"missingLogsFor,omitempty"`
}

type adapter struct {
	versionArgs []string
	// fixFile adjusts a rendered file to this host before it is written.
	fixFile func(name, content string) string
}

var adapters = map[string]adapter{
	"xray":    {versionArgs: []string{"version"}},
	"singbox": {versionArgs: []string{"version"}},
	// singbox-companion runs the metering companions (C-03) as a
	// second sing-box instance: same binary, separate instance key,
	// so a companion Apply never restarts the main instance.
	"singbox-companion": {versionArgs: []string{"version"}},
	// realm runs the forwards of realm-engine tunnels.
	"realm": {versionArgs: []string{"-v"}, fixFile: realmFixFile},
}

// realmFixFile listens on IPv4 only on hosts without IPv6 sockets: realm,
// unlike Go, cannot bind [::] there.
func realmFixFile(name, content string) string {
	if name != "config.toml" || ipv6Sockets() {
		return content
	}
	return strings.ReplaceAll(content, `listen = "[::]:`, `listen = "0.0.0.0:`)
}

var ipv6Sockets = func() bool {
	c, err := net.ListenPacket("udp6", "[::]:0")
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

var (
	safeNamePattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	safeVersionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,39}$`)
)

func safeName(name string) bool {
	return safeNamePattern.MatchString(name) && !strings.Contains(name, "..")
}

func (r *ApplyRequest) validate() error {
	if _, ok := adapters[r.Type]; !ok {
		return fmt.Errorf("不支持的内核类型: %s", r.Type)
	}
	if r.Version == "" {
		r.Version = "latest"
	}
	if !safeVersionPattern.MatchString(r.Version) || strings.Contains(r.Version, "..") {
		return fmt.Errorf("版本号格式错误: %s", r.Version)
	}
	if err := r.Download.validate(); err != nil {
		return err
	}
	extras := make(map[string]bool, len(r.Extras))
	for _, e := range r.Extras {
		if !safeName(e.Name) || !safeVersionPattern.MatchString(e.Version) || strings.Contains(e.Version, "..") {
			return fmt.Errorf("非法附加程序: %s", e.Name)
		}
		if err := e.Download.validate(); err != nil {
			return err
		}
		extras[e.Name] = true
	}
	seen := make(map[string]bool, len(r.Processes))
	for _, p := range r.Processes {
		if !safeName(p.Name) || seen[p.Name] {
			return fmt.Errorf("非法进程名: %s", p.Name)
		}
		seen[p.Name] = true
		if p.Bin != "" && !extras[p.Bin] {
			return fmt.Errorf("进程 %s 引用了未声明的程序 %s", p.Name, p.Bin)
		}
	}
	for _, f := range r.Files {
		if !safeName(f.Name) || filepath.Base(f.Name) != f.Name {
			return fmt.Errorf("非法配置文件名: %s", f.Name)
		}
	}
	for _, g := range r.GeoData {
		if err := g.validate(); err != nil {
			return err
		}
	}
	for _, id := range r.UserMarks {
		if id <= 0 || id > userMarkMax {
			return fmt.Errorf("非法用户标记: %d", id)
		}
	}
	// F2-DBL-01：base 只许是合法布局（高字节 0x46 legacy 或 0x47
	// per-agent、低 16 位全 0，含 legacy 0x46560000 与 per-agent
	// 0x47aaxxxx）；0 表示旧面板未下发、用 legacy。其他值是面板
	// bug，不得静默接受（mark 错位会让用户计数归零或串户）。
	// F-R4-01：0x47 是 per-agent 专属 tag——0x46 的 per-agent
	// 空间本身含 legacy 基址（节点 id ≡ 86 时恰为 0x46560000），
	// 旧 tag 的 per-agent 基址仍放行（老面板/未升级 agent 的
	// 过渡态），但面板新渲染一律用 0x47。
	if r.UserMarkBase != 0 && ((r.UserMarkBase>>24 != 0x46 && r.UserMarkBase>>24 != 0x47) || r.UserMarkBase&userMarkMax != 0) {
		return fmt.Errorf("非法用户标记基址: 0x%x", r.UserMarkBase)
	}
	for _, id := range r.ConnLogInbounds {
		if id <= 0 {
			return fmt.Errorf("非法连接日志入站: %d", id)
		}
	}
	return r.validatePorts()
}

func (r *ApplyRequest) validatePorts() error {
	for _, p := range r.Ports {
		if p.Port < 1 || p.Port > 65535 || p.InboundID <= 0 {
			return fmt.Errorf("非法端口: %d", p.Port)
		}
		if p.HopFrom != 0 || p.HopTo != 0 {
			if p.HopFrom < 1 || p.HopTo > 65535 || p.HopFrom >= p.HopTo || (p.Port >= p.HopFrom && p.Port <= p.HopTo) {
				return fmt.Errorf("非法端口跳跃范围: %d-%d", p.HopFrom, p.HopTo)
			}
		}
		if len(p.Allow) > 512 {
			return fmt.Errorf("白名单条目过多")
		}
		for _, c := range p.Allow {
			if _, _, err := net.ParseCIDR(c); err != nil {
				return fmt.Errorf("非法白名单条目: %q", c)
			}
		}
	}
	return nil
}

func (d *Download) validate() error {
	if d == nil || !safeName(d.BinName) {
		return fmt.Errorf("缺少下载信息")
	}
	switch d.Archive {
	case "zip", "tar.gz", "raw":
	default:
		return fmt.Errorf("不支持的压缩格式: %s", d.Archive)
	}
	for arch, sum := range d.SHA256 {
		if !sha256HexPattern.MatchString(sum) || len(sum) != 64 || !safeName(arch) {
			return fmt.Errorf("非法校验值")
		}
	}
	if g := d.GitHub; g != nil {
		if !githubRepoPattern.MatchString(g.Repo) || (g.Tag != "" && !safeVersionPattern.MatchString(strings.TrimPrefix(g.Tag, "v"))) {
			return fmt.Errorf("非法 GitHub 发布信息")
		}
	}
	return nil
}

var githubRepoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// processSpecs returns the processes to run, falling back to the single
// main-binary process described by Args.
func (r ApplyRequest) processSpecs() []ProcessSpec {
	if len(r.Processes) > 0 {
		return r.Processes
	}
	return []ProcessSpec{{Name: r.Type, Args: r.Args}}
}
