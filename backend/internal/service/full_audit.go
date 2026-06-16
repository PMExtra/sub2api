package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

const (
	FullAuditEnqueueFailurePolicyFailClosed = "fail_closed"
	FullAuditEnqueueFailurePolicySkip       = "skip"

	defaultFullAuditWorkerCount        = 4
	defaultFullAuditQueueSize          = 32768
	defaultFullAuditTaskTimeoutSeconds = 5
	defaultFullAuditUnavailableMessage = "Audit service is unavailable, please retry later"
)

type FullAuditRepository interface {
	UpsertMessages(ctx context.Context, messages []FullAuditMessageRecord) error
	CreateRequestLog(ctx context.Context, log *FullAuditRequestLog) error
}

type FullAuditConfig struct {
	Enabled              bool   `json:"enabled"`
	EnqueueFailurePolicy string `json:"enqueue_failure_policy"`
}

type FullAuditConfigView struct {
	Enabled              bool   `json:"enabled"`
	EnqueueFailurePolicy string `json:"enqueue_failure_policy"`
}

type UpdateFullAuditConfigInput struct {
	Enabled              *bool   `json:"enabled"`
	EnqueueFailurePolicy *string `json:"enqueue_failure_policy"`
}

type FullAuditCheckInput struct {
	RequestID  string
	UserID     int64
	UserEmail  string
	APIKeyID   int64
	APIKeyName string
	GroupID    *int64
	GroupName  string
	Endpoint   string
	Provider   string
	Model      string
	Protocol   string
	Body       []byte
}

type FullAuditDecision struct {
	Allowed    bool
	Skipped    bool
	Blocked    bool
	StatusCode int
	Message    string
	Reason     string
}

type FullAuditMessageRecord struct {
	Hash     string
	Protocol string
	Role     string
	Raw      string
	Size     int
}

type FullAuditRequestLog struct {
	ID            int64
	RequestID     string
	UserID        *int64
	UserEmail     string
	APIKeyID      *int64
	APIKeyName    string
	GroupID       *int64
	GroupName     string
	Endpoint      string
	Provider      string
	Model         string
	Protocol      string
	BodyHash      string
	MessageHashes []string
	MessageCount  int
	CreatedAt     time.Time
}

type FullAuditStatus struct {
	Enabled        bool   `json:"enabled"`
	Policy         string `json:"enqueue_failure_policy"`
	WorkerCount    int    `json:"worker_count"`
	QueueSize      int    `json:"queue_size"`
	QueueLength    int    `json:"queue_length"`
	EnqueuedTasks  uint64 `json:"enqueued_tasks"`
	SkippedTasks   uint64 `json:"skipped_tasks"`
	ProcessedTasks uint64 `json:"processed_tasks"`
	FailedTasks    uint64 `json:"failed_tasks"`
}

type fullAuditTask struct {
	Log      FullAuditRequestLog
	Messages []FullAuditMessageRecord
	QueuedAt time.Time
}

type FullAuditService struct {
	settingRepo SettingRepository
	repo        FullAuditRepository
	queue       chan fullAuditTask
	timeout     time.Duration
	workers     int
	stopCh      chan struct{}
	stopOnce    sync.Once

	enqueued  atomic.Uint64
	skipped   atomic.Uint64
	processed atomic.Uint64
	failed    atomic.Uint64
}

func NewFullAuditService(settingRepo SettingRepository, repo FullAuditRepository, cfg *config.Config) *FullAuditService {
	opts := fullAuditOptionsFromConfig(cfg)
	svc := &FullAuditService{
		settingRepo: settingRepo,
		repo:        repo,
		queue:       make(chan fullAuditTask, opts.QueueSize),
		timeout:     time.Duration(opts.TaskTimeoutSeconds) * time.Second,
		workers:     opts.WorkerCount,
		stopCh:      make(chan struct{}),
	}
	for i := 0; i < svc.workers; i++ {
		go svc.worker()
	}
	return svc
}

func (s *FullAuditService) GetConfig(ctx context.Context) (*FullAuditConfigView, error) {
	cfg, err := s.loadConfig(ctx)
	if err != nil {
		return nil, err
	}
	return fullAuditConfigView(cfg), nil
}

