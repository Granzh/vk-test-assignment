-- +goose Up
CREATE TABLE users (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    telegram_id  BIGINT      NOT NULL UNIQUE,
    username     TEXT,
    first_name   TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    is_blocked   BOOLEAN     NOT NULL DEFAULT false
);

CREATE INDEX users_created_at_idx ON users (created_at);

-- +goose Down
DROP TABLE users;
