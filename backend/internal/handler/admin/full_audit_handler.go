package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type FullAuditHandler struct {
	service *service.FullAuditService
}

func NewFullAuditHandler(svc *service.FullAuditService) *FullAuditHandler {
	return &FullAuditHandler{service: svc}
}

type fullAuditConfigRequest struct {
	Enabled              *bool   `json:"enabled"`
	EnqueueFailurePolicy *string `json:"enqueue_failure_policy"`
}

func (h *FullAuditHandler) GetConfig(c *gin.Context) {
	cfg, err := h.service.GetConfig(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, cfg)
}

func (h *FullAuditHandler) UpdateConfig(c *gin.Context) {
	var req fullAuditConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	cfg, err := h.service.UpdateConfig(c.Request.Context(), service.UpdateFullAuditConfigInput{
		Enabled:              req.Enabled,
		EnqueueFailurePolicy: req.EnqueueFailurePolicy,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, cfg)
}

func (h *FullAuditHandler) GetStatus(c *gin.Context) {
	status, err := h.service.Status(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, status)
}
