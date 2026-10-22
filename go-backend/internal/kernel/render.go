package kernel

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// File is one configuration file the agent writes before (re)starting a
// kernel. Name is relative to the kernel's run directory.
type File struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// Process is one supervised process; Bin is "" for the kernel's own binary
// or the name of an extra binary.
type Process struct {
	Name string   `json:"name"`
	Bin  string   `json:"bin"`
	Args []string `json:"args"`
}

// Rendered is everything the agent needs to run one kernel.
type Rendered struct {
	Files     []File    `json:"files"`
	Args      []string  `json:"args"`
	Processes []Process `json:"processes,omitempty"`
	// Extras names helper binaries the processes need (e.g. shadow-tls).
	Extras []string `json:"extras,omitempty"`
	// Idle means there is nothing to run (a per-inbound kernel without
	// inbounds); the kernel is applied disabled.
	Idle bool `json:"idle,omitempty"`
	// UserMarks lists the users whose outgoing traffic carries UserMark, so
	// the agent can count it per user.
	UserMarks []int64 `json:"userMarks,omitempty"`
	Hash      string  `json:"hash"`
}

// RenderOptions are panel settings a kernel config depends on.
type RenderOptions struct {
	// RuleSetMirror prefixes the GitHub URLs sing-box downloads 分流 rule
	// sets from ("" downloads directly).
	RuleSetMirror string
	// SingboxHTTPClient defines the HTTP client rule sets download with
	// (sing-box 1.14+; older versions reject the field, newer ones drop
	// the implicit default in 1.16).
	SingboxHTTPClient bool
	// SingboxSnell lets sing-box dial Snell targets (chained outbounds and
	// 分流 targets); sing-box 1.14+.
	SingboxSnell bool
}

// Render produces the kernel configuration for the given enabled inbounds.
// Inbounds must already be normalized.
func Render(kernelType string, inbounds []Inbound) (Rendered, error) {
	return RenderWith(kernelType, inbounds, RenderOptions{})
}

// RenderWith is Render with panel settings.
func RenderWith(kernelType string, inbounds []Inbound, opts RenderOptions) (Rendered, error) {
	sorted := make([]Inbound, len(inbounds))
	copy(sorted, inbounds)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	// 渲染期复检（与保存期 normalizeExtra 同口径）：指定出站 IP 策略的
	// 入站，其 Extra 不得覆盖 tag/detour（S4）。
	for _, in := range sorted {
		if err := ValidateOutboundIPExtra(kernelType, in.OutboundIPStrategy, in.Extra); err != nil {
			return Rendered{}, err
		}
	}

	var out Rendered
	switch kernelType {
	case TypeXray:
		content, err := renderXray(sorted)
		if err != nil {
			return Rendered{}, err
		}
		out.Files = []File{{Name: "config.json", Content: content}}
		out.Args = []string{"run", "-c", "{RUN_DIR}/config.json"}
	case TypeSingbox:
		content, err := renderSingbox(sorted, opts)
		if err != nil {
			return Rendered{}, err
		}
		out.Files = []File{{Name: "config.json", Content: content}}
		out.Args = []string{"run", "-c", "{RUN_DIR}/config.json"}
	case TypeSnell:
		r, err := renderSnell(sorted)
		if err != nil {
			return Rendered{}, err
		}
		out = r
	default:
		return Rendered{}, fmt.Errorf("暂不支持内核 %s", kernelType)
	}
	out.UserMarks = markedUsers(sorted)
	out.Hash = hashRendered(out)
	return out, nil
}

func hashRendered(r Rendered) string {
	h := sha256.New()
	for _, f := range r.Files {
		h.Write([]byte(f.Name))
		h.Write([]byte{0})
		h.Write([]byte(f.Content))
		h.Write([]byte{0})
	}
	for _, a := range r.Args {
		h.Write([]byte(a))
		h.Write([]byte{0})
	}
	for _, p := range r.Processes {
		h.Write([]byte(p.Name + "\x00" + p.Bin + "\x00" + strings.Join(p.Args, "\x00")))
		h.Write([]byte{1})
	}
	for _, e := range r.Extras {
		h.Write([]byte(e))
		h.Write([]byte{2})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func marshalConfig(v interface{}) (string, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b) + "\n", nil
}

// extraProtectedTopKeys 是高级配置（Extra）不得覆盖的顶层键：这些字段是
// 面板在保存期校验过的不变量（端口归属、监听、tag 引用、凭据集合、安全层），
// 被 Extra 改掉会造成「面板显示正常、实际监听/安全层/凭据全变」的隐性不一致
// （F-022）。集合取 Xray 与 sing-box 两种入站对象的并集，两边都不会误伤：
// 预期用途只有 sniffing、streamSettings.sockopt 这类增量项。
var extraProtectedTopKeys = map[string]bool{
	"tag": true, "listen": true, "listen_port": true, "port": true,
	"protocol": true, "type": true,
	"settings": true, "users": true, "tls": true, "transport": true,
	"method": true, "password": true,
	// R3X-013：sing-box Snell 入站的凭据键名是 psk（非 password）、
	// v6 整形键名 mode，两键亦在并集内（Xray 对象无同名顶层键）。
	"psk": true, "mode": true,
	"detour": true, "handshake": true, "version": true,
}

// extraProtectedStreamSubKeys 是 streamSettings 下同样受保护的子键
// （安全层与传输形态）；sockopt 等增量子键不在其列。
var extraProtectedStreamSubKeys = map[string]bool{
	"security": true, "tlsSettings": true, "realitySettings": true,
	"network": true,
	"wsSettings": true, "grpcSettings": true, "xhttpSettings": true,
	"splithttpSettings": true, "tcpSettings": true, "kcpSettings": true,
	"quicSettings": true, "httpupgradeSettings": true,
}

// validateExtraProtectedKeys 拒绝含受保护键的 Extra（保存期与渲染期共用）。
func validateExtraProtectedKeys(obj map[string]interface{}) error {
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if extraProtectedTopKeys[k] {
			return fmt.Errorf("高级配置不能覆盖受保护字段 %q", k)
		}
		if k == "streamSettings" {
			if sub, ok := obj[k].(map[string]interface{}); ok {
				subKeys := make([]string, 0, len(sub))
				for sk := range sub {
					subKeys = append(subKeys, sk)
				}
				sort.Strings(subKeys)
				for _, sk := range subKeys {
					if extraProtectedStreamSubKeys[sk] {
						return fmt.Errorf("高级配置不能覆盖受保护字段 %q", "streamSettings."+sk)
					}
				}
			}
		}
	}
	return nil
}

// mergeExtra deep-merges a user supplied JSON object into dst. Objects merge
// recursively; any other value replaces the default.
func mergeExtra(dst map[string]interface{}, extra json.RawMessage) error {
	if len(extra) == 0 {
		return nil
	}
	var src map[string]interface{}
	if err := json.Unmarshal(extra, &src); err != nil {
		return fmt.Errorf("高级配置解析失败: %v", err)
	}
	// F-022: 渲染期兜底（存量/导入行可能绕过 Normalize 的保存期校验）。
	if err := validateExtraProtectedKeys(src); err != nil {
		return err
	}
	deepMerge(dst, src)
	return nil
}

func deepMerge(dst, src map[string]interface{}) {
	for k, v := range src {
		srcMap, srcIsMap := v.(map[string]interface{})
		dstMap, dstIsMap := dst[k].(map[string]interface{})
		if srcIsMap && dstIsMap {
			deepMerge(dstMap, srcMap)
			continue
		}
		dst[k] = v
	}
}
