package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"chat_bot/internal/storage/db"
)

// Store gives services access to queries, plain or inside a transaction.
type Store struct {
	pool *pgxpool.Pool
	*db.Queries
}

// NewStore wraps a connection pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, Queries: db.New(pool)}
}

// InTx runs fn in a transaction: it commits if fn returns nil and rolls back
// otherwise (including on panic).
func (s *Store) InTx(ctx context.Context, fn func(q *db.Queries) error) (err error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
			panic(p)
		}
	}()

	if err := fn(s.Queries.WithTx(tx)); err != nil {
		if rbErr := tx.Rollback(context.WithoutCancel(ctx)); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			return fmt.Errorf("%w (rollback: %v)", err, rbErr)
		}
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// UpsertUser registers or refreshes a user and returns their row. It shadows
// the generated query, whose CTE form gives sqlc a row type of its own, so
// that callers keep working with db.User.
func (s *Store) UpsertUser(ctx context.Context, arg db.UpsertUserParams) (db.User, error) {
	r, err := s.Queries.UpsertUser(ctx, arg)
	if errors.Is(err, pgx.ErrNoRows) {
		// Two first messages of a new user raced: the other transaction inserted the
		// row after our statement took its snapshot, so the statement could neither
		// update it (nothing changed) nor see it. A new statement does.
		return s.Queries.GetUserByTelegramID(ctx, arg.TelegramID)
	}
	if err != nil {
		return db.User{}, err
	}
	return db.User(r), nil
}
