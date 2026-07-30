#!/usr/bin/env bash
# Provision a bare VPS to serve yarrit.com.
#
# This exists because the edge host was rebuilt on 2026-07-28 and the entire
# stream stack went with it -- Caddy, both Go services and their unit files had
# only ever been created by hand on the box, so there was nothing to restore
# from. deploy.sh ships the frontend and assumes everything underneath it is
# already running; this is the part that was missing.
#
# Safe to re-run. It installs and enables, it does not wipe: an existing
# /etc/mw-search.env is left alone, and Caddy config is replaced only from the
# repo's own Caddyfile.
#
# Usage, from a machine that can reach the VPS:
#   stream/deploy/provision.sh            # build, upload, install, start
#   stream/deploy/provision.sh --no-build # reuse binaries already built
set -euo pipefail
cd "$(dirname "$0")/.."          # -> stream/

JUMP=root@192.168.0.6
KEY=/root/.ssh/vps_edge
VPS=root@104.129.28.137
BIN_OUT=${BIN_OUT:-./deploy/bin}

# Every command reaches the VPS through the jump host, the same double hop
# deploy.sh uses -- the edge does not accept connections from anywhere else.
on_vps() { ssh "$JUMP" "ssh -i $KEY -o BatchMode=yes $VPS '$1'"; }
to_vps()  { ssh "$JUMP" "ssh -i $KEY -o BatchMode=yes $VPS 'cat > $1'" < "$2"; }

if [ "${1:-}" != "--no-build" ]; then
  echo "==> building static linux binaries"
  mkdir -p "$BIN_OUT"
  for svc in search bridge; do
    ( cd "$svc" && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
        go build -trimpath -ldflags="-s -w" -o "../$BIN_OUT/mw-$svc" . )
    echo "    mw-$svc"
  done
fi

echo "==> checking we can reach the VPS"
on_vps 'true' || {
  echo "cannot reach $VPS with $KEY."
  echo "After a rebuild the host has no authorized_keys. Add the deploy key:"
  echo "  mkdir -p /root/.ssh && chmod 700 /root/.ssh"
  echo "  echo '<contents of ${KEY}.pub>' >> /root/.ssh/authorized_keys"
  echo "  chmod 600 /root/.ssh/authorized_keys"
  exit 1
}

echo "==> installing caddy"
on_vps 'command -v caddy >/dev/null || {
  apt-get update -qq
  apt-get install -y -qq debian-keyring debian-archive-keyring apt-transport-https curl
  curl -fsSL https://dl.cloudsmith.io/public/caddy/stable/gpg.key \
    | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
  echo "deb [signed-by=/usr/share/keyrings/caddy-stable-archive-keyring.gpg] https://dl.cloudsmith.io/public/caddy/stable/deb/debian any-version main" \
    > /etc/apt/sources.list.d/caddy-stable.list
  apt-get update -qq && apt-get install -y -qq caddy
}'

echo "==> service accounts and directories"
on_vps 'for u in mw-search mw-bridge; do
  id "$u" >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin "$u"
done
mkdir -p /srv/stream/www /var/log/caddy
chown -R caddy:caddy /srv/stream /var/log/caddy'

echo "==> binaries"
for svc in search bridge; do
  to_vps "/usr/local/bin/mw-$svc.new" "$BIN_OUT/mw-$svc"
  # Swapped into place rather than written over: a running binary cannot be
  # overwritten, and a half-uploaded one must never become the live file.
  on_vps "chmod 0755 /usr/local/bin/mw-$svc.new && mv /usr/local/bin/mw-$svc.new /usr/local/bin/mw-$svc"
  echo "    mw-$svc"
done

echo "==> units and site config"
to_vps /etc/systemd/system/mw-search.service deploy/mw-search.service
to_vps /etc/systemd/system/mw-bridge.service deploy/mw-bridge.service
to_vps /etc/caddy/Caddyfile                  Caddyfile

# The API keys are not in the repo. Write a template if none exists so systemd
# has something to read, but leave any real file untouched.
on_vps 'test -f /etc/mw-search.env || {
  cat > /etc/mw-search.env <<EOF
# Fill these in -- mw-search starts without them but answers with empty
# shelves, which reads as a broken frontend rather than missing credentials.
IGDB_CLIENT_ID=
IGDB_CLIENT_SECRET=
PROWLARR_API_KEY=
TMDB_API_KEY=
EOF
  chmod 0640 /etc/mw-search.env
  chown root:mw-search /etc/mw-search.env
  echo "    NOTE: wrote a blank /etc/mw-search.env -- fill it in"
}'

echo "==> starting"
on_vps 'systemctl daemon-reload
systemctl enable --now mw-bridge mw-search >/dev/null 2>&1
systemctl restart mw-bridge mw-search
caddy validate --config /etc/caddy/Caddyfile >/dev/null && systemctl restart caddy'

echo "==> state"
on_vps 'for u in mw-bridge mw-search caddy; do printf "    %-12s %s\n" "$u" "$(systemctl is-active $u)"; done
ss -lntp 2>/dev/null | grep -E ":(80|443|8801|8802)\b" | sed "s/^/    /"'

cat <<'EOF'

==> done. Two things this script deliberately does not do:

  * WireGuard. mw-search reaches Prowlarr at 192.168.0.115:9696, which is only
    routable over the tunnel -- see ../wireguard/. Search returns nothing until
    that peer is up, and the failure looks like an empty index rather than a
    network problem.
  * The frontend. Run stream/web/deploy.sh next.
EOF
