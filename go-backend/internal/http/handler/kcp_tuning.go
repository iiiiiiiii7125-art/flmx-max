package handler

import (
	"fmt"
	"strings"

	"go-backend/internal/store/repo"
)

// FIX-KCPMEM（P1-04/P1-05）：KCP 参数量级面。此前 tunnelKCPMetadata
// 恒下发窗口/缓冲全开常量（sndwnd/rcvwnd 4096、sockbuf/smuxbuf
// 4MiB、streambuf 2MiB），与调优面③抬升的 rmem_max/wmem_max
// （约总内存 5%，512MB 机约 25MiB、2GB 机约 100MiB）叠加真实可
// 达：单条忙 KCP 会话最坏可吃 10–25MiB，小内存节点多 KCP 目标
// 时先爆（P1-04）；FEC datashard 10/parityshard 3 恒下发、+30%
// 带宽无任何关闭档（P1-05）。本文件是这两面的新真源：按节点内
// 存档位封顶窗口/缓冲（内存档位帽），FEC 经面板设置给关闭档。

// KCP 内存档位（按节点总内存，字节）：
//
//	档位            节点内存        sndwnd/rcvwnd  sockbuf   smuxbuf   streambuf  单会话最坏*
//	small（保守缺省） ≤512MiB        512            1MiB      1MiB      512KiB     ≈4.8MiB
//	medium           ≤2GiB          2048           2MiB      2MiB      1MiB       ≈12.3MiB
//	large            >2GiB          4096           4MiB      4MiB      2MiB       ≈24.5MiB（＝现行值）
//
// *单会话最坏＝(sndwnd+rcvwnd)×mtu(1350B)＋sockbuf 收发两向＋
// smuxbuf＋streambuf×1 流（流数增加按 streambuf 线性叠加，三档同
// 理）。large 档取值与本修复前逐字相同——大内存部署的吞吐能力不
// 变；只有 small/medium 两档收紧（行为变更面见档位函数的 BC 注记）。
//
// 档位来源：面板当前没有可靠的节点总内存上报（agent SystemInfo
// 只报 memory_usage 使用率百分比，无总内存字节数），故缺省恒走
// small 保守档；节点内存可经面板设置 kcp_node_mem_mb 声明
// （nodeID→MiB 的 JSON 对象，见 kcpNodeMemSpec），声明的节点按
// 声明值定档。无声明/声明非法一律 small，fail-closed 向小档。
type kcpMemTier struct {
	name      string
	sndWnd    int
	rcvWnd    int
	sockBuf   int
	smuxBuf   int
	streamBuf int
}

var (
	kcpTierSmall = kcpMemTier{
		name: "small", sndWnd: 512, rcvWnd: 512,
		sockBuf: 1048576, smuxBuf: 1048576, streamBuf: 524288,
	}
	kcpTierMedium = kcpMemTier{
		name: "medium", sndWnd: 2048, rcvWnd: 2048,
		sockBuf: 2097152, smuxBuf: 2097152, streamBuf: 1048576,
	}
	kcpTierLarge = kcpMemTier{
		name: "large", sndWnd: 4096, rcvWnd: 4096,
		sockBuf: 4194304, smuxBuf: 4194304, streamBuf: 2097152,
	}
)

const (
	kcpTierSmallMaxMB  = 512
	kcpTierMediumMaxMB = 2048
)

// kcpMemTierForMB 按节点总内存（MiB）定档。memMB<=0（未知）回
// small 保守缺省档。
//
// BC（行为变更，须进发版说明）：本修复前全部节点恒按 large 档
// 下发；修复后未声明内存的节点、及声明 ≤2GiB 的节点，其 KCP 隧
// 道的窗口/缓冲被收紧到 small/medium 档——这些节点上 KCP 会话
// 的吞吐上限下降（换取小内存不被多会话打爆），>2GiB 声明节点
// 与修复前完全一致。节点内存声明口：面板设置 kcp_node_mem_mb。
func kcpMemTierForMB(memMB int) kcpMemTier {
	switch {
	case memMB <= 0 || memMB <= kcpTierSmallMaxMB:
		return kcpTierSmall
	case memMB <= kcpTierMediumMaxMB:
		return kcpTierMedium
	default:
		return kcpTierLarge
	}
}

