-- +goose Up
CREATE TABLE partners (
    id                BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name              TEXT        NOT NULL CHECK (name <> ''),
    url               TEXT        NOT NULL DEFAULT '',
    chat_id           BIGINT,
    promo_text        TEXT        NOT NULL DEFAULT '',
    promo_terms       TEXT        NOT NULL DEFAULT '',
    promo_valid_until TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- day is the local campaign calendar date (TIMEZONE + DAY_BOUNDARY), not an instant.
CREATE TABLE campaign_days (
    id                 BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    day                DATE        NOT NULL UNIQUE,
    partner_id         BIGINT      NOT NULL REFERENCES partners (id),
    winners_count      INT         NOT NULL CHECK (winners_count > 0),
    prizes_description TEXT        NOT NULL DEFAULT '',
    status             TEXT        NOT NULL DEFAULT 'pending'
                       CHECK (status IN ('pending', 'drawn', 'confirmed', 'notified')),
    drawn_at           TIMESTAMPTZ,
    confirmed_at       TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX campaign_days_partner_id_idx ON campaign_days (partner_id);
CREATE INDEX campaign_days_status_idx ON campaign_days (status);

-- +goose Down
DROP TABLE campaign_days;
DROP TABLE partners;
