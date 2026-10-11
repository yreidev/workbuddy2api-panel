# 配置

配置文件是一个 JSON，默认读取当前目录的 `config.json`（Docker 镜像默认读取 `/app/data/config.json`），可用 `-config <路径>` 指定。文件不存在时程序会自动生成一份推荐配置（`api_key` 为随机值）。完整样例见仓库根目录的 [`config.example.json`](../config.example.json)，里面的密钥都是 `test_key` 之类的占位值。

大多数配置项都能在面板「配置」页直接修改，保存后立即生效；需要重启的字段，面板会在保存后列出来。

- [最常改的几项](#最常改的几项)
- [字段速查](#字段速查)
- [上游超时](#上游超时)
- [环境变量](#环境变量)
- [热生效与写回规则](#热生效与写回规则)

## 最常改的几项

| 字段 | 默认 | 什么时候改 |
|---|---|---|
| `api_key` | 自动生成时为随机值 | 想换成自己的密钥。**为空 = 不鉴权**，暴露到局域网或公网前务必设置 |
| `listen` | `:7863` | 换端口，或只监听本机（`127.0.0.1:7863`） |
| `pool.max_in_flight` | `3` | 单个账号同时处理几个请求 |
| `pool.cross_realm` | `false` | 同时有国内版和国际版账号，想让不带前缀的模型名在两边合池负载均衡 |
| `pool.credit_floor` | `0`（关） | 防止收费模型把余额打穿，样例配置里是 `100` |
| `prompt.mode` | `passthrough` | 客户端的系统提示词频繁被上游内容审核误拦时改成 `custom` |
| `schedule.*_enabled` | 全部 `true` | 关掉不需要的定时任务 |
| `global.enabled` | `true` | 只用国内版、想彻底关掉国际版路由时设 `false` |

## 字段速查

### 基础

| 字段 | 默认 | 说明 |
|---|---|---|
| `listen` | `:7863` | HTTP 监听地址 |
| `api_key` | 空 | 网关与面板共用的鉴权密钥；空 = 不鉴权 |
| `auth_dir` | `./auths` | 账号凭证目录 |
| `state_file` | `./data/state.json` | 账号池状态文件；用量统计、请求归档、模型探测结果都放在同一目录 |
| `server.read_timeout` | `300s` | 读取一个入站请求（含上传请求体）的总时长上限。大上下文请求经反代转发超时会报 400 `read body: i/o timeout`，可调大；`0` = 不限制 |
| `panel.package_detail_limit` | `5` | 积分构成页单个账号默认展开的最早到期积分包数，其余折叠 |

### 日志

| 字段 | 默认 | 说明 |
|---|---|---|
| `logging.request_archive_enabled` | `true` | 请求元数据按天归档为 `requests-<日期>.jsonl`。只记模型、账号、耗时、token 等元数据，**不记提示词、响应正文和 Authorization** |
| `logging.request_retention_days` | `7` | 归档保留天数 |
| `logging.request_archive_max_mb` | `100` | 归档总容量上限（MiB），超出先删最旧的 |
| `logging.request_client_info` | `true` | 是否记录调用来源（客户端 IP 与 User-Agent）。IP 属于个人信息，共享部署可关 |

### 冷却

| 字段 | 默认 | 说明 |
|---|---|---|
| `cooldown.soft_rate` | `600s` | 软限流（429 或限流文案）的冷却基数；同一账号连续触发按 2 倍指数退避 |
| `cooldown.soft_rate_max` | `2h` | 软冷却退避上限 |

### 定时任务

时刻按进程所在时区（容器里由 `TZ` 决定）整点触发。各任务的具体行为见[定时任务与积分任务](tasks.md)。

| 字段 | 默认 | 说明 |
|---|---|---|
| `schedule.checkin_hours` | `[9, 21]` | 签到 + 余额查询，末尾顺带跑连登兑换与抽奖 |
| `schedule.growth_hours` | `[1]` | 成长任务队列：扫描全部账号并执行待办 |
| `schedule.travel_hours` | `[9, 21]` | 猫猫旅行：领养 / 派出 / 领奖 |
| `schedule.activity_hours` | `[10]` | 对话活跃上报（点亮连登、解锁领养） |
| `schedule.keepalive_hours` | `[22]` | 刷新 token 保活 |
| `schedule.blackcat_hours` | `[23]` | 夜猫子任务补足 |
| `schedule.<任务>_enabled` | `true` | 每类任务各自的开关：`checkin` / `growth` / `travel` / `activity` / `keepalive` / `blackcat` |
| `schedule.balance_refresh_enabled` | `true` | 后台周期刷新全部账号余额 |
| `schedule.balance_refresh_minutes` | `5` | 余额刷新间隔（分钟） |
| `schedule.include_disabled_in_tasks` | `false` | 让签到、活跃上报、保活、余额刷新对**已禁用**账号也执行（禁用账号仍不参与选号） |

注意：`*_hours` 写空数组或 `null` 表示「未配置，用默认值」，**不是关闭**；关闭请用对应的 `*_enabled: false`。关闭时小时配置原样保留，改回 `true` 即恢复。小时必须是 0–23，非法值启动即报错。

### 国际版

| 字段 | 默认 | 说明 |
|---|---|---|
| `global.enabled` | `true` | 国际版路由开关。设 `false` 后即使凭证标了国际版也不按国际版路由 |
| `global.chat_base` / `global.billing_base` | 空 | 国际版上游地址覆盖；空 = 内置默认 `https://www.workbuddy.ai` |

### 上游

| 字段 | 默认 | 说明 |
|---|---|---|
| `upstream.timeout_seconds` | `120` | 短请求总时长上限，见[上游超时](#上游超时) |
| `upstream.header_timeout_seconds` | 同 `timeout_seconds` | 聊天请求等到首字节的上限 |
| `upstream.idle_timeout_seconds` | `300` | 聊天流中途无数据的上限 |
| `upstream.user_agent` | 空 | 出站 User-Agent 整体覆盖。空 = 内置的官方桌面端三段式 `WorkBuddy/<client_version> WorkBuddy/<client_version> CLI/<cli_version>` |
| `upstream.client_version` | 空（= `5.5.4`） | UA 与归属头里的客户端版本 |
| `upstream.cli_version` | 空（= `2.137.1`） | UA 里 `CLI/` 段的版本 |
| `upstream.client_name` | 空 | 用量归属头。空 = 只发 `X-Product: SaaS`；配 `WorkBuddy` 则 `X-Product`、`X-IDE-Name`、`X-IDE-Type`、`X-IDE-Version` 都按官方桌面端发送 |
| `upstream.device_token` | 空 | 设备风控头 `X-Device-Token` 的全局兜底值；单个凭证文件里的 `device_token` 优先 |
| `upstream.device_token_file` | 空 | 从文件读取 device token（5 分钟缓存），优先级低于前两者 |
| `upstream.passthrough_ip` | `false` | 是否把客户端 IP 透传给上游 |

### 提示词与脱敏

| 字段 | 默认 | 说明 |
|---|---|---|
| `prompt.mode` | `passthrough` | `passthrough` 透传客户端的系统提示词；`custom` 用网关自带提示词替换；`append` 在客户端提示词之后追加网关提示词。详见[网关与选号](gateway.md#系统提示词与内容审核) |
| `prompt.file` | 空 | 自定义提示词文件；空 = 内置默认（约 2KB）；路径非空但读不到会**启动报错** |
| `features.sanitize_blacklist_fingerprints` | `true` | 清洗出站请求体里会被上游审核误拦的客户端指纹串 |

### 账号池

| 字段 | 默认 | 说明 |
|---|---|---|
| `pool.max_in_flight` | `3` | 国内版单账号最大在途请求数；`0` = 不限 |
| `pool.max_in_flight_global` | `2` | 国际版单账号在途上限（国际版风控更紧） |
| `pool.cross_realm` | `false` | 跨域负载均衡，见[网关与选号](gateway.md#跨域负载均衡) |
| `pool.credit_floor` | `0` | 积分保底：余额低于该值的账号不再接实测收费的模型；`0` = 关闭 |
| `pool.prefer_expiring` | `true` | 有积分快过期的账号选号权重 ×3，优先把快过期的积分用掉 |
| `pool.expiring_soon` | `168h` | 「快过期」的窗口；空或 `0` 关闭 |
| `pool.cost_explore_interval` | `30m` | 免费层垄断时，每个窗口把一个真实请求改道给成本未知的账号，以便学到它的真实成本；`0` = 关闭 |
| `pool.breaker_threshold` | `3` | 连续失败几次触发熔断 |
| `pool.breaker_cooldown` / `pool.breaker_cooldown_max` | `30m` / `6h` | 熔断基础时长与指数退避上限 |
| `pool.degrade_threshold` | `5` | 未知错误连败几次临时出池 |
| `pool.degrade_cooldown` / `pool.degrade_cooldown_max` | `10m` / `2h` | 连败出池时长与上限 |
| `pool.idle_weight_per_hour` / `pool.idle_weight_max` | `0.5` / `5.0` | 闲置补偿：每闲置一小时加的权重与上限 |

### 会话粘性与 Redis

| 字段 | 默认 | 说明 |
|---|---|---|
| `session_sticky.enabled` | `true` | 同一会话尽量固定用同一个账号 |
| `session_sticky.ttl` | `30m` | 会话绑定有效期，每次使用滚动续期 |
| `session_sticky.gc_interval` | `5m` | 过期绑定清理周期 |
| `upstash.url` / `upstash.token` | 空 | Upstash Redis（或任意 `rediss://` 地址）。空 = 纯内存模式，功能不受影响；配置后池状态与会话绑定异步镜像到 Redis |

## 上游超时

三个超时各管一段：

| 字段 | 作用对象 | 默认 | 行为 |
|---|---|---|---|
| `timeout_seconds` | 短请求：token 刷新、签到、余额、模型列表 | `120` | 总时长硬上限，到期报错并按规则换号或熔断 |
| `header_timeout_seconds` | 聊天请求的**首字节前** | `120` | 超时即换号重发 |
| `idle_timeout_seconds` | 聊天流**中途**无数据 | `300` | 只要还在吐数据就不掐；静默超时才断流 |

聊天流（流式和非流式都一样）**没有总时长上限**，长思考、长输出不会被掐断。

## 环境变量

加载顺序：配置文件 → `WB2A_*` 环境变量（变量非空才覆盖）。

| 变量 | 对应字段 |
|---|---|
| `WB2A_LISTEN` | `listen` |
| `WB2A_API_KEY` | `api_key` |
| `WB2A_AUTH_DIR` | `auth_dir` |
| `WB2A_STATE_FILE` | `state_file` |
| `WB2A_SOFT_RATE` / `WB2A_SOFT_RATE_MAX` | `cooldown.soft_rate` / `cooldown.soft_rate_max` |
| `WB2A_TIMEOUT_SECONDS` | `upstream.timeout_seconds` |
| `WB2A_HEADER_TIMEOUT_SECONDS` | `upstream.header_timeout_seconds` |
| `WB2A_IDLE_TIMEOUT_SECONDS` | `upstream.idle_timeout_seconds` |
| `WB2A_USER_AGENT` | `upstream.user_agent` |
| `WB2A_CLIENT_VERSION` / `WB2A_CLI_VERSION` | `upstream.client_version` / `upstream.cli_version` |
| `WB2A_CLIENT_NAME` | `upstream.client_name` |
| `WB2A_DEVICE_TOKEN` / `WB2A_DEVICE_TOKEN_FILE` | `upstream.device_token` / `upstream.device_token_file` |
| `WB2A_PASSTHROUGH_IP` | `upstream.passthrough_ip`（布尔） |
| `WB2A_SANITIZE_FINGERPRINTS` | `features.sanitize_blacklist_fingerprints`（布尔） |
| `WB2A_PROMPT_MODE` / `WB2A_PROMPT_FILE` | `prompt.mode` / `prompt.file` |
| `WB2A_PREFER_EXPIRING` / `WB2A_EXPIRING_SOON` | `pool.prefer_expiring`（布尔） / `pool.expiring_soon` |
| `WB2A_SHUTDOWN_TIMEOUT` | 只能用环境变量设置：停机时等在途请求的上限，默认 `5s`，见[部署与运维](deployment.md#升级备份与停机) |

## 热生效与写回规则

- **立即生效**：`api_key`、`cooldown.soft_rate`、`features.*`、`logging.request_client_info`、`pool.*`、`schedule.*`。
- **需要重启**：`listen`、`auth_dir`、`state_file`、`server.read_timeout`、`upstream.*`、`prompt.*`、`upstash.*`、`session_sticky.*`，以及请求归档的开关、保留天数与容量。面板在保存后会提示具体是哪几项。
- 面板写回配置时做**深合并 + 原子替换**：只更新表单里出现的键，你手写的其他键（包括程序不认识的键）原样保留。
- 时长字段使用 Go 的写法，如 `600s`、`30m`、`2h`、`168h`。
