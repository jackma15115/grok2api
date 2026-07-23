# seed-hex-catch

English | [简体中文](README.zh-CN.md)

`seed-hex-catch` periodically observes the transient SVG path used by Grok's
Statsig module, reads the runtime-selected seed and computed animation style,
encodes HEX independently, and serves the resulting material as JSON. It does
not capture an `x-statsig-id` request and does not hook Web Crypto SHA input.

The published image contains FlareSolverr, Chromium, Playwright, and the
collector in one container. It never observes a signed request or SHA input.
A refresh runs at startup and every 10 minutes by default. Each refresh opens
one natural page in a fresh browser context and closes the browser after the
seed, selected SVG path, and computed style have been paired.

## Start

Run the published image directly with one command:

```bash
docker run -d --name seed-hex-catch --restart unless-stopped --init --security-opt no-new-privileges:true --shm-size 128m -p 8789:8789 ghcr.io/jackma15115/grok2api-seed-hex-catch:latest
```

Or use Compose:

```bash
docker compose -f docker-compose.seed-hex-catch.yml up -d
```

The material endpoint is `http://HOST:8789/material`. In Grok2API, select
`Local` Statsig mode and set the Material service URL to:

```text
http://seed-hex-catch:8789/material
```

Containers started by separate Compose projects do not automatically share a
DNS network. Use the host address, or attach both services to the same external
Docker network.

## API

- `GET /healthz` reports readiness, timestamps, path count, and path version.
- `GET /material` returns `seed`, `hex`, refresh timestamps, and path metadata.
- `POST /refresh` triggers an immediate capture when `CATCH_API_TOKEN` is set.

`CATCH_API_TOKEN` protects `/material` and enables `/refresh` with a Bearer
token. When it is empty, `/material` is public and manual refresh is disabled.
Grok2API does not send that token, so leave it empty for a direct connection
or have a trusted reverse proxy add authorization.

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `CATCH_TARGET_URL` | `https://grok.com/` | Grok page to inspect |
| `CATCH_REFRESH_INTERVAL_MS` | `600000` | Successful refresh interval |
| `CATCH_RETRY_INTERVAL_MS` | `15000` | Retry interval after failure |
| `CATCH_FLARESOLVERR_TIMEOUT_MS` | `90000` | Cloudflare solve timeout |
| `CATCH_BROWSER_TIMEOUT_MS` | `60000` | Per-page path capture timeout |
| `CATCH_PAGE_SETTLE_MS` | `5000` | Time to let the runtime seed/style pair settle |
| `CATCH_PROXY_URL` | empty | Shared HTTP(S)/SOCKS5 egress for FlareSolverr and Chromium |
| `CATCH_API_TOKEN` | empty | Optional API Bearer token |

The default Compose shared-memory size is 128 MiB. It can be adjusted with
`SEED_HEX_CATCH_SHM_SIZE` when required by the host's Chromium build.

The collector automatically follows SVG path and seed rotations because it
reads both from the live browser runtime. A change to the surrounding runtime
structure can still require a software update.
