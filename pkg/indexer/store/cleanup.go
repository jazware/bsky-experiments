package store

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
)

type RepoCleanupJob struct {
	JobID           string     `json:"job_id"`
	Repo            string     `json:"repo"`
	RefreshToken    string     `json:"refresh_token"`
	CleanupTypes    []string   `json:"cleanup_types"`
	DeleteOlderThan time.Time  `json:"delete_older_than"`
	NumDeleted      int64      `json:"num_deleted"`
	NumDeletedToday int64      `json:"num_deleted_today"`
	EstNumRemaining int64      `json:"est_num_remaining"`
	JobState        string     `json:"job_state"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	LastDeletedAt   *time.Time `json:"last_deleted_at"`
}

func (s *Store) UpsertRepoCleanupJob(ctx context.Context, job RepoCleanupJob) (result *RepoCleanupJob, err error) {
	_, done := observe(ctx, "exec", &err,
		attribute.String("job.id", job.JobID),
		attribute.String("repo", job.Repo),
		attribute.String("job.state", job.JobState))
	defer done()

	timeUs := time.Now().UnixMicro()

	query := `
		INSERT INTO repo_cleanup_jobs (
			job_id, repo, refresh_token, cleanup_types, delete_older_than,
			num_deleted, num_deleted_today, est_num_remaining, job_state,
			created_at, updated_at, last_deleted_at, time_us
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	err = s.DB.Exec(ctx, query,
		job.JobID,
		job.Repo,
		job.RefreshToken,
		job.CleanupTypes,
		job.DeleteOlderThan,
		job.NumDeleted,
		job.NumDeletedToday,
		job.EstNumRemaining,
		job.JobState,
		job.CreatedAt,
		job.UpdatedAt,
		job.LastDeletedAt,
		timeUs,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to upsert repo cleanup job: %w", err)
	}

	return &job, nil
}

func (s *Store) GetRepoCleanupJob(ctx context.Context, jobID string) (job *RepoCleanupJob, err error) {
	ctx, done := observe(ctx, "query_row", &err,
		attribute.String("job.id", jobID))
	defer done()

	query := `
		SELECT
			job_id, repo, refresh_token, cleanup_types, delete_older_than,
			num_deleted, num_deleted_today, est_num_remaining, job_state,
			created_at, updated_at, last_deleted_at
		FROM repo_cleanup_jobs
		WHERE job_id = ?
		ORDER BY time_us DESC
		LIMIT 1
	`

	job = &RepoCleanupJob{}
	err = s.DB.QueryRow(ctx, query, jobID).Scan(
		&job.JobID,
		&job.Repo,
		&job.RefreshToken,
		&job.CleanupTypes,
		&job.DeleteOlderThan,
		&job.NumDeleted,
		&job.NumDeletedToday,
		&job.EstNumRemaining,
		&job.JobState,
		&job.CreatedAt,
		&job.UpdatedAt,
		&job.LastDeletedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get repo cleanup job: %w", err)
	}

	return job, nil
}

// The table is a ReplacingMergeTree keyed by job_id: every update is a new
// row, and old versions stay visible until a merge. GetCleanupJobsByRepo and
// the GetRunningCleanupJobs queries take each job's latest row before
// filtering, or a finished job's older "running" row brings it back with a
// refresh token that's been revoked.
func (s *Store) GetCleanupJobsByRepo(ctx context.Context, repo string, limit int) (jobs []RepoCleanupJob, err error) {
	ctx, done := observe(ctx, "query", &err,
		attribute.String("repo", repo),
		attribute.Int("limit", limit))
	defer func() { done(attribute.Int("result.count", len(jobs))) }()

	query := `
		SELECT * FROM (
			SELECT
				job_id, repo, refresh_token, cleanup_types, delete_older_than,
				num_deleted, num_deleted_today, est_num_remaining, job_state,
				created_at, updated_at, last_deleted_at
			FROM repo_cleanup_jobs
			WHERE repo = ?
			ORDER BY time_us DESC
			LIMIT 1 BY job_id
		)
		ORDER BY updated_at DESC
		LIMIT ?
	`

	rows, err := s.DB.Query(ctx, query, repo, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query cleanup jobs by repo: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var job RepoCleanupJob
		if err := rows.Scan(
			&job.JobID,
			&job.Repo,
			&job.RefreshToken,
			&job.CleanupTypes,
			&job.DeleteOlderThan,
			&job.NumDeleted,
			&job.NumDeletedToday,
			&job.EstNumRemaining,
			&job.JobState,
			&job.CreatedAt,
			&job.UpdatedAt,
			&job.LastDeletedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan cleanup job: %w", err)
		}
		jobs = append(jobs, job)
	}

	if err = rows.Err(); err != nil {
		return nil, err
	}
	return jobs, nil
}

func (s *Store) GetRunningCleanupJobsByRepo(ctx context.Context, repo string) (jobs []RepoCleanupJob, err error) {
	ctx, done := observe(ctx, "query", &err,
		attribute.String("repo", repo))
	defer func() { done(attribute.Int("result.count", len(jobs))) }()

	query := `
		SELECT * FROM (
			SELECT
				job_id, repo, refresh_token, cleanup_types, delete_older_than,
				num_deleted, num_deleted_today, est_num_remaining, job_state,
				created_at, updated_at, last_deleted_at
			FROM repo_cleanup_jobs
			WHERE repo = ?
			ORDER BY time_us DESC
			LIMIT 1 BY job_id
		)
		WHERE job_state = 'running'
		ORDER BY updated_at DESC
	`

	rows, err := s.DB.Query(ctx, query, repo)
	if err != nil {
		return nil, fmt.Errorf("failed to query running cleanup jobs: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var job RepoCleanupJob
		if err := rows.Scan(
			&job.JobID,
			&job.Repo,
			&job.RefreshToken,
			&job.CleanupTypes,
			&job.DeleteOlderThan,
			&job.NumDeleted,
			&job.NumDeletedToday,
			&job.EstNumRemaining,
			&job.JobState,
			&job.CreatedAt,
			&job.UpdatedAt,
			&job.LastDeletedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan cleanup job: %w", err)
		}
		jobs = append(jobs, job)
	}

	if err = rows.Err(); err != nil {
		return nil, err
	}
	return jobs, nil
}

func (s *Store) GetRunningCleanupJobs(ctx context.Context, limit int) (jobs []RepoCleanupJob, err error) {
	ctx, done := observe(ctx, "query", &err,
		attribute.Int("limit", limit))
	defer func() { done(attribute.Int("result.count", len(jobs))) }()

	query := `
		SELECT * FROM (
			SELECT
				job_id, repo, refresh_token, cleanup_types, delete_older_than,
				num_deleted, num_deleted_today, est_num_remaining, job_state,
				created_at, updated_at, last_deleted_at
			FROM repo_cleanup_jobs
			ORDER BY time_us DESC
			LIMIT 1 BY job_id
		)
		WHERE job_state = 'running'
		ORDER BY updated_at ASC
		LIMIT ?
	`

	rows, err := s.DB.Query(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query running cleanup jobs: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var job RepoCleanupJob
		if err := rows.Scan(
			&job.JobID,
			&job.Repo,
			&job.RefreshToken,
			&job.CleanupTypes,
			&job.DeleteOlderThan,
			&job.NumDeleted,
			&job.NumDeletedToday,
			&job.EstNumRemaining,
			&job.JobState,
			&job.CreatedAt,
			&job.UpdatedAt,
			&job.LastDeletedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan cleanup job: %w", err)
		}
		jobs = append(jobs, job)
	}

	if err = rows.Err(); err != nil {
		return nil, err
	}
	return jobs, nil
}

type CleanupStats struct {
	TotalNumDeleted int64  `json:"total_num_deleted"`
	NumJobs         uint64 `json:"num_jobs"`
	NumRepos        uint64 `json:"num_repos"`
}

func (s *Store) GetCleanupStats(ctx context.Context) (stats *CleanupStats, err error) {
	ctx, done := observe(ctx, "query_row", &err)
	defer done()

	query := `
		SELECT
			sum(num_deleted) AS total_num_deleted,
			count() AS num_jobs,
			uniq(repo) AS num_repos
		FROM repo_cleanup_jobs
	`

	stats = &CleanupStats{}
	err = s.DB.QueryRow(ctx, query).Scan(
		&stats.TotalNumDeleted,
		&stats.NumJobs,
		&stats.NumRepos,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get cleanup stats: %w", err)
	}

	return stats, nil
}

func (s *Store) DeleteRepoCleanupJob(ctx context.Context, jobID string) error {
	// ReplacingMergeTree rows can't be deleted; this is a deliberate no-op.
	return nil
}
