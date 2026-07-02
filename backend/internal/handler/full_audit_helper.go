package handler

import (
	"context"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

type fullAuditChecker interface {
	Check(ctx context.Context, input service.FullAuditCheckInput) (*service.FullAuditDecision, error)
}

func (h *GatewayHandler) checkFullAudit(c *gin.Context, reqLog *zap.Logger, apiKey *service.APIKey, subject middleware2.AuthSubject, protocol string, model string, body []byte) *service.FullAuditDecision {
	if h == nil || h.fullAuditService == nil {
		return nil
	}
	return runFullAudit(c, reqLog, h.fullAuditService, apiKey, subject, protocol, model, body)
}

func (h *OpenAIGatewayHandler) checkFullAudit(c *gin.Context, reqLog *zap.Logger, apiKey *service.APIKey, subject middleware2.AuthSubject, protocol string, model string, body []byte) *service.FullAuditDecision {
	if h == nil || h.fullAuditService == nil {
		return nil
	}
	return runFullAudit(c, reqLog, h.fullAuditService, apiKey, subject, protocol, model, body)
}

func runFullAudit(c *gin.Context, reqLog *zap.Logger, svc fullAuditChecker, apiKey *service.APIKey, subject middleware2.AuthSubject, protocol string, model string, body []byte) (decision *service.FullAuditDecision) {
	if svc == nil || c == nil || c.Request == nil {
		return nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			if reqLog != nil {
				reqLog.Error("full_audit.check_panic", zap.Any("panic", recovered))
			}
			decision = service.FullAuditRuntimeBlockedDecision("runtime_panic")
		}
	}()
	input := buildFullAuditInput(c, apiKey, subject, protocol, model, body)
	decision, err := svc.Check(c.Request.Context(), input)
	if err != nil {
		if reqLog != nil {
			reqLog.Warn("full_audit.check_failed", zap.Error(err))
		}
		if decision != nil {
			return decision
		}
		return service.FullAuditRuntimeBlockedDecision("runtime_error")
	}
	if reqLog != nil && decision != nil {
		reqLog.Info("full_audit.gateway_check_done",
			zap.String("request_id", input.RequestID),
			zap.Bool("allowed", decision.Allowed),
			zap.Bool("blocked", decision.Blocked),
			zap.Bool("skipped", decision.Skipped),
			zap.String("reason", decision.Reason),
			zap.String("protocol", input.Protocol),
			zap.String("endpoint", input.Endpoint),
		)
	}
	return decision
}

func buildFullAuditInput(c *gin.Context, apiKey *service.APIKey, subject middleware2.AuthSubject, protocol string, model string, body []byte) service.FullAuditCheckInput {
	input := service.FullAuditCheckInput{
		RequestID:       contentModerationRequestID(c.Request.Context()),
		ClientRequestID: fullAuditClientRequestID(c.Request.Context()),
		UserID:          subject.UserID,
		Endpoint:        GetInboundEndpoint(c),
		Provider:        contentModerationProvider(apiKey),
		Model:           strings.TrimSpace(model),
		Protocol:        protocol,
		ClientIP:        strings.TrimSpace(c.ClientIP()),
		UserAgent:       strings.TrimSpace(c.GetHeader("User-Agent")),
		SessionID:       extractFullAuditSessionID(c, body),
		Body:            body,
	}
	if forcedPlatform, ok := middleware2.GetForcePlatformFromContext(c); ok {
		input.Provider = strings.TrimSpace(forcedPlatform)
	}
	if apiKey != nil {
		input.APIKeyID = apiKey.ID
		input.APIKeyName = apiKey.Name
		if apiKey.User != nil {
			input.UserEmail = apiKey.User.Email
		}
		if apiKey.GroupID != nil {
			groupID := *apiKey.GroupID
			input.GroupID = &groupID
		}
		if apiKey.Group != nil {
			input.GroupName = apiKey.Group.Name
		}
	}
	if input.Endpoint == "" && c.Request != nil && c.Request.URL != nil {
		input.Endpoint = c.Request.URL.Path
	}
	return input
}

func fullAuditClientRequestID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if clientRequestID, ok := ctx.Value(ctxkey.ClientRequestID).(string); ok {
		return strings.TrimSpace(clientRequestID)
	}
	return ""
}

func extractFullAuditSessionID(c *gin.Context, body []byte) string {
	if c != nil {
		if value := strings.TrimSpace(c.GetHeader("session_id")); value != "" {
			return value
		}
		if value := strings.TrimSpace(c.GetHeader("conversation_id")); value != "" {
			return value
		}
		if value := strings.TrimSpace(c.GetHeader("X-Claude-Code-Session-Id")); value != "" {
			return value
		}
	}
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return ""
	}
	if value := strings.TrimSpace(gjson.GetBytes(body, "prompt_cache_key").String()); value != "" {
		return value
	}
	if parsed := service.ParseMetadataUserID(gjson.GetBytes(body, "metadata.user_id").String()); parsed != nil {
		return strings.TrimSpace(parsed.SessionID)
	}
	return ""
}

func fullAuditStatus(decision *service.FullAuditDecision) int {
	if decision == nil || decision.StatusCode < 400 || decision.StatusCode > 599 {
		return http.StatusServiceUnavailable
	}
	return decision.StatusCode
}

func fullAuditErrorCode(decision *service.FullAuditDecision) string {
	return "audit_enqueue_failed"
}
