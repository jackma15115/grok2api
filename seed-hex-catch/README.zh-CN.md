# seed-hex-catch

[English](README.md) | 简体中文

`seed-hex-catch` 会定期观察 Grok 真实的 Statsig 请求及其对应的 Web Crypto 摘要输入，
经过校验后提取当前 seed 和 HEX，并使用当前浏览器采集的材料提供与
`grok.wodf.de/sign` 兼容的签名接口。

发布镜像在同一个容器内包含 FlareSolverr、Chromium、Playwright 和采集器。服务启动时立即刷新，
之后默认每 10 分钟刷新一次。每轮在全新的浏览器上下文中打开页面，观察自然产生的
`x-statsig-id` 请求；如果没有自然请求，则发送一个会被拦截的同源探针。请求与 SHA 输入
匹配后关闭浏览器。SVG path 仅保留为可选诊断元数据。

## 启动

使用一条命令直接运行发布镜像：

```bash
docker run -d --name seed-hex-catch --restart unless-stopped --init --security-opt no-new-privileges:true --shm-size 128m -p 8789:8789 ghcr.io/jackma15115/grok2api-seed-hex-catch:latest
```

或者使用 Compose：

```bash
docker compose -f docker-compose.seed-hex-catch.yml up -d
```

Grok2API 支持以下两种接入模式：

- 选择 `URL` 模式，将签名服务 URL 填写为：

```text
http://seed-hex-catch:8789/sign
```

- 选择 `Local` 模式，将 Material 服务 URL 填写为：

```text
http://seed-hex-catch:8789/material
```

`URL` 模式将签名生成留在 `seed-hex-catch`；`Local` 模式获取同一组当前 seed/HEX，
在 Grok2API 内生成。首次成功采集后，当前材料没有过期时间；后续成功采集会原子替换，
刷新失败则继续保留当前材料。

不同 Compose project 默认不共享 DNS 网络。可以填写宿主机地址，或者将两个
服务接入同一个外部 Docker 网络。

## API

- `GET /healthz` 返回就绪状态、时间、path 数量和 path 版本。
- `POST /sign` 接受兼容的 `method`、`path` 和可选 `environment` 请求体，返回
  `{"x-statsig-id":"..."}`。
- `GET /material` 返回当前 `seed`、`hex`、刷新时间和 path 元数据。
- `POST /refresh` 在设置 `CATCH_API_TOKEN` 后立即触发一次采集。

`CATCH_API_TOKEN` 使用 Bearer Token 保护 `/sign` 和 `/material`，并启用 `/refresh`。
留空时 `/sign` 和 `/material` 公开可用，手动刷新接口禁用。Grok2API 当前不会发送该 Token，
因此直连时应留空，或者由可信反向代理补充认证。

## 配置

| 变量 | 默认值 | 用途 |
| --- | --- | --- |
| `CATCH_TARGET_URL` | `https://grok.com/` | 要检查的 Grok 页面 |
| `CATCH_REFRESH_INTERVAL_MS` | `600000` | 成功后的刷新周期 |
| `CATCH_RETRY_INTERVAL_MS` | `15000` | 失败后的重试周期 |
| `CATCH_FLARESOLVERR_TIMEOUT_MS` | `90000` | Cloudflare 求解超时 |
| `CATCH_BROWSER_TIMEOUT_MS` | `60000` | 单个页面的 Statsig 捕获超时 |
| `CATCH_PAGE_SETTLE_MS` | `5000` | 发送探针前等待自然 Statsig 请求的时间 |
| `CATCH_PROBE_PATH` | `/rest/rate-limits` | 同源回退探针路径 |
| `CATCH_PROBE_METHOD` | `POST` | 回退探针方法 |
| `CATCH_PROXY_URL` | 空 | FlareSolverr 与 Chromium 共用的 HTTP(S)/SOCKS5 出口 |
| `CATCH_MAX_BODY_BYTES` | `65536` | `/sign` 请求体大小上限 |
| `CATCH_API_TOKEN` | 空 | 可选的 API Bearer Token |

Compose 默认提供 128 MiB 共享内存。如果宿主机 Chromium 版本需要更多空间，
可通过 `SEED_HEX_CATCH_SHM_SIZE` 调整。

采集器发布 seed/HEX 前会校验捕获的 SHA 前缀，因此无关的浏览器加密调用不会成为签名材料。
如果 Grok 修改 Statsig 请求格式，仍可能需要升级程序。
