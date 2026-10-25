package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/go-gost/core/logger"
	"github.com/go-gost/x/config"
	parser "github.com/go-gost/x/config/parsing/auth"
	"github.com/go-gost/x/registry"
)

// swagger:parameters createAutherRequest
type createAutherRequest struct {
	// in: body
	Data config.AutherConfig `json:"data"`
}

// successful operation.
// swagger:response createAutherResponse
type createAutherResponse struct {
	Data Response
}

func createAuther(ctx *gin.Context) {
	// swagger:route POST /config/authers Auther createAutherRequest
	//
	// Create a new auther, the name of the auther must be unique in auther list.
	//
	//     Security:
	//       basicAuth: []
	//
	//     Responses:
	//       200: createAutherResponse

	var req createAutherRequest
	ctx.ShouldBindJSON(&req.Data)

	name := strings.TrimSpace(req.Data.Name)
	if name == "" {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeInvalid, "auther name is required"))
		return
	}
	req.Data.Name = name

	if registry.AutherRegistry().IsRegistered(name) {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeDup, fmt.Sprintf("auther %s already exists", name)))
		return
	}

	v := parser.ParseAuther(&req.Data)
	if err := registry.AutherRegistry().Register(name, v); err != nil {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeDup, fmt.Sprintf("auther %s already exists", name)))
		return
	}

	config.OnUpdate(func(c *config.Config) error {
		// R18-F02/F-04: 按名 upsert（R17-A 同款）。成员已在配置中、但不在注册表里时
		// （例如暂停后 loader 跳过注册），无去重 append 会把第二份同名项写进配置并落盘。
		c.Authers = upsertNamed(c.Authers, &req.Data, func(v *config.AutherConfig) string { return v.Name })
		return nil
	})

	ctx.JSON(http.StatusOK, Response{
		Msg: "OK",
	})
}

// swagger:parameters updateAutherRequest
type updateAutherRequest struct {
	// in: path
	// required: true
	Auther string `uri:"auther" json:"auther"`
	// in: body
	Data config.AutherConfig `json:"data"`
}

// successful operation.
// swagger:response updateAutherResponse
type updateAutherResponse struct {
	Data Response
}

func updateAuther(ctx *gin.Context) {
	// swagger:route PUT /config/authers/{auther} Auther updateAutherRequest
	//
	// Update auther by name, the auther must already exist.
	//
	//     Security:
	//       basicAuth: []
	//
	//     Responses:
	//       200: updateAutherResponse

	var req updateAutherRequest
	ctx.ShouldBindUri(&req)
	ctx.ShouldBindJSON(&req.Data)

	name := strings.TrimSpace(req.Auther)

	if !registry.AutherRegistry().IsRegistered(name) {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeNotFound, fmt.Sprintf("auther %s not found", name)))
		return
	}

	req.Data.Name = name

	v := parser.ParseAuther(&req.Data)
	registry.AutherRegistry().Unregister(name)

	if err := registry.AutherRegistry().Register(name, v); err != nil {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeDup, fmt.Sprintf("auther %s already exists", name)))
		return
	}

	config.OnUpdate(func(c *config.Config) error {
		for i := range c.Authers {
			// R19-F02: nil 项跳过并记日志（原样保留，清理只走启动自愈），不得 panic。
			if c.Authers[i] == nil {
				logger.Default().Warnf("update auther: skipped nil auther entry at index %d", i)
				continue
			}
			if c.Authers[i].Name == name {
				c.Authers[i] = &req.Data
				break
			}
		}
		return nil
	})

	ctx.JSON(http.StatusOK, Response{
		Msg: "OK",
	})
}

// swagger:parameters deleteAutherRequest
type deleteAutherRequest struct {
	// in: path
	// required: true
	Auther string `uri:"auther" json:"auther"`
}

// successful operation.
// swagger:response deleteAutherResponse
type deleteAutherResponse struct {
	Data Response
}

func deleteAuther(ctx *gin.Context) {
	// swagger:route DELETE /config/authers/{auther} Auther deleteAutherRequest
	//
	// Delete auther by name.
	//
	//     Security:
	//       basicAuth: []
	//
	//     Responses:
	//       200: deleteAutherResponse

	var req deleteAutherRequest
	ctx.ShouldBindUri(&req)

	name := strings.TrimSpace(req.Auther)

	if !registry.AutherRegistry().IsRegistered(name) {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeNotFound, fmt.Sprintf("auther %s not found", name)))
		return
	}
	registry.AutherRegistry().Unregister(name)

	config.OnUpdate(func(c *config.Config) error {
		authers := c.Authers
		c.Authers = nil
		for _, s := range authers {
			// R19-F02: nil 项原样保留并记日志，不得 panic（清理只走启动自愈）。
			if s == nil {
				logger.Default().Warnf("delete auther: kept nil auther entry as-is")
				c.Authers = append(c.Authers, s)
				continue
			}
			if s.Name == name {
				continue
			}
			c.Authers = append(c.Authers, s)
		}
		return nil
	})

	ctx.JSON(http.StatusOK, Response{
		Msg: "OK",
	})
}
