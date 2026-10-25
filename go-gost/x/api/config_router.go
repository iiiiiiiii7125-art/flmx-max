package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/go-gost/core/logger"
	"github.com/go-gost/x/config"
	parser "github.com/go-gost/x/config/parsing/router"
	"github.com/go-gost/x/registry"
)

// swagger:parameters createRouterRequest
type createRouterRequest struct {
	// in: body
	Data config.RouterConfig `json:"data"`
}

// successful operation.
// swagger:response createRouterResponse
type createRouterResponse struct {
	Data Response
}

func createRouter(ctx *gin.Context) {
	// swagger:route POST /config/routers Router createRouterRequest
	//
	// Create a new router, the name of the router must be unique in router list.
	//
	//     Security:
	//       basicAuth: []
	//
	//     Responses:
	//       200: createRouterResponse

	var req createRouterRequest
	ctx.ShouldBindJSON(&req.Data)

	name := strings.TrimSpace(req.Data.Name)
	if name == "" {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeInvalid, "router name is required"))
		return
	}
	req.Data.Name = name

	if registry.RouterRegistry().IsRegistered(name) {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeDup, fmt.Sprintf("router %s already exists", name)))
		return
	}

	v := parser.ParseRouter(&req.Data)

	if err := registry.RouterRegistry().Register(name, v); err != nil {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeDup, fmt.Sprintf("router %s already exists", name)))
		return
	}

	config.OnUpdate(func(c *config.Config) error {
		// R18-F02/F-04: 按名 upsert（R17-A 同款）。成员已在配置中、但不在注册表里时
		// （例如暂停后 loader 跳过注册），无去重 append 会把第二份同名项写进配置并落盘。
		c.Routers = upsertNamed(c.Routers, &req.Data, func(v *config.RouterConfig) string { return v.Name })
		return nil
	})

	ctx.JSON(http.StatusOK, Response{
		Msg: "OK",
	})
}

// swagger:parameters updateRouterRequest
type updateRouterRequest struct {
	// in: path
	// required: true
	Router string `uri:"router" json:"router"`
	// in: body
	Data config.RouterConfig `json:"data"`
}

// successful operation.
// swagger:response updateRouterResponse
type updateRouterResponse struct {
	Data Response
}

func updateRouter(ctx *gin.Context) {
	// swagger:route PUT /config/routers/{router} Router updateRouterRequest
	//
	// Update router by name, the router must already exist.
	//
	//     Security:
	//       basicAuth: []
	//
	//     Responses:
	//       200: updateRouterResponse

	var req updateRouterRequest
	ctx.ShouldBindUri(&req)
	ctx.ShouldBindJSON(&req.Data)

	name := strings.TrimSpace(req.Router)

	if !registry.RouterRegistry().IsRegistered(name) {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeNotFound, fmt.Sprintf("router %s not found", name)))
		return
	}

	req.Data.Name = name

	v := parser.ParseRouter(&req.Data)

	registry.RouterRegistry().Unregister(name)

	if err := registry.RouterRegistry().Register(name, v); err != nil {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeDup, fmt.Sprintf("router %s already exists", name)))
		return
	}

	config.OnUpdate(func(c *config.Config) error {
		for i := range c.Routers {
			// R19-F02: nil 项跳过并记日志（原样保留，清理只走启动自愈），不得 panic。
			if c.Routers[i] == nil {
				logger.Default().Warnf("update router: skipped nil router entry at index %d", i)
				continue
			}
			if c.Routers[i].Name == name {
				c.Routers[i] = &req.Data
				break
			}
		}
		return nil
	})

	ctx.JSON(http.StatusOK, Response{
		Msg: "OK",
	})
}

// swagger:parameters deleteRouterRequest
type deleteRouterRequest struct {
	// in: path
	// required: true
	Router string `uri:"router" json:"router"`
}

// successful operation.
// swagger:response deleteRouterResponse
type deleteRouterResponse struct {
	Data Response
}

func deleteRouter(ctx *gin.Context) {
	// swagger:route DELETE /config/routers/{router} Router deleteRouterRequest
	//
	// Delete router by name.
	//
	//     Security:
	//       basicAuth: []
	//
	//     Responses:
	//       200: deleteRouterResponse

	var req deleteRouterRequest
	ctx.ShouldBindUri(&req)

	name := strings.TrimSpace(req.Router)

	if !registry.RouterRegistry().IsRegistered(name) {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeNotFound, fmt.Sprintf("router %s not found", name)))
		return
	}
	registry.RouterRegistry().Unregister(name)

	config.OnUpdate(func(c *config.Config) error {
		routeres := c.Routers
		c.Routers = nil
		for _, s := range routeres {
			// R19-F02: nil 项原样保留并记日志，不得 panic（清理只走启动自愈）。
			if s == nil {
				logger.Default().Warnf("delete router: kept nil router entry as-is")
				c.Routers = append(c.Routers, s)
				continue
			}
			if s.Name == name {
				continue
			}
			c.Routers = append(c.Routers, s)
		}
		return nil
	})

	ctx.JSON(http.StatusOK, Response{
		Msg: "OK",
	})
}
