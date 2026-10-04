package endpoints

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/araddon/dateparse"
	"github.com/bluesky-social/indigo/api/bsky"
	"github.com/bluesky-social/indigo/atproto/syntax"
	"github.com/bluesky-social/indigo/repo"
	"github.com/ipfs/go-cid"
	"github.com/jazware/bsky-experiments/telemetry"
	"github.com/labstack/echo/v4"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

func (api *API) GetRepoAsJSON(c echo.Context) error {
	ctx := c.Request().Context()
	ctx, span := tracer.Start(ctx, "GetRepoAsJSON")
	defer span.End()

	var req GetRepoRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	repoDID := req.DID
	if repoDID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "did must be provided in path"})
	}

	var url = "https://bsky.network/xrpc/com.atproto.sync.getRepo?did=" + repoDID

	// GET and CAR decode the body
	client := &http.Client{
		Transport: otelhttp.NewTransport(http.DefaultTransport),
		Timeout:   120 * time.Second,
	}
	httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		log.Printf("Error creating request: %v", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Error creating request"})
	}

	httpReq.Header.Set("Accept", "application/vnd.ipld.car")
	httpReq.Header.Set("User-Agent", "jaz-repo-checkout-search-API/0.0.1")

	// Do your rate limit wait here
	api.CheckoutLimiter.Wait(ctx)

	resp, err := client.Do(httpReq)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Errorf("Error getting repo from BSky: %w", err).Error()})
	}

	if resp.StatusCode != http.StatusOK {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Errorf("Error getting repo from BSky: %s", resp.Status).Error()})
	}

	r, err := repo.ReadRepoFromCar(ctx, resp.Body)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Errorf("Error reading repo CAR: %w", err).Error()})
	}

	err = resp.Body.Close()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Errorf("Error closing response body: %w", err).Error()})
	}

	repoJSON := ""
	posts := []string{}
	likes := []string{}
	reposts := []string{}
	follows := []string{}
	blocks := []string{}
	profile := ""

	// indigo opens a span per record: keep the repo walk out of the trace.
	walkCtx := telemetry.WithoutChildSpans(ctx)
	r.ForEach(walkCtx, "", func(path string, nodeCid cid.Cid) error {
		recordCid, rec, err := r.GetRecord(walkCtx, path)
		if err != nil {
			log.Printf("Error getting record: %v", err)
			return nil
		}

		// Verify that the record cid matches the cid in the event
		if recordCid != nodeCid {
			log.Printf("mismatch in record and op cid: %s != %s", recordCid, nodeCid)
			return nil
		}

		// Do something with the record `rec`
		// collection := strings.Split(path, "/")[0]
		// rkey := strings.Split(path, "/")[1]
		switch rec := rec.(type) {
		case *bsky.FeedPost:
			recCreatedAt, parseError := dateparse.ParseAny(rec.CreatedAt)
			if parseError != nil {
				log.Printf("Error parsing date: %v", parseError)
				return nil
			}
			rec.CreatedAt = recCreatedAt.Format(time.RFC3339)
			recBytes, err := json.Marshal(rec)
			if err != nil {
				log.Printf("Error marshaling record: %v", err)
				return nil
			}
			entity := fmt.Sprintf(`{"uri":"at://%s/%s","content":%s}`, repoDID, path, string(recBytes))
			posts = append(posts, string(entity))
		case *bsky.FeedLike:
			recCreatedAt, parseError := dateparse.ParseAny(rec.CreatedAt)
			if parseError != nil {
				log.Printf("Error parsing date: %v", parseError)
				return nil
			}
			rec.CreatedAt = recCreatedAt.Format(time.RFC3339)
			recBytes, err := json.Marshal(rec)
			if err != nil {
				log.Printf("Error marshaling record: %v", err)
				return nil
			}
			entity := fmt.Sprintf(`{"uri":"at://%s/%s","content":%s}`, repoDID, path, string(recBytes))
			likes = append(likes, string(entity))
		case *bsky.FeedRepost:
			recCreatedAt, parseError := dateparse.ParseAny(rec.CreatedAt)
			if parseError != nil {
				log.Printf("Error parsing date: %v", parseError)
				return nil
			}
			rec.CreatedAt = recCreatedAt.Format(time.RFC3339)
			recBytes, err := json.Marshal(rec)
			if err != nil {
				log.Printf("Error marshaling record: %v", err)
				return nil
			}
			entity := fmt.Sprintf(`{"uri":"at://%s/%s","content":%s}`, repoDID, path, string(recBytes))
			reposts = append(reposts, string(entity))
		case *bsky.GraphFollow:
			recCreatedAt, parseError := dateparse.ParseAny(rec.CreatedAt)
			if parseError != nil {
				log.Printf("Error parsing date: %v", parseError)
				return nil
			}
			rec.CreatedAt = recCreatedAt.Format(time.RFC3339)
			recBytes, err := json.Marshal(rec)
			if err != nil {
				log.Printf("Error marshaling record: %v", err)
				return nil
			}
			entity := fmt.Sprintf(`{"uri":"at://%s/%s","content":%s}`, repoDID, path, string(recBytes))
			follows = append(follows, string(entity))
		case *bsky.GraphBlock:
			recCreatedAt, parseError := dateparse.ParseAny(rec.CreatedAt)
			if parseError != nil {
				log.Printf("Error parsing date: %v", parseError)
				return nil
			}
			rec.CreatedAt = recCreatedAt.Format(time.RFC3339)
			recBytes, err := json.Marshal(rec)
			if err != nil {
				log.Printf("Error marshaling record: %v", err)
				return nil
			}
			entity := fmt.Sprintf(`{"uri":"at://%s/%s","content":%s}`, repoDID, path, string(recBytes))
			blocks = append(blocks, string(entity))
		case *bsky.ActorProfile:
			recBytes, err := json.Marshal(rec)
			if err != nil {
				log.Printf("Error marshaling record: %v", err)
				return nil
			}
			entity := fmt.Sprintf(`{"uri":"at://%s/%s","content":%s}`, repoDID, path, string(recBytes))
			profile = string(entity)
		}
		return nil
	})

	repoJSON = fmt.Sprintf(`{"posts": [%s],"likes": [%s],"reposts": [%s],"follows": [%s],"blocks": [%s],"profile": %s}`,
		strings.Join(posts, ","),
		strings.Join(likes, ","),
		strings.Join(reposts, ","),
		strings.Join(follows, ","),
		strings.Join(blocks, ","),
		profile,
	)

	return c.Blob(http.StatusOK, "application/json", []byte(repoJSON))
}

