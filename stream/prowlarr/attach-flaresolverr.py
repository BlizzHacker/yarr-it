#!/usr/bin/env python3
"""Attach the flaresolverr tag to the proxy and to Cloudflare-walled indexers.

Prowlarr only routes an indexer through an indexer-proxy when the two share a
tag. Without that link the proxy exists but is never used, and every
Cloudflare-protected tracker fails its connectivity test on add -- which is
exactly what happened to 1337x, EZTV and friends.
"""
import json
import urllib.error
import urllib.request

KEY = "PROWLARR_API_KEY_REDACTED"
BASE = "http://localhost:9696/api/v1"

# LXC 112. Prowlarr had 192.168.0.126, which nothing answers on.
FLARESOLVERR_URL = "http://192.168.0.146:8191"

# Trackers known to sit behind Cloudflare; they need the proxy to be reachable.
CLOUDFLARE_WALLED = [
    "1337x", "eztv", "extratorrent-st", "kickasstorrents-to",
    "ebookbay", "btdirectory", "torrentdownload", "gamestorrents",
    "linuxtracker", "torrent9",
]


def api(path, method="GET", body=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(
        BASE + path, data=data, method=method,
        headers={"X-Api-Key": KEY, "Content-Type": "application/json"},
    )
    raw = urllib.request.urlopen(req, timeout=240).read()
    return json.loads(raw) if raw else None


def main():
    tags = api("/tag")
    tag = next((t for t in tags if t["label"] == "flaresolverr"), None)
    if tag is None:
        tag = api("/tag", "POST", {"label": "flaresolverr"})
    tid = tag["id"]
    print(f"tag: {tag['label']} (id {tid})")

    # Point the proxy at the right host and link it to the tag.
    #
    # It was configured as 192.168.0.126, but LXC 112 actually answers on
    # 192.168.0.146 -- so the proxy had never worked, and every Cloudflare
    # protected tracker failed its connectivity test on add. Prowlarr validates
    # the host on save, so a wrong address also makes the PUT itself 400.
    for proxy in api("/indexerproxy"):
        if proxy.get("implementation") != "FlareSolverr":
            continue
        changed = False
        for field in proxy.get("fields", []):
            if field.get("name") == "host" and field.get("value") != FLARESOLVERR_URL:
                print(f"fixing host {field.get('value')} -> {FLARESOLVERR_URL}")
                field["value"] = FLARESOLVERR_URL
                changed = True
        if tid not in (proxy.get("tags") or []):
            proxy["tags"] = sorted(set((proxy.get("tags") or []) + [tid]))
            changed = True
        if changed:
            api(f"/indexerproxy/{proxy['id']}", "PUT", proxy)
            print(f"updated proxy {proxy['name']}")
        else:
            print(f"proxy {proxy['name']} already correct")

    # Tag any already-configured indexer that needs the proxy.
    for idx in api("/indexer"):
        if idx.get("definitionName") in CLOUDFLARE_WALLED:
            if tid not in (idx.get("tags") or []):
                idx["tags"] = sorted(set((idx.get("tags") or []) + [tid]))
                api(f"/indexer/{idx['id']}", "PUT", idx)
                print(f"tagged existing indexer {idx['name']}")

    # Add the ones that previously failed, now that the proxy is wired.
    schema = {s.get("definitionName"): s for s in api("/indexer/schema")}
    existing = {i.get("definitionName") for i in api("/indexer")}
    added, failed = [], []
    for name in CLOUDFLARE_WALLED:
        if name in existing:
            continue
        spec = schema.get(name)
        if not spec:
            failed.append((name, "no definition"))
            continue
        spec = dict(spec)
        spec.update(enable=True, appProfileId=1, tags=[tid])
        spec.pop("id", None)
        try:
            res = api("/indexer", "POST", spec)
            added.append(res.get("name", name))
        except urllib.error.HTTPError as err:
            detail = err.read().decode()
            msg = "blocked"
            try:
                parsed = json.loads(detail)
                if isinstance(parsed, list) and parsed:
                    msg = parsed[0].get("errorMessage", msg)[:60]
            except json.JSONDecodeError:
                pass
            failed.append((name, msg))
        except Exception as err:  # noqa: BLE001 - report and continue
            failed.append((name, str(err)[:60]))

    print("\nADDED:", ", ".join(added) if added else "none")
    for name, why in failed:
        print(f"  failed {name:<22} {why}")

    torrents = [i for i in api("/indexer") if i.get("protocol") == "torrent"]
    print(f"\nTOTAL torrent indexers: {len(torrents)} "
          f"(enabled {sum(1 for i in torrents if i.get('enable'))})")


if __name__ == "__main__":
    main()
