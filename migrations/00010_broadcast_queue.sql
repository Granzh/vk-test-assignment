-- +goose Up
-- Broadcast queue (spec 7). A broadcast is a draft until an admin confirms it;
-- confirming materializes its recipients, and the worker sends to them in
-- order. Totals are counted from broadcast_recipients, never stored.
ALTER TABLE broadcasts
    DROP COLUMN sent_count,
    DROP COLUMN failed_count,
    ADD COLUMN segment         TEXT NOT NULL DEFAULT 'all' CHECK (segment IN ('all', 'no_promo', 'no_entry')),
    ADD COLUMN campaign_day_id BIGINT REFERENCES campaign_days (id);

CREATE TABLE broadcast_recipients (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    broadcast_id BIGINT      NOT NULL REFERENCES broadcasts (id),
    user_id      BIGINT      NOT NULL REFERENCES users (id),
    telegram_id  BIGINT      NOT NULL,
    status       TEXT        NOT NULL DEFAULT 'pending'
                 CHECK (status IN ('pending', 'sending', 'sent', 'failed', 'blocked')),
    attempts     INT         NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error   TEXT,
    claimed_at   TIMESTAMPTZ,
    sent_at      TIMESTAMPTZ,
    UNIQUE (broadcast_id, user_id)
);

-- The worker takes pending rows of one broadcast in id order.
CREATE INDEX broadcast_recipients_queue_idx ON broadcast_recipients (broadcast_id, status, id);

-- +goose Down
DROP TABLE broadcast_recipients;
ALTER TABLE broadcasts
    DROP COLUMN campaign_day_id,
    DROP COLUMN segment,
    ADD COLUMN sent_count   INT NOT NULL DEFAULT 0 CHECK (sent_count >= 0),
    ADD COLUMN failed_count INT NOT NULL DEFAULT 0 CHECK (failed_count >= 0);
