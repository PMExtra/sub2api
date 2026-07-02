package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type fullAuditTestSettingRepo struct {
	values map[string]string
	err    error
}

func (r *fullAuditTestSettingRepo) Get(ctx context.Context, key string) (*Setting, error) {
	if r.err != nil {
		return nil, r.err
	}
	if value, ok := r.values[key]; ok {
		return &Setting{Key: key, Value: value}, nil
	}
	return nil, ErrSettingNotFound
}

func (r *fullAuditTestSettingRepo) GetValue(ctx context.Context, key string) (string, error) {
	if r.err != nil {
		return "", r.err
	}
	if value, ok := r.values[key]; ok {
		return value, nil
	}
	return "", ErrSettingNotFound
}

func (r *fullAuditTestSettingRepo) Set(ctx context.Context, key, value string) error {
	if r.values == nil {
		r.values = map[string]string{}
	}
	r.values[key] = value
	return nil
}

func (r *fullAuditTestSettingRepo) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	out := map[string]string{}
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			out[key] = value
		}
	}
	return out, nil
}

func (r *fullAuditTestSettingRepo) SetMultiple(ctx context.Context, settings map[string]string) error {
	if r.values == nil {
		r.values = map[string]string{}
	}
	for key, value := range settings {
		r.values[key] = value
	}
	return nil
}

func (r *fullAuditTestSettingRepo) GetAll(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	for key, value := range r.values {
		out[key] = value
	}
	return out, nil
}

func (r *fullAuditTestSettingRepo) Delete(ctx context.Context, key string) error {
	delete(r.values, key)
	return nil
}

type fullAuditTestRepo struct {
	mu          sync.Mutex
	messages    map[string]FullAuditMessageRecord
	requestLogs []FullAuditRequestLog
	upsertErr   error
	logErr      error
}

