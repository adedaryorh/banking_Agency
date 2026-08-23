package service

import (
	"context"

	"github.com/jackc/pgx/v5"
)

type DB interface {
	Exec(ctx context.Context, sql string, arguments ...any) (interface{}, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Begin(ctx context.Context) (pgx.Tx, error)
}
