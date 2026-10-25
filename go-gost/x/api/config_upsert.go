package api

import (
	"strings"
)

// upsertNamed 把 item 按名并入 items：已存在同名项时用 item 替换首次出现
// 的那一份、并清除其余同名副本；不存在时追加。nil 项原样保留，不 panic。
//
// 用途：R18-F02/F-04——管理 API 各 create handler 在 registry IsRegistered
// 守卫之后、config.OnUpdate 内曾对配置做无去重 append。成员已在配置中、
// 但不在注册表里时（例如暂停后 loader 跳过注册、或注册表与配置漂移），
// 一次重复 create 就会把第二份同名项写进配置并落盘、毒化 gost.json。
// 与 R17-A（x/socket/service.go 第 4 阶段）同款口径：按名查配置成员，
// 以配置为准去重；名按 TrimSpace 后比较。
func upsertNamed[T any](items []*T, item *T, nameOf func(*T) string) []*T {
	if item == nil {
		return items
	}
	name := strings.TrimSpace(nameOf(item))
	replaced := false
	kept := make([]*T, 0, len(items)+1)
	for _, existing := range items {
		if existing != nil && strings.TrimSpace(nameOf(existing)) == name {
			if replaced {
				continue
			}
			kept = append(kept, item)
			replaced = true
			continue
		}
		kept = append(kept, existing)
	}
	if !replaced {
		kept = append(kept, item)
	}
	return kept
}
