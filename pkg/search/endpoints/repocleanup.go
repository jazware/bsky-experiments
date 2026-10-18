package endpoints

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/mail"
	"strings"
	"sync"
	"time"

	comatproto "github.com/bluesky-social/indigo/api/atproto"
	"github.com/bluesky-social/indigo/atproto/identity"
	"github.com/bluesky-social/indigo/atproto/syntax"
	"github.com/bluesky-social/indigo/xrpc"
	"github.com/google/uuid"
	"github.com/jazware/bsky-experiments/pkg/indexer/store"
	"github.com/labstack/echo/v4"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"golang.org/x/sync/semaphore"
	"golang.org/x/time/rate"
)

var cleanupUserAgent = "jaz-repo-cleanup-tool/0.0.1"

func (api *API) CleanupOldRecords(c echo.Context) error {
	ctx := c.Request().Context()
	ctx, span := tracer.Start(ctx, "CleanupOldRecords")
	defer span.End()

	var req CleanupOldRecordsRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": fmt.Errorf("error parsing request: %w", err).Error()})
	}

	res, err := api.enqueueCleanupJob(ctx, req)
	if errors.Is(err, errScanInProgress) {
		return c.JSON(http.StatusTooManyRequests, map[string]string{"error": err.Error()})
	}
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Errorf("error cleaning up records: %w", err).Error()})
	}

	return c.JSON(http.StatusOK, res)
}

func (api *API) GetCleanupStatus(c echo.Context) error {
	ctx := c.Request().Context()
	ctx, span := tracer.Start(ctx, "GetCleanupStatus")
	defer span.End()

	var req GetCleanupStatusRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	if req.JobID != "" {
		job, err := api.Store.GetRepoCleanupJob(ctx, req.JobID)
		if err != nil {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "job not found"})
		}
		job.RefreshToken = ""
		return c.JSON(http.StatusOK, job)
	}

	if req.DID != "" {
		jobs, err := api.Store.GetCleanupJobsByRepo(ctx, req.DID, 100)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Errorf("error getting jobs: %w", err).Error()})
		}
		if len(jobs) == 0 {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "no jobs found for the provided DID"})
		}
		for i := range jobs {
			jobs[i].RefreshToken = ""
		}
		return c.JSON(http.StatusOK, jobs)
	}

	return c.JSON(http.StatusBadRequest, map[string]string{"error": "must specify either job_id or did in query params"})
}

func (api *API) GetCleanupStats(c echo.Context) error {
	ctx := c.Request().Context()
	ctx, span := tracer.Start(ctx, "GetCleanupStats")
	defer span.End()

	cleanupStats, err := api.Store.GetCleanupStats(ctx)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Errorf("error getting cleanup metadata: %w", err).Error()})
	}

	return c.JSON(http.StatusOK, cleanupStats)
}

func (api *API) CancelCleanupJob(c echo.Context) error {
	ctx := c.Request().Context()
	ctx, span := tracer.Start(ctx, "CancelCleanupJob")
	defer span.End()

	var req CancelCleanupJobRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	if req.JobID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "must specify job_id in query params"})
	}

	job, err := api.Store.GetRepoCleanupJob(ctx, req.JobID)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "job not found"})
	}

	if job.JobState == "completed" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "job already completed"})
	}

	job.JobState = "cancelled"
	job.RefreshToken = ""
	job.UpdatedAt = time.Now().UTC()

	_, err = api.Store.UpsertRepoCleanupJob(ctx, *job)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Errorf("error updating job: %w", err).Error()})
	}

	return c.JSON(http.StatusOK, map[string]string{"message": "job cancelled"})
}

type cleanupInfo struct {
	NumEnqueued int    `json:"num_enqueued"`
	DryRun      bool   `json:"dry_run"`
	Message     string `json:"message,omitempty"`
	JobID       string `json:"job_id,omitempty"`
}

