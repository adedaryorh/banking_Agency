package db

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PoolAdapter wraps pgxpool.Pool to match the service.DB interface
type PoolAdapter struct {
	pool *pgxpool.Pool
}

func NewAdapter(pool *pgxpool.Pool) *PoolAdapter {
	return &PoolAdapter{pool: pool}
}

func (a *PoolAdapter) Exec(ctx context.Context, sql string, arguments ...any) (interface{}, error) {
	return a.pool.Exec(ctx, sql, arguments...)
}

func (a *PoolAdapter) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return a.pool.Query(ctx, sql, args...)
}

func (a *PoolAdapter) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return a.pool.QueryRow(ctx, sql, args...)
}

func (a *PoolAdapter) Begin(ctx context.Context) (pgx.Tx, error) {
	return a.pool.Begin(ctx)
}
