package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/gin-gonic/gin"
)

var ErrCodexOnly = errors.New("this group only allows Codex official clients")

type openAIGroupRoutingKey struct{}

// Keep the authenticated API key and ctxkey.Group intact: authorization and
// downstream billing belong to the original group, just as with ClaudeCodeOnly.
type openAIGroupRouting struct {
	sourceID int64
	group    *Group
}

func OpenAIRoutingGroupID(ctx context.Context, groupID *int64) *int64 {
	if ctx != nil && groupID != nil {
		if route, ok := ctx.Value(openAIGroupRoutingKey{}).(openAIGroupRouting); ok && route.sourceID == *groupID {
			return &route.group.ID
		}
	}
	return groupID
}

func OpenAIRoutingGroup(ctx context.Context, group *Group) *Group {
	if ctx != nil && group != nil {
		if route, ok := ctx.Value(openAIGroupRoutingKey{}).(openAIGroupRouting); ok && route.sourceID == group.ID {
			return route.group
		}
	}
	return group
}

// WithCodexGroupRouting resolves only group-level policy. Account-level client
// restrictions remain unchanged and are checked by their existing forwarders.
func (s *OpenAIGatewayService) WithCodexGroupRouting(c *gin.Context, group *Group, body []byte) (context.Context, error) {
	ctx := c.Request.Context()
	if group == nil || group.Platform != PlatformOpenAI || !group.CodexOnly {
		return ctx, nil
	}
	// Reuse the same global client policy, without any account-specific exception.
	clientPolicyAccount := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{"codex_cli_only": true}}
	result := s.detectCodexClientRestriction(c, clientPolicyAccount, body)
	if result.Matched {
		return ctx, nil
	}
	if group.FallbackGroupID == nil {
		return ctx, ErrCodexOnly
	}
	if s == nil || s.schedulerSnapshot == nil {
		return ctx, fmt.Errorf("resolve Codex fallback group: scheduler unavailable")
	}
	sourceID := group.ID
	visited := map[int64]bool{sourceID: true}
	for group.CodexOnly {
		if group.FallbackGroupID == nil {
			return ctx, ErrCodexOnly
		}
		id := *group.FallbackGroupID
		if visited[id] {
			return ctx, fmt.Errorf("Codex fallback group cycle detected")
		}
		visited[id] = true
		var err error
		group, err = s.schedulerSnapshot.GetGroupByIDLite(ctx, id)
		if err != nil {
			return ctx, fmt.Errorf("resolve Codex fallback group: %w", err)
		}
		if group == nil || group.Platform != PlatformOpenAI || group.Status != StatusActive {
			return ctx, fmt.Errorf("Codex fallback group must be an active OpenAI group")
		}
	}
	return context.WithValue(ctx, openAIGroupRoutingKey{}, openAIGroupRouting{sourceID: sourceID, group: group}), nil
}
