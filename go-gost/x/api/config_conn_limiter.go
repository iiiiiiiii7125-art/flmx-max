package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/go-gost/core/logger"
	"github.com/go-gost/x/config"
	parser "github.com/go-gost/x/config/parsing/limiter"
	"github.com/go-gost/x/registry"
)

// swagger:parameters createConnLimiterRequest
type createConnLimiterRequest struct {
	// in: body
	Data config.LimiterConfig `json:"data"`
}

// successful operation.
// swagger:response createConnLimiterResponse
type createConnLimiterResponse struct {
	Data Response
}

func createConnLimiter(ctx *gin.Context) {
	// swagger:route POST /config/climiters Limiter createConnLimiterRequest
	//
	// Create a new conn limiter, the name of limiter must be unique in limiter list.
	//
	//     Security:
	//       basicAuth: []
	//
	//     Responses:
	//       200: createConnLimiterResponse

	var req createConnLimiterRequest
	ctx.ShouldBindJSON(&req.Data)

	name := strings.TrimSpace(req.Data.Name)
	if name == "" {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeInvalid, "limiter name is required"))
		return
	}
	req.Data.Name = name

	if registry.ConnLimiterRegistry().IsRegistered(name) {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeDup, fmt.Sprintf("limiter %s already exists", name)))
		return
	}

	v := parser.ParseConnLimiter(&req.Data)
	if err := registry.ConnLimiterRegistry().Register(name, v); err != nil {
		writeError(ctx, NewError(http.StatusInternalServerError, ErrCodeFailed, fmt.Sprintf("create limmiter %s failed: %s", name, err.Error())))
		return
	}

	config.OnUpdate(func(c *config.Config) error {
		// R18-F02/F-04: 按名 upsert（R17-A 同款）。成员已在配置中、但不在注册表里时
		// （例如暂停后 loader 跳过注册），无去重 append 会把第二份同名项写进配置并落盘。
		c.CLimiters = upsertNamed(c.CLimiters, &req.Data, func(v *config.LimiterConfig) string { return v.Name })
		return nil
	})

	ctx.JSON(http.StatusOK, Response{
		Msg: "OK",
	})
}

// swagger:parameters updateConnLimiterRequest
type updateConnLimiterRequest struct {
	// in: path
	// required: true
	Limiter string `uri:"limiter" json:"limiter"`
	// in: body
	Data config.LimiterConfig `json:"data"`
}

// successful operation.
// swagger:response updateConnLimiterResponse
type updateConnLimiterResponse struct {
	Data Response
}

func updateConnLimiter(ctx *gin.Context) {
	// swagger:route PUT /config/climiters/{limiter} Limiter updateConnLimiterRequest
	//
	// Update conn limiter by name, the limiter must already exist.
	//
	//     Security:
	//       basicAuth: []
	//
	//     Responses:
	//       200: updateConnLimiterResponse

	var req updateConnLimiterRequest
	ctx.ShouldBindUri(&req)
	ctx.ShouldBindJSON(&req.Data)

	name := strings.TrimSpace(req.Limiter)

	if !registry.ConnLimiterRegistry().IsRegistered(name) {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeNotFound, fmt.Sprintf("limiter %s not found", name)))
		return
	}

	req.Data.Name = name

	v := parser.ParseConnLimiter(&req.Data)

	registry.ConnLimiterRegistry().Unregister(name)

	if err := registry.ConnLimiterRegistry().Register(name, v); err != nil {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeDup, fmt.Sprintf("limiter %s already exists", name)))
		return
	}

	config.OnUpdate(func(c *config.Config) error {
		for i := range c.CLimiters {
			// R19-F02: nil 项跳过并记日志（原样保留，清理只走启动自愈），不得 panic。
			if c.CLimiters[i] == nil {
				logger.Default().Warnf("update conn limiter: skipped nil conn limiter entry at index %d", i)
				continue
			}
			if c.CLimiters[i].Name == name {
				c.CLimiters[i] = &req.Data
				break
			}
		}
		return nil
	})

	ctx.JSON(http.StatusOK, Response{
		Msg: "OK",
	})
}

// swagger:parameters deleteConnLimiterRequest
type deleteConnLimiterRequest struct {
	// in: path
	// required: true
	Limiter string `uri:"limiter" json:"limiter"`
}

// successful operation.
// swagger:response deleteConnLimiterResponse
type deleteConnLimiterResponse struct {
	Data Response
}

func deleteConnLimiter(ctx *gin.Context) {
	// swagger:route DELETE /config/climiters/{limiter} Limiter deleteConnLimiterRequest
	//
	// Delete conn limiter by name.
	//
	//     Security:
	//       basicAuth: []
	//
	//     Responses:
	//       200: deleteConnLimiterResponse

	var req deleteConnLimiterRequest
	ctx.ShouldBindUri(&req)

	name := strings.TrimSpace(req.Limiter)

	if !registry.ConnLimiterRegistry().IsRegistered(name) {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeNotFound, fmt.Sprintf("limiter %s not found", name)))
		return
	}
	registry.ConnLimiterRegistry().Unregister(name)

	config.OnUpdate(func(c *config.Config) error {
		limiteres := c.CLimiters
		c.CLimiters = nil
		for _, s := range limiteres {
			// R19-F02: nil 项原样保留并记日志，不得 panic（清理只走启动自愈）。
			if s == nil {
				logger.Default().Warnf("delete conn limiter: kept nil conn limiter entry as-is")
				c.CLimiters = append(c.CLimiters, s)
				continue
			}
			if s.Name == name {
				continue
			}
			c.CLimiters = append(c.CLimiters, s)
		}
		return nil
	})

	ctx.JSON(http.StatusOK, Response{
		Msg: "OK",
	})
}
