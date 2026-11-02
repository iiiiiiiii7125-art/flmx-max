package kernel

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Snell egress guard (R7 · RB1-01). snell-server has no target ACL of its
// own: whoever holds an inbound PSK names the destination and the server
// dials it, so before this guard a Snell inbound could reach the node's
// private network and the cloud metadata address, unlike Xray (freedom's
// built-in private block) and sing-box (the panel-rendered ip_is_private
// reject rule). The guard therefore lives in the agent:
//
//   - snell-server processes (only those; shadow-tls fronts keep running
//     as the agent user — they dial the admin-configured TLS camouflage
//     server and the loopback Snell backend, never a client-chosen
//     target) run as the dedicated system user flmx-snell;
//   - the flmx_kernel nftables table drops that uid's egress packets to
//     non-public destination ranges, by destination IP, so domain-named
//     targets are covered the moment they are resolved, exactly like
//     literal ones.
//
// The drop set expands the product's canonical non-public definition
// (the flmx-no-private bypass / netguard classifier: IsPrivate ∪
// loopback ∪ link-local ∪ multicast ∪ unspecified ∪ the explicit
// special-purpose CIDRs) into nft interval sets, plus the documentation
// ranges and the IPv4-mapped forms of every v4 range. Deliberate
// differences from flmx-no-private, both forced by the packet layer:
// translated forms (NAT64/6to4 embedded IPv4) cannot be extracted by
// nft, so the translation prefixes themselves are not dropped (dropping
// them wholesale would kill legitimate NAT64 public traffic); and the
// node's own configured DNS resolvers are exempted on ports 53/853 —
// snell-server resolves target names through the system resolver, and
// without the exemption every domain target would break on nodes whose
// resolver is a loopback stub or a LAN gateway. The exemption is bounded
// to the exact resolver IPs read from /etc/resolv.conf.

// snellGuardUserName is the dedicated system user snell-server runs as.
const snellGuardUserName = "flmx-snell"

// snellGuard is the resolved state the nft script renders from. A nil
// guard (or UID 0) means no live Snell process needs the guard and the
// script carries no trace of it.
type snellGuard struct {
	UID       uint32
	Resolvers []string // system resolver IPs, sorted, exempted on 53/853
}

// snellPrivateV4 is the IPv4 drop set (see the header comment for the
// definition it expands).
var snellPrivateV4 = []string{
	"0.0.0.0/8",       // "this host on this network" (RFC 791)
	"10.0.0.0/8",      // RFC 1918
	"100.64.0.0/10",   // shared address space / CGNAT (RFC 6598)
	"127.0.0.0/8",     // loopback
	"169.254.0.0/16",  // link-local, incl. cloud metadata (RFC 3927)
	"172.16.0.0/12",   // RFC 1918
	"192.0.0.0/24",    // IETF protocol assignments (RFC 6890)
	"192.0.2.0/24",    // documentation TEST-NET-1 (RFC 5737)
	"192.168.0.0/16",  // RFC 1918
	"198.18.0.0/15",   // benchmarking (RFC 2544)
	"198.51.100.0/24", // documentation TEST-NET-2 (RFC 5737)
	"203.0.113.0/24",  // documentation TEST-NET-3 (RFC 5737)
	"224.0.0.0/4",     // multicast (RFC 1112)
	"240.0.0.0/4",     // reserved, incl. 255.255.255.255 (RFC 1112)
}

// snellPrivateV6 is the native IPv6 drop set.
var snellPrivateV6 = []string{
	"::/128",        // unspecified
	"::1/128",       // loopback
	"100::/64",      // discard-only (RFC 6666)
	"2001:db8::/32", // documentation (RFC 3849)
	"fc00::/7",      // unique local (RFC 4193)
	"fe80::/10",     // link-local (RFC 4291)
	"ff00::/8",      // multicast (RFC 4291)
}

// snellPrivateV6All returns the native v6 set plus the IPv4-mapped form
// (::ffff:a.b.c.d) of every v4 range, derived from snellPrivateV4 so the
// two can never drift apart: a resolver or DNS64 path that hands
// snell-server a mapped address must not slip past the v4 set.
func snellPrivateV6All() []string {
	out := append([]string(nil), snellPrivateV6...)
	for _, c := range snellPrivateV4 {
		ip, ipnet, err := net.ParseCIDR(c)
		if err != nil || ip.To4() == nil {
			continue
		}
		ones, _ := ipnet.Mask.Size()
		out = append(out, fmt.Sprintf("::ffff:%s/%d", ip.String(), 96+ones))
	}
	return out
}

// systemResolvers reads the nameserver IPs from /etc/resolv.conf. It is
// a variable so tests can substitute a fixed resolver set.
var systemResolvers = func() []string {
	b, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		return nil
	}
	return parseResolvConf(string(b))
}

// parseResolvConf extracts the sorted, de-duplicated nameserver IPs
// from resolv.conf content.
func parseResolvConf(content string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, line := range strings.Split(content, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 || f[0] != "nameserver" {
			continue
		}
		ip := net.ParseIP(f[1])
		if ip == nil || seen[ip.String()] {
			continue
		}
		seen[ip.String()] = true
		out = append(out, ip.String())
	}
	sort.Strings(out)
	return out
}

