\timing off
\echo === users
EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF, SUMMARY ON) SELECT count(*) AS total, count(*) FILTER (WHERE is_blocked) AS blocked FROM users;
\echo === new users by day
EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF, SUMMARY ON)
SELECT ((u.created_at AT TIME ZONE 'Europe/Moscow') - make_interval(mins => 0))::date AS day, count(*) FROM users u GROUP BY 1 ORDER BY 1;
\echo === promo requests
EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF, SUMMARY ON)
SELECT coalesce(sum(requests), 0)::bigint, count(*), count(*) FILTER (WHERE issued), count(*) FILTER (WHERE NOT issued) FROM promo_requests;
\echo === issued
EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF, SUMMARY ON) SELECT count(*) FROM promo_issues;
\echo === days
EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF, SUMMARY ON)
SELECT cd.id, cd.day, p.name, cd.status,
 (SELECT count(*) FROM daily_entries de WHERE de.campaign_day_id = cd.id),
 (SELECT count(*) FROM daily_entries de2 WHERE de2.campaign_day_id = cd.id AND de2.excluded_at IS NOT NULL),
 (SELECT count(*) FROM promo_requests pr WHERE pr.campaign_day_id = cd.id),
 (SELECT count(*) FROM promo_issues pi WHERE pi.campaign_day_id = cd.id)
FROM campaign_days cd JOIN partners p ON p.id = cd.partner_id ORDER BY cd.day;
\echo === chance distribution
EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF, SUMMARY ON)
SELECT t.chances::int, count(*) FROM (SELECT count(*) AS chances FROM activity_days GROUP BY user_id) t GROUP BY t.chances ORDER BY t.chances;
\echo === promo stats (stock per partner)
EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF, SUMMARY ON)
SELECT p.id, p.name, count(pc.id), count(pc.id) FILTER (WHERE pc.assigned_to_user_id IS NULL)
FROM partners p LEFT JOIN promo_codes pc ON pc.partner_id = p.id GROUP BY p.id, p.name ORDER BY p.id;
