package repo

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// KCPNodeMemConfigKey 是「节点内存声明」设置键（FIX-KCPMEM，
// P1-04）：JSON 对象，键为节点 ID（十进制字符串）、值为该节点
// 总内存 MiB。面板据此给 KCP 生成点定内存档位帽（窗口/缓冲封
// 顶，见 handler.kcp_tuning.go 的档位表）。
//
// 为什么是声明而非上报：agent 的 SystemInfo 只上报
// memory_usage（使用率百分比），没有总内存字节数，面板无可靠
// 的节点内存真源；由管理员按机器规格声明是当前唯一可达口径。
// 未声明节点走保守缺省（small 档），声明只放大不缩小安全边际
// 之外的东西——声明值过大只是把该节点放回大档参数，由管理员
// 对机器规格负责。
//
// 归一化/校验单点在本文件（NormalizeKCPNodeMemMB）：保存面
// （handler.normalizeAndValidateConfigValue）与读面
// （KCPNodeMemMB）共用口径；读面遇到脏值（绕过保存面直入 DB）
// 按未声明处理，不让脏数据放大任何节点的参数帽。
const KCPNodeMemConfigKey = "kcp_node_mem_mb"

// MaxKCPNodeMemMB 是单节点声明内存上限（1TiB）：防误填天文数
// 字；真实节点远用不到，超过即判整批非法。
const MaxKCPNodeMemMB = 1048576

// NormalizeKCPNodeMemMB 校验并归一节点内存声明值。空串返回 ""
// （走调用方既有可清空语义：删除该键、回落未配置）；整体非法
// （非 JSON 对象、键非节点 ID、值非正整数或超上限）整批拒绝
// 并点名原因。归一形态：键按数值升序、紧凑 JSON。
func NormalizeKCPNodeMemMB(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	var entries map[string]int
	if err := json.Unmarshal([]byte(trimmed), &entries); err != nil {
		return "", fmt.Errorf("节点内存声明格式无效：须为 JSON 对象（节点ID→总内存MiB）")
	}
	if len(entries) == 0 {
		return "", nil
	}
	parsed := make(map[int64]int, len(entries))
	for key, mb := range entries {
		id, err := strconv.ParseInt(strings.TrimSpace(key), 10, 64)
		if err != nil || id <= 0 {
			return "", fmt.Errorf("节点内存声明键无效：%q 不是合法节点 ID", key)
		}
		if mb <= 0 || mb > MaxKCPNodeMemMB {
			return "", fmt.Errorf("节点 %d 的内存声明无效：须为 1..%d MiB", id, MaxKCPNodeMemMB)
		}
		parsed[id] = mb
	}
	ids := make([]int64, 0, len(parsed))
	for id := range parsed {
		ids = append(ids, id)
	}
	// 升序输出，保证归一形态唯一（保存/备份两面逐字可比）。
	for i := 0; i < len(ids); i++ {
		for j := i + 1; j < len(ids); j++ {
			if ids[j] < ids[i] {
				ids[i], ids[j] = ids[j], ids[i]
			}
		}
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, id := range ids {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%q:%d", strconv.FormatInt(id, 10), parsed[id])
	}
	b.WriteByte('}')
	return b.String(), nil
}

// KCPNodeMemMB 查某节点的声明内存（MiB）；未声明、spec 为空或
// spec 非法（脏值直入 DB）一律返回 0（调用方按未知→保守缺省
// 档处理）。本函数先过 NormalizeKCPNodeMemMB 同口径校验再查
// 表，脏 spec 整体作废而不是逐条放行。
func KCPNodeMemMB(spec string, nodeID int64) int {
	if nodeID <= 0 || strings.TrimSpace(spec) == "" {
		return 0
	}
	if _, err := NormalizeKCPNodeMemMB(spec); err != nil {
		return 0
	}
	var entries map[string]int
	if err := json.Unmarshal([]byte(spec), &entries); err != nil {
		return 0
	}
	return entries[strconv.FormatInt(nodeID, 10)]
}
