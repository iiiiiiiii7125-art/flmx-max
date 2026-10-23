package handler

import (
	"net/http"
	"strings"

	"go-backend/internal/http/response"
	"go-backend/internal/store/repo"
)

func (h *Handler) getPublicConfigByName(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}

	var req nameRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("配置名称不能为空"))
		return
	}

	configName := strings.ToLower(strings.TrimSpace(req.Name))
	if configName == "" {
		response.WriteJSON(w, response.ErrDefault("配置名称不能为空"))
		return
	}
	// R10X-014（A-02）：公开读路径同走 PolicyForConfig 单一裁决
	// 点（仅 public 级可达），与 config/get 口径一致。
	if repo.PolicyForConfig(configName) != repo.ConfigAccessPublic {
		response.WriteJSON(w, response.Err(403, "禁止访问敏感配置"))
		return
	}
	cfg, err := h.repo.GetConfigByName(configName)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if cfg == nil {
		response.WriteJSON(w, response.ErrDefault("配置不存在"))
		return
	}

	response.WriteJSON(w, response.OK(cfg))
}
