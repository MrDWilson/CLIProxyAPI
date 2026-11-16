package handlers

import "github.com/gin-gonic/gin"

// FilterRoutingModels applies the authenticated caller's credential/model policy
// before protocol-specific model-list formatting (including Claude aliases).
func (h *BaseAPIHandler) FilterRoutingModels(c *gin.Context, models []map[string]any) []map[string]any {
	if h == nil || h.AuthManager == nil || c == nil || c.Request == nil {
		return models
	}
	return h.AuthManager.FilterRoutingModels(c.Request.Context(), models)
}
