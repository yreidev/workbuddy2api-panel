# API 参考

网关对外暴露 OpenAI 兼容接口、给 Claude Code 用的 Anthropic Messages 接口、给 Codex 用的 Responses 接口，以及几个运维端点。OpenAI 兼容客户端把 Base URL 设成 `http://<主机>:7863/v1`、API Key 填 `api_key` 或面板里建的[客户端密钥](#客户端密钥)即可；Claude Code、Codex 的接法见 README 的[接入客户端](../README.md#接入客户端)。

- [端点](#端点)
- [客户端密钥](#客户端密钥)
- [Claude Code 与 Codex 入口](#claude-code-与-codex-入口)
- [联网搜索](#联网搜索)
- [模型名](#模型名)
- [模型路由](#模型路由)
- [健康检查](#健康检查)
- [请求日志](#请求日志)
- [上游接口](#上游接口)

## 端点

| 端点 | 鉴权 | 说明 |
|---|---|---|
| `POST /v1/chat/completions` | Bearer | OpenAI 兼容对话补全，支持流式与非流式、工具调用、图片输入、推理参数。网关不限制请求体大小，超限由上游返回 |
| `POST /v1/messages` | Bearer 或 `x-api-key` | Anthropic Messages 格式（Claude Code），见[下节](#claude-code-与-codex-入口) |
| `POST /v1/messages/count_tokens` | Bearer 或 `x-api-key` | 按字数估算输入 token，不发上游 |
| `POST /v1/responses` | Bearer 或 `x-api-key` | OpenAI Responses 格式（Codex） |
| `POST /v1/search/messages` | Bearer 或 `x-api-key` | 联网搜索，Anthropic Messages 形状，见[联网搜索](#联网搜索) |
| `POST /mcp` | Bearer 或 `x-api-key` | MCP 服务器，只有一个工具 `web_search`，给 Codex 这类客户端联网搜索，见[联网搜索](#联网搜索) |
| `GET /v1/models` | Bearer | 模型列表，见[模型名](#模型名) |
| `GET /status` | Bearer | 账号状态汇总与每个账号的详情（积分、冷却、熔断、在途、粘性），以及按「域 + 模型」聚合的限额视图 `model_locks` |
| `GET /healthz` | 无 | 健康检查：有可用账号返回 200，否则 503 |
| `GET /readyz` | 无 | 就绪检查：平时 200，收到停止信号后 503 |
| `/panel/` | 面板内输入 | Web 管理面板；接口见[面板接口](panel.md#面板接口) |

Base URL 多写了 `/v1`、请求成了 `/v1/v1/...` 时，网关不校验密钥、不打上游，直接回 400 说明怎么改（Claude Code 的路径按 Anthropic 格式，其余按 OpenAI 格式）。不回 404 是因为 Claude Code 收到 404 一律提示模型不存在，看不到原因。

鉴权规则：

- `/v1/*` 和 `/mcp` 认管理员密钥（`api_key`）和[客户端密钥](#客户端密钥)，放在 `Authorization: Bearer <密钥>`；除了 `/v1/chat/completions` 和 `/v1/models`，这些端点在没有 `Authorization` 头时也认 `x-api-key: <密钥>`（Claude Code 用 `ANTHROPIC_API_KEY` 时发这个头）。
- `/status` 和面板只认管理员密钥。
- `api_key` 为空、也没建过客户端密钥时全部放行；只要建过客户端密钥，`/v1/*` 和 `/mcp` 就必须带有效密钥，`api_key` 为空也一样（`/status` 和面板仍然放行）。
- `/healthz`、`/readyz` 始终不鉴权。每个响应都带 `X-Request-Id`。

## 客户端密钥

给别人用时在面板「密钥」页建客户端密钥，不用把 `api_key` 发出去。每个密钥可以设：

- **启用开关和到期时间**：停用、过期、删掉的密钥回 401，`error.code` 分别是 `key_disabled`、`key_expired`、`key_deleted`，`message` 说明是哪个密钥、哪天到期；
- **每天和总量的用量上限**：请求次数、token、积分三项，不填 = 不限。每天的上限按北京时间 00:00 重置；
- **模型白名单**：只认 `*` 通配、不区分大小写，按经[模型路由](#模型路由)改写后实际发给上游的模型名匹配，不带 `cn:` / `global:` 前缀。比如 Claude Code 发 `claude-sonnet-4-5`、被路由改成 `glm-5.2`，白名单里要有 `glm-5.2`（或 `glm-*`）。不在白名单里回 403 `model_not_allowed`（Claude Code 入口是 `permission_error`），`GET /v1/models` 也只列白名单放行的模型；
- **账号分组**：勾了分组（面板账号池页下方建）就只用这些组里的号，组里的号都接不了时回 503，不借别的号；不勾 = 不限。见[选号策略](gateway.md#选号策略)。

到上限时回 429 `key_quota_exceeded`，带 `X-Should-Retry: false`（Anthropic、OpenAI 的 SDK 看到它不自动重试）；每天的上限另带 `Retry-After`，是到北京时间 00:00 的秒数。计数口径：

- 请求次数按客户端请求计，换号重试不重复计。放行时先计上，最后失败的（没号可用、上游报错）再退还，所以只算成功的请求，号池出问题时客户端的自动重试不会把次数烧光；客户端中途断开的照算；
- token、积分在上游回复后计，上游没回积分的请求不计积分。并发请求在放行时还没计入 token 和积分，可能略超上限；
- 管理员密钥不受这些限制。

用量统计和请求记录都按密钥区分：用量里客户端密钥记它的 id、管理员密钥记 `admin`，没开鉴权和 1.3.0 之前的数据不区分；请求记录的 `api_key` 是密钥名，管理员密钥记「管理员」。面板上的[测活](panel.md#测活)两边都单独记：用量里记 `liveness`，请求记录里记「测活」。密钥连同原文和计数存在数据目录的 `api_keys.json`（`0600`），用量计数每 10 秒写一次盘，停机时再写一次。

## Claude Code 与 Codex 入口

`/v1/messages` 和 `/v1/responses` 先把请求翻译成 OpenAI 对话格式，再走和 `/v1/chat/completions` 完全相同的流程：选号、换号重试、会话粘性、出站请求对齐、用量与请求日志都一样；上游的回复再翻译回客户端的格式，流式逐帧转换。请求日志里这两类请求的路径照实记成 `/v1/messages`、`/v1/responses`。

协议翻译直接用 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 的翻译层（`sdk/translator`），实际效果：

| 客户端发的 | 到上游变成 |
|---|---|
| Claude Code 的 `thinking`（含 `adaptive`）与 `output_config.effort` | `reasoning_effort`；超出模型支持范围时网关再按模型目录降级 |
| Claude Code 的 `cache_control`、`metadata.user_id`、`context_management`、思考块 | 丢掉（上游是 OpenAI 格式，没有对应字段） |
| Codex 的 `store`、`include`、`prompt_cache_key`、内置 `web_search` 工具 | 丢掉（Codex 要联网搜索就接网关的 [`/mcp`](#联网搜索)） |
| Codex 的自定义工具 `apply_patch` | 函数工具（参数 `input` 是字符串），回复里再还原成 `custom_tool_call` |
| `developer` 角色 | `user` 角色 |

错误按客户端协议返回：Claude Code 收到 `{"type":"error","error":{...}}`（状态码映射到 `invalid_request_error`、`authentication_error`、`rate_limit_error`、`overloaded_error` 等），Codex 收到 OpenAI 格式；流开始后的上游错误变成 Claude 的 `event: error` 或 Responses 的 `response.failed`。

模型名不在上游目录里、也没有[路由规则](#模型路由)命中时，直接返回 404 并说明该设哪个变量，不拿每个号去试。目录还没拉到时放行，交给上游判断。

`/v1/messages/count_tokens` 只做估算：只数 `system`、`messages`、`tools` 里的文本，CJK 字符每字 1 个 token，其余每 4 字节 1 个。Claude Code 用它显示上下文占用，不影响计费。

## 联网搜索

网关用官方的联网搜索接口（桌面端内置 CLI 的 WebSearch 工具用的那个，请求形状见[官方客户端对照](official-client.md#联网搜索)）替客户端搜，一次最多 5 条结果。有三个入口：

- **Claude Code 的 WebSearch**：不用配。Claude Code 调 WebSearch 时另发一个 `/v1/messages` 子请求，声明 Anthropic 的服务端工具 `web_search_20250305`，用户消息是 `Perform a web search for the query: <搜索词>`，原本由 Anthropic 的模型调这个工具搜。上游的模型没有这个工具，网关认出这种子请求就不交给模型，直接搜，按 Anthropic 服务端搜索的形状回（`server_tool_use`、`web_search_tool_result`、`text` 三块，流式、非流式都支持），WebSearch 的 `allowed_domains`、`blocked_domains` 照传。只有同时满足「声明了 `web_search_` 开头的服务端工具」和「最后一条用户消息以那句话开头」才拦，客户端自己定义的 `web_search` 函数工具照常交给模型。2026-10-31 用 Claude Code 2.1.314 实测可用；
- **`POST /v1/search/messages`**：同样是 Anthropic Messages 的形状，给直接调搜索的客户端（照原版保留）。`tools` 里要声明搜索工具（`type` 以 `web_search_` 开头，或 `type`、`name` 是 `web_search`），没声明回 400；搜索词取最后一条用户消息，带上面那句前缀的去掉；
- **`/mcp`**：MCP 服务器（Streamable HTTP，无状态，回应直接是 JSON），只有一个工具 `web_search`：入参 `query`（至少 2 个字）、`allowed_domains`、`blocked_domains`、`freshness`（`d`、`d1`～`d30`、`m`、`m1`～`m12`、`y`、`y1`～`y5`，同官方），返回照官方 WebSearch 的写法列出的 Markdown 文字。给 Codex 这类能接 MCP 服务器的客户端用，接法见 README 的 [Codex](../README.md#codex)。只收 POST，GET、DELETE 回 405。2026-10-31 用 Codex 0.166.0 实测可用，模型看到的工具名是 `mcp__<服务器名>__web_search`。

三个入口的规则相同：

- 选号、在途名额（满了照样排队）、刷新令牌、换号重试和对话一样，只是不看模型。在哪个域选号：Claude Code 子请求的模型带 `cn:` / `global:` 前缀时照前缀，否则同不带前缀的对话，按 `pool.cross_realm`；
- 没搜成时：余额不足、限流、登录失效、封号、WAF 拦截这几类和对话一样罚号再换号；上游故障、404 和认不出的错误只换号，不罚号；请求本身有问题（`11101`、`11128` 这类）回 400，上游满了（`6021`、`6020`）回 503，上游超时回 503，这三种都不换号；
- Claude Code 和 `/v1/search/messages` 的失败回应带 `X-Should-Retry: false`：Claude Code 遇到 5xx、429 默认会自动重试好几次，搜索失败就直接交给主模型，要不要再搜由它定。`/mcp` 的失败是工具结果里的 `isError`，文字以 `web search failed:` 开头；
- 客户端密钥：每搜一次计一次请求，不看模型白名单，没搜成的退还，不计 token 和积分；
- 搜索不进用量统计（没有 token）。请求记录照记：路径照实（Claude Code 的子请求记 `/v1/messages`），模型一列写 `web_search`（国际版的号写 `global:web_search`）；`/mcp` 只有调了搜索才记，握手、列工具不记；
- 会话头：客户端带了 `X-Conversation-ID`、`X-Conversation-Request-ID` 就原样转给上游，没带就各生成一个。Claude Code 的子请求不带这两个头，所以和主对话的会话 id 不关联，上游会不会核对，未确认；
- 搜一次扣不扣积分、扣多少：还没在真实账号上测（未确认）。

## 模型名

`/v1/models` 从上游动态拉取，缓存 1 小时（失败时返回空列表并缓存 5 分钟）。每个模型除了 `id`，还带上游给出的积分倍率、`context_length`、`max_output_tokens`、支持的思考档位与默认档位、能否关闭思考、是否支持工具与视觉等字段。

被[隐藏](#隐藏模型)的模型不列。只在池里企业号自己的目录里出现的模型也会以 `cn:` 前缀列出，并带 `"enterprise_only": true`；请求只派给目录里有它的企业号，见[企业版模型](tasks.md#禁用暂停与企业版账号)。

| 写法 | 去向 |
|---|---|
| `cn:glm-5.2` | 只在国内版账号里选 |
| `global:gpt-5.5` | 只在国际版账号里选 |
| `glm-5.2` | 默认走国内版；`pool.cross_realm` 设成 `merge`（合池）或 `cn_first`（国内版优先）后按两域模型目录分到两个域，见[跨域负载均衡](gateway.md#跨域负载均衡) |

## 模型路由

Claude Code、Codex 不配模型时发的是自家官方模型名（如 Claude Code 的 `claude-sonnet-4-6`），上游目录里多半没有。面板「模型与档位 → 模型路由」可以配一张规则表（存在 `config.json` 的 [`model_routes`](configuration.md#模型路由)），三个对话端点都按它改写：

- 只改上游目录里没有的名字。目录里有的直通，规则写成 `*` 兜底也不会动到它；
- 带 `cn:` / `global:` 前缀的名字是明确指定，不改；目标可以带前缀；
- 从上往下找第一条匹配的规则，只认 `*` 通配，不区分大小写；
- 国内版目录拉不到时判断不了，不改，原样交给上游；
- 命中时日志打一行 `model route <原名> -> <目标>`；目标写错（不在目录里）时，Claude Code / Codex 入口返回 404 并指出是哪条规则。

客户端界面上显示的仍是它自己的模型名，实际用的是规则目标。

## 隐藏模型

面板「模型与档位 → 对外隐藏的模型」可以把模型藏起来（存在 `config.json` 的 [`models.hidden`](configuration.md#隐藏模型)），加一项、删一项都立即生效：

- `/v1/models` 不列；客户端密钥看到的列表在这之上再按它的白名单过滤；
- 点名调用回 400 + `model_unavailable`，不选号、不发上游，三个对话端点都一样；
- 带 `cn:` / `global:` 前缀的只藏那个域，不带前缀的两个域都藏；精确匹配、区分大小写，不认通配；
- 按经模型路由改写后实际发给上游的模型判：规则的目标被藏起来时，命中这条规则的请求也会被拒；
- `pool.cross_realm` 不是 `off` 时，只藏了一个域的模型照样能用不带前缀的名字调，只分到另一个域。

## 健康检查

```bash
curl -s http://127.0.0.1:7863/healthz
```

```json
{"healthy": 2, "total": 3, "service": "workbuddy2api", "realm_servable": {"cn": true, "global": false}}
```

- 200 和 503 的结构相同，只是状态码与计数不同。账号全部在途占满时也返回 503，表示此刻没有号能马上接新请求；对话请求这时会先排队等空位（`pool.in_flight_wait`，默认 30 秒），见[网关与选号](gateway.md#选号策略)。
- `realm_servable` 分别给出国内版、国际版此刻能否受理请求，双域部署可以单独告警。
- 响应带 `X-Service: workbuddy2api` 头。`service` 字段和这个头用于区分本网关与同端口上的其他服务，避免探活误判。
- 需要强校验时用 `/status` + `api_key`：只有持有正确密钥的本网关会返回 200。
- 容器编排的就绪探针请用 `/readyz`。

## 请求日志

每个对话请求结束时，终端输出一行：

```text
| #001 | 18:31:31 | deepseek-v4 | stream | 200 | uid=0851ce35 | TTFB=801ms | tok=60 | 23.5tok/s | total=2.6s | rid=… | out=success | try=1 | credit=0.0123 | src=203.0.113.7 ua="OpenAI/Python" |
```

| 字段 | 含义 |
|---|---|
| `#001` | 进程内请求序号 |
| `18:31:31` | 结束时刻 |
| `deepseek-v4` | 模型名（过长会截断；跨域选号时写成 `域:模型名`） |
| `stream` / `sync` | 流式 / 非流式 |
| `200` | 状态码 |
| `uid=` | 账号 UID 前 8 位 |
| `TTFB` | 首字节耗时 |
| `tok` / `tok/s` / `total` | 输出 token 数 / 速率 / 总耗时 |
| `rid` / `out` / `try` / `credit` | 请求 ID / 结果 / 尝试次数 / 本次扣除积分 |
| `src` / `ua` | 调用来源 IP 与客户端标签（`logging.request_client_info` 关闭时不输出） |

这一行只打在终端（`docker logs` 看得到），面板的「运行日志」不收；同样的信息在面板「请求记录」里，失败的请求还带失败原因（回给客户端的错误消息）；用了[代理池](proxy.md)时请求记录还带号走的代理名，终端这一行不打（没起名字的代理名字就是 IP）。

日志里不出现任何 token、`api_key` 明文，也不记录提示词和响应内容。请求元数据另外归档到 `data/request-logs/`，可在面板「请求记录」里查询。

## 上游接口

上游均为官方客户端使用的**非公开接口**，没有公开文档，路径与 Host 以代码里的常量为准：

- **国内版**：全部发往官方产品入口 `www.workbuddy.cn`（对话、token 刷新、登录、模型列表、积分、签到、成长体系、上报）。1.1.2 以前对话一类走 `copilot.tencent.com`、积分一类走 `www.codebuddy.cn`，见[官方客户端对照](official-client.md#国内版域名)
- **国际版**：`www.workbuddy.ai`（可用 `global.chat_base` / `global.billing_base` 覆盖）

| 相对路径 | 方法 | 用途 |
|---|---|---|
| `chat/completions` | POST | 对话补全（SSE） |
| `/v3/config` | GET | 模型目录（完整路径，不加 `/v2`）；按账号拉，和官方桌面端拉到的是同一份 |
| `console/enterprises/personal/models` | GET | 企业号另拉的企业模型名单，并进 `/v3/config` 的目录；个人号不拉 |
| `plugin/auth/token/refresh` | POST | 刷新 token |
| `plugin/auth/state` · `plugin/auth/token` · `plugin/login/account` | POST / GET | OAuth 登录 |
| `billing/meter/daily-checkin` | POST | 每日签到 |
| `billing/meter/get-user-resource` | POST | 余额查询 |
| `billing/meter/get-enterprise-user-usage` | POST | 企业版额度查询 |
| `/billing/meter/compensation-status` · `/billing/meter/claim-compensation` | POST | 国内版个人账号补偿状态与领取（完整路径，不加 `/v2`） |
| `report` | POST | 行为与活跃上报 |
| `activity/growth/buddy/*` | GET / POST | 猫猫旅行：协议、领养、状态、派出、领奖 |
| `activity/growth/streak` · `redeem` | GET / POST | 连登天数、档位兑换 |
| `activity/growth/lottery/summary` · `draw` | GET / POST | 抽奖次数、抽奖 |
| `activity/growth/tasks` · `tasks/accept` · `tasks/<code>/claim` | GET / POST | 成长任务列表、报名、领奖（领奖走 Web 域） |

聊天请求永远不携带 refresh token，该头只出现在 token 刷新请求里。
