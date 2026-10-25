#!/usr/bin/env bash
# R21-F01 new-machine agent restore: put back the pristine gost.json
# (byte-identical; current file is {"services": null}, semantically
# equal but md5 differs), restart the agent, verify md5 + node online.
# Lane R21-F01; run only while holding the testmachine lock.
set -u
R=/root/r21-f01
echo "=== agent restore start=$(date -u +%FT%TZ) initiator=R21-F01 ==="
md5sum /etc/flux_agent/gost.json /etc/flux_agent/flux_agent
cp $R/gost.json.pristine /etc/flux_agent/gost.json
md5sum /etc/flux_agent/gost.json
systemctl reset-failed flux_agent
systemctl restart flux_agent
systemctl is-active flux_agent
sleep 5
md5sum /etc/flux_agent/gost.json /etc/flux_agent/flux_agent
python3 -c "import json;d=json.load(open('/etc/flux_agent/gost.json'));print('services',len(d.get('services') or []));print('climiters',sorted(c.get('name','') for c in (d.get('climiters') or [])));print('refs',sorted({s.get('climiter') for s in (d.get('services') or []) if s.get('climiter')}))"
echo "=== agent restore done=$(date -u +%FT%TZ) ==="
