package config

import "os"

// GetEnvAny 返回入参顺序中第一个非空的环境变量值。
// 改名过渡期使用：新名（FLMX_*）优先、旧名（FLVX_*）兜底，
// 使存量部署 .env 里已有的 FLVX_* 配置无需改动继续生效。
func GetEnvAny(names ...string) string {
	for _, n := range names {
		if v := os.Getenv(n); v != "" {
			return v
		}
	}
	return ""
}
