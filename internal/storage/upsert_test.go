package storage_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"chat_bot/internal/storage"
	"chat_bot/internal/storage/db"
	"chat_bot/internal/storage/storagetest"
)

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: true} }

func TestUpsertUserWritesOnlyWhenSomethingChanged(t *testing.T) {
	pool := storagetest.New(t)
	store := storage.NewStore(pool)
	ctx := context.Background()
	arg := db.UpsertUserParams{TelegramID: 42, Username: text("ann"), FirstName: text("Anna")}

	first, err := store.UpsertUser(ctx, arg)
	if err != nil || first.ID == 0 || first.Username.String != "ann" {
		t.Fatalf("first = %+v, %v", first, err)
	}

	// the same user again right away: the row is returned and not rewritten
	for i := 0; i < 3; i++ {
		again, err := store.UpsertUser(ctx, arg)
		if err != nil {
			t.Fatal(err)
		}
		if again.ID != first.ID || !again.LastSeenAt.Time.Equal(first.LastSeenAt.Time) {
			t.Fatalf("a repeat within a minute rewrote the row: first %v, again %v", first.LastSeenAt.Time, again.LastSeenAt.Time)
		}
	}
	var xmin1, xmin2 string
	if err := pool.QueryRow(ctx, `SELECT xmin::text FROM users WHERE id = $1`, first.ID).Scan(&xmin1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertUser(ctx, arg); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT xmin::text FROM users WHERE id = $1`, first.ID).Scan(&xmin2); err != nil {
		t.Fatal(err)
	}
	if xmin1 != xmin2 {
		t.Error("a new row version was written although nothing changed")
	}

	// a changed name is saved
	renamed, err := store.UpsertUser(ctx, db.UpsertUserParams{TelegramID: 42, Username: text("ann_new"), FirstName: text("Anna")})
	if err != nil || renamed.Username.String != "ann_new" || renamed.ID != first.ID {
		t.Errorf("renamed = %+v, %v", renamed, err)
	}

	// an old last_seen_at is refreshed
	if _, err := pool.Exec(ctx, `UPDATE users SET last_seen_at = now() - interval '5 minutes' WHERE id = $1`, first.ID); err != nil {
		t.Fatal(err)
	}
	stale, _ := store.GetUserByTelegramID(ctx, 42)
	fresh, err := store.UpsertUser(ctx, db.UpsertUserParams{TelegramID: 42, Username: text("ann_new"), FirstName: text("Anna")})
	if err != nil || !fresh.LastSeenAt.Time.After(stale.LastSeenAt.Time.Add(4*time.Minute)) {
		t.Errorf("last_seen_at was not refreshed: %v -> %v (%v)", stale.LastSeenAt.Time, fresh.LastSeenAt.Time, err)
	}

	// a user who comes back after blocking the bot is unblocked at once
	if err := store.SetUserBlocked(ctx, db.SetUserBlockedParams{TelegramID: 42, IsBlocked: true}); err != nil {
		t.Fatal(err)
	}
	back, err := store.UpsertUser(ctx, db.UpsertUserParams{TelegramID: 42, Username: text("ann_new"), FirstName: text("Anna")})
	if err != nil || back.IsBlocked {
		t.Errorf("back = %+v, %v; writing to the bot must clear is_blocked", back, err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n); err != nil || n != 1 {
		t.Errorf("users = %d, %v", n, err)
	}
}

func TestUpsertUserConcurrentFirstContact(t *testing.T) {
	pool := storagetest.New(t)
	store := storage.NewStore(pool)
	ctx := context.Background()
	const n = 20
	ids := make(chan int64, n)
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			u, err := store.UpsertUser(ctx, db.UpsertUserParams{TelegramID: 77, Username: text("bob")})
			ids <- u.ID
			errs <- err
		}()
	}
	var first int64
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		id := <-ids
		if first == 0 {
			first = id
		}
		if id == 0 || id != first {
			t.Fatalf("got user id %d, want %d for every concurrent first contact", id, first)
		}
	}
}
