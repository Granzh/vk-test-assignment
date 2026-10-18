-- +goose Up
-- A participant dropped from the daily draw (e.g. unsubscribed before the draw).
-- The row stays, so the full participant list is preserved.
ALTER TABLE daily_entries
    ADD COLUMN excluded_at     TIMESTAMPTZ,
    ADD COLUMN excluded_reason TEXT CHECK (excluded_reason IN ('unsubscribed')),
    ADD CONSTRAINT daily_entries_excluded_chk CHECK ((excluded_at IS NULL) = (excluded_reason IS NULL));

-- +goose Down
ALTER TABLE daily_entries
    DROP COLUMN excluded_reason,
    DROP COLUMN excluded_at;
