package endpoints

import (
	"log/slog"
	"time"

	"github.com/bluesky-social/indigo/atproto/identity"
	"github.com/jazware/bsky-experiments/pkg/indexer/store"
	"github.com/jazware/bsky-experiments/pkg/search"
	"github.com/jazware/bsky-experiments/pkg/search/appview"
	"github.com/jazware/bsky-experiments/pkg/search/postcard"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	"golang.org/x/time/rate"
)

type API struct {
	Logger          *slog.Logger
	SearchService   *search.SearchService
	Store           *store.Store
	Directory       identity.Directory
	CheckoutLimiter *rate.Limiter
	MagicHeaderVal  string

	// Link-proxy embed dependencies
	Appview   *appview.Client
	Renderer  *postcard.Renderer // nil disables card rendering
	Redis     *redis.Client
	PublicURL string // externally-visible base URL; derived from the request when empty
}

var tracer = otel.Tracer("search-api")

func NewAPI(
	logger *slog.Logger,
	searchService *search.SearchService,
	store *store.Store,
	magicHeaderVal string,
	appviewClient *appview.Client,
	renderer *postcard.Renderer,
	redisClient *redis.Client,
	publicURL string,
) (*API, error) {
	dir := identity.DefaultDirectory()

	return &API{
		Logger:          logger.With("component", "api"),
		SearchService:   searchService,
		Store:           store,
		Directory:       dir,
		MagicHeaderVal:  magicHeaderVal,
		CheckoutLimiter: rate.NewLimiter(rate.Every(2*time.Second), 1),
		Appview:         appviewClient,
		Renderer:        renderer,
		Redis:           redisClient,
		PublicURL:       publicURL,
	}, nil
}
