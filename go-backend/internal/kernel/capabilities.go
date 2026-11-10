// Package kernel turns panel-side inbound definitions into proxy-kernel
// configuration (Xray, sing-box, ...). Everything here is pure so it can
// be covered by golden tests; the HTTP layer only persists and dispatches.
package kernel

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	TypeXray    = "xray"
	TypeSingbox = "singbox"
)

const (
	ProtocolVLESS       = "vless"
	ProtocolVMess       = "vmess"
	ProtocolTrojan      = "trojan"
	ProtocolShadowsocks = "shadowsocks"
	ProtocolHysteria2   = "hysteria2"
	ProtocolTUIC        = "tuic"
	ProtocolAnyTLS      = "anytls"
	ProtocolSnell       = "snell"
	ProtocolNaive       = "naive"
)

const (
	TransportTCP   = "tcp"
	TransportWS    = "ws"
	TransportGRPC  = "grpc"
	TransportXHTTP = "xhttp"
	TransportQUIC  = "quic"
)

const (
	SecurityNone      = "none"
	SecurityTLS       = "tls"
	SecurityReality   = "reality"
	SecurityShadowTLS = "shadowtls"
)

// TransportCap lists which security layers a transport accepts for a protocol.
type TransportCap struct {
	Type       string   `json:"type"`
	Securities []string `json:"securities"`
}

type ProtocolCap struct {
	Protocol   string         `json:"protocol"`
	Transports []TransportCap `json:"transports"`
}

// Download describes where an agent fetches the kernel binary. {VERSION} and
// {ARCH} are substituted by the agent; ArchMap translates GOARCH to the
// release asset naming of the kernel project.
type Download struct {
	URL         string            `json:"url"`
	LatestURL   string            `json:"latestUrl"`
	ChecksumURL string            `json:"checksumUrl"`
	LatestSum   string            `json:"latestChecksumUrl"`
	Archive     string            `json:"archive"`
	BinName     string            `json:"binName"`
	ArchMap     map[string]string `json:"archMap"`
	// Pinned SHA-256 per version and mapped arch, for projects that publish
	// no checksum files. Only these versions install.
	Pinned map[string]map[string]string `json:"-"`
	// GitHub repo + asset template for digest lookup ("" when unused).
	GitHubRepo  string `json:"-"`
	GitHubAsset string `json:"-"`
	// Mirrorable: this download may go through a GitHub mirror prefix
	// (the agent's built-in candidate routes since 1.7.7; the panel's
	// removed github_proxy_url setting no longer feeds this).
	Mirrorable bool `json:"-"`
}

type Info struct {
	Type        string        `json:"type"`
	Name        string        `json:"name"`
	Implemented bool          `json:"implemented"`
	Protocols   []ProtocolCap `json:"protocols"`
	// Versions, when set, is the closed list of installable versions (the
	// first is the default); otherwise any version or "latest" is allowed.
	Versions []string `json:"versions,omitempty"`
	// ReleaseRepo is the GitHub repository whose releases list the
	// installable versions when Versions is empty.
	ReleaseRepo string `json:"-"`
	// CertModes lists the TLS certificate sources the kernel supports.
	CertModes []string  `json:"certModes"`
	Download  *Download `json:"-"`
	// Relay kernels run forwards, not inbounds: the panel installs them
	// itself and the UI keeps them out of the add-kernel list.
	Relay bool `json:"relay,omitempty"`
}

// Xray gets the sing-box treatment (C2-02): the integrity anchor is the
// GitHub release API's asset digest, not the .dgst checksum file served
// from the same release as the binary. A co-located checksum file is a
// single self-consistent anchor — one route serving a fake (binary,
// .dgst) pair installs whenever the agent's cross-route corroboration
// degrades to that route. The API digest is corroborated cross-route
// by the agent and a release without a digest fails closed.
var xrayDownload = &Download{
	URL:         "https://github.com/XTLS/Xray-core/releases/download/v{VERSION}/Xray-linux-{ARCH}.zip",
	Archive:     "zip",
	BinName:     "xray",
	ArchMap:     map[string]string{"amd64": "64", "arm64": "arm64-v8a", "386": "32", "arm": "arm32-v7a"},
	GitHubRepo:  "XTLS/Xray-core",
	GitHubAsset: "Xray-linux-{ARCH}.zip",
	Mirrorable:  true,
}

