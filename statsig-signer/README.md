# Self-hosted Statsig signer

English | [简体中文](README.zh-CN.md)

This service is a Playwright-backed replacement for `https://grok.wodf.de/sign`.
It keeps a real Grok page open during calibration, captures one browser-generated
`x-statsig-id`, verifies the embedded seed and SHA input byte-for-byte, then uses
the verified pair for fast per-request signing in Node.

The browser is not launched for every `/sign` request. A refresh runs at startup
and every 30 minutes by default. The previous verified pair remains available if
a later refresh fails.

## API

`POST /sign` accepts the same payload used by Grok2API:

```json
{
  "method": "POST",
  "path": "/rest/app-chat/conversations/new",
  "environment": { "metaContent": "optional-compatible-field" }
}
```

Successful responses are:

```json
{ "x-statsig-id": "..." }
```

`GET /healthz` reports readiness without returning the seed or HEX. `POST
/refresh` forces a browser recalibration. The service returns `503` until it has
one verified pair, unless `SIGNER_FALLBACK_SEED` and `SIGNER_FALLBACK_HEX` are
configured.

## Compose

Run it with Grok2API, WARP, and FlareSolverr from the repository root:

```powershell
docker compose -f docker-compose.all.yml up -d
```

Run it as a standalone public signer without the Grok2API service:

```powershell
docker compose -f docker-compose.statsig-signer.yml up -d
```

The standalone endpoint is `http://HOST:8787/sign`. The default image is
`ghcr.io/jackma15115/grok2api-statsig-signer:latest`. Set
`STATSIG_SIGNER_IMAGE` to use a fork or a pinned release tag.

In Grok2API settings select the URL Statsig mode and set:

```text
http://statsig-signer:8787/sign
```

The all-in-one Compose file keeps the signer on the internal network. The
standalone file publishes port `8787`; protect public deployments with a reverse
proxy, rate limiting, and an allowlist where possible.

## Calibration environment

| Variable | Default | Purpose |
| --- | --- | --- |
| `SIGNER_TARGET_URL` | `https://grok.com/` | Page used for calibration |
| `SIGNER_FLARESOLVERR_URL` | empty outside Compose | Solve Cloudflare before launching Playwright |
| `SIGNER_FLARESOLVERR_TIMEOUT_MS` | `60000` | FlareSolverr solve timeout |
| `SIGNER_PROBE_PATH` | `/rest/rate-limits` | Aborted fetch used to trigger the page interceptor |
| `SIGNER_COOKIE` | empty | Optional browser Cookie header for Cloudflare/session access |
| `SIGNER_USER_AGENT` | Playwright default | Match the UA used to obtain `SIGNER_COOKIE` |
| `SIGNER_PROXY_URL` | empty | Optional HTTP(S)/SOCKS5 browser egress |
| `SIGNER_BROWSER_EXECUTABLE_PATH` | bundled Chromium | Optional host Chrome/Edge executable |
| `SIGNER_REFRESH_INTERVAL_MS` | `1800000` | Recalibration interval |
| `SIGNER_PAGE_SETTLE_MS` | `5000` | Time allowed for page scripts or a browser challenge |
| `SIGNER_API_TOKEN` | empty | Optional Bearer token for `/sign` and `/refresh` |

The current Grok2API URL signer client does not send an Authorization header,
so leave `SIGNER_API_TOKEN` empty when connecting it directly. Use the token
only behind a client or reverse proxy that adds the header.

When FlareSolverr is configured, the signer first requests a solved Grok page
using the same `SIGNER_PROXY_URL`, then injects the returned cookies and
User-Agent into Playwright. FlareSolverr cannot run the Statsig SHA hook itself,
so Playwright is still required for the final browser capture. If the two
containers do not share the same public egress, Cloudflare may reject the
transferred clearance.

If calibration still fails, `/healthz` remains `503`. The service deliberately
does not treat a random 70-byte value as ready.

## Tests

Pure signing and HTTP compatibility tests do not require a browser:

```powershell
npm test
```

To run the Playwright capture integration test with a host browser:

```powershell
$env:SIGNER_TEST_BROWSER = "C:\Program Files\Google\Chrome\Application\chrome.exe"
node --test test/calibrator.integration.test.mjs
```
