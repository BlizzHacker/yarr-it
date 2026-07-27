# WireGuard: VPS mail edge ↔ LXC 160

**Direction matters.** The VPS is the *listener*; LXC 160 dials out to it.

The obvious layout (VPS dials home at `173.207.168.55:51820`) does not work — the
home router does not forward inbound UDP 51820 to LXC 160, so the handshake never
completes (`0 B received`). Reversing it is also simply better:

- the VPS has a static public IP and already allows 51820/udp in ufw;
- no home port-forward is required, so nothing breaks if the router config is reset;
- it survives a CableOne IP change, because home re-dials out.

## Addresses

| Host | Tunnel IP | Role |
|---|---|---|
| VPS `104.129.28.137` | `10.10.10.3` | listener, `ListenPort = 51820` |
| LXC 160 (Hestia mail) | `10.10.10.1` | dials out, `PersistentKeepalive = 25` |
| (unused demo client, May 2026) | `10.10.10.2` | left alone — **do not reuse** |

## VPS `/etc/wireguard/wg0.conf`

```ini
[Interface]
Address = 10.10.10.3/24
ListenPort = 51820
PrivateKey = <in /etc/wireguard/vps_private.key>

[Peer]
# LXC 160 (HestiaCP mail). Home dials out; no endpoint pinned here.
PublicKey = W9dlV1pNfy6GiB6XU3kTYl5Ruj0WScNVzUjyEEBecTs=
AllowedIPs = 10.10.10.0/24, 192.168.0.0/24
```

## LXC 160 peer block appended to `/etc/wireguard/wg0.conf`

```ini
[Peer]
# VPS mail edge 104.129.28.137
PublicKey = 6Znsb0AWhNkd0c6rXlrBBUuO+7BHscWN+be97pU5Wnw=
AllowedIPs = 10.10.10.3/32
Endpoint = 104.129.28.137:51820
PersistentKeepalive = 25
```

## Verification

```bash
bash scripts/vps.sh 'wg show; ping -c3 10.10.10.1'
bash scripts/vps.sh 'timeout 8 bash -c "exec 3<>/dev/tcp/10.10.10.1/25; head -1 <&3"'
```

Expected: a recent handshake, 0% loss (~74 ms), and
`220 moveweight-hosting.moveweight.com`.

Backups of the LXC 160 config are at `/etc/wireguard/wg0.conf.bak.*`.
