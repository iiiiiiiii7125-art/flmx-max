package handler

import (
	"go-backend/internal/store/repo"
)

// 订阅前置地址（subscription_front_urls）的归一化/校验单点实现在
// repo 包（repo.NormalizeSubscriptionFrontURLs）：保存面
// （normalizeAndValidateConfigValue）与备份恢复面（importConfigs）
// 共用同一实现。R4B09-001 前实现住本文件、恢复面漏接，非法/未归一
// 值可经备份导入绕过写面单点原文落库；因 handler 依赖 repo、反向
// 不可，实现下沉 repo，本文件只留委托别名（既有调用点与测试不改）。
const subscriptionFrontURLsConfigKey = repo.SubscriptionFrontURLsConfigKey

// maxSubscriptionFrontURLs 是前置地址个数上限的保存面别名（单点值在
// repo.MaxSubscriptionFrontURLs）。
const maxSubscriptionFrontURLs = repo.MaxSubscriptionFrontURLs

// normalizeSubscriptionFrontURLs 委托单点实现 repo.NormalizeSubscriptionFrontURLs。
func normalizeSubscriptionFrontURLs(raw string) (string, error) {
	return repo.NormalizeSubscriptionFrontURLs(raw)
}