// sing-box publishes no checksum files; the agent verifies against the
// GitHub release asset digest.
var singboxDownload = &Download{
	URL:         "https://github.com/SagerNet/sing-box/releases/download/v{VERSION}/sing-box-{VERSION}-linux-{ARCH}.tar.gz",
	Archive:     "tar.gz",
	BinName:     "sing-box",
	ArchMap:     map[string]string{"amd64": "amd64", "arm64": "arm64", "arm": "armv7", "386": "386"},
	GitHubRepo:  "SagerNet/sing-box",
	GitHubAsset: "sing-box-{VERSION}-linux-{ARCH}.tar.gz",
	Mirrorable:  true,
}

var catalog = []Info{
	{
		Type:        TypeXray,
		Name:        "Xray",
		Implemented: true,
		ReleaseRepo: "XTLS/Xray-core",
		CertModes:   []string{"self", "file"},
		Download:    xrayDownload,
		Protocols: []ProtocolCap{
			{Protocol: ProtocolVLESS, Transports: []TransportCap{
				{Type: TransportTCP, Securities: []string{SecurityReality, SecurityTLS, SecurityNone}},
				{Type: TransportXHTTP, Securities: []string{SecurityReality, SecurityTLS, SecurityNone}},
				{Type: TransportGRPC, Securities: []string{SecurityReality, SecurityTLS, SecurityNone}},
				{Type: TransportWS, Securities: []string{SecurityTLS, SecurityNone}},
			}},
			{Protocol: ProtocolVMess, Transports: []TransportCap{
				{Type: TransportTCP, Securities: []string{SecurityTLS, SecurityNone}},
				{Type: TransportWS, Securities: []string{SecurityTLS, SecurityNone}},
				{Type: TransportGRPC, Securities: []string{SecurityTLS, SecurityNone}},
			}},
			{Protocol: ProtocolTrojan, Transports: []TransportCap{
				{Type: TransportTCP, Securities: []string{SecurityTLS, SecurityReality}},
				{Type: TransportWS, Securities: []string{SecurityTLS}},
				{Type: TransportGRPC, Securities: []string{SecurityTLS, SecurityReality}},
			}},
			{Protocol: ProtocolShadowsocks, Transports: []TransportCap{
				{Type: TransportTCP, Securities: []string{SecurityNone}},
			}},
		},
	},
	{
		Type:        TypeSingbox,
		Name:        "sing-box",
		Implemented: true,
		ReleaseRepo: "SagerNet/sing-box",
		CertModes:   []string{"self", "file", "acme"},
		Download:    singboxDownload,
		Protocols: []ProtocolCap{
			{Protocol: ProtocolHysteria2, Transports: []TransportCap{{Type: TransportQUIC, Securities: []string{SecurityTLS}}}},
			{Protocol: ProtocolTUIC, Transports: []TransportCap{{Type: TransportQUIC, Securities: []string{SecurityTLS}}}},
			{Protocol: ProtocolAnyTLS, Transports: []TransportCap{{Type: TransportTCP, Securities: []string{SecurityTLS}}}},
			{Protocol: ProtocolNaive, Transports: []TransportCap{{Type: TransportTCP, Securities: []string{SecurityTLS}}}},
			{Protocol: ProtocolVLESS, Transports: []TransportCap{
				{Type: TransportTCP, Securities: []string{SecurityReality, SecurityTLS, SecurityNone}},
				{Type: TransportWS, Securities: []string{SecurityTLS, SecurityNone}},
				{Type: TransportGRPC, Securities: []string{SecurityTLS, SecurityNone, SecurityReality}},
			}},
			{Protocol: ProtocolVMess, Transports: []TransportCap{
				{Type: TransportTCP, Securities: []string{SecurityTLS, SecurityNone}},
				{Type: TransportWS, Securities: []string{SecurityTLS, SecurityNone}},
				{Type: TransportGRPC, Securities: []string{SecurityTLS, SecurityNone}},
			}},
			{Protocol: ProtocolTrojan, Transports: []TransportCap{
				{Type: TransportTCP, Securities: []string{SecurityTLS, SecurityReality}},
				{Type: TransportWS, Securities: []string{SecurityTLS}},
				{Type: TransportGRPC, Securities: []string{SecurityTLS, SecurityReality}},
			}},
			{Protocol: ProtocolShadowsocks, Transports: []TransportCap{
				{Type: TransportTCP, Securities: []string{SecurityNone, SecurityShadowTLS}},
			}},
		},
	},
	{
		Type:        TypeRealm,
		Name:        "realm",
		Implemented: true,
		CertModes:   []string{},
		Download:    realmDownload,
		Versions:    []string{RealmVersion},
		Protocols:   []ProtocolCap{},
		Relay:       true,
	},
}

