package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/go-gost/core/logger"
	"github.com/go-gost/x/config"
	parser "github.com/go-gost/x/config/parsing/hosts"
	"github.com/go-gost/x/registry"
)

// swagger:parameters createHostsRequest
type createHostsRequest struct {
	// in: body
	Data config.HostsConfig `json:"data"`
}

// successful operation.
// swagger:response createHostsResponse
type createHostsesponse struct {
	Data Response
}

func createHosts(ctx *gin.Context) {
	// swagger:route POST /config/hosts Hosts createHostsRequest
	//
	// Create a new hosts, the name of the hosts must be unique in hosts list.
	//
	//     Security:
	//       basicAuth: []
	//
	//     Responses:
	//       200: createHostsResponse

	var req createHostsRequest
	ctx.ShouldBindJSON(&req.Data)

	name := strings.TrimSpace(req.Data.Name)
	if name == "" {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeInvalid, "hosts name is required"))
		return
	}
	req.Data.Name = name

	if registry.HostsRegistry().IsRegistered(name) {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeDup, fmt.Sprintf("hosts %s already exists", name)))
		return
	}

	v := parser.ParseHostMapper(&req.Data)

	if err := registry.HostsRegistry().Register(name, v); err != nil {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeDup, fmt.Sprintf("hosts %s already exists", name)))
		return
	}

	config.OnUpdate(func(c *config.Config) error {
		// R18-F02/F-04: 按名 upsert（R17-A 同款）。成员已在配置中、但不在注册表里时
		// （例如暂停后 loader 跳过注册），无去重 append 会把第二份同名项写进配置并落盘。
		c.Hosts = upsertNamed(c.Hosts, &req.Data, func(v *config.HostsConfig) string { return v.Name })
		return nil
	})

	ctx.JSON(http.StatusOK, Response{
		Msg: "OK",
	})
}

// swagger:parameters updateHostsRequest
type updateHostsRequest struct {
	// in: path
	// required: true
	Hosts string `uri:"hosts" json:"hosts"`
	// in: body
	Data config.HostsConfig `json:"data"`
}

// successful operation.
// swagger:response updateHostsResponse
type updateHostsResponse struct {
	Data Response
}

func updateHosts(ctx *gin.Context) {
	// swagger:route PUT /config/hosts/{hosts} Hosts updateHostsRequest
	//
	// Update hosts by name, the hosts must already exist.
	//
	//     Security:
	//       basicAuth: []
	//
	//     Responses:
	//       200: updateHostsResponse

	var req updateHostsRequest
	ctx.ShouldBindUri(&req)
	ctx.ShouldBindJSON(&req.Data)

	name := strings.TrimSpace(req.Hosts)

	if !registry.HostsRegistry().IsRegistered(name) {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeNotFound, fmt.Sprintf("hosts %s not found", name)))
		return
	}

	req.Data.Name = name

	v := parser.ParseHostMapper(&req.Data)

	registry.HostsRegistry().Unregister(name)

	if err := registry.HostsRegistry().Register(name, v); err != nil {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeDup, fmt.Sprintf("hosts %s already exists", name)))
		return
	}

	config.OnUpdate(func(c *config.Config) error {
		for i := range c.Hosts {
			// R19-F02: nil 项跳过并记日志（原样保留，清理只走启动自愈），不得 panic。
			if c.Hosts[i] == nil {
				logger.Default().Warnf("update hosts: skipped nil hosts entry at index %d", i)
				continue
			}
			if c.Hosts[i].Name == name {
				c.Hosts[i] = &req.Data
				break
			}
		}
		return nil
	})

	ctx.JSON(http.StatusOK, Response{
		Msg: "OK",
	})
}

// swagger:parameters deleteHostsRequest
type deleteHostsRequest struct {
	// in: path
	// required: true
	Hosts string `uri:"hosts" json:"hosts"`
}

// successful operation.
// swagger:response deleteHostsResponse
type deleteHostsResponse struct {
	Data Response
}

func deleteHosts(ctx *gin.Context) {
	// swagger:route DELETE /config/hosts/{hosts} Hosts deleteHostsRequest
	//
	// Delete hosts by name.
	//
	//     Security:
	//       basicAuth: []
	//
	//     Responses:
	//       200: deleteHostsResponse

	var req deleteHostsRequest
	ctx.ShouldBindUri(&req)

	name := strings.TrimSpace(req.Hosts)

	if !registry.HostsRegistry().IsRegistered(name) {
		writeError(ctx, NewError(http.StatusBadRequest, ErrCodeNotFound, fmt.Sprintf("hosts %s not found", name)))
		return
	}
	registry.HostsRegistry().Unregister(name)

	config.OnUpdate(func(c *config.Config) error {
		hosts := c.Hosts
		c.Hosts = nil
		for _, s := range hosts {
			// R19-F02: nil 项原样保留并记日志，不得 panic（清理只走启动自愈）。
			if s == nil {
				logger.Default().Warnf("delete hosts: kept nil hosts entry as-is")
				c.Hosts = append(c.Hosts, s)
				continue
			}
			if s.Name == name {
				continue
			}
			c.Hosts = append(c.Hosts, s)
		}
		return nil
	})

	ctx.JSON(http.StatusOK, Response{
		Msg: "OK",
	})
}
