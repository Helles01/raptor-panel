-- Read-only questions to ask the database that came over from the old panel, before anything is converted or started (docs/cutover.md, step 5).
-- Every one only reads. They are the list of plan 13.5 as SQL, until the raptor audit command is built; run them with the mysql client against the NEW database:
--     mysql -u raptor -p raptor < docs/cutover-review.sql | less
-- Nothing here shows a secret: the settings whose names look like a secret are not shown, and the install script of an egg is shown as its hash and length, to be
-- compared with the egg files of the reference version the old panel was made from.

-- 1 administrators and accounts that changed lately
SELECT id, username, email, root_admin, use_totp, created_at, updated_at FROM users WHERE root_admin = 1 ORDER BY id;
SELECT id, username, email, created_at, updated_at FROM users WHERE updated_at > NOW() - INTERVAL 90 DAY ORDER BY updated_at DESC LIMIT 50;
-- 2 API keys, ssh keys, subusers
SELECT k.id, k.user_id, u.username, k.key_type, k.identifier, k.memo, k.allowed_ips, k.last_used_at, k.created_at FROM api_keys k JOIN users u ON u.id = k.user_id ORDER BY k.created_at DESC;
SELECT id, user_id, name, fingerprint, created_at FROM user_ssh_keys WHERE deleted_at IS NULL ORDER BY created_at DESC;
SELECT s.id, s.server_id, s.user_id, u.username, s.permissions FROM subusers s JOIN users u ON u.id = s.user_id ORDER BY s.server_id;
-- 3 eggs: what runs on a node
SELECT id, name, author, updated_at, LEFT(startup, 120) AS startup, config_from, copy_script_from FROM eggs ORDER BY updated_at DESC;
SELECT id, name, script_container, script_entry, LENGTH(script_install) AS install_script_bytes, SHA2(script_install, 256) AS install_script_sha256 FROM eggs ORDER BY id;
-- 4 schedules and their tasks
SELECT s.id, s.server_id, s.name, s.is_active, s.cron_minute, s.cron_hour, s.cron_day_of_month, s.cron_month, s.cron_day_of_week FROM schedules s ORDER BY s.id;
SELECT t.id, t.schedule_id, t.sequence_id, t.action, LEFT(t.payload, 200) AS payload FROM tasks t ORDER BY t.schedule_id, t.sequence_id;
-- 5 mounts, nodes, database hosts, settings
SELECT id, name, source, target, read_only, user_mountable FROM mounts ORDER BY id;
SELECT id, name, fqdn, scheme, behind_proxy, public, maintenance_mode, updated_at FROM nodes ORDER BY id;
SELECT id, name, host, port, username, node_id FROM database_hosts ORDER BY id;
SELECT `key`, CASE WHEN `key` LIKE '%password%' OR `key` LIKE '%secret%' OR `key` LIKE '%key%' THEN '(not shown)' ELSE LEFT(`value`, 120) END AS value FROM settings ORDER BY `key`;
-- 6 activity that is worth a look
SELECT event, COUNT(*) AS n, MAX(`timestamp`) AS last_at FROM activity_logs WHERE event LIKE 'auth:%' OR event LIKE 'user:%' OR event LIKE 'server:backup.download' GROUP BY event ORDER BY last_at DESC;
SELECT id, event, ip, actor_id, `timestamp` FROM activity_logs WHERE event IN ('auth:success', 'auth:fail', 'auth:checkpoint') ORDER BY id DESC LIMIT 50;
