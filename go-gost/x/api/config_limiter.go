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

// swagger:parameters createLimiterRequest
type createLimiterRequest struct {
	// in: body
	Data config.LimiterConfig `json:"data"`
}

// successful operation.
// swagger:response createLimiterResponse
type createLimiterResponse struct {
	Data Response
}

func createLimiter(ctx *gin.Context) {
	// swagger:route POST /config/limiters Limiter createLimiterRequest
	//
	// Create a new limiter, the name of limiter must be unique in limiter list.
	//
	//     Security:
	//       basicAuth: []
	//
	//     Responses:
	//       200: createLimiterResponse

	var req createLimiterRequest
	ctx.ShouldBindJSON(&req.Data)

	name := strings.TrimSpace(req.Data.Name)
	if name == "" {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeInvalid, "limiter name is required"))
		return
	}
	req.Data.Name = name

	if registry.TrafficLimiterRegistry().IsRegistered(name) {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeDup, fmt.Sprintf("limiter %s already exists", name)))
		return
	}

	v := parser.ParseTrafficLimiter(&req.Data)

	if err := registry.TrafficLimiterRegistry().Register(name, v); err != nil {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeDup, fmt.Sprintf("limiter %s already exists", name)))
		return
	}

	config.OnUpdate(func(c *config.Config) error {
		// R18-F02/F-04: 按名 upsert（R17-A 同款）。成员已在配置中、但不在注册表里时
		// （例如暂停后 loader 跳过注册），无去重 append 会把第二份同名项写进配置并落盘。
		c.Limiters = upsertNamed(c.Limiters, &req.Data, func(v *config.LimiterConfig) string { return v.Name })
		return nil
	})

	ctx.JSON(http.StatusOK, Response{
		Msg: "OK",
	})
}

// swagger:parameters updateLimiterRequest
type updateLimiterRequest struct {
	// in: path
	// required: true
	Limiter string `uri:"limiter" json:"limiter"`
	// in: body
	Data config.LimiterConfig `json:"data"`
}

// successful operation.
// swagger:response updateLimiterResponse
type updateLimiterResponse struct {
	Data Response
}

func updateLimiter(ctx *gin.Context) {
	// swagger:route PUT /config/limiters/{limiter} Limiter updateLimiterRequest
	//
	// Update limiter by name, the limiter must already exist.
	//
	//     Security:
	//       basicAuth: []
	//
	//     Responses:
	//       200: updateLimiterResponse

	var req updateLimiterRequest
	ctx.ShouldBindUri(&req)
	ctx.ShouldBindJSON(&req.Data)

	name := strings.TrimSpace(req.Limiter)

	if !registry.TrafficLimiterRegistry().IsRegistered(name) {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeNotFound, fmt.Sprintf("limiter %s not found", name)))
		return
	}

	req.Data.Name = name

	v := parser.ParseTrafficLimiter(&req.Data)

	registry.TrafficLimiterRegistry().Unregister(name)

	if err := registry.TrafficLimiterRegistry().Register(name, v); err != nil {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeDup, fmt.Sprintf("limiter %s already exists", name)))
		return
	}

	config.OnUpdate(func(c *config.Config) error {
		for i := range c.Limiters {
			// R19-F02: nil 项跳过并记日志（原样保留，清理只走启动自愈），不得 panic。
			if c.Limiters[i] == nil {
				logger.Default().Warnf("update limiter: skipped nil limiter entry at index %d", i)
				continue
			}
			if c.Limiters[i].Name == name {
				c.Limiters[i] = &req.Data
				break
			}
		}
		return nil
	})

	ctx.JSON(http.StatusOK, Response{
		Msg: "OK",
	})
}

// swagger:parameters deleteLimiterRequest
type deleteLimiterRequest struct {
	// in: path
	// required: true
	Limiter string `uri:"limiter" json:"limiter"`
}

// successful operation.
// swagger:response deleteLimiterResponse
type deleteLimiterResponse struct {
	Data Response
}

func deleteLimiter(ctx *gin.Context) {
	// swagger:route DELETE /config/limiters/{limiter} Limiter deleteLimiterRequest
	//
	// Delete limiter by name.
	//
	//     Security:
	//       basicAuth: []
	//
	//     Responses:
	//       200: deleteLimiterResponse

	var req deleteLimiterRequest
	ctx.ShouldBindUri(&req)

	name := strings.TrimSpace(req.Limiter)

	if !registry.TrafficLimiterRegistry().IsRegistered(name) {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeNotFound, fmt.Sprintf("limiter %s not found", name)))
		return
	}
	registry.TrafficLimiterRegistry().Unregister(name)

	config.OnUpdate(func(c *config.Config) error {
		limiteres := c.Limiters
		c.Limiters = nil
		for _, s := range limiteres {
			// R19-F02: nil 项原样保留并记日志，不得 panic（清理只走启动自愈）。
			if s == nil {
				logger.Default().Warnf("delete limiter: kept nil limiter entry as-is")
				c.Limiters = append(c.Limiters, s)
				continue
			}
			if s.Name == name {
				continue
			}
			c.Limiters = append(c.Limiters, s)
		}
		return nil
	})

	ctx.JSON(http.StatusOK, Response{
		Msg: "OK",
	})
}
