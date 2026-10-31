package api

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/go-gost/core/logger"
	"github.com/go-gost/x/config"
	"github.com/go-gost/x/config/loader"
	"github.com/go-gost/x/config/parsing/parser"
	"github.com/go-gost/x/registry"
	xservice "github.com/go-gost/x/service"
)

// swagger:parameters reloadConfigRequest
type reloadConfigRequest struct{}

// successful operation.
// swagger:response reloadConfigResponse
type reloadConfigResponse struct {
	Data Response
}

func reloadConfig(ctx *gin.Context) {
	// swagger:route POST /config/reload Reload reloadConfigRequest
	//
	// Hot reload config.
	//
	//     Security:
	//       basicAuth: []
	//
	//     Responses:
	//       200: reloadConfigResponse

	cfg, err := parser.Parse()
	if err != nil {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeInvalid, err.Error()))
		return
	}

	// R19-F02 + R3-A01-003: API 重载与启动/SIGHUP 走同一自愈全集——
	// 共用入口 config.HealPersistedConfig（剥 nil → 17 类去重 → 剪
	// 无引用 conn limiter，逐步重新落盘）。此前本入口只有剥 nil 一
	// 步：重复名毒化配置经 SIGHUP/重启能自愈、经 API 重载却永远在
	// loader.Reload 的 duplicate object 处失败。落盘失败只告警、
	// 不阻塞重载（loader 守卫仍会干净报错）。
	report := config.HealPersistedConfig(cfg)
	if len(report.Stripped) > 0 {
		logger.Default().Warnf("config reload self-heal: removed %s from persisted config", strings.Join(report.Stripped, ", "))
		if report.StripPersistErr != nil {
			logger.Default().Warnf("config reload self-heal: failed to persist nil-stripped config: %v", report.StripPersistErr)
		}
	}
	for _, heal := range report.Duplicates {
		logger.Default().Warnf("config reload self-heal: removed duplicate %s(s) %v from persisted config", heal.Kind, heal.Removed)
		if heal.Err != nil {
			logger.Default().Warnf("config reload self-heal: failed to persist deduplicated config: %v", heal.Err)
		}
	}
	if len(report.Pruned) > 0 {
		logger.Default().Warnf("config reload self-heal: removed unreferenced conn limiter(s) %v from persisted config", report.Pruned)
		if report.PrunePersistErr != nil {
			logger.Default().Warnf("config reload self-heal: failed to persist pruned config: %v", report.PrunePersistErr)
		}
	}

	if err := loader.Reload(cfg, func(*config.Config) error {
		for _, svc := range registry.ServiceRegistry().GetAll() {
			go svc.Serve()
		}
		return nil
	}); err != nil {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeInvalid, err.Error()))
		return
	}
	activeServices := make(map[string]struct{}, len(cfg.Services))
	for _, svc := range cfg.Services {
		if svc != nil {
			activeServices[svc.Name] = struct{}{}
		}
	}
	xservice.GetGlobalTrafficManager().RetainServices(activeServices)

	ctx.JSON(http.StatusOK, Response{
		Msg: "OK",
	})
}
