package handler

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type fullAuditHelperSettingRepo struct {
	values map[string]string
	err    error
}

func (r *fullAuditHelperSettingRepo) Get(ctx context.Context, key string) (*service.Setting, error) {
	if r.err != nil {
		return nil, r.err
	}
	if value, ok := r.values[key]; ok {
		return &service.Setting{Key: key, Value: value}, nil
	}
	return nil, service.ErrSettingNotFound
}

func (r *fullAuditHelperSettingRepo) GetValue(ctx context.Context, key string) (string, error) {
	if r.err != nil {
		return "", r.err
	}
	if value, ok := r.values[key]; ok {
		return value, nil
	}
	return "", service.ErrSettingNotFound
}

func (r *fullAuditHelperSettingRepo) Set(ctx context.Context, key, value string) error {
	if r.values == nil {
		r.values = map[string]string{}
	}
	r.values[key] = value
	return nil
}

func (r *fullAuditHelperSettingRepo) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	out := map[string]string{}
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			out[key] = value
		}
	}
	return out, nil
}

func (r *fullAuditHelperSettingRepo) SetMultiple(ctx context.Context, settings map[string]string) error {
	if r.values == nil {
		r.values = map[string]string{}
	}
	for key, value := range settings {
		r.values[key] = value
	}
	return nil
}

func (r *fullAuditHelperSettingRepo) GetAll(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	for key, value := range r.values {
		out[key] = value
	}
	return out, nil
}

func (r *fullAuditHelperSettingRepo) Delete(ctx context.Context, key string) error {
	delete(r.values, key)
	return nil
}

func TestRunFullAudit_ConfigErrorReturnsBlockedDecision(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	svc := service.NewFullAuditService(&fullAuditHelperSettingRepo{err: errors.New("settings down")}, nil, nil)
	defer svc.Stop()

	decision := runFullAudit(
		c,
		nil,
		svc,
		&service.APIKey{ID: 1},
		middleware2.AuthSubject{UserID: 1},
		service.FullAuditProtocolOpenAIChat,
		"gpt-test",
		[]byte(`{"model":"gpt-test","messages":[{"role":"user","content":"hello"}]}`),
	)

	require.NotNil(t, decision)
	require.True(t, decision.Blocked)
	require.False(t, decision.Allowed)
	require.Equal(t, "config_error", decision.Reason)
}

func TestRunFullAudit_PanicReturnsBlockedDecision(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)

	decision := runFullAudit(
		c,
		nil,
		panicFullAuditChecker{},
		&service.APIKey{ID: 1},
		middleware2.AuthSubject{UserID: 1},
		service.FullAuditProtocolOpenAIChat,
		"gpt-test",
		[]byte(`{"model":"gpt-test","messages":[{"role":"user","content":"hello"}]}`),
	)

	require.NotNil(t, decision)
	require.True(t, decision.Blocked)
	require.Equal(t, "runtime_panic", decision.Reason)
}

type panicFullAuditChecker struct{}

func (panicFullAuditChecker) Check(ctx context.Context, input service.FullAuditCheckInput) (*service.FullAuditDecision, error) {
	panic("boom")
}
