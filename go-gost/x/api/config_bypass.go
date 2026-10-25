package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/go-gost/core/logger"
	"github.com/go-gost/x/config"
	parser "github.com/go-gost/x/config/parsing/bypass"
	"github.com/go-gost/x/registry"
)

// swagger:parameters createBypassRequest
type createBypassRequest struct {
	// in: body
	Data config.BypassConfig `json:"data"`
}

// successful operation.
// swagger:response createBypassResponse
type createBypassResponse struct {
	Data Response
}

func createBypass(ctx *gin.Context) {
	// swagger:route POST /config/bypasses Bypass createBypassRequest
	//
	// Create a new bypass, the name of bypass must be unique in bypass list.
	//
	//     Security:
	//       basicAuth: []
	//
	//     Responses:
	//       200: createBypassResponse

	var req createBypassRequest
	ctx.ShouldBindJSON(&req.Data)

	name := strings.TrimSpace(req.Data.Name)
	if name == "" {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeInvalid, "bypass name is required"))
		return
	}
	req.Data.Name = name

	if registry.BypassRegistry().IsRegistered(name) {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeDup, fmt.Sprintf("bypass %s already exists", name)))
		return
	}

	v := parser.ParseBypass(&req.Data)

	if err := registry.BypassRegistry().Register(name, v); err != nil {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeDup, fmt.Sprintf("bypass %s already exists", name)))
		return
	}

	config.OnUpdate(func(c *config.Config) error {
		// R18-F02/F-04: 按名 upsert（R17-A 同款）。成员已在配置中、但不在注册表里时
		// （例如暂停后 loader 跳过注册），无去重 append 会把第二份同名项写进配置并落盘。
		c.Bypasses = upsertNamed(c.Bypasses, &req.Data, func(v *config.BypassConfig) string { return v.Name })
		return nil
	})

	ctx.JSON(http.StatusOK, Response{
		Msg: "OK",
	})
}

// swagger:parameters updateBypassRequest
type updateBypassRequest struct {
	// in: path
	// required: true
	Bypass string `uri:"bypass" json:"bypass"`
	// in: body
	Data config.BypassConfig `json:"data"`
}

// successful operation.
// swagger:response updateBypassResponse
type updateBypassResponse struct {
	Data Response
}

func updateBypass(ctx *gin.Context) {
	// swagger:route PUT /config/bypasses/{bypass} Bypass updateBypassRequest
	//
	// Update bypass by name, the bypass must already exist.
	//
	//     Security:
	//       basicAuth: []
	//
	//     Responses:
	//       200: updateBypassResponse

	var req updateBypassRequest
	ctx.ShouldBindUri(&req)
	ctx.ShouldBindJSON(&req.Data)

	name := strings.TrimSpace(req.Bypass)

	if !registry.BypassRegistry().IsRegistered(name) {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeNotFound, fmt.Sprintf("bypass %s not found", name)))
		return
	}

	req.Data.Name = name

	v := parser.ParseBypass(&req.Data)

	registry.BypassRegistry().Unregister(name)

	if err := registry.BypassRegistry().Register(name, v); err != nil {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeDup, fmt.Sprintf("bypass %s already exists", name)))
		return
	}

	config.OnUpdate(func(c *config.Config) error {
		for i := range c.Bypasses {
			// R19-F02: nil 项跳过并记日志（原样保留，清理只走启动自愈），不得 panic。
			if c.Bypasses[i] == nil {
				logger.Default().Warnf("update bypass: skipped nil bypass entry at index %d", i)
				continue
			}
			if c.Bypasses[i].Name == name {
				c.Bypasses[i] = &req.Data
				break
			}
		}
		return nil
	})

	ctx.JSON(http.StatusOK, Response{
		Msg: "OK",
	})
}

// swagger:parameters deleteBypassRequest
type deleteBypassRequest struct {
	// in: path
	// required: true
	Bypass string `uri:"bypass" json:"bypass"`
}

// successful operation.
// swagger:response deleteBypassResponse
type deleteBypassResponse struct {
	Data Response
}

func deleteBypass(ctx *gin.Context) {
	// swagger:route DELETE /config/bypasses/{bypass} Bypass deleteBypassRequest
	//
	// Delete bypass by name.
	//
	//     Security:
	//       basicAuth: []
	//
	//     Responses:
	//       200: deleteBypassResponse

	var req deleteBypassRequest
	ctx.ShouldBindUri(&req)

	name := strings.TrimSpace(req.Bypass)

	if !registry.BypassRegistry().IsRegistered(name) {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeNotFound, fmt.Sprintf("bypass %s not found", name)))
		return
	}
	registry.BypassRegistry().Unregister(name)

	config.OnUpdate(func(c *config.Config) error {
		bypasses := c.Bypasses
		c.Bypasses = nil
		for _, s := range bypasses {
			// R19-F02: nil 项原样保留并记日志，不得 panic（清理只走启动自愈）。
			if s == nil {
				logger.Default().Warnf("delete bypass: kept nil bypass entry as-is")
				c.Bypasses = append(c.Bypasses, s)
				continue
			}
			if s.Name == name {
				continue
			}
			c.Bypasses = append(c.Bypasses, s)
		}
		return nil
	})

	ctx.JSON(http.StatusOK, Response{
		Msg: "OK",
	})
}