func (api *API) enqueueCleanupJob(ctx context.Context, req CleanupOldRecordsRequest) (*cleanupInfo, error) {
	log := slog.With("source", "cleanup_old_records_handler")

	var ident *identity.Identity

	atID, err := syntax.ParseAtIdentifier(req.Identifier)
	if err == nil {
		ident, err = api.Directory.Lookup(ctx, atID)
		if err != nil {
			log.Error("Error looking up identity", "error", err)
			return nil, fmt.Errorf("error looking up identity: %w", err)
		}
	} else if emailAddr, err := mail.ParseAddress(req.Identifier); err == nil && emailAddr != nil {
		// Identifier is a valid email address
	} else {
		log.Error("Failed to parse identifier as at-identifier or email address", "identifier", req.Identifier, "error", err)
		return nil, fmt.Errorf("failed to parse identifier as at-identifier or email address: %w", err)
	}

	pdsHost := "https://bsky.social"
	if ident != nil &&
		ident.PDSEndpoint() != "" &&
		!strings.HasSuffix(ident.PDSEndpoint(), ".host.bsky.network") {
		pdsHost = ident.PDSEndpoint()
	}

	client := xrpc.Client{
		Client: &http.Client{
			Transport: otelhttp.NewTransport(http.DefaultTransport),
			Timeout:   5 * time.Minute,
		},
		Host:      pdsHost,
		UserAgent: &cleanupUserAgent,
	}

	if api.MagicHeaderVal != "" {
		client.Headers = map[string]string{"x-ratelimit-bypass": api.MagicHeaderVal}
	}

	log = log.With("identifier", req.Identifier)

	out, err := comatproto.ServerCreateSession(ctx, &client, &comatproto.ServerCreateSession_Input{
		Identifier: req.Identifier,
		Password:   req.AppPassword,
	})
	if err != nil {
		log.Error("Error logging in", "error", err)
		return nil, fmt.Errorf("error logging in: %w", err)
	}

	client.Auth = &xrpc.AuthInfo{
		Did:        out.Did,
		Handle:     out.Handle,
		RefreshJwt: out.RefreshJwt,
		AccessJwt:  out.AccessJwt,
	}

	log = log.With("did", out.Did)
	log = log.With("handle", out.Handle)

	existingJobs, err := api.Store.GetRunningCleanupJobsByRepo(ctx, out.Did)
	if err != nil {
		log.Error("Error getting existing jobs", "error", err)
	} else if len(existingJobs) > 0 {
		log.Info("Found existing job for DID, skipping")
		return &cleanupInfo{
			JobID:   existingJobs[0].JobID,
			Message: "Found existing active job for your account, you can only have one job running at a time.",
		}, nil
	}

	did, err := syntax.ParseDID(out.Did)
	if err != nil {
		log.Error("Error parsing DID", "error", err)
		return nil, fmt.Errorf("error parsing DID: %w", err)
	}

	if ident == nil {
		ident, err = api.Directory.LookupDID(ctx, did)
		if err != nil {
			log.Error("Error looking up DID", "error", err)
			return nil, fmt.Errorf("error looking up DID: %w", err)
		}
	}

	client.Host = ident.PDSEndpoint()

	if client.Host == "" {
		log.Error("No PDS endpoint found for DID")
		return nil, fmt.Errorf("no PDS endpoint found for DID")
	}

	log = log.With("pds", client.Host)

	crit := cleanupCriteria{
		types:  req.CleanupTypes,
		before: time.Now().AddDate(0, 0, -req.DeleteUntilDaysAgo),
	}
	release, err := api.repoScans().acquire(ctx, did.String())
	if err != nil {
		log.Info("Not scanning repo", "error", err)
		return nil, err
	}
	scan, err := scanRepo(ctx, &client, did.String(), crit, 0)
	release()
	if err != nil {
		log.Error("Error scanning repo", "error", err)
		return nil, err
	}

	log.Info("Found records to delete", "count", scan.Total, "mst_bytes", scan.NodeBytes)

	info := cleanupInfo{
		NumEnqueued: scan.Total,
		DryRun:      !req.ActuallyDeleteStuff,
		Message:     "Records will not be deleted",
	}
	if req.ActuallyDeleteStuff {
		now := time.Now().UTC()
		job := store.RepoCleanupJob{
			JobID:           uuid.New().String(),
			Repo:            did.String(),
			RefreshToken:    out.RefreshJwt,
			CleanupTypes:    req.CleanupTypes,
			DeleteOlderThan: time.Now().UTC().AddDate(0, 0, -req.DeleteUntilDaysAgo),
			NumDeleted:      0,
			NumDeletedToday: 0,
			EstNumRemaining: int64(scan.Total),
			JobState:        "running",
			CreatedAt:       now,
			UpdatedAt:       now,
			LastDeletedAt:   nil,
		}

		createdJob, err := api.Store.UpsertRepoCleanupJob(ctx, job)
		if err != nil {
			log.Error("Error creating job", "error", err)
			return nil, fmt.Errorf("error creating job: %w", err)
		}

		info.Message = fmt.Sprintf("%d records enqueued for deletion in job at: https://bsky-search.jazco.io/repo/cleanup?job_id=%s", scan.Total, createdJob.JobID)
		info.JobID = createdJob.JobID
		log.Info("Created cleanup job", "job_id", createdJob.JobID)
	}

	return &info, nil
}

