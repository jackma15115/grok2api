# seed-hex-catch

[English](README.md) | 简体中文

`seed-hex-catch` 会定期观察 Grok Statsig 模块使用的瞬时 SVG path，读取运行时
实际选用的 seed 和浏览器已计算的动画样式，独立编码 HEX，并通过 JSON 接口提供材料。它不会捕获
`x-statsig-id` 请求，也不会 hook Web Crypto 的 SHA 输入。

发布镜像在同一个容器内包含 FlareSolverr、Chromium、Playwright 和采集器。
服务启动时立即刷新，之后默认每 10 分钟刷新一次。每轮只在全新的浏览器上下文中
打开一个自然页面，将 seed、实际选中的 SVG path 和 computed style 配对后关闭浏览器。

## 启动

使用一条命令直接运行发布镜像：

```bash
docker run -d --name seed-hex-catch --restart unless-stopped --init --security-opt no-new-privileges:true --shm-size 128m -p 8789:8789 ghcr.io/jackma15115/grok2api-seed-hex-catch:latest
```

或者使用 Compose：

```bash
docker compose -f docker-compose.seed-hex-catch.yml up -d
```

Material 接口为 `http://HOST:8789/material`。在 Grok2API 中选择 Statsig
`Local` 模式，并将 Material 服务 URL 填写为：

```text
http://seed-hex-catch:8789/material
```

不同 Compose project 默认不共享 DNS 网络。可以填写宿主机地址，或者将两个
服务接入同一个外部 Docker 网络。

## API

- `GET /healthz` 返回就绪状态、时间、path 数量和 path 版本。
- `GET /material` 返回 `seed`、`hex`、刷新时间和 path 元数据。
- `POST /refresh` 在设置 `CATCH_API_TOKEN` 后立即触发一次采集。

`CATCH_API_TOKEN` 使用 Bearer Token 保护 `/material` 并启用 `/refresh`。
留空时 `/material` 公开可读，手动刷新接口禁用。Grok2API 当前不会发送该 Token，
因此直连时应留空，或者由可信反向代理补充认证。

## 配置

| 变量 | 默认值 | 用途 |
| --- | --- | --- |
| `CATCH_TARGET_URL` | `https://grok.com/` | 要检查的 Grok 页面 |
| `CATCH_REFRESH_INTERVAL_MS` | `600000` | 成功后的刷新周期 |
| `CATCH_RETRY_INTERVAL_MS` | `15000` | 失败后的重试周期 |
| `CATCH_FLARESOLVERR_TIMEOUT_MS` | `90000` | Cloudflare 求解超时 |
| `CATCH_BROWSER_TIMEOUT_MS` | `60000` | 单个页面的 path 捕获超时 |
| `CATCH_PAGE_SETTLE_MS` | `5000` | 等待运行时 seed/style 配对稳定的时间 |
| `CATCH_PROXY_URL` | 空 | FlareSolverr 与 Chromium 共用的 HTTP(S)/SOCKS5 出口 |
| `CATCH_API_TOKEN` | 空 | 可选的 API Bearer Token |

Compose 默认提供 128 MiB 共享内存。如果宿主机 Chromium 版本需要更多空间，
可通过 `SEED_HEX_CATCH_SHM_SIZE` 调整。

采集器从浏览器运行时读取 path 和 seed，因此可以自动跟随两者轮换。如果 Grok
修改外围运行时结构，仍可能需要升级程序。
