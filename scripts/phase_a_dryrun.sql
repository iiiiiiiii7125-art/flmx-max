-- R5-B4 阶段 A: 只读 dry-run（7 类清单）
-- 用法：sqlite3 gost.db < phase_a_dryrun.sql
-- 或 PG: psql -d <db> -f phase_a_dryrun.sql
-- 只读 SELECT，无 DDL，无 DELETE。

.headers on
.mode column

.print "=== 1. forward_port (node_id, port) 重复组 ==="
SELECT node_id, port, COUNT(*) AS cnt,
       GROUP_CONCAT(forward_id) AS forward_ids
FROM forward_port
GROUP BY node_id, port
HAVING COUNT(*) > 1
ORDER BY node_id, port;

.print "=== 2. forward_port 孤儿（forward_id 不在 forward 表） ==="
SELECT fp.node_id, fp.port, fp.forward_id
FROM forward_port fp
LEFT JOIN "forward" f ON f.id = fp.forward_id
WHERE f.id IS NULL
ORDER BY fp.node_id, fp.port;

.print "=== 3. chain_tunnel (node_id, port) 重复组 ==="
SELECT node_id, port, COUNT(*) AS cnt,
       GROUP_CONCAT(tunnel_id) AS tunnel_ids
FROM chain_tunnel
WHERE port IS NOT NULL AND port > 0
GROUP BY node_id, port
HAVING COUNT(*) > 1
ORDER BY node_id, port;

.print "=== 4. node_inbound (node_id, port) 重复组 ==="
SELECT node_id, port, COUNT(*) AS cnt,
       GROUP_CONCAT(id) AS inbound_ids
FROM node_inbound
GROUP BY node_id, port
HAVING COUNT(*) > 1
ORDER BY node_id, port;

.print "=== 5. 跨表冲突（同 node_id+port 出现在 2 张以上表） ==="
WITH all_ports AS (
  SELECT node_id, port, 'forward_port' AS tbl FROM forward_port
  UNION ALL
  SELECT node_id, CAST(port AS INTEGER), 'chain_tunnel' FROM chain_tunnel WHERE port IS NOT NULL AND port > 0
  UNION ALL
  SELECT node_id, port, 'node_inbound' AS tbl FROM node_inbound
),
grouped AS (
  SELECT node_id, port, COUNT(DISTINCT tbl) AS tbl_cnt,
         GROUP_CONCAT(DISTINCT tbl) AS tables
  FROM all_ports
  GROUP BY node_id, port
)
SELECT node_id, port, tbl_cnt, tables
FROM grouped
WHERE tbl_cnt > 1
ORDER BY node_id, port;

.print "=== 6. 用户名重复 ==="
SELECT username, COUNT(*) AS cnt,
       GROUP_CONCAT(id) AS user_ids
FROM "user"
GROUP BY username
HAVING COUNT(*) > 1
ORDER BY cnt DESC;

.print "=== 7. 默认订阅重复（同 user_id 多条 is_default=1） ==="
SELECT user_id, COUNT(*) AS cnt,
       GROUP_CONCAT(id) AS sub_ids
FROM subscription
WHERE is_default = 1
GROUP BY user_id
HAVING COUNT(*) > 1
ORDER BY user_id;

.print "=== 汇总计数 ==="
SELECT 'forward_port_dup_groups' AS item, COUNT(*) AS n FROM (
  SELECT 1 FROM forward_port GROUP BY node_id, port HAVING COUNT(*) > 1
);
SELECT 'forward_port_orphans' AS item, COUNT(*) AS n FROM forward_port fp
  LEFT JOIN "forward" f ON f.id = fp.forward_id WHERE f.id IS NULL;
SELECT 'chain_tunnel_dup_groups' AS item, COUNT(*) AS n FROM (
  SELECT 1 FROM chain_tunnel WHERE port IS NOT NULL AND port > 0 GROUP BY node_id, port HAVING COUNT(*) > 1
);
SELECT 'node_inbound_dup_groups' AS item, COUNT(*) AS n FROM (
  SELECT 1 FROM node_inbound GROUP BY node_id, port HAVING COUNT(*) > 1
);
SELECT 'username_dup_groups' AS item, COUNT(*) AS n FROM (
  SELECT 1 FROM "user" GROUP BY username HAVING COUNT(*) > 1
);
SELECT 'default_sub_dup_groups' AS item, COUNT(*) AS n FROM (
  SELECT 1 FROM subscription WHERE is_default = 1 GROUP BY user_id HAVING COUNT(*) > 1
);