func (api *API) RunCleanupDaemon(ctx context.Context) {
	log := slog.With("source", "cleanup_daemon")
	log.Info("Starting cleanup daemon")

	for {
		log.Info("Getting jobs to process")
		jobs, err := api.Store.GetRunningCleanupJobs(ctx, 100)
		if err != nil {
			log.Error("Error getting running jobs", "error", err)
			time.Sleep(30 * time.Second)
			continue
		}

		// Filter for jobs that haven't had a deletion run in the last hour and haven't hit the max for the day
		jobsToRun := []*store.RepoCleanupJob{}
		anHourAgo := time.Now().UTC().Add(-1 * time.Hour)
		aDayAgo := time.Now().UTC().Add(-24 * time.Hour)
		for i := range jobs {
			job := jobs[i]
			if job.LastDeletedAt == nil || // Add new jobs
				(job.LastDeletedAt.Before(anHourAgo) && job.NumDeletedToday < int64(maxDeletesPerDay)) ||
				(job.LastDeletedAt.Before(aDayAgo)) {
				jobsToRun = append(jobsToRun, &job)
			}
		}

		log.Info("Found jobs to run", "count", len(jobsToRun))

		if len(jobsToRun) == 0 {
			log.Info("No jobs to run, sleeping")
			time.Sleep(30 * time.Second)
			continue
		}

		wg := sync.WaitGroup{}
		// Repo downloads also share api.repoScans; the rest of a job is
		// rate-limited deletes.
		maxConcurrent := 10
		sem := semaphore.NewWeighted(int64(maxConcurrent))
		for i := range jobsToRun {
			job := jobsToRun[i]
			wg.Add(1)
			if err := sem.Acquire(ctx, 1); err != nil {
				log.Error("Error acquiring semaphore", "error", err)
				wg.Done()
				continue
			}
			go func(job store.RepoCleanupJob) {
				defer func() {
					sem.Release(1)
					wg.Done()
				}()
				log := log.With("job_id", job.JobID, "did", job.Repo)
				resJob, err := api.cleanupNextBatch(ctx, job)
				if errors.Is(err, errScanInProgress) {
					return
				}
				if err != nil {
					log.Error("Error cleaning up batch", "error", err)
					if resJob == nil {
						resJob = &job
					}
					// Save the job so it keeps a rotated refresh token and
					// waits out the hour before its next try.
					now := time.Now().UTC()
					resJob.LastDeletedAt = &now
					resJob.UpdatedAt = now
				}

				if resJob != nil {
					_, err = api.Store.UpsertRepoCleanupJob(ctx, *resJob)
					if err != nil {
						log.Error("Error updating job", "error", err)
						return
					}
				}
			}(*job)
		}

		wg.Wait()
		log.Info("Finished running jobs, sleeping")
		time.Sleep(30 * time.Second)
	}
}

var maxDeletesPerHour = 4000
var maxDeletesPerDay = 30_000

