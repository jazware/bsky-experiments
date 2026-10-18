package store

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
)

type APIKey struct {
	APIKey       string
	AuthEntity   []byte
	AssignedUser string
	CreatedAt    time.Time
}

func (s *Store) CreateAPIKey(ctx context.Context, apiKey, authEntity, assignedUser string) (err error) {
	_, done := observe(ctx, "exec", &err,
		attribute.String("assigned.actor", assignedUser))
	defer done()

	query := `
		INSERT INTO api_keys (api_key, auth_entity, assigned_user, time_us, deleted)
		VALUES (?, ?, ?, ?, 0)
	`

	timeUs := time.Now().UnixMicro()
	err = s.DB.Exec(ctx, query, apiKey, authEntity, assignedUser, timeUs)
	if err != nil {
		return fmt.Errorf("failed to create API key: %w", err)
	}

	return nil
}

func (s *Store) GetAPIKey(ctx context.Context, apiKey string) (key *APIKey, err error) {
	ctx, done := observe(ctx, "query_row", &err)
	defer done()

	query := `
		SELECT api_key, auth_entity, assigned_user, created_at
		FROM api_keys
		WHERE api_key = ? AND deleted = 0
		ORDER BY time_us DESC
		LIMIT 1
	`

	key = &APIKey{}
	err = s.DB.QueryRow(ctx, query, apiKey).Scan(
		&key.APIKey,
		&key.AuthEntity,
		&key.AssignedUser,
		&key.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get API key: %w", err)
	}

	return key, nil
}
