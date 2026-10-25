package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/go-gost/core/logger"
	"github.com/go-gost/x/config"
	parser "github.com/go-gost/x/config/parsing/ingress"
	"github.com/go-gost/x/registry"
)

// swagger:parameters createIngressRequest
type createIngressRequest struct {
	// in: body
	Data config.IngressConfig `json:"data"`
}

// successful operation.
// swagger:response createIngressResponse
type createIngressResponse struct {
	Data Response
}

func createIngress(ctx *gin.Context) {
	// swagger:route POST /config/ingresses Ingress createIngressRequest
	//
	// Create a new ingress, the name of the ingress must be unique in ingress list.
	//
	//     Security:
	//       basicAuth: []
	//
	//     Responses:
	//       200: createIngressResponse

	var req createIngressRequest
	ctx.ShouldBindJSON(&req.Data)

	name := strings.TrimSpace(req.Data.Name)
	if name == "" {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeInvalid, "ingress name is required"))
		return
	}
	req.Data.Name = name

	if registry.IngressRegistry().IsRegistered(name) {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeDup, fmt.Sprintf("ingress %s already exists", name)))
		return
	}

	v := parser.ParseIngress(&req.Data)

	if err := registry.IngressRegistry().Register(name, v); err != nil {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeDup, fmt.Sprintf("ingress %s already exists", name)))
		return
	}

	config.OnUpdate(func(c *config.Config) error {
		// R18-F02/F-04: 按名 upsert（R17-A 同款）。成员已在配置中、但不在注册表里时
		// （例如暂停后 loader 跳过注册），无去重 append 会把第二份同名项写进配置并落盘。
		c.Ingresses = upsertNamed(c.Ingresses, &req.Data, func(v *config.IngressConfig) string { return v.Name })
		return nil
	})

	ctx.JSON(http.StatusOK, Response{
		Msg: "OK",
	})
}

// swagger:parameters updateIngressRequest
type updateIngressRequest struct {
	// in: path
	// required: true
	Ingress string `uri:"ingress" json:"ingress"`
	// in: body
	Data config.IngressConfig `json:"data"`
}

// successful operation.
// swagger:response updateIngressResponse
type updateIngressResponse struct {
	Data Response
}

func updateIngress(ctx *gin.Context) {
	// swagger:route PUT /config/ingresses/{ingress} Ingress updateIngressRequest
	//
	// Update ingress by name, the ingress must already exist.
	//
	//     Security:
	//       basicAuth: []
	//
	//     Responses:
	//       200: updateIngressResponse

	var req updateIngressRequest
	ctx.ShouldBindUri(&req)
	ctx.ShouldBindJSON(&req.Data)

	name := strings.TrimSpace(req.Ingress)

	if !registry.IngressRegistry().IsRegistered(name) {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeNotFound, fmt.Sprintf("ingress %s not found", name)))
		return
	}

	req.Data.Name = name

	v := parser.ParseIngress(&req.Data)

	registry.IngressRegistry().Unregister(name)

	if err := registry.IngressRegistry().Register(name, v); err != nil {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeDup, fmt.Sprintf("ingress %s already exists", name)))
		return
	}

	config.OnUpdate(func(c *config.Config) error {
		for i := range c.Ingresses {
			// R19-F02: nil 项跳过并记日志（原样保留，清理只走启动自愈），不得 panic。
			if c.Ingresses[i] == nil {
				logger.Default().Warnf("update ingress: skipped nil ingress entry at index %d", i)
				continue
			}
			if c.Ingresses[i].Name == name {
				c.Ingresses[i] = &req.Data
				break
			}
		}
		return nil
	})

	ctx.JSON(http.StatusOK, Response{
		Msg: "OK",
	})
}

// swagger:parameters deleteIngressRequest
type deleteIngressRequest struct {
	// in: path
	// required: true
	Ingress string `uri:"ingress" json:"ingress"`
}

// successful operation.
// swagger:response deleteIngressResponse
type deleteIngressResponse struct {
	Data Response
}

func deleteIngress(ctx *gin.Context) {
	// swagger:route DELETE /config/ingresses/{ingress} Ingress deleteIngressRequest
	//
	// Delete ingress by name.
	//
	//     Security:
	//       basicAuth: []
	//
	//     Responses:
	//       200: deleteIngressResponse

	var req deleteIngressRequest
	ctx.ShouldBindUri(&req)

	name := strings.TrimSpace(req.Ingress)

	if !registry.IngressRegistry().IsRegistered(name) {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeNotFound, fmt.Sprintf("ingress %s not found", name)))
		return
	}
	registry.IngressRegistry().Unregister(name)

	config.OnUpdate(func(c *config.Config) error {
		ingresses := c.Ingresses
		c.Ingresses = nil
		for _, s := range ingresses {
			// R19-F02: nil 项原样保留并记日志，不得 panic（清理只走启动自愈）。
			if s == nil {
				logger.Default().Warnf("delete ingress: kept nil ingress entry as-is")
				c.Ingresses = append(c.Ingresses, s)
				continue
			}
			if s.Name == name {
				continue
			}
			c.Ingresses = append(c.Ingresses, s)
		}
		return nil
	})

	ctx.JSON(http.StatusOK, Response{
		Msg: "OK",
	})
}
