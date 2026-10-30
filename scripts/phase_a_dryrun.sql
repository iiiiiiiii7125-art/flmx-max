-- R5-B4 阶段 A: 只读 dry-run（7 类清单）
-- 用法：sqlite3 gost.db < phase_a_dryrun.sql
-- F-122: 本脚本是 sqlite3 CLI 专用（.headers/.mode/.print 点命令与
-- GROUP_CONCAT 均为 SQLite 方言）。此前头部宣称可用于 PG 属误导：
-- psql 既不认点命令也没有 GROUP_CONCAT。PG 环境请手工适配后再跑
-- （去掉点命令、GROUP_CONCAT 改 string_agg、.print 改注释）。
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

.print "=== 4. node_inbound (node_id, port) 重复组（仅 kernel_id > 0：B4/F-003 后索引为部分唯一索引，导入行同端口合法） ==="
SELECT node_id, port, COUNT(*) AS cnt,
       GROUP_CONCAT(id) AS inbound_ids
FROM node_inbound
WHERE kernel_id > 0
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

.print "=== 6. 用户名重复（列名为 user） ==="
SELECT "user", COUNT(*) AS cnt,
       GROUP_CONCAT(id) AS user_ids
FROM "user"
GROUP BY "user"
HAVING COUNT(*) > 1
ORDER BY cnt DESC;

.print "=== 6b. 用户名大小写变体重复（B02-003 口径：按 LOWER(user) 分组） ==="
-- 判重不区分大小写后，仅大小写不同的同名用户（Admin 与 admin）视为
-- 重复：v5 迁移（k10_v5_username_casefold_unique）会因这些组跳过折叠
-- 唯一索引并在启动日志逐组点名。本节即迁移预检的同形查询，清理前
-- 先在此定位、人工决定每组保留哪一个，系统不自动合并/改名。
-- PostgreSQL 等价写法：GROUP_CONCAT 换成 string_agg(id::text, ',') /
-- string_agg("user", ',')，其余同形。
SELECT LOWER("user") AS folded, COUNT(*) AS cnt,
       GROUP_CONCAT(id) AS user_ids,
       GROUP_CONCAT("user") AS stored_forms
FROM "user"
GROUP BY LOWER("user")
HAVING COUNT(*) > 1
ORDER BY cnt DESC;

.print "=== 7. subscription 概览（DefaultSubscription 取 inx 最小第一条，无 is_default 列） ==="
SELECT COUNT(*) AS total_subs FROM subscription;
SELECT id, name, inx FROM subscription ORDER BY inx ASC, id ASC LIMIT 5;

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
  SELECT 1 FROM node_inbound WHERE kernel_id > 0 GROUP BY node_id, port HAVING COUNT(*) > 1
);
-- F-122: 用户名列名为 "user"（与第 6 节同口径）；原汇总写 username，
-- 在真实 schema 上直接报错、整段汇总中断。
SELECT 'username_dup_groups' AS item, COUNT(*) AS n FROM (
  SELECT 1 FROM "user" GROUP BY "user" HAVING COUNT(*) > 1
);
-- B02-003: 大小写变体重复组数（与第 6b 节同口径）。
SELECT 'username_fold_dup_groups' AS item, COUNT(*) AS n FROM (
  SELECT 1 FROM "user" GROUP BY LOWER("user") HAVING COUNT(*) > 1
);
-- F-122: subscription 既无 is_default 列也无 user_id 列——「默认订阅」
-- 是运行时按排序第一条计算的概念（见第 7 节），原汇总查询必然报错。
-- 这里以订阅总数代替该不存在的分组统计。
SELECT 'subscriptions_total' AS item, COUNT(*) AS n FROM subscription;
