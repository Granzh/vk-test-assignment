-- +goose Up
-- Promo code requests (spec 8: "number of promo code requests"). promo_issues only
-- knows about requests that got a code, so requests that found the pool empty
-- were invisible. One row per (user, campaign day) counts every press of the button.
CREATE TABLE promo_requests (
    id                BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id           BIGINT      NOT NULL REFERENCES users (id),
    campaign_day_id   BIGINT      NOT NULL REFERENCES campaign_days (id),
    requests          INT         NOT NULL DEFAULT 1 CHECK (requests > 0),
    issued            BOOLEAN     NOT NULL,
    first_requested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_requested_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, campaign_day_id)
);

-- Per-day counts of the stats screen.
CREATE INDEX promo_requests_day_idx ON promo_requests (campaign_day_id);

-- Requests made before this table existed are known only when they got a code.
INSERT INTO promo_requests (user_id, campaign_day_id, requests, issued, first_requested_at, last_requested_at)
SELECT user_id, campaign_day_id, 1, true, issued_at, issued_at FROM promo_issues;

-- +goose Down
DROP TABLE promo_requests;
