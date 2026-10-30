package handler

import (
	"fmt"
	"math"
	"strconv"
)

// R11X-008：Tunnel.TrafficRatio 的合法集为 [0,100]（0=零计费），此前
// 只存在于前端表单、后端 create/update 无范围校验，负值落库后计费
// （钳 0）/forward 徽标（归一 1）/tunnel 列表（原样）三面读法分歧。
// 拒绝文案与 parseTrafficLimit 同格（中文点名字段 + 范围）。
func validateTunnelTrafficRatio(ratio float64) error {
	if math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio < 0 || ratio > 100 {
		return fmt.Errorf("流量倍率超出范围 [0,100]: %v", ratio)
	}
	return nil
}

// R11X-009：Tunnel.Flow（计费方向）的合法集为 {1=单向, 2=双向}，此前
// 后端 create/update 无枚举校验，域外值落库后展示显「未知」而计费按
// 归一口径（0→单向、≥3→双向）执行。拒绝文案与 parseTrafficLimit 同格。
func validateTunnelFlow(flow int64) error {
	if flow != 1 && flow != 2 {
		return fmt.Errorf("计费方向超出范围 {1,2}: %d", flow)
	}
	return nil
}

// B06-001（Round 2 K9）：Tunnel.Type / Tunnel.Status 的合法集为
// {1=端口转发, 2=隧道转发} / {0=停用, 1=启用}。同文件 flow、
// trafficRatio 与分组 status（R4X-012）都已立门，唯独这两个字段在
// create/update 两侧无校验：type=0/3/9、status=-1/7 原样落库并经
// list/get 回吐——消费面靠 ==1/==2 正向判定兜底暂未出事故，但脏值
// 与前端 {0,1}/{1,2} 映射错位、未来按闭集假设写的新消费面必踩。
// 拒绝文案与 validateTunnelFlow 同格（点名字段 + 合法集 + 实际值）。
func validateTunnelType(typeVal int) error {
	if typeVal != 1 && typeVal != 2 {
		return fmt.Errorf("隧道类型超出范围 {1,2}: %d", typeVal)
	}
	return nil
}

func validateTunnelStatus(status int) error {
	if status != 0 && status != 1 {
		return fmt.Errorf("隧道状态超出范围 {0,1}: %d", status)
	}
	return nil
}

// flowMiB is optional so older clients can keep sending the GB-based flow field.
// A positive value takes precedence and preserves sub-GB limits exactly.
func parseTrafficLimit(req map[string]interface{}, defaultGB int64) (flowGB, flowMiB int64, err error) {
	flowGB = asInt64(req["flow"], defaultGB)
	if flowGB < 0 {
		return 0, 0, fmt.Errorf("流量限制不能小于0")
	}
	raw, present := req["flowMiB"]
	if !present {
		return flowGB, 0, nil
	}
	flowMiB, err = strconv.ParseInt(asString(raw), 10, 64)
	if err != nil || flowMiB < 0 || flowMiB > math.MaxInt64/bytesPerMiB {
		return 0, 0, fmt.Errorf("流量限制超出范围")
	}
	if flowMiB > 0 {
		flowGB = (flowMiB-1)/1024 + 1
	}
	return flowGB, flowMiB, nil
}
