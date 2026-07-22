# 自托管 Statsig 签名器

[English](README.md) | 简体中文

该服务是 `https://grok.wodf.de/sign` 的 Playwright 自托管替代方案。它会在校准时打开真实 Grok 页面，捕获浏览器生成的 `x-statsig-id`，逐字节验证其中的 seed 和 SHA 输入，再使用验证后的材料在 Node 中快速完成后续签名。

浏览器不会为每个 `/sign` 请求重复启动。服务默认在启动时校准，此后每 30 分钟刷新一次。如果后续刷新失败，最近一次验证通过的签名材料仍可继续使用。

## API

`POST /sign` 接受与 Grok2API 相同的请求格式：

```json
{
  "method": "POST",
  "path": "/rest/app-chat/conversations/new",
  "environment": { "metaContent": "可选兼容字段" }
}
```

成功响应：

```json
{ "x-statsig-id": "..." }
```

`GET /healthz` 返回就绪状态，但不会暴露 seed 或 HEX。`POST /refresh` 会强制执行一次浏览器重新校准。在获得至少一组验证通过的材料前，服务返回 `503`；配置了 `SIGNER_FALLBACK_SEED` 和 `SIGNER_FALLBACK_HEX` 时除外。

## Compose

在仓库根目录启动 Grok2API、WARP、FlareSolverr 和 signer 全量版本：

```powershell
docker compose -f docker-compose.all.yml up -d
```

不启动 Grok2API，仅将 signer 作为公共服务运行：

```powershell
docker compose -f docker-compose.statsig-signer.yml up -d
```

独立服务地址为 `http://HOST:8787/sign`。默认镜像是 `ghcr.io/jackma15115/grok2api-statsig-signer:latest`，可以通过 `STATSIG_SIGNER_IMAGE` 使用其他仓库或固定版本标签。

全量 Compose 不会向宿主机发布 signer 端口。独立 Compose 会发布 `8787` 端口；公网部署时应配置反向代理、限流，并尽可能使用 IP 白名单。

在 Grok2API 管理端选择 Statsig `URL` 模式。全量 Compose 内部地址为：

```text
http://statsig-signer:8787/sign
```

## 校准环境变量

| 变量 | 默认值 | 用途 |
| --- | --- | --- |
| `SIGNER_TARGET_URL` | `https://grok.com/` | 用于校准的页面 |
| `SIGNER_FLARESOLVERR_URL` | Compose 外为空 | 启动 Playwright 前解决 Cloudflare challenge |
| `SIGNER_FLARESOLVERR_TIMEOUT_MS` | `60000` | FlareSolverr 求解超时 |
| `SIGNER_PROBE_PATH` | `/rest/rate-limits` | 用于触发页面拦截器的探测请求 |
| `SIGNER_COOKIE` | 空 | 可选的浏览器 Cloudflare Cookie |
| `SIGNER_USER_AGENT` | Playwright 默认值 | 必须与 Cookie 获取环境匹配 |
| `SIGNER_PROXY_URL` | 空 | 可选的 HTTP(S) 或 SOCKS5 浏览器出口 |
| `SIGNER_BROWSER_EXECUTABLE_PATH` | 内置 Chromium | 可选的宿主机 Chrome 或 Edge 路径 |
| `SIGNER_REFRESH_INTERVAL_MS` | `1800000` | 重新校准间隔 |
| `SIGNER_PAGE_SETTLE_MS` | `5000` | 等待页面脚本或浏览器 challenge 的时间 |
| `SIGNER_API_TOKEN` | 空 | `/sign` 和 `/refresh` 的可选 Bearer Token |

当前 Grok2API 的 URL signer 客户端不会发送 Authorization 请求头，因此直接连接时应保持 `SIGNER_API_TOKEN` 为空。只有在客户端或反向代理能够添加 Bearer Token 时才启用它。

配置 FlareSolverr 后，signer 会先使用相同的 `SIGNER_PROXY_URL` 请求 Grok 页面，再把获得的 Cloudflare Cookie 和 User-Agent 注入 Playwright。FlareSolverr 无法执行 Statsig SHA hook，因此最终捕获仍必须由 Playwright 完成。如果两个容器的公网出口不一致，Cloudflare 可能拒绝转移后的 clearance。

校准失败时 `/healthz` 保持 `503`。服务不会把随机的 70 字节值当作有效签名材料。

## 测试

纯签名与 HTTP 兼容测试不需要浏览器：

```powershell
npm test
```

使用宿主机浏览器运行 Playwright 捕获集成测试：

```powershell
$env:SIGNER_TEST_BROWSER = "C:\Program Files\Google\Chrome\Application\chrome.exe"
node --test test/calibrator.integration.test.mjs
```
