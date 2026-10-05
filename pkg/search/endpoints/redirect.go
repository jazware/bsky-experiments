package endpoints

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/bluesky-social/indigo/atproto/syntax"
	"github.com/labstack/echo/v4"
)

type RedirectRequest struct {
	Q string `query:"q"`
}

func (api *API) RedirectAtURI(c echo.Context) error {
	ctx := c.Request().Context()
	ctx, span := tracer.Start(ctx, "RedirectAtURI")
	defer span.End()

	var req RedirectRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	query := req.Q

	if strings.HasPrefix(query, "https://") {
		// Turn the bsky URL into an atURI
		u, err := url.Parse(query)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": fmt.Errorf("failed to parse url: %w", err).Error()})
		}

		// Profile: https://bsky.app/profile/{handle_or_did}
		// Post: https://bsky.app/profile/{handle_or_did}/post/{post_rkey}
		// Feed: https://bsky.app/profile/{handle_or_did}/feed/{feed_rkey}
		// List: https://bsky.app/profile/{handle_or_did}/lists/{list_rkey}
		// Starter Pack: https://bsky.app/starter-pack/{handle_or_did}/{starter_pack_rkey}

		// Ignore the host to support URLs from staging environments
		path := strings.TrimPrefix(u.Path, "/")
		parts := strings.Split(path, "/")

		if len(parts) < 2 {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": fmt.Errorf("unsupported url: %s", query).Error()})
		}

		var identString string
		var collection string
		var recordKey string
		switch parts[0] {
		case "profile":
			identString = parts[1]
			if len(parts) > 3 {
				switch parts[2] {
				case "post":
					collection = "app.bsky.feed.post"
				case "feed":
					collection = "app.bsky.feed.generator"
				case "lists":
					collection = "app.bsky.graph.list"
				default:
					return c.JSON(http.StatusBadRequest, map[string]string{"error": fmt.Errorf("unsupported url: %s", query).Error()})
				}
				recordKey = parts[3]
			}
		case "starter-pack":
			if len(parts) < 3 {
				return c.JSON(http.StatusBadRequest, map[string]string{"error": fmt.Errorf("unsupported url: %s", query).Error()})
			}
			identString = parts[1]
			collection = "app.bsky.graph.starterpack"
			recordKey = parts[2]
		default:
			return c.JSON(http.StatusBadRequest, map[string]string{"error": fmt.Errorf("unsupported url: %s", query).Error()})
		}

		identifier, err := syntax.ParseAtIdentifier(identString)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": fmt.Errorf("failed to parse identifier: %w", err).Error()})
		}

		ident, err := api.Directory.Lookup(ctx, identifier)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": fmt.Errorf("failed to lookup identity: %w", err).Error()})
		} else if ident == nil {
			return c.JSON(http.StatusNotFound, map[string]string{"error": fmt.Errorf("identity not found: %s", identifier.String()).Error()})
		}

		ret := ident.DID.String()
		if collection != "" && recordKey != "" {
			ret = fmt.Sprintf("at://%s/%s/%s", ident.DID.String(), collection, recordKey)
		}
		return c.String(http.StatusOK, ret)
	}

	atURI, err := syntax.ParseATURI(query)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": fmt.Errorf("failed to parse atUri: %w", err).Error()})
	}

	switch atURI.Collection() {
	case "":
		return c.Redirect(http.StatusFound, fmt.Sprintf("https://bsky.app/profile/%s", atURI.Authority().String()))
	case "app.bsky.feed.post":
		return c.Redirect(http.StatusFound,
			fmt.Sprintf(
				"https://bsky.app/profile/%s/post/%s",
				atURI.Authority().String(),
				atURI.RecordKey().String(),
			),
		)
	case "app.bsky.feed.generator":
		return c.Redirect(http.StatusFound,
			fmt.Sprintf(
				"https://bsky.app/profile/%s/feed/%s",
				atURI.Authority().String(),
				atURI.RecordKey().String(),
			),
		)
	case "app.bsky.graph.list":
		return c.Redirect(http.StatusFound,
			fmt.Sprintf(
				"https://bsky.app/profile/%s/lists/%s",
				atURI.Authority().String(),
				atURI.RecordKey().String(),
			),
		)
	case "app.bsky.graph.starterpack":
		return c.Redirect(http.StatusFound,
			fmt.Sprintf(
				"https://bsky.app/starter-pack/%s/%s",
				atURI.Authority().String(),
				atURI.RecordKey().String(),
			),
		)
	default:
		return c.JSON(http.StatusBadRequest, map[string]string{"error": fmt.Errorf("invalid atUri: %s", atURI.String()).Error()})
	}
}