func (r *fullAuditTestRepo) UpsertMessages(ctx context.Context, messages []FullAuditMessageRecord) error {
	if r.upsertErr != nil {
		return r.upsertErr
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.messages == nil {
		r.messages = map[string]FullAuditMessageRecord{}
	}
	for _, msg := range messages {
		if _, ok := r.messages[msg.Hash]; !ok {
			r.messages[msg.Hash] = msg
		}
	}
	return nil
}

func (r *fullAuditTestRepo) CreateRequestLog(ctx context.Context, log *FullAuditRequestLog) error {
	if r.logErr != nil {
		return r.logErr
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requestLogs = append(r.requestLogs, *log)
	return nil
}

func TestFullAuditCheck_FailClosedBlocksWhenQueueFull(t *testing.T) {
	svc := newFullAuditTestService(t, FullAuditEnqueueFailurePolicyFailClosed, 0, 1, nil)
	svc.queue <- fullAuditTask{}

	decision, err := svc.Check(context.Background(), fullAuditCheckInput())

	require.NoError(t, err)
	require.NotNil(t, decision)
	require.True(t, decision.Blocked)
	require.False(t, decision.Allowed)
	require.Equal(t, "queue_full", decision.Reason)
}

func TestFullAuditCheck_SkipAllowsWhenQueueFull(t *testing.T) {
	svc := newFullAuditTestService(t, FullAuditEnqueueFailurePolicySkip, 0, 1, nil)
	svc.queue <- fullAuditTask{}

	decision, err := svc.Check(context.Background(), fullAuditCheckInput())

	require.NoError(t, err)
	require.NotNil(t, decision)
	require.True(t, decision.Allowed)
	require.True(t, decision.Skipped)
	require.Equal(t, uint64(1), svc.skipped.Load())
}

func TestFullAuditCheck_ConfigRepositoryErrorBlocks(t *testing.T) {
	svc := &FullAuditService{
		settingRepo: &fullAuditTestSettingRepo{err: errors.New("settings down")},
		queue:       make(chan fullAuditTask, 1),
		stopCh:      make(chan struct{}),
	}

	decision, err := svc.Check(context.Background(), fullAuditCheckInput())

	require.Error(t, err)
	require.NotNil(t, decision)
	require.True(t, decision.Blocked)
	require.False(t, decision.Allowed)
	require.Equal(t, "config_error", decision.Reason)
}

func TestFullAuditCheck_InvalidConfigBlocks(t *testing.T) {
	svc := &FullAuditService{
		settingRepo: &fullAuditTestSettingRepo{values: map[string]string{SettingKeyFullAuditConfig: `{`}},
		queue:       make(chan fullAuditTask, 1),
		stopCh:      make(chan struct{}),
	}

	decision, err := svc.Check(context.Background(), fullAuditCheckInput())

	require.Error(t, err)
	require.NotNil(t, decision)
	require.True(t, decision.Blocked)
	require.False(t, decision.Allowed)
	require.Equal(t, "config_error", decision.Reason)
}

func TestFullAuditCheck_NoUserInputDoesNotEnqueue(t *testing.T) {
	svc := newFullAuditTestService(t, FullAuditEnqueueFailurePolicyFailClosed, 0, 1, nil)
	input := fullAuditCheckInput()
	input.Body = []byte(`{"model":"gpt-test","messages":[{"role":"assistant","content":"ok"}]}`)

	decision, err := svc.Check(context.Background(), input)

	require.NoError(t, err)
	require.NotNil(t, decision)
	require.True(t, decision.Allowed)
	require.Len(t, svc.queue, 0)
	require.Equal(t, uint64(0), svc.enqueued.Load())
}

func TestFullAuditWorker_PersistsMessagesAndRequestLog(t *testing.T) {
	repo := &fullAuditTestRepo{}
	svc := newFullAuditTestService(t, FullAuditEnqueueFailurePolicyFailClosed, 1, 8, repo)
	defer svc.Stop()

	decision, err := svc.Check(context.Background(), fullAuditCheckInput())

	require.NoError(t, err)
	require.True(t, decision.Allowed)
	require.Eventually(t, func() bool {
		repo.mu.Lock()
		defer repo.mu.Unlock()
		return len(repo.messages) == 2 && len(repo.requestLogs) == 1
	}, time.Second, 10*time.Millisecond)
	require.Equal(t, uint64(1), svc.processed.Load())
	repo.mu.Lock()
	defer repo.mu.Unlock()
	log := repo.requestLogs[0]
	require.Equal(t, "client_req_1", log.ClientRequestID)
	require.Equal(t, "203.0.113.10", log.ClientIP)
	require.Equal(t, "full-audit-test/1.0", log.UserAgent)
	require.Equal(t, "sess-1", log.SessionID)
}

func TestBuildFullAuditTask_TruncatesSessionID(t *testing.T) {
	input := fullAuditCheckInput()
	input.SessionID = strings.Repeat("s", maxFullAuditSessionIDLength+20)

	task := buildFullAuditTask(input, ExtractFullAuditUserMessages(input.Protocol, input.Body))

	require.Len(t, task.Log.SessionID, maxFullAuditSessionIDLength)
	require.Equal(t, strings.Repeat("s", maxFullAuditSessionIDLength), task.Log.SessionID)
}

func TestFullAuditWorker_KVFailureStillWritesRequestLog(t *testing.T) {
	repo := &fullAuditTestRepo{upsertErr: errors.New("db down")}
	svc := newFullAuditTestService(t, FullAuditEnqueueFailurePolicyFailClosed, 1, 8, repo)
	defer svc.Stop()

	decision, err := svc.Check(context.Background(), fullAuditCheckInput())

	require.NoError(t, err)
	require.True(t, decision.Allowed)
	require.Eventually(t, func() bool {
		return svc.failed.Load() == 1
	}, time.Second, 10*time.Millisecond)
	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.requestLogs, 1)
}

func TestFullAuditWorker_RequestLogFailureDoesNotRollbackMessages(t *testing.T) {
	repo := &fullAuditTestRepo{logErr: errors.New("log insert down")}
	svc := newFullAuditTestService(t, FullAuditEnqueueFailurePolicyFailClosed, 1, 8, repo)
	defer svc.Stop()

	decision, err := svc.Check(context.Background(), fullAuditCheckInput())

	require.NoError(t, err)
	require.True(t, decision.Allowed)
	require.Eventually(t, func() bool {
		return svc.failed.Load() == 1
	}, time.Second, 10*time.Millisecond)
	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Len(t, repo.messages, 2)
	require.Empty(t, repo.requestLogs)
}

func newFullAuditTestService(t *testing.T, policy string, workers int, queueSize int, repo FullAuditRepository) *FullAuditService {
	t.Helper()
	cfg := FullAuditConfig{Enabled: true, EnqueueFailurePolicy: policy}
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	svc := NewFullAuditService(
		&fullAuditTestSettingRepo{values: map[string]string{SettingKeyFullAuditConfig: string(raw)}},
		repo,
		&config.Config{Gateway: config.GatewayConfig{FullAudit: config.GatewayFullAuditConfig{
			WorkerCount:        workers,
			QueueSize:          queueSize,
			TaskTimeoutSeconds: 1,
		}}},
	)
	if workers == 0 {
		svc.Stop()
		svc = &FullAuditService{
			settingRepo: &fullAuditTestSettingRepo{values: map[string]string{SettingKeyFullAuditConfig: string(raw)}},
			repo:        repo,
			queue:       make(chan fullAuditTask, queueSize),
			timeout:     time.Second,
			workers:     0,
			stopCh:      make(chan struct{}),
		}
	}
	return svc
}

func fullAuditCheckInput() FullAuditCheckInput {
	return FullAuditCheckInput{
		RequestID:       "req_1",
		ClientRequestID: "client_req_1",
		UserID:          1001,
		UserEmail:       "user@example.com",
		APIKeyID:        2001,
		APIKeyName:      "key",
		Endpoint:        "/v1/chat/completions",
		Provider:        "openai",
		Model:           "gpt-test",
		Protocol:        FullAuditProtocolOpenAIChat,
		ClientIP:        "203.0.113.10",
		UserAgent:       "full-audit-test/1.0",
		SessionID:       "sess-1",
		Body: []byte(`{
			"model":"gpt-test",
			"messages":[
				{"role":"user","content":"one"},
				{"role":"assistant","content":"ok"},
				{"role":"user","content":"two"}
			]
		}`),
	}
}
