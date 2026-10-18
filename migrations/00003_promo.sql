-- +goose Up
CREATE TABLE promo_codes (
    id                  BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    partner_id          BIGINT NOT NULL REFERENCES partners (id),
    code                TEXT   NOT NULL CHECK (code <> ''),
    assigned_to_user_id BIGINT REFERENCES users (id),
    assigned_at         TIMESTAMPTZ,
    UNIQUE (partner_id, code),
    CHECK ((assigned_to_user_id IS NULL) = (assigned_at IS NULL))
);

-- Free codes per partner: used when issuing and when counting the remainder.
CREATE INDEX promo_codes_free_idx ON promo_codes (partner_id) WHERE assigned_to_user_id IS NULL;
CREATE INDEX promo_codes_user_idx ON promo_codes (assigned_to_user_id) WHERE assigned_to_user_id IS NOT NULL;

CREATE TABLE promo_issues (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id         BIGINT      NOT NULL REFERENCES users (id),
    campaign_day_id BIGINT      NOT NULL REFERENCES campaign_days (id),
    promo_code_id   BIGINT      NOT NULL UNIQUE REFERENCES promo_codes (id),
    issued_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, campaign_day_id)
);

CREATE INDEX promo_issues_campaign_day_idx ON promo_issues (campaign_day_id);

-- +goose Down
DROP TABLE promo_issues;
DROP TABLE promo_codes;
