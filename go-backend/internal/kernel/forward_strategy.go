package kernel

import "strings"

// ForwardStrategyLegal 是转发负载策略写面合法集 {fifo, round, rand,
// hash} 的单一定义点（R4-F4-02）：handler 的 create/update 与 repo
// 的备份恢复面共用本判，不得在各面另立字面集——集外值（如
// "banana"、大小写异形 "FIFO"）此前三面均原文落库，而 agent 的
// selector 解析对未知策略一律 default 为 roundrobin，声明策略与
// 生效策略静默不一致且无任何回执。
//
// 合法集取面板契约集（前端策略选项与 forward.md 口径）：agent 解析
// 面另认的别名（rr/random/ha）不在面板契约内、不放行。空串不是
// 合法策略值——它是各写面的「未提供」形态，由各面先按自身默认
// 语义解析（create 缺省 fifo、update 回填存量、恢复按 fifo 补齐）
// 后再过本判。
func ForwardStrategyLegal(strategy string) bool {
	switch strings.TrimSpace(strategy) {
	case "fifo", "round", "rand", "hash":
		return true
	}
	return false
}
