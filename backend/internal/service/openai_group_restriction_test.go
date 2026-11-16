package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type codexRoutingGroupRepo struct {
	GroupRepository
	groups map[int64]*Group
}

func (r codexRoutingGroupRepo) GetByIDLite(_ context.Context, id int64) (*Group, error) {
	if group := r.groups[id]; group != nil {
		return group, nil
	}
	return nil, ErrGroupNotFound
}

func codexRoutingContext(group *Group, official bool) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.Group, group))
	if official {
		c.Request.Header.Set("User-Agent", "codex_cli_rs/0.100.0")
		c.Request.Header.Set("x-codex-session-id", "test-session")
	} else {
		c.Request.Header.Set("User-Agent", "curl/8.0")
	}
	return c
}

func TestCodexGroupRouting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name     string
		official bool
		fallback bool
		wantID   int64
		denied   bool
	}{
		{"official stays in original group", true, true, 1, false},
		{"official needs no fallback", true, false, 1, false},
		{"third party uses exclusive fallback without membership", false, true, 2, false},
		{"third party without fallback is forbidden", false, false, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fallback := &Group{ID: 2, Platform: PlatformOpenAI, Status: StatusActive, IsExclusive: true, RateMultiplier: 0.5}
			parent := &Group{ID: 1, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true, CodexOnly: true, RateMultiplier: 2}
			if tc.fallback {
				parent.FallbackGroupID = &fallback.ID
			}
			svc := &OpenAIGatewayService{schedulerSnapshot: &SchedulerSnapshotService{groupRepo: codexRoutingGroupRepo{groups: map[int64]*Group{2: fallback}}}}
			c := codexRoutingContext(parent, tc.official)
			ctx, err := svc.WithCodexGroupRouting(c, parent, nil)
			if tc.denied {
				require.ErrorIs(t, err, ErrCodexOnly)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.wantID, *OpenAIRoutingGroupID(ctx, &parent.ID))
			require.Same(t, parent, ctx.Value(ctxkey.Group), "billing and authorization must keep the authenticated group")
			require.Equal(t, int64(2), *OpenAIRoutingGroupID(ctx, &fallback.ID), "resolving the target again must be idempotent")
		})
	}
}

func TestCodexGroupRoutingFailsClosedForInvalidTarget(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target *Group
	}{
		{"deleted", nil},
		{"disabled", &Group{ID: 2, Platform: PlatformOpenAI, Status: "disabled"}},
		{"wrong platform", &Group{ID: 2, Platform: PlatformAnthropic, Status: StatusActive}},
		{"cycle", &Group{ID: 2, Platform: PlatformOpenAI, Status: StatusActive, CodexOnly: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent := &Group{ID: 1, Platform: PlatformOpenAI, CodexOnly: true}
			id := int64(2)
			parent.FallbackGroupID = &id
			if tc.target != nil && tc.target.CodexOnly {
				tc.target.FallbackGroupID = &parent.ID
			}
			svc := &OpenAIGatewayService{schedulerSnapshot: &SchedulerSnapshotService{groupRepo: codexRoutingGroupRepo{groups: map[int64]*Group{2: tc.target}}}}
			_, err := svc.WithCodexGroupRouting(codexRoutingContext(parent, false), parent, nil)
			require.Error(t, err)
		})
	}
}

func TestCodexGroupRoutingPreservesOriginalBilling(t *testing.T) {
	parent := &Group{ID: 1, Hydrated: true, Platform: PlatformOpenAI, Status: StatusActive, CodexOnly: true, RateMultiplier: 2}
	target := &Group{ID: 2, Hydrated: true, Platform: PlatformOpenAI, Status: StatusActive, IsExclusive: true, RateMultiplier: 0.5}
	parent.FallbackGroupID = &target.ID
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, nil, nil, nil)
	svc.cfg.RunMode = config.RunModeSimple
	inputPrice, outputPrice := 7e-6, 11e-6
	parent.ModelPricing = []ChannelModelPricing{{Models: []string{"gpt-5"}, BillingMode: BillingModeToken, InputPrice: &inputPrice, OutputPrice: &outputPrice}}
	svc.resolver = NewModelPricingResolver(nil, svc.billingService)
	svc.schedulerSnapshot = &SchedulerSnapshotService{groupRepo: codexRoutingGroupRepo{groups: map[int64]*Group{2: target}}}
	c := codexRoutingContext(parent, false)
	user := &User{ID: 10, AllowedGroups: []int64{parent.ID}}
	key := &APIKey{ID: 20, UserID: user.ID, User: user, GroupID: &parent.ID, Group: parent}
	c.Set("api_key", key)
	ctx, err := svc.WithCodexGroupRouting(c, parent, nil)
	require.NoError(t, err)
	c.Request = c.Request.WithContext(ctx)
	require.False(t, user.CanBindGroup(target.ID, target.IsExclusive))
	require.Equal(t, target.ID, getOpenAIGroupIDFromContext(c), "upstream response affinity must use the target group")
	err = svc.RecordUsage(ctx, &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{RequestID: "codex-fallback-billing", Model: "gpt-5", Usage: OpenAIUsage{InputTokens: 100, OutputTokens: 10}},
		APIKey: key, User: user, Account: &Account{ID: 30, Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
	})
	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	require.Equal(t, parent.ID, *usageRepo.lastLog.GroupID)
	require.Equal(t, 2.0, usageRepo.lastLog.RateMultiplier)
	require.InDelta(t, 100*inputPrice+10*outputPrice, usageRepo.lastLog.TotalCost, 1e-12)
	require.InDelta(t, usageRepo.lastLog.TotalCost*2, usageRepo.lastLog.ActualCost, 1e-12)
	require.Same(t, parent, key.Group)
}