// Catalog returns every known kernel; unimplemented ones are listed so the UI
// can show them as upcoming.
func Catalog() []Info {
	out := make([]Info, len(catalog))
	copy(out, catalog)
	return out
}

// normalizeKernelType folds user-supplied kernel names onto the catalog
// key space: lower-case, trim, and drop hyphens. The catalog display
// name for sing-box is "sing-box" while its Type is "singbox", so the
// alias is the name every operator naturally types (M-0001). Before
// this, the alias failed three faces with three different wordings:
// /kernel/versions said "未知内核", inbound creation said "请先在该
// 节点安装内核" (misleading — the kernel was installed), and
// Normalize said "暂不支持内核 sing-box".
func normalizeKernelType(kernelType string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(kernelType)), "-", "")
}

// CanonicalKernelType returns the catalog Type for a kernel name, or
// the normalized input when it names no known kernel, so DB lookups
// keep today's not-found behaviour for genuinely unknown names.
func CanonicalKernelType(kernelType string) string {
	if info, ok := Lookup(kernelType); ok {
		return info.Type
	}
	return normalizeKernelType(kernelType)
}

func Lookup(kernelType string) (Info, bool) {
	kernelType = normalizeKernelType(kernelType)
	for _, info := range catalog {
		if info.Type == kernelType {
			return info, true
		}
	}
	return Info{}, false
}

// ComboSupported 只读判断 (协议, 传输, 安全层) 是否在该内核的 catalog 内，
// 供下发前复检使用：历史脏数据不得在渲染层被静默降级，必须整单拒绝并报错。
// transport 为空按 Normalize 的缺省（有 tcp 用 tcp、否则该协议的首个
// 传输，W3/P-001 两面同口径）。security 为空则不豁免、直接判不支持
// （W3/P-002）：合法存量行的安全层恒由 Normalize 填充，空值只可能是
// 毒行，而渲染面按字面消费空值（如 trojan 空安全层渲染为无 TLS、
// 弱于界面所示），复检面若放行即等于背书该降级——拒绝由调用方点名
// 整单报错。
func ComboSupported(kernelType, protocol, transport, security string) bool {
	info, ok := Lookup(kernelType)
	if !ok {
		return false
	}
	pc, ok := info.protocol(protocol)
	if !ok {
		return false
	}
	if transport == "" {
		transport = TransportTCP
		if _, ok := pc.transport(transport); !ok && len(pc.Transports) > 0 {
			transport = pc.Transports[0].Type
		}
	}
	tc, ok := pc.transport(transport)
	if !ok {
		return false
	}
	if security == "" {
		return false
	}
	for _, s := range tc.Securities {
		if s == security {
			return true
		}
	}
	return false
}

func (info Info) protocol(name string) (ProtocolCap, bool) {
	for _, p := range info.Protocols {
		if p.Protocol == name {
			return p, true
		}
	}
	return ProtocolCap{}, false
}

func (p ProtocolCap) transport(name string) (TransportCap, bool) {
	for _, t := range p.Transports {
		if t.Type == name {
			return t, true
		}
	}
	return TransportCap{}, false
}

