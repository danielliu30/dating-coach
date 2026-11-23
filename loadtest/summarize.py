#!/usr/bin/env python3
"""Print a one-line summary per component from the newest out/*.json report."""
import glob
import json
import os

WATCH = ("fail", "desync", "cross", "leak", "missing", "mismatch",
         "conflict", "unexpected", "violation", "foreign")

latest = {}
for p in glob.glob(os.path.join("out", "*.json")):
    comp = os.path.basename(p).rsplit("-", 1)[0]
    mt = os.path.getmtime(p)
    if mt > latest.get(comp, (0,))[0]:
        latest[comp] = (mt, p)

for comp, (mt, p) in sorted(latest.items()):
    with open(p) as f:
        d = json.load(f)
    ramp = (d.get("extra") or {}).get("ramp") or {}
    good = (ramp.get("last_good_stage") or {}).get("Name", "-")
    flags = {k: v for k, v in (d.get("counters") or {}).items()
             if v and any(w in k for w in WATCH)}
    print(f"{comp:9} last_good={good:6} tripped={str(ramp.get('tripped', False)):5} {flags}")