// lookupSnellGuardUser resolves the dedicated user's uid/gid. It is a
// variable so tests can inject a fixed identity without touching the
// host's user database.
var lookupSnellGuardUser = func() (uid, gid uint32, err error) {
	u, err := user.Lookup(snellGuardUserName)
	if err != nil {
		return 0, 0, err
	}
	uid64, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return 0, 0, fmt.Errorf("用户 %s 的 uid 非法: %v", snellGuardUserName, err)
	}
	gid64, err := strconv.ParseUint(u.Gid, 10, 32)
	if err != nil {
		return 0, 0, fmt.Errorf("用户 %s 的 gid 非法: %v", snellGuardUserName, err)
	}
	return uint32(uid64), uint32(gid64), nil
}

// createSnellGuardUser creates the dedicated system user. It is a
// variable so tests never create real users.
var createSnellGuardUser = func(ctx context.Context) error {
	if _, err := exec.LookPath("useradd"); err == nil {
		cmd := exec.CommandContext(ctx, "useradd", "--system", "--no-create-home",
			"--shell", "/usr/sbin/nologin", snellGuardUserName)
		if out, err := cmd.CombinedOutput(); err == nil {
			return nil
		} else {
			return fmt.Errorf("useradd %s 失败: %v (%s)", snellGuardUserName, err, strings.TrimSpace(string(out)))
		}
	}
	if _, err := exec.LookPath("adduser"); err == nil {
		// BusyBox/Alpine form: system user, no home, no password login.
		cmd := exec.CommandContext(ctx, "adduser", "-S", "-D", "-H", snellGuardUserName)
		if out, err := cmd.CombinedOutput(); err == nil {
			return nil
		} else {
			return fmt.Errorf("adduser %s 失败: %v (%s)", snellGuardUserName, err, strings.TrimSpace(string(out)))
		}
	}
	return fmt.Errorf("未找到 useradd/adduser，无法创建专用用户 %s", snellGuardUserName)
}

// ensureSnellGuardUser returns the dedicated user's uid/gid, creating
// the user on first use. Any failure is fatal to a Snell apply: running
// snell-server as the agent user would leave it unguarded.
func ensureSnellGuardUser(ctx context.Context) (uid, gid uint32, err error) {
	uid, gid, err = lookupSnellGuardUser()
	if err == nil {
		return uid, gid, nil
	}
	if cerr := createSnellGuardUser(ctx); cerr != nil {
		return 0, 0, fmt.Errorf("Snell 私网拦截需要专用用户 %s: %v", snellGuardUserName, cerr)
	}
	uid, gid, err = lookupSnellGuardUser()
	if err != nil {
		return 0, 0, fmt.Errorf("创建专用用户 %s 后仍无法解析: %v", snellGuardUserName, err)
	}
	return uid, gid, nil
}

// isSnellServerSpec reports whether a process spec is an actual
// snell-server process (as opposed to a shadow-tls front). The panel
// renders one "snell-<inboundID>" process per Snell inbound.
func isSnellServerSpec(name string) bool {
	return strings.HasPrefix(name, "snell-")
}

// reqHasSnellServer reports whether the request runs at least one
// snell-server process when enabled.
func reqHasSnellServer(req ApplyRequest) bool {
	if req.Type != "snell" || !req.Enabled {
		return false
	}
	for _, spec := range req.processSpecs() {
		if isSnellServerSpec(spec.Name) {
			return true
		}
	}
	return false
}

// snellGuardFor derives the guard the manager must render now: non-nil
// exactly while a Snell instance with at least one snell-server process
// is enabled. uid > 0 must have been resolved by the caller (Apply /
// Control ensure it before processes start); without a uid the guard
// stays off and the caller is responsible for failing loudly instead.
func (m *Manager) snellGuardFor(uid uint32) *snellGuard {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snellGuardForLocked(uid)
}

// snellGuardForLocked is snellGuardFor with m.mu already held.
func (m *Manager) snellGuardForLocked(uid uint32) *snellGuard {
	if uid == 0 {
		return nil
	}
	for _, inst := range m.instances {
		if reqHasSnellServer(inst.req) {
			return &snellGuard{UID: uid, Resolvers: systemResolvers()}
		}
	}
	return nil
}

// fixSnellRunPerms makes a Snell instance's files reachable by the
// guard user: the run directory and the binary path ancestors become
// traverse-only (0711, no listing) and the rendered configs are owned
// by the guard user, so snell-server (running as that user) can read
// its own config while the file contents stay 0600. Other kernel types
// are untouched.
func fixSnellRunPerms(runDir string, files []File, uid, gid uint32) error {
	if err := os.Chmod(runDir, 0o711); err != nil {
		return fmt.Errorf("调整 Snell 运行目录权限失败: %v", err)
	}
	for _, f := range files {
		if err := os.Chown(filepath.Join(runDir, f.Name), int(uid), int(gid)); err != nil {
			return fmt.Errorf("调整 Snell 配置 %s 属主失败: %v", f.Name, err)
		}
	}
	return nil
}

// chmodTraverse makes the ancestor directories of dir traverse-only so
// the guard user can reach the kernel binaries beneath the agent's
// 0700 base directory without being able to list anything.
func chmodTraverseAncestors(baseDir, dir string) error {
	base := filepath.Clean(baseDir)
	cur := filepath.Clean(dir)
	var chain []string
	for {
		chain = append(chain, cur)
		if cur == base {
			break
		}
		parent := filepath.Dir(cur)
		if parent == cur || !strings.HasPrefix(cur, base+string(filepath.Separator)) {
			return fmt.Errorf("目录 %s 不在 %s 之下", dir, baseDir)
		}
		cur = parent
	}
	for _, d := range chain {
		if err := os.Chmod(d, 0o711); err != nil {
			return fmt.Errorf("调整目录 %s 权限失败: %v", d, err)
		}
	}
	return nil
}
