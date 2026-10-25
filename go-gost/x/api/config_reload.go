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

	// R19-F02: API 重载与 SIGHUP 走同一自愈——先剥离落盘 JSON null 读入
	// 的 nil 项并重新落盘，否则 loader.Load 在首个 nil 类 panic（经 gin
	// Recovery 变 500，但文件仍毒化、下次重载/启动仍失败）。落盘失败只
	// 告警、不阻塞重载（loader 守卫仍会干净报错）。
	if stripped := config.StripNilConfigItems(cfg); len(stripped) > 0 {
		logger.Default().Warnf("config reload self-heal: removed %s from persisted config", strings.Join(stripped, ", "))
		if err := config.PersistNow(cfg); err != nil {
			logger.Default().Warnf("config reload self-heal: failed to persist nil-stripped config: %v", err)
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