func (s *FullAuditService) UpdateConfig(ctx context.Context, input UpdateFullAuditConfigInput) (*FullAuditConfigView, error) {
	cfg, err := s.loadConfig(ctx)
	if err != nil {
		return nil, err
	}
	if input.Enabled != nil {
		cfg.Enabled = *input.Enabled
	}
	if input.EnqueueFailurePolicy != nil {
		cfg.EnqueueFailurePolicy = strings.TrimSpace(*input.EnqueueFailurePolicy)
	}
	cfg.normalize()
	if err := validateFullAuditConfig(cfg); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshal full audit config: %w", err)
	}
	if err := s.settingRepo.Set(ctx, SettingKeyFullAuditConfig, string(raw)); err != nil {
		return nil, fmt.Errorf("save full audit config: %w", err)
	}
	return fullAuditConfigView(cfg), nil
}

func (s *FullAuditService) Status(ctx context.Context) (*FullAuditStatus, error) {
	cfg, err := s.loadConfig(ctx)
	if err != nil {
		return nil, err
	}
	status := &FullAuditStatus{
		Enabled:        cfg.Enabled,
		Policy:         cfg.EnqueueFailurePolicy,
		WorkerCount:    s.workers,
		QueueSize:      cap(s.queue),
		QueueLength:    len(s.queue),
		EnqueuedTasks:  s.enqueued.Load(),
		SkippedTasks:   s.skipped.Load(),
		ProcessedTasks: s.processed.Load(),
		FailedTasks:    s.failed.Load(),
	}
	return status, nil
}

func (s *FullAuditService) Check(ctx context.Context, input FullAuditCheckInput) (*FullAuditDecision, error) {
	if s == nil {
		return nil, nil
	}
	if s.settingRepo == nil {
		return FullAuditRuntimeBlockedDecision("setting_repo_unavailable"), nil
	}
	cfg, err := s.loadConfig(ctx)
	if err != nil {
		return FullAuditRuntimeBlockedDecision("config_error"), err
	}
	if !cfg.Enabled {
		return &FullAuditDecision{Allowed: true}, nil
	}
	messages := ExtractFullAuditUserMessages(input.Protocol, input.Body)
	if len(messages) == 0 {
		return &FullAuditDecision{Allowed: true}, nil
	}
	task := buildFullAuditTask(input, messages)
	if s.queue == nil {
		return s.handleEnqueueFailure(cfg, "queue_unavailable"), nil
	}
	select {
	case s.queue <- task:
		s.enqueued.Add(1)
		return &FullAuditDecision{Allowed: true}, nil
	default:
		return s.handleEnqueueFailure(cfg, "queue_full"), nil
	}
}

func FullAuditRuntimeBlockedDecision(reason string) *FullAuditDecision {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "runtime_error"
	}
	return &FullAuditDecision{
		Allowed:    false,
		Blocked:    true,
		StatusCode: http.StatusServiceUnavailable,
		Message:    defaultFullAuditUnavailableMessage,
		Reason:     reason,
	}
}

func (s *FullAuditService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})
}

func (s *FullAuditService) handleEnqueueFailure(cfg *FullAuditConfig, reason string) *FullAuditDecision {
	if cfg != nil && cfg.EnqueueFailurePolicy == FullAuditEnqueueFailurePolicySkip {
		s.skipped.Add(1)
		logger.L().Warn("full_audit.enqueue_failed_skip", zap.String("reason", reason))
		return &FullAuditDecision{Allowed: true, Skipped: true, Reason: reason}
	}
	return &FullAuditDecision{
		Allowed:    false,
		Blocked:    true,
		StatusCode: http.StatusServiceUnavailable,
		Message:    "Audit queue is unavailable, please retry later",
		Reason:     reason,
	}
}

func (s *FullAuditService) worker() {
	for {
		select {
		case <-s.stopCh:
			return
		case task := <-s.queue:
			s.persistTask(task)
		}
	}
}

