#!/usr/bin/env bash
# Rebuild yarrit.com on this host, from scratch, with no outside help.
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
#
# **raw.githubusercontent.com caches for several minutes.** Immediately after a
# push, that URL serves the PREVIOUS version, and the script happily clones the
# new commit while running the old logic -- which looks like a fix that did not
# work rather than a file that never arrived. To re-run after a change, use the
# clone this script already made, which is always at the fetched commit:
#
#   bash /opt/yarr-it/stream/deploy/bootstrap.sh
set -euo pipefail

REPO=https://github.com/BlizzHacker/yarr-it.git
BRANCH=feat/universal-source-layer
SRC=/opt/yarr-it
WWW=/srv/stream/www

[ "$(id -u)" -eq 0 ] || { echo "run as root"; exit 1; }

# --- do not damage the mail relay -------------------------------------------
# The relay certificate now renews over DNS-01 (certbot --dns-cloudflare),
# which completes through the Cloudflare API and needs no port, so Caddy may
# hold :80 and serve the HTTP->HTTPS redirect. Before that nothing answered on
# :80 at all: desktop browsers quietly upgraded to HTTPS and looked fine, while
# a phone typing a bare hostname got connection refused.
#
# If this host is ever rebuilt, /root/.secrets/cloudflare.ini must exist with
# dns_cloudflare_api_token, or renewal falls back to wanting :80 again.
CERTBOT_STANDALONE=0
if [ -d /etc/letsencrypt/renewal ] && grep -rqs "authenticator *= *standalone" /etc/letsencrypt/renewal; then
  CERTBOT_STANDALONE=1
  echo "==> certbot uses standalone (:80); Caddy will be held off that port"
fi

echo "==> packages (no firewall, nothing mail-related)"
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq git nodejs npm debian-keyring debian-archive-keyring apt-transport-https curl ca-certificates

# Go, from upstream rather than apt. Debian 12 ships 1.19 and both modules
# declare `go 1.24`, so the packaged toolchain cannot build them at all -- it
# fails with a version complaint that reads like a broken checkout.
GO_WANT=1.24
GO_HAVE=$(go version 2>/dev/null | sed -n 's/.*go\([0-9][0-9.]*\).*/\1/p')
# sort -V -C succeeds only when input is already ordered, i.e. want <= have.
if [ -z "$GO_HAVE" ] || ! printf '%s\n%s\n' "$GO_WANT" "$GO_HAVE" | sort -V -C; then
  GO_VER=$(curl -fsSL 'https://go.dev/VERSION?m=text' 2>/dev/null | head -1)
  case "$GO_VER" in go*) ;; *) GO_VER=go1.24.5 ;; esac
  echo "    installing $GO_VER (found: ${GO_HAVE:-none})"
  curl -fsSL "https://go.dev/dl/${GO_VER}.linux-amd64.tar.gz" -o /tmp/go.tgz
  rm -rf /usr/local/go && tar -C /usr/local -xzf /tmp/go.tgz && rm -f /tmp/go.tgz
fi
export PATH=/usr/local/go/bin:$PATH
grep -qs '/usr/local/go/bin' /etc/profile.d/go.sh 2>/dev/null || \
  echo 'export PATH=/usr/local/go/bin:$PATH' > /etc/profile.d/go.sh
go version | sed 's/^/    /'

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

# Sign-in. The gate only switches on once both SSO values are set, so a
# half-filled file leaves the site open rather than locking everyone out.
SSO_CLIENT_ID=
SSO_CLIENT_SECRET=
# Persist this or every restart signs everybody out.
SESSION_SECRET=

# How far the gate reaches:
#   tv  - TV apps must sign in, browsers are open (default, and what ships)
#   all - everybody signs in, browsers included
#   off - nobody does
AUTH_SCOPE=tv

# Origin of a Vimm vault, if there is one. Blank means every Vimm result stays
# an external link to vimm.net, which is what a deployment without a vault can
# honestly offer. Set it and the entries the vault can serve play here instead,
# with its box art. See vimmVaultBase in search/vimm.go.
VIMM_VAULT=
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
for m in tracker-udp.js engine.js bridge-peer.js server.js dht.js bencode.js; do
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
# validate runs as root and OPENS the access log, creating it root:root 0600 --
# after which the caddy user cannot write it and the service dies on start with
# "permission denied". Re-chown after validating, not before.
chown -R caddy:caddy /var/log/caddy
systemctl restart caddy

echo "==> verifying the mail relay is untouched"
if [ "$CERTBOT_STANDALONE" = "1" ]; then
  # The port field must END at :80 -- a bare ":80" also matches 8801
  # and 8802, which are our own services.
  if ss -lntp 2>/dev/null | awk '$4 ~ /:80$/' | grep -q caddy; then
    echo "FAIL: caddy took port 80 -- certbot standalone renewal would break." >&2
    echo "Stopping caddy and leaving the relay intact." >&2
    systemctl stop caddy
    exit 1
  fi
  echo "    caddy is not on :80  (certbot keeps it)"
  # The real proof. A dry run exercises the whole renewal path without
  # spending a rate limit or replacing the live certificate.
  if certbot renew --dry-run --cert-name relay >/tmp/certbot-dryrun.log 2>&1; then
    echo "    certbot renew --dry-run: PASS"
  else
    echo "FAIL: certbot dry-run failed after starting caddy. Stopping caddy." >&2
    tail -15 /tmp/certbot-dryrun.log >&2
    systemctl stop caddy
    exit 1
  fi
fi
if ss -lnt 2>/dev/null | awk '{print $4}' | grep -qE ':25$'; then
  PORT25=listening
else
  PORT25=GONE
fi
printf "    postfix: %s   port 25: %s
" "$(systemctl is-active postfix)" "$PORT25"

echo "==> state"
for u in mw-bridge mw-search caddy postfix; do
  printf "    %-12s %s\n" "$u" "$(systemctl is-active "$u" 2>/dev/null || echo n/a)"
done
ss -lntp 2>/dev/null | grep -E ":(80|443|8801|8802)\b" | sed 's/^/    /'
echo "    build $HASH"

cat <<EOF

==> yarrit.com should now answer. Two notes:

  * mw-search reaches Prowlarr at 192.168.0.115:9696, which is not routable
    from here. Search will return nothing until that path exists -- it fails as
    an empty result set, not an error, so do not read silence as success.
${NEED_KEYS:+  * /etc/mw-search.env was created blank. Fill in the IGDB/Prowlarr/TMDB keys
    and 'systemctl restart mw-search', or every shelf stays empty.
}
Mail was not touched: postfix, the reverse tunnel and the absence of ufw are
exactly as they were.
EOF
