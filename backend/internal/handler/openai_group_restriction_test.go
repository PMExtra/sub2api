//go:build unit

package handler

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newCodexGroupRoutingHandler(t *testing.T, withFallback bool) (*OpenAIGatewayHandler, *service.APIKey, *groupScopedSchedulerCache) {
	t.Helper()
	parent := &service.Group{ID: 9400, Hydrated: true, Platform: service.PlatformOpenAI, Status: service.StatusActive, CodexOnly: true, AllowMessagesDispatch: true}
	target := &service.Group{ID: 9401, Hydrated: true, Platform: service.PlatformOpenAI, Status: service.StatusActive, IsExclusive: true}
	if withFallback {
		parent.FallbackGroupID = &target.ID
	}
	accounts := []service.Account{}
	for i, group := range []*service.Group{parent, target} {
		id := int64(9410 + i)
		accounts = append(accounts, service.Account{ID: id, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
			Status: service.StatusActive, Schedulable: true, Concurrency: 1,
			AccountGroups: []service.AccountGroup{{AccountID: id, GroupID: group.ID}},
		})
	}
	cache := &groupScopedSchedulerCache{fakeSchedulerCache: &fakeSchedulerCache{accounts: []*service.Account{&accounts[0], &accounts[1]}}}
	groupRepo := &groupMapRepo{groups: map[int64]*service.Group{parent.ID: parent, target.ID: target}}
	snapshot := service.NewSchedulerSnapshotService(cache, nil, nil, groupRepo, nil)
	cfg := &config.Config{RunMode: config.RunModeSimple}
	billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billing.Stop)
	svc := service.NewOpenAIGatewayService(
		&openAIWSFailoverHandlerAccountRepoStub{accounts: accounts}, nil, nil, nil, nil, nil, nil, cfg,
		snapshot, nil, service.NewBillingService(cfg, nil), nil, billing, nil, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil,
	)
	h := &OpenAIGatewayHandler{gatewayService: svc, billingCacheService: billing, apiKeyService: &service.APIKeyService{}, cfg: cfg, maxAccountSwitches: 1,
		concurrencyHelper: NewConcurrencyHelper(service.NewConcurrencyService(&fakeConcurrencyCache{}), SSEPingFormatComment, 0)}
	user := &service.User{ID: 9430, Concurrency: 10, Balance: 100, AllowedGroups: []int64{parent.ID}}
	key := &service.APIKey{ID: 9420, UserID: user.ID, User: user, GroupID: &parent.ID, Group: parent, Status: service.StatusActive}
	return h, key, cache
}

func setCodexGroupRoutingAuth(c *gin.Context, key *service.APIKey) {
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.Group, key.Group))
	c.Set(string(middleware.ContextKeyAPIKey), key)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: key.UserID, Concurrency: 10})
}

func TestOpenAICodexGroupRoutingHTTP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, endpoint := range []struct {
		name, path, body string
		call             func(*OpenAIGatewayHandler, *gin.Context)
	}{
		{"responses", "/v1/responses", `{"model":"gpt-5","input":"hello"}`, (*OpenAIGatewayHandler).Responses},
		{"chat", "/v1/chat/completions", `{"model":"gpt-5","messages":[{"role":"user","content":"hello"}]}`, (*OpenAIGatewayHandler).ChatCompletions},
		{"messages", "/v1/messages", `{"model":"gpt-5","max_tokens":20,"messages":[{"role":"user","content":"hello"}]}`, (*OpenAIGatewayHandler).Messages},
	} {
		for _, tc := range []struct {
			name     string
			fallback bool
			official bool
		}{
			{"fallback", true, false},
			{"forbidden", false, false},
			{"official", true, true},
		} {
			t.Run(endpoint.name+"/"+tc.name, func(t *testing.T) {
				h, key, cache := newCodexGroupRoutingHandler(t, tc.fallback)
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				c.Request = httptest.NewRequest(http.MethodPost, endpoint.path, bytes.NewBufferString(endpoint.body))
				c.Request.Header.Set("Content-Type", "application/json")
				if tc.official {
					c.Request.Header.Set("User-Agent", "codex_cli_rs/0.100.0")
					c.Request.Header.Set("x-codex-session-id", "test-session")
				}
				setCodexGroupRoutingAuth(c, key)
				endpoint.call(h, c)
				if !tc.fallback && !tc.official {
					require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
					require.Empty(t, cache.queriedGroupIDs())
					return
				}
				selected, exists := c.Get(opsAccountIDKey)
				require.True(t, exists, recorder.Body.String())
				if tc.official {
					require.Equal(t, int64(9410), selected)
					require.Contains(t, cache.queriedGroupIDs(), int64(9400))
					require.NotContains(t, cache.queriedGroupIDs(), int64(9401))
				} else {
					require.Equal(t, int64(9411), selected)
					require.Contains(t, cache.queriedGroupIDs(), int64(9401))
					require.NotContains(t, cache.queriedGroupIDs(), int64(9400))
				}
				require.Equal(t, int64(9400), *key.GroupID)
			})
		}
	}
}

func TestOpenAICodexGroupRoutingWebSocket(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, fallback := range []bool{false, true} {
		t.Run(map[bool]string{false: "forbidden", true: "fallback"}[fallback], func(t *testing.T) {
			h, key, cache := newCodexGroupRoutingHandler(t, fallback)
			router := gin.New()
			router.GET("/v1/responses", func(c *gin.Context) {
				setCodexGroupRoutingAuth(c, key)
				h.ResponsesWebSocket(c)
			})
			server := httptest.NewServer(router)
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
			require.NoError(t, err)
			defer conn.CloseNow()
			require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5","input":"hello"}`)))
			_, _, err = conn.Read(ctx)
			require.Error(t, err)
			if !fallback {
				require.Equal(t, coderws.StatusPolicyViolation, coderws.CloseStatus(err))
				require.Empty(t, cache.queriedGroupIDs())
			} else {
				// Test accounts deliberately have no credentials/WS transport. The
				// request must still reach selection exclusively in the target group.
				require.Contains(t, cache.queriedGroupIDs(), int64(9401))
				require.NotContains(t, cache.queriedGroupIDs(), int64(9400))
			}
		})
	}
}
