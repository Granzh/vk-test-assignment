-- name: RecordPromoRequest :exec
-- Counts a press of the promo button. issued stays true once a code was given.
INSERT INTO promo_requests (user_id, campaign_day_id, issued)
VALUES ($1, $2, $3)
ON CONFLICT (user_id, campaign_day_id) DO UPDATE
SET requests          = promo_requests.requests + 1,
    issued            = promo_requests.issued OR EXCLUDED.issued,
    last_requested_at = now();

-- name: StatsUsers :one
SELECT count(*) AS total, count(*) FILTER (WHERE is_blocked) AS blocked FROM users;

-- name: StatsNewUsersByDay :many
-- New users per campaign day: local time (tz) shifted by the day boundary, as domain.Calendar does.
SELECT ((u.created_at AT TIME ZONE @tz::text) - make_interval(mins => @boundary_minutes::int))::date AS day,
       count(*) AS users
FROM users u
GROUP BY 1
ORDER BY 1;

-- name: StatsPromoRequests :one
-- requests: all presses; requesters: distinct (user, day) pairs; got_code / no_code split the pairs.
SELECT coalesce(sum(requests), 0)::bigint        AS requests,
       count(*)                                  AS requesters,
       count(*) FILTER (WHERE issued)            AS got_code,
       count(*) FILTER (WHERE NOT issued)        AS no_code
FROM promo_requests;

-- name: StatsIssuedPromoCodes :one
SELECT count(*) FROM promo_issues;

-- name: StatsDays :many
-- One row per assigned campaign day. Each table is aggregated once and joined,
-- which is 2-3 times faster than per-day subqueries on hundreds of thousands of rows.
SELECT cd.id, cd.day, p.name AS partner_name, cd.status,
       coalesce(e.entries, 0)::bigint  AS entries,
       coalesce(e.excluded, 0)::bigint AS excluded,
       coalesce(r.requesters, 0)::bigint AS promo_requesters,
       coalesce(i.issued, 0)::bigint   AS promo_issued
FROM campaign_days cd
JOIN partners p ON p.id = cd.partner_id
LEFT JOIN (
    SELECT de.campaign_day_id, count(*) AS entries, count(*) FILTER (WHERE de.excluded_at IS NOT NULL) AS excluded
    FROM daily_entries de GROUP BY de.campaign_day_id
) e ON e.campaign_day_id = cd.id
LEFT JOIN (
    SELECT pr.campaign_day_id, count(*) AS requesters FROM promo_requests pr GROUP BY pr.campaign_day_id
) r ON r.campaign_day_id = cd.id
LEFT JOIN (
    SELECT pi.campaign_day_id, count(*) AS issued FROM promo_issues pi GROUP BY pi.campaign_day_id
) i ON i.campaign_day_id = cd.id
ORDER BY cd.day;

-- name: StatsChanceDistribution :many
-- How many users have exactly n chances (n >= 1). Users without any are the rest.
SELECT t.chances::int AS chances, count(*) AS users
FROM (SELECT count(*) AS chances FROM activity_days GROUP BY user_id) t
GROUP BY t.chances
ORDER BY t.chances;
