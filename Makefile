GOOSE = go run github.com/pressly/goose/v3/cmd/goose@v3.24.1
SQLC  = go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.27.0

.PHONY: build test test-db test-perf load-test lint vet staticcheck migrate-up migrate-down sqlc run docker-build

build:
	go build -o bin/bot ./cmd/bot

test:
	go test ./...

# Integration tests against the compose Postgres (host port POSTGRES_PORT, default 5432).
test-db:
	TEST_DATABASE_URL="postgres://bot:bot@localhost:$${POSTGRES_PORT:-5432}/postgres?sslmode=disable" go test -race -count=1 ./...

# Requires DATABASE_URL in the environment.
migrate-up:
	$(GOOSE) -dir migrations postgres "$(DATABASE_URL)" up

migrate-down:
	$(GOOSE) -dir migrations postgres "$(DATABASE_URL)" down

sqlc:
	$(SQLC) generate

run:
	go run ./cmd/bot

STATICCHECK = go run honnef.co/go/tools/cmd/staticcheck@2025.1.1

vet:
	go vet ./...

staticcheck:
	$(STATICCHECK) ./...

lint: vet staticcheck

# Production images (docs/deploy.md).
docker-build:
	docker compose -f docker-compose.prod.yml build

# 1000 users press the buttons at once; prints latencies, errors and pool usage (docs/load-test.md).
# Tune with LOAD_USERS, LOAD_POOL, LOAD_CHECK_RATE, LOAD_LATENCY_MS, LOAD_CONCURRENCY.
load-test:
	LOAD_TEST=1 TEST_DATABASE_URL="postgres://bot:bot@localhost:$${POSTGRES_PORT:-5432}/postgres?sslmode=disable" go test -count=1 -v -run TestLoad ./internal/loadtest

# Statistics on 100 000 users (slow: ~1 min). Needs the compose Postgres.
test-perf:
	STATS_PERF=1 TEST_DATABASE_URL="postgres://bot:bot@localhost:$${POSTGRES_PORT:-5432}/postgres?sslmode=disable" go test -count=1 -run TestStatsReportOnALargeDatabase -v ./internal/service
