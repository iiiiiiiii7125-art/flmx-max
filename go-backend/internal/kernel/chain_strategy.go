package kernel

import "strings"

// ChainStrategyLegal 是隧道链路策略（chain_tunnel.strategy）写面
// 合法集的单一定义点（R1-03-M1）：handler 的 tunnel create/update、
// repo 的备份恢复面与联邦 ApplyRole 面共用本判，不得在各面另立
// 字面集。此前链路策略全写面无门：banana/FIFO 等集外值原文落库，
// 而 agent 的 selector 解析（go-gost ParseChainSelector/
// ParseNodeSelector）对集外值一律 default 为 roundrobin——声明
// fifo（主备）实际轮询、备出口在主出口健康时持续分到生产流量，
// 且无任何回执，与 R4-F4-02 当年为转发面立门的机制完全同族。
//
// 合法集取面板契约集（前端 tunnel.tsx 的分段 Select 口径），按段
// 分格：
//   - 出口行（chainType 3）：{fifo, round, rand, best}——best 是
//     面板最优出口伪策略，下发前由 runtimeTunnelStrategy 转 fifo
//     并经 best-exit 排序生效，只对出口目标集有语义；
//   - 链路跳行（chainType 2）：{fifo, round, rand}——best 落在跳行
//     永不触发 best-exit 排序、只会被静默降级为 fifo，故写面即拒；
//   - 入口行（chainType 1）：策略不参与任何 selector 选取（消费面
//     取目标行策略），但与出口行同负载形态在前端回显，取并集
//     {fifo, round, rand, best}；
//   - 未知 chainType：按最严的跳行集判（fail-closed）。
//
// 大小写敏感、TrimSpace 后判（与 ForwardStrategyLegal 同口径）：
// agent 另认的别名（rr/random/ha）不在面板契约内、不放行。空串
// 不是合法策略值——它是各写面的「未提供」形态，由各面先按自身
// 缺省语义解析（链路统一缺省 round）后再过本判。
func ChainStrategyLegal(chainType int, strategy string) bool {
	switch strings.TrimSpace(strategy) {
	case "fifo", "round", "rand":
		return true
	case "best":
		return chainType == 1 || chainType == 3
	}
	return false
}

// ChainStrategyLegalValues 返回某段合法集的回执文案（与
// ChainStrategyLegal 同口径，错误信息点名用）。
func ChainStrategyLegalValues(chainType int) string {
	if chainType == 2 {
		return "fifo、round、rand"
	}
	return "fifo、round、rand、best"
}
