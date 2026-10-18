-- +goose Up
CREATE TABLE daily_winners (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    campaign_day_id BIGINT      NOT NULL REFERENCES campaign_days (id),
    user_id         BIGINT      NOT NULL REFERENCES users (id),
    prize           TEXT        NOT NULL DEFAULT '',
    is_reserve      BOOLEAN     NOT NULL DEFAULT false,
    status          TEXT        NOT NULL DEFAULT 'selected'
                    CHECK (status IN ('selected', 'notified', 'claimed', 'forfeited')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (campaign_day_id, user_id)
);

CREATE INDEX daily_winners_user_idx ON daily_winners (user_id);

CREATE TABLE final_draw (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    held_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    status       TEXT        NOT NULL DEFAULT 'drawn'
                 CHECK (status IN ('drawn', 'confirmed', 'notified')),
    confirmed_at TIMESTAMPTZ
);

CREATE TABLE final_winners (
    id               BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    final_draw_id    BIGINT      NOT NULL REFERENCES final_draw (id),
    user_id          BIGINT      NOT NULL REFERENCES users (id),
    tickets_count    INT         NOT NULL CHECK (tickets_count > 0),
    is_reserve       BOOLEAN     NOT NULL DEFAULT false,
    status           TEXT        NOT NULL DEFAULT 'selected'
                     CHECK (status IN ('selected', 'notified', 'contact_received', 'forfeited')),
    deadline         TIMESTAMPTZ,
    contact_name     TEXT,
    consent_given_at TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (final_draw_id, user_id),
    CHECK (contact_name IS NULL OR consent_given_at IS NOT NULL)
);

-- Finds winners whose response deadline has passed.
CREATE INDEX final_winners_status_deadline_idx ON final_winners (status, deadline);
CREATE INDEX final_winners_user_idx ON final_winners (user_id);

-- +goose Down
DROP TABLE final_winners;
DROP TABLE final_draw;
DROP TABLE daily_winners;