func (s *FullAuditService) persistTask(task fullAuditTask) {
	if s == nil || s.repo == nil {
		s.failed.Add(1)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	failed := false
	if err := s.repo.UpsertMessages(ctx, task.Messages); err != nil {
		failed = true
		logger.L().Error("full_audit.upsert_messages_failed", zap.Error(err), zap.String("request_id", task.Log.RequestID))
	}
	if err := s.repo.CreateRequestLog(ctx, &task.Log); err != nil {
		failed = true
		logger.L().Error("full_audit.create_request_log_failed", zap.Error(err), zap.String("request_id", task.Log.RequestID))
	}
	if failed {
		s.failed.Add(1)
		return
	}
	s.processed.Add(1)
}

func buildFullAuditTask(input FullAuditCheckInput, extracted []FullAuditExtractedMessage) fullAuditTask {
	messages := make([]FullAuditMessageRecord, 0, len(extracted))
	hashes := make([]string, 0, len(extracted))
	for _, item := range extracted {
		messages = append(messages, FullAuditMessageRecord{
			Hash:     item.Hash,
			Protocol: input.Protocol,
			Role:     item.Role,
			Raw:      item.Raw,
			Size:     item.Size,
		})
		hashes = append(hashes, item.Hash)
	}
	log := FullAuditRequestLog{
		RequestID:     strings.TrimSpace(input.RequestID),
		UserID:        fullAuditInt64PtrIfPositive(input.UserID),
		UserEmail:     strings.TrimSpace(input.UserEmail),
		APIKeyID:      fullAuditInt64PtrIfPositive(input.APIKeyID),
		APIKeyName:    strings.TrimSpace(input.APIKeyName),
		GroupID:       input.GroupID,
		GroupName:     strings.TrimSpace(input.GroupName),
		Endpoint:      strings.TrimSpace(input.Endpoint),
		Provider:      strings.TrimSpace(input.Provider),
		Model:         strings.TrimSpace(input.Model),
		Protocol:      strings.TrimSpace(input.Protocol),
		BodyHash:      FullAuditBodyHash(input.Body),
		MessageHashes: hashes,
		MessageCount:  len(hashes),
	}
	return fullAuditTask{Log: log, Messages: messages, QueuedAt: time.Now()}
}

func FullAuditBodyHash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func fullAuditInt64PtrIfPositive(v int64) *int64 {
	if v <= 0 {
		return nil
	}
	out := v
	return &out
}

func (s *FullAuditService) loadConfig(ctx context.Context) (*FullAuditConfig, error) {
	cfg := defaultFullAuditConfig()
	if s == nil || s.settingRepo == nil {
		return cfg, nil
	}
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyFullAuditConfig)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			cfg.normalize()
			return cfg, nil
		}
		return nil, fmt.Errorf("get full audit config: %w", err)
	}
	if strings.TrimSpace(raw) == "" {
		cfg.normalize()
		return cfg, nil
	}
	if err := json.Unmarshal([]byte(raw), cfg); err != nil {
		return nil, infraerrors.BadRequest("INVALID_FULL_AUDIT_CONFIG", "全量审计配置不是有效 JSON")
	}
	cfg.normalize()
	return cfg, nil
}

func defaultFullAuditConfig() *FullAuditConfig {
	cfg := &FullAuditConfig{
		Enabled:              false,
		EnqueueFailurePolicy: FullAuditEnqueueFailurePolicyFailClosed,
	}
	cfg.normalize()
	return cfg
}

func (c *FullAuditConfig) normalize() {
	if c == nil {
		return
	}
	c.EnqueueFailurePolicy = strings.ToLower(strings.TrimSpace(c.EnqueueFailurePolicy))
	if c.EnqueueFailurePolicy == "" {
		c.EnqueueFailurePolicy = FullAuditEnqueueFailurePolicyFailClosed
	}
}

func validateFullAuditConfig(cfg *FullAuditConfig) error {
	if cfg == nil {
		return infraerrors.BadRequest("INVALID_FULL_AUDIT_CONFIG", "全量审计配置不能为空")
	}
	switch cfg.EnqueueFailurePolicy {
	case FullAuditEnqueueFailurePolicyFailClosed, FullAuditEnqueueFailurePolicySkip:
		return nil
	default:
		return infraerrors.BadRequest("INVALID_FULL_AUDIT_FAILURE_POLICY", "全量审计入队失败策略无效")
	}
}

func fullAuditConfigView(cfg *FullAuditConfig) *FullAuditConfigView {
	if cfg == nil {
		cfg = defaultFullAuditConfig()
	}
	cfg.normalize()
	return &FullAuditConfigView{
		Enabled:              cfg.Enabled,
		EnqueueFailurePolicy: cfg.EnqueueFailurePolicy,
	}
}

func fullAuditOptionsFromConfig(cfg *config.Config) config.GatewayFullAuditConfig {
	opts := config.GatewayFullAuditConfig{
		WorkerCount:        defaultFullAuditWorkerCount,
		QueueSize:          defaultFullAuditQueueSize,
		TaskTimeoutSeconds: defaultFullAuditTaskTimeoutSeconds,
	}
	if cfg != nil {
		if cfg.Gateway.FullAudit.WorkerCount > 0 {
			opts.WorkerCount = cfg.Gateway.FullAudit.WorkerCount
		}
		if cfg.Gateway.FullAudit.QueueSize > 0 {
			opts.QueueSize = cfg.Gateway.FullAudit.QueueSize
		}
		if cfg.Gateway.FullAudit.TaskTimeoutSeconds > 0 {
			opts.TaskTimeoutSeconds = cfg.Gateway.FullAudit.TaskTimeoutSeconds
		}
	}
	return opts
}
