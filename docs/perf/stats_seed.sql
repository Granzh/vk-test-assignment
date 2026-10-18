-- synthetic load: 200k users, 10 partners, 30 campaign days
INSERT INTO partners (name, chat_id) SELECT 'Partner ' || g, -1000 - g FROM generate_series(1, 10) g;
INSERT INTO campaign_days (day, partner_id, winners_count)
SELECT date '2026-09-01' + (g - 1), ((g - 1) % 10) + 1, 3 FROM generate_series(1, 30) g;

INSERT INTO users (telegram_id, username, first_name, created_at, last_seen_at, is_blocked)
SELECT 1000000 + g, 'u' || g, 'Name' || g,
       timestamptz '2026-08-28 00:00:00+03' + (random() * interval '34 days'),
       now(), random() < 0.03
FROM generate_series(1, 200000) g;

INSERT INTO promo_codes (partner_id, code, assigned_to_user_id, assigned_at)
SELECT p, 'C' || p || '-' || g, NULL, NULL FROM generate_series(1, 10) p, generate_series(1, 50000) g;

-- every user is active on 0..10 days (skewed to low numbers)
CREATE TEMP TABLE act AS
SELECT u.id AS user_id, d.id AS day_id
FROM users u
JOIN campaign_days d ON d.id <= floor(power(random(), 2.2) * 11)::int + (u.id % 1) ;

INSERT INTO activity_days (user_id, campaign_day_id) SELECT user_id, day_id FROM act;

INSERT INTO daily_entries (user_id, campaign_day_id, subscribed_checked_at)
SELECT user_id, day_id, now() FROM act WHERE random() < 0.6;

-- promo codes: assign and issue to ~70% of activity rows
WITH picks AS (
    SELECT user_id, day_id, row_number() OVER () AS rn FROM act WHERE random() < 0.7
), codes AS (
    SELECT id, row_number() OVER (ORDER BY id) AS rn FROM promo_codes
)
INSERT INTO promo_issues (user_id, campaign_day_id, promo_code_id)
SELECT p.user_id, p.day_id, c.id FROM picks p JOIN codes c ON c.rn = p.rn;

UPDATE promo_codes pc SET assigned_to_user_id = pi.user_id, assigned_at = now()
FROM promo_issues pi WHERE pi.promo_code_id = pc.id;

INSERT INTO promo_requests (user_id, campaign_day_id, requests, issued)
SELECT user_id, campaign_day_id, 1 + (random() * 2)::int, true FROM promo_issues;
INSERT INTO promo_requests (user_id, campaign_day_id, requests, issued)
SELECT user_id, day_id, 1, false FROM act a
WHERE random() < 0.1 AND NOT EXISTS (SELECT 1 FROM promo_requests r WHERE r.user_id = a.user_id AND r.campaign_day_id = a.day_id);

ANALYZE;
SELECT (SELECT count(*) FROM users) users, (SELECT count(*) FROM activity_days) activity,
       (SELECT count(*) FROM daily_entries) entries, (SELECT count(*) FROM promo_issues) issues,
       (SELECT count(*) FROM promo_requests) requests, (SELECT count(*) FROM promo_codes) codes;
