package socket

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// F2-PORT-01：live socket 探针（只读命令）。面板的端口占用检查只查
// DB 三源（forward_port / node_inbound / chain_tunnel）时，看不到
// 节点本机上 DB 之外的监听端口（手动服务、旧部署残留、非面板进
// 程）；面板下发前先问 agent 扫一遍 /proc/net，DB＋live socket
// 双源判定占用。TCP 只认 LISTEN（st=0A），UDP 无 LISTEN 态、任
// 意表项即已绑定。探针只读：isMutationCommand 默认新命令为只读，
// 无需另行登记。

// procNetDir 是 /proc/net 的根；测试用临时目录覆盖。
var procNetDir = "/proc/net"

type checkPortListeningRequest struct {
	Ranges []struct {
		Lo int `json:"lo"`
		Hi int `json:"hi"`
	} `json:"ranges"`
}

type checkPortListeningResponse struct {
	// Listening 是 TCP＋UDP 的并集（端口号不分协议占位：面板的占
	// 用判定按端口号，不按协议拆分）。
	Listening []int `json:"listening"`
	TCP       []int `json:"tcp"`
	UDP       []int `json:"udp"`
	// Established 是各 TCP 表上处于 ESTABLISHED（st=01）态的端口
	// （C-04：暂停验收须证“无已建立连接”，只看 LISTEN 会漏掉
	// 已建立连接仍排干的情形）。恒 present（可为空集）；老面板
	// 收到多一个字段无影响。
	Established []int `json:"established"`
}

// parseProcNetListening 解析一份 /proc/net/{tcp,tcp6,udp,udp6} 内容，
// 返回 want 集合内已占用的端口。tcp=true 时只认 LISTEN 行
// （st=0A）；tcp=false（UDP）时任意表项即已绑定。行首为表头、字段
// 不足或端口非十六进制的行直接跳过（不报错：内核表项格式随版本
// 变，探针按「认得出的行」尽力而为）。
func parseProcNetListening(content string, tcp bool, want map[int]bool) map[int]bool {
	found := make(map[int]bool)
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		local := fields[1]
		colon := strings.LastIndexByte(local, ':')
		if colon < 0 {
			continue
		}
		port64, err := strconv.ParseUint(local[colon+1:], 16, 32)
		if err != nil {
			continue
		}
		port := int(port64)
		if !want[port] {
			continue
		}
		if tcp && fields[3] != "0A" {
			continue
		}
		found[port] = true
	}
	return found
}

// scanProcNetPorts 扫 dir 下的 tcp/tcp6/udp/udp6 四张表，返回 want
// 内已占用的 TCP/UDP 端口（各自升序）。缺失的表（如无 IPv6 时的
// tcp6）跳过；其他读错误返回 err，由调用方按探针失败处理。
func scanProcNetPorts(dir string, want map[int]bool) (tcpPorts, udpPorts []int, err error) {
	seenTCP := make(map[int]bool)
	seenUDP := make(map[int]bool)
	for _, f := range []struct {
		name string
		tcp  bool
		seen map[int]bool
	}{
		{"tcp", true, seenTCP}, {"tcp6", true, seenTCP},
		{"udp", false, seenUDP}, {"udp6", false, seenUDP},
	} {
		b, rerr := os.ReadFile(filepath.Join(dir, f.name))
		if rerr != nil {
			if os.IsNotExist(rerr) {
				continue
			}
			return nil, nil, fmt.Errorf("读取 /proc/net/%s 失败: %w", f.name, rerr)
		}
		for p := range parseProcNetListening(string(b), f.tcp, want) {
			f.seen[p] = true
		}
	}
	return sortedPorts(seenTCP), sortedPorts(seenUDP), nil
}