func (api *API) cleanupNextBatch(ctx context.Context, job store.RepoCleanupJob) (*store.RepoCleanupJob, error) {
	ctx, span := tracer.Start(ctx, "cleanupNextBatch")
	defer span.End()

	log := slog.With("source", "cleanup_next_batch", "job_id", job.JobID, "did", job.Repo)
	log.Info("Cleaning up next batch")
	// If the last deletion job ran today and we're at the max for the day, return
	if job.LastDeletedAt != nil && job.LastDeletedAt.UTC().Day() == time.Now().UTC().Day() && job.NumDeletedToday >= int64(maxDeletesPerDay) {
		log.Info("Already deleted max records today, skipping")
		return nil, nil
	} else if job.LastDeletedAt != nil && job.LastDeletedAt.UTC().Day() != time.Now().UTC().Day() {
		// If the last deletion job ran on a different day, reset the daily counter
		job.NumDeletedToday = 0
	}

	// Take a scan slot before refreshing: the refresh rotates the token, and
	// a job that then waited and gave up would have to save it.
	release, err := api.repoScans().acquire(ctx, job.Repo)
	if err != nil {
		return nil, err
	}
	defer release()

	ident, err := api.Directory.LookupDID(ctx, syntax.DID(job.Repo))
	if err != nil {
		log.Error("Error looking up DID", "error", err)
		return nil, fmt.Errorf("error looking up DID: %w", err)
	}

	client := xrpc.Client{
		Client: &http.Client{
			Transport: otelhttp.NewTransport(http.DefaultTransport),
			Timeout:   5 * time.Minute,
		},
		Host:      "https://bsky.social",
		UserAgent: &cleanupUserAgent,
	}

	if api.MagicHeaderVal != "" {
		client.Headers = map[string]string{"x-ratelimit-bypass": api.MagicHeaderVal}
	}

	client.Auth = &xrpc.AuthInfo{
		Did:       job.Repo,
		AccessJwt: job.RefreshToken,
	}

	// Talk directly to the user's PDS if the PDS isn't hosted by bsky
	if !strings.HasSuffix(ident.PDSEndpoint(), ".host.bsky.network") {
		client.Host = ident.PDSEndpoint()
	}

	out, err := comatproto.ServerRefreshSession(ctx, &client)
	if err != nil {
		log.Error("Error refreshing session", "error", err)
		if strings.Contains(err.Error(), "ExpiredToken") {
			job.RefreshToken = ""
			job.JobState = "errored: ExpiredToken"
			job.UpdatedAt = time.Now().UTC()
			return &job, nil
		}
		if strings.Contains(err.Error(), "Could not find user info for account") {
			job.RefreshToken = ""
			job.JobState = "errored: Could not find user info for account (account may have been deleted)"
			job.UpdatedAt = time.Now().UTC()
			return &job, nil
		}
		return nil, fmt.Errorf("error refreshing session: %w", err)
	}

	client.Auth = &xrpc.AuthInfo{
		Did:        job.Repo,
		Handle:     out.Handle,
		RefreshJwt: out.RefreshJwt,
		AccessJwt:  out.AccessJwt,
	}

	// The old refresh token is revoked now: every return from here on hands
	// back the job, so the daemon saves the new one.
	job.RefreshToken = out.RefreshJwt

	log = log.With("pds", client.Host)

	client.Host = ident.PDSEndpoint()

	crit := cleanupCriteria{types: job.CleanupTypes, before: job.DeleteOlderThan}
	scan, err := scanRepo(ctx, &client, job.Repo, crit, maxDeletesPerHour+1)
	release()
	if err != nil {
		log.Error("Error scanning repo", "error", err)
		return &job, err
	}

	log.Info("Found records to delete", "count", scan.Total, "mst_bytes", scan.NodeBytes)

	deleteBatches := []*comatproto.RepoApplyWrites_Input{}
	batchNum := 0
	for i, path := range scan.Paths {
		if i > maxDeletesPerHour || job.NumDeletedToday+int64(i) > int64(maxDeletesPerDay) {
			break
		}
		if i%10 == 0 {
			if i != 0 {
				batchNum++
			}
			nextBatch := comatproto.RepoApplyWrites_Input{
				Repo:   job.Repo,
				Writes: []*comatproto.RepoApplyWrites_Input_Writes_Elem{},
			}
			deleteBatches = append(deleteBatches, &nextBatch)
		}

		collection := strings.Split(path, "/")[0]
		rkey := strings.Split(path, "/")[1]

		deleteObj := comatproto.RepoApplyWrites_Delete{
			Collection: collection,
			Rkey:       rkey,
		}

		deleteBatch := deleteBatches[batchNum]
		deleteBatch.Writes = append(deleteBatch.Writes, &comatproto.RepoApplyWrites_Input_Writes_Elem{
			RepoApplyWrites_Delete: &deleteObj,
		})
	}

	numDeleted := 0
	recordProgress := func() {
		job.NumDeletedToday += int64(numDeleted)
		job.NumDeleted += int64(numDeleted)
		job.EstNumRemaining = int64(scan.Total - numDeleted)
	}

	limiter := rate.NewLimiter(rate.Limit(4), 1)
	for _, batch := range deleteBatches {
		if err := limiter.Wait(ctx); err != nil {
			log.Error("Error waiting for rate limiter", "error", err)
			recordProgress()
			return &job, fmt.Errorf("error waiting for rate limiter: %w", err)
		}

		_, err := comatproto.RepoApplyWrites(ctx, &client, batch)
		if err != nil {
			log.Error("Error applying writes", "error", err)
			recordProgress()
			return &job, fmt.Errorf("Errored out after deleting (%d) records: %w", numDeleted, err)
		}
		numDeleted += len(batch.Writes)
	}

	recordProgress()

	log.Info("Deleted records",
		"count", numDeleted,
		"total", scan.Total,
		"est_remaining", job.EstNumRemaining,
	)

	now := time.Now().UTC()
	job.UpdatedAt = now
	job.LastDeletedAt = &now

	if job.EstNumRemaining <= 0 {
		job.RefreshToken = ""
		job.JobState = "completed"
	}

	return &job, nil
}
