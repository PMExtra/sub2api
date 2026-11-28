package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func (h *OpenAIGatewayHandler) prepareCodexGroupRouting(c *gin.Context, apiKey *service.APIKey, body []byte) bool {
	ctx, err := h.gatewayService.WithCodexGroupRouting(c, apiKey.Group, body)
	if err == nil {
		c.Request = c.Request.WithContext(ctx)
		return true
	}
	status, code, message := http.StatusServiceUnavailable, "api_error", "Codex fallback group is unavailable"
	if errors.Is(err, service.ErrCodexOnly) {
		service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalPolicyDenied)
		status, code, message = http.StatusForbidden, "permission_error", err.Error()
	} else {
		var groupID int64
		if apiKey.Group != nil {
			groupID = apiKey.Group.ID
		}
		logger.FromContext(c.Request.Context()).With(
			zap.String("component", "handler.openai_gateway.codex_group_routing"),
			zap.Int64("group_id", groupID),
			zap.Error(err),
		).Warn("openai.codex_fallback_group_unavailable")
	}
	if strings.Contains(c.Request.URL.Path, "/messages") {
		h.anthropicErrorResponse(c, status, code, message)
	} else {
		h.errorResponse(c, status, code, message)
	}
	return false
}