func sortedPorts(set map[int]bool) []int {
	out := make([]int, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Ints(out)
	return out
}

// parseProcNetEstablished 与 parseProcNetListening 同形，只认 TCP
// ESTABLISHED（st=01）行——暂停验收的“已建立连接”面。TIME_WAIT
// 等其它态不算（连接已死，只剩内核回收态）。
func parseProcNetEstablished(content string, want map[int]bool) map[int]bool {
	return parseProcNetByState(content, want, func(st string) bool { return st == "01" })
}

// procNetLiveStates are the TCP states in which a socket on a port
// can still deliver data to a peer (or is a live connection):
// ESTABLISHED plus the half-closed/draining states. TIME_WAIT (06)
// is genuinely dead (both sides closed, kernel reclaim only) and
// LISTEN (0A) is the listener itself, so neither counts. A-NEW: a
// bulk relay flow is in FIN-WAIT-2/CLOSE-WAIT by the time the pause
// lands, with megabytes still buffered in the peer's Recv-Q —
// counting only ESTABLISHED let the pause skip the kill entirely.
var procNetLiveStates = map[string]bool{
	"01": true, // ESTABLISHED
	"02": true, // SYN_SENT
	"03": true, // SYN_RECV
	"04": true, // FIN_WAIT1
	"05": true, // FIN_WAIT2
	"08": true, // CLOSE_WAIT
	"09": true, // LAST_ACK
	"0B": true, // CLOSING
}

// parseProcNetConnected 与 parseProcNetEstablished 同形，但认全部
// live/draining 态（见 procNetLiveStates）——暂停的 kill 决策面。
func parseProcNetConnected(content string, want map[int]bool) map[int]bool {
	return parseProcNetByState(content, want, func(st string) bool { return procNetLiveStates[st] })
}

func parseProcNetByState(content string, want map[int]bool, match func(string) bool) map[int]bool {
	found := make(map[int]bool)
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		local := fields[1]
		colon := strings.LastIndexByte(local, ':')
		if colon < 0 {
			continue
		}
		port64, err := strconv.ParseUint(local[colon+1:], 16, 32)
		if err != nil {
			continue
		}
		port := int(port64)
		if !want[port] {
			continue
		}
		if !match(fields[3]) {
			continue
		}
		found[port] = true
	}
	return found
}

// scanProcNetEstablished 扫 tcp/tcp6 两张表，返回 want 内有
// ESTABLISHED 连接的端口（升序）。缺失的表跳过；其他读错误返回 err。
func scanProcNetEstablished(dir string, want map[int]bool) ([]int, error) {
	seen := make(map[int]bool)
	for _, name := range []string{"tcp", "tcp6"} {
		b, rerr := os.ReadFile(filepath.Join(dir, name))
		if rerr != nil {
			if os.IsNotExist(rerr) {
				continue
			}
			return nil, fmt.Errorf("读取 /proc/net/%s 失败: %w", name, rerr)
		}
		for p := range parseProcNetEstablished(string(b), want) {
			seen[p] = true
		}
	}
	return sortedPorts(seen), nil
}

// scanProcNetConnected 与 scanProcNetEstablished 同形，但认全部
// live/draining 态（见 procNetLiveStates）——暂停的 kill 决策面：
// bulk 流暂停时多半已不在 ESTABLISHED，仍须杀。
func scanProcNetConnected(dir string, want map[int]bool) ([]int, error) {
	seen := make(map[int]bool)
	for _, name := range []string{"tcp", "tcp6"} {
		b, rerr := os.ReadFile(filepath.Join(dir, name))
		if rerr != nil {
			if os.IsNotExist(rerr) {
				continue
			}
			return nil, fmt.Errorf("读取 /proc/net/%s 失败: %w", name, rerr)
		}
		for p := range parseProcNetConnected(string(b), want) {
			seen[p] = true
		}
	}
	return sortedPorts(seen), nil
}

func handleCheckPortListening(data interface{}) (interface{}, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("序列化数据失败: %v", err)
	}
	var req checkPortListeningRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("解析端口探针请求失败: %v", err)
	}
	if len(req.Ranges) == 0 {
		return nil, fmt.Errorf("端口探针请求至少需要一个区间")
	}
	want := make(map[int]bool)
	for _, r := range req.Ranges {
		if r.Lo < 1 || r.Hi > 65535 || r.Lo > r.Hi {
			return nil, fmt.Errorf("端口区间无效: %d-%d", r.Lo, r.Hi)
		}
		for p := r.Lo; p <= r.Hi; p++ {
			want[p] = true
		}
	}
	tcpPorts, udpPorts, err := scanProcNetPorts(procNetDir, want)
	if err != nil {
		return nil, err
	}
	establishedPorts, err := scanProcNetEstablished(procNetDir, want)
	if err != nil {
		return nil, err
	}
	union := make(map[int]bool, len(tcpPorts)+len(udpPorts))
	for _, p := range tcpPorts {
		union[p] = true
	}
	for _, p := range udpPorts {
		union[p] = true
	}
	return checkPortListeningResponse{
		Listening:   sortedPorts(union),
		TCP:         tcpPorts,
		UDP:         udpPorts,
		Established: establishedPorts,
	}, nil
}
