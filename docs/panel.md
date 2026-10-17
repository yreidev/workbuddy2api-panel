# Web 管理面板

面板打包在网关二进制里，启动后访问 `http://<主机>:7863/panel/`。

- `api_key` 非空时，首次打开需要输入一次密钥（浏览器会记住）；为空则直接进入。
- 支持明暗主题（首次跟随系统）和手机端（导航收进抽屉，表格换成卡片）。

<p align="center"><img src="images/hero-light.webp" alt="账号池" width="860"></p>

- [视图一览](#视图一览)
- [时间范围](#时间范围)
- [调用来源](#调用来源)
- [面板接口](#面板接口)
- [安全](#安全)

## 视图一览

| 视图 | 能做什么 |
|---|---|
| **账号池** | 统计卡：总数 / 可用 / 冷却 / 禁用 / 暂停，点一下即按状态筛选；另有积分合计与粘性会话数。<br>积分到期提醒：打开就列出每个账号最早一批要过期的积分，以及到期前日均至少要用掉多少（本地快照，不查上游）；点「检查」逐账号向上游查询，看到全部到期批次。<br>账号表：状态与冷却倒计时、被限额的模型及解封时间、积分量条与最早到期、成功 / 失败 / 在途、用量与速率。<br>单号操作：签到、余额、任务、解冻、暂停选号、禁用、移除。<br>批量操作：全部签到、旅行巡检、活跃上报、全部保活。<br>模型锁池：哪些模型被上游按模型限额、还剩几个号可用、多久后解锁。 |
| **账号详情**（点账号行） | 模型限额台账、每个模型的实测单价、最早到期批次、连败与熔断计数、最近一次请求的模型与耗时 |
| **添加账号** | 浏览器内完成国内版 / 国际版 OAuth 登录，凭证落盘并热加载进池，不用重启；国际版会自动完成注册地区、激活与试用额度领取。也可导入 cockpit tools 导出的 JSON |
| **用量** | 请求数、token 构成、失败率、平均延迟与吐字速率；按小时的 token 时序图；按账号 / 模型 / 域的用量明细；积分扣除历史（每百万 token 积分、前缀缓存命中率） |
| **积分构成** | 积分到期分布（按剩余天数、按账号着色）、账号对比、单账号逐包明细 |
| **任务中心** | 扫描全部账号的成长任务待办，排队一键执行，实时显示进度；积分补偿查询与领取；券码查询 |
| **模型与档位** | 每个模型的积分倍率（含限时优惠）、默认思考档、支持的档位、上下文长度与最大输出；可叠加[实测输出上限](deployment.md#探测模型真实输出上限)。支持关键词、域、能力、档位、价格筛选和多种排序，数据一次拉全后在浏览器里筛 |
| **配置** | 在线编辑配置，分服务 / 定时任务 / 账号池与流量治理 / 上游与高级四组；底部保存条显示改了几项，需要重启的字段有标记，时长与时点当场校验 |
| **运行日志** | 请求记录：成功率、平均耗时、每条请求的模型 / 账号 / 来源 IP / User-Agent / token 与缓存命中 / 积分 / 请求 ID，可按条件和时间范围从归档里查。<br>服务日志：最近 500 行，按任务 / 对话 / 系统频道、级别和关键字筛选 |

| 用量 | 模型与档位 |
|:---:|:---:|
| <img src="images/usage.webp" alt="用量" width="420"> | <img src="images/models.webp" alt="模型与档位" width="420"> |
| **任务中心** | **手机端** |
| <img src="images/tasks.webp" alt="任务中心" width="420"> | <img src="images/mobile-accounts.webp" alt="手机端账号池" height="292"> <img src="images/mobile-usage.webp" alt="手机端用量" height="292"> |

## 时间范围

「用量」与「运行日志 → 请求记录」共用同一个时间范围控件：今天 / 近 24 小时 / 近 3 天 / 近 7 天 / 近 30 天 / 全部历史 / 自定义（精确到分钟）。

- 「今天」和「自定义」由浏览器按本地时区算好区间再发给服务端，服务端和浏览器时区不同时也不会切错日子。
- 滚动预设按整点往回数 N 小时，与统计分桶对齐。
- 请求记录的时间过滤在归档侧完成，查更早的时段也能取到记录。

## 调用来源

请求记录会显示每次调用的来源 IP 和 User-Agent，用来排查「谁在用网关」：

- IP 依次取 `X-Forwarded-For` 首段、`X-Real-IP`、TCP 对端地址；
- User-Agent 截断到 200 字节；终端里的请求流水行会附上压缩后的客户端标签。

IP 属于个人信息，所以由 `logging.request_client_info` 控制，默认开启、热生效，可在「配置 → 上游与高级 → 请求日志」里关掉。关闭后归档与面板都不再出现来源字段。

## 面板接口

面板的所有操作都是 `/panel/api/*` 接口，与页面使用同一个 Bearer 鉴权，可以直接写脚本调用：

| 分组 | 端点 |
|---|---|
| 只读 | `GET overview` · `usage`（`hours` 或 `from`/`to`）· `packages` · `models` · `model_probes` · `logs` · `request_metrics` · `request_logs`（`limit` / `outcome` / `account` / `model` / `client_ip` / `user_agent` / `from` / `to`）· `config` · `tasks/queue` · `school/vouchers` |
| 账号运维 | `POST accounts/{uid}/checkin` · `balance` · `revive` · `pause` · `resume` · `disable` · `remove`<br>`POST checkin_all` · `travel_all` · `activity_all` · `keepalive_all` · `balance_all` |
| 任务 | `GET accounts/{uid}/tasks`<br>`POST accounts/{uid}/tasks/accept` · `accept_all` · `claim` · `auto` · `auto_all`<br>`POST tasks/scan_all` · `tasks/run_queue` |
| 积分补偿 | `GET compensation`（逐账号查询官方状态，含已禁用账号，带 `disabled` 标记）<br>`POST accounts/{uid}/compensation/claim` · `POST compensation/claim_all`（先查状态再领取，已禁用账号只查不领；批量结果逐账号返回） |
| 添加账号 | `POST login/start` · `GET login/poll` · `GET login/regions` · `POST import/cockpit` |
| 写入 | `POST config`（校验 → 落盘 → 热应用，返回需重启的字段）· `POST usage/save`（立即把内存里的用量落盘） |

## 安全

- 面板页面与全部 `/panel/api/*` 响应都带 `Content-Security-Policy`（`default-src 'none'`，脚本仅同源，禁止被嵌套）、`X-Content-Type-Options: nosniff`、`X-Frame-Options: DENY`、`Referrer-Policy: no-referrer`；前端没有内联脚本。
- 密钥校验用 SHA-256 摘要加常量时间比较。
- 上游返回的 `uid` 经白名单校验（`[A-Za-z0-9_-]`，不超过 64 位）后才用于拼凭证文件名，防止路径穿越。
- 服务本身只提供 HTTP。公网部署请放在 HTTPS 反向代理之后，并设置 `api_key`。
