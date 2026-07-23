FROM node:22-bookworm-slim AS dependencies

WORKDIR /build
COPY package.json package-lock.json ./
RUN npm ci --omit=dev --ignore-scripts

FROM ghcr.io/flaresolverr/flaresolverr:v3.5.0

ENV NODE_ENV=production \
    CATCH_HOST=0.0.0.0 \
    CATCH_PORT=8789 \
    CATCH_FLARESOLVERR_URL=http://127.0.0.1:8191 \
    CATCH_BROWSER_EXECUTABLE_PATH=/usr/bin/chromium \
    CATCH_PAGE_SETTLE_MS=5000 \
    CATCH_REFRESH_INTERVAL_MS=600000

USER root
COPY --from=dependencies /usr/local/bin/node /usr/local/bin/node
COPY --from=dependencies /build/node_modules /opt/seed-hex-catch/node_modules
COPY package.json package-lock.json /opt/seed-hex-catch/
COPY src /opt/seed-hex-catch/src
COPY --chmod=0755 docker-entrypoint.sh /usr/local/bin/seed-hex-catch-entrypoint
RUN chown -R flaresolverr:flaresolverr /opt/seed-hex-catch

USER flaresolverr
EXPOSE 8789
HEALTHCHECK --interval=30s --timeout=5s --start-period=120s --retries=3 \
  CMD curl -fsS http://127.0.0.1:8789/healthz >/dev/null || exit 1

ENTRYPOINT ["/usr/local/bin/seed-hex-catch-entrypoint"]
