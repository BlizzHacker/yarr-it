#!/usr/bin/env bash
# Rebuild stream.moveweight.com on this host, from scratch, with no outside help.
#
# provision.sh drives the VPS from a workstation over the jump host. This does
# the same job while running ON the VPS, for when key access from outside is not
# available -- paste it into a console session and walk away.
#
# It is deliberately narrow about what it touches:
#
#   * It NEVER installs, enables or configures ufw. ufw's default policy blocked
#     outbound port 25 and took mail down for weeks while the provider took the
#     blame. It is purged from this host and must stay that way.
#   * It NEVER touches postfix, the reverse SSH tunnel, or anything mail. Those
#     are working and verified; this only adds a web stack beside them.
#
# Usage, as root on the VPS:
#   curl -fsSL https://raw.githubusercontent.com/BlizzHacker/yarr-it/feat/universal-source-layer/stream/deploy/bootstrap.sh -o bootstrap.sh
#   less bootstrap.sh          # it is short; read it before running it
#   bash bootstrap.sh
set -euo pipefail

REPO=https://github.com/BlizzHacker/yarr-it.git
BRANCH=feat/universal-source-layer
SRC=/opt/yarr-it
WWW=/srv/stream/www

[ "$(id -u)" -eq 0 ] || { echo "run as root"; exit 1; }

# --- refuse to damage the mail relay ----------------------------------------
# Caddy will want :80 for ACME. If certbot renews the relay's certificate with
# the standalone plugin, it also needs :80, and whichever holds it wins -- the
# renewal would fail silently ~60 days from now, long after anyone connects it
# to this script. Better to stop and be told.
if [ -d /etc/letsencrypt/renewal ] && grep -rqs "authenticator *= *standalone" /etc/letsencrypt/renewal; then
  cat >&2 <<'EOF'
REFUSING: certbot renews a certificate here using the standalone plugin, which
binds port 80. Caddy needs port 80 for its own ACME challenges, and the two
cannot share it -- the mail certificate would fail to renew, silently, weeks
from now.

Switch that renewal to --webroot (/var/www/html) or a DNS challenge first, then
re-run. Nothing has been changed.
EOF
  exit 1
fi

echo "==> packages (no firewall, nothing mail-related)"
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq git golang-go nodejs npm debian-keyring debian-archive-keyring apt-transport-https curl

if ! command -v caddy >/dev/null; then
  curl -fsSL https://dl.cloudsmith.io/public/caddy/stable/gpg.key \
    | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
  echo "deb [signed-by=/usr/share/keyrings/caddy-stable-archive-keyring.gpg] https://dl.cloudsmith.io/public/caddy/stable/deb/debian any-version main" \
    > /etc/apt/sources.list.d/caddy-stable.list
  apt-get update -qq && apt-get install -y -qq caddy
fi

echo "==> source"
if [ -d "$SRC/.git" ]; then
  git -C "$SRC" fetch --depth 1 origin "$BRANCH" && git -C "$SRC" reset --hard FETCH_HEAD
else
  git clone --depth 1 --branch "$BRANCH" "$REPO" "$SRC"
fi

echo "==> building services"
for svc in search bridge; do
  ( cd "$SRC/stream/$svc" && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o "/usr/local/bin/mw-$svc.new" . )
  chmod 0755 "/usr/local/bin/mw-$svc.new"
  # Swap rather than overwrite: a running binary cannot be written to, and a
  # half-written one must never become the live file.
  mv "/usr/local/bin/mw-$svc.new" "/usr/local/bin/mw-$svc"
  echo "    mw-$svc"
done

echo "==> accounts, units, site config"
for u in mw-search mw-bridge; do
  id "$u" >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin "$u"
done
mkdir -p "$WWW" /var/log/caddy
install -m 0644 "$SRC/stream/deploy/mw-search.service" /etc/systemd/system/
install -m 0644 "$SRC/stream/deploy/mw-bridge.service" /etc/systemd/system/
install -m 0644 "$SRC/stream/Caddyfile" /etc/caddy/Caddyfile

