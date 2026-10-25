#!/usr/bin/env bash
# R21-F01 new-machine pristine recon + backups (runs on test machine as root).
# Lane R21-F01; run only while holding the testmachine lock.
set -e
echo "=== pristine recon start=$(date -u +%FT%TZ) initiator=R21-F01 ==="
cd /root/r21-f01
python3 R21-FIX-F01-legs-newmachine.py counts
python3 R21-FIX-F01-legs-newmachine.py nodelist
python3 R21-FIX-F01-legs-newmachine.py gost
cp /etc/flux_agent/gost.json /root/r21-f01/gost.json.pristine
python3 - <<'PYEOF'
import sqlite3
src = sqlite3.connect("file:/var/lib/docker/volumes/sqlite_data/_data/gost.db?mode=ro", uri=True)
dst = sqlite3.connect("/root/r21-f01/gost.db.pristine")
src.backup(dst)
dst.close(); src.close()
print("db backup ok")
c = sqlite3.connect("file:/var/lib/docker/volumes/sqlite_data/_data/gost.db?mode=ro", uri=True)
print("tables", c.execute("SELECT count(*) FROM sqlite_master WHERE type='table'").fetchone()[0])
PYEOF
md5sum /root/r21-f01/gost.db.pristine /etc/flux_agent/gost.json
echo "=== pristine recon end=$(date -u +%FT%TZ) ==="
