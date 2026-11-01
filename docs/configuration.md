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
| `pool.cross_realm` | `off` | 同时有国内版和国际版账号，想让不带前缀的模型名在两边合池负载均衡（`merge`），或者国内版优先、国际版只做后备（`cn_first`） |
| `pool.credit_floor` | `0`（关） | 防止收费模型把余额打穿，样例配置里是 `100` |
| `prompt.mode` | `passthrough` | 客户端的系统提示词频繁被上游内容审核误拦时改成 `custom` |
| `schedule.*_enabled` | 全部 `true` | 关掉不需要的定时任务 |
| `global.enabled` | `true` | 只用国内版、想彻底关掉国际版路由时设 `false` |

## 字段速查

### 基础

| 字段 | 默认 | 说明 |
|---|---|---|
| `listen` | `:7863` | HTTP 监听地址 |
| `api_key` | 空 | 管理员密钥：能登面板、调全部接口，不受客户端密钥的上限约束；空 = 不鉴权（但建过[客户端密钥](api.md#客户端密钥)后 `/v1/*` 仍必须带密钥） |
| `auth_dir` | `./auths` | 账号凭证目录 |
| `state_file` | `./data/state.json` | 账号池状态文件；用量统计、请求归档、模型探测结果、客户端密钥（`api_keys.json`）、会话记录（`sessions.json`）都放在同一目录 |
| `dns_servers` | 空 | 出站解析只问这几台 DNS 服务器，按顺序试，前一台连不上换下一台；每项是 IP 或 `IP:端口`（没写端口用 53），写域名会在启动时报错。空 = 用系统解析器。用在没有 `/etc/resolv.conf` 的环境（Android、精简容器），那时 Go 的解析器会退回问本机 53 端口，查什么都失败。换的是整个进程的解析器：对话、积分、网页请求，更新检查、Redis 镜像都跟着换；国内版的 HTTPDNS 和钉 IP 的连接本来就直接连 IP，不受影响。需重启 |
| `server.read_timeout` | `300s` | 读取一个入站请求（含上传请求体）的总时长上限。大上下文请求经反代转发超时会报 400 `read body: i/o timeout`，可调大；`0` = 不限制 |
| `panel.package_detail_limit` | `5` | 积分构成页单个账号默认展开的最早到期积分包数，其余折叠 |
| `panel.update_check` | `true` | 每 6 小时从 GitHub 读取一次更新日志，有新版本时面板提醒，并可[一键更新](panel.md#一键更新)；`false` = 不联网检查，也不出现一键更新按钮。见[更新提醒](panel.md#更新提醒) |

### 日志留存

请求记录、任务日志、系统日志、积分获取各自按天写文件，各设开关、保留天数和容量上限；用量统计只设保留天数。面板「配置 → 日志留存」里改，**保存即生效**：当场按新规则清理一次，之后每分钟左右再查一次。

- 保留天数 `0` = 永久保留，按天的文件仍受容量上限约束；负数回落默认值。
- 超过容量上限先删最旧的文件；正在写的那份不删。
- 关掉某类留存后不再写新文件，已有的文件照样按保留天数到期删除；任务、系统日志关掉后，运行日志页的「历史」仍查得到关闭前留下的。
- 配置页每类卡片下面显示现有的文件数、大小和最早日期，可以一键清空（要再确认一次）。

| 字段 | 默认 | 说明 |
|---|---|---|
| `logging.request_archive_enabled` | `true` | 请求元数据按天归档为 `request-logs/requests-<日期>.jsonl`。只记模型、账号、耗时、token 等元数据，失败的请求另记失败原因（回给客户端的错误消息，最多 200 字），**不记提示词、响应正文和 Authorization**。关掉后请求记录页只有内存里的最近 100 条 |
| `logging.request_retention_days` | `7` | 请求记录保留天数，`0` = 永久保留 |
| `logging.request_archive_max_mb` | `100` | 请求记录的容量上限（MiB） |
| `logging.request_client_info` | `true` | 是否记录调用来源（客户端 IP 与 User-Agent）。IP 属于个人信息，共享部署可关 |
| `logging.task_log_enabled` | `true` | 运行日志「任务」频道（签到、保活、旅行、成长任务等）按天写进 `runtime-logs/task-<日期>.jsonl` |
| `logging.task_log_retention_days` | `30` | 任务日志保留天数，`0` = 永久保留 |
| `logging.task_log_max_mb` | `20` | 任务日志的容量上限（MiB） |
| `logging.system_log_enabled` | `true` | 运行日志「系统」频道（选号、冷却、熔断、配置改动等）按天写进 `runtime-logs/system-<日期>.jsonl` |
| `logging.system_log_retention_days` | `30` | 系统日志保留天数，`0` = 永久保留 |
| `logging.system_log_max_mb` | `50` | 系统日志的容量上限（MiB） |
| `logging.usage_retention_days` | `0` | 用量统计（`usage.json` 里按小时、按天的用量明细）保留天数，`0` = 永久保留。超期的明细删掉后，用量页能查到的最早时间随之后移；客户端密钥的每天、总量限额另有计数，不受影响 |
| `logging.credit_log_enabled` | `true` | 签到、成长任务领奖、积分补偿、猫猫旅行、新手礼包、连登兑换领到的积分逐笔写进 `credit-logs/credit-<日期>.jsonl`（时间、账号、来源、任务码或档位、积分数），面板「积分获取」页查的就是它。定时任务、面板操作、任务队列领到的都记；命令行工具（`cmd/signin` 等）领到的不记 |
| `logging.credit_log_retention_days` | `0` | 积分获取保留天数，`0` = 永久保留（一个号一天也就几笔） |
| `logging.credit_log_max_mb` | `20` | 积分获取的容量上限（MiB） |

目录都在状态文件（`state_file`）旁边，Docker 部署就是 `data/` 下面。

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
| `schedule.liveness_enabled` | `true` | 定时测活：给整个号在限流冷却、熔断中的号发一条最短的真实对话，测通就提前恢复（见[测活](panel.md#测活)） |
| `schedule.liveness_minutes` | `30` | 定时测活间隔（分钟） |
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
| `upstream.user_agent` | 空 | 出站 User-Agent 整体覆盖（任务中心的桌面端上报、外观设置、专家列表除外）。空 = 按请求的来源取官方的 UA：对话、模型目录由桌面端内置的 CLI 发，形如 `WorkBuddy/<client_version> <应用名>/<client_version> CLI/<cli_version>`；联网搜索也由内置 CLI 发，但不经它的请求框架，是 `CLI/<client_version>`；登录、刷新令牌、上报由桌面端后台服务发，两段都是应用名：`<应用名>/<client_version> <应用名>/<client_version> CLI/<cli_version>`；积分、签到由后台服务另用 axios 发，是 `axios/1.16.1`；成长中心、积分补偿、新手礼包、试用只在官网网页上调，用 Mac 上 Chrome 的 UA。应用名国内版是 `WorkBuddy`，国际版是 `workbuddy-ai` |
| `upstream.client_version` | 空（国内版 `5.9.3`，国际版 `5.9.4`） | UA 与 `X-IDE-Version` 里的桌面端版本；填了就两个域都用这个值 |
| `upstream.cli_version` | 空（两个域都是 `2.165.0`） | UA 里 `CLI/` 段的版本，是各域桌面端内置 CLI 的版本；填了就两个域都用这个值。联网搜索的 UA 例外：官方在那里写的是桌面端版本（`client_version`） |
| `upstream.client_name` | 空（= `WorkBuddy`） | 对话请求的归属头。空或 `WorkBuddy` 时按官方桌面端发送 `X-Agent-Purpose`、`X-IDE-Name`、`X-IDE-Type`、`X-IDE-Version`；配 `SaaS` 是旧行为，对话不带这几个头（联网搜索同样不带 `X-IDE-*`），积分、签到这类请求不带 UA。`X-Product` 不受这项影响，带它的请求和官方一样固定为 `SaaS` |
| `upstream.device_token` | 空 | 设备风控头 `X-Device-Token` 的全局兜底值；单个凭证文件里的 `device_token` 优先。签到、登录的发起授权，以及对话都带（对话只在填了才带——网关在 Linux 做不出真令牌，没填就不发），见[官方客户端对照](official-client.md#几类请求) |
| `upstream.device_token_file` | 空 | 从文件读取 device token（5 分钟缓存），优先级低于前两者 |
| `upstream.passthrough_ip` | `false` | 是否把客户端 IP 透传给上游 |
| `upstream.model_optimization` | `false` | 是否允许上游用对话做模型优化。关闭时每条对话都带 `X-Private-Data: true`（不允许），等于在桌面端手动关掉「模型优化」：国际版 5.9.4 没动过这个开关时也是这样，国内版 5.9.3 起没动过时个人号发 `false`（允许）。打开时个人号发 `false`，企业号（有企业 id，或账号类型是 `ultimate`、`exclusive`）仍发 `true`，和国内版 5.9.3 没动过开关时一样。面板「上游与高级 → 出站请求」可改，保存即生效 |
| `upstream.tls_fingerprint` | `true` | 出站 TLS 握手是否伪装成官方桌面端的 ClientHello（用 uTLS 复刻，降低被按握手指纹识别的概率，见[官方客户端对照](official-client.md#没对齐或没实测的)）。默认开；关掉回落 Go 标准库握手。改 config 后重载生效，对新建连接起用 |

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
| `pool.max_rpm` | `0` | 国内版单账号每分钟最多接几个请求（按最近 60 秒算），到了就当这个号忙，换号或排队；`0` = 不限，见[网关与选号](gateway.md#选号策略) |
| `pool.max_rpm_global` | `0` | 国际版单账号每分钟请求数上限，只管国际版的号，不回落到 `pool.max_rpm`；`0` = 不限 |
| `pool.in_flight_wait` | `30s` | 能接请求的号在途都满或每分钟请求数到上限时，请求最多排队等多久空位，等满才回 503，见[网关与选号](gateway.md#选号策略)；`0` = 不等 |
| `pool.cross_realm` | `off` | 跨域负载均衡：`off` 关闭、`merge` 合池、`cn_first` 国内版优先；老配置的 `true` 读成 `merge`、`false` 读成 `off`，见[网关与选号](gateway.md#跨域负载均衡) |
| `pool.credit_floor` | `0` | 积分保底：余额低于该值的账号不再接实测收费的模型；`0` = 关闭 |
| `pool.prefer_expiring` | `true` | 有积分快过期的账号选号权重 ×3，优先把快过期的积分用掉 |
| `pool.expiring_soon` | `168h` | 「快过期」的窗口；空或 `0` 关闭 |
| `pool.cost_explore_interval` | `30m` | 免费层垄断时，每个窗口把一个真实请求改道给成本未知的账号，以便学到它的真实成本；`0` = 关闭 |
| `pool.breaker_threshold` | `3` | 连续失败几次触发熔断 |
| `pool.breaker_cooldown` / `pool.breaker_cooldown_max` | `30m` / `6h` | 熔断基础时长与指数退避上限 |
| `pool.degrade_threshold` | `5` | 未知错误连败几次临时出池 |
| `pool.degrade_cooldown` / `pool.degrade_cooldown_max` | `10m` / `2h` | 连败出池时长与上限 |
| `pool.idle_weight_per_hour` / `pool.idle_weight_max` | `0.5` / `5.0` | 闲置补偿：每闲置一小时加的权重与上限 |
| `pool.restriction_wave_threshold` | `2` | 风控波暂停：`restriction_wave_window` 内同一个域有这么多个号被上游 `11140` 风控限制，就暂停该域其余的号，见[网关与选号](gateway.md#错误分类与账号处置)；`0` = 关闭 |
| `pool.restriction_wave_window` / `pool.restriction_wave_pause` | `15m` / `30m` | 风控波的统计窗口与暂停时长；空或非正值回落默认 |

### 模型路由

`model_routes` 是一个有序数组，每条 `{"match": "...", "target": "..."}`：客户端发来的模型名不在上游目录里时，从上往下找第一条 `match` 匹配的规则，改用它的 `target`。只认 `*` 通配，不区分大小写；带 `cn:` / `global:` 前缀的名字不改，`target` 可以带前缀。默认没有规则。完整行为见 [API 参考](api.md#模型路由)。

```json
"model_routes": [
  {"match": "claude-*haiku*", "target": "deepseek-v4-flash"},
  {"match": "claude-*", "target": "glm-5.2"}
]
```

`match`、`target` 不能为空，`target` 不能带 `*`，否则启动和面板保存都会报错。一般在面板「模型与档位 → 模型路由」里编辑，保存即生效。

### 隐藏模型

`models.hidden` 是不对外的模型名单：`/v1/models` 不列，点名调用回 400 + `model_unavailable`。带 `cn:` / `global:` 前缀的只藏那个域，不带前缀的两个域都藏；精确匹配、区分大小写，不认通配。按经模型路由改写后实际发给上游的模型判，和客户端密钥的模型白名单叠加。默认为空。完整行为见 [API 参考](api.md#隐藏模型)。

```json
"models": {
  "hidden": ["glm-4.7", "global:gpt-5.5"]
}
```

一般在面板「模型与档位 → 对外隐藏的模型」里编辑，加一项、删一项都立即生效。

### 会话粘性与 Redis

| 字段 | 默认 | 说明 |
|---|---|---|
| `session_sticky.enabled` | `true` | 同一会话尽量固定用同一个账号 |
| `session_sticky.ttl` | `30m` | 会话绑定有效期，每次使用滚动续期。过期后再来的请求先回到这段对话用过的号，见[会话粘性](gateway.md#会话粘性) |
| `session_sticky.gc_interval` | `5m` | 过期绑定清理周期 |
| `upstash.url` / `upstash.token` | 空 | Upstash Redis（或任意 `rediss://` 地址）。空 = 不镜像，功能不受影响；配置后池状态、会话绑定与会话记录异步镜像到 Redis |

## 上游超时

三个超时各管一段：

| 字段 | 作用对象 | 默认 | 行为 |
|---|---|---|---|
| `timeout_seconds` | 短请求：token 刷新、签到、余额、模型列表 | `120` | 总时长硬上限，到期报错并按规则换号或熔断 |
| `header_timeout_seconds` | 聊天请求的**首字节前** | `120` | 超时即换号重发；国内版照官方先在备用入口重发一次（也按这个超时），仍超时再换号 |
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
| `WB2A_DNS` | `dns_servers`（逗号分隔，如 `223.5.5.5,119.29.29.29`） |
| `WB2A_SOFT_RATE` / `WB2A_SOFT_RATE_MAX` | `cooldown.soft_rate` / `cooldown.soft_rate_max` |
| `WB2A_TIMEOUT_SECONDS` | `upstream.timeout_seconds` |
| `WB2A_HEADER_TIMEOUT_SECONDS` | `upstream.header_timeout_seconds` |
| `WB2A_IDLE_TIMEOUT_SECONDS` | `upstream.idle_timeout_seconds` |
| `WB2A_USER_AGENT` | `upstream.user_agent` |
| `WB2A_CLIENT_VERSION` / `WB2A_CLI_VERSION` | `upstream.client_version` / `upstream.cli_version` |
| `WB2A_CLIENT_NAME` | `upstream.client_name` |
| `WB2A_DEVICE_TOKEN` / `WB2A_DEVICE_TOKEN_FILE` | `upstream.device_token` / `upstream.device_token_file` |
| `WB2A_PASSTHROUGH_IP` | `upstream.passthrough_ip`（布尔） |
| `WB2A_MODEL_OPTIMIZATION` | `upstream.model_optimization`（布尔） |
| `WB2A_TLS_FINGERPRINT` | `upstream.tls_fingerprint`（布尔） |
| `WB2A_SANITIZE_FINGERPRINTS` | `features.sanitize_blacklist_fingerprints`（布尔） |
| `WB2A_PROMPT_MODE` / `WB2A_PROMPT_FILE` | `prompt.mode` / `prompt.file` |
| `WB2A_PREFER_EXPIRING` / `WB2A_EXPIRING_SOON` | `pool.prefer_expiring`（布尔） / `pool.expiring_soon` |
| `WB2A_SHUTDOWN_TIMEOUT` | 只能用环境变量设置：停机时等在途请求的上限，默认 `5s`，见[部署与运维](deployment.md#升级备份与停机) |

### 出站代理

发给官方的请求（对话、积分、任务、登录等）照官方客户端读代理环境变量，这几个变量没有对应的配置字段，改了要重启。要让每个号走自己的代理，用面板「代理」页的代理池，见[按号代理与代理池](proxy.md)；用了代理池以后，这里的代理是**默认线路**：没绑代理的号、不带号的请求（官方静态站点、`models.dev`）走它。

| 变量 | 说明 |
|---|---|
| `HTTPS_PROXY`、`https_proxy`、`HTTP_PROXY`、`http_proxy`、`ALL_PROXY`、`all_proxy` | 代理地址，按这个顺序取第一个非空的。支持 `http://`、`https://`、`socks5://`、`socks5h://`，没写协议按 `http://`；要认证的把用户名密码写进地址，如 `http://user:pass@127.0.0.1:7890` |
| `NO_PROXY`、`no_proxy` | 不走代理的域名，逗号分隔：`*` 全部直连，`example.com` 连同子域，`.example.com` 只管子域 |

- 隧道里照样伪装官方客户端的 TLS 指纹（`upstream.tls_fingerprint`），走不走代理上游看到的握手都一样。
- 本机地址（`localhost`、`127.0.0.1`、`::1`）总是直连。代理连不上时请求报错，不会退回直连。
- 照官方，国内版主入口出错、改用 HTTPDNS 地址或备用 IP 重发的那几次请求直连，HTTPDNS 查询本身也直连，这些请求用的是服务器自己的 IP；拉模型目录走代理时不试备用 IP。绑了代理池里代理的号不一样：重发也经号的代理连到那个 IP。依据见[对齐文档](official-client.md#出站代理)。
- 启动日志会写一行用的是哪个代理；设了但用不了（协议不认、地址写错）时写一行 WARN 并直连。
- 面板的版本检查和一键更新另走 Go 标准库的代理处理，同样认 `HTTPS_PROXY`、`NO_PROXY`。

## 热生效与写回规则

- **立即生效**：`api_key`、`cooldown.soft_rate`、`features.*`、`logging.*`、`panel.*`、`pool.*`、`schedule.*`、`model_routes`、`models.hidden`。
- **需要重启**：`listen`、`auth_dir`、`state_file`、`dns_servers`、`server.read_timeout`、`upstream.*`、`prompt.*`、`upstash.*`、`session_sticky.*`。面板在保存后会提示具体是哪几项。
- 代理池（代理、池和设置）不在 `config.json` 里，存在状态文件同目录的 `proxies.json`，在面板「代理」页改，保存即生效，见[按号代理与代理池](proxy.md#存放备份与日志)。
- 面板写回配置时做**深合并 + 原子替换**：只更新表单里出现的键，你手写的其他键（包括程序不认识的键）原样保留。
- 时长字段使用 Go 的写法，如 `600s`、`30m`、`2h`、`168h`。
