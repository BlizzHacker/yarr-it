# Yarr.It in one image: the search API, the peer bridge and the web client,
# behind Caddy on one origin. Build context is the repository root.
#
#   docker build -f engine/yarrit.Dockerfile -t yarrit .

FROM golang:1.24-bookworm AS go
WORKDIR /src
COPY stream/search/ search/
COPY stream/bridge/ bridge/
RUN cd search && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mw-search . \
 && cd ../bridge && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mw-bridge .

FROM node:22-bookworm-slim AS web
WORKDIR /src/stream
# schema.json is imported by the client from ../search, so it travels too.
COPY stream/search/schema.json search/schema.json
COPY stream/web/package.json stream/web/package-lock.json web/
# --ignore-scripts: the client needs esbuild and webtorrent's prebuilt dist
# files only; webtorrent's optional native peers try to compile and fail.
RUN cd web && npm ci --no-audit --no-fund --ignore-scripts
# Ruffle (Flash) is too large for git; fetch-vendor.sh downloads the pinned
# release so the image can play SWF games. build-dist.sh refuses to build without it.
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl python3 && rm -rf /var/lib/apt/lists/*
COPY stream/web/ web/
RUN bash web/fetch-vendor.sh && bash web/build-dist.sh /out/www

FROM caddy:2.8
# yt-dlp backs the paste-a-link resolver; without it uncached links return 503.
RUN apk add --no-cache tini yt-dlp
COPY --from=go /out/mw-search /out/mw-bridge /usr/local/bin/
COPY --from=web /out/www /srv/www
COPY engine/Caddyfile /etc/caddy/Caddyfile
COPY engine/yarrit-entrypoint.sh /usr/local/bin/yarrit-entrypoint
RUN chmod +x /usr/local/bin/yarrit-entrypoint
ENV LIBRARY_PATH=/data/library.json \
    SEARCH_CACHE_DIR=/data/search-cache \
    LINEAR_STATE_DIR=/data/linear \
    ADDON_STATE_PATH=/data/addons.json
VOLUME /data
EXPOSE 8800
ENTRYPOINT ["/sbin/tini", "--", "yarrit-entrypoint"]
