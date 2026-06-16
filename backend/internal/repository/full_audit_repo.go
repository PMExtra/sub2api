package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type fullAuditRepository struct {
	db *sql.DB
}

func NewFullAuditRepository(db *sql.DB) service.FullAuditRepository {
	return &fullAuditRepository{db: db}
}

func (r *fullAuditRepository) UpsertMessages(ctx context.Context, messages []service.FullAuditMessageRecord) error {
	if r == nil || r.db == nil || len(messages) == 0 {
		return nil
	}
	var joined error
	for _, msg := range messages {
		if msg.Hash == "" || msg.Raw == "" {
			continue
		}
		_, err := r.db.ExecContext(ctx, `
INSERT INTO audit_message_kv (message_hash, protocol, role, raw_message, raw_message_size)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (message_hash) DO NOTHING`,
			msg.Hash, msg.Protocol, msg.Role, msg.Raw, msg.Size)
		if err != nil {
			joined = errors.Join(joined, fmt.Errorf("upsert full audit message %s: %w", msg.Hash, err))
		}
	}
	return joined
}

func (r *fullAuditRepository) CreateRequestLog(ctx context.Context, log *service.FullAuditRequestLog) error {
	if r == nil || r.db == nil || log == nil {
		return nil
	}
	messageHashes, err := json.Marshal(log.MessageHashes)
	if err != nil {
		return fmt.Errorf("marshal full audit message hashes: %w", err)
	}
	var userID any
	if log.UserID != nil {
		userID = *log.UserID
	}
	var apiKeyID any
	if log.APIKeyID != nil {
		apiKeyID = *log.APIKeyID
	}
	var groupID any
	if log.GroupID != nil {
		groupID = *log.GroupID
	}
	err = r.db.QueryRowContext(ctx, `
INSERT INTO audit_request_logs (
    request_id, user_id, user_email, api_key_id, api_key_name, group_id, group_name,
    endpoint, provider, model, protocol, body_hash, message_hashes, message_count
) VALUES (
    $1, $2, $3, $4, $5, $6, $7,
    $8, $9, $10, $11, $12, $13::jsonb, $14
) RETURNING id, created_at`,
		log.RequestID, userID, log.UserEmail, apiKeyID, log.APIKeyName, groupID, log.GroupName,
		log.Endpoint, log.Provider, log.Model, log.Protocol, log.BodyHash, string(messageHashes), log.MessageCount,
	).Scan(&log.ID, &log.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert full audit request log: %w", err)
	}
	return nil
}
