#!/usr/bin/env python3
"""Emit every valid mail recipient as postfix relay_recipient_maps lines.

Covers three cases that all matter:
  1. real mailboxes            -> user@domain OK
  2. per-account aliases       -> alias@domain OK
  3. domain catch-alls         -> @domain OK   (every address at that domain)
"""
import json
import subprocess

HESTIA = "/usr/local/hestia/bin"


def run(*args):
    out = subprocess.run([f"{HESTIA}/{args[0]}", *args[1:]],
                         capture_output=True, text=True, timeout=60)
    if out.returncode != 0 or not out.stdout.strip():
        return {}
    try:
        return json.loads(out.stdout)
    except json.JSONDecodeError:
        return {}


lines = []
domains = run("v-list-mail-domains", "admin", "json")
for domain, dinfo in domains.items():
    if dinfo.get("CATCHALL"):
        lines.append(f"@{domain} OK")
    for acct, ainfo in run("v-list-mail-accounts", "admin", domain, "json").items():
        if ainfo.get("SUSPENDED") == "yes":
            continue
        lines.append(f"{acct}@{domain} OK")
        for alias in (ainfo.get("ALIAS") or "").split():
            lines.append(f"{alias}@{domain} OK")

print("\n".join(sorted(set(lines))))
