package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/go-gost/core/logger"
	"github.com/go-gost/x/config"
	parser "github.com/go-gost/x/config/parsing/sd"
	"github.com/go-gost/x/registry"
)

// swagger:parameters createSDRequest
type createSDRequest struct {
	// in: body
	Data config.SDConfig `json:"data"`
}

// successful operation.
// swagger:response createSDResponse
type createSDResponse struct {
	Data Response
}

func createSD(ctx *gin.Context) {
	// swagger:route POST /config/sds SD createSDRequest
	//
	// Create a new SD, the name of the SD must be unique in SD list.
	//
	//     Security:
	//       basicAuth: []
	//
	//     Responses:
	//       200: createSDResponse

	var req createSDRequest
	ctx.ShouldBindJSON(&req.Data)

	name := strings.TrimSpace(req.Data.Name)
	if name == "" {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeInvalid, "sd name is required"))
		return
	}
	req.Data.Name = name

	if registry.SDRegistry().IsRegistered(name) {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeDup, fmt.Sprintf("sd %s already exists", name)))
		return
	}

	v := parser.ParseSD(&req.Data)

	if err := registry.SDRegistry().Register(name, v); err != nil {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeDup, fmt.Sprintf("sd %s already exists", name)))
		return
	}

	config.OnUpdate(func(c *config.Config) error {
		// R18-F02/F-04: 按名 upsert（R17-A 同款）。成员已在配置中、但不在注册表里时
		// （例如暂停后 loader 跳过注册），无去重 append 会把第二份同名项写进配置并落盘。
		c.SDs = upsertNamed(c.SDs, &req.Data, func(v *config.SDConfig) string { return v.Name })
		return nil
	})

	ctx.JSON(http.StatusOK, Response{
		Msg: "OK",
	})
}

// swagger:parameters updateSDRequest
type updateSDRequest struct {
	// in: path
	// required: true
	SD string `uri:"sd" json:"sd"`
	// in: body
	Data config.SDConfig `json:"data"`
}

// successful operation.
// swagger:response updateSDResponse
type updateSDResponse struct {
	Data Response
}

func updateSD(ctx *gin.Context) {
	// swagger:route PUT /config/sds/{sd} SD updateSDRequest
	//
	// Update SD by name, the SD must already exist.
	//
	//     Security:
	//       basicAuth: []
	//
	//     Responses:
	//       200: updateSDResponse

	var req updateSDRequest
	ctx.ShouldBindUri(&req)
	ctx.ShouldBindJSON(&req.Data)

	name := strings.TrimSpace(req.SD)

	if !registry.SDRegistry().IsRegistered(name) {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeNotFound, fmt.Sprintf("sd %s not found", name)))
		return
	}

	req.Data.Name = name

	v := parser.ParseSD(&req.Data)

	registry.SDRegistry().Unregister(name)

	if err := registry.SDRegistry().Register(name, v); err != nil {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeDup, fmt.Sprintf("sd %s already exists", name)))
		return
	}

	config.OnUpdate(func(c *config.Config) error {
		for i := range c.SDs {
			// R19-F02: nil 项跳过并记日志（原样保留，清理只走启动自愈），不得 panic。
			if c.SDs[i] == nil {
				logger.Default().Warnf("update sd: skipped nil sd entry at index %d", i)
				continue
			}
			if c.SDs[i].Name == name {
				c.SDs[i] = &req.Data
				break
			}
		}
		return nil
	})

	ctx.JSON(http.StatusOK, Response{
		Msg: "OK",
	})
}

// swagger:parameters deleteSDRequest
type deleteSDRequest struct {
	// in: path
	// required: true
	SD string `uri:"sd" json:"sd"`
}

// successful operation.
// swagger:response deleteSDResponse
type deleteSDResponse struct {
	Data Response
}

func deleteSD(ctx *gin.Context) {
	// swagger:route DELETE /config/sds/{sd} SD deleteSDRequest
	//
	// Delete SD by name.
	//
	//     Security:
	//       basicAuth: []
	//
	//     Responses:
	//       200: deleteSDResponse

	var req deleteSDRequest
	ctx.ShouldBindUri(&req)

	name := strings.TrimSpace(req.SD)

	if !registry.SDRegistry().IsRegistered(name) {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeNotFound, fmt.Sprintf("sd %s not found", name)))
		return
	}
	registry.SDRegistry().Unregister(name)

	config.OnUpdate(func(c *config.Config) error {
		sds := c.SDs
		c.SDs = nil
		for _, s := range sds {
			// R19-F02: nil 项原样保留并记日志，不得 panic（清理只走启动自愈）。
			if s == nil {
				logger.Default().Warnf("delete sd: kept nil sd entry as-is")
				c.SDs = append(c.SDs, s)
				continue
			}
			if s.Name == name {
				continue
			}
			c.SDs = append(c.SDs, s)
		}
		return nil
	})

	ctx.JSON(http.StatusOK, Response{
		Msg: "OK",
	})
}