func TestValidateCodexGroupRestriction(t *testing.T) {
	target := &Group{ID: 2, Platform: PlatformOpenAI, Status: StatusActive, IsExclusive: true}
	svc := &adminServiceImpl{groupRepo: codexRoutingGroupRepo{groups: map[int64]*Group{2: target}}}
	require.NoError(t, svc.validateCodexGroupRestriction(context.Background(), 1, PlatformOpenAI, true, &target.ID))
	require.Error(t, svc.validateCodexGroupRestriction(context.Background(), 1, PlatformAnthropic, true, nil))
	require.Error(t, svc.validateCodexGroupRestriction(context.Background(), 2, PlatformOpenAI, true, &target.ID))
	target.CodexOnly = true
	require.Error(t, svc.validateCodexGroupRestriction(context.Background(), 1, PlatformOpenAI, true, &target.ID))
	target.CodexOnly = false
	target.Platform = PlatformAnthropic
	require.Error(t, svc.validateCodexGroupRestriction(context.Background(), 1, PlatformOpenAI, true, &target.ID))
}

func TestCodexGroupAuthSnapshotRoundtrip(t *testing.T) {
	groupID, fallbackID := int64(1), int64(2)
	key := &APIKey{ID: 10, Key: "sk-group-codex", Status: StatusActive, GroupID: &groupID,
		User:  &User{ID: 20, Status: StatusActive},
		Group: &Group{ID: groupID, Platform: PlatformOpenAI, Status: StatusActive, CodexOnly: true, FallbackGroupID: &fallbackID}}
	svc := &APIKeyService{}
	payload, err := json.Marshal(&APIKeyAuthCacheEntry{Snapshot: svc.snapshotFromAPIKey(context.Background(), key)})
	require.NoError(t, err)
	var entry APIKeyAuthCacheEntry
	require.NoError(t, json.Unmarshal(payload, &entry))
	materialized, used, err := svc.applyAuthCacheEntry(key.Key, &entry)
	require.NoError(t, err)
	require.True(t, used)
	require.True(t, materialized.Group.CodexOnly)
	require.Equal(t, fallbackID, *materialized.Group.FallbackGroupID)
}

type codexRoutingSchedulerCache struct {
	SchedulerCache
	accounts []*Account
	queried  []int64
}

func (c *codexRoutingSchedulerCache) GetSnapshot(_ context.Context, bucket SchedulerBucket) ([]*Account, bool, error) {
	c.queried = append(c.queried, bucket.GroupID)
	var accounts []*Account
	for _, account := range c.accounts {
		for _, membership := range account.AccountGroups {
			if membership.GroupID == bucket.GroupID {
				accounts = append(accounts, account)
			}
		}
	}
	return accounts, true, nil
}

func (c *codexRoutingSchedulerCache) GetAccount(_ context.Context, id int64) (*Account, error) {
	for _, account := range c.accounts {
		if account.ID == id {
			return account, nil
		}
	}
	return nil, nil
}

func TestCodexGroupRoutingSelectionAndFailoverStayInTarget(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{true: "legacy", false: "scheduler"}[legacy], func(t *testing.T) {
			parent := &Group{ID: 1, Platform: PlatformOpenAI, CodexOnly: true}
			target := &Group{ID: 2, Platform: PlatformOpenAI, Status: StatusActive}
			parent.FallbackGroupID = &target.ID
			cache := &codexRoutingSchedulerCache{}
			for _, id := range []int64{11, 21, 22} {
				groupID := id / 10
				cache.accounts = append(cache.accounts, &Account{
					ID: id, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
					Status: StatusActive, Schedulable: true, Concurrency: 1,
					AccountGroups: []AccountGroup{{AccountID: id, GroupID: groupID}},
				})
			}
			svc := &OpenAIGatewayService{schedulerSnapshot: &SchedulerSnapshotService{
				cache: cache, groupRepo: codexRoutingGroupRepo{groups: map[int64]*Group{2: target}},
			}}
			ctx, err := svc.WithCodexGroupRouting(codexRoutingContext(parent, false), parent, nil)
			require.NoError(t, err)
			excluded := map[int64]struct{}{}
			for attempt := 0; attempt < 2; attempt++ {
				var account *Account
				if legacy {
					account, err = svc.SelectAccountForModelWithExclusions(ctx, &parent.ID, "", "gpt-5", excluded)
				} else {
					var selection *AccountSelectionResult
					selection, _, err = svc.SelectAccountWithSchedulerForCapability(ctx, &parent.ID, "", "", "gpt-5", excluded, OpenAIUpstreamTransportAny, "", false, false, false)
					if selection != nil {
						account = selection.Account
						if selection.ReleaseFunc != nil {
							selection.ReleaseFunc()
						}
					}
				}
				require.NoError(t, err)
				require.NotNil(t, account)
				require.Contains(t, []int64{21, 22}, account.ID)
				require.NotContains(t, excluded, account.ID)
				excluded[account.ID] = struct{}{}
			}
			require.Contains(t, cache.queried, target.ID)
			require.NotContains(t, cache.queried, parent.ID)
		})
	}
}