func (api *API) GetListMembers(c echo.Context) error {
	ctx := c.Request().Context()
	ctx, span := tracer.Start(ctx, "GetListMembers")
	defer span.End()

	var req GetListMembersRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}

	uriParam := req.URI
	if uriParam == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "uri must be provided in query"})
	}

	listURI, err := syntax.ParseATURI(uriParam)
	if err != nil {
		// Try to parse as https://bsky.app/profile/{handle_or_did}/lists/{rkey}
		parts := strings.Split(uriParam, "/")
		if len(parts) != 7 {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": fmt.Errorf("Error parsing URI: %w", err).Error()})
		}
		if parts[3] != "profile" && parts[5] != "lists" {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": fmt.Errorf("Error parsing URI: %w", err).Error()})
		}
		listURI = syntax.ATURI(fmt.Sprintf("at://%s/app.bsky.graph.list/%s", parts[4], parts[6]))
	}

	repoDID, err := listURI.Authority().AsDID()
	if err != nil {
		asHandle, err := listURI.Authority().AsHandle()
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": fmt.Errorf("Failed to parse handle or DID from URI: %w", err).Error()})
		}

		id, err := api.Directory.LookupHandle(ctx, asHandle)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Errorf("Error looking up handle: %w", err).Error()})
		}

		repoDID = id.DID
	}

	listURI = syntax.ATURI(fmt.Sprintf("at://%s/app.bsky.graph.list/%s", repoDID.String(), listURI.RecordKey().String()))

	var repoFetchURL = "https://bsky.network/xrpc/com.atproto.sync.getRepo?did=" + repoDID.String()

	// GET and CAR decode the body
	client := &http.Client{
		Transport: otelhttp.NewTransport(http.DefaultTransport),
		Timeout:   120 * time.Second,
	}
	httpReq, err := http.NewRequestWithContext(ctx, "GET", repoFetchURL, nil)
	if err != nil {
		log.Printf("Error creating request: %v", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Error creating request"})
	}

	httpReq.Header.Set("Accept", "application/vnd.ipld.car")
	httpReq.Header.Set("User-Agent", "jaz-repo-checkout-search-API/0.0.1")

	// Do your rate limit wait here
	api.CheckoutLimiter.Wait(ctx)

	resp, err := client.Do(httpReq)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Errorf("Error getting repo from BSky: %w", err).Error()})
	}

	if resp.StatusCode != http.StatusOK {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Errorf("Error getting repo from BSky: %s", resp.Status).Error()})
	}

	r, err := repo.ReadRepoFromCar(ctx, resp.Body)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Errorf("Error reading repo CAR: %w", err).Error()})
	}

	err = resp.Body.Close()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Errorf("Error closing response body: %w", err).Error()})
	}

	listMembers := []string{}
	listObj := &bsky.GraphList{}

	// indigo opens a span per record: keep the repo walk out of the trace.
	walkCtx := telemetry.WithoutChildSpans(ctx)
	r.ForEach(walkCtx, "app.bsky.graph.list", func(path string, nodeCid cid.Cid) error {
		recordCid, rec, err := r.GetRecord(walkCtx, path)
		if err != nil {
			log.Printf("Error getting record: %v", err)
			return nil
		}

		// Verify that the record cid matches the cid in the event
		if recordCid != nodeCid {
			log.Printf("mismatch in record and op cid: %s != %s", recordCid, nodeCid)
			return nil
		}

		pathParts := strings.Split(path, "/")
		if len(pathParts) < 2 {
			return nil
		}

		rkey := pathParts[1]

		switch rec := rec.(type) {
		case *bsky.GraphList:
			if rkey == listURI.RecordKey().String() {
				listObj = rec
			}
		case *bsky.GraphListitem:
			if rec.List == listURI.String() {
				listMembers = append(listMembers, rec.Subject)
			}
		}
		return nil
	})

	return c.JSON(http.StatusOK, map[string]any{"members": listMembers, "list": listObj})
}
