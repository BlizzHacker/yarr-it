# vps-edge

> **Yarr.It is dedicated to the memory of Samuel Thomas Trimble Cliffe
> (1992–2011), who wanted it to exist.** See [DEDICATION.md](DEDICATION.md).

Configuration and tooling for the MoveWeight VPS mail edge.

**Host:** `104.129.28.137` (RackNerd, Chicago) — `relay.moveweight.net`
**Role:** inbound + outbound mail edge for the estate, keeping `173.207.168.55`
(CableOne, home) out of public DNS.

Specs and plan:
- `../docs/superpowers/specs/2026-07-26-vps-edge-design.md`
- `../docs/superpowers/plans/2026-07-26-vps-mail-edge.md`

## Access

The VPS accepts key auth only, from the Proxmox host `192.168.0.6`
(`/root/.ssh/vps_edge`). Everything goes through the wrapper:

```bash
bash scripts/vps.sh 'postconf -n'
```

## Layout

| Path | Purpose |
|---|---|
| `scripts/vps.sh` | run a command on the VPS |
| `scripts/sync-recipients.sh` | rebuild postfix's valid-recipient map from Hestia |
| `scripts/audit-dns.py` | list any public DNS record still exposing home/private IPs |
| `postfix/` | reference copies of VPS postfix config |
| `baseline/` | captured pre-change state |

## Secrets

None are stored here. The Cloudflare token lives in `/etc/traefik/cloudflare.env`
on LXC 107; the SASL relay password lives in `/etc/exim4/relay_pool.conf` on LXC 160.