func containsString(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

// kernelVersionPattern mirrors the agent's safeVersionPattern
// (go-gost/x/kernel): a concrete version is a single URL path segment —
// alphanumeric start, then alphanumerics, dots and dashes, at most 40
// chars. validKernelVersion additionally rejects "..", which the bare
// charset admits ("1..2") but a path segment must never contain.
//
// C2-01: this validation lives in the kernel package as the single
// point. Before, ResolveVersion/downloadSpec substituted the version
// into download URL templates unchecked, and the only guards were the
// handler's normaliser and the agent's pattern — any future caller
// bypassing the normaliser silently reopened template injection.
//
// 规范文法（R2-P2-11 收敛，本点为规范表述）:版本/tag 单段文法＝
// ^[0-9A-Za-z][0-9A-Za-z.-]{0,39}$（首字符字母数字、总长 ≤40）且
// 不含 ".."。其余实现点——agent safeVersionPattern（镜像本点）、
// handler normalizeKernelVersion、validateUpgradeVersion、
// redirectTagPattern、脚本 panel_validate_explicit_version 与
// version_value_format_ok——一律按本规范对齐边缘;保留差异者必须
// 在各自实现处点名并给出理由,新增版本文法实现点须先对照本规范。
var kernelVersionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,39}$`)

func validKernelVersion(version string) bool {
	return kernelVersionPattern.MatchString(version) && !strings.Contains(version, "..")
}

// ResolveVersion maps a requested version to the one that will be installed:
// kernels with a closed version list use their default for "latest" and
// reject anything else they cannot verify.
func ResolveVersion(info Info, version string) (string, error) {
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	if len(info.Versions) == 0 {
		if version == "" {
			return "latest", nil
		}
		if strings.EqualFold(version, "latest") {
			return version, nil
		}
		if !validKernelVersion(version) {
			return "", fmt.Errorf("版本号 %s 格式不正确", version)
		}
		return version, nil
	}
	if version == "" || strings.EqualFold(version, "latest") {
		return info.Versions[0], nil
	}
	for _, v := range info.Versions {
		if v == version {
			return version, nil
		}
	}
	return "", fmt.Errorf("%s 仅支持版本 %s", info.Name, strings.Join(info.Versions, " / "))
}

// ResolveDownload builds the agent download spec for an installable version.
// proxy, when non-empty, prefixes GitHub URLs the same way agent upgrades are
// mirrored.
func ResolveDownload(info Info, version, proxy string) (map[string]interface{}, error) {
	if info.Download == nil {
		return nil, fmt.Errorf("%s 没有下载信息", info.Name)
	}
	version, err := ResolveVersion(info, version)
	if err != nil {
		return nil, err
	}
	return downloadSpec(info.Download, version, proxy)
}

func downloadSpec(d *Download, version, proxy string) (map[string]interface{}, error) {
	// C2-01: downloadSpec is the funnel every spec build passes through
	// (via ResolveDownload), so the version is validated here as
	// well — a caller that skips ResolveVersion still cannot inject
	// into the URL template.
	if version != "" && !strings.EqualFold(version, "latest") && !validKernelVersion(version) {
		return nil, fmt.Errorf("版本号 %s 格式不正确", version)
	}
	proxy = strings.TrimSuffix(strings.TrimSpace(proxy), "/")
	if !d.Mirrorable {
		proxy = ""
	}
	mirror := func(u string) string {
		if proxy == "" || u == "" {
			return u
		}
		return proxy + "/" + u
	}
	archMap := make(map[string]string, len(d.ArchMap))
	for k, v := range d.ArchMap {
		archMap[k] = v
	}
	spec := map[string]interface{}{
		"archive": d.Archive,
		"binName": d.BinName,
		"archMap": archMap,
	}
	latest := version == "" || strings.EqualFold(version, "latest")

	switch {
	case d.Pinned != nil:
		sums, ok := d.Pinned[version]
		if !ok {
			return nil, fmt.Errorf("版本 %s 没有内置校验值，无法安装", version)
		}
		pinned := make(map[string]string, len(sums))
		for k, v := range sums {
			pinned[k] = v
		}
		// Arches without a build of this version are left out, so the
		// agent reports an unsupported arch instead of a missing checksum.
		for goarch, arch := range archMap {
			if _, ok := sums[arch]; !ok {
				delete(archMap, goarch)
			}
		}
		spec["url"] = mirror(strings.ReplaceAll(d.URL, "{VERSION}", version))
		spec["sha256"] = pinned
	case d.GitHubRepo != "":
		gh := map[string]interface{}{"repo": d.GitHubRepo, "asset": d.GitHubAsset, "proxy": proxy}
		if latest {
			spec["url"] = d.URL // {VERSION} left for the agent to resolve
		} else {
			spec["url"] = mirror(strings.ReplaceAll(d.URL, "{VERSION}", version))
			gh["tag"] = "v" + version
		}
		spec["github"] = gh
	default:
		url, sum := d.LatestURL, d.LatestSum
		if !latest {
			url = strings.ReplaceAll(d.URL, "{VERSION}", version)
			sum = strings.ReplaceAll(d.ChecksumURL, "{VERSION}", version)
		}
		spec["url"] = mirror(url)
		spec["checksumUrl"] = mirror(sum)
	}
	return spec, nil
}