# Secrets are not in the repo. Written blank if absent so systemd has something
# to read; mw-search starts without them but serves empty shelves, which reads
# as a broken frontend rather than missing credentials.
if [ ! -f /etc/mw-search.env ]; then
  cat > /etc/mw-search.env <<'EOF'
IGDB_CLIENT_ID=
IGDB_CLIENT_SECRET=
PROWLARR_API_KEY=
TMDB_API_KEY=
EOF
  chmod 0640 /etc/mw-search.env
  chown root:mw-search /etc/mw-search.env
  NEED_KEYS=1
fi

echo "==> frontend"
cd "$SRC/stream/web"
npm ci --silent --no-audit --no-fund 2>/dev/null || npm install --silent --no-audit --no-fund
mkdir -p dist
./node_modules/.bin/esbuild src/main.js --bundle --format=esm --outfile=dist/app.js \
  --define:global=globalThis --external:./webtorrent.min.js --minify
cp -f node_modules/webtorrent/dist/webtorrent.min.js dist/
cp -f node_modules/webtorrent/dist/sw.min.js dist/

# Every build stamps a content hash into the script URL. /app.js is otherwise a
# stable URL behind a week of caching, and a returning visitor would keep
# loading the previous build.
HASH=$(sha256sum dist/app.js | cut -c1-12)
ICONHASH=$(sha256sum dist/icon-512.png 2>/dev/null | cut -c1-12 || echo 000000000000)
sed -e "s/__BUILD__/$HASH/g" -e "s/__ICON__/$ICONHASH/g" index.html > dist/index.html
sed -e "s/__ICON__/$ICONHASH/g" manifest.webmanifest > dist/manifest.webmanifest

install -m 0644 dist/index.html dist/app.js dist/webtorrent.min.js dist/sw.min.js \
                dist/manifest.webmanifest privacy.html "$WWW/"
for m in tracker-udp.js engine.js bridge-peer.js dht.js bencode.js; do
  install -m 0644 "src/$m" "$WWW/"
done
for i in 192 512; do
  [ -f "dist/icon-$i.png" ] || continue
  install -m 0644 "dist/icon-$i.png" "$WWW/icon-$i.png"
  install -m 0644 "dist/icon-$i.png" "$WWW/icon-$i.$ICONHASH.png"
done
[ -d dist/ruffle ] && cp -r dist/ruffle "$WWW/"
[ -d dist/roms ]   && cp -r dist/roms   "$WWW/"
chown -R caddy:caddy /srv/stream /var/log/caddy

echo "==> starting"
systemctl daemon-reload
systemctl enable --now mw-bridge mw-search >/dev/null 2>&1 || true
systemctl restart mw-bridge mw-search
caddy validate --config /etc/caddy/Caddyfile >/dev/null
systemctl restart caddy

echo "==> state"
for u in mw-bridge mw-search caddy postfix; do
  printf "    %-12s %s\n" "$u" "$(systemctl is-active "$u" 2>/dev/null || echo n/a)"
done
ss -lntp 2>/dev/null | grep -E ":(80|443|8801|8802)\b" | sed 's/^/    /'
echo "    build $HASH"

cat <<EOF

==> stream.moveweight.com should now answer. Two notes:

  * mw-search reaches Prowlarr at 192.168.0.115:9696, which is not routable
    from here. Search will return nothing until that path exists -- it fails as
    an empty result set, not an error, so do not read silence as success.
${NEED_KEYS:+  * /etc/mw-search.env was created blank. Fill in the IGDB/Prowlarr/TMDB keys
    and 'systemctl restart mw-search', or every shelf stays empty.
}
Mail was not touched: postfix, the reverse tunnel and the absence of ufw are
exactly as they were.
EOF
