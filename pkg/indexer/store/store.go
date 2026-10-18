package store

import (
	"context"
	"fmt"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

type Store struct {
	DB driver.Conn
}

// NewStore creates a new ClickHouse store
// NOTE: Schema migrations must be run separately via `just migrate-up`
func NewStore(dsn, username, password string) (*Store, error) {
	opts := &clickhouse.Options{
		Addr: []string{dsn},
	}

	if username != "" {
		opts.Auth = clickhouse.Auth{
			Username: username,
			Password: password,
		}
	}

	conn, err := clickhouse.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to open clickhouse connection: %w", err)
	}

	if err := conn.Ping(context.Background()); err != nil {
		return nil, fmt.Errorf("failed to ping clickhouse: %w", err)
	}

	return &Store{DB: conn}, nil
}

func (s *Store) Close() error {
	return s.DB.Close()
}
