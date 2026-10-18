// Package storagetest provides isolated PostgreSQL databases for integration tests.
package storagetest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

const envURL = "TEST_DATABASE_URL"

// New creates a fresh database with all migrations applied and returns a pool
// connected to it. The test is skipped when TEST_DATABASE_URL is not set; the
// URL must point to a server where the user may CREATE DATABASE.
// The database is dropped when the test finishes.
func New(t testing.TB) *pgxpool.Pool {
	t.Helper()
	adminURL := os.Getenv(envURL)
	if adminURL == "" {
		t.Skipf("%s is not set; skipping integration test", envURL)
	}
	ctx := context.Background()

	admin, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatalf("connect to %s: %v", envURL, err)
	}

	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	name := "test_" + hex.EncodeToString(suffix)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		admin.Close()
		t.Fatalf("create database: %v", err)
	}

	u, err := url.Parse(adminURL)
	if err != nil {
		admin.Close()
		t.Fatalf("parse %s: %v", envURL, err)
	}
	u.Path = "/" + name

	pool, err := pgxpool.New(ctx, u.String())
	if err != nil {
		admin.Close()
		t.Fatalf("connect to test database: %v", err)
	}

	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		admin.Close()
	})

	migrate(t, pool)
	return pool
}

// migrate applies the "+goose Up" section of every migration file in order.
// It understands only plain statements, which is all the migrations use.
func migrate(t testing.TB, pool *pgxpool.Pool) {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "migrations")

	files, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("find migrations in %s: %v", dir, err)
	}
	sort.Strings(files)

	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		up := string(raw)
		if i := strings.Index(up, "-- +goose Down"); i >= 0 {
			up = up[:i]
		}
		up = strings.Replace(up, "-- +goose Up", "", 1)
		if _, err := pool.Exec(context.Background(), up); err != nil {
			t.Fatalf("apply %s: %v", filepath.Base(f), err)
		}
	}
}
