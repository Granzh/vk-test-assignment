-- +goose Up
CREATE TABLE broadcasts (
    id                     BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    text                   TEXT        NOT NULL CHECK (text <> ''),
    status                 TEXT        NOT NULL DEFAULT 'draft'
                           CHECK (status IN ('draft', 'sending', 'done', 'cancelled')),
    created_by_telegram_id BIGINT      NOT NULL,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at             TIMESTAMPTZ,
    finished_at            TIMESTAMPTZ,
    sent_count             INT         NOT NULL DEFAULT 0 CHECK (sent_count >= 0),
    failed_count           INT         NOT NULL DEFAULT 0 CHECK (failed_count >= 0)
);

CREATE INDEX broadcasts_status_idx ON broadcasts (status);

CREATE TABLE audit_log (
    id                BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    admin_telegram_id BIGINT      NOT NULL,
    action            TEXT        NOT NULL CHECK (action <> ''),
    entity_type       TEXT        NOT NULL,
    entity_id         BIGINT,
    details           JSONB       NOT NULL DEFAULT '{}',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX audit_log_entity_idx ON audit_log (entity_type, entity_id);
CREATE INDEX audit_log_created_at_idx ON audit_log (created_at);

-- +goose Down
DROP TABLE audit_log;
DROP TABLE broadcasts;
