# 当前 Grok 的 sign、seed 和 HEX（2026-11-16）

2026-11-17 已实现默认禁用旧签名，主程序适配器无 signer/material 完成 Fast `HI`。
实现与验收见本文末尾的后续记录；下文保留 11 月 16 日调查时的行为和证据。

本次线上检查确认：**Fast 聊天、Fast/Auto 额度查询、Imagine 配额查询，可以不带
`x-statsig-id` 正常完成。** 当前加载的前端请求中间件生成 `x-xai-request-id`
随机 UUID；检查范围内没有发现旧 Statsig 签名代码或签名材料。

`seed-hex-catch` 的 Cookie 导入问题已修正，但**真实 seed/HEX 完整采集仍未通过**。
当前页面没有产生它需要捕获的旧签名请求。之前成功的 WebSocket `HI` 和本次成功的
REST `HI`，均属于无 Statsig 的聊天验证，不能算作“采集材料 → `/sign` → 聊天”的验收。

脱敏证据见 [sign-2026-11-16.json](research/sign-2026-11-16.json)。测试使用同一测试出口和
用户授权的 SSO；额度对照在同一个已登录浏览器上下文内完成。证据不包含 SSO、Cookie 值或用户 ID。

## 已验证的接口可以不带旧签名工作

| 接口/操作 | 不带签名 | 错误字符串签名 | 仓库旧材料生成的签名 | 结果含义 |
| --- | --- | --- | --- | --- |
| `POST /rest/rate-limits`，`modelName=fast` | 200 | 200 | 200 | 三组均返回剩余 29、总额 30 |
| `POST /rest/rate-limits`，`modelName=auto` | 200 | 200 | 200 | 三组均返回剩余 7、总额 7 |
| `POST /rest/media/imagine/quota_info`，`{}` | 200 | 200 | 200 | 三组配额 JSON 完全相同 |
| `POST /rest/app-chat/conversations/new`，Fast，`HI` | 200，完整回复 | 未发送 | 未发送 | 无签名可以完成真实推理 |
| `/ws/mgw/`，Fast，`HI` | 前序测试已完成回复 | 未发送 | 未发送 | 主程序聊天入口本身也未调用签名器 |

REST `HI` 使用 `modelName=grok-3`、`modelMode=MODEL_MODE_FAST` 和临时会话，返回
21 条 JSON 记录，其中包含最终 `modelResponse`，回答为：

> Hi! How can I help you today?

请求实际发送的头中没有 `x-statsig-id`，有 `x-xai-request-id`。本次没有生成图片或视频，
也没有修改生日、内容偏好或服务条款状态；这些操作的服务端校验条件仍需对应操作的有效请求确认。
三组对照中的“旧材料签名”来自仓库内置常量，未被标记为新采集材料。

## 当前前端使用 UUID 请求标识

2026-11-16 09:54 UTC 开始的已登录页面检查，共读取 464 个已加载 JS 资源，
31,340,134 字节，其中 444 个来自 `cdn.grok.com`。检查包括首页和 `/imagine`
入口返回的页面；后者在客户端跳回 `/`，所以这里不把它算作完成媒体操作的证据。

原始 HTML 和运行后的 DOM 中均没有 `grok-site-verification`、
`twitter-site-verification` 或 `loading-x-anim-` SVG。全部已读取 JS 中，
`botoxSign`、`obfiowerehiring`、`x-statsig-id`、`grok-site-verification`、
`loading-x-anim-` 的匹配数均为 0。观察到的 57 个同源 API 请求中，
41 个带 `x-xai-request-id`，没有自然产生的 `x-statsig-id` 请求。

当前公共脚本中的请求中间件逻辑为：

```javascript
// 省略压缩变量名及错误处理；原始片段保存在证据 JSON 中。
headers["x-xai-request-id"] = crypto.randomUUID();
addActingOrganizationHeader(headers);
fetch(url, { ...init, credentials: "include", headers });
```

