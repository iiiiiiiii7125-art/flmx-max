package config

import "os"

// Getenv 返回环境变量的值（未设或为空返回空串）。
// 改名过渡期的 FLVX_* 旧名双读已于 1.9.10 退场：此处只读新名
// FLMX_*，存量部署须把 .env 里的 FLVX_* 键改名为对应 FLMX_* 键
// （清单见 doc/guide/upgrade.md「FLVX_* 环境变量双读退场」节）。
func Getenv(name string) string {
	return os.Getenv(name)
}
