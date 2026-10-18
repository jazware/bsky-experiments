package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
)

func (s *Store) ActorHasLabel(ctx context.Context, actorDID, label string) (hasLabel bool, err error) {
	ctx, done := observe(ctx, "query_row", &err,
		attribute.String("actor.did", actorDID),
		attribute.String("label", label))
	defer func() { done(attribute.Bool("has.label", hasLabel)) }()

	query := `
		SELECT count() > 0
		FROM actor_labels FINAL
		WHERE actor_did = ? AND label = ? AND deleted = 0
	`

	err = s.DB.QueryRow(ctx, query, actorDID, label).Scan(&hasLabel)
	if err != nil {
		return false, fmt.Errorf("failed to check actor label: %w", err)
	}

	return hasLabel, nil
}

func (s *Store) CreateActorLabel(ctx context.Context, actorDID, label string) (err error) {
	_, done := observe(ctx, "exec", &err,
		attribute.String("actor.did", actorDID),
		attribute.String("label", label))
	defer done()

	query := `
		INSERT INTO actor_labels (actor_did, label, time_us, deleted)
		VALUES (?, ?, ?, 0)
	`

	timeUs := time.Now().UnixMicro()
	err = s.DB.Exec(ctx, query, actorDID, label, timeUs)
	if err != nil {
		return fmt.Errorf("failed to create actor label: %w", err)
	}

	return nil
}

func (s *Store) DeleteActorLabel(ctx context.Context, actorDID, label string) (err error) {
	_, done := observe(ctx, "exec", &err,
		attribute.String("actor.did", actorDID),
		attribute.String("label", label))
	defer done()

	query := `
		INSERT INTO actor_labels (actor_did, label, time_us, deleted)
		VALUES (?, ?, ?, 1)
	`

	timeUs := time.Now().UnixMicro()
	err = s.DB.Exec(ctx, query, actorDID, label, timeUs)
	if err != nil {
		return fmt.Errorf("failed to delete actor label: %w", err)
	}

	return nil
}

