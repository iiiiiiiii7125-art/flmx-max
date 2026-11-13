#!/usr/bin/env python3
"""check-nginx-timeouts.sh — nginx 代理超时与服务端写截止对账门（N06）。

AUDIT-HIST L7 (N06) 立案：13 条慢路径的服务端写截止 S 大于 nginx 按
最长匹配选中的 location 的 proxy_read_timeout N，后端作业耗时落入
(N, S] 时 nginx 先回 504（后端仍在跑、结果丢弃）。本门机械对账，
任一路由 S > N 即 FAIL。

- 服务端值 S：从 go-backend/internal/http/handler/write_deadline.go
  与 gh_mirror.go 机械提取具名常量（秒），不写死数值。
- 代理值 N：解析 vite-frontend/nginx.conf 的 location 块，按 nginx
  最长匹配选型（精确 `=` > 最长 `^~` 前缀 > 通用 `^~ /api/v1/`；
  无 location 时 nginx 默认 60s），取 proxy_read_timeout（与
  proxy_send_timeout 成对核查，两者须同时 >= S）。
- 路由表：N06 失配 13 条＋此前已对齐 12 条（回归网）；新增慢路径
  端点须在此登记，否则下一轮审计的普查差集会点名。

用法：check-nginx-timeouts.sh [REPO_ROOT]（默认脚本所在仓库根）。
退出码：0 = 全部对齐；1 = 存在失配/缺格/前提失效。
变异自检：把任一 location 的超时改小（或把任一写截止常量改大），
本门必须红（见 check-timeout-budgets-selftest.sh 的同族做法）。
"""

import re
import sys
from pathlib import Path

# 路由 -> 服务端写截止常量名。前缀路由以 "/" 结尾，表示整段前缀。
ROUTES = {
    # N06 A 组（无条件/长作业 bound）
    "/api/v1/node/upgrade": "nodeUpgradeWriteDeadline",
    "/api/v1/inbound/batch-delete": "inboundBatchWriteDeadline",
    "/api/v1/inbound/batch-toggle": "inboundBatchWriteDeadline",
    "/api/v1/node/swap-set": "nodeSwapSetWriteDeadline",
    "/api/v1/node/install": "nodeInstallWriteDeadline",
    "/api/v1/open_api/gh/": "ghMirrorServeFor",
    # N06 B 组（条件性内核 apply）
    "/api/v1/kernel/control": "kernelApplyWriteDeadline",
    "/api/v1/inbound/delete": "kernelApplyWriteDeadline",
    "/api/v1/inbound/toggle": "kernelApplyWriteDeadline",
    "/api/v1/inbound/regenerate": "kernelApplyWriteDeadline",
    "/api/v1/inbound/split": "kernelApplyWriteDeadline",
    "/api/v1/inbound/external/update": "kernelApplyWriteDeadline",
    "/api/v1/subscription/save": "kernelApplyWriteDeadline",
    # 此前已对齐（回归网：S <= N 必须保持）
    "/api/v1/kernel/install": "kernelApplyWriteDeadline",
    "/api/v1/inbound/create": "kernelApplyWriteDeadline",
    "/api/v1/inbound/update": "kernelApplyWriteDeadline",
    "/api/v1/node/batch-upgrade": "nodeBatchUpgradeWriteDeadline",
    "/api/v1/system/version": "systemVersionWriteDeadline",
    "/api/v1/system/check-updates": "systemVersionWriteDeadline",
    "/api/v1/system/upgrade": "systemUpgradeWriteDeadline",
    "/api/v1/tunnel/create": "tunnelCreateWriteDeadline",
    "/api/v1/tunnel/update": "tunnelUpdateWriteDeadline",
    "/api/v1/tunnel/diagnose": "diagnosisWriteDeadline",
    "/api/v1/forward/diagnose": "diagnosisWriteDeadline",
    "/api/v1/backup/": "backupWriteDeadline",
}

CONST_RE = re.compile(
    r"^\s*([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(\d+)\s*\*\s*time\.(Second|Minute)\s*$",
    re.M,
)
LOCATION_RE = re.compile(
    r"location\s+(?:(=\s*|\^~\s*|~\*?\s*))?(\S+)\s*\{", re.M
)
TIMEOUT_RE = re.compile(r"proxy_(read|send)_timeout\s+(\d+)s\s*;")


def parse_go_constants(root: Path) -> dict:
    consts = {}
    for rel in (
        "go-backend/internal/http/handler/write_deadline.go",
        "go-backend/internal/http/handler/gh_mirror.go",
    ):
        text = (root / rel).read_text()
        for name, num, unit in CONST_RE.findall(text):
            consts[name] = int(num) * (60 if unit == "Minute" else 1)
    return consts


def parse_nginx_locations(root: Path) -> list:
    text = (root / "vite-frontend/nginx.conf").read_text()
    locations = []
    for m in LOCATION_RE.finditer(text):
        modifier = (m.group(1) or "").strip()
        pattern = m.group(2)
        # Brace-matching: location bodies in this file nest no blocks.
        depth, i = 0, m.end() - 1
        while i < len(text):
            if text[i] == "{":
                depth += 1
            elif text[i] == "}":
                depth -= 1
                if depth == 0:
                    break
            i += 1
        body = text[m.end():i]
        timeouts = dict(TIMEOUT_RE.findall(body))
        locations.append(
            {
                "modifier": modifier,
                "pattern": pattern,
                "read": int(timeouts["read"]) if "read" in timeouts else None,
                "send": int(timeouts["send"]) if "send" in timeouts else None,
            }
        )
    return locations


def effective_timeout(route: str, locations: list) -> tuple:
    """Return (read, send, matched_pattern) per nginx longest match."""
    for loc in locations:
        if loc["modifier"] == "=" and loc["pattern"] == route:
            return loc["read"], loc["send"], f"= {loc['pattern']}"
    best = None
    for loc in locations:
        if loc["modifier"] == "^~" and route.startswith(loc["pattern"]):
            if best is None or len(loc["pattern"]) > len(best["pattern"]):
                best = loc
    if best is not None:
        return best["read"], best["send"], f"^~ {best['pattern']}"
    return 60, 60, "(nginx default 60s)"


def main() -> int:
    root = Path(sys.argv[1] if len(sys.argv) > 1 else Path(__file__).resolve().parent.parent)
    consts = parse_go_constants(root)
    missing_consts = sorted({c for c in ROUTES.values() if c not in consts})
    if missing_consts:
        print(f"FAIL: server write-deadline constants not found: {missing_consts}")
        return 1
    locations = parse_nginx_locations(root)
    if not locations:
        print("FAIL: no locations parsed from vite-frontend/nginx.conf")
        return 1
    failures = []
    for route, const_name in sorted(ROUTES.items()):
        server_s = consts[const_name]
        read, send, matched = effective_timeout(route, locations)
        read = read if read is not None else 60
        send = send if send is not None else 60
        status = "ok" if read >= server_s and send >= server_s else "FAIL"
        print(f"{status} {route} server={server_s}s nginx_read={read}s nginx_send={send}s via {matched} ({const_name})")
        if status == "FAIL":
            failures.append(route)
    if failures:
        print(f"nginx-timeouts: FAIL ({len(failures)}/{len(ROUTES)} routes misaligned: {failures})")
        return 1
    print(f"nginx-timeouts: OK ({len(ROUTES)} routes aligned, server <= nginx)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