// kcpFecModeConfigKey 是 FEC 档位设置键（P1-05 可配口）：
//   - ""（未配置）/ "default"：datashard 10 / parityshard 3，与
//     本修复前恒下发值一致，存量行为不变；
//   - "off"：关闭档——不下发 kcp.datashard/kcp.parityshard 两键，
//     agent 侧在 metadata 解析末尾把 DataShard/ParityShard 归零
//     （见 x 两侧 kcp metadata 的 flmxFecOff 标记处理），kcp-go
//     以 0/0 建 FEC 编码器即冗余比归零。仅写 0 不够：x 的
//     ParseFromFile/clone 缺省带 10/3，metadata 缺键时缺省值穿
//     透，故关闭档另下发布尔标记 kcp.fec_off=true 由 agent 显式
//     归零（该标记键非 kcp-go Config 字段，agent 消费后不入配置）。
//
// 本键在 repo.config_policy 中按 sensitive 收口（未分类默认亦
// sensitive）：仅管理员可读写，与其余生成面设置同口径。
const kcpFecModeConfigKey = "kcp_fec_mode"

const (
	kcpFecModeDefault = "default"
	kcpFecModeOff     = "off"
)

// normalizeKCPFecMode 校验 FEC 档位设置值（保存面单点，挂
// normalizeAndValidateConfigValue）。空串返回 ""走调用方既有可
// 清空语义（删键回未配置＝default）。
func normalizeKCPFecMode(value string) (string, error) {
	normalized := strings.TrimSpace(strings.ToLower(value))
	switch normalized {
	case "", kcpFecModeDefault, kcpFecModeOff:
		return normalized, nil
	default:
		return "", fmt.Errorf("KCP FEC 档位无效：仅支持 default（10/3）或 off（关闭）")
	}
}

// kcpFecOff 解析生成时的 FEC 是否关闭：只认确切的 "off"，其余
// （未配置、default、以及绕过保存校验直入 DB 的脏值）一律按
// default 处理——脏值不许静默关掉 FEC 保护。
func kcpFecOff(mode string) bool {
	return strings.TrimSpace(strings.ToLower(mode)) == kcpFecModeOff
}

// kcpFecMode 从面板设置读 FEC 档位；repo 不可用/读失败回
// default（存量行为），不让设置面故障阻断隧道下发。
func (h *Handler) kcpFecMode() string {
	if h == nil || h.repo == nil {
		return kcpFecModeDefault
	}
	cfg, err := h.repo.GetConfigByName(kcpFecModeConfigKey)
	if err != nil || cfg == nil {
		return kcpFecModeDefault
	}
	if kcpFecOff(cfg.Value) {
		return kcpFecModeOff
	}
	return kcpFecModeDefault
}

// kcpNodeMemSpec 是节点内存声明的解析结果（kcp_node_mem_mb 设
// 置值：JSON 对象，键为节点 ID 字符串、值为总内存 MiB）。解析
// 单点在 repo.NormalizeKCPNodeMemMB（保存面与本读面共用校验口
// 径）；此处只做已归一值的查表。
func kcpNodeMemTier(spec string, nodeID int64) kcpMemTier {
	return kcpMemTierForMB(repo.KCPNodeMemMB(spec, nodeID))
}

// kcpNodeMemSpec 读节点内存声明原文；repo 不可用/读失败回 ""
// （全部节点走 small 保守缺省档），不阻断下发。
func (h *Handler) kcpNodeMemSpec() string {
	if h == nil || h.repo == nil {
		return ""
	}
	cfg, err := h.repo.GetConfigByName(repo.KCPNodeMemConfigKey)
	if err != nil || cfg == nil {
		return ""
	}
	return cfg.Value
}

// kcpTiersForNodes 给一条隧道链涉及的节点集合定逐节点档位：
// 返回 nodeID→档位。KCP 段两端（拨号方、监听方）各自按本节点
// 档位生成 metadata——窗口/缓冲是逐端本地资源，不是协商参数，
// 两端异档可正常互通（ARQ 窗口按各自 rcvwnd 通告、缓冲纯本地）。
func (h *Handler) kcpTiersForNodes(nodeIDs ...int64) map[int64]kcpMemTier {
	spec := h.kcpNodeMemSpec()
	tiers := make(map[int64]kcpMemTier, len(nodeIDs))
	for _, id := range nodeIDs {
		tiers[id] = kcpNodeMemTier(spec, id)
	}
	return tiers
}

// kcpTierOrDefault 查档位表，未登记节点回 small 保守缺省档。
func kcpTierOrDefault(tiers map[int64]kcpMemTier, nodeID int64) kcpMemTier {
	if tiers != nil {
		if tier, ok := tiers[nodeID]; ok {
			return tier
		}
	}
	return kcpTierSmall
}
