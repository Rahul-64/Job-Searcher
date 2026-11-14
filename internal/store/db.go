// Package store owns all database access (connection pool, migrations, queries).
package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"jobscraper/migrations"
)

// Open creates a pgx pool (5 connections) and verifies connectivity.
func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	return OpenWithMaxConns(ctx, url, 5)
}

// OpenWithMaxConns is Open with an explicit pool size. Serverless instances
// should keep it small: many instances share one Neon connection budget.
func OpenWithMaxConns(ctx context.Context, url string, maxConns int32) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	// Neon scales to zero; keep the pool small and tolerate a slow first connect.
	cfg.MaxConns = maxConns
	cfg.ConnConfig.ConnectTimeout = 15 * time.Second
	// Neon's pooled endpoint sits behind pgbouncer; unnamed prepared statements
	// (describe mode) work there, named cached ones can break.
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeCacheDescribe

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

// openSQL opens a database/sql handle (goose needs one) on top of pgx.
func openSQL(url string) (*sql.DB, error) {
	cfg, err := stdlibConfig(url)
	if err != nil {
		return nil, err
	}
	return stdlib.OpenDB(*cfg), nil
}

func setupGoose() error {
	goose.SetBaseFS(migrations.FS)
	return goose.SetDialect("postgres")
}

// MigrateUp applies all pending migrations.
func MigrateUp(ctx context.Context, url string) error {
	if err := setupGoose(); err != nil {
		return err
	}
	db, err := openSQL(url)
	if err != nil {
		return err
	}
	defer db.Close()
	return goose.UpContext(ctx, db, ".")
}

// MigrateDown rolls back the most recent migration.
func MigrateDown(ctx context.Context, url string) error {
	if err := setupGoose(); err != nil {
		return err
	}
	db, err := openSQL(url)
	if err != nil {
		return err
	}
	defer db.Close()
	return goose.DownContext(ctx, db, ".")
}

// MigrateStatus prints the migration status to stdout.
func MigrateStatus(ctx context.Context, url string) error {
	if err := setupGoose(); err != nil {
		return err
	}
	db, err := openSQL(url)
	if err != nil {
		return err
	}
	defer db.Close()
	return goose.StatusContext(ctx, db, ".")
}