公共 API 客户端的 `pre` 中间件添加随机 UUID 和适用的组织 ID。
下层 `fetchAndRecord4xx5xx` 负责错误记录，并在明确的 Cloudflare challenge 响应后触发
Turnstile；再下层调用 `globalThis.fetch`。这条可检查的调用链没有旧 seed/HEX 计算。
`signedTeamsFetch` 这个现存函数名也采用上述 UUID 逻辑，函数名本身不能证明在做 Statsig 签名。

关键源文件：

- [请求中间件及额度 API](https://cdn.grok.com/_next/static/chunks/2s2ilh32gw41w.js)：模块 `2569170`、`6224142` 等。
- [组织请求头](https://cdn.grok.com/_next/static/chunks/2s2ilh32gw41w.js)：模块 `981022`，添加 `x-acting-organization-id`。
- [错误记录及 Cloudflare 处理](https://cdn.grok.com/_next/static/chunks/42a21lpkdlv24.js)：模块 `112524`。
- [底层 fetch](https://cdn.grok.com/_next/static/chunks/0j8t7vab2mmt6.js)：模块 `572671`。
- [Gateway 地址](https://cdn.grok.com/_next/static/chunks/2ue3e31_kabcx.js)：模块 `994814`，`/ws/mgw/?uid=...`。
- [REST 聊天客户端](https://cdn.grok.com/_next/static/chunks/217fe-29o2dz7.js)：`chatCreateConversationAndRespondRaw`。

这些结果支持“本次账号、出口和线上前端路径已不依赖旧签名”。其他账号分组、未加载页面或
未测试接口的行为，应以其实际请求为准。SSO 登录态和 Cloudflare 校验仍是独立条件。

## 仓库中的旧 seed/HEX 算法仍能生成值

当前仓库的实现位于 [statsig.mjs](src/statsig.mjs)、[hex.mjs](src/hex.mjs) 和
[statsig_local.go](../backend/internal/infra/provider/web/statsig_local.go)。

- **seed**：48 字节公开页面材料，旧流程可从 verification meta 或签名载荷取得；
  当前采集器通过解码真实签名提取。它与 SSO 无关，也没有作为 HMAC 密钥参与下面的摘要。
- **HEX**：旧动画元素的 RGB 和 CSS transform matrix 计算出的十六进制样式指纹。
  仓库实现把各值保留两位小数，转十六进制、拼接并去掉点号及负号。
- **sign**：服务接口，把材料、HTTP 方法、pathname 和当前时间编码成 `x-statsig-id`。

默认的 70 字节格式为：

```text
t = floor(unixSeconds) - 1682924400
digest = SHA256(METHOD + "!" + pathname + "!" + t + "obfiowerehiring" + HEX)[0:16]
raw = randomByte || XOR(randomByte, seed[48] || t[4, little-endian] || digest[16] || 0x03)
x-statsig-id = Base64(raw)，去掉尾部 =
```

仓库还支持短前缀、16–32 字节摘要及无 marker 变体。这个算法不包含请求体和 SSO。
旧常量可以继续生成格式正确的值；接口返回 200 也不能证明服务端使用该值校验了请求。

## 主程序仍有旧签名调用，采集服务仍缺真实材料

| 位置 | 当前行为 | 对本次问题的影响 |
| --- | --- | --- |
| [chat.go](../backend/internal/infra/provider/web/chat.go)，`openChat` | 直接进入 `openGatewayChat` | Fast 主聊天路径不调用 `/sign` |
| [gateway.go](../backend/internal/infra/provider/web/gateway.go)，`gatewayHeaders` | SSO/CF Cookie、`x-userid` Cookie、Origin、UA | 没有 Statsig 头 |
| [quota.go](../backend/internal/infra/provider/web/quota.go) | 两类额度请求仍调用 `applySignedStatsig` | 本次实测额度可无签名完成 |
| [image.go](../backend/internal/infra/provider/web/image.go)、[video.go](../backend/internal/infra/provider/web/video.go) | 图片编辑、视频等 REST 调用仍经过签名公共函数 | 当前前端仍有对应 REST 客户端；具体媒体生成未实测 |
| [account_settings.go](../backend/internal/infra/provider/web/account_settings.go) | 若干写操作仍请求签名 | 本次未改变用户资料，未验证这些写操作 |
| [statsig.go](../backend/internal/infra/provider/web/statsig.go)，URL 模式 | 调用任意 `/sign` 前先获取 verification meta | 当前 `/index` 为 404，首页无 meta，会在调用签名服务前失败 |
| 同文件 `applySignedStatsig` | 自动签名失败后记录日志，继续发送无签名请求 | 签名警告与业务成功可以同时发生 |
| [statsig_local.go](../backend/internal/infra/provider/web/statsig_local.go)，Local 模式 | 远端材料不可用时回退内置旧材料 | Local 模式成功不等于新材料采集成功 |
| [app.mjs](src/app.mjs)，`/sign`、`/material` | 没有完整材料时返回 503 | 真实采集未完成时维持未就绪状态 |

另一个需要修正的验收判断是
`TestLiveStatsigAcceptedByGrok`：它通过额度返回成功和聊天回复成功来判断签名“被接受”。
本次对照已经表明，这两种成功都可在没有 Statsig 的情况下发生，所以该测试不能独立证明
真实材料已采集或上游实际校验了签名。

## 建议按已验证的协议调整依赖

Fast 聊天应以 SSO 身份、Gateway 连接和完整回复验收；额度查询已有无签名的有效对照。
建议让这些已验证路径不再依赖旧 Statsig 预热及 meta 获取，并保留显式的兼容配置供其他接口使用。
媒体和账号写操作的兼容调整，需要其有效请求的独立证据。

旧 seed/HEX 采集的完成条件仍是：获得浏览器实际产生的 `x-statsig-id`，提取同一请求的
材料并验证摘要，然后证明 `/sign` 使用该材料。当前线上检查没有获得这份材料，
所以“完整采集 → `/sign` → HI”这项验收保持未通过。本次研究没有删除或关闭主程序的签名配置。

Cookie 导入、启动重试和 Turbopack 捕获相关修改的本地检查为 23 项通过、0 项跳过，
包含真实 Chromium 运行的协议夹具；线上材料采集的结果单独以上述证据判断。

## 2026-11-17：主程序默认跳过旧签名，并通过实际 Fast 聊天

主程序新增 `disabled` 模式，作为新配置的默认值。该模式不添加 `x-statsig-id`，
跳过 verification meta、签名与 Material 服务、周期预热和签名失效重试；就绪状态将
Statsig 标为 `disabled`，不再因缺少旧签名而降级 Web。SSO 和 Cloudflare Clearance 仍按原配置使用。

已保存的 URL、Local、手动模式继续生效。已有部署在管理端选择
**设置 → Grok Web → 旧签名兼容 → 禁用（推荐）** 并保存，即可热切换；原手动值和服务地址保留，
可切回兼容模式。组合部署默认只启动主程序、WARP 和 FlareSolverr，旧签名器使用
`--profile legacy-statsig` 显式启动。

在线验收使用主程序的 Go Web 适配器，测试环境只启动 FlareSolverr，未启动 signer 或
Material 服务。`TestLiveWebQuotaAndFastChat` 用临时标准输入接收授权 SSO：

- `SyncQuotaMode(..., "auto")` 成功，剩余额度 7、总额 7。
- `ForwardResponse` 使用 `grok-chat-fast` 发送 `HI`，返回 HTTP 200 和完整回答：
  `Hi! How can I help you today?`

首次运行在 `/api/auth/session` 收到 403；随后两种请求头的 Session 诊断均返回 200，
相同主程序测试在未修改代码的情况下通过。首次 403 的具体原因尚未确定，仍属于需要关注的上游稳定性现象。
脱敏结果见 [signing-disabled-2026-11-17.json](research/signing-disabled-2026-11-17.json)。

原 `TestLiveStatsigAcceptedByGrok` 已更名为上述测试，按实际额度和聊天行为验收。
旧 seed/HEX 的真实完整采集仍未通过；本次修改让主程序在没有这份材料时按已验证的流程运行。
