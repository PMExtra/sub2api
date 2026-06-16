package repository

import (
	"context"
	"regexp"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestFullAuditRepositoryUpsertMessages_BestEffortContinuesAfterError(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	repo := NewFullAuditRepository(db)
	insertSQL := regexp.QuoteMeta("INSERT INTO audit_message_kv (message_hash, protocol, role, raw_message, raw_message_size)")
	mock.ExpectExec(insertSQL).
		WithArgs("hash1", service.FullAuditProtocolOpenAIChat, "user", `{"role":"user","content":"one"}`, 31).
		WillReturnError(context.DeadlineExceeded)
	mock.ExpectExec(insertSQL).
		WithArgs("hash2", service.FullAuditProtocolOpenAIChat, "user", `{"role":"user","content":"two"}`, 31).
		WillReturnResult(sqlmock.NewResult(0, 1))

	err = repo.UpsertMessages(context.Background(), []service.FullAuditMessageRecord{
		{Hash: "hash1", Protocol: service.FullAuditProtocolOpenAIChat, Role: "user", Raw: `{"role":"user","content":"one"}`, Size: 31},
		{Hash: "hash2", Protocol: service.FullAuditProtocolOpenAIChat, Role: "user", Raw: `{"role":"user","content":"two"}`, Size: 31},
	})

	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestFullAuditRepositoryCreateRequestLog_InsertsSnapshotFields(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	repo := NewFullAuditRepository(db)
	userID := int64(1001)
	apiKeyID := int64(2001)
	groupID := int64(3001)
	insertSQL := regexp.QuoteMeta("INSERT INTO audit_request_logs (")
	mock.ExpectQuery(insertSQL).
		WithArgs(
			"req_1", userID, "user@example.com", apiKeyID, "key", groupID, "group",
			"/v1/chat/completions", "openai", "gpt-test", service.FullAuditProtocolOpenAIChat,
			"body_hash", `["hash1","hash2"]`, 2,
		).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), time.Now()))

	log := &service.FullAuditRequestLog{
		RequestID:     "req_1",
		UserID:        &userID,
		UserEmail:     "user@example.com",
		APIKeyID:      &apiKeyID,
		APIKeyName:    "key",
		GroupID:       &groupID,
		GroupName:     "group",
		Endpoint:      "/v1/chat/completions",
		Provider:      "openai",
		Model:         "gpt-test",
		Protocol:      service.FullAuditProtocolOpenAIChat,
		BodyHash:      "body_hash",
		MessageHashes: []string{"hash1", "hash2"},
		MessageCount:  2,
	}

	err = repo.CreateRequestLog(context.Background(), log)

	require.NoError(t, err)
	require.Equal(t, int64(1), log.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}
