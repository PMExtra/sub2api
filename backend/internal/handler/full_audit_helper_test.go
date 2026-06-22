package handler

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
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

func TestRunFullAudit_BuildsSnapshotFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxkey.ClientRequestID, "client-req-1"))
	req.Header.Set("User-Agent", "audit-client/1.0")
	req.RemoteAddr = "203.0.113.20:12345"
	req.Header.Set("session_id", "sess-header")
	c.Request = req

	checker := &captureFullAuditChecker{}
	decision := runFullAudit(
		c,
		nil,
		checker,
		&service.APIKey{ID: 10, Name: "key"},
		middleware2.AuthSubject{UserID: 20},
		service.FullAuditProtocolOpenAIChat,
		"gpt-test",
		[]byte(`{"model":"gpt-test","messages":[{"role":"user","content":"hello"}]}`),
	)

	require.NotNil(t, decision)
	require.True(t, decision.Allowed)
	require.Equal(t, "client-req-1", checker.input.ClientRequestID)
	require.Equal(t, "203.0.113.20", checker.input.ClientIP)
	require.Equal(t, "audit-client/1.0", checker.input.UserAgent)
	require.Equal(t, "sess-header", checker.input.SessionID)
}

func TestExtractFullAuditSessionIDPriorityAndBodyFallbacks(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("conversation header", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
		c.Request.Header.Set("conversation_id", "conv-header")
		require.Equal(t, "conv-header", extractFullAuditSessionID(c, []byte(`{"prompt_cache_key":"body-key"}`)))
	})

	t.Run("claude session header", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
		c.Request.Header.Set("X-Claude-Code-Session-Id", "claude-header")
		require.Equal(t, "claude-header", extractFullAuditSessionID(c, []byte(`{"prompt_cache_key":"body-key"}`)))
	})

	t.Run("prompt cache key", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
		require.Equal(t, "body-key", extractFullAuditSessionID(c, []byte(`{"prompt_cache_key":"body-key"}`)))
	})

	t.Run("metadata user id", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
		body := []byte(`{"metadata":{"user_id":"{\"device_id\":\"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\"account_uuid\":\"\",\"session_id\":\"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa\"}"}}`)
		require.Equal(t, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", extractFullAuditSessionID(c, body))
	})
}

type panicFullAuditChecker struct{}

func (panicFullAuditChecker) Check(ctx context.Context, input service.FullAuditCheckInput) (*service.FullAuditDecision, error) {
	panic("boom")
}

type captureFullAuditChecker struct {
	input service.FullAuditCheckInput
}

func (c *captureFullAuditChecker) Check(ctx context.Context, input service.FullAuditCheckInput) (*service.FullAuditDecision, error) {
	c.input = input
	return &service.FullAuditDecision{Allowed: true}, nil
}