func (s *Store) ListActorsByLabel(ctx context.Context, label string) (actors []string, err error) {
	ctx, done := observe(ctx, "query", &err,
		attribute.String("label", label))
	defer func() { done(attribute.Int("result.count", len(actors))) }()

	query := `
		SELECT DISTINCT actor_did
		FROM actor_labels FINAL
		WHERE label = ? AND deleted = 0
		ORDER BY actor_did
	`

	rows, err := s.DB.Query(ctx, query, label)
	if err != nil {
		return nil, fmt.Errorf("failed to list actors by label: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var actorDID string
		if err := rows.Scan(&actorDID); err != nil {
			return nil, fmt.Errorf("failed to scan actor DID: %w", err)
		}
		actors = append(actors, actorDID)
	}

	if err = rows.Err(); err != nil {
		return nil, err
	}
	return actors, nil
}

type ActorLabelRow struct {
	ActorDID    string
	Handle      string
	DisplayName string
	CreatedAt   time.Time
	TimeUs      int64
}

// ListActorsByLabelPaginated returns actors with a specific label using page-based pagination
// Results are sorted by time added (most recent first) and joined with profiles for display info
// If search is provided, filters by handle or DID containing the search term
func (s *Store) ListActorsByLabelPaginated(ctx context.Context, label string, page int, limit int, search string) (actors []ActorLabelRow, totalCount uint64, err error) {
	ctx, done := observe(ctx, "query", &err,
		attribute.String("label", label),
		attribute.Int("page", page),
		attribute.Int("limit", limit),
		attribute.String("search", search))
	defer func() {
		done(attribute.Int("result.count", len(actors)), attribute.Int64("total.count", int64(totalCount)))
	}()

	// For search queries, we fetch all members and filter in Go to avoid expensive
	// ILIKE queries on the profiles table. Feeds typically have <10K members so this is fast.
	if search != "" {
		actors, totalCount, err = s.listActorsByLabelWithSearch(ctx, label, page, limit, search)
		if err != nil {
			return nil, 0, err
		}
		return actors, totalCount, nil
	}

	countQuery := `
		SELECT count(DISTINCT actor_did)
		FROM actor_labels FINAL
		WHERE label = ? AND deleted = 0
	`
	if err = s.DB.QueryRow(ctx, countQuery, label).Scan(&totalCount); err != nil {
		return nil, 0, fmt.Errorf("failed to count actors by label: %w", err)
	}

	offset := max((page-1)*limit, 0)

	query := `
		SELECT
			al.actor_did,
			COALESCE(p.handle, '') as handle,
			COALESCE(p.display_name, '') as display_name,
			al.created_at,
			al.time_us
		FROM (
			SELECT actor_did, toDateTime(time_us / 1000000) as created_at, time_us
			FROM actor_labels FINAL
			WHERE label = ? AND deleted = 0
			ORDER BY time_us DESC
			LIMIT ? OFFSET ?
		) al
		LEFT JOIN profiles p ON al.actor_did = p.did AND p.deleted = 0
		ORDER BY al.time_us DESC
	`

	rows, err := s.DB.Query(ctx, query, label, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list actors by label paginated: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var row ActorLabelRow
		if err := rows.Scan(&row.ActorDID, &row.Handle, &row.DisplayName, &row.CreatedAt, &row.TimeUs); err != nil {
			return nil, 0, fmt.Errorf("failed to scan actor row: %w", err)
		}
		actors = append(actors, row)
	}

	if err = rows.Err(); err != nil {
		return nil, 0, err
	}

	return actors, totalCount, nil
}

// listActorsByLabelWithSearch fetches all members with profiles and filters in Go
// This avoids expensive ILIKE queries on the large profiles table
func (s *Store) listActorsByLabelWithSearch(ctx context.Context, label string, page int, limit int, search string) ([]ActorLabelRow, uint64, error) {
	labelQuery := `
		SELECT actor_did, time_us
		FROM actor_labels FINAL
		WHERE label = ? AND deleted = 0
		ORDER BY time_us DESC
	`
	labelRows, err := s.DB.Query(ctx, labelQuery, label)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list actors by label: %w", err)
	}

	type memberInfo struct {
		actorDID string
		timeUs   int64
	}
	var members []memberInfo
	var dids []string
	for labelRows.Next() {
		var m memberInfo
		if err := labelRows.Scan(&m.actorDID, &m.timeUs); err != nil {
			labelRows.Close()
			return nil, 0, fmt.Errorf("failed to scan actor: %w", err)
		}
		members = append(members, m)
		dids = append(dids, m.actorDID)
	}
	labelRows.Close()
	if err := labelRows.Err(); err != nil {
		return nil, 0, err
	}

	if len(members) == 0 {
		return []ActorLabelRow{}, 0, nil
	}

	profileQuery := `
		SELECT did, handle, display_name
		FROM profiles
		WHERE did IN (?) AND deleted = 0
	`
	profileRows, err := s.DB.Query(ctx, profileQuery, dids)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to fetch profiles: %w", err)
	}

	profileMap := make(map[string]struct {
		handle      string
		displayName string
	})
	for profileRows.Next() {
		var did, handle, displayName string
		if err := profileRows.Scan(&did, &handle, &displayName); err != nil {
			profileRows.Close()
			return nil, 0, fmt.Errorf("failed to scan profile: %w", err)
		}
		profileMap[did] = struct {
			handle      string
			displayName string
		}{handle, displayName}
	}
	profileRows.Close()
	if err := profileRows.Err(); err != nil {
		return nil, 0, err
	}

	var allMembers []ActorLabelRow
	for _, m := range members {
		row := ActorLabelRow{
			ActorDID:  m.actorDID,
			CreatedAt: time.UnixMicro(m.timeUs),
			TimeUs:    m.timeUs,
		}
		if p, ok := profileMap[m.actorDID]; ok {
			row.Handle = p.handle
			row.DisplayName = p.displayName
		}
		allMembers = append(allMembers, row)
	}

	searchLower := strings.ToLower(search)
	var filtered []ActorLabelRow
	for _, m := range allMembers {
		if strings.Contains(strings.ToLower(m.ActorDID), searchLower) ||
			strings.Contains(strings.ToLower(m.Handle), searchLower) ||
			strings.Contains(strings.ToLower(m.DisplayName), searchLower) {
			filtered = append(filtered, m)
		}
	}

	totalCount := uint64(len(filtered))

	offset := max((page-1)*limit, 0)
	end := min(offset+limit, len(filtered))
	if offset >= len(filtered) {
		return []ActorLabelRow{}, totalCount, nil
	}

	return filtered[offset:end], totalCount, nil
}
