-- +goose Up
CREATE TABLE daily_entries (
    id                    BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id               BIGINT      NOT NULL REFERENCES users (id),
    campaign_day_id       BIGINT      NOT NULL REFERENCES campaign_days (id),
    subscribed_checked_at TIMESTAMPTZ NOT NULL,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, campaign_day_id)
);

CREATE INDEX daily_entries_campaign_day_idx ON daily_entries (campaign_day_id);

-- One active day = one chance. The MAX_CHANCES cap is enforced by the service.
CREATE TABLE activity_days (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id         BIGINT      NOT NULL REFERENCES users (id),
    campaign_day_id BIGINT      NOT NULL REFERENCES campaign_days (id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, campaign_day_id)
);

CREATE INDEX activity_days_campaign_day_idx ON activity_days (campaign_day_id);

-- +goose Down
DROP TABLE activity_days;
DROP TABLE daily_entries;
