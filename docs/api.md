# API 参考

网关对外只暴露 OpenAI 兼容接口和几个运维端点。客户端把 Base URL 设成 `http://<主机>:7863/v1`、API Key 填 `api_key` 即可。

- [端点](#端点)
- [模型名](#模型名)
- [健康检查](#健康检查)
- [请求日志](#请求日志)
- [上游接口](#上游接口)

## 端点

| 端点 | 鉴权 | 说明 |
|---|---|---|
| `POST /v1/chat/completions` | Bearer | OpenAI 兼容对话补全，支持流式与非流式、工具调用、图片输入、推理参数。网关不限制请求体大小，超限由上游返回 |
| `GET /v1/models` | Bearer | 模型列表，见下节 |
| `GET /status` | Bearer | 账号状态汇总与每个账号的详情（积分、冷却、熔断、在途、粘性），以及按「域 + 模型」聚合的限额视图 `model_locks` |
| `GET /healthz` | 无 | 健康检查：有可用账号返回 200，否则 503 |
| `GET /readyz` | 无 | 就绪检查：平时 200，收到停止信号后 503 |
| `/panel/` | 面板内输入 | Web 管理面板；接口见[面板接口](panel.md#面板接口) |

鉴权规则：`api_key` 非空时校验 `Authorization: Bearer <api_key>`；`api_key` 为空时全部放行。`/healthz`、`/readyz` 始终不鉴权。每个响应都带 `X-Request-Id`。

## 模型名

`/v1/models` 从上游动态拉取，缓存 1 小时（失败时返回空列表并缓存 5 分钟）。每个模型除了 `id`，还带上游给出的积分倍率、`context_length`、`max_output_tokens`、支持的思考档位与默认档位、能否关闭思考、是否支持工具与视觉等字段。

| 写法 | 去向 |
|---|---|
| `cn:glm-5.2` | 只在国内版账号里选 |
| `global:gpt-5.5` | 只在国际版账号里选 |
| `glm-5.2` | 默认走国内版；打开 `pool.cross_realm` 后按两域模型目录合池，见[跨域负载均衡](gateway.md#跨域负载均衡) |

## 健康检查

```bash
curl -s http://127.0.0.1:7863/healthz
```

```json
{"healthy": 2, "total": 3, "service": "workbuddy2api", "realm_servable": {"cn": true, "global": false}}
```

- 200 和 503 的结构相同，只是状态码与计数不同。账号全部在途占满时也返回 503，与对话接口能否受理保持一致。
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

日志里不出现任何 token、`api_key` 明文，也不记录提示词和响应内容。请求元数据另外归档到 `data/request-logs/`，可在面板「运行日志」里查询。

## 上游接口

上游均为官方客户端使用的**非公开接口**，没有公开文档，路径与 Host 以代码里的常量为准：

- **国内版**：`copilot.tencent.com`（对话、token 刷新、OAuth、模型列表、成长体系），`www.codebuddy.cn`（签到、余额、活跃上报）
- **国际版**：`www.workbuddy.ai`（可用 `global.chat_base` / `global.billing_base` 覆盖）

| 相对路径 | 方法 | 用途 |
|---|---|---|
| `chat/completions` | POST | 对话补全（SSE） |
| `console/enterprises/personal/models` | GET | 模型列表 |
| `plugin/auth/token/refresh` | POST | 刷新 token |
| `plugin/auth/state` · `plugin/auth/token` · `plugin/login/account` | POST / GET | OAuth 登录 |
| `billing/meter/daily-checkin` | POST | 每日签到 |
| `billing/meter/get-user-resource` | POST | 余额查询 |
| `billing/meter/get-enterprise-user-usage` | POST | 企业版额度查询 |
| `report` | POST | 行为与活跃上报 |
| `activity/growth/buddy/*` | GET / POST | 猫猫旅行：协议、领养、状态、派出、领奖 |
| `activity/growth/streak` · `redeem` | GET / POST | 连登天数、档位兑换 |
| `activity/growth/lottery/summary` · `draw` | GET / POST | 抽奖次数、抽奖 |
| `activity/growth/tasks` · `tasks/accept` · `tasks/<code>/claim` | GET / POST | 成长任务列表、报名、领奖（领奖走 Web 域） |

聊天请求永远不携带 refresh token，该头只出现在 token 刷新请求里。
