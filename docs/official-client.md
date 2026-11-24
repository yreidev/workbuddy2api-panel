# 官方客户端对照

WorkBuddy 的出站请求按官方桌面端发送；TraeWork、Trae Code、Trae 授权和 Qoder 分别使用对应运行时的传输配置。这里记录依据、对齐范围和官方升级后的复核方法；多平台采样见[分平台传输与身份基准](#分平台传输与身份基准)。

## 依据

| 项 | 国内版 | 国际版 |
|---|---|---|
| 桌面端安装包 | WorkBuddy 5.11.3.43806934（macOS arm64） | WorkBuddy AI 5.11.4.44121853（macOS arm64） |
| 内置 CLI（`@tencent-ai/codebuddy-code`） | 2.176.0 | 2.176.0 |
| `applicationName`（product.json） | `WorkBuddy` | `workbuddy-ai` |

2026-10-20 从官网下载两个安装包（当时国内版是 5.8.3、国际版是 5.8.4），只解包阅读，没有运行；国内版 5.9.3（2026-10-28）、国际版 5.9.4（2026-10-31）出来后照同样的做法重新对过，见[与桌面端 5.9.3 的对照](#与桌面端-593-的对照)和[与国际版桌面端 5.9.4 的对照](#与国际版桌面端-594-的对照)。要看的文件有三处：

- `app.asar.unpacked/cli/dist/codebuddy-lite-wb.mjs`：内置 CLI，对话、模型目录、联网搜索、备用入口切换都由它做；
- `app.asar.unpacked/cli/product.json`：版本、`applicationName`、产品开关与备用入口配置；
- `app.asar` 里的 `main/`：桌面端后台服务，积分、签到、登录、刷新令牌、上报、外观、专家列表由它发（积分、签到在 `authority-snapshot.js`，5.8.3 是 `tar.js`；登录、刷新令牌在 `common2.js`；压缩后的文件名会随版本变）；每次提问前它还给内置 CLI 的模型请求另加几个头，见[几类请求](#几类请求)里的对话。

成长中心、积分补偿、新手礼包、试用只在官网网页上，同日读了 `www.workbuddy.cn` 成长中心页的前端代码。

npm 上更新的 CLI 只作对照，取值以安装包内置的版本为准：用户实际跑的是安装包里的 CLI，网关在 UA 里报的也是它的版本号。npm 上的 CLI 几乎每天发一版，和桌面端不同步；只把 UA 里的 `CLI/<版本>` 换成 npm 的新版本，会凑出官方不存在的组合（5.8.3 的桌面端不会带 2.174.0 的 CLI），新版 CLI 多发或改发的头也对不上。

### 与国内版桌面端 5.11.3 及国际版桌面端 5.11.4 的对照

2026-11-16 先解开[国内 macOS arm64 稳定包](https://download.codebuddy.cn/workbuddy/saas/darwin-arm64/WorkBuddy-darwin-arm64-5.11.3.43806934-ecace0f7.zip)同步了国内版版本号与构建信息；2026-11-24 再次查询两域更新接口并完整解包[国内版 5.11.3.43806934](https://download.codebuddy.cn/workbuddy/saas/darwin-arm64/WorkBuddy-darwin-arm64-5.11.3.43806934-ecace0f7.zip)（SHA-256 `26a9e617c30bff038efb651ee5fc8eec7c188c37ec0f32231726793302a91156`）与[国际版 5.11.4.44121853](https://download.codebuddy.ai/workbuddy/saas/darwin-arm64/WorkBuddy-darwin-arm64-5.11.4.44121853-0026dff0.zip)（SHA-256 `fe5d9be52791bb10df320ff7f66a24923a279f588dfe55284911f30a64eaeef8`），只解包阅读、没有运行：

- **版本与构建信息**：国内版 `5.11.3` / 内置 CLI `2.176.0`，上报 `commit` `ecace0f7028166695994300182295329b8b95449`、`releaseDate` `1794501984587`（`2026-11-12T16:46:24.587Z`）；国际版 `5.11.4` / 内置 CLI `2.176.0`，上报 `commit` `0026dff06ce8414140e6f2fa731049855c3ecc6f`、`releaseDate` `1794817062759`（`2026-11-16T08:17:42.759Z`）。两域安装包内置的 `cli/dist/codebuddy-lite-wb.mjs` 逐字节一致，`applicationName` 仍分别为 `WorkBuddy` 与 `workbuddy-ai`。
- **运行时与 SDK**：两域包内 Electron Framework 均为 `37.10.3`、内置 Node `v22.21.1`、OpenAI SDK `6.25.0`（`3043.50ee4dca.mjs`）、后台 axios `1.16.1`，公共 UA 模板和 1200 秒 SDK 超时未变。同版 Electron Linux runtime 回环 `fetch` 验证 ClientHello 的 JA3、JA4 与原基准一致。
- **客户端身份头组 `X-Client-Version` / `X-Client-Platform` / `X-Client-Product`**：5.11.x 新增 `app/main/client-identity.js` 与统一出站头注册表 `app/main/proxy-env2.js`，在 `index.js`、`server.js` 注册第一方来源（`[endpoint, ...trustedOrigins]`），同时由 `sidecar-manager.js` 通过 `CODEBUDDY_CUSTOM_HEADERS` 传给内置 CLI 子进程：
  - 取值：`X-Client-Version` 为桌面端版本（`5.11.3` / `5.11.4`），`X-Client-Platform` 为平台映射（`darwin` → `macos`），`X-Client-Product` 取 `applicationName`（国内版 `WorkBuddy`、国际版 `workbuddy-ai`，不是 `productName`，也不是缺配置回退值 `workbuddy-desktop`）；
  - 范围：后台 `restOperations`（登录、刷新、上报、外观、专家列表、模型目录）、`AuthService` axios（积分、企业用量、签到）、默认 axios（容量排队 `/v2/chat/queue/status` 与 `/cancel`，同时由 `tls-verification.js` 补 32 位 hex `X-Request-ID`），以及内置 CLI 的对话、模型目录、联网搜索（`performWebSearch` 调 `customHeadersProvider.applyTo`）全部发送；仅在目标头尚未设置时不覆盖已有值，不影响官网网页（`X-Client-Platform: web`）、网页上报（不带）与小程序（`workbuddy-mp` / `mp-weixin` / `miniprogram`）及静态公开 CDN（`scenes-v2.json`、`playbook`）。网关已全量对齐（`client_name="SaaS"` 旧行为不发）。
- **请求体 `cache_retention`**：内置 CLI 2.176.0 的 `axiosToFetchAdapter` 对未带该字段的非自定义模型请求自动补 `cache_retention`，`resolveCacheRetention` 默认返回 `"long"`（仅 `CODEBUDDY_DISABLE_PROMPT_CACHING_1H` 为真时返回 `"short"`）并在会话内锁定。网关现按官方默认对缺少该字段的请求补 `"long"`，保留客户端显式传入值（注：1 小时缓存在部分模型上写入成本可能更高，尚未在真实账号逐模型对比成本）。
- **请求体 `extra_vars.harnessMetadata` 与 `extra_vars.initiator`**：内置 CLI 2.176.0 对官方 `/v2/chat/completions`（带 `messages` 数组）合并写入 `extra_vars`（保留其他已有键）：
  - `extra_vars.harnessMetadata` 为 JSON 字符串（`buildHarnessMetadata`）：`{"schemaVersion":1,"harnessVersion":"2.176.0","stepIndex":<n>[, "previous":{"conversationMessageId":"<上一步X-Conversation-Message-ID>"}],"agent":{"depth":0}}`；同一对话轮（同 `X-Conversation-Request-ID`）首步 `stepIndex=0` 不带 `previous`，工具调用续步依次递增并串联上一步消息 id，失败重试（`startRetriedModelAttempt`）复用同一消息 id 与步数不递增。网关通过 `internal/session/harness.go` 与 `ChatMeta.MessageID` / `HarnessStep` 精确对齐；
  - `extra_vars.initiator`（`MessageInitiatorRequestProcessor`）：末尾消息为普通 `role: "user"` 时写入 `"user"`，末尾为 `role: "tool"` 等非 `user` 角色时不写入。网关已对齐。
- **其他核实项**：`AuthService`（积分、签到）将 `X-Device-Token` 迁至全局 `device-token.js`；网关在 Linux 无法生成真图灵盾令牌，仍保持仅在用户配置了 `device_token` 时于登录发起授权、签到与对话按需发送。5.9.3 快照曾出现的 `x-real-ip` 仅属内置 CLI `isTrustedLocalRequest` 的入站本地请求代理头拒检列表，5.11.x 移除该列表，官方从未出站发送 `X-Real-IP`。`RequestBodyGzip` 在 5.9.3 起即已开启，网关照旧发明文 JSON 请求体。本次均为读包与离线单测对齐，未用真实账号逐项在线实测。

### 与 npm 2.174.0 的对照

2026-10-24 社区传「官方改了登录」，同类项目同日按独立 CLI 2.174.0 重写了登录（它们之前停在 `CLI/2.63.2`）。当天从 npm 下载 `@tencent-ai/codebuddy-code` 2.174.0（台湾时间 10-24 04:32 发布）只解包阅读，包里也带了桌面端用的 `dist/codebuddy-lite-wb.mjs` 和 `dist/lazy-lite-wb/`，和 5.8.3 内置的 2.161.0 比对：

- 登录和刷新令牌没有变：发起授权、轮询令牌、取账号、刷新令牌、授权被拒的业务码、`calculateExpiresAt` 除了压缩后的变量名都一样。
- 新增了一套交接登录：`GET /v2/plugin/auth/handoff/info?code=<码>` 查身份、`POST /v2/plugin/auth/handoff/token`（请求体 `{code}`）换令牌、`GET /v2/plugin/auth/handoff/account` 取账号（`loginWithHandoff`）。CLI 自己不调用它，应该是给桌面端外壳把别处的登录交过来用的；原来的扫码流程照旧保留。
- 对话请求多了一个头 `X-Agent-Runtime`：取会话的 `codebuddy.ai/agentRuntime` 或环境变量 `CODEBUDDY_AGENT_RUNTIME`，只认 `local-agent`、`cloud-agent`、`agent-agent`，都没有时是 `other`。2.161.0 不发。当时没确认桌面端换用新 CLI 后发什么值；5.9.3（内置 2.165.0）给本机的会话发 `local-agent`，见[与桌面端 5.9.3 的对照](#与桌面端-593-的对照)。

结论：网关的登录不用改，版本号等桌面端出新版再一起换（国内版 5.9.3 时换了）。比对时发现的两处网关缺口（授权被拒的提示、毫秒的到期时间）在 1.5.1 补上了，见[登录](#登录)与[刷新令牌](#刷新令牌)。

### 与 npm 2.177.0 的对照

2026-10-27 从 npm 下载 `@tencent-ai/codebuddy-code` 2.177.0（台湾时间 10-27 00:31 发布）只解包阅读，和 2.175.0 比对。包里的更新日志把下面这条记在 2.176.0 名下，桌面端用的 `dist/codebuddy-lite-wb.mjs` 里也有：

- 专家请求头改由提示带的 W3C `baggage` 展开：宿主给这一轮提示的 `baggage` 里有 `x-expert-id`、`x-expert-team-task`、`x-moderation-type` 时，内置 CLI 把它们展开成 `X-Expert-Id`、`X-Expert-Team-Task`、`X-Moderation-Type`（`resolveExpertHeaders`），加在内置模型请求和平台接口上。值为空或不是合法的请求头值时不发，没有默认值。前两个网关本来就在发，见[几类请求](#几类请求)里的专家对话；`X-Moderation-Type` 网关 2026-10-28 起也在专家对话里发了，按的是后台服务那条（`shouldProtectSession`），不是这条 `baggage`。
- 未确认：桌面端在哪些场景往 `baggage` 里放 `x-moderation-type`、放什么值。当时写的「后台服务那边也没有代码可以对照」不对：5.8.3 的后台服务每次提问前就直接给模型请求加 `X-Moderation-Type`，不经 `baggage`，2026-10-28 对 5.9.3 时才发现，见[几类请求](#几类请求)里的对话。`baggage` 这条路 5.9.3 内置的 2.165.0 还没有。
- 同时新增的 `x-source-channel-id`、`x-source-model-type`、`x-source-client-type`、`x-source-enterprise-id`，只在请求经 https 发往 `tencentmaas.com`、`tencentmaas.cn`、`tencentcloudmaas.com`、`tencentcloudmaas.tech` 及其子域时补上（`isMaasSourceHeaderUrl`、`applyMaasSourceHeaders`）。网关的对话主入口和备用入口都不是这些域名，不用加。

其余变化是独立 CLI 自己的版本号、`product.json` 的 `commit` 与 `date`，以及自动模式审批的提示词，都和网关无关。结论：网关这次不用改。

### 与桌面端 5.9.3 的对照

2026-10-28 国内版桌面端出了 5.9.3（更新接口给的版本是 `5.9.3.42398134`），内置 CLI 2.165.0。监控仓库自动下载 macOS 和 Windows 两个包、比对出变化，同日人工解开 Windows 包（只解包阅读）逐项核对，和 5.8.3 比：国际版还是 5.8.4，没有新包。

变了、网关跟着改的：

- 版本号：桌面端 5.9.3，内置 CLI 2.165.0。国际版仍是 5.8.4 和 2.161.0，所以 UA 里 `CLI/` 的版本改成按账号的域取（`defaultCliVersionCN`、`defaultCliVersionGlobal`）。`applicationName` 没变，还是 `WorkBuddy`。
- 上报的 `commit`、`releaseDate` 换成 mac 包 `product.json` 的 `d46924219e79332f49c37ccebf27292f87001b19`、`2026-10-27T09:23:43.713Z`（Windows 包的 `commit` 相同，`date` 晚 27 秒）。
- 对话请求多了 `X-Agent-Runtime: local-agent`。2.165.0 的对话头（`getCustomHeaders`）和 2.161.0 比只多了这一个，取法见[与 npm 2.174.0 的对照](#与-npm-21740-的对照)；桌面端在本机会话（`transport` 为 `local`）的元数据里放 `agentRuntime: local-agent`（`conversations.js`）。网关对国内版的对话都发这个值，国际版当时不发（国际版 5.9.4 也内置 2.165.0，2026-10-31 起两域都发，见下一节）。
- `X-Private-Data` 的算法换了，见[几类请求](#几类请求)里的对话。网关默认仍发 `"true"`（2026-10-28 定）；`upstream.model_optimization` 打开时，判断企业版改照官方的 `isEnterpriseEditionAccount`，多认账号类型 `ultimate`、`exclusive`。

没变的：两个 UA 拦截器、请求框架的公共头、上报事件的处理器（只多了下面的 `oneId`）、后台服务每次提问前加的头。

新出现、网关没跟的：

- 上报事件多了 `oneId`（`BuddyOneIdEventProcessor`），取环境变量 `WORKBUDDY_BUDDY_ONE_ID`。桌面端登录时查 `/api/v1/copilot/account/buddyone/status`，回 `code` 0 且带 `buddyone_id` 才设这个变量。网关不查这个接口，事件里也不带 `oneId`；个人号查这个接口会回什么没实测（未确认）。
- AICC 保密模型通道（企业号用，包内配置没开）和业务码 `11145`（自动分析说是移动 TokenPlan 的算力豆用完，内置 CLI 的额度用完集合里没有它）：只看了自动分析，没有人工核对（未确认）。

核对时发现的旧缺口（5.8.3 里就有，1.1.2 对齐 5.8.3 时漏看了）：对话请求经 OpenAI SDK 发出时带的 `X-Stainless-*` 和 `Accept: application/json`，以及后台服务每次提问前加的 `X-Device-Token` 和 `X-Moderation-Type`。其中 `X-Stainless-*` 与 `Accept` 2026-10-28 已照官方补上（读了两域 5.9.3 / 5.8.4 的 mac 包核对，见[几类请求](#几类请求)里的对话）；`X-Device-Token` 同日改为对话也带（只在使用者填了 `device_token` 时——网关做不出真令牌，没填就不发，见[几类请求](#几类请求)）；`X-Moderation-Type` 同日也补上——专家对话里按该专家是不是云端 / 付费发（`shouldProtectSession`），普通对话不带（普通对话本来就不带专家），见[几类请求](#几类请求)里的对话。

同类项目怎么做（2026-10-28 在 GitHub 上搜代码、读默认分支的源码）：

- 版本：[magpie-community/plugins](https://github.com/magpie-community/plugins/tree/main/packages/workbuddy) 的 WorkBuddy 插件当天也换成国内版 5.9.3 / 2.165.0、国际版 5.8.4 / 2.161.0，[YahooYuan666/BargainProxy](https://github.com/YahooYuan666/BargainProxy) 的测试里也是 5.9.3；[maiphucgiang/codebuddy2api](https://github.com/maiphucgiang/codebuddy2api)、[kzxheli-commits/dsh-workbuddy-pool](https://github.com/kzxheli-commits/dsh-workbuddy-pool) 停在 5.9.2（也带 2.165.0）。
- `X-Agent-Runtime`：magpie、BargainProxy 只对国内版发 `local-agent`，和网关当时一样；[workbuddy2api-hub](https://github.com/ardeyouxipianyi/workbuddy2api-hub)、[Parrot](https://github.com/danger-dream/Parrot)、[Cartethyia](https://github.com/risunCodex/Cartethyia) 发 `other`，它们仿的是独立 CLI，不是桌面端。
- `X-Private-Data`：仿桌面端的都发 `"true"`（workbuddy2api-hub、codebuddy2api、[CangShui/workbuddy-gateway](https://github.com/CangShui/workbuddy-gateway)、[modelmux](https://github.com/bluechonk/modelmux) 等）；仿独立 CLI 的个人号发 `false`、企业号发 `true`。codebuddy2api 也把账号类型 `ultimate`、`exclusive` 当企业号。
- `X-Moderation-Type`：同类项目没有一家发；网关 2026-10-28 起在专家对话里按该专家的云端 / 付费属性发。
- `X-Device-Token`：没有一家自己造。magpie、CangShui/workbuddy-gateway 对话不带；workbuddy2api-hub（只对国内版）、modelmux、[codebuddy-intl-cpa](https://github.com/zidanefaqih/codebuddy-intl-cpa)、BargainProxy 在使用者填了真令牌时才带；[jzh-1228/wb2api](https://github.com/jzh-1228/wb2api)、[weecliz/workbuddy2api](https://github.com/weecliz/workbuddy2api) 在装了桌面端的 Windows 机器上调官方的图灵盾 SDK 现取。workbuddy2api-hub 仿 VSCode 插件时，个人号没有令牌就发 `X-Device-Token-Error: sdk_not_initialized`，仿桌面端时不发。
- `X-Stainless-*`：magpie 照官方补齐了 8 个，`OS`、`Arch` 取它自己所在机器的值；workbuddy2api-hub 仿独立 CLI 时发，仿桌面端时要设 `WB_DESKTOP_CHAT_COMPAT=1` 才发，而且不带 `Timeout`（它注明桌面端的超时没核对；内置 CLI 的客户端超时是 1200 秒，桌面端没有用 `CODEBUDDY_FIRST_TOKEN_TIMEOUT_MS` 改它）。网关 2026-10-28 也补齐了 8 个，但 `OS`、`Arch` 不取网关所在机器（网关在 Linux），而是照设备指纹（`deviceOS` / `deviceArch`，Mac arm64）映射，和设备信息同源，`Runtime-Version` 取桌面端 Electron 的 Node 版本。
- 链路头：magpie 的说明写官方「只在服务端追踪打开时」才发 `traceparent`。实际上两域 5.9.3 / 5.8.4（以及国际版 5.9.4）的 `product.json` 里 `telemetry.tracing.enabled` 和 `galileo.enable` 默认都是 `true`，桌面端后台每次提问建 `daemon.prompt.forward` span 把 `traceparent` 塞进 `_meta`，内置 CLI 的 `injectOtelSpanHeaders` 据此发 `traceparent` / `b3` / `X-B3-*`——也就是官方默认就在发，网关每条都发是对的（10-20 对齐），magpie 那句对默认配置不成立。采样器 `fraction: 1e-4` 但 `enableParentSample: true`，父 span 由后台建好，所以子请求跟着采。

### 与国际版桌面端 5.9.4 的对照

2026-10-30 国际版桌面端出了 5.9.4（更新接口给的版本是 `5.9.4.42593613`），内置 CLI 2.165.0。监控仓库当天自动下载 macOS 和 Windows 两个包、比对出变化；2026-10-31 人工解开 macOS 包（只解包阅读），主要拿它和国内版 5.9.3 比，涉及 5.9 新改动的地方再和 5.8.3 / 5.8.4 按函数比：

- 内置 CLI：去掉压缩后的变量名，和国内版 5.9.3 内置的 2.165.0 相同，只差构建产物。所以对话、模型目录、联网搜索、备用入口这些由内置 CLI 发的请求，两域现在同一个形状。
- 后台服务 `main/`：327 个文件逐个归一化后和国内版 5.9.3 比，只有三处不同：`account.js` 的订阅档压低（见下）；`main-bootstrap.js` 的 Windows 下载地址（mac 包里是空的）；`server.js` 的 `wrapFacade`、`wrapHttpWithLogging` 改成只在已有活动 span 时才建 span，只影响官方自己上报的链路数据，后台没有往请求头里注入链路的代码，请求头不变。
- 界面 `renderer/`：和国内版 5.9.3 比只差界面上的东西（订阅档压低的同一处改动、多语言开关的默认值、专家列表的加载状态、样式和图标）。
- `product.json`：`applicationName` 还是 `workbuddy-ai`，`deploymentType` 还是 `SaaS`；Electron 37.10.3、内置 Node `v22.21.1` 和 5.8.4 相同。产品开关比 5.8.4 多了日、法、韩语界面、记忆平台 v2、PDF 外部包，少了消息中心入口和安全中心两项，都和网关无关；和国内版的差异见[产品开关](#产品开关)。

变了、网关跟着改的：

- 版本号：桌面端 5.9.4，内置 CLI 2.165.0（`defaultClientVersionGlobal`、`defaultCliVersionGlobal`），两域的 CLI 现在是同一版。
- 上报的 `commit`、`releaseDate` 改成按账号所在的域取，国际版是 mac 包 `product.json` 的 `fa0a0901906addaa1a4259f6df347388a5563ded`、`2026-10-29T16:17:33.510Z`。以前国际版号的每日活跃上报带的是国内版的值。
- 两域的对话和联网搜索都发 `X-Agent-Runtime: local-agent`：国际版 5.9.4 的会话元数据同样放 `agentRuntime: local-agent`（`conversations.js` 和国内版相同）。
- `X-Private-Data`：国际版换成 2.165.0 的算法，产品开关 `DefaultModelOptimizationOff` 在国际版包里是 `true`，没动过「模型优化」开关时个人号、企业号都发 `"true"`，和网关的默认相同，取值不用改，见[几类请求](#几类请求)里的对话。
- 积分查询照 5.9.x 改了四处，见[积分与签到](#积分与签到)：付费包多了 BuddyOne 会员；国内版纯免费号不查付费包；版本档先看汇总的订阅包；汇总没查到时，同时有几个订阅档只算排在最前的一个。前三处国内版 5.9.3 里就有，1.8.0 对齐 5.9.3 时只比了请求头和上报，漏看了积分；第四处 5.9.3 只对国内版做，5.9.4 改成两域都做，体验包的两档也是 5.9.4 才排进去。另外，国际版的免费月包和礼包官方一直（5.8.3 起）当一次性的包显示，网关以前当刷新的包算到期，这次一起改了，见[积分包的到期时刻](#积分包的到期时刻)。

没变的：两个 UA 拦截器、请求框架的公共头、后台服务每次提问前加的头、上报事件的处理器、容量排队（`conversations.js` 和 `server.js` 里的两套都相同）。登录的几个函数（`fetchAuthState`、`loopGetToken`、`loopGetAccount`、`fetchAccounts`、`getAccountSnapshot`）从 5.8.3 到 5.9.x 也没变；签到的四个函数只多了产品开关 `DisableCheckin` 的判断（国际版是 `true`，官方国际版不签到，网关本来就对国际版跳过）。

新出现、网关没跟的：

- `oneId`、AICC 保密模型通道、业务码 `11145`：同国内版 5.9.3，见上一节。
- 5.9 起每次查积分汇总都另报一条 `wb.api_response` 日志（`reportApiResponseLog`，进官方遥测）。网关不发客户端日志，不跟。
- 5.9 起专家列表有页缓存（`ExpertRequestCache`：没有关键词、不要付费资产时才缓存），请求形状不变。
- 5.9 起窗口切回前台时（30 秒内最多一次），令牌 10 分钟内到期就先刷新（`refreshSession` 的 `minValidityMs`）。只是刷新的时机，网关按自己的节奏刷新，不跟。

核对时看到的旧缺口，没改：国际版的上报事件官方另带本机的公网 IP（`clientIp`，只有国际版查）和系统时区；网关不带 `clientIp`，时区固定 `Asia/Shanghai`。网关给国际版号发的上报只有每日活跃上报一种。

同类项目怎么做（2026-10-31 在 GitHub 上搜代码、读默认分支的源码）：

- [misakano7545/workbuddy2api-panel](https://github.com/misakano7545/workbuddy2api-panel) 10-30 已跟到国际版 5.9.4（[che246/ddy2panel](https://github.com/che246/ddy2panel) 是同一份代码），两版都有真机抓包：对话的 UA 和网关相同；后台服务的请求是 `<应用名>/<版本> <应用名>/<版本> CLI/<cli>`；积分、签到这类请求是 `axios/1.16.1`；`/v3/config` 是两段的 `WorkBuddy/<版本> CLI/<cli>`。它的 `releaseDate` 取自真机（Windows 包）的上报，和 mac 包 `product.json` 的 `date` 不同（国际版早约 31 分钟）；网关的设备指纹是 Mac，取 mac 包的值。
- [YahooYuan666/BargainProxy](https://github.com/YahooYuan666/BargainProxy)、[jin66god/workbuddy2api-panel](https://github.com/jin66god/workbuddy2api-panel) 也换成了国际版 5.9.4 / 2.165.0，后台请求的 UA 两段都是 `workbuddy-ai`；BargainProxy 两域都发 `X-Agent-Runtime: local-agent`。
- [magpie-community/plugins](https://github.com/magpie-community/plugins/tree/main/packages/workbuddy) 国际版写的是 5.9.3（注释说是 10-29 更新接口给的版本），两域都发 `X-Agent-Runtime: local-agent`。监控仓库看到的国际版是 5.8.4 直接到 5.9.4，它说的 5.9.3 没核实。
- [BegoniaHe/wokbuddy-demon](https://github.com/BegoniaHe/wokbuddy-demon) 已把 BuddyOne 会员包 `TCACA_code_043` 加进付费包。

核对同类项目时发现的旧缺口（1.1.2 起就有，2026-10-31 回官方 5.8.3、5.9.4 安装包核实后改了）：

- 积分查询（汇总和两个逐包列表）、企业用量、签到状态和签到这 6 个请求，官方后台服务不走请求框架，用 `AuthService` 自己的 axios 实例发（`authority-snapshot.js` 的 `get http`，5.8.3 在 `tar.js`），头见[积分与签到](#积分与签到)。网关以前按请求框架发（后台服务的 UA 加 `X-Requested-With`、`X-Product`、`X-Department-Info` 等）：1.1.2 对齐时读的是 `account.js` 里的 `BackendProvider`，它用到 `document`、`localStorage`，是界面层的代码，读错了地方。
- 后台服务的 UA：后台服务的 `platform` 取产品配置的 `applicationName`（`WorkbuddyClientInfoProvider.initClientInfo`）。桌面端只给内置 CLI 的进程设 `CLIENT_INFO_PLATFORM=WorkBuddy`，后台服务（默认作为子进程启动，环境变量见 `buildDaemonProcessEnv`）没有这个变量，所以国际版后台请求的 UA 两段都是 `workbuddy-ai`。网关以前第二段发 `WorkBuddy`；国内版两段本来都是 `WorkBuddy`，不受影响。
- 刷新令牌：桌面端由后台服务刷新（`common2.js` 里两个登录提供者的 `refreshSession`），UA 是后台服务的。网关以前用对话的 UA，国际版第一段对不上；刷新后取账号一起改了。
- 没改的：官方 `/v3/config` 也由后台服务拉（`CloudProductProvider`），带查询参数 `repos`（本机 git 仓库的信息）。后台服务在产品配置载入前用兜底的客户端信息，那时 UA 只有 `WorkBuddy/<版本> CLI/<cli>` 两段，载入后是上面的三段；内置 CLI 也可能自己拉。什么时机发哪种 UA、`repos` 为空时怎么发，未确认。网关仍按对话的 UA 拉，不带 `repos`。

## 几类请求

| | 对话 | 上报、外观、专家列表、登录、刷新令牌 | 积分、签到 | 成长中心、积分补偿、新手礼包、试用、账号资料 | 联网搜索 |
|---|---|---|---|---|---|
| 谁发 | 内置 CLI | 后台服务 | 后台服务的 `AuthService` | 官网网页（桌面端在浏览器里打开） | 内置 CLI 的 WebSearch 工具 |
| UA | `WorkBuddy/<版本> <应用名>/<版本> CLI/<cli>` | `<应用名>/<版本> <应用名>/<版本> CLI/<cli>` | `axios/1.16.1` | 浏览器的 UA | `CLI/<版本>` |
| 来源 | `UserAgentHttpInterceptor` | `WorkbuddyUserAgentHttpInterceptor` | `AuthService` 自己的 axios 实例（`get http`），头是 `buildHeaders` | 官网前端的 axios 实例，靠 Cookie 认人 | `performWebSearch`，Node 原生 `fetch` |
| 网关实现 | `ChatHeaders` | `BillingHeaders`、`frameworkHeaders`、`RefreshHeaders`、`RefreshToken` | `authServiceHeaders`、`authServiceJSON` | `webHeaders`、`webJSON`、`websession.go` | `searchHeaders`、`Search` |

国内版的应用名是 `WorkBuddy`，对话和后台服务的 UA 一样；国际版是 `workbuddy-ai`，两种 UA 第一段不同。后台服务的 UA 两段都是应用名、刷新令牌由后台服务发，是 2026-10-31 核对后改的，见[与国际版桌面端 5.9.4 的对照](#与国际版桌面端-594-的对照)。

对话和后台服务这两类请求经过官方请求框架（`restOperations`）；积分、签到和联网搜索不经过，头分别见[积分与签到](#积分与签到)和[联网搜索](#联网搜索)。内置 CLI 和后台服务各有一套框架，挂的拦截器相同，所以共用这些头：

- `Accept: application/json, text/plain, */*`（对话例外，见下文），有请求体的请求带 `Content-Type: application/json`（桌面端的 axios 对对象请求体只发这个值）；
- `X-Requested-With: XMLHttpRequest`；
- `X-Request-ID`：32 位十六进制，每个请求新生成（`CommonHeaderHttpInterceptor`；对话请求另用消息 id）；
- `X-Product: SaaS`：产品配置里的 `deploymentType`，两个安装包都是 `SaaS`（`ProductEndpointHttpInterceptor`）；
- `Authorization`、`X-User-Id`，企业号再带 `X-Enterprise-Id` 与同值的 `X-Tenant-Id`；
- `X-Domain`：登录域；
- 账号有部门全名（`account.departmentFullName`，企业号才有）时带 `X-Department-Info`，值原样（`AuthenticationHttpInterceptor`）。网关从 1.1.2 起把它存进凭证文件的 `account.departmentFullName`：登录时取，刷新令牌后随取账号更新。官方用 Node 发请求，值里有超出 Latin-1 的字符（比如中文）时 Node 会直接报错，所以上游返回的大概是 ASCII 或编码过的值；没有企业号实测过，网关照原样发；
- `Accept-Encoding: gzip, compress, deflate, br`：axios 的 http 适配器自动加的（内置 CLI 的 `advertiseZstdAcceptEncoding` 默认关，不带 zstd），官方没有在哪个请求上改写。网关按 `Content-Encoding` 解压（`encoding.go`）；
- `X-Client-Version`、`X-Client-Platform: macos`、`X-Client-Product`（国内版 `WorkBuddy`、国际版 `workbuddy-ai`）：桌面端 5.11.3 / 5.11.4 起通过 `app/main/client-identity.js` 与 `proxy-env2.js` 对第一方来源统一注入，并由 `sidecar-manager.js` 经 `CODEBUDDY_CUSTOM_HEADERS` 透传给内置 CLI；网关在全部桌面端出站通道（对话、模型目录、登录、刷新、积分/签到、上报、外观、专家列表、联网搜索、容量排队）补齐，`client_name="SaaS"` 旧行为不发，且绝不覆盖小程序或官网网页自身的身份头。

官方这两类请求都不发 `Origin`、`Referer`、`X-CodeBuddy-Request`、`X-Machine-ID`、`X-Session-ID`。个别请求另带的头：

| 头 | 只出现在 | 官方来源 |
|---|---|---|
| `X-Device-Token` | 登录的发起授权；对话也带，见下文 | `WorkbuddyAuthStateHeaderProvider` |
| `X-Refresh-Token`、`X-Auth-Refresh-Source: plugin` | 刷新令牌 | `refreshSession` |
| `Content-Type: application/json;charset=UTF-8` | 上报 `/v2/report` | `StandardEventService.send` |

对话请求另有这些头和字段：

- 对话请求由 OpenAI Node SDK 6.25.0 组好，再经内置 CLI 的 `axiosToFetchAdapter` 交给请求框架发出：SDK 加的头只去掉 `Authorization` 和 `User-Agent`，其余照发（只有设了跳过内部头的环境变量时才删）。所以对话的 `Accept` 是 SDK 的 `application/json`（`chat/completions` 不是二进制端点，走 `buildHeaders` 的默认 `Accept`；这版 SDK 流式也不改 `Accept`，由请求体 `stream:true` 决定流式），另带 8 个 `X-Stainless-*`：`Retry-Count: 0`（`maxRetries` 为 0，只发一次）、`Timeout: 1200`（SDK 把请求级超时回落到客户端超时，桌面端 `resolveRequestTimeoutMs` 默认 1200000ms，/1000）、`Lang: js`、`Package-Version: 6.25.0`、`OS: MacOS`（`normalizePlatform(process.platform)`，darwin → MacOS）、`Arch: arm64`（`normalizeArch(process.arch)`）、`Runtime: node`、`Runtime-Version`（运行 CLI 的 Node 的版本：桌面端以 `ELECTRON_RUN_AS_NODE` 在 Electron 里跑 CLI，两域 5.9.3 / 5.8.4 的 mac 包都是 Electron 37.10.3、内置 Node `v22.21.1`，2026-10-28 分别读 `Electron Framework` 实测，国际版 5.9.4 也一样）。SDK 所在的分块（`lazy-lite-wb/3043.50ee4dca.mjs`）5.8.3 和 5.9.3 一字不差，国际版 5.9.4 内置的 CLI 和国内版 5.9.3 相同。网关 2026-10-28 起照这组值发（`injectSDKHeaders`）：`OS` / `Arch` 不取网关所在机器而是照设备指纹（`deviceOS` / `deviceArch`）映射，与设备信息同源；`client_name="SaaS"`（旧行为）不伪装桌面端，不发 `X-Stainless-*`，`Accept` 回落 `application/json, text/event-stream`；
- `X-Agent-Intent: craft`、`X-Agent-Type: main`、`X-Agent-Purpose: conversation`；
- `X-Agent-Runtime: local-agent`：2.165.0 起才有（国内版 5.9.3、国际版 5.9.4），取会话元数据的 `agentRuntime`，桌面端给本机的会话设 `local-agent`；国际版 5.8.4 内置的 2.161.0 不发。网关两域都发（国际版 2026-10-31 起）；
- `X-IDE-Name` / `X-IDE-Type: WorkBuddy`，`X-IDE-Version` 为桌面端版本；
- `X-Private-Data`：告诉上游能不能拿这段对话做模型优化，`"true"` 是不允许。2.161.0（国际版 5.8.4，以及国内版 5.8.3）：设置里的 `enableModelOptimization` 设过就照设置；没设过、且产品开关 `DisableYuanbaoChannel` 为真（两个域的安装包都是）时发 `"true"`。2.165.0（国内版 5.9.3、国际版 5.9.4）改了：设过照设置；没设过时先看产品开关 `DefaultModelOptimizationOff`（国内版包里是 `false`、国际版是 `true`，为真时直接发 `"true"`；经 `abTestService` 读，远端配置会不会改它未确认），再看是不是企业版（`isEnterpriseEditionAccount`：有企业 id，或账号类型是 `ultimate`、`exclusive`），结果国内版个人号发 `"false"`、企业号发 `"true"`，国际版都发 `"true"`。网关默认一律发 `"true"`，国际版和官方没动过开关时一样，对国内版等于用户在桌面端手动关了这个开关（2026-10-28 定）；打开 `upstream.model_optimization` 时个人号发 `"false"`、企业号仍发 `"true"`，和 5.9.3 没动过开关时一样（5.9.3 手动打开的话企业号也发 `"false"`，网关取保守的一边）；
- 后台服务每次提问前另加的头（`createWorkbuddyAppServerBeforePromptExpertActivation`）：经 `switchExpertPluginForSession` 设成内置 CLI 的 `internalModelRequestHeaders`，内置 CLI 只加在官方模型的请求上（`shouldApplyInternalModelRequestHeaders`，自定义模型不加）。5.8.3、5.9.3 和国际版 5.9.4 的这段代码相同：
  - `X-Device-Token`：腾讯图灵盾（TuringShield）原生 SDK 给的设备令牌（`getTuringDeviceToken`；mac 包里是 arm64 的 `native/turing-sdk/build/Release/turing_sdk.node`，SDK 只支持 macOS 和 Windows）。令牌缓存着用，到了刷新间隔就在后台换新；没有令牌而有错误码时改发 `X-Device-Token-Error`（错误码只留 ASCII 可见字符、截到 64 个），两样都没有时都不发。远端产品开关 `TuringDeviceToken` 为 `false` 时停用；
  - `X-Moderation-Type: ["protected"]`（值就是这串字符，带方括号和引号）：专家是云端专家（`expertIsCloud`）或付费专家（`expertPayMode`），或启用的技能里有云端技能时加（`shouldProtectSession`）；
  - 专家对话的 `X-Expert-Id` 和团队专家的 `X-Expert-Team-Task: true`，见本节最后一条。

  网关的对话按官方也带 `X-Device-Token`，但只在使用者填了 `device_token` 时——网关在 Linux 做不出真令牌，没填就不发，对应官方「令牌和错误码都没有时都不发」的状态，不造 `X-Device-Token-Error`；`X-Moderation-Type` 2026-10-28 起在专家对话里按该专家是不是云端（`is_cloud===1`）或付费（`pay_detail` 的 `pay_mode`，回落 `need_pay===1`）发 `["protected"]`（`MarketExpert.Protected`，对齐官方 `shouldProtectSession`；网关的专家对话不加载技能，「云端技能」那条恒假），普通对话不带专家所以也不带；
- `X-Conversation-ID`：会话 id，整段对话不变。新会话的 id 是内置 CLI 建会话时生成的 `uuidv7()`，带连字符的 36 位，前 48 位是毫秒时间戳（`sessionManager.create` 的 `id ?? uuidv7()`；桌面端新建会话不传 id）；
- `X-Conversation-Request-ID`：轮次 id，每次用户发言开一轮时生成，是 `uuidv7()` 去掉连字符（`generateConversationRequestId`）；
- `X-Conversation-Message-ID`、`X-Request-ID`：同一个模型消息 id，每次调模型新生成，同样是 `uuidv7()` 去掉连字符（`withModelMessageCorrelation`）。上报事件里的会话 id、轮次 id、消息 id 也是这三种形状。网关客户端没给会话 id 时的做法见[会话粘性](gateway.md#会话粘性)；
- 链路头 `traceparent`、`b3` 与 `X-B3-*`；
- 请求体补充字段（`internal/upstream/official_body.go`）：
  - 带了思考档位（`reasoning_effort` 或 `reasoning.effort`）时补 `reasoning_summary: "auto"`（`injectReasoningSummaryIfNeeded`）；
  - `cache_retention`：2.161.0、2.165.0 不发；2.176.0（桌面端 5.11.3 / 5.11.4）由 `axiosToFetchAdapter` 对未带该字段的官方模型请求补 `"long"`（`resolveCacheRetention` 默认 `"long"`，仅 `CODEBUDDY_DISABLE_PROMPT_CACHING_1H` 为真时回落 `"short"`）。网关对未显式传该字段的请求默认补 `"long"`，保留客户端显式值，仍不发 `prompt_cache_key`；
  - `extra_vars`：2.176.0 对官方对话请求（带 `messages` 数组）合并写入 `extra_vars.harnessMetadata`（JSON 字符串，含 `schemaVersion: 1`、`harnessVersion: <cliVersion>`、`stepIndex`、续步时 `previous.conversationMessageId` 与 `agent.depth: 0`；由 `internal/session/harness.go` 按 `X-Conversation-Request-ID` 跟踪并在失败重试时复用消息 id 与步数）及 `extra_vars.initiator`（仅末尾消息 `role == "user"` 且未显式指定时写 `"user"`，末尾为 `tool` 等非 `user` 角色时不写），保留已有其他 `extra_vars` 键；
- 任务中心的专家对话就是一次普通对话，只多带 `X-Expert-Id`（专家插件激活时追加）；团队专家（专家列表里 `expertType` 为 `team`）另带 `X-Expert-Team-Task: true`（后台服务的 `applyPreparedExpertSettings`），网关按专家列表里的类型带。

模型目录：网关用对话的 UA，按 GET 拉 `/v3/config`，带 `Connection: close`。官方后台服务也拉（`CloudProductProvider`，另带查询参数 `repos`），UA 跟时机有关，未确认，见[与国际版桌面端 5.9.4 的对照](#与国际版桌面端-594-的对照)。企业号另拉企业模型，见[企业号模型目录](#企业号模型目录)。

## 联网搜索

内置 CLI 的 WebSearch 工具（2.165.0 / 2.176.0 的 `performWebSearch`，2026-10-31 读国内版 5.9.3 与国际版 5.9.4 安装包、2026-11-24 复核国内版 5.11.3 与国际版 5.11.4）不经请求框架，用 Node 原生 `fetch` 发 `POST <对话入口>/agenttool/v1/search`，20 秒超时。网关照这份发（`upstream/search.go`），不复用对话的头（原版 linguo2625469 是复用 `ChatHeaders` 再改几项）：

- 请求头只有这些：`Accept: application/json`、`Content-Type: application/json;charset=UTF-8`、`X-Requested-With: XMLHttpRequest`、`User-Agent: CLI/<版本>`（版本是客户端信息里的 `platformVersion`，取产品配置的 `productVersion`，在桌面端里就是桌面端版本；`CLI` 是默认平台名常量）、`Authorization`、`X-Agent-Intent`（会话的模式，默认 `craft`）、`X-Agent-Runtime`（同对话，见[几类请求](#几类请求)）、`X-Conversation-ID`、`X-Conversation-Request-ID`（没有时是空串）、`X-Conversation-Message-ID` 与同值的 `X-Request-ID`（每次新生成的 `uuidv7()` 去连字符）、`X-Product`、`X-IDE-Type`、`X-IDE-Name`、`X-IDE-Version`（同 UA 里的版本）、`X-User-Id`、`X-Department-Info`（总是带，没有部门时是空串），5.11.3 / 5.11.4 起再经 `customHeadersProvider.applyTo` 带桌面端透传的 `X-Client-Version`、`X-Client-Platform: macos`、`X-Client-Product`（国内 `WorkBuddy`、国际 `workbuddy-ai`）。不带企业头（`X-Enterprise-Id`、`X-Tenant-Id`）、`X-Domain`、`X-Stainless-*` 和链路头；`Accept-Language: *`、`Sec-Fetch-Mode: cors`、`Accept-Encoding: br, gzip, deflate` 是原生 fetch（undici）自己加的。内网版（IOA）的默认头、专家会话的自定义头官方另加，网关都不涉及。`client_name` 配成 `SaaS`（旧行为）时同对话，不带 `X-IDE-*` 和 `X-Client-*`；
- 请求体：`{"query": <搜索词>, "type": "text2text", "max_results": 5, "allowed_domains": [...], "freshness": "..."}`，后两个有才带，字段顺序照官方；域名黑名单不单独传，拼在搜索词后面：`<搜索词> (-site:a.com -site:b.com)`。搜索词至少 2 个字、`freshness` 的写法（`d`、`d1`～`d30`、`m`、`m1`～`m12`、`y`、`y1`～`y5`），官方在发请求前就校验，网关同样；
- 回应：HTTP 不是 2xx，或回应体的 `code` 不是 0，官方都当失败；网关按[错误码](#错误码)归类，决定罚不罚号（见 [API 参考](api.md#联网搜索)）。结果按 `title`（空的写 `Untitled`）、`url`、`snippet`、`site`、`favicon` 整理，链接不是 http/https 的条目去掉，图标不安全的只去掉图标，去掉过条目时 `total_results` 改成剩下的条数（`filterUnsafeResults`）。官方还拿本地的恶意域名库再筛一遍，网关没有这个库，不筛；
- 交给模型的文字照官方 `formatSearchResults`：`# Search Results for "<搜索词>"`，每条是 `## <序号>. [<标题>](<链接>)`、摘要、`**URL:** <链接>`、`---`，最后一行 `*Found N result(s) • Provider: <提供方> • Search performed on <UTC 日期>*`；没有结果时是 `No search results found for query: "<搜索词>"`，结果都被筛掉时是 `No safe search results found for query: "<搜索词>"`。

没确认的：

- 会话头：官方带的是当前会话的 id，网关转客户端给的 `X-Conversation-ID`、`X-Conversation-Request-ID`，没给就各生成一个，和主对话的会话 id 不关联，上游会不会核对未确认；
- 搜一次扣不扣积分、扣多少：没在真实账号上测。

## 积分与签到

官方桌面端的积分、签到由后台服务的 `AuthService` 发（5.9.x 在 `authority-snapshot.js`，5.8.3 在 `tar.js`），不经请求框架：它自己建一个 axios 实例（`get http`：keep-alive，拦截器只挂代理、记日志、加 `X-Request-ID`，401 时刷新），请求头只有 `buildHeaders` 给的这些：

- `Accept: application/json`、`Content-Type: application/json`；
- `User-Agent: axios/1.16.1`：没人设 UA，axios 的 http 适配器补默认值（后台服务打包的 axios 是 1.16.1）；`Accept-Encoding: gzip, compress, deflate, br` 同样是 axios 补的；
- `X-Request-ID`：每个请求新生成；
- `Authorization`、`X-User-Id`（没有 uid 时是空串），企业号再带 `X-Enterprise-Id` 与同值的 `X-Tenant-Id`；有登录域时带 `X-Domain`；
- 5.11.3 / 5.11.4 起经 `proxy-env2.js` 对第一方来源统一补 `X-Client-Version`、`X-Client-Platform: macos`、`X-Client-Product`（`client_name="SaaS"` 旧行为不发）；
- 签到状态和签到另带 `X-Device-Token`（5.9.x 为 `buildHeadersWithTuringToken`，5.11.x 迁至全局 `device-token.js` 注册表，令牌和错误码的规则同对话，见[几类请求](#几类请求)）；积分查询另带 `Accept-Language`（`resolveAcceptLanguage`：界面语言是中文发 `zh`、英文发 `en`，都不是时国际版 `en`、国内版 `zh`，网关按域取）。

不带 `X-Requested-With`、`X-Product`、`X-Department-Info`、`X-IDE-*` 和链路头。网关照这组发（`authServiceHeaders`、`authServiceJSON`），配了 `upstream.user_agent` 时 UA 照配置；`client_name="SaaS"`（旧行为）时不带 UA，交给 Go 的默认值。1.8.0 及以前按请求框架的形状发，见[与国际版桌面端 5.9.4 的对照](#与国际版桌面端-594-的对照)。

积分查询（`getPersonalUsage` → `account.js` 的 `fetchUserResources`）发三个请求，两个域路径相同，都不带 `/v2`：

| 请求 | 请求体 |
|---|---|
| `POST /billing/meter/get-user-resource-summary` | `{}` |
| `POST /billing/meter/get-user-resource-paid-packages` | 付费包代码、`Status: [0, 3]`、`NeedRenewInfo: true`，每页 200 条，最多 10 页 |
| `POST /billing/meter/get-user-resource-free-packages` | 免费包代码、`Status: [0, 3]`，加上当天（台湾时间）的 `SlicePeriodStartTime` / `SlicePeriodEndTime` |

- 国际版三个同时发。国内版先发汇总和免费包，汇总显示是纯免费号（`isFreeOnlySummary`：`IsPaidUser` 为假、订阅包没有或是免费包、各档都是免费包，一档都没有不算）就不查付费包（`skipPaidForFreeUser`，5.9 起；调用方带 `force` 时照查，网关一律按不带算）；汇总没查到照查。
- 付费包代码 5.9 起多了 BuddyOne 会员 `TCACA_code_043`，5.8.3 / 5.8.4 没有。
- 两个列表按 `ResourceId` 去重；按天发放的包（`CapacityType` 4）有当天的发放明细时用明细，其余用 `CycleCapacitySizePrecise` / `CycleCapacityRemainPrecise`（字符串）。
- 余额以汇总为准；汇总没查到时用逐包合计，同时有几个订阅档只算排在最前的一个（`excludeSuppressedPlanBase`：先比档位，BuddyOne 会员、旗舰、高级、专业、青春、专业试用、免费月包依次往后，再让没用完的、额度大的在前）。这条 5.9.3 只对国内版做，5.9.4 起两域都做，专业试用那两档也是 5.9.4 才排进去。
- 官方三个请求任一失败，这次查询就整次算失败、不显示积分（`refusing to report incomplete credits`）。网关从宽：两个列表任一失败算查询失败，汇总失败时照用逐包合计（2026-10-20 生产实测两者一致）。
- 版本档（外观上报的 `editionType`）先看汇总的订阅包（5.9 起），认不出再看逐包列表里有哪档订阅包。
- 5.9 起官方每次查汇总都另报一条 `wb.api_response` 日志，网关不发。
- 1.1.2 以前用 `/v2/billing/meter/get-user-resource`，官方 5.8.3 里已经没有这个接口。2026-10-20 生产对比：新旧接口给的包相同，按整数字段算出的旧余额和汇总对不上，按官方算法算的一致。
- 企业号查 `POST /v2/billing/meter/get-enterprise-user-usage`（`getEnterpriseUsage`），头同上，带企业头。
- 1.8.0 对齐国内版 5.9.3 时只比了请求头和上报，漏看了积分查询，BuddyOne 会员、纯免费号不查付费包、版本档先看汇总这三处 2026-10-31 对齐国际版 5.9.4 时才补上。

### 积分包的到期时刻

官方后台服务的 `getPersonalUsage` 按包类型取时刻（5.8.3 起如此；界面层 `account.js` 的 `getCurrentPlan` 是同一套，只是不分域：免费月包 `008` 一律当刷新的，礼包 `006` 两样都显示），网关的到期提醒、快照和快过期加权都照这个办法（`resourceAccount.expiry`）：

| 包 | 官方显示 | 网关算作废的时刻 |
|---|---|---|
| 每日免费包（`TCACA_code_001`） | 到期：`CycleEndTime` | `CycleEndTime` |
| 一次性发放的包：活动包 `007`（签到、任务发的裂变包）、加油包 `009` / `038`、`029`、`030`、国际版加油包 `036`；国际版另有免费月包 `008`、礼包 `006` | 到期：`DeductionEndTime`，不显示刷新 | `DeductionEndTime`，缺失时依次用 `ExpiredTime`、`CycleEndTime`（同官方 `toExpiredCreditPackage`） |
| 其余（订阅、体验类：`002`、`003`、`005`、`023`、`026`、`027`、`028`、`035`、国际版 Bonus Pack `037`、`039`、`040`、`043`，以及国内版的 `008`、`006`） | 刷新：`CycleEndTime` | `CycleEndTime`，缺失时同上回落 |

- 刷新的包按「刷新时没用完的作废」算，会不会结转未确认；
- 2026-10-01 实测上游按 `DeductionEndTime` 先到期先扣（两个号一致）；
- 1.8.0 及以前，面板「检查」一律先读 `DeductionEndTime`（订阅、体验类包会显示成好几年后到期），快照和快过期加权一律读 `CycleEndTime`，两边对不上；国际版的 `008`、`006` 也当刷新的包算。

签到照官方先查状态再签（`AuthService` 的 `getCheckinStatus`、`claimDailyCheckin`，头见上文）：`POST /v2/billing/meter/checkin-activity-status`，活动没开（`active` 为假）就不签，今天已签（`today_checked_in`）也不签；状态查不到时照常签，再 `POST /v2/billing/meter/daily-checkin`。产品开关 `DisableCheckin` 为真（国际版）时官方两条都不发，网关的签到同样跳过国际版号。

### 积分获取的积分数

面板「积分获取」逐笔记的积分数都取官方回应里的值，读法如下：

- 签到：`daily-checkin` 回应 `data.credit`。官方桌面端 5.9.3（`app/main/account.js` 的 `checkin()`）同样读 `credit`、`streak_days`、`is_streak_day`，缺了当 0；没给积分数的一次不记。
- 成长任务领奖（网页、小程序两种）：`data.credit`，`already_claimed` 为真的不记；只给成长值（`energy`）的不记。
- 积分补偿：领取回应的 `credit`，没带就用补偿状态里的 `credit_num`。
- 猫猫旅行：`data.reward_credit`；新手礼包：`data.credit`。
- 连登兑换：桌面端安装包和成长中心网页里都没找到兑换回应的读法，网关记的是连登状态里这一档列出的积分，兑换回应里有没有、是不是同一个数未确认。
- 抽奖不记：奖品形状没读到过（实测的号都没有次数）。

## 登录

官方的登录由后台服务发：国内版是 `AccountScopedExternalLinkAuthenticationProvider`，国际版是 `ExternalLinkAuthenticationProvider`。两个域的路径前缀都是 `/plugin`，`platform` 国内版是 `workbuddy`，国际版是 `workbuddy-ai`。

| 请求 | 路径 | 另带的头 |
|---|---|---|
| 发起授权 | `POST /v2/plugin/auth/state?platform=<platform>`，请求体 `{}` | `X-Domain`（登录域）、`X-Custom-Channel-Id: unknown`、有设备令牌时 `X-Device-Token`、四个 `X-No-*: true` |
| 轮询令牌 | `GET /v2/plugin/auth/token?state=<state>` | 四个 `X-No-*: true` |
| 取账号（国内版） | `GET /v2/plugin/account` | `Authorization`、`X-Domain` |
| 取账号（国际版） | `GET /v2/plugin/login/account?state=<state>` | `Authorization`、`X-Domain`、`X-No-User-Id` 等三个 |

四个 `X-No-*` 是 `X-No-Authorization`、`X-No-User-Id`、`X-No-Enterprise-Id`、`X-No-Department-Info`，告诉请求框架这条请求不要补账号头，会原样发出去。国际版取到账号后，官方还会用新令牌取一次账号列表 `GET /v2/plugin/accounts`（`getAccountSnapshot`），网关照做，失败只记日志。

链路头：登录和刷新令牌都用 `traceService.startTrace` 新开的根 span，请求带 `X-Trace-ID`、`b3: <traceId>-<spanId>-1-`（父 span 为空）、`X-B3-TraceId`、`X-B3-ParentSpanId`（空值）、`X-B3-SpanId`、`X-B3-Sampled: 1`。从登录界面发起的登录另开「登录观测」：每次登录一个 `loginSessionId`（UUID，trace id 就是它去掉连字符）和一个 `login_owner_id`，每类请求再带一个子 span 的 W3C `traceparent`，同一类请求的多次轮询共用这个子 span。国际版发起授权（`ExternalLinkAuthenticationProvider.fetchAuthState`）把根 span 的头放在了请求头之外，实际不发，只有 `traceparent`。面板添加账号、命令行登录都按从登录界面发起处理，命令行登录把这条链路存进 state 文件，`poll` 时沿用。

授权页地址照 `openAuthUrl` 追加参数：两个域都加 `version=<客户端版本>`；国内版另加 `traceparent`（打开授权页这一步自己的子 span）、`loginSessionId`、`login_owner_id`。

账号授权被拒：发起授权回 HTTP 错误、响应体的业务码是 `12005`（席位已满）、`11212`（授权已过期）或 `11216`（试用已过期）时，官方直接弹出上游的 `msg`（`LICENSE_ERROR_CODES`、`toSignLicenseError`），只认发起授权这一步。网关从 1.5.1 起照做，面板显示「登录被拒：席位已满（code=12005）：<上游提示>」；以前 HTTP 错误的响应体不读，只显示 `http_error: upstream <状态码>`。其他登录请求回 HTTP 错误时，错误文本也带上响应体里的业务码和提示。

轮询令牌：官方除了 `11217` 继续等，其他结果（别的业务码、HTTP 错误、网络错误）都当登录失败，不再轮询。网关的面板遇到这些照旧显示「等待授权完成」，前端每 3 秒继续查，直到 15 分钟后登录会话过期，和官方不同。

`X-Device-Id-List`：官方拿得到设备的 qimei36 时才发（发起授权和对话请求都是）。qimei36 由腾讯闭源的设备标识 SDK 算出（`@tencent/qimei-node`，Windows 上是 `qimei.dll`，主进程起一个 `qimei-helper.js` 去取）。下载的两个 macOS 安装包里 `qimei-node` 只有 `package.json`、没有原生库，也没有打进 `qimei-helper.js`，所以 macOS 版官方客户端拿不到 qimei36，这个头一直不发，上报事件里也没有 `qimei36`。网关按 macOS 版对齐（见[设备与平台](#设备与平台)），同样不发。

2026-10-20 生产实测：两个域按上表发起授权、轮询（未登录时 `code=11217`）、取账号都正常；按上面的规则修饰过的授权页两个域都返回 200；国际版的账号列表是 `data.accounts[]`，里面有本号的 uid。同日用国内版走完了扫码登录：扫码确认后轮询拿到令牌（有效期约 28 天）和账号，按面板的方式落盘、读回，再拉模型目录、查积分包都成功。1.1.2 以前是旧版 CodeBuddy CLI 的形状（UA `CLI/2.63.2 CodeBuddy/2.63.2`、带 `Origin` 和 `Referer`、`platform=CLI`）。

## 刷新令牌

桌面端由后台服务刷新（`common2.js` 里两个登录提供者的 `refreshSession`，国内版 `AccountScopedExternalLinkAuthenticationProvider`、国际版 `ExternalLinkAuthenticationProvider`），经后台服务的请求框架发，UA 是后台服务的 `<应用名>/<版本> <应用名>/<版本> CLI/<cli>`（`refreshUserAgent`；国际版两段都是 `workbuddy-ai`）。内置 CLI 里也有同一份代码，桌面端里它会不会自己刷新没完全确认，形状一样。1.8.0 及以前网关刷新令牌和刷新后取账号用的是对话的 UA，国内版两者相同，国际版第一段不同。

1. `POST /v2/plugin/auth/token/refresh`，请求体 `{}`，头是请求框架的公共头加 `X-Domain`、`X-Refresh-Token`、`X-Auth-Refresh-Source: plugin` 和根 span 的链路头（见[登录](#登录)）；
2. 用新令牌取账号：国内版 `GET /v2/plugin/account`（`fetchCurrentAccount`），国际版 `GET /v2/plugin/accounts`（`getAccountSnapshot`），带同一条链路；
3. 取到的账号照 `mergeCurrentAccountDetails` 合并进本地账号：在 uid 相同的账号里，先挑类型和企业 id 都相同的恰好一个，再挑企业 id 相同的恰好一个（本地有企业 id 时），再挑 `lastLogin` 为真、个人版与否和本地一样的恰好一个（本地没有企业 id 时）；挑出的账号的字段盖到本地，uid 和类型保留本地的。挑不出来就保留本地账号，照常用新令牌。
4. 刷新失败、新访问令牌和旧的一样、取不到账号（国内版 `data` 为空、国际版列表为空），或者身份变了，都算刷新失败，新令牌不保存。身份按 `getAuthenticationAuthorityKey` 比：uid、账号类型、企业 id、`oneidAccountId`、部门全名、`idp`、令牌的 `tokenType` 和登录域，缺这个键和空串算不同的值；本地有 `idp` 而合并后为空时沿用本地的（`normalizeAndAssertRefreshedAuthority`）。

同一时刻只放一个刷新出去：官方 `AuthenticationManager.scheduleRefreshSession` 发现已有刷新在途就等它的结果（日志 `[AuthRefresh] joined: reason=in-flight`）。网关按账号做同样的合并：对话前临近过期的刷新、保活、签到工具同时刷同一个号时，只有第一个真正发请求，其余的等它的结果（照上游 linguo2625469 `3c4eb88`）。上游 #215 报告刷新令牌用过一次再用会回 12153、整条令牌链作废；本仓库早先实测并发两次刷新都成功，没复现，未确认。另外，刷新回 12153 时网关会重读一次凭证文件：文件里的刷新令牌已经被别的进程（例如同时跑的签到工具）换过，就用文件里的令牌再刷一次。

网关从 1.1.2 起把这些字段原样存进凭证文件（账号的 `type`、`oneidAccountId`、`idp`、`departmentFullName`，令牌的 `tokenType`），登录时写，刷新后更新，照上面的规则挑账号、比对身份。1.1.2 以前的凭证文件没有账号类型（账号接口每次都返回 `type`，有没有它就能分出新旧文件），这种文件第一次刷新时挑账号不看类型、只补字段不比对，之后就照官方比对。身份变了时官方要求重新登录；网关和别的刷新失败一样记一次失败，旧访问令牌照用到过期。1.1.2 以前国内版取到的 uid 对不上算刷新失败，官方不算。

到期时间：官方按 `calculateExpiresAt` 用 `Date.now() + 1000 × expiresIn` 算，存的是毫秒；网关的凭证文件存秒。网关从 1.5.1 起读凭证时两种都认，大于 10¹¹ 的按毫秒换成秒。以前原样当秒读，导入的凭证如果是毫秒，网关永远认为令牌还没快到期、不会主动刷新，到期后对话只剩 401。网关自己登录、刷新写的都是秒，不受影响。

2026-10-20 生产实测，两个域各刷新一个个人号：两个令牌都换了；旧的访问令牌刷新后仍能用；`expiresIn` 国内版 2592000（30 天）、国际版 31488222（约 1 年）。两个域的响应都有 `accessToken`、`refreshToken`、`expiresIn`、`refreshExpiresIn`、`domain`、`sessionState`、`tokenType`，国际版另有 `scope`。导回网关时用的是响应里的 `domain`，之后生产上国内版 6 个号的登录域都还是 `www.codebuddy.cn`，可见经 `www.workbuddy.cn` 刷新登录域不变。同日只读取了两个域的账号：个人号有 `type: personal`、`idp`（空串）、`oneidAccountId`（空串）、`lastLogin`（布尔值），没有 `enterpriseId` 和 `departmentFullName` 这两个键。

## 官网网页的请求

成长中心（任务、猫猫旅行、连登、抽奖、补签卡、兑换）、积分补偿、新手礼包和试用只由官网网页发：桌面端点“成长中心”会在浏览器里打开 `https://www.workbuddy.cn/profile/growth-center`。网关按网页的 axios 实例发：

- `Accept: application/json, text/plain, */*`，有请求体时才带 `Content-Type: application/json`；
- `X-Client-Platform: web`；
- 非 GET 请求带 `Origin`，`Referer` 是对应页面（成长中心 `/profile/growth-center`，积分补偿和新手礼包 `/profile/usage`，试用 `/profile`，账号资料 `/profile/account-settings`）；
- 企业号只带 `X-Enterprise-Id`；不带 `X-User-Id`、`X-Domain`、`X-Tenant-Id`、`X-Product`、`X-Request-ID`、`X-Requested-With`；
- 成长中心接口（地址含 `/activity/growth`，包括 `/v2/activity/growth/...`）另设 `Accept-Language`：官网 `config-*.js` 的请求拦截器对这类地址显式设语言头（2026-10-31 读 `ef3f6107` 版，2026-11-24 复核当前 `7b25e1f2` 版 `config-sJViT0yz.js` 及 5 个成长中心懒加载 chunk，拦截器逻辑零变动），读取 `sessionStorage["growth-center-lang"]` 并归一化为 `en* → en-US` / `zh* → zh-CN`（默认 `zh-CN`），桌面端打开成长中心的地址不带 `lang`。网关国内版发 `zh-CN`，国际版按英文页面发 `en-US`（网关不对国际版调成长中心，国际版页面怎么带 `lang` 未确认）。1.8.0 及以前发的是 Chrome 自动带的 `zh-CN,zh;q=0.9`；
- UA 是 Mac 上的 Chrome 152（和上报的桌面端同一台 Mac，见[设备与平台](#设备与平台)；版本取任务中心抓包里的网页上报），`Sec-CH-UA-Platform: "macOS"`，`upstream.user_agent` 配了时用它；
- `Accept-Encoding: gzip, deflate, br, zstd`（Chrome 123 起的默认值；配了自定义 UA 时也发这个）。

认人照官方的流程用网页登录态（`websession.go`）：

1. 桌面端打开官网页面前，后台服务主进程用 Node 内置的 `fetch` 发 `POST <产品入口>/v2/plugin/device/auth/code`，没有请求体，头只有 `Content-Type: application/json`、`X-User-Id`、`Authorization`、企业号的 `X-Enterprise-Id` 与 `X-Tenant-Id`、`X-Domain` 和 `X-Refresh-Token`（`wechatMpCreateDeviceAuthCode`），换回一次性登录码 `deviceCode`（5 分钟有效）。不带 `X-Refresh-Token` 时回 401 `code=10034`。
2. 浏览器打开 `<官网>/console/client-login?code=<登录码>&target=<页面路径>`，网站 302 跳到目标页，同时种下 `session`（7 天，`HttpOnly`）、国内版另有 `session_2`，以及负载均衡用的 `tgw_l7_route`。网关照浏览器跟随跳转打开目标页（同一套导航头、带上 Cookie、不带 `Referer`，最多 20 跳），页面内容读完丢掉；页面里的脚本、图片和页面自己发的请求不模拟。打开目标页出错不影响登录态。
3. 之后网页的请求只靠这些 Cookie 认人，不带 `Authorization`。

网关每个账号在每个域各存一份 Cookie，只放内存；请求回 401 时清掉重取一次，再 401 就按原错误返回。浏览器请求另带 Chrome 自动加的 `Accept-Language`（国内版 `zh-CN,zh;q=0.9`、国际版 `en-US,en;q=0.9`；成长中心接口除外，见上）、`Sec-Fetch-*`，以及用内置 UA 时的 `Sec-CH-UA*`；`Sec-CH-UA` 的值按 Chromium 的 GREASE 规则由主版本 152 推算，没有抓包确认。登录码请求另带 Node `fetch` 自动加的头：`Accept: */*`、`Accept-Language: *`、`Sec-Fetch-Mode: cors`、`User-Agent: node`、`Accept-Encoding: br, gzip, deflate`，取自安装包里 undici 6.25.0 的源码，同样没有抓包确认。

2026-10-20 生产实测：两个域都按上面的流程拿到了 Cookie；只带 Cookie 时国内版的成长任务、账号资料、抽奖次数都返回 200，国际版的账号资料、抽奖次数也返回 200（国际版没有成长体系，`/v2/activity/growth/tasks` 回 401，网关对国际版不调）；什么都不带时回 401。用 1.1.2 的代码在 6 个国内版号上走了一遍：任务列表、旅行状态正常，3 个号旅行领奖成功、1 个号出发成功。

小程序限定任务：微信小程序用 web-view 打开同一份网页（`/profile/growth-center?platform=miniProgram&token=<令牌>`），网页把令牌存起来，之后发 `X-Client-Platform: miniprogram` 和 `Authorization: Bearer <令牌>`，其余和网页一样（成长中心接口的 `Accept-Language` 也是 `zh-CN`）。网关照这个形状发往官网域，UA 用微信小程序 web-view 的格式（`MicroMessenger/…` 加 `miniProgram/<小程序 appid>`，appid 取自官网前端代码，iOS 和微信的版本号是挑的近期值，没有抓包），`Accept-Encoding` 用 iOS WebKit 的 `gzip, deflate, br`。生产实测任务列表正常，领不存在的任务回 `400 task not completed`。

几处请求和以前不同：

- 任务列表先查一次订阅任务状态 `GET /v2/activity/growth/subscribe-task/status`；
- 接受任务改用 `POST /activity/growth/tasks/accept`（不带 `/v2`），领奖、领养、积分补偿、新手礼包、试用都不带请求体，旅行领奖发 `{}`；
- 兑换和抽奖的 `client_token` 带上网页的前缀：`redeem-<档位>-<uuid>`、`draw-<uuid>`；
- 新手礼包先查 `POST /billing/meter/check-gift-claimed`，活动开着且没领过才领。

官网的事件上报用 `navigator.sendBeacon` 发：请求体是 JSON 字符串，`Content-Type: text/plain;charset=UTF-8`，靠 Cookie 认人。网关照这个形状发，用同一份网页登录态。事件字段照官网前端的公共参数：`os`、`osVersion` 由 UA 解析（Mac 上的 Chrome 是 `macOS`、`10.15.7`），`sessionId` 是「毫秒时间戳-9 位随机 36 进制」，`machineId` 是 32 位十六进制的浏览器指纹，`product: UserCenter`，元素点击另带 `type`、`elementId`、`elementName`。`username` 网页取账号资料，网关没有这一项，用昵称，没有确认。

## 备用入口

官方产品配置 `productFeaturesConfig.NetworkFailover` 给国内版 SaaS 配了三个可信入口：主入口 `https://www.workbuddy.cn`、备选入口 `https://copilot.tencent.com`、备用入口 `https://wb.tencentbuddy.com`。内置 CLI 和后台服务出错时换入口、换地址，网关照做（`failover.go`、`address.go`、`httpdns.go`），只对国内版；国际版官方没有这一套。`chat_base_cn` 配成备用入口时只换地址，配成别的地址时整套不生效。

对话请求：

- 换入口：主入口或备选入口返回 HTTP 408、502、503、504，出现传输错误（连不上、连接被重置、建连或响应头超时），或者 2xx 但响应体是空的，同一请求改发备用入口一次。响应体的 `retry_policy.retry` 为 `false` 时不换。
- 换地址：DNS 解析失败时先换地址、再换入口；408、502、503 和传输错误在换不了入口（已经是备用入口）时换地址，504 不换地址。地址按候选逐个钉上去重发：URL、`Host`、TLS 的 SNI 与证书校验都还是原域名，只把 TCP 连到这个 IP。
- 候选地址：这个域名的 HTTPDNS 结果，备用入口另加官方内置的备用 IP（`httpdns_consts.go`）。本机 DNS 的结果不进候选。30 秒内失败过的 IP 跳过（全都失败过时照样全试），上次成功的排第一个。
- 同一个 `X-Conversation-Request-ID` 换过的地址记住，后续重发沿用（最多记 256 个请求）；同一请求正在换入口时，新来的重发直接返回错误，不再并行切换。
- 熔断按入口记：60 秒内 502、503、504 或连接类失败满 3 次，打开 15 秒，期间直接发备用入口；打开期间再失败依次延长 30、60、120 秒；成功一次清零。

其他请求：

- 拉模型目录 `/v3/config`：5 秒总预算内依次试主入口、备用入口、备用 IP；408、502、503、传输错误，或者 200 但目录不能用（官方 `isPersistableProductConfiguration`）时换下一个，504 不换。剩余预算不到 250 毫秒就不再试，但上一步是超时或目录不能用时，下一步另给 3 秒。
- 登录和刷新令牌不换入口（`loginPreselect: false`）。发起授权和刷新令牌在 408、502、503、传输错误，或者 2xx 但内容无效时，依次换 HTTPDNS 地址重发；轮询和取账号不换。每次尝试单独计时：发起授权、刷新令牌 8 秒，轮询、取账号 4 秒。

HTTPDNS 用腾讯云的服务，照官方的做法：

- 先按 IP 向 `119.29.29.99`、`119.28.28.99` 取服务 IP 列表（`/conf?token=`），再向服务 IP 解析域名（`/d?dn=&token=`）。请求按 IP 直连，`Host` 写 IP 本身，不校验证书，只带 `Host`；全部为空时强制重取一次列表，新列表不同就再解析一次。
- 单个请求的 300 毫秒是 Node `https.request` 的 `timeout`，也就是套接字闲置超时：连接、握手、等响应期间连续 300 毫秒没有收发才算失败，不限整个请求的时长；超时不重发。
- 地址层最多等 600 毫秒（`withTimeout(lookupHttp, 600, [])`），到点按没有结果处理，查询本身继续，查完写进缓存。
- 服务 IP 列表按返回的 `ttl` 缓存（缺省 12 小时，夹在 60 秒到 12 小时），解析结果缓存 300 秒，空结果也一样；都是过了 75% 在后台刷新（每份缓存同时只刷一路），过期后同步重查。列表刷新失败保留旧的、下一次取用时再刷；解析结果刷新到空结果时照官方覆盖旧值。
- 解析结果的后台刷新照官方（`skipCache`）不读也不写服务 IP 列表缓存：现取一次列表，取不到用旧列表或内置列表。
- 缓存未命中时每次各查一路，同一域名的并发查询不合并（同官方 `lookupHttpDns`）。

2026-10-20 生产（美国机房）实测：HTTPDNS 查到 `www.workbuddy.cn`、`copilot.tencent.com` 各 1 个 IP，`wb.tencentbuddy.com` 3 个，和系统 DNS 一致；钉到这些 IP 和备用 IP 拉模型目录全部成功。这种网络下每个 HTTPDNS 请求约 0.65 秒、一次完整查询约 1.4 秒，第一次查询按没有结果返回，之后命中缓存。改成闲置超时以前，单个请求按 300 毫秒总时长算，在这台机器上从来查不到。

## 出站代理

依据：桌面端 5.9.3 内置 CLI 2.165.0 的 `ProxyResolver`、请求拦截器 `HttpProxyInterceptor` 与地址层，以及桌面端主进程的地址层（`app/main/auth-liveness.js` 里打包的那份）；npm 上的 CLI 2.179.0 这几处相同。2026-10-31 读安装包确认。

官方的做法：

- 代理地址依次取 `HTTPS_PROXY`、`https_proxy`、`HTTP_PROXY`、`http_proxy` 第一个非空的，没写协议按 `http://`，只认 `http`、`https` 两种代理。官方另有配置文件（`~/.workbuddy/settings.json` 的 `http.proxy`）排在环境变量前、系统代理排在后，网关都不读。
- `NO_PROXY`（没设再看 `no_proxy`）逗号分隔：`*` 全部直连，`example.com` 匹配它自己和子域，`.example.com` 只匹配子域，`*.example.com` 匹配它自己和子域，不认端口和网段；`localhost`、`127.0.0.1`、`::1` 总是直连。
- https 目标经 `HttpsProxyAgent` 发 `CONNECT` 建隧道，在隧道里做 TLS。环境变量给的代理连不上就报错，不退回直连（只有桌面端传进来的系统代理，即 `WORKBUDDY_PROXY_SOURCE=system`，才会退回直连）。
- 地址层照常工作（`shouldSkipAddressLayer` 目前恒返回不跳过）：走代理的对话、登录请求出错后照样换 HTTPDNS 地址和备用 IP，钉 IP 的请求一律直连（`applyPinnedAddress` 设 `proxy: false`，钉过 IP 的请求之后也强制直连）。
- 拉模型目录的「备用 IP」一步只给直连的请求用（CLI 的 `applyConfigFallbackAddress`、主进程的 `prepareConfigFallbackAddressSlot` 都要求请求是直连的），走代理时没有这一步。
- HTTPDNS 查询用不带代理的 `http.request`、`https.request`，直连。

网关照做（`proxy.go`），不同之处：

- 多认 `ALL_PROXY`、`all_proxy`（排在最后）和 `socks5`、`socks5h` 代理，两种 socks 都把域名交给代理解析（同 Go 标准库）。
- 隧道自己建（`CONNECT` 只带 `Host` 和 `Proxy-Authorization`），隧道里照样用 uTLS 复刻官方的 ClientHello（见[没对齐或没实测的](#没对齐或没实测的)里的传输层），上游看到的指纹和直连一样，`proxy_test.go` 锁死。不用 Go 的 `http.Transport.Proxy`：设了它，https 请求改由标准库自己握手，指纹会丢。
- 设了但用不了的值（协议不认、地址写错）启动时记一行 WARN、出站直连；官方是不提示直接直连。用上的代理也在启动时记一行，不带用户名密码。

按号代理（代理池，`route.go`，规则见[按号代理与代理池](proxy.md)）是网关自己加的，官方客户端只有上面这一个全局代理：

- 绑了代理的号，发给官方的请求都经号的代理出去，每个代理一套独立的连接池，隧道和握手同上；没绑的号和不带号的请求（官方静态站点、`models.dev`）走上面的环境变量代理或直连。
- 地址层钉 IP 的重发（HTTPDNS 候选、备用 IP）也经号的代理连到那个 IP，SNI、证书校验和 `Host` 仍按原域名；官方钉 IP 的连接一律直连。这里偏离官方，免得号的请求从服务器 IP 出去。同样的原因，绑了代理的号拉模型目录时也试备用 IP（经号的代理），官方走代理时没有这一步。HTTPDNS 查询本身照旧直连。
- 号的代理连不上（连不上代理、代理认证失败、代理拒绝转发）不切入口、不换 IP、不记入口熔断，网关换号重试。
- 健康检查经代理连对话主入口的 443 端口，做完 TLS 握手（指纹和真实请求一样，证书按域名校验）就断开，不发 HTTP 请求，默认每个代理 5 分钟一次。官方没有这类请求。
- 查出口 IP 发给第三方（默认 `ip-api.com`、`ipify`），用 Go 标准库的 TLS，不伪装官方指纹。官方没有这类请求。

未实测：官方走代理时隧道里的握手和直连是否逐字节一致（按 `HttpsProxyAgent` 的实现应当一致）；网关走真实代理连上游，包括按号代理。

## 设备与平台

两个安装包都是 macOS（darwin-arm64）版，网关按 macOS 版官方客户端对齐（10-20 定）：

- 上报事件的公共字段照 5.8.3 的事件处理器补（后台服务和内置 CLI 相同，`desktop.go` 的 `eventFingerprint`；5.9.3 只多了 `oneId`，见[与桌面端 5.9.3 的对照](#与桌面端-593-的对照)）：
  - `product`、`releaseDate`（product.json 的 `date` 转毫秒）、`commit`；
  - `userId`、`username` 与 `userNickname`（官方都取昵称）、企业号的 `enterpriseId`，有部门全名时的 `enterprise`；
  - `timezone`；
  - `os`、`arch`、`osVersion`、`cpuModel`、`cpuCores`、`memorySize`；
  - `extName: workbuddy-desktop`、`extVersion`；
  - `ideName`、`ideType`、`ideVersion`、`machineId`、`sessionId`。
- 设备取值：官方从本机读（`os.platform()`、`arch()`、`release()`、`cpus()`、`totalmem()`），网关挑一台常见配置：`darwin`、`arm64`、`24.6.0`（macOS 15.6）、`Apple M2`、8 核、16 GB。所有账号用同一组。
- `machineId`：官方 macOS 版取 `ioreg` 的 `IOPlatformUUID`，是大写 UUID；网关按账号派生一个，同一账号不变。
- `sessionId`：后台服务的事件是空串（桌面端没给后台服务设会话 id）；内置 CLI 的事件（对话活跃上报）是 CLI 进程启动时随机生成的 UUID，网关按账号固定一个。
- `reportDelay`：官方攒 2 秒发一批，发送时算 `now - timestamp`；网关立即发，把 `timestamp` 往前推 2 秒。
- 官方有、网关不带的：`qimei36` 与 `X-Device-Id-List`（macOS 版拿不到，见[登录](#登录)）、`clientIp`（只有国际版去 `api.ipify.org` 查，任务中心不对国际版开放）、`downloadChannel`（安装包没配）、`buddyId` / `buddyName`（后台服务只在 Buddy 应用里才补）、`expKeys`。界面上报的事件另有两个字段，见[没对齐或没实测的](#没对齐或没实测的)里任务中心那条。
- 官网网页按同一台 Mac 上的 Chrome 发，见[官网网页的请求](#官网网页的请求)。

以前事件里是 Windows（`win32`、`x64`、`10.0.26220`）、36 位十六进制的 `machineId` 和一个派生的 `sessionId`，取自 5.5.6 的抓包；Windows 版官方会带 qimei36，网关做不出来，所以改按 macOS。2026-10-20 生产实测：按新字段发桌面端上报、对话活跃上报、网页上报都返回成功，用 Mac 版 Chrome 的 UA 拉成长任务列表正常。

## 企业号模型目录

官方内置 CLI 只对企业号（账号有 `enterpriseId`）调 `GET /console/enterprises/{企业 ID}/config/models`，超时 5 秒。`data` 是模型数组，带 `chat` 标签的模型并进各个 agent 的名单。网关两个域的企业号都这样拉，和 `/v3/config` 合并：同名以 `/v3/config` 为准，企业接口独有的补进来；企业接口失败只用 `/v3/config`。个人号只拉 `/v3/config`，和官方一样。

1.1.2 以前国内版企业号调 `/console/enterprises/personal/models`、国际版企业号调 `/v2/enterprises/personal/models`，都不是官方的接口。2026-10-20 生产用个人号请求 `/console/enterprises/personal/config/models`，返回的 `data` 是空数组，结构和官方代码读的一致；企业号没有实测。

## 国内版域名

官方的接口请求都发往产品配置里的 `endpoint`。国内版是 `https://www.workbuddy.cn`；`https://copilot.tencent.com` 是备选入口，`https://wb.tencentbuddy.com` 是出错时切换的备用入口。`www.codebuddy.cn` 在桌面端里只用于网页链接。

网关从 1.1.2 起国内版全部发往 `https://www.workbuddy.cn`。以前对话、刷新令牌、登录、模型目录、成长任务和任务中心发往 `copilot.tencent.com`，积分、签到、积分补偿、开学季活动和小程序上报发往 `www.codebuddy.cn`。

2026-10-20 在生产用国内版个人号对比了两个域名，下面这些请求的状态码、业务码和返回的数据结构都相同，模型目录的模型名单也相同：

- 模型目录 `/v3/config`、企业模型名单 `/console/enterprises/personal/models`；
- 对话 `/v2/chat/completions`；
- 积分包查询、签到状态、积分补偿状态、开学季券；
- 成长任务列表、猫猫信息、旅行状态、连登、抽奖次数；
- 任务中心的专家列表和一次空上报；
- 登录：发起授权和轮询（授权页随之变成 `www.workbuddy.cn`）。

会改动账号状态的请求不能拿真实参数对比。2026-10-20 在 `www.workbuddy.cn` 上：

- 网关本来就会自动做的，用 1.1.2 的代码真做了一次：两个域各刷新一个号的令牌（见[刷新令牌](#刷新令牌)），3 个号旅行领奖、1 个号旅行出发，都成功。
- 其余的用必然失败的参数按新形状发：签到（今天已签过）、领奖（任务没完成）、旅行出发（地点不对）、兑换（档位不存在）、补签卡（日期不在本月）、领新手礼包（已领过）、抽奖（没有次数）、领积分补偿（已领过）、小程序领奖（任务不存在）都返回业务错误；接受任务（任务码不存在）返回成功；国际版试用返回“已申请过”。说明这些路径和认证都对。
- 设置外观不校验资源键，探测时写进了一个不存在的值，已改回原值。

真正领到的那一次没有试到的：抽奖（6 个号都没有次数）、积分补偿（都已领过）、小程序任务（没有已完成的小程序任务）、签到成功（当天都已签）。

对话请求在主入口出错时切到备用入口，见[备用入口](#备用入口)。

## 产品开关

国际版 `product.json` 里有这几项（5.8.4、5.9.4 都有），国内版都没开：

- `DisableCheckin: true`、`UserGrowth: false`：国际版没有签到和成长任务，网关对国际版账号也都跳过；
- `DisableActivityBanner: true`；
- `DefaultModelOptimizationOff: true`：5.8.4 内置的 2.161.0 不读它，5.9.4 内置的 2.165.0 起影响 `X-Private-Data`。

国际版 5.9.4 比 5.8.4 多了 `DisableMemoryPlatformV2`、`EnableFrenchLocale`、`EnableJapaneseLocale`、`EnableKoreanLocale`、`ExternalPackagePdfCli`，少了 `DisableMessageCenterEntry`、`EnableSecurityCenter`，都只管界面和本地功能，网关不用跟。

国内版 5.9.3 的 `product.json` 新写了 `DefaultModelOptimizationOff: false`（5.8.3 没有这一项），影响 `X-Private-Data`，见[几类请求](#几类请求)里的对话；`DisableYuanbaoChannel` 两个域都是 `true`，2.165.0 起不再参与 `X-Private-Data`。

## 错误码

上游错误体里的业务码按内置 CLI 的错误码枚举归类（`internal/upstream/bizcode.go`），先于 HTTP 状态码和文案判：

| 业务码 | 官方名字 | 官方怎么处理 | 网关怎么处置 |
|---|---|---|---|
| `14003` | `RateLimitError` | 临时限流，原请求退避重发（0.5 秒起、封顶 32 秒） | 只冷却这个号的这个模型约 30 秒，当场换号 |
| `6001` `6002` `6005` `6006` | `CraftRateTPS/TPM/RPS/RPMLimit` | 同上 | 有重置时间就冷却到那时，没有就同 14003 |
| `6000` `6003` `6004` `6007` `6008` | `CraftRateLimit`、`CraftRateTPH/TPD/RPH/RPDLimit` | 不自动重发 | 按模型冷却到上游给的重置时间，换别的模型照样用 |
| `14001` `14012` `14013` `14014` `14018` `14019` `14022` `14023` | `UsageLimitExceeded*`、`UsageLimitUserExhausted`、`UsageLimitNoTokenBudget`、`UsageLimitEnterprisePackageExhausted` | 额度用完（`isQuotaExhaustedError`），不重试 | 余额不足，冷却到次日 04:00 |
| `14015` | `UsageLimitLicenseExpired` | 归授权过期 | 账号软冷却 |
| `14016` `14017` | `UsageLimitEnterpriseNotActivated`、`UsageLimitUserNotActivated` | 归未激活 | 账号软冷却 |
| `10105` | `ConversationLimitExceeded` | 归同时进行的会话太多 | 账号冷却约 60 秒，当场换号 |
| `6020` | 桌面端容量排队的 `WAITING` | 在原来的号上排队，到号后原样重发 | 不罚账号、不换号，在这个号上照官方排队，到号原样重发；等不到回 503 + `upstream_busy`，见[容量排队](#容量排队) |
| `6021` | 桌面端容量排队的 `FULL` | 队列满，只提示，不自动重发 | 不罚账号、不换号，回 503 + `upstream_busy` |
| `6022` | 桌面端容量排队的 `USER_LIMIT` | 这个用户排队的数量到上限，只提示，不自动重发 | 不罚账号，换下一个号；都回 6022 时回 503 + `upstream_busy` |
| `11115` | `ContextTooLong` | 归输入超长 | 不罚账号、不换号，原文返回；文案照 `isInputLengthError` 的词表与正则认 |

`14022`（`UsageLimitExceededDepartment`，部门额度用完）和 `14023`（企业资源包用完）桌面端 5.9.3 内置的 CLI 2.165.0 还没有，npm 上的官方 CLI（`@tencent-ai/codebuddy-code` 2.179.0 读包）已列进 `isQuotaExhaustedError` 的码集合。网关和别的额度码一样冷却到次日 04:00，余额刷新发现恢复就提前解冻；部门额度、企业资源包实际什么时候恢复未确认，没见过实际响应。

容量排队的三个码不在内置 CLI 里，在桌面端，网关怎么排见[容量排队](#容量排队)。`6021` 上游 linguo2625469 `f14f374` 生产见过 `429 {"code":6021,"msg":"queue.full.title"}`（以前落进通用 429，逐号软冷却，2 秒内整池健康号全被冷却）；`6020`、`6022` 没见过实际响应。

`14002`（`ConversationChatTooMany`）官方只在统计里归成额度用完，既不算额度用完也不算临时限流，含义没确认，网关仍按状态码处理；`15001`（联网搜索限流）网关用不到。

`11128` 是上游拦下了这一条请求，响应体是数字码 `{"code":11128,...}`（同类项目讨论时常写成「11-128」，是为了避开上游对这串数字本身的拦截，并不是上游回的形状）。文案有两种：`Illegal API invocation from an unapproved channel` 是内容里有渠道指纹，`request illegal` / `conversation id missing` 是渠道校验没过（例如国际版缺会话头，见[生产实测](#生产实测)）。网关都按内容拦截处理，不罚账号；指纹清洗的规则见[网关与选号](gateway.md#系统提示词与内容审核)。

`11140` 按文案分两种：带 `request illegal` 的是账号被上游风控限制，网关禁用该号、原因里提示去官方申诉，并计入风控波（见[网关与选号](gateway.md#错误分类与账号处置)）；其余形态是临时限流，按状态码处理。

2026-10-23 生产实测一个国际版个人号，是**只封对话、不封其他**的账号级风控：

- 每次对话都回 `403` + `code 11140` + `request illegal`，`displayMsg` 是「账号存在异常行为 / Your account has been restricted due to unusual activity」。导入后第一次对话即被封，人工解禁后再试 3 次仍是同样的报错，每次都被重新禁用；
- 同一个号的积分查询、成长任务列表、网页登录态、国际版注册激活、国际版试用领取全部正常，说明 token 有效、身份能过，限制只落在对话上；
- 该号的凭证在导入前就带着 `HTTP 403 (account restricted)` 和 `restrictedUntil`（换算后是导入后几分钟），可见是账号在导出时已被风控，不是网关或导入造成的。同一时段其余账号对话全部 200，网关侧的请求形状没有问题。

结论：这类号的 token 是有效的，重新登录一般解不开，要向官方申诉。网关 1.5.0 起把禁用原因从「`re-login required`」改成提示申诉。同类项目 workbuddy2api-hub 的 #629 反馈，10-23 下午起国际版号成批被这样限制，国内版不受影响；网关的风控波暂停照它的做法：15 分钟内同一个域有 2 个号被限，就把该域其余的号暂停 30 分钟。

### 容量排队

上游这一路满了时，对话请求回业务码 `6020` 和排位，客户端在原来的号上排队，到号了原样重发。依据是国内版桌面端 5.9.3 安装包（2026-10-31 读包）：渲染层 `app/renderer/assets/workbuddy-provider-*.js` 的 `QueuePollingManager` 管排队（受 `featureToggles.QueueBanner` 开关控制，没关就开），查询和取消交给主进程 `app/main/server.js` 的 `queue:getStatus`、`queue:cancel` 去发。国际版 5.9.4 的这两处代码和国内版相同（2026-10-31 按函数比对）。

- 码：`6020` `WAITING` 排进队了；`6021` `FULL` 队列满，只提示；`6022` `USER_LIMIT` 这个用户排队的数量到上限，只提示。
- 6020 回应里的排位 `queue_position` 和 `retry_after`（秒）：渲染层直接从错误数据里读，主进程先取 `data` 再看 `data.data`，网关按顶层、`data`、`data.data` 依次找。
- 查询：`POST {对话入口}/v2/chat/queue/status`，请求体 `{"model":模型}`，头是 `Authorization: Bearer <token>`、`Content-Type: application/json`、`X-Conversation-Request-ID`（排进队的那一轮），有会话 id 时加 `X-Conversation-ID`。主进程用 axios 1.16.1 的默认实例发，没有官方请求框架的那些头，只多 axios 自带的 `Accept: application/json, text/plain, */*`、`Accept-Encoding: gzip, compress, deflate, br` 和 `User-Agent: axios/1.16.1`；5.11.3 / 5.11.4 起默认 axios 由 `tls-verification.js` 补 32 位 hex `X-Request-ID`，并由 `proxy-env2.js` 对第一方来源补 `X-Client-Version`、`X-Client-Platform: macos`、`X-Client-Product`（`client_name="SaaS"` 旧行为不发 `X-Client-*`）；超时 15 秒。回 `code 0` 且带 `data` 时看 `data.status`：`waiting`（带 `queue_position`）接着等，`ready` 到号，`expired` 过期；`code` 不是 0 或没有 `data` 都当过期，说明取 `msg`，没有就是 `Unknown error`。
- 取消：`POST {对话入口}/v2/chat/queue/cancel`，请求体同上，头不带 `X-Conversation-ID`。只在使用者点取消时发，过期、超时都不发。
- 节奏：第一次在 `retry_after` 秒后查（没带按 5 秒）；之后按排位隔一会儿查一次：前 5 名 2 秒、前 20 名 5 秒、前 50 名 10 秒、再往后 15 秒。`waiting` 用这次回的排位，没给按 50；查询出错（HTTP 出错、超时）按已知的排位再查。合计等满 20 分钟还没到号就停，提示超时。
- 到号后原样重发：同一份提示词、同一份 `_meta`。

主进程 `app/main/conversations.js` 另有一套 `CapacityQueueManager`，给本地会话经 ACP 转给 CLI 的那条路和云端会话用，节奏略有不同（前 10 名 2 秒、前 50 名 5 秒、再往后 10 秒）；内置 CLI 里没找到它转过去的处理。网关照的是渲染层那一套。

网关（`internal/upstream/capqueue.go`、`internal/server/capqueue.go`）照上面的请求形状和节奏做，三个对话入口都走这条路：

- `6020`：不罚号、不换号，在这个号上排队，到号后用同一个号原样重发（同一份请求体、同一组会话头），重发又回 `6020` 就接着排。排队期间不给客户端发数据，这个号的在途名额一直占着。
- 等多久：官方等 20 分钟，网关改用 `pool.in_flight_wait`（默认 30 秒，号都满时排队也用这个上限，两边各算各的），从第一次 `6020` 起算。等满还没到号，或者客户端断开了，网关先取消排队（等于替使用者点了取消），再回 503 + `upstream_busy`、带上游原文；上游说过期时同官方不取消，直接回 503。`in_flight_wait` 设成 `0` 时不排队，取消后直接回 503。
- `6021`：不罚号、不换号，直接回 503 + `upstream_busy`。
- `6022`：按号的上限，换别的号还能排，所以不罚号、换下一个号；都回 `6022` 时回 503 + `upstream_busy`。

没确认的见[没对齐或没实测的](#没对齐或没实测的)。

## 生产实测

2026-10-19 至 10-20，用国内版、国际版各几个个人免费号在生产实测：

- 上面这些请求形状，两个域的对话、积分查询、签到状态（只读）、模型目录都返回 200；
- 各类请求按官方的 `Accept-Encoding` 发，上游目前都不压缩响应，对话流、模型目录、网页登录态和成长任务照常；
- 备用入口与 HTTPDNS 见[备用入口](#备用入口)；
- 积分的三个接口与旧接口给的包相同，按官方算法算出的余额与汇总一致；
- 登录的发起授权、轮询、取账号按官方形状都正常；
- 官网网页的请求按官方流程换到 Cookie 后只带 Cookie，成长中心、账号资料、抽奖次数都返回 200，见[官网网页的请求](#官网网页的请求)；
- 刷新令牌两个域各真刷了一个号，见[刷新令牌](#刷新令牌)；
- 前缀缓存按 `X-Conversation-ID` 命中：`glm-5.2` 会话 id 稳定时第二轮 8/8 命中，每轮换新只有 3/10；`deepseek`、`kimi`、`glm-5.3` 不论会话 id 都能命中；`prompt_cache_key` 对哪个模型都没有帮助；
- 2026-10-27 在国内版又测了一次前缀缓存（前缀约 1 万 token）：`glm-5.3-flash` 换号或同号换新 id 都整段未命中，同号同 id 闲置 2、5、10、20 分钟都还命中，更久的没测；`deepseek-v4.1-flash` 同一个号换新 id 3 次都命中、换号 1 次未命中，按号缓存，跟会话 id 无关。当时据此写了「`glm-5.3-flash` 要同一个号、同一个会话 id 才命中」，但换号的那几次同时换了 id，号的那一半没有单独测过，10-28 单独测后更正，见下一条。`glm-5.3-flash` 和上一条的 `glm-5.3` 结论不同，要分开看。不带会话 id 的多轮对话在旧做法下第 1、2 轮未命中、第 3 轮起命中（第二轮网关换了会话 id），网关因此改成第一轮就定下会话 id，见[会话粘性](gateway.md#会话粘性)。1.6.0 上线后 2026-10-28 复测：不带会话 id 的三轮对话第 1 轮未命中，第 2、3 轮都命中（10368 / 10410 token），三轮在同一个号上；滚动重启网关后接着聊第 4 轮仍命中，还是同一个号；
- 2026-10-28 在国内版单独测 `glm-5.3-flash` 认不认号（前缀约 1 万 token）：同一个会话 id 在原来的号上 4/4 命中，换到别的号上也 4/4 命中；换新 id，同一个号 0/3、别的号 0/9。所以它的缓存跟着会话 id 走、不认号，网关因此改成换号也沿用这段对话原来的会话 id，见[会话粘性](gateway.md#会话粘性)。命中的 token 数都是 64 的整数倍（如 10368 = 162 × 64），看起来缓存按 64 token 一块记；
- 同一天测了「换号再换回」：对话在 A 号上聊第 1、2 轮，第 3、4 轮换到 B 号（1.6.0 在 B 号上用了新的会话 id），第 5、6 轮回到 A 号、用回原来的 id，都命中 10432 token。B 号上用新 id 的两轮也命中了 10368 token，和上一条「换新 id，别的号 0/9」对不上；回到 A 号命中的 10432 token 比 A 号上任何一轮的提示词都长，只能是 B 号第 4 轮（10438 token）写进的缓存。推测上游按会话 id 把请求分到某台机器、缓存按内容记在机器上，B 号的新 id 碰巧分到了同一台，未确认；
- 串会话：两个域、4 个模型（`deepseek-v4.1-flash`、`glm-5.2`、`glm-5.3`、`kimi-k2.8-preview`）共 29 次，用同一个会话 id 发不相干的内容，都没有带出别的对话的内容。2026-10-28 又用 `glm-5.3-flash` 补测：一段对话让模型记暗号，另一段对话用同一个会话 id 问暗号，分和记暗号的请求同时发、之后发、用同一个轮次 id、在自己的第二轮问四种；只算和记暗号的请求落在同一个号上的，两个域共 25 次，也都没有带出；
- 国际版要求第一条消息是 `system`：没有，或者放在第二条，都回 400 `code 11128`；第一条是 `developer` 也是 11128。网关会把 `developer` 改成 `system`，没有 `system` 时自动补一条；
- 个人免费号调 GPT、Gemini、Grok 回 `11158`（这类用户不能用），所以这几个模型没测到。

## 没对齐或没实测的

- 真正领到的那一次没有试到：抽奖（实测的号都没有次数）、积分补偿（都已领过）、小程序任务（没有已完成的）、签到成功（当天都已签）。这几样都按新形状用必然失败的参数试过，路径和认证是对的，见[国内版域名](#国内版域名)。
- 扫码登录只走了国内版：国际版的发起授权、轮询、取账号按新形状实测正常，扫码那一步没做。
- 设备信息是挑的一台常见 Mac 配置，不是哪台真机，见[设备与平台](#设备与平台)。
- 小程序事件（开学季、小程序任务）里的会话 id、轮次 id 按桌面端的形状生成，小程序没有源码可对照，没确认。
- 部门全名（`X-Department-Info`、上报的 `enterprise`）：照官方接上了，池里没有带部门的企业号，上游返回的值什么样、刷新时会不会变都没实测。
- WorkBuddy 传输层：这条桌面链路使用 HTTP/1.1（见 `transport.go`）；新平台按下文独立配置，不共用这项限制。TLS 握手 2026-10-28 起照官方桌面端伪装（`tlsmimic.go`，默认开，此前是 Go 标准库握手）：
  - 桌面端请求官方走 HTTP/1.1，TLS 握手由 Electron 37.10.3 里的 Node `v22.21.1`（BoringSSL）发。它的 ClientHello——密码套件顺序、扩展顺序、不带 GREASE、ALPN 只 `http/1.1`——既不同于 Go 标准库，也不同于任何浏览器预设，服务端能据此分出客户端类型，所以只能照抓到的真实握手复刻。
  - 用 [uTLS](https://github.com/refraction-networking/utls) v1.8.2（BSD，许可相容）：`tlsmimic.go` 里 embed 一份本机实测抓到的官方 ClientHello 原始字节（`clientHelloHex`），每次拨号用 uTLS 的 `Fingerprinter` 现解析出一份独立的 `ClientHelloSpec`（避免扩展对象跨连接共享），再用 `HelloCustom` + `ApplyPreset` 按它握手。SNI、随机数、`key_share` 等每连接的值由 uTLS 现生成，embed 里抓包时的具体值（含当时 SNI）不会泄漏到实际连接；证书校验与 SNI 仍按请求域名，钉 IP 时也如此。
  - 抓取办法：2026-10-28 用桌面端内置 Node（`ELECTRON_RUN_AS_NODE` 跑 Electron 的 node）向本机回环发一次握手、抓下 ClientHello。该样本来自 Linux 的同版 Electron Node 与对应网络配置；没有直接抓取 macOS 应用线上流量。目标 `JA3 = 771,4865-4866-4867-49199-49195-49200-49196-49191-52393-52392-49161-49171-49162-49172-156-157-47-53,0-23-65281-10-11-35-16-13-51-45-43-21,29-23-24,0`，`JA3 MD5 = 1a3153f314dc13e133dc71d113a81b16`，`JA4 = t13d1812h1_5d04281c6031_ef7df7f74e48`（JA4 比 JA3 还多算进签名算法扩展的内容）；uTLS 发出的密码套件、扩展、JA3 与 JA4 已核对，随机数和密钥等动态值不比较；单元测试 `tlsmimic_test.go` 把 JA3、JA4 都照同一份握手锁死，指纹一漂移就红。桌面端升级后重抓替换 `clientHelloHex` 即可。
  - 开关：配置 `upstream.tls_fingerprint`（默认开，改后重启生效），关掉回落 Go 标准库握手（旧行为）。
  - WorkBuddy 桌面链路未采用官网网页的浏览器配置：网页走 HTTP/2 和 Chrome 的 TLS 握手，HTTP/2 才有的 `priority` 头网关也没有；网关伪装的是桌面端（HTTP/1.1），不是浏览器，所以不去对齐网页那套指纹。
  - 未实测：本地抓包复刻与单元测试已过，生产上线后的流式对话还没回归（上线后看日志）。
- 企业号：模型目录、积分、请求头里的企业身份都照官方改了，池里没有企业号，都没有实测；国际版企业号同样。
- 签到、领奖、接受任务、兑换、补签卡、新手礼包领取、试用：只用必然失败的参数试过，真正成功的那一次要上线后看日志；上报没有实测。
- 官网网页的 UA 固定为一个 Mac Chrome 152 的值，`Sec-CH-UA` 由版本号推算；小程序 web-view 的 iOS、微信版本号是挑的近期值；网页登录码请求的头取自 Node 内置 fetch 的源码。这几样都没有抓包确认，见[官网网页的请求](#官网网页的请求)。
- 备用入口与 HTTPDNS 的切换只在生产上验证了「查得到、钉 IP 能用」，主入口真正出错时的切换没有遇到过，靠离线测试覆盖。
- 出站代理只做了离线测试（假的 CONNECT、SOCKS5 代理），没有经真实代理连过上游，见[出站代理](#出站代理)。
- 任务中心的桌面端上报：各个事件的业务字段 1.1.2 起照 5.8.3 逐个核对过，依据是安装包里各事件的上报代码和给每个事件补字段的处理器（内置 CLI 的 baggage 处理器补 `codebuddy.session_id` 等，Buddy 处理器只在有 Buddy 时补 `buddyId`）。事件走哪条路决定多不多两个字段：
  - 界面的 `reportEvent`（模板点击、灵感案例的点击和 `playbook_cta_click`、换主题、专家召唤、定时任务、Buddy 应用、`query_recommend_prompt_send`）经渲染层上报服务 `AdapterTelemetryService` 和桌面适配器：`mode` 统一盖成 `LOCAL`（建任务、推荐类事件另有改写），没有 `buddyId`、`buddyName` 时补两个空串（`injectBuddyTelemetryContext`）；
  - 首轮回复后经 `wb.metrics` 报的（建任务、带模板建任务、做同款发送、专家使用、设计画布）、界面自动埋点（`data-track-id`，如召唤按钮、模板按钮的点击）和内置 CLI 的对话事件，不补这两个字段。

  灵感案例从官方静态站点现拉（`static.workbuddy.cn/workbuddy/playbook`，发现页同一份），首页模板从 `download.codebuddy.cn/plugin-marketplace/scenes-v2.json` 现拉，事件里的 id、名字、类型、分类、提示词长度是真实值。官方是 Electron 渲染进程里的请求，网关用官网网页的那个 UA 拉，Electron 37 内置 Chrome 的确切版本没定。灵感案例索引有 6 MB 多，网关把挑出的案例和模板列表各缓存一小时（官方也是拉一次用一阵），生产服务器上两个站点都能访问（10-20 实测）。

  这些字段是运行时才有的值，网关取的典型值：
  - 对话链的输入长度（专家对话按实际发的提示词算，做同款按案例提示词算，模板任务没有真发对话，用典型值）、步数、token 数和 `presentAt`；
  - 点模板按钮的自动埋点的 `pageURL` 取首页的 hash `#/`（首页也能从 `#/home` 进，没确认）；模板任务只模拟点模板按钮，不模拟再点它下面的示例提示词（选了示例官方会再报一次 `template_used`，带 `prompt_index`）；
  - 设计画布两个事件的 `cost` 和画布文件 id；
  - `skill_info` 的 `fileCount` 按 5.8.3 安装包内置的 48 个技能加上这个技能算（49），`skillId`、`skillVersion` 取自抓包。

  小程序口径的事件和官网资料库的 `library_doc_intro_click` 不在桌面端安装包里，没有核对。
- 2026-10 新上的「体验某位专家」任务（`WorkBuddy_Valorant` 和四个 `expert_use_*`）：任务说明是「在专家页搜索并召唤某位专家，完成一次对话」，网关照 `expert_5` 那套去做（召唤链、带 `X-Expert-Id` 的真实对话、`expert_actual_use`），判据是推断的。2026-10-28 在一个国内版个人号上实测：四个 `expert_use_*` 都点亮了，进度 1/1，奖励自动领到；它们的跳转链接里带专家 id，网关直接用，召唤事件的 `pageURL`、`position` 同 `expert_5`。`WorkBuddy_Valorant` 只跳到专家页，1.6.0 按名字在 `agent` 类、按推荐排序的专家列表前 5 页里找，没找到「无畏契约拼豆专家」，没做成。之后照桌面端 5.9.3 安装包（2026-10-28 下载，只解包阅读）改成先搜索：
  - 专家页的搜索就是专家列表接口带上关键词：`POST /portal/operation-platform/market/expert/list`，请求体 `{page, page_size: 20, expert_type, keyword, sort_by: "reco_rank", sort_order: "desc"}`，`expert_type` 按当前页签（单专家 `agent`、团队 `team`）；
  - 多带请求头 `X-Language`（界面语言，默认 `zh-CN`）：5.9.3 的专家页拉列表每次都带，对话框里选专家的搜索只在有关键词时带。网关只在搜索时带；5.8.3 的专家页是不是也每次都带，没核对（未确认）；
  - 响应先取 `data.items`，没有再取 `data.experts`（`data.items ?? data.experts`）。1.6.0 只读 `experts`，接口要是回的 `items`，就会读成空列表，这可能也是没找到的原因；两个原因是哪个，未确认；
  - 搜索框停 500 毫秒后报 `expert_search`：`{query, count, scope: "center", source, type}`，`count` 是搜到的条数，`source` 是来源页签（默认 `builtin`，企业专家是 `enterprise`），`type` 是列表页签（单专家 `expert`、团队 `team`）。网关照默认页签报 `builtin` / `expert`；
  - 网关先在单专家页签搜，没有再不分类型搜一次（对话框里的搜索就不分类型），都搜不到就报错，不召唤别的专家。页签里的分类筛选（`categories`）和付费筛选（`need_pay_assets`）网关不带；
  - 搜索这一步算不算任务判据、改后能不能点亮，还没在真实账号上验证（未确认）。专家列表往下翻页只改 `page`，也没有抓包（未确认）。
- 对话请求里官方有、网关没发的（2026-10-28 对 5.9.3 时发现，5.8.3 里就有），同类项目的做法见[与桌面端 5.9.3 的对照](#与桌面端-593-的对照)。其中 OpenAI SDK 的 8 个 `X-Stainless-*` 与 `Accept: application/json` 2026-10-28 已补上（读了两域 mac 包核对 Electron，见[几类请求](#几类请求)里的对话）；另外两个 2026-10-28 也处理了：
  - `X-Device-Token`：2026-10-28 起对话也带了，但只在使用者填了 `device_token`（账号 / config / 文件）时——网关跑在 Linux 上，图灵盾 SDK 只支持 macOS 和 Windows，自己做不出真令牌，没填就不发（对应官方「令牌和错误码都没有时都不发」，不造 `X-Device-Token-Error`）。1.1.2 以前对话带过配置里的 `device_token`，1.1.2 按当时「官方对话不带」的错误结论去掉，现已恢复并修正。令牌能不能多个号共用、填进去的多久失效，仍没实测（未确认）；
  - `X-Moderation-Type`：2026-10-28 起专家对话按该专家是不是云端（`is_cloud===1`）或付费（`pay_detail` 的 `pay_mode` / `payMode`，回落 `need_pay===1`）决定带不带 `["protected"]`（`MarketExpert.Protected`，对齐官方 `shouldProtectSession`；官方还判「启用的技能里有云端技能」，网关的专家对话不加载技能，这条恒假）。任务中心的专家（`expert_5`、`expert_use_*`、`WorkBuddy_Valorant`）从市场列表拿到这些字段就按实发；列表里没查到、用占位专家时默认不带。这几个任务专家实际是不是云端 / 付费，没在真实账号上核对（未确认）。
- 联网搜索照国内版 5.9.3 读包实现（国际版 5.9.4 内置的是同一份 CLI）：搜一次扣不扣积分没实测，见[联网搜索](#联网搜索)。
- 容量排队（`6020`、`6022`）照国内版 5.9.3 读包实现，没见过实际响应，见[容量排队](#容量排队)：
  - 只认 HTTP 出错回应里的 `6020`；流开了以后才来的错误帧里要是带 `6020`，照普通错误处理（官方渲染层是从错误数据里读，上游会用哪种形态回，未确认）；
  - 重发沿用排进队那一轮的 `X-Conversation-Request-ID`。官方带原样 `_meta` 重发，CLI 会不会给重发的请求换新的请求 id，未确认。
- 错误码：生产日志（10-19 至 10-23）只见过 `6004`、`14003`、`11101`、`11128`、`11140`、`11158`，其余业务码照官方枚举归类，没有真遇到过；`14002` 的含义没确认，见[错误码](#错误码)。`11140` 只见过 `request illegal` 一种文案（账号级风控，见上），限流那种文案没遇到过；`11158` 是个人免费号调 GPT、Gemini、Grok 时回「该用户类型不能用这个模型」（`model [...] is not allowed for user type [personal_free_user]`），官方客户端里没有这个码；网关照上游 linguo2625469 `6ee5491` 和 `11102` 一样处理：这个号的这个模型记负缓存、换号，可选的号都没权限时回 400 `model_unavailable`（以前按其他客户端错误换号，所有号都没权限时报 503 没有可用账号）。1.13.0 起 `11158` 不再按到期避让，改为加进这个号不接的模型名单一直屏蔽：它按账号类型定死，到期再试只会再撞一次；issue #4 有使用者反馈国际版免费号调 deepseek 也回 `11158`（未自己实测）。
- 积分、签到、企业用量改发 `AuthService` 那组头（UA `axios/1.16.1`），国际版后台服务和刷新令牌的 UA 两段改成 `workbuddy-ai`：都是 2026-10-31 照安装包改的，同类项目的真机抓包也是这样，网关自己还没在生产上跑过新形状。
- `X-Request-ID`（请求框架、`AuthService` 补的那个，不是对话的消息 id）：官方是 `crypto.randomUUID()` 去掉连字符，32 位十六进制里第 13 位固定是 `4`、第 17 位是 `8`～`b`；网关是 16 个随机字节的十六进制，长度一样，这两位不固定。没改。
- 模型目录 `/v3/config`：官方后台服务拉的时候带 `repos` 参数，UA 跟启动时机有关（产品配置载入前是两段的 `WorkBuddy/<版本> CLI/<cli>`），内置 CLI 也可能自己拉，哪种是常态未确认；网关用对话的 UA、不带 `repos`。
- 小程序的埋点事件（开学季活动）照的是一次 Windows 版 PC 微信的抓包，系统写 `windows`、`machineId` 是抓包里的固定值；小程序 web-view 的 UA 却是 iPhone 微信，开学季接口和埋点请求从 1.1.2 起也用这个 UA（以前没设，发的是 Go 默认的 `Go-http-client/1.1`）。没有小程序的包、也没有 PC 微信的 UA 抓包可对照，系统和 UA 两边都没改成一致，见[官网网页的请求](#官网网页的请求)。
- 桌面端 5.11.3 / 5.11.4 对齐项（`X-Client-Version` / `X-Client-Platform` / `X-Client-Product`、容量排队 `X-Request-ID`、对话体 `cache_retention: "long"` 与 `extra_vars.harnessMetadata` / `extra_vars.initiator`）均依据真实安装包读包与离线单测验证，尚未在真实账号上在线回归，其中 `cache_retention: "long"` 在不同模型上的实际计费与缓存命中影响未实测（未确认）。

## 官方升级后怎么核对

1. 从官网下载两个域的新安装包，各自解到一个空目录，只读，不运行。
2. 在 `cli/product.json` 里读版本、`applicationName`、`commit`、`date` 与 `productFeatures`。
3. 在内置 CLI 和 `main/` 里搜上文表格列出的函数名，逐项对照取值。压缩后的变量名、文件名每版都变，直接比文件看不出东西：先去掉变量名、按目录逐个文件比，有差别的文件再按函数切开比（2026-10-31 对国际版 5.9.4 就是这样比出 `main/` 只有三处不同）。
4. 有变化就改这几处常量，再按改动补测试、写更新日志：
   - `internal/upstream/headers.go` 的 `defaultClientVersionCN`、`defaultClientVersionGlobal`、`defaultCliVersionCN`、`defaultCliVersionGlobal`（两个域的桌面端不一定同时升级，CLI 的版本各取各的安装包）；客户端身份头组在 `injectClientIdentity`，后台对应 `app/main/client-identity.js`、`app/main/proxy-env2.js` 与 `sidecar-manager.js`（`CODEBUDDY_CUSTOM_HEADERS`）；
   - 对话请求头在 `internal/upstream/headers.go` 的 `ChatHeaders`，请求体补齐在 `internal/upstream/official_body.go`（步数跟踪在 `internal/session/harness.go`）：内置 CLI 里搜 `getCustomHeaders`、`resolveAgentRuntime`、`axiosToFetchAdapter`、`resolveCacheRetention`、`buildHarnessMetadata`、`MessageInitiatorRequestProcessor`，`X-Private-Data` 搜 `resolveModelOptimizationEnabled`、`isEnterpriseEditionAccount`；后台服务每次提问前加的头搜 `BeforePromptExpertActivation`、`getTuringDeviceToken`、`shouldProtectSession`；OpenAI SDK 的 `X-Stainless-*` 在 `lazy-lite-wb/` 里搜 `X-Stainless-Package-Version`（版本号）、`buildHeaders`（取值）、`normalizePlatform` / `normalizeArch`（OS / Arch 映射），`X-Stainless-Runtime-Version` 要按 Electron 内置的 Node 定——`unzip -p … 'Electron Framework' | strings | grep '^v2'` 读 `Electron Framework.framework`，`Info.plist` 里是 Electron 版本（改 `nodeRuntimeVersionCN` / `nodeRuntimeVersionGlobal`）；
   - 后台服务的 UA（`defaultDaemonUA`）：后台服务里搜 `WorkbuddyUserAgentHttpInterceptor`、`initClientInfo`，后台服务进程的环境变量搜 `buildDaemonProcessEnv`（有没有 `CLIENT_INFO_PLATFORM`）；
   - 积分、签到的请求头（`authServiceHeaders`）：后台服务里搜 `buildHeadersWithTuringToken`，同一个类里的 `get http`、`buildHeaders`；积分查询（`internal/upstream/resources.go`）搜 `getPersonalUsage`、`fetchUserResources`、`isFreeOnlySummary`、`excludeSuppressedPlanBase`，axios 的版本看 `main/axios.js`；
   - `internal/upstream/desktop.go` 的 `desktopCommitCN`、`desktopReleaseDateCN`、`desktopCommitGlobal`、`desktopReleaseDateGlobal`（按域各取各的安装包）；
   - 刷新令牌后的账号合并与身份比对在 `internal/upstream/refresh_identity.go`（取账号在 `client.go` 的 `refreshAccountCheck`）：内置 CLI 里搜 `mergeCurrentAccountDetails`、`getAuthenticationAuthorityKey`、`normalizeAndAssertRefreshedAuthority`；
   - `cli/product.json` 的 `productFeaturesConfig.NetworkFailover` 变了，改 `internal/upstream/failover.go` 的入口与熔断参数；
   - HTTPDNS 的 token、启动 IP、备用 IP 在 `internal/upstream/httpdns_consts.go`（官方代码里搜 `719980335`、`119.29.29.99`），超时与缓存时长在 `internal/upstream/httpdns.go`；
   - 上报事件的公共字段在 `internal/upstream/desktop.go` 的 `eventFingerprint`：后台服务里搜 `OSEventProcessor`、`ProductInfoEventProcessor`、`UserinfoEventProcessor`，网页上报的公共参数在官网前端里搜 `UserCenter`；
   - 任务中心各事件的业务字段在 `internal/upstream/desktop.go` 和 `internal/panel/autotask.go`：界面的事件码在 `renderer/assets/adapter-telemetry-service-*.js`（形如 ``e.TaskCreated=`agent_task_created` ``），按枚举名搜调用处；对话链在内置 CLI 里搜 `reportChatRequestSendTelemetry`、`reportMessageSendTelemetry`；换主题的事件搜 `buildSkinApplyPayload`，主题列表接口是 `operation-platform/appearance/resources`，版本档搜 `getEditionDisplayType`；界面事件补的字段搜 `AdapterTelemetryService`、`injectBuddyTelemetryContext`；灵感案例站点搜 `getDiscoverBaseUrl`（`internal/upstream/playbook.go`），首页模板搜 `scenes-v2.json`、`reportTemplateUsed`（`internal/upstream/scenes.go`）；
   - 各类请求的 `Accept-Encoding` 在 `internal/upstream/encoding.go`：内置 CLI 里搜 `advertiseZstdAcceptEncoding`，Node fetch 的默认头在 `node_modules/undici/lib/web/fetch/index.js`；
   - 登录的 `platform`、路径、授权被拒的业务码在 `internal/upstream/login.go`（后台服务里搜 `LICENSE_ERROR_CODES`、`toSignLicenseError`），积分包代码在 `internal/upstream/resources.go`；
   - 业务码表在 `internal/upstream/bizcode.go`：内置 CLI 里搜 `CraftRateTPSLimit`（错误码枚举）、`isTransientRateLimitBusinessCode`、`isQuotaExhaustedError`、`classifyErrorDetail`、`computeRequestRetryDelayMs`；上下文超限的词表在 `internal/upstream/client.go` 的 `promptTooLongMarkers`，搜 `isInputLengthError`。
5. 官网网页的接口变化要另看 `www.workbuddy.cn` 成长中心页的前端代码，对应 `internal/upstream/web.go` 和用它的几个文件。

不想等新版本发布时，可以用 `upstream.client_version`、`upstream.cli_version` 先改版本号，见[配置](configuration.md)。

## 新增平台的协议依据与验收边界

2026-11-16 的 TraeWork、Trae Code 与 Qoder 为实验实现。下表分别注明固定提交的第三方协议分析、官方静态源码、本地假上游和真实账号回归；下方另列 2026-11-16 的官方运行时／网页网络采样。**静态源码、回环采样和真实账号调用各有边界，不互相替代**。真实授权、目录、推理、刷新和扣费须逐项记录，当前进度见[多平台验收记录](multi-platform.md#本地验收记录)。

| 平台／操作 | 本仓库请求与处理 | 固定依据 |
|---|---|---|
| Trae 国内授权 | PKCE `/authorization`，回调为 `http://127.0.0.1:1024/authorize`，读取 `authCodeInfo.AuthCode`，向 `api.trae.com.cn` 发 `POST /trae/api/v3/oauth/ExchangeToken`，保存同一机器、设备与 P-256 密钥；`GetUserInfo` 带 `ReqSource=IDE`、`IDEVersion` | [cli2api credential/client/device](https://github.com/caigee-cmd/cli2api/tree/5224c100c6412d08ce56895e68ddadd6d7979e5d/internal/providers/trae)；下方补充官方网页核对 |
| Trae 刷新 | 有设备私钥时走 v3 `DeviceInfo`／`DeviceProof`，签名覆盖方法、路径、client ID、refresh token、Unix 秒、nonce；只有不带设备密钥的导入凭证才使用旧 cloudide 刷新形状，不在拒绝后切换接口 | 同上 `exchangeDeviceBound`、`deviceProofPayload`；旧凭证是否仍被接受未确认 |
| Trae Code 目录／对话 | `POST /api/ide/v1/get_detail_param` 的 `chat_v3` 目录；`POST /api/agent/v3/llm_utils_chat`；工具 schema 为 JSON 字符串，历史调用使用 `function_call`；指定工具的 `tool_choice` 从 OpenAI 对象转换为工具名字符串，`none` 时移除工具定义与选择字段；解析 output、token_usage、done，并忽略合法字符串形式的 progress_notice | 同上 catalog/payload/sse。当前版本改按 2026-11-24 核对的官方稳定包产品 `3.4.24`、构建 `2.3.106768`、AI 扩展 API version code `20260212`；2026-11-17 真实请求确认 progress_notice 可为 JSON 字符串，旧解析器会报错；指定工具对象原样转发时三协议均被上游以 HTTP 400／4001 拒绝，工具名字符串转换依据固定参考源码。回归覆盖通知后继续输出、工具、用量与完成及禁用工具，仍拒绝坏 JSON／错误正文及缺少完成事件 |
| TraeWork 目录／对话 | `work.trae.cn/api/remote/v1/models` 只取 `solo_work_remote`；创建 `mode=work` 会话后 GET events；合并累计文本快照，不跨产品回落 | [Trae2api-cn remote client](https://github.com/autumnsentiment/Trae2api-cn/blob/5c87d6c309499974f5dc786ba2fe22d3c64db1cf/src/trae_remote_client.py)、[SSE 分析](https://github.com/autumnsentiment/Trae2api-cn/blob/5c87d6c309499974f5dc786ba2fe22d3c64db1cf/src/sse.py) |
| TraeWork 会话创建（官方网页核对） | `initial_message.query` 是 JSON 序列化的 `[{"type":"text","data":{"content":"…"}}]`，不能发送普通文本；`custom_model` 将目录的 `name`／`config_name` 映射为 `model_name`／`config_name`，并带 `config_source`、`is_preset` 和 `use_remote_service` 等请求字段，不直接照搬目录展示对象。`__hit_shared_artifact_and_workroom`／`__hit_my_space` 在请求顶层；`common_params` 使用 `biz_session_id`、`solo_chat_mode` | 2026-11-16 读取[官方网页主脚本](https://lf-cdn.marscode.cn/obj/marscode-bucket-cn/goofy_deploy_edenx/ide/trae-web-mono/static/js/main.3b45430b.js)与[会话模块脚本](https://lf-cdn.marscode.cn/obj/marscode-bucket-cn/goofy_deploy_edenx/ide/trae-web-mono/static/js/1402.b3bc0495.js)，核对 `buildSendMessageRequest`、`createSessionAndSendMessage`、模型描述构造和公共参数。会话模块 SHA-256：`059fdb8c4681715276b9d60c1ca6dfb09110add8c74130fd5ad2ad391484bd63`。本地回归覆盖这些形状；后续真实测活与指定模型的三协议测试通过，此前 HTTP 500／`995000` 未再出现，范围见多平台验收记录 |
| Qoder 设备授权／刷新 | 国内／国际 device selectAccounts、deviceToken poll、userinfo，404 作为等待状态；国内直接使用设备令牌及 deviceToken/refresh，国际使用 me/jobToken 及 jobToken/refresh；单独保存 token_kind | [Magpie Qoder 插件](https://github.com/magpie-community/plugins/tree/175dfd25552d769905bbfd57ecd57036eaa37910)；国内设备流程另核对下方官方 CLI。国内网页授权身份已通过真实目录、三协议、CLI 工具与设备刷新；国际版仍未真实验收 |
| Qoder PAT／身份 | `POST /algo/api/v3/user/jobToken?Encode=1`；`user/status` 获取 `userType`，不硬编码个人订阅档位 | [cli2api qauth](https://github.com/caigee-cmd/cli2api/tree/5224c100c6412d08ce56895e68ddadd6d7979e5d/internal/providers/qoder/qauth) |
| Qoder 目录／对话 | COSY 编码、签名；`/algo/api/v2/model/list?Encode=1`；`service/pro/sse/agent_chat_generation`，`session_type=qodercli`；body 对象／JSON 字符串包络解析；网关把已校验的纯文本段按顺序合并，再填入消息、`system` 与 `chat_context` | [cli2api Qoder](https://github.com/caigee-cmd/cli2api/tree/5224c100c6412d08ce56895e68ddadd6d7979e5d/internal/providers/qoder)；COSY 代码的上游 MIT 许可保存在 `internal/providers/cosy/NOTICE`；文本段归一化是本仓库的入口适配，本地假上游已验证，非新增的官方抓包结论 |
| Qoder SSE 包络与完成标记 | 对话包络包含 `statusCodeValue` 与字符串 `body`；`body` 内的 `[DONE]` 是控制标记，不应按 JSON 正文解析。网关在业务状态成功且没有错误字段／事件时结束读取，仍要求有效的 `finish_reason`；错误状态、坏正文或提前断流继续报错 | 下方固定版本官方 CLI 的 `streamFrames` 源码显式检查 `statusCodeValue` 与 `body.trim() === "[DONE]"`。用户反馈「已有回复和用量，随后 body 报 invalid character 'D'」；本仓库用假上游包络重现并回归，后续真实三协议和 CLI 工具验收未再出现该错误。真实调用结论不替代原始上游流量抓包 |
| Qoder 资源限制包络 | 官方响应可为 HTTP 200 的 SSE，外层 `statusCodeValue=403`，`body` 和 `message` 再包 JSON，内层为 `10605 / RESOURCE_LIMIT`，含排队和重试时间。网关只在明确业务码与 subCode 同时匹配时返回可重试的 503 和资源提示；其他 403 仍按原失败处理，不把原始 message 透传给客户端 | 2026-11-16 从 dev 服务容器取得指定模型的真实响应；旧版本号、RFC 日期及 WorkBuddy TLS 对照亦返回同一包络。假上游回归覆盖真实形状、无正文／完成事件和普通权限拒绝不误分类。此错误不等于指纹拒绝，也不能仅据它判断 credits 用完 |
| Trae 国内账号额度 | `POST https://api.trae.cn/trae/api/v2/pay/web_user_ent_usage`，`Authorization: Cloud-IDE-JWT …`，JSON `{"require_usage":true}`；按 `is_credits_billing`／`is_dollar_usage_billing` 区分单位，保留汇总和包用量，不据此分摊单次价格 | 上述官方主脚本的 `fetchEntitlementUsage` 使用 `getAiPayHost()`；上述会话模块中的 `env.cn` 将 `WEBSITE_API_URL` 设为 `https://api.trae.cn`，国内构建使用该 host。2026-11-16 从服务容器真实读到 credits 计费和扣减方向；逐包与汇总增量未对齐，专用产品额度池与逐请求费率未确认。已接入账号池与积分构成页，按账号保存成功快照 |
| Qoder 国内账号额度 | `GET https://openapi.qoder.com.cn/sash/api/v2/me/usage`，Bearer 设备令牌，`Cosy-ClientType: 10`、`User-Agent: Qoder`；检查 `displayMode`、`qoderUsage.userQuota` 及原始 `unit`，不领取签到或活动奖励 | 上述固定提交的 Magpie `fetchUsage` 提供查询形状；2026-11-16 从服务容器真实返回 `unit=credits`，已用增加与剩余减少的增量一致。此项为第三方源码加本轮实测依据，未独立核对官方网页实现，其他订阅未验收；已接入账号池与积分构成页；国际版的后续接入与验收见下方专项 |

2026-11-16 根据授权失败反馈补充核对了两份官方静态代码：

- [Trae 授权页脚本](https://lf-cdn.trae.com.cn/obj/trae-com-cn/trae_website_prod_cn/static/js/async/authorization/page.f3f2728ac7.js)在发起登录前用正则检查回调必须是 `http://127.0.0.1:<端口>/authorize`；PKCE 成功后把 `TraeGetPCAuthCode.Result` 序列化进 `authCodeInfo`，并附上 `loginTraceID`。原来的 `localhost` 地址会在官方页面被直接拒绝，顶层 `code` 也不能代替官方的 JSON 回调格式。
- 官方 npm 包 [`@qodercn-ai/qoderclicn@1.1.89`](https://www.npmjs.com/package/@qodercn-ai/qoderclicn/v/1.1.89) 的 `bundle/qoderclicn.js`：`deviceTokenPoll` 将 404 作为等待状态，`startBrowserDeviceFlow` 直接使用轮询返回的设备令牌，刷新策略为 `device-token`，没有 `/api/v1/me/jobToken` 兑换步骤。`refreshDeviceToken` 向 `/api/v1/deviceToken/refresh` 发送 `refresh_token` 和原有 `machine_id`（可带 `machine_token`），读取 `device_token` 与 `expires_at`。tarball SHA-256 为 `946e0d21099819e9103fdf0b24af2b26e808dad24d1845c5a746c525d01f73f8`。

官方网页／发布包核对、本地假上游和真实账号调用是三类证据。本地假上游覆盖请求路径和形状、PKCE state、设备刷新签名验证、令牌单飞与持久化、并行工具与回传、三协议流式／非流式、SSE 完整性和权限隔离。2026-11-16 的真实验收另覆盖国内 Work 与 Qoder 的指定模型、Qoder 的两个 CLI 工具闭环、设备刷新、重启及官方额度变化，详见[真实验收记录](multi-platform.md#真实账号验收记录)。这些结果不证明其他目标、全部模型、长期风控兼容性、专用额度池或逐请求价格已验证。


### Qoder 10605 排队协议与调查结果

2026-11-17（台湾时间）核对[官方 CLI SDK 错误文档](https://docs.qoder.com/cli/sdk/errors)：`10605` 的定义是 `Model request is queued`，官方 CLI 通常会等待并重试。登录失效、使用上限和 credits 耗尽另有错误码；不能把 SSE 包络中的外层 `403` 直接解释成登录或风控拒绝。

固定版本 `@qodercn-ai/qoderclicn@1.1.89` 的官方 bundle 有以下恢复流程；这是静态源码依据，后续网关实现按此核对：

- 尚未输出正文且有原 `sessionId`、`requestSetId` 时，查询 `GET /algo/api/v2/service/ask/queue/status`，query 带 `requestSetId`、`modelKey` 与可选的 `queueType`；头带原请求的 `X-Request-ID`、`X-Session-ID`，使用 COSY 签名和当前登录身份。
- 只有 `isQueued === false` 且 `serviceAvailable !== false` 才视为就绪。未就绪时按 `retryAfterSeconds` 等待；默认与单次等待上限均为 30 秒。就绪响应明确返回 `retryAfterSeconds=0` 时立即重发，不能回落为再等 30 秒。
- 重发保留原会话和请求集，更新 `requestId`；再次排队仍可进入恢复流程。官方默认总等待上限为一小时，最多恢复十次；这些数值是源码默认值，不是本仓库当前的超时承诺。
- 完成或取消时调用 `POST /algo/api/v2/service/ask/finish?Encode=1`，将 `model_key`、`request_set_id`、`user_id`、`time_consumed` 放入字符串 `payload`，外层带 `encodeVersion=1`，再进行 COSY 编码和签名。`X-Request-ID` 使用请求集 ID，释放请求超时为 3 秒。

本轮在实际 dev 服务容器中做了有限的真实对照：

1. 同一国内账号的目录和额度查询成功，credits 剩余为正。`auto` 推理返回 `10605 / RESOURCE_LIMIT`，队列字段为 `isQueued=true`、`serviceAvailable=false`、`retryAfterSeconds=30`；没有生成正文。
2. 保留原请求集轮询，约 30 秒后返回 `isQueued=false`、`serviceAvailable=true`、`retryAfterSeconds=0`。`queueType` 从 `p2` 变为 `p4`；这些内部标签的业务含义未确认。`queueCount=0` 与资源暂不可用可以同时出现，不能据此推断全站故障。
3. 就绪后立即使用同一请求集重发，仍返回 `10605`；再等待、轮询和重发一次，结果相同。排队状态可恢复已验证，生成恢复未通过。首次诊断脚本曾把就绪后的 0 秒误处理成 30 秒，并等待 SSE 连接 EOF；该次结果不作为生成恢复的依据，以上结论来自修正后的补测。
4. 有记录的测试请求集发送了 finish，返回 HTTP 200；原始流与私有请求保存在仓库外。诊断只读当前授权，前后全部账号的完整凭证摘要一致，没有刷新或回退 token。

首轮可确定的是：上游明确报告模型资源排队，当时网关缺少官方的轮询、原请求集恢复和释放流程，遇到 `10605` 就返回可重试错误并触发账号冷却。上述首轮只调查和记录，没有修改网关处理逻辑。

后续对照与实现（2026-11-17，台湾时间）：

- 在实际 dev 容器运行官方国内原生 CLI 1.1.89，隔离配置和空工作目录，使用同一 MachineID 和当前设备令牌，禁止工具，不提供刷新令牌。客户端确实进入 `model_queue_status`，持续报告 `service_available=false`；达到本轮设定的 70 秒排队预算后以执行失败结束，没有有效模型正文。这独立确认了官方客户端也在等待资源，不能再仅凭网关失败判断为模拟传输被拒；该有限窗口仍不能证明平台长期故障。
- 同条件运行官方国际原生 CLI 的 `--list-models`，客户端提示 `Warning: no models available for your account.`。网关空目录与当前官方客户端结果一致，账号是否应有模型、订阅／权限与资源供应的具体原因仍未确认。官方实际查询 `api2.qoder.sh`，当时网关固定 `api1.qoder.sh`；此缺口已在下方国际端点专项中补齐，不能用两侧空目录证明固定端点设计正确。
- `qoder_queue.go` 已实现同一账号和请求集轮询、只更新请求 ID 的重发、有界等待、完成／取消释放。累计队列等待最多一小时、最多恢复十次，请求自身更短的期限和取消优先；未返回正文前才恢复，发出任何业务帧后不重试。排队期间仍占用当前账号的在途槽位。查询单次最多 30 秒，连续三次查询错误或 HTTP 404 终止；就绪后的明确 0 秒立即重发。
- 释放请求保留原线路上下文，以独立的 3 秒期限执行；释放失败记录操作、目标和模型，不把已完成的生成改判失败。普通权限拒绝仍按错误处理，不触发排队恢复。
- 本地假上游覆盖恢复后正文与结束、原请求集／会话／业务正文和设备身份保持、0 秒立即重发、反复排队上限、取消／期限到达释放、线路上下文、不重放已输出内容、非法排队响应和直接 HTTP `10605`。官方诊断前后全部账号完整凭证摘要一致，诊断二进制及临时目录已清理。
- 队列实现已保数据重建至 dev，并从实际服务容器验收：真实请求等待时持有当前账号的在途槽位；诊断客户端在约 45 秒超时取消后，槽位归零。该次没有正文或正常结束帧，因此只判定等待与取消释放通过，生成恢复仍未通过。升级及验收前后，全部账号身份、设备字段、当前 access／refresh token、账号设置、配置和原有 auth 文件摘要一致，没有从旧备份恢复令牌。
- 本轮相关 Qoder 回归、providers race、离线 Go 全量测试、`go build ./...`、`go vet ./...`、golangci-lint、gofmt 与 diff 检查通过；没有前端源码变更，复用此前已验证的产物。

### Qoder 国际端点与额度

2026-11-17（台湾时间）继续核对官方 [`@qoder-ai/qodercli@1.1.89`](https://www.npmjs.com/package/@qoder-ai/qodercli/v/1.1.89) 的 endpoint resolver、`syncEndpointAsync`、`getRegionEndpointsV3/V5`、`getUsagePresentation` 与 `fetchUsagePresentation`，并与此前原生 CLI 回环请求交叉检查：

- 国际生产环境的默认推理地址为 `https://api2.qoder.sh`，显式区域 `us/sg/jp` 分别对应 `api1/api2/api3`。网关已纠正默认地址；目前没有新增显式区域配置。
- 自动模式从中心服务 `https://center.qoder.sh/algo/api/v3/service/region/endpoints` 读取端点。V3 使用 `Date`、`Signature`、`Appcode` 的匿名签名和原设备字段，不发送账号 Bearer；有设备令牌与 UID 时可用 V5 Bearer 和 `x-gw-user-id`，只有 HTTP 401／403 才回落 V3。既有国际 job token 不能冒充原设备令牌，当前按 V3 发现。
- 从 dev 容器对公开 V3 接口实测返回 HTTP 200 的 COSY 编码文本，解码后包含 `centerNodes/inferNodes/openapiNodes/fastMode` 等字段。网关同时支持编码文本、JSON 及现有 JSON 包络；公开记录不保存服务返回的备用 IP。
- 发现结果保留仍在候选中的当前端点，否则取第一个可用的公共节点；只接受官方 `*.qoder.sh` 的 HTTPS 地址，跳过官方默认关闭的企业专用节点。推理、OpenAPI 和系统 `fast` 模型分别选对应字段；国内继续使用国内网关，不接受国际节点。
- 官方 CLI 1.1.89 的 `reportFailure` 从当前端点候选中移除失败节点，端点中间件仅对可回放请求继续选路，幂等读取的 HTTP 502／503／504 可切换。网关已将这部分用于国际模型目录：保留发现返回的推理候选，连接错误或上述状态码最多尝试三个不同节点，目录读取总预算十秒。成功读取后遇到业务错误、全部停用或格式错误直接返回。原账号身份与线路随请求上下文保留，失败节点按账号和线路隔离，已开始的生成和队列操作仍持有原选路。后台探测及其他操作的故障切换继续待验。
- 发现操作复用账号线路，新鲜期沿用官方 CLI 的 30 秒；账号、MachineID／MachineToken、令牌类型、当前访问令牌与代理线路的摘要隔离缓存，并发发现合并。网关发现期限为两秒。此前沿用一秒，但在 dev 容器使用标准 TLS 客户端三次新建直连，从 DNS 到响应头约需 1.1–1.3 秒，主要耗时在 TLS 握手和等待上游响应。端点过新鲜期后仍可回退，过期满五分钟且未再刷新时清理。发现失败会记录操作和原因，并在保留期限内继续使用此前选定的节点，无旧记录时使用 `api2`。调用方取消会恢复旧缓存的原过期时间，后续请求可立即重新发现。一次生成开始后固定端点，排队、重发与 finish 不因缓存过期重新选路。缓存不落盘，不修改凭证或账号地区。
- 国际额度支持 `GET https://openapi.qoder.sh/sash/api/v2/me/usage`，Bearer 使用当前账号令牌；官方 CLI 同时存在个人账号的 `/api/v2/quota/usage` 路径。本轮从 dev 容器只读查询两条路径，均成功返回 credits 与已知的总额、已用、剩余；网关复用已确认的 `displayMode=qoder` 解析与快照展示。未知／企业响应仍明确报未支持，不补零、不推导单价。按官方 `fetchUsagePresentation → qEi` 调用，国际额度使用 `Cosy-ClientType: 5`、CLI 版本／OS、`Bun/1.4.2` UA 和 `traceparent`；不套用 OAuth 的 `qoder/1.1.89` UA。这两条额度路径的完整原生请求字节尚未独立采样。
- 实际账号返回的到期／重置日期可为公元 9999 年的毫秒时间。原来的 `1e14` 上限会错误拒绝这类有效日期，已改为校验可持久化的 RFC3339 年份范围，并统一保存 UTC，避免时区转换后跨至公元 10000 年而无法序列化；仍拒绝负数、小数和越界日期。保留官方日期，不把它解释成永久或自行计算周期。

本地假上游覆盖编码响应、默认与下发端点、匿名签名、设备 V5 认证回落、国际额度、非法地址拒绝、同号并发合并、账号／令牌／代理缓存隔离、发现取消，以及缓存过期时 fast 排队请求保持端点。新增回归先在旧实现复现固定 `api1` 与国际额度拒绝，再验证修正。后台健康探测、网络失败后的完整域名切换、HTTPDNS 和所有专用业务节点仍未对齐，不能据此称为完整官方选路实现。

同日保数据更新 dev 后，国际额度经管理员接口查询成功，余额、已用、总额和单位与实际服务容器查询的官方值一致；重启后快照原样恢复，全部账号凭证、设备字段、设置、配置和原有 auth 文件摘要保持一致。服务内端点发现两次触及一秒超时预算，实际按默认 `api2` 回落，尚不能将下发节点的应用记为真实通过；国际目录仍为空。本轮浏览器连接不可用，页面未复验；没有发起模型推理。最新 providers race、离线 Go 全量测试、build、vet、golangci-lint、gofmt 和 diff 检查通过，前端复用此前已验证的产物。

2026-11-18 09:55（台湾时间）保数据更新 dev。新增回环回归确认，跨账号查询与取消刷新均不会丢掉上一节点，发现失败时继续使用它，收到排除旧节点的新列表才切换。久未刷新的旧记录会清理。专项连续十轮、providers 整包 race 通过。全量 Go 测试复现已记录的 Work 多流分片差异。从实际 dev 强制刷新国际目录和额度时，发现再次达到一秒期限，额度刷新成功，目录仍为空。账号凭据、设置、配置、绑定及用量与重建备份一致。本轮没有新增推理实测，真实下发节点的应用仍待验收。

同日 10:01（台湾时间）以两秒期限保数据更新 dev。回环延迟 1.1 秒的发现响应，旧期限走默认端点，新期限使用返回的节点；相关专项连续四轮 race 通过。实际 dev 强制刷新目录和额度，本次容器日志无端点发现失败，额度返回有效快照，目录仍为空。本轮未独立记录目录请求选定的主机，真实节点切换继续待验收。配置、账号凭据、设置、绑定和已存用量与重建备份一致。Go 全量测试仍报 Trae Code 多流上传取消场景的五秒超时，本次没有修改该传输路径。

同日 10:21（台湾时间）补验目录。使用 dev 内现有账号和网关相同的目录请求，探针两次观察到中心发现返回 HTTP 200，随后目录实际请求 `api1.qoder.sh`。国际响应中 `assistant` 的 17 条模型全部停用，国内同场景 14 条均启用。官方国内／国际 CLI 1.1.89 的默认场景均为 `assistant`。网关目录现从该场景读取，保留数组包络兼容，区分空列表、全部停用、响应格式错误及业务错误。上游明确返回空列表或全部停用时清除旧目录，临时 HTTP 错误不清除。保数据更新 dev 后，由管理员接口验证国际版报告全部停用，国内返回 14 个模型。配置、凭据、账号设置、绑定及历史用量与重建备份一致。专项四轮及 providers 整包 race 通过。全量 Go 测试复现已记录的 TraeWork 多流分片差异。此次验证包含发现节点的实际使用，后台探测、故障切换、真实推理和扣费仍待验收。

同日 11:16（台湾时间）保数据更新 dev，接入国际目录读取的候选切换。回环先复现 502／503／504 和连接断开均不访问备用节点，修正后覆盖三个不同节点上限、去重、后续查询沿用剩余节点、账号及线路隔离、取消不污染缓存，以及目录切换期间活动生成／轮询／释放维持原节点。相关专项三轮 race、providers 整包 race、隔离网络的 Go 全量测试、vet 和 lint 通过。真实只读探针给首次目录读取注入一次 503，两次中心发现均仅返回 `api1.qoder.sh` 一个推理节点，因此真实备用节点切换验收未通过，故障注入结果保留。更新后管理员接口确认国内目录可用、国际目录全部停用、国际额度可刷新。配置、账号凭据、绑定和已存用量与重建备份一致。

### 分平台传输与身份基准

2026-11-16–17（台湾时间）按请求链路采样。平台与线路分别建立连接池，Trae 的授权请求显式选 Electron 配置；直连、绑定代理、钉 IP 均继续通过现有路由检查。关闭 `upstream.tls_fingerprint` 时回落 Go TLS；HTTP 协议仍由各平台 ALPN 协商。WorkBuddy 与 Qoder 默认 HTTP/1.1，Trae 链路提供 `h2,http/1.1`，服务端只接受 HTTP/1.1 时在发送请求前回退。

| 请求链路 | 官方来源与采样方式 | UA、版本与设备字段 | 传输基准 |
|---|---|---|---|
| WorkBuddy 国内／国际 | 上述官方安装包元数据；同版 Electron 37.10.3 的 Node 回环 `fetch` | 国内 `5.11.3`／CLI `2.176.0`，国际 `5.11.4`／CLI `2.176.0`；继续使用原有持久化设备身份 | Node 22.21.1／BoringSSL，ALPN `http/1.1`；JA3 MD5 `1a3153f314dc13e133dc71d113a81b16`，JA4 `t13d1812h1_5d04281c6031_ef7df7f74e48` |
| Qoder 国内／国际 | 2026-11-24 核对 [`@qodercn-ai/qoderclicn@1.1.96`](https://www.npmjs.com/package/@qodercn-ai/qoderclicn/v/1.1.96)（tarball SHA-256 `40cfa531...`）、[`@qoder-ai/qodercli@1.1.96`](https://www.npmjs.com/package/@qoder-ai/qodercli/v/1.1.96)（`23b46bc0...`）及国内／国际原生 `1.1.96` 包（`Bun/1.4.4`），确认 `qoder_auth_wasm` COSY 签名算法、请求头、业务端点与双架构 UMID ELF 摘要均与 1.1.89 一致；传输采样沿用 1.1.89 [国内固定版本清单](https://static.qoder.com.cn/qoder-cli-cn/channels/1.1.89/manifest.json)与[国际固定版本清单](https://qoder-ide.oss-accelerate.aliyuncs.com/qodercli/channels/1.1.89/manifest.json) Linux x64 原生包隔离回环样本 | 运行时 Bun `1.4.4`；COSY 网关 `Bun/1.4.4`，OpenAPI `qoder/1.1.96`；`Cosy-Version: 1.1.96`、clienttype `5`、默认 machineos `x86_64_linux`；保留账号原 MachineID／MachineToken，支持导入原 MachineCode／MachineType／MachineOS／MachineHostname。目录压缩 `identity`，推理与 OpenAPI `gzip, deflate, br, zstd` | 两版原生推理样本一致；ALPN `http/1.1`；JA3 MD5 `1523504b38f0fae0d881d4b6554aac1b`，JA4 `t13d1713h1_5b57614c22b0_6a3d802a7139`（1.1.89 原生样本，1.1.96 `Bun/1.4.4` TLS 指纹未另行重采）；HTTP/1.1 应用头大小写、字典序及 Bun 尾部头序已校准。原 npm/Node 24 样本保留作历史参考，不再作为运行中的 Qoder 基准 |
| Trae Code 国内 | 2026-11-24 核对[官方稳定清单](https://api.trae.cn/icube/api/v1/native/version/trae/cn/latest)与[Linux x64 3.4.24 包](https://lf-cdn.trae.com.cn/obj/trae-com-cn/pkg/app/releases/stable/2.3.106768/linux/TRAE_CN-linux-x64.tar.gz)（SHA-256 `12c47adc...`），确认根目录 `libsscronet.so` TTNet UA、`Electron/39.2.7`、`Chrome/142.0.7444.235` 与 AI 扩展 `versionCode: 20260212` 均未变；传输回环采样沿用 `3.4.20`（`2.3.103559`）包内 `@aha-kit/net`、原生 binding 和 `libsscronet.so` 样本 | `TraeClient/TTNet Cronet/TTNetVersion:a61b3479 2026-08-04 QuicVersion:40cc763c 2025-07-01`；产品 `3.4.24`、包 `2.3.106768`、AI 扩展 API version code `20260212`；设备类型 Linux，取不到硬件信息时发空串；不再固定写 Windows 品牌。原 MachineID、DeviceID、client ID、公私钥均保留 | ALPN `h2,http/1.1`；JA3 MD5 `cd08e31494f9531f560d64c695473da9`，JA4 `t13d1516h2_8daaf6152771_e5627efa2ab1`（3.4.20 回环样本，3.4.24 根目录 `libsscronet.so` TTNet 版本串未变）；HTTP/2 SETTINGS、窗口、优先级、应用头顺序、正文分帧以及 HTTP/1.1 回退已采样并校准，详见下文 |
| Trae 国内授权／刷新／用户信息 | 同一包 `main.js` 的 `Qg.request` 使用 `electron.net.fetch`；官方 Electron 39.2.7、原包 name/version 的最小启动应用向回环 TLS 服务发相同网络调用 | `TraeCN/1.107.1 Chrome/142.0.7444.235 Electron/39.2.7` 的 Linux UA；`Sec-Fetch-Site: none`、mode `no-cors`、dest `empty`、语言 `en-US`、Priority `u=4, i`；JSON 与 `x-cloudide-token` 按官方调用设置 | JA4 `t13d1516h2_8daaf6152771_d8a2da3f94cd`；ALPN h2/h1；HTTP/2 参数见下文。这是运行时与调用形状的回环样本，不是已登录官方 UI 的线上完整抓包 |
| TraeWork 国内网页 | 隔离 Chrome 156.0.8078.79 实际访问 [work.trae.cn](https://work.trae.cn/) 的 NetLog；取首次、不带 PSK 的 ClientHello，并核对网页 API 请求与 h2 帧 | 实际样本为 Linux `HeadlessChrome/156.0.0.0`，client hints 同 Chromium 156，语言 `zh-CN`、timezone `Asia/Taipei`、Priority `u=1, i`；不混入 IDE 的 MachineID 头；JSON 仅在有请求体时加，Origin 仅随创建会话 POST 发送 | JA4 `t13d1517h2_8daaf6152771_cb7bf5808d99`；ALPN h2/h1；Chrome 扩展顺序逐连接随机，JA3 不固定 |

ClientHello 的密码套件、扩展及 ALPN 从采样解析为独立的 uTLS spec；SNI、随机数、密钥份额按连接生成，证书仍按目标域名校验。Chrome 系链路每次打乱可随机的扩展，测试比较 JA4 与非随机字段；固定顺序的 Node／Bun／TTNet 同时检查 JA3。回归还逐项比较密码、压缩方法、session ID 长度、稳定扩展内容和 key share 类型／长度，并确认随机数、key share 与 GREASE ECH 没有复用样本或上一条连接的值。仓库只保存回环／公开站点 ClientHello，原始 NetLog、机器身份及账号流量不入库。

HTTP/2 使用 [`imroc/req/v3` v3.61.0](https://github.com/imroc/req/tree/v3.61.0) 的固定源码 Transport（`third_party/req`，原 MIT／Go BSD 许可保留；路径索引、服务端表大小与超大字段策略的差异见[专项记录](upstream-req-hpack-alignment.md)），uTLS 仍是现有 `refraction-networking/utls`，不自行维护连接池。不开 HTTP/3，也不跟随 Alt-Svc 另拨线路。Work 网页与 Electron 授权均实测 `SETTINGS` 顺序 `1=65536, 2=0, 4=6291456, 6=262144`，连接 `WINDOW_UPDATE=15663105`，伪头顺序 `:method,:authority,:scheme,:path`；HEADERS 独占、parent 0，实际 weight 分别 220／110（线上编码为 219／109）。这些参数及主要普通头顺序已设置；回环帧解析测试核对真实发出的 SETTINGS、窗口和 HEADERS 优先级。请求取消、首包超时、清空闲连接不截断活动流、平台与代理池隔离亦有回归。正常请求不会因 h2 协商回退而重复发送 POST。


TTNet 在独立 mount/network namespace 内使用临时 NSS 信任库，成功完成原生 CA 校验后采到 HTTP/2：`SETTINGS 1=65536, 2=0, 3=1000, 4=6291456, 6=262144`，连接窗口 `15663105`，HEADERS 独占、parent 0、编码 weight `219`。`@aha-kit/net` 将应用头经 WHATWG Headers 按字典序变成小写，TTNet 再追加 `x-request-id`、`x-tt-trace-id`、UA 和 `accept-encoding: gzip, deflate`。POST 不发 content-length，先发不带 END_STREAM 的正文 DATA，再单发空 DATA END_STREAM。HTTP/1.1 回退使用 `Host, Connection, Transfer-Encoding` 前缀、相同应用头序、`User-Agent, Accept-Encoding` 尾部，正文为 chunked。上述已确认项目均有真实线上字节的回环测试；fixture 使用假凭证和虚构设备 ID。

同一 TTNet 进程收到 GOAWAY 后的第二次 TLS 1.3 连接，官方样本恢复了会话。Trae 的传输使用每个 Transport 独立的内存会话缓存；首次不发空 PSK，恢复时将 PSK 放在末尾。后续实测将初始的 64 项／仅 SNI 实现修正为 1024 端点 LRU、主机名和端口隔离、TLS 1.3 两张单次票据，见下方缓存专项。缓存随平台、代理和钉 IP 隔离，不写入账号文件。回归覆盖首次不恢复、同线路恢复、换平台／线路不恢复。Qoder 原生 CONNECT 连续关闭重连样本未恢复会话，暂不为其启用缓存。另以同版 Electron 39.2.7 的 `electron.net.fetch` 在隔离 NSS 信任环境中补采，GOAWAY 后第二连接同样恢复了 TLS 1.3 会话；此前 Chrome Work 的 NetLog 也包含 PSK ClientHello。

Qoder Linux x64 原生包 SHA-256：国内 `85ba1f24c28c1fdeace69191d03d4b5fdfb6ac9b7e41b9528d4656bd2f975f81`，国际 `431bd471ae23add46a3cd9134b3c70a2dc2e81549b18f847038648b2af998f11`。推理补齐官方样本中的 Cache-Control、Connection、模型 key/source 与 W3C traceparent 字段。COSY 日期仍为 Unix 秒；凭证交换的 Date 保持 GMT，二者不混用。UMID helper 使用独立传输：后续 x64／ARM64 双地区采样确认是发往 443 的明文 HTTP/1.0，没有 ClientHello；早先“另一套 TLS”的判断已更正，不能将其套入业务 API profile。

尚未确认的边界：

- TTNet 原生 `x-tt-trace-id` 的生成现已按下方专项补齐。三条 Trae profile 的路径不索引和后续动态表字节已按连续请求样本校准，Work／Auth 另覆盖初始非默认表、扩容至 1 MiB、缩表、禁用与恢复，详见[差异与修正记录](upstream-req-hpack-alignment.md)。TTNet 在这些已等待 ACK 的样本中仍沿用原 4096 表；这一字节匹配不代表小表兼容性通过。帧调度及所有业务头也不能由基础 SETTINGS 与头序相同推出完整一致。
- Qoder 官方 `Cosy-MachineCode`、`Cosy-MachineType` 来自原生 UMID 组件，`Cosy-MachineHostname` 来自规范化的系统 hostname；网关保留导入设备身份，并为首次使用的账号接入官方组件初始化、持久化和按需刷新，不复制采样主机的身份。设备授权／PAT 等没有独立完整采样的路径仍按前文协议依据实现。
- UMID 自动初始化当前仅支持 Linux x64／ARM64；x64 已在 Alpine 3.20 加 `gcompat`／`libstdc++` 实测，ARM64 已通过 QEMU 用户态双地区运行与网络验证，尚未在原生 ARM64 硬件验收。组件从官方固定包提取、完整 SHA-256 校验，不随源码／镜像分发，文件名摘要与官方同为 SHA-256 前 16 位。官方 helper 忽略代理变量，现经 `proxychains-ng` 和单次本机认证桥使用所选代理，失败不直连；网络受限容器中，当前源码桥的双地区真实交互通过。实际传输是 HTTP/1.0 明文到官方 443，业务 Bun TLS profile 与此分开。依赖、持久化、重试与导入边界见[Qoder 设备身份](qoder-device-identity.md)。
- Trae Linux 的 OS 描述当前使用官方失败回落值 `Linux`，没有复制采样宿主的发行版、硬件名或设备信息；已导入凭证没有保存完整原设备系统资料。已保存 ID 与密钥不因这次校准重建。
- 核对上述版本与运行时的首次握手，并补采下方列明的 TLS 1.2／1.3 重连、短寿命票据和缓存容量／LRU／消费行为。内存压力清理、过期条目的批量清扫、其他 0-RTT 配置、TCP/IP 内核特征、HTTP/2 并发与流控动态、所有头／业务路径及长期风控兼容性未全量验证。样本匹配不等于平台认可，也不保证未来版本继续相同。
- 最新真实回归覆盖 Trae Code 国内 `Doubao-Seed-Code` 的三协议流式／非流式文本、三协议工具回传，以及 Claude Code／Codex 的读文件、修复和测试闭环，范围见[验收记录](multi-platform.md#trae-code-三协议与工具验收)。Qoder 国际账号已存在，但官方客户端和网关均无可用目录，推理尚未验收，不能据回环测试标为真实验收。

### TLS 重连、过期与 TTNet trace 补齐（2026-11-17）

再次使用同一官方 TTNet 原生库、Electron 39.2.7 及 Work 同版 Chrome 156.0.8078.79，在独立网络／mount namespace 内仅连接可信的回环 TLS 服务，使用假凭证和虚构设备字段。矩阵分别限制 TLS 1.2／1.3，收到 HTTP/2 GOAWAY 后重连；第一次完整握手、第二次恢复，服务端更换票据密钥后第三次完成完整握手，第四次使用新票据恢复。三套官方运行时均得到 `false → true → false → true`。Chrome 由本地引导页调用原生网页 fetch，未替换网络栈。网关离线回归对三条 Trae profile 验证相同行为，另覆盖服务端关闭票据时始终完整握手，并确认四次连接只发送四次业务 POST。

`clienthello/trae-{code,work,auth}-tls{12,13}-resume.bin` 保留官方第二连接的 ClientHello。回归逐项比较恢复握手的稳定字段；票据密文、年龄、binder 和由 SNI／票据长度决定的 padding 不固定为采样值。TLS 1.3 的 PSK 仍由 uTLS 生成和验签，不复制票据或密钥。

另用 OpenSSL 3.6.5 的独立诊断服务下发两秒寿命的票据，在同一官方进程中发送 GET、立即 GET、等待超过寿命后 GET、再 POST，响应关闭连接：

| 运行时／协议 | 立即重连 | 超过两秒后 | 随后的新票据 | 0-RTT |
|---|---|---|---|---|
| TTNet／TLS 1.2 | 恢复 | 仍携带旧 SessionTicket，服务端拒绝后完整握手 | 恢复 | TLS 1.2 不适用 |
| Electron 授权／TLS 1.2 | 恢复 | 同上 | 恢复 | TLS 1.2 不适用 |
| Work Chrome／TLS 1.2 | 恢复 | 同上 | 恢复 | TLS 1.2 不适用 |
| TTNet／TLS 1.3 | 恢复 | ClientHello 不再提供过期 PSK，完整握手 | 恢复 | 服务端允许 early data，样本未发 early_data 扩展或提前业务数据 |
| Electron 授权／TLS 1.3 | 恢复 | 同上 | 恢复 | 同上 |
| Work Chrome／TLS 1.3 | 恢复 | 同上 | 恢复 | 同上 |
| WorkBuddy Electron 37.10.3 的 Node fetch／TLS 1.2、1.3 | 均未恢复 | 完整握手 | 仍未恢复 | 未发送提前业务数据 |

这些结果支持当前 WorkBuddy 不开会话缓存、Trae 不启用 0-RTT 的策略，仅覆盖该版本、默认调用与上述 GET／POST 场景。Qoder 原生此前的关闭重连样本未恢复，仍不开缓存；本轮没有据此外推其所有 TLS 版本行为。Trae TLS 1.3 的本地回归使用配置时钟推进超过假上游票据寿命，验证过期票据被移除、不发 PSK、新票据可再次恢复；uTLS 配置继承标准配置的 `Time`，使证书和票据检查使用同一时钟。

Work Chrome 的多连接样本还确认签名算法列表首项 GREASE 会随机变化。uTLS 1.8.2 的 `ApplyPreset` 会重写 cipher／group／key share／version 的 GREASE，却不重写 `SignatureAlgorithmsExtension` 中的 GREASE，直接解析抓包会一直沿用样本值。网关现对样本实际包含的签名 GREASE 每连接独立随机生成，不改变其他算法或位置；八次新连接回归确认没有固定在采样值。稳定字段比较对该随机项归一化；JA4 辅助函数也按 [FoxIO 官方定义](https://github.com/FoxIO-LLC/ja4/blob/main/technical_details/JA4.md) 排除签名 GREASE。因此表中的 Work JA4 从旧的 `f4f7aae15fbc` 校正为 `cb7bf5808d99`；旧值是比较辅助函数误计随机 GREASE 的结果。

后续连续编码专项补齐三条 Trae profile 的路径不索引与超大字段清表，以及 Work／Auth 的服务端表大小处理，50 个官方同值请求头块均逐字节匹配，详见[HPACK 专项](upstream-req-hpack-alignment.md)。根模块全量普通测试、固定依赖全量及相关 race、providers race、build、vet、lint、gofmt 和 diff 检查通过。upstream 的既有超大响应测试在整包 race 中两次触及两秒等待上限；该用例单独 race 通过，其余 upstream 用例分开 race 通过，未改其等待时间，不能把整包一次运行记为通过。

这批修正已保数据更新至 dev，实际容器内的 Work `deepseek-v4.1-flash` 与 Code `Doubao-Seed-Code` 各一次流式回复均得到正常正文和结束帧。配置、auth 文件、账号设置和原有会话绑定保持；两次新请求只新增自己的绑定。Qoder 国内的本机 UMID 缓存恰好到期，启动后的账号操作正常刷新 `machine_token/machine_code/machine_type`：旧值与旧缓存一致，新值与新缓存一致，旧缓存刷新期限早于本次更新，`machine_local` 仍为 true。其余凭证字段（含当前账号授权 token、设备 ID 与密钥）逐项一致；没有回填旧 UMID 或旧 token。这次未做面板目视验收，也未重复发起 Qoder 推理。

TTNet trace 通过官方 `libsscronet.so`（SHA-256 `1ff0035645652c9b0e71412a6601730a60ad6bcb5a8089718a340f8813ea643c`）的字符串交叉引用定位到原生生成函数，再在独立诊断进程固定时间和随机输入，取得十进制设备 ID、非数字、正负号及溢出输入的样本。未修改的 `@aha-kit/net.fetch` 另以虚构数字设备 ID 采样，验证了相同的设备编码：

- 格式为 `00-<32位trace>-<trace前16位>-01`。
- trace 前八位是 Unix 毫秒对 `0xffffffff` 取余后的八位小写十六进制，不能替换为普通 uint32 截断。
- 设备 ID 按有符号十进制 int64 解析，再以 uint64 十六进制编码，前置两位十六进制字符长度；原生解析失败时使用数值零。
- 中间以独立随机十六进制字符填足，最后四位为 app ID 的定宽十六进制值。官方国内 `801256` 超过四位上限，原生结果为 `ffff`。

网关按每次请求的 `X-Device-Id` 和新的随机值生成 trace，不使用采样设备或跨账号的进程全局身份；保留调用方已经提供的 trace。生成仅应用于 Trae Code 的 TTNet profile，HTTP/2 与 HTTP/1.1 回退都发送并保持原生尾部头序。固定输入原生向量、线上头序、不同请求生成不同 trace，以及既有同值 HPACK 字节回归共同验证这项改动。该 trace 不等于账号认证或设备证明，也不据此承诺平台长期接受全部请求。

本轮离线 Go 全量测试、upstream／providers race、构建、vet、golangci-lint、gofmt 与 diff 检查通过。首次 race 的既有目录超大响应测试触及两秒等待上限，单项及完整复跑均通过，未修改该测试。保数据更新 dev 后，实际服务容器内的 Trae Code 流式请求正常返回正文与结束帧；完整账号凭据（含当前 token）、设备字段、配置、auth 文件和账号设置保持一致。原有会话绑定逐项保留，本次真实请求正常新增自己的绑定；没有从旧备份回填数据。

### TLS 缓存容量、端口与票据消费（2026-11-17）

沿用官方 TTNet 原生库、Work 同版 Chrome 156.0.8078.79 和 Electron 39.2.7 `net.fetch`，在只启用回环的隔离网络中连接可信 TLS 服务；没有使用真实账号。服务端只提供 HTTP/1.1，并在每次 GET 后关闭连接，确保每次都重新握手。同一 SSLContext 的监听端口共享票据密钥，排除“服务端不接受跨端口票据”对客户端缓存隔离判断的影响。

| 采样 | 三套官方运行时的共同结果 | 实现修正 |
|---|---|---|
| 同一主机的两个端口，TLS 1.2／1.3 各访问 A、A、B、A、B | `DidResume=false,true,false,true,true`；B 首次访问不携带 A 的会话 | 从仅按 SNI 改为按原始主机名和端口缓存；默认 HTTPS 端口规范为 443，主机名转小写。地址来自请求上下文，不使用代理或钉 IP 的实际 TCP 对端作为身份 |
| TLS 1.2／1.3 各先访问 1024 个不同端口，重访第 0 项，再插入新端点，最后访问第 1 项和第 0 项 | 第 0 项仍恢复；插入第 1025 个端点后第 1 项不恢复；第 0 项仍恢复 | 从 64 项改为 1024 项 LRU；命中会提升最近使用顺序 |
| TLS 1.3 首次握手发两张票据，之后不再补发，连续访问五次 | `false,true,true,false,false` | 每端点保存最新两张 TLS 1.3 票据，每张只消费一次；不再重复复用同一 PSK |
| 首次改发五张票据，其余条件不变 | 仍为 `false,true,true,false,false` | 两张上限按端点计算，丢弃更早票据；TLS 1.2 继续复用最新票据 |

Chromium 的公开 [SSLClientSessionCache 定义](https://chromium.googlesource.com/chromium/src/+/main/net/ssl/ssl_client_session_cache.h)和[实现](https://chromium.googlesource.com/chromium/src/+/main/net/ssl/ssl_client_session_cache.cc)可交叉确认 1024 项默认值、HostPortPair、LRU 及两张 single-use 票据；上表结论以这里实际运行的三个版本独立采样为准，没有把当前 main 的其他行为直接套给旧运行时。

uTLS 的 `NewLRUClientSessionCache` 只保存一张可重复读取的会话，且默认键为 SNI，不能表达上述策略。网关新增仅存内存的窄范围适配，使用标准库 `container/list` 维护 1024 项顺序；互斥锁保证并发连接不会消费同一张 TLS 1.3 票据。连接绑定原始请求端点，TLS 握手、密钥、PSK binder、证书和票据过期检查继续交 uTLS，既有线路和账号持久化不变。

离线回归先复现旧实现在两个 TLS 版本下跨端口错误恢复，再覆盖端口隔离、LRU 边界、TLS 1.2 重用、TLS 1.3 两张上限／消费、清除及并发消费。另将实际网关传输连接到同一隔离服务，三条 profile 各六组、共 18 组对照全部得到与官方相同的完整握手结果序列，其中每组容量实验包含 1028 次真实握手，官方与网关各完成 6228 次握手。诊断程序和完整无账号流量证据保存在仓库外。

本轮根模块全量普通测试、缓存／ClientHello／HPACK 专项、构建、vet、golangci-lint、gofmt 和 diff 检查通过。upstream／providers 的 race 在排除既有 `TestListingModelsDevFailuresSilent` 后通过；该测试的“超大响应”子项在整包、单独测试和单独子项的 race 中均触及两秒等待上限。本轮未修改这个无关测试，不能记作全量 race 通过。

缓存修正已保数据更新至 dev，容器健康检查通过。实际服务容器内的 Work `deepseek-v4.1-flash` 与 Code `Doubao-Seed-Code` 各一次流式请求均返回正常正文和结束帧。重建前后以及真实请求后，配置、auth 文件、完整凭据和账号设置一致；重建前后的全部会话绑定逐项一致。真实请求复用的绑定仅 `expires_at` 正常续期，账号和上游会话 ID 均保持，已用停机备份逐字段核对；未删除绑定或回填旧凭据。这轮没有重复验收 Qoder 推理。

本次覆盖正常内存条件下的上述行为。恢复握手阶段 EOF／handshake_failure 的后续行为已按下节补采；Chromium 的网络匿名化／隐私模式、客户端证书、内存压力触发缩容、批量过期清扫及其他握手异常后的全部失效策略尚未完整采样；目前网关也没有网页跨站上下文的匿名化键模型。不能据这组结果称为所有缓存行为完全一致。

### HTTP/2 动态流控、并发与恢复早期失败（2026-11-17）

沿用上述三个官方运行时、可信回环和假身份，新增以下独立采样，未访问真实账号：

| 场景 | 官方行为与修正 |
|---|---|
| 连续消费 20 MiB HTTP/2 响应 | 三者均在消费量超过流窗口／连接窗口一半时发送 WINDOW_UPDATE。流为 6 MiB、连接为 15 MiB。原网关按 Go 的约 4 KiB 阈值频繁更新，现仅对三条 Trae profile 改为官方合并策略 |
| 小响应在约 4.8 秒、5.2 秒后继续消费 | 三者均在超过五秒后的下一次消费归还累计额度；没有新消费时不单独启动补窗定时器。源码交叉依据为 Chromium `SpdyStream::IncreaseRecvWindowSize` 与 `SpdySession::IncreaseRecvWindowSize`，本轮运行的官方样本独立确认默认五秒 |
| 先发 5 MiB，客户端暂不读正文 | 网关旧实现声明 6 MiB 流窗口，内部却仍按默认 4 MiB 扣额度，可拒绝合法响应。现使用实际发出的 INITIAL_WINDOW_SIZE 初始化额度；离线回归先复现失败再验证合法响应完整读完 |
| 服务端 MAX_CONCURRENT_STREAMS=2，预热后并发八次请求 | 官方均保留一条连接、最多两个在途流，排队完成其余请求。网关开启已有的 StrictMaxConcurrentStreams，按同一连接排队；三条 profile 的实际传输对照均得到一连接、峰值两流、九条完整响应 |
| 库的非严格模式另开 HTTP/2 连接 | 原生 uTLS 的 ConnectionState 返回类型不同，req 后续拨号强制断言接口时会 panic。网关增加保留实际握手方法与标准 TLS 状态的连接适配，初次和后续连接均使用；回归同时覆盖严格排队和非严格新建连接，后者确保没有用排队设置掩盖崩溃 |

窗口更新的具体增量取决于运行时的读缓冲分片，官方独立运行也并非逐帧相同：本轮大响应增量大致为流半窗加至多一次读取量、连接半窗加一次读取量。网关校准窗口、严格大于半窗的条件与五秒消费触发，不将某次读取分片硬编码成固定包大小；不能把这个策略匹配写成所有 WINDOW_UPDATE 帧逐字节相同。

另在 TLS 1.2／1.3 中，先取得票据，再在第二次 ClientHello 后由服务端直接关闭，或发送 fatal handshake_failure，随后保持原票据密钥。TLS 1.3 首次发送两张票据，此后不补发。三个官方运行时均在 HTTP 正文发送前自动重连一次：

- Work／Auth 的 TLS 1.2 继续使用原票据，TLS 1.3 消费下一张未用票据。
- TTNet 的紧接重连不提供票据，但保留尚未消费的票据；TLS 1.3 再下一次请求可以恢复。先前把它解释为清空端点缓存是不正确的。
- 网关只在已选到缓存票据、上下文仍有效、握手返回 EOF／UnexpectedEOF 或远端 handshake_failure 时重连一次。该操作仍在 TLS 回调内，尚未发送 HTTP 请求；证书失败、其他 TLS 错误、没有缓存的首次握手或已发送的业务请求不走这条重试。

网关将 uTLS 的握手期删除通知延后至握手结果已知，再区分失效与上述暂时失败；过期／失效会话仍清理，新票据仍由 uTLS 校验与保存。三条 profile、两个 TLS 版本、两个故障类型，共 12 组实际传输对照的完整事件序列与官方一致，每侧 60 次握手，每组仅四次业务 POST。

继续在同样的隔离环境补采上传、取消、REFUSED_STREAM 与 GOAWAY，共三种官方运行时各四组；修正后的网关均通过对应场景：

- 服务端声明 32 KiB 流窗口，固定正文上传 256 KiB；三者的单个 DATA 负载最多 16375 字节，流或连接额度归零时停止，收到 WINDOW_UPDATE 后继续。网关原先发送 16384 字节，现仅为这三条 profile 限制为 16375；缓冲池取到更大内存时也按本次限制读取。Work／Auth 本组 DATA 长度、END_STREAM 和剩余额度事件与官方逐项相同。
- 读取未结束的响应时取消；三者均发送 RST_STREAM CANCEL（8），随后在同一连接发出下一次请求。网关对照一致。
- Work／Auth 的 POST 被 REFUSED_STREAM（7）拒绝时，在原连接重放；GOAWAY NO_ERROR、last_stream_id=1 排除当前 stream=3 时，在新连接重放，正文保持一致。对照均通过；这只覆盖服务端明确未处理的场景，不代表可以重放已处理的业务请求。
- TTNet 的 `@aha-kit/net/wrapper.js` 通过 `bodyProviderPair` 逐块交给原生层，没有回放源；上述两种拒绝在官方均报 `internal_code=-351`，没有再次发送该正文，后续独立请求仍能成功。网关原先保留 Go GetBody 而重发，现按该路径取消正文回放，并保留 HTTP/2 拒绝原因与无法回放错误。

TTNet 的固定正文结束条件另外覆盖 2、16374、16375、16376、32750、32751、65536、262144 字节，各重复两次：首块内结束时另发空 END_STREAM；跨过首块后可在最后一个非空 DATA 上结束。网关对已知长度、可重建的固定正文保留内部长度，仅用于还原读取结束状态，线上仍不发送 Content-Length、也不允许回放。16 组完整 DATA 长度／结束标志／剩余额度事件与官方相同；未把这个结论扩展到未知长度、任意逐块生产的上传源。后续逐块上传已确认差异，见下节。

离线新增上传额度与分片、结束边界、拒绝后的正文重放、后续连接恢复，以及首次握手 EOF、其他 TLS 告警、取消后不重连、连续恢复失败只重连一次、新票据不被旧失效通知误删的回归。尚未覆盖多流上传压力、任意上传源、反复 GOAWAY／拒绝和取消竞态的全部调度，也未据此称所有动态帧逐字节一致。

最终代码的固定 req 全量普通测试、HTTP/2／HPACK race、根模块全量普通测试、upstream／providers race（排除前述既有 `TestListingModelsDevFailuresSilent`）、构建、vet、lint、格式与 diff 检查通过。保数据重建后的 dev 健康；在实际容器内，Work `deepseek-v4.1-flash` 与 Code `Doubao-Seed-Code` 的流式正文、结束帧及无错误检查均通过。配置、auth 文件、完整凭据和账号设置保持一致，重建前后的全部绑定一致；真实请求后两条复用绑定仅到期时间续期，账号与上游会话 ID 保持，已用停机备份逐字段核对，未回填旧凭据。本轮未重复验收 Qoder 推理。

### 连续拒绝、取消与未知长度上传（2026-11-17）

继续使用未修改的 Chrome 156、Electron 39.2.7 `net.fetch` 与官方 `@aha-kit/net`／TTNet，在隔离网络中连接可信回环假上游。连续拒绝同一 POST，分别采集 REFUSED_STREAM 与排除当前流的 GOAWAY；另确认两次拒绝后的第三次发送可以成功。GOAWAY 服务在关闭前排空控制帧，避免未读 ACK 触发 TCP RST；早期未排空的结果不作为重试结论依据。

| 客户端 | 同一正文被连续拒绝时的官方行为 | 网关修正 |
|---|---|---|
| Work／Auth | 原请求加两次立即重发；第三次仍拒绝则报错，后续独立请求正常 | 原先第三次拒绝后等待约一秒再发第四次；现共享一次外层请求的两次重发预算，缓存连接与新连接不重复计数 |
| TTNet | 只发送一次，没有可回放源，报错后独立请求正常 | 保持不可回放，不启用 Work／Auth 的预算策略 |

离线回归覆盖三条 profile × 两种拒绝 × 拒绝一／二／三次 × 冷／复用连接，共 36 个场景，同时检查正文、发送次数、GetBody 调用次数、拒绝原因及独立后续请求。另在收到正文后、发出拒绝前取消上下文：必须返回取消原因，不重发旧正文，后续请求完整返回。预算耗尽与取消的检查在创建回放正文之前，避免创建未使用且未关闭的新正文。上述回归已通过 race；最终源码重新编译诊断客户端后，三种 profile × 两种拒绝的六组请求事件均与各自官方样本一致，客户端均保留拒绝错误，后续独立请求成功。比较不把执行时延当成固定指纹。

未知长度上传最初完成两轮官方采样：每 25 毫秒生产 1、16374、16375、16376、32751 字节，共 81877 字节；服务端流窗口 32768、连接窗口 65535，零窗口等待 80 毫秒后补充。三种官方客户端均正确暂停与恢复，当时网关与官方仍有以下确定差异；后续修正见下一节：

- Work／TTNet 在等待补窗期间积累后续块，继续读取时可合并块边界，并在最后非空的两个字节上设置 END_STREAM。网关以 `io.Pipe` 对照时仍分别读出前一生产块剩下的一个字节，最后另发空 END_STREAM。
- Auth 在发送前收完正文，首个 DATA 为 16375 字节；当前网关仍立即发送首个一个字节。该行为有 [Electron v39.2.7 的 net-fetch.ts](https://github.com/electron/electron/blob/v39.2.7/lib/browser/api/net-fetch.ts) 与 [net-client-request.ts](https://github.com/electron/electron/blob/v39.2.7/lib/common/api/net-client-request.ts) 依据：fetch 将正文 pipeTo 默认 ClientRequest，后者使用 SlurpStream，在 finish 后才启动请求。不能为追求采样帧数而猜测增加缓冲规则。

Work 的 CORS 预检单独记录；比较业务流时归一化 stream ID，不把 OPTIONS 算成额外业务请求。上述两轮结果确认了缺口，当时尚未修正，不属于“全部动态帧一致”。多流上传压力和全部取消竞态也仍未完成。

本轮 Go 全量普通测试、upstream／providers 完整包 race、固定 req 全量 race、build、vet、golangci-lint、gofmt 和 diff 检查通过；完整包 race 未排除旧的超大响应测试。保数据更新 dev 后容器健康，实际服务容器中的 Work／Code 各一次流式回复、正常结束与无错误检查通过。配置、auth 文件、账号设置和全部已有绑定在重建时保持；真实请求只使两条原有绑定正常续期，账号与上游会话 ID 未变。

构建前摘要与停机备份之间，Qoder 国内 UMID 恰好到期刷新，仅 machine_token／machine_code／machine_type 更新；旧字段与旧缓存一致，新字段与新缓存一致，旧刷新期限早于本次更新，更新时间处于镜像构建期间、停止服务之前。停机备份与重建后、实测后的完整凭据一致；账号授权 token、设备 ID、密钥及 machine_local 均保持，未回填旧凭据。最初严格摘要比较的失败记录保留，并以停机备份和两代缓存独立核实此变化。本轮未重复调用 Qoder 推理，所有改动仍未提交、未推送。

### 未知长度正文的缓冲与完成通知（2026-11-17）

在前节确定差异后，进一步核对官方实现，再修正 `profile_upload.go`：

- Work：同版 Chromium `156.0.8078.79` 的 [ChunkedDataPipeUploadDataStream](https://github.com/chromium/chromium/blob/156.0.8078.79/services/network/chunked_data_pipe_upload_data_stream.cc) 从 Mojo 管道读取已经到达的数据，完成大小已知时可将 END_STREAM 放在最后一个非空 DATA。[MojoCreateDataPipeIpcz](https://github.com/chromium/chromium/blob/156.0.8078.79/mojo/core/core_ipcz.cc) 的默认容量明确为 64 KiB。网关现在将生产正文与等待 HTTP/2 窗口分开，未读队列限制为 64 KiB，读取时合并已到达的数据。
- TTNet：官方 `@aha-kit/net-linux-x64-gnu` `0.4.0-dev.25113f695920251126182417` 的原生包 SHA-256 为 `c43a3b188d98fb9a3f5c81562051bf3bff7ba6a4ec2fda8d95d96ddc9ba0cfd4`。直接调用原生 `bodyProviderPair` 而不启动消费者，分别发送 1、16375、65536、262144 字节的块，四组均只完成前三次发送，第四次等待。`body_provider_pair_c_callback` 的反汇编同时显示容量 3，调用 Tokio 有界 mpsc。网关未知长度正文相应使用三条消息的队列，完成／读取错误也通过队列传递；正文仍不可回放。
- Auth：按前节已核实的 Electron SlurpStream，在发送前收取未知正文，之后提供固定长度和回放源；原有已知、可回放的固定正文不额外复制。补采并解码官方 HPACK 后，未知流的 Content-Length 实际为 81877；Work 的流式上传与 TTNet 均不发送此字段，网关回归分别检查这三种结果。

新网关在相同零窗口场景连续复采两轮，三种 profile 共六组完整 DATA 长度、结束标志、剩余额度与暂停／补窗事件，归一化 Work 预检流号后，均与各自官方样本一致。这里比较的是业务帧事件，不包含所有请求头字节或运行时延。已知固定正文继续使用此前的读取方式，避免改变 TTNet 首块内结束时独立发送完成通知的行为。

离线回归先在旧实现复现三个 profile 的失败，再检查完整正文、Content-Length、分片、最后非空结束帧和同连接后续请求。缓冲为空／已满时取消的回归检查生产 goroutine 退出、正文恰好关闭一次和取消原因；有界队列通过阻塞后续生产验证背压。Auth 在读取失败或取消时不得开始拨号，保留原始读取错误或取消原因；Work／Code 拨号失败也必须关闭生产源。关闭正文会等待生产 goroutine 退出。相关回归通过 race；最终源码重新编译诊断客户端后，三组上传业务帧事件再次与官方一致。

Go `io.Reader` 不携带任意 JS Uint8Array 的原始生产边界；适配器以每次 Read 为生产块，单次生产读取缓冲为 64 KiB。上述结果仅证明已采样的逐块时序、窗口与结束条件，不能外推为任意上传源、跨线程调度、全部取消竞态或多流压力的逐帧一致。所有未确认项继续保留。

本轮最终代码 Go 全量普通测试、upstream／providers 完整包 race、build、vet、golangci-lint、gofmt 与 diff 检查通过。完整包 race 首次在既有目录超大响应测试触及两秒等待上限，保留失败日志，未修改或跳过该测试；完整复跑通过。固定 req 与前端产物沿用前轮已验证版本。

上传修正已保数据更新 dev，容器健康；实际服务容器内 Work／Code 各一次流式请求返回正常正文与结束帧，无错误。重建前后和实测后，配置、auth 文件、完整最新凭据与账号设置一致；重建时全部已有绑定一致，实测只使两条复用绑定正常续期，账号与上游会话 ID 保持。原始严格摘要中的绑定变化记录保留，并另与停机备份逐字段核实。未回填旧凭据，本轮未重复 Qoder 推理，未提交、未推送。

### 多流上传、零窗口取消与 TTNet 残片读取（2026-11-17）

进一步使用未修改的 Work 浏览器、授权 Electron 和 TTNet 原生网络模块采样。主矩阵为三种运行时 × 固定／未知长度正文 × 单流／连接窗口归零 × 两轮，共 24 组官方样本；修正后网关以相同采样器完成 24 组。先在同一连接预热，再启动三个各 128 KiB 的上传，服务端限制最多两个并发流、初始流窗口一个字节；第三流在取消前排队。两种取消条件分别为两个活动流耗尽各自窗口，以及共享连接窗口归零。

这组样本与离线回归已确认：零窗口不阻塞 RST_STREAM／CANCEL；取消一个活动流后释放并发槽位；已取消流收到迟到 WINDOW_UPDATE 后不再发送旧正文；另外两份正文及完成事件保持完整，最后一个探针继续复用同一连接，没有重放取消的正文。这里检查的是稳定的协议行为，不表示 DATA 的全部交错已一致。

采样发现并修正一个确定的 TTNet 差异：未知正文每 10 毫秒生产 16 KiB，取消前累计发送 32768 字节时，官方 DATA 长度为 `[1, 16374, 16375, 18]`，旧网关却为 `[1, 16374, 9, 16375, 9]`。生产间隔改为 100 毫秒后，官方仍保持前一种结果。相同原生包的 `tokio_ttnet::body::upload_data::create_upload_data_provider::read` 及其异步 closure 显示，官方先复制当前消息的残片，用完后再接收至多一条新消息；下一消息未到达时，即使已复制部分字节也会等待。网关现在将当前消息残片与三条消息的队列分别保存，按该方式读取。Work 继续使用已有的管道方式，Auth 继续预先收完正文。

离线帧回归先在旧实现复现多出的两个 9 字节 DATA，修正后通过；另覆盖残片等待期间收到下一块、EOF、读取失败和取消，分别检查正文连贯、完成通知、带操作名的原始错误，以及取消后不输出残片、生产者退出和源恰好关闭一次。官方和最终网关两轮样本中，取消前该流的分片与结束标志一致。

跨流调度仍有确定缺口：固定正文额外重复采样后，Work／Auth 的两个存活流在官方六轮中的分片各自稳定，网关仍随 goroutine 调度变化。TTNet 官方自身的存活流有多种交错，不能拿其中一轮作为唯一顺序；也不能据此忽略 Work／Auth 已确认的差异。发送调度、任意生产块映射与其他取消竞态继续待修正和核对，尚未达到完全对齐。

采样器修正了两个边界：预热请求可能在客户端收到初始 SETTINGS 前按原窗口发送，属于有效行为；TTNet 的独立空完成帧也需要给探针保留发送机会。未知长度上传取消时，还复现官方 `@aha-kit/net` 包装层向已关闭队列调用 `sendError`，产生未处理的 Promise rejection。官方 `out/bootstrap-fork.js` 会记录该异常而继续运行，采样宿主采用相同的记录方式，保留原始日志，未修改官方网络模块；业务 fetch 的失败仍使验收失败。这是官方包装层问题的本地记录，不表示本仓库复刻其进程异常，也没有代用户对外提交 issue。

最终 Go 全量普通测试、upstream／providers 完整包 race、build、vet、golangci-lint、gofmt 与 diff 检查通过。相关上传回归通过三轮 race；新测试按 staticcheck 建议改用 switch 后，另跑多流 race 通过。完整包 race 首次仍在既有超大响应测试触及两秒等待上限，未修改或跳过，保留失败日志后完整复跑通过。前端和固定 req 本轮没有修改，沿用已有验证。

已保数据更新 dev，实际服务容器内 Work／Code 流式正文、完成事件与无错误检查通过。重建时全部已有绑定和最新凭据、设备字段、配置、认证文件、账号设置保持；实测只续期仍有效的绑定，并按既有逻辑清理在实测开始前已过期的绑定。有效绑定的账号与上游会话 ID 未变。严格摘要比较的绑定变化及最初续期检查的失败报告均保留，另以停机备份、到期时间和 `internal/providers/execute.go` 的清理分支核实，未回填过期绑定或旧凭据。本轮未重复 Qoder 推理，未提交、未推送；多流发送调度缺口继续保留。

### Work／Auth 连接级上传队列与采样时序（2026-11-17）

上节记录的固定正文差异已在后续修正。源码依据为 Work Chromium [156.0.8078.79 的 SpdySession](https://github.com/chromium/chromium/blob/156.0.8078.79/net/spdy/spdy_session.cc)、授权 Electron 39.2.7 所用 Chromium [142.0.7444.235](https://github.com/chromium/chromium/blob/142.0.7444.235/net/spdy/spdy_session.cc)，以及各自同版本的 `spdy_stream.cc`、`spdy_write_queue.cc`、`spdy_http_stream.cc`。官方每流最多排一个 DATA，同优先级按 FIFO 写出；排帧时预留连接和流窗口，写完成后将该流下一帧放到队尾。可同步读取的内存正文在写完成回调中继续读取，异步正文等待不阻塞其他流；同步写循环排空当前队列，再处理下一次 socket 读取。

固定 req 副本增加默认关闭的 `SetHTTP2ChromiumUpload`，仅 Work／Auth 开启。连接级队列代替各流独立抢写锁，保留每流最多一个已预留 DATA；写循环运行时的补窗留待当前可写队列排空后按接收顺序处理，取消和连接错误仍可中断阻塞写入。未写出的排队 DATA 取消时归还窗口，空的结束 DATA 不消耗窗口；其在零／负窗口下仍需等待，后续修正见下一节。补窗溢出会终止流或连接，并修正原有流溢出分支重复获取连接锁的问题。TTNet 继续走原有 writer，不能以 Work／Auth 的采样替代其原生调度。

生产的内存 JSON 改由 `NewBufferedRequest` 建立请求，正文及回放共用已有字节，去掉原来的字节转字符串复制。它与未知正文都保留 `ReadAvailable`，只同步读取已就绪数据；req 的 `readTrackingBody` 转发该接口，并仅在真正读取时标记正文已消费，维持未读正文的重试判断。普通 Reader 仍异步读取。

离线回归保留上节六轮官方的四个固定正文向量，没有放宽断言：修正前重复测试失败，修正后连续十轮通过；相关上传三轮 race 通过。固定依赖另覆盖等待正文不阻塞另一流、阻塞写入期间取消排队 DATA 并归还窗口、读取失败／长度超限、零窗口结束／trailers、补窗溢出、Clone 隔离和读取跟踪与回放判定。

复采进一步发现 Work 浏览器等待并发槽位时只预取一块，下一次 `pull` 在取得槽位后才继续；网关原先已持续拉取来源。Work 现于上传层首次读取后启动有界管道生产，取消尚未开始的上传也会关闭源并退出生产 goroutine。采样器同时对齐官方第九次 `pull` 才通知 EOF 的时机，保留最初采样报告。

两轮原采样条件下的修正后网关共 24 组，均通过正文、取消、槽位释放和复用检查，Work／Auth 固定正文逐流 DATA 长度及结束标志与原六轮官方基准一致。对异步正文继续检查时，发现 Python 采样服务端默认 Nagle／延迟 ACK 会造成约 40 毫秒补窗间隔，从而改变期间积压的正文。随后为采样服务端显式开启 TCP_NODELAY，官方三种运行时及网关各重采两轮、各 24 组，全部协议完整性检查通过。新官方 Auth 的一组固定正文也出现不同分片，说明原六轮稳定结果不能外推成所有 socket 时序的唯一向量。

这轮只确认上述队列和读取时机修正。统一 TCP 设置后，Auth 未知正文的两种零窗口场景与两轮网关逐流匹配；Work 未知正文部分存活流及 TTNet 存活流仍有分片差异。其他异步读取完成事件与 socket 写阻塞时的调度尚未完全复刻，不通过任意 sleep 固定时序，也不把新增官方变化当作忽略剩余差异的依据。TCP/IP 栈、任意 JS 生产块映射及全平台全部网络行为仍不能宣称完全一致。

本轮最终检查：根模块普通全量、upstream／providers 完整包 race、固定 req 全量 race、相关上传三轮 race、build、vet、golangci-lint、gofmt 和 diff 均通过，没有修改或跳过既有超大响应测试。台湾时间 22:08 已保数据更新 dev，实际服务容器内 Work／Code 流式正文与结束事件均通过；配置、认证文件、完整最新凭据及账号设置一致，两条已有绑定仅续期，账号及上游会话 ID 未变。严格绑定差异报告与独立续期核对均保留。前端源码未改，复用已有构建产物；未提交、未推送，其他待办保持。

### 空结束 DATA 的零窗口与负窗口等待（2026-11-17）

继续核对 `SpdySession::CreateDataBuffer` 时，发现上一轮对空结束 DATA 的判断不符合官方实现：HTTP/2 的零字节 DATA 虽然不消耗流控额度，该 Chromium 路径仍先检查流窗口与连接窗口，任何一个小于等于零都会排队。不能据通用协议允许的行为替代目标客户端的实际行为；旧回归中“零窗口立即发送空结束帧”的预期也随之纠正。

独立样本使用未修改的 Work 浏览器和官方 TTNet 网络模块。服务端先下发 `INITIAL_WINDOW_SIZE=1`，客户端发送一个未结束的字节后，通过控制端点通知来源结束。零窗口场景此时流窗口为零；负窗口场景再下发 `INITIAL_WINDOW_SIZE=0`，等 SETTINGS ACK 后通知来源结束，此时流窗口为负一。两者在后续 150 毫秒内都没有发出 DATA；服务端分别补窗一个或两个字节、使流窗口恢复为正一后，才收到零字节 END_STREAM。Work 与 TTNet 共十份官方样本，修正后网关各两轮、共八份样本，DATA 长度、结束标志与补窗先后均一致。

旧网关四个场景都复现差异：Work／TTNet 的零窗口和 TTNet 的负窗口会提前发送空结束帧；Work 的负窗口会把负额度作为切片长度，触发 `slice bounds out of range [:-1]` panic，导致采样进程退出。这里确认的是该特定路径的退出原因，不能用来解释其他未采样的退出。

Work／Auth 上传队列恢复为先等两级窗口均为正，再排帧；空 DATA 取得零额度，仍不扣减窗口。TTNet 保留原 writer，但发送独立空结束 DATA 前，按已开启的 `ChromiumFlowControl` 等待正窗口；等待期间不持写锁，SETTINGS ACK、RST_STREAM 与其他流仍能前进。非空 trailers 的 HEADERS 继续不受 DATA 流控限制。默认未开启 Chromium 流控的 req 行为保持原样。

新增离线帧回归覆盖 Work／TTNet 的零／负窗口、补窗完成及取消，共八个场景；三轮 race 通过。另检查连接零窗口、流零窗口、负窗口时空 DATA 的等待，以及正文耗尽窗口后非空 trailers 能结束。尚未补窗时取消仍返回原 context 错误、发出 CANCEL，并且没有发送结束 DATA。官方样本、旧网关失败与 panic、修正后样本及日志全部保留；本次修正不表示异步上传的完整事件调度已一致。

本轮最终 Go 普通全量、upstream／providers 完整包 race、固定 req 全量 race、相关回归三轮 race、build、vet、golangci-lint、gofmt 和 diff 均通过，没有排除旧测试。台湾时间 22:26 已保数据更新 dev，实际服务容器内 Work／Code 流式正文、结束事件及无错误检查通过；配置、认证文件、完整最新凭据和账号设置保持，两条已有绑定只续期，账号和上游会话 ID 未变。严格差异报告保留，续期另行逐字段核对。前端源码未改，未提交、未推送。

### TLS record 的大小与写缓冲（2026-11-17）

继续研究 socket 写阻塞前，独立采集 TLS 记录层，发现此前只校准 ClientHello、HTTP/2 帧而遗漏了 uTLS 默认的自适应记录大小。用逐条 TLS record 喂入的服务端 MemoryBIO 解密，按实际记录边界统计明文长度，不把 TCP recv 边界当成记录边界；全部使用可信回环、假凭据与固定测试正文，不连接真实上游。

| 官方运行时 | 采样路径与结果 |
|---|---|
| Work Chrome 156、授权 Electron 39.2.7、TTNet 原生模块 | TLS 1.2／1.3、128 KiB 固定正文的 HTTP/2 上传。首段大正文即按 16384 字节记录上限发送，短控制帧和末段保持实际长度，没有 Go 的逐记录递增 |
| WorkBuddy Electron 37.10.3／Node 22 | TLS 1.2／1.3、128 KiB 正文的 fetch／HTTP/1.1，同样从首段大正文采用 16384 上限 |
| Qoder 国内／国际原生 CLI | TLS 1.2／1.3、隔离配置与虚构身份，由随机固定种子提示生成超过 256 KiB 的真实推理请求体；经可信假端点完整接收，记录同样采用 16384 上限。这里只采请求，不把假响应算成真实推理成功 |

旧网关在所有五套 profile 的离线回归中均复现 Go 的增长策略：TLS 1.3 最初的大正文记录明文只有 1186、2372、3558 等字节，随后逐步增大；已经发过小的 HTTP/2 前言和请求头时，增长序号也继续累积。因此 ClientHello／JA3／JA4 匹配不能推出记录层匹配。

现仅在启用官方 TLS 指纹的 uTLS 配置中设置 `DynamicRecordSizingDisabled=true`，WorkBuddy 与新增平台均接线；关闭指纹时仍按标准库原路径处理。新增五 profile × TLS 1.2／1.3 共十个离线场景，验证首段 32 KiB、一个字节的短写、后续 64 KiB 的完整接收，以及加密记录的大小与尾部；旧实现先失败，再验证修正。不改 uTLS 模块缓存，不使用延时整形。三条 Trae profile 的实际 HTTP/2 复采中，网关也不再出现上述递增记录；WorkBuddy／Qoder 的 TLS 1.2／1.3 HTTP/1.1 网关复采同样确认从首段大正文使用 16 KiB 上限，正文完整接收且客户端正常退出。

这项修正只校准记录的切分上限，不声称所有完整记录序列一致：请求头、预检、窗口 ACK／补窗交错，以及待写队列都会影响每次 TLS Write 的输入。完整 socket 背压与异步事件调度仍待核对。官方 Chromium 156／142 的 `SSLClientSocketImpl` 使用容量为 17 KiB 的 `SocketBIOAdapter` 写环形缓冲；底层 TCP 返回 pending 时，BIO 只要还有容量仍可同步接收，直到缓冲满才通知 SSL 等待。因此不能将原始 TCP would-block 直接当作 HTTP/2 写循环让出的边界。对应两版本源码与采样材料保留在仓库外。

本轮根模块普通全量、upstream／providers 完整包 race、build、vet、golangci-lint、格式及 diff 检查通过，没有跳过旧测试；固定 req 源码与前端未改，未重复其已通过的检查。台湾时间 22:46 保数据更新 dev 并完成实际容器 Work／Code 流式复验，正文、结束事件与无错误检查均通过；配置、auth 文件、完整最新凭据与账号设置保持，两条已有绑定只正常续期，账号与上游会话 ID 未变。严格差异报告保留，并独立逐字段核对。未提交、未推送，完整异步调度与其他待办继续保留。

### Socket 背压期间的补窗与正文事件（2026-11-17）

此前把所有补窗推迟到 DATA 队列排空，遗漏了 Chromium 的另一条分支：`SpdySession::DoWriteLoop` 在 SSL 返回 `ERR_IO_PENDING` 时让出事件循环，期间可以处理补窗和异步正文完成。依据为上述 Chromium 156／142 的 `spdy_session.cc`、`ssl_client_socket_impl.cc`、`socket_bio_adapter.cc` 和 POSIX socket 实现。原始 TCP 短写／EAGAIN 与 TLS pending 不等价，不能用固定延时替代这条边界。

独立场景先预热同一连接，再同时上传 A（8 MiB）与 B（128 KiB）。服务端初始流窗口为 32768，两流耗尽后停止读取，给 A 加窗；确认 TCP 发送队列积压约 2.5 MiB 后再给 B 加窗，随后恢复读取。全部为可信回环、隔离网络和固定假正文，不使用真实账号。

| 两轮独立样本 | B 是否在 A 结束前恢复 | 完整性 |
|---|---|---|
| Work Chrome 156、Auth Electron 39.2.7 | 四份均是；官方 NetLog 另证实暂停读取期间处理了 B 的补窗 | 正文和结束响应完整 |
| 修正前网关 Work／Auth | 四份均否，B 被推迟到 A 结束后 | 正文完整，但调度与官方不同 |
| 修正后网关 Work／Auth | 四份均是 | 两份正文、结束事件与响应完整 |

Work／Auth 现于 uTLS 下增加 17 KiB 有界密文环形缓冲。Unix TCP 用 Go `RawConn.Write` 探测同步写入，保留 poller 的 deadline／close 检查；正数短写继续同步，实际 EAGAIN 后才启动排空 worker。只有缓冲已满、当前 TLS Write 仍有输入时，才通知 HTTP/2 进入 pending；本次输入全部接收后恢复同步状态。缓冲排空即退出 worker，异步错误关闭底层连接以唤醒读并保留写失败原因，关闭连接等待 worker 退出。

固定 req 的 Chromium 上传路径把补窗与异步正文完成合入同一 FIFO：同步写循环期间延后，TLS pending 时按到达顺序处理，避免将其他流一直压到大上传结束。零字节读取后的异步重试也经过该队列；取消后的正文事件忽略，流／连接补窗溢出保留各自错误。原固定正文的精确分片断言继续保留，没有用新样本放宽旧断言。

新增真实 socket 回归等待实际 TLS pending 和已处理的补窗帧，再恢复读取，不靠 sleep 推测该时机；另覆盖缓冲绕回、部分写、异步失败、回调内关闭，以及真实背压中的 deadline／close 唤醒。固定副本覆盖 pending 恢复顺序、读取完成与补窗先后导致的不同流控结果、零字节重试事件归属。

本轮接线仅识别 Unix `*net.TCPConn`，上述独立复采在 Linux x64 直连完成。非 Unix、SOCKS5 返回的包装连接、HTTPS 代理等路径的 BIO 接线及官方对照仍未完成；未通过反射解包或绕过代理协议。HEADERS／SETTINGS ACK／RST 等控制帧仍共用原写锁，完整控制帧调度、TTNet 异步调度、任意 JS 生产块映射及其他未采样网络路径也没有因此完成。ClientHello、记录大小和本场景通过，不能推出全部传输行为完全一致。

本轮根模块普通全量、upstream／providers 完整包 race、固定 req 全量 race、上传队列三轮 race、build、vet、golangci-lint、gofmt 与 diff 检查通过，未修改或跳过旧测试。Linux ARM64、Darwin ARM64、Windows AMD64 的 upstream 测试包交叉编译通过；这三项仅证明可编译，没有在对应设备执行测试，也没有将 Windows 原路径算作已接入 BIO。

最终源码重新编译诊断客户端后，Work／Auth 各再复采一份，均通过上述背压顺序及完整性检查。台湾时间 23:24 已保数据更新 dev，实际服务容器内 Work／Code 流式正文、结束事件与无错误检查通过；配置、auth 文件、完整最新凭据、设备字段及账号设置保持，两条已有绑定只续期，账号和上游会话 ID 不变。原始严格绑定差异报告保留，并独立逐字段确认续期。前端源码未改，复用已有产物；未提交、未推送，完整对齐仍未完成。

### PING／SETTINGS 不阻塞后续补窗（2026-11-17）

沿用上一节的双流背压条件，在暂停读取期间先发送一个 PING，或发送 `MAX_CONCURRENT_STREAMS=100` 的 SETTINGS，再给 B 补窗。Work Chrome 156 与 Auth Electron 39.2.7 各采两轮、两种控制帧，共八份官方样本；旧网关同样八份。官方 NetLog 均在服务端恢复读取前记录 B 的 WINDOW_UPDATE，恢复后 ACK 的下一份非空 DATA 均来自 B；旧网关八份均继续发送 A，部分样本甚至等到 A 结束才恢复 B。不能仅以“两流最终均完成”判作调度一致。

源码依据仍为相同版本的 `SpdySession`／`SpdyWriteQueue`：PING 与 SETTINGS ACK 以 `HIGHEST` 优先级排入连接写队列，接收处理不等待 socket 可写。固定 req 的 Work／Auth 路径现将这两类帧纳入已有上传事件 FIFO，并使用高于待写 DATA 的控制队列；同步写循环期间延后处理，TLS pending 时按接收顺序处理。写失败关闭连接、打断等待正文并保留原始错误；未开启 `ChromiumUpload` 的路径保持原调度。

SETTINGS 接收不再持有网络写锁：拥有自己的帧缓冲副本，避免下一次 Framer 读取覆盖；连接设置与 HPACK 编码／表更新均由连接锁保护。编码器按原有逻辑记录连续表大小变化，在下一个头块发出 table-size-update，不另存无限增长的更新列表，也不以空值丢弃中间缩表。两版本源码的默认 `kSpdySessionMaxQueuedCappedFrames=10000`，入队前比较 `>`；新队列按同一默认边界限制待处理的 PING／SETTINGS ACK，超限返回明确错误并由读取循环关闭连接。额外的 PING 压力采样确认本次两个官方运行时与网关均允许积压 10001 个，在第 10002 个触发上限关闭；官方 NetLog 明确记录 `Exceeded max queued capped frames`。但官方关闭前仍发出已排队的 10001 个 ACK，当时网关直接关闭、观察到 0 个 ACK；该差异于次日按下节修正。压力材料分别记录“上限边界通过”和“完整压力行为未通过”，不能混为通过；当时尚未采集 SETTINGS 与混合控制帧压力，次日补采见下节。

新增真实 socket 回归在发送控制帧后等待客户端已处理 B 的补窗，再恢复读取；修正前四个场景有三个在该等待处超时，原始失败记录保留，未将偶然通过的一项删掉。修正后该组通过，另保留旧的无控制帧背压场景。固定副本的回归验证控制帧在 B 的 DATA 前发送、读取不等写锁、控制写失败唤醒等待正文、SETTINGS 帧复用及 0／64／8192 连续表变化、同步延后／TLS pending 两种队列上限。

修正后网关独立复采两轮、两 profile、两种控制帧，共八份样本，ACK 后的第一份非空 DATA 全部来自 B，与上述官方顺序一致；正文、结束与响应完整。HEADERS、RST_STREAM、出站 WINDOW_UPDATE、GOAWAY 等尚未全部并入这套调度，TTNet、代理与其他平台的既有缺口也未因此完成；本节不宣称完整控制帧调度已对齐。

最终源码重编译后，四份 PING／SETTINGS 网关复采再次通过同一顺序检查。根模块普通全量、upstream／providers 完整包 race、固定 req 全量 race、相关背压／HPACK／多流及固定副本三轮 race、build、vet、golangci-lint、gofmt 和 diff 均通过；Linux ARM64、Darwin ARM64、Windows AMD64 测试包交叉编译通过，未在这些设备运行。相关组合首轮的 TTNet 多流取消五秒超时记录保留，未改测试或跳过，完整复跑通过；该次超时的具体原因未确认。压力边界和关闭排空的独立结果仍按上文分别记载。

台湾时间 23:52 已保数据更新 dev，实际容器内 Work／Code 流式正文、结束与无错误检查通过。配置、auth 文件、完整最新凭据、设备字段和账号设置保持；两条已有绑定仅续期，账号和上游会话 ID 不变。严格差异报告与逐字段续期核对均保留。前端未改，未提交、未推送；超限关闭前排空 ACK 在下一节继续验证，其余未完成项仍保留。

### 控制队列超限后的排空关闭（2026-11-18）

上一轮验证了允许积压 10001 个控制帧、第 10002 个触发关闭，却没有对齐关闭前排空 ACK：官方收到 10001 个 ACK，旧网关为 0。不能把上限匹配算作完整压力通过。

源码依据为 Chromium [156.0.8078.79](https://github.com/chromium/chromium/blob/156.0.8078.79/net/spdy/spdy_session.cc) 与 [142.0.7444.235](https://github.com/chromium/chromium/blob/142.0.7444.235/net/spdy/spdy_session.cc) 的 `DoDrainSession`、`DeleteStream`、`PumpWriteLoop`：会话停止复用并终止活动流，删除尚未开始的流帧，保留在途写和会话级控制队列；在途写完成且队列为空才销毁连接。队列超限使用 `ERR_CONNECTION_CLOSED`，不另发会超限的 GOAWAY。156 使用异步发起排空，142 在调用处直接进入排空；本轮只确认下述压力场景，不将两者其他事件循环行为视为完全相同。

固定 req 的 Work／Auth 现仅对控制队列超限启用排空状态。停止接收和新请求，关闭正文并通知原始超限错误，删除待写 DATA，保留正在写出的 DATA 及排队的 PING／SETTINGS ACK。流清理不额外发 RST_STREAM，删除最后一个流、空闲回收与 graceful shutdown 不抢先关 socket。排空后才完成未取消的失败请求；请求取消仍能及时返回，会话继续排空控制帧。网络写失败、写 deadline 或显式 Close 保持可中断，不将所有错误改成等待排空。

离线回归先在旧实现复现丢弃 ACK，再覆盖同步写／TLS pending 下 10001 个混合 ACK 的数量与顺序、保留在途 DATA、删除待写正文、禁止复用和空闲回收，以及写失败／Close／deadline 唤醒。真实 TLS socket 回归等待实际 pending 和正文关闭，验证原始错误、取消提前返回、取消后 ACK 仍完整及没有额外 RST／GOAWAY。新 socket 测试首轮未预热连接、初始窗口未固定而超时，修正测试后通过；失败日志保留，未删旧断言。

最终源码重新编译诊断客户端后，两轮独立压力复采包含 PING、SETTINGS 和交替混合三种控制帧、10001／10002 两个数量、官方 Work Chrome 156／Auth Electron 39.2.7 及修正后网关，共 48 份：10001 个均正常完成并收到全部 ACK；10002 个均触发上限，并在 EOF 关闭前按原顺序收到前 10001 个 ACK。随后四份普通 PING／SETTINGS 双流样本仍为 ACK 后先恢复 B，正文、结束和响应完整。HEADERS／RST／出站 WINDOW_UPDATE／GOAWAY 的统一调度、TTNet 完整异步调度、其他代理与系统路径仍未完成，完整对齐继续标为未完成。

根模块普通全量、固定 req 全量 race、相关三轮 race、upstream／providers 完整包 race、build、vet、golangci-lint、gofmt 和 diff 检查通过；Linux ARM64、Darwin ARM64、Windows AMD64 测试包交叉编译通过，仅代表可编译。upstream race 的既有 `TestListingModelsDevFailuresSilent/超大响应` 首轮及单独复跑曾两秒超时，完整包随后未跳过测试复跑通过；失败日志保留，超时原因未确认。

台湾时间 00:18 保数据更新 dev，00:19 实际容器内 Work／Code 流式文本、正常结束及无错误检查通过；配置、auth 文件、完整最新凭据、设备字段和账号设置保持。两条已有绑定只续期，账号与上游会话 ID 不变；严格差异报告和独立逐字段核对保留。前端源码未改，未提交、未推送，完整对齐仍未完成。

### 响应补窗与上传背压（2026-11-18）

在 Linux x64 回环连接上，先预热同一连接，再并发上传 A、B。服务端暂停读取，让 A 持续占用发送队列，五秒后对 B 发送带 255 字节尾部填充的空 DATA，随后为 B 补充发送窗口并发来 PING。Work Chrome 156 和 Auth Electron 39.2.7 的两轮 NetLog 均在服务端恢复读取前记录了两次出站补窗，并处理了 B 的入站补窗。

四份官方样本在已进入 TCP 队列的 DATA 之后，依次发送连接 WINDOW_UPDATE（3）、PING ACK、B 的 WINDOW_UPDATE（1）及 B 的 DATA。连接补窗的 3 字节包含预热响应消费的 2 字节和本次 padding 长度的 1 字节。旧网关把整段填充一次归还，四份样本分别发送连接窗口 258、流窗口 256，且流补窗之后继续发送 A 的 DATA。修正后四份网关样本均匹配官方的补窗数值和帧顺序，两个上传和响应均完整。

两版本 Chromium 的 `SpdySession::OnStreamPadding` 按解码回调归还连接和流窗口。`SendWindowUpdateFrame` 把连接补窗放在最高优先级，流补窗使用流优先级。Work／Auth 固定副本现分别处理长度字节和尾部 padding，并把连接补窗放进 ACK 队列、流补窗按 FIFO 排在同优先级的 DATA 中。正文消费与关闭产生的补窗也走同一队列，计入既有控制帧上限。连接关闭后仍可读取已接收的完整响应。

回归覆盖暂停写入期间的接收、两级补窗数值与帧序、正文读取触发上限后的排空，以及读完已缓冲的完整响应。RST_STREAM、HEADERS、GOAWAY 的完整调度、代理路径以及跨 TLS 读取边界的 padding 分段仍待验证。

全量 Go 测试、固定 req 和 upstream／providers 的 race 测试、构建、vet、golangci-lint、格式与 diff 检查通过。Linux ARM64、Darwin ARM64、Windows AMD64 通过交叉编译。台湾时间 08:07 保数据更新 dev，实际服务容器内 TraeWork／Trae Code 流式通过。重建前后的配置、auth 文件、凭据、账号设置及绑定一致，真实调用后两条绑定续期、一条已过期绑定按现有逻辑清理。

### 上传背压时取消流（2026-11-18）

Chromium 156／142 的 `SpdySession::ResetStreamIterator` 以原流优先级把 `RST_STREAM` 排入会话队列，再关闭流。`DeleteStream` 移除该流剩余的排队 DATA，已开始写的帧继续完成。会话队列中的取消帧在流删除后仍待发送，且计入控制帧上限。

Work／Auth 改为把本地 `RST_STREAM` 放入与 DATA 同优先级的写队列，socket 写阻塞期间也能清理流并释放并发槽位。最后一条流结束后，单次连接会等已排队的帧写完。回归先在旧代码复现槽位阻塞，再验证 PING ACK 优先于取消帧、取消帧排在下一段 DATA 前，以及单次连接发出取消帧后关闭。

三轮 Linux x64 回环 TLS 对照共十二份样本，覆盖官方 Work、官方 Auth 和网关的两套配置。服务端停止读取，A 上传造成约 2.5 MB TCP 积压，B 取消后服务端再发迟到补窗及 PING。恢复读取后，十二份样本均先收到 PING ACK，再收到 B 的 `RST_STREAM(CANCEL)`，随后 A 继续完成上传与响应。B 未在迟到补窗后发送 DATA。两套官方 NetLog 均记录在服务端恢复读取前已排入取消帧。

首次全量回归发现，第三条流的 HEADERS 可能抢在已排队的取消帧前到达，让服务端看到超出并发上限的请求。新请求现等已排队的取消帧写出后再发送 HEADERS，满并发与迟到补窗回归连续五轮 race 通过。HEADERS 的完整队列调度、GOAWAY 及其他 RST 错误码选择仍待进一步核实。

Go 全量、固定 req 和 upstream／providers 的 race、构建、静态检查及三平台交叉编译通过。根模块首轮有一次 TTNet 零连接窗口用例超时，该用例独立复跑八次通过，根模块全量复跑通过。台湾时间 08:21 保数据更新 dev，实际服务容器内 TraeWork 和 Trae Code 流式通过。配置、鉴权文件、凭据和设置保持，真实调用后两条会话绑定仅续期。

### 请求头排队与流编号分配（2026-11-18）

Chromium 156／142 的 `SpdyStream::SendRequestHeaders` 将头块生产器排入流队列。`SpdySession::DoWrite` 取到该头块后才开启流并分配编号，再调用 `ProduceHeadersFrame` 编码。取消等待写入的流时，`DeleteStream` 删除它的待发帧，已开始写出的帧继续完成。

Work／Auth 的初始 HEADERS 与非空请求尾部字段现在进入和 DATA 同优先级的队列。编号、初始窗口和 HPACK 编码在实际发送时确定，HEADERS 与后续 CONTINUATION 连续写出。取消排队请求释放并发槽位，且不消耗编号或推进 HPACK。先前为防止 HEADERS 越过 RST_STREAM 加的等待改由同优先级 FIFO 保证。收到 GOAWAY 时，待开启流走原有重试判定，未被 GOAWAY 拒绝的流仍可发送尾部字段。

离线回归覆盖写阻塞期间取消请求、后续编号无跳号、PING ACK 优先、跨帧头块解码，以及收到 GOAWAY 后保留已开启流的尾部字段。真实 socket 背压、多流取消和已有官方 HPACK 向量继续通过。完整 GOAWAY 发送调度仍待验证。

补充三轮 Linux x64 回环对照共十二份样本。上传造成约 2.5 MB TCP 积压后，两个新请求在服务端暂停读取期间先后发起，其中一个取消。官方 NetLog 确认两份请求头都已提交，恢复读取后只给存活请求分配流编号。官方 Work、官方 Auth 和两套网关均观察到 PING ACK 紧接存活请求 HEADERS，流编号为 1、3、5，取消请求没有 HEADERS 或 RST_STREAM，两个存活上传和响应完整。

Go 全量、固定 req 和 upstream／providers 的 race、构建、静态检查及三平台交叉编译通过。既有 `models.dev` 超大响应测试在完整 race 首轮超时，完整复跑通过，失败日志保留。最终源码的两套网关回环复采仍按 PING ACK、取消帧、存活流 DATA 的顺序发送。台湾时间 08:36 保数据更新 dev，实际容器内 TraeWork 和 Trae Code 流式通过。配置、鉴权文件、凭据及设置保持，真实调用后两条会话绑定仅续期。

### 协议错误时发送 GOAWAY（2026-11-18）

Chromium 156／142 的 `SpdySession::DoDrainSession` 在正常关闭时等活动流完成。遇到连接级协议错误时，停止接收新流，移除尚未开始的流帧，保留已开始写出的帧和已排队会话控制帧，再按最高优先级排入 GOAWAY。队列超限仍沿用不发送 GOAWAY 的排空路径。固定 req 的 Work／Auth 配置按同样顺序处理这些场景，并保留原连接错误。

三轮 Linux x64 回环对照共十二份样本。在上传背压期间，本地服务端依次发送 PING 和客户端已禁用的 PUSH_PROMISE。恢复读取后，官方 Work、官方 Auth 和两套网关均收到先前在途 DATA、PING ACK、`GOAWAY(last_stream_id=0, PROTOCOL_ERROR, "PUSH_PROMISE received")`，随后连接关闭。官方与网关在进入错误排空前已送入 TCP 的 DATA 字节数不同，其他协议错误的调试字段未逐项对照。离线回归另验证正常关闭等待活动流且不发 GOAWAY，以及队列超限保留已排队 ACK。

Go 全量、固定 req 和 upstream／providers 的 race、构建、静态检查及三平台交叉编译通过。台湾时间 08:50 保数据更新 dev，实际容器内 TraeWork 和 Trae Code 的流式请求均完整结束。重建前后配置、鉴权文件、凭据和设置一致，真实调用后两条活跃会话绑定仅续期。

### 代理隧道上的写入背压（2026-11-18）

Unix 上的 Work／Auth 现在可在 HTTP CONNECT、HTTPS CONNECT、SOCKS5 和 SOCKS5H 隧道内处理 TLS 写阻塞。在 SOCKS CONNECT 握手成功后使用原 TCP 连接，代理认证和目标解析仍由 `x/net/proxy` 负责。HTTPS CONNECT 在代理 TLS 下方缓冲已加密的数据，将底层写阻塞通知传给 HTTP/2。发给目标的连接仍通过外层 TLS，代理证书的校验规则不变。

Linux x64 回环回归同时开启两个上传流，让第一个流填满发送缓冲，再由假上游发送 PING 和第二流的 WINDOW_UPDATE。四种代理均通过同一隧道传输，收到 PING ACK 后第二流在大上传结束前恢复。旧实现的 SOCKS5 和 HTTPS CONNECT 未通知 TLS pending，测试等到超时。官方运行时通过代理的帧序、非 Unix 写阻塞和真实代理上游仍待验收。

HTTPS 代理的外层 TLS 可将一次内层写入拆成多次 socket 写入，补窗也可能在两次 pending 通知之间到达。测试等代理到达暂停点，观察补窗到达后写循环处于 pending，再恢复转发。HTTP／HTTPS CONNECT 压力回归连续通过八轮 race，四种代理与直连组合连续通过三轮 race。Go 全量、构建、vet 和 lint 通过。

台湾时间 09:06 保数据更新 dev，实际容器内 TraeWork 和 Trae Code 流式请求均正常结束。重建前后配置、鉴权文件、最新凭据、账号设置、会话绑定的摘要一致。实测后两条活跃绑定续期，两条实测前已过期的绑定按现有逻辑清理。这次真实流式测试未控制代理线路。

后续 SOCKS5／SOCKS5H 回环偶见大流先结束，服务端因顺序断言失败关闭连接，客户端随后收到 EOF。夹具原先只让 CONNECT 代理显式暂停，现四种代理共用上传转发暂停点，等补窗已交给 HTTP/2 处理、写循环处于 pending 后才恢复转发。ACK、单隧道、正文完整性和 B 在 A 结束前恢复的断言均保留。修正后的四种代理与直连组合通过十二轮普通测试和四轮 race，根模块普通全量通过。本轮只修夹具，未改运行时。

同轮回环也重现了一次 Work 固定正文在连接窗口归零后的存活流分片差异。归档中带 TCP_NODELAY 的两份官方 Work 同场景样本仍一致。Auth 流窗口归零的两份样本则有不同分片。全量复跑通过，已出现的 Work 差异原因仍未确认，严格分片断言保留。

后续定位了 Trae Code 固定正文、单流窗口归零场景中的偶发五秒超时。两次失败均在 B 的最后一个 DATA 帧耗尽连接窗口后出现，此时 C 的流窗口仍有余量，测试服务端却因为 B 已结束而跳过连接补窗。回环夹具现为已结束的 DATA 帧照常补连接窗口，只为仍在上传的流补单流窗口。修正后该子项连续 240 轮 race 通过，Go 全量测试通过。此次修正仅涉及测试夹具。Work 的固定分片差异仍待定位。

2026-11-18 11:31（台湾时间）补齐官方 Auth 代理回环样本。原夹具已选中代理，但测试域名在建立隧道前返回 `ERR_NAME_NOT_RESOLVED`，加入 resolver rules 或隔离的 hosts 映射仍未取得隧道。最终使用已通过直连采样的 `localhost`，以 `<-loopback>` 显式关闭 Electron 的回环地址代理绕过，保留官方 Trae 包名／版本，调用包内 Electron 39.2.7 的 `net.fetch`。Work 浏览器及网关仍使用 `capture.trae.cn`。此处验证控制帧和 DATA 恢复顺序，未比较不同测试域名下的请求头字节及所有 TLS 记录边界。

HTTP CONNECT、HTTPS CONNECT 和 SOCKS5 分别采两轮 PING 与 SETTINGS。官方 Work、官方 Auth、网关 Work、网关 Auth 共 48 份样本均只建立一条目标隧道，B 的 WINDOW_UPDATE 到达前已形成超过 1 MiB 的 TCP 积压，控制 ACK 后首个非空 DATA 来自 B，B 在 A 完成前恢复，两份正文及响应均完整。24 份官方 NetLog 还逐份确认在服务端恢复读取前收到 B 的补窗。原失败样本、原始 NetLog、结果及独立核对脚本归档在仓库外。非 Unix、真实代理到上游、TTNet 完整调度和其他异常路径继续待验收。

### HEADERS 写完即启动正文（2026-11-18）

Chromium 156／142 的 `SpdyStream::OnFrameWriteComplete(HEADERS)` 在同一写完成回调中调用 `SpdyHttpStream::OnHeadersSent()`，后者立即读取并发送正文。原网关只在连接写循环中通知 HEADERS 完成，再由请求 goroutine 开始正文；这使同步可用正文的首帧入队取决于另一 goroutine 的调度。

固定 req 副本现于成功写完 HEADERS 后，在同一写循环内启动正文。同步可用正文立即按原窗口预留规则排入 FIFO，未就绪正文仍由后台读取，不阻塞其他流。等待 `100-continue` 时保留原等待逻辑，收到继续信号或超时后才读取；请求头编码／写入失败、已取消或已停止上传的请求不启动读取。请求 goroutine 等待同一个上传结果，保留 trailers、长度超限、原始读取错误、dump 与取消处理，不再重新启动正文。

新增确定性回归在旧实现的固定正文、未知长度、trailers 和长度超限场景均复现启动过晚，修正后通过；另覆盖异步读取、`100-continue`、继续前取消和请求头错误。此前偶发分片差异的 Work／固定正文／连接零窗口子项连续 300 轮 race 通过，完整十二场景组合通过十轮 race；严格逐帧断言未放宽。

同条件下重新采集官方 Chrome Work、官方 Electron Auth 及对应网关，单流／连接窗口归零各两轮，共 16 份样本。全部只使用一条连接，取消后其余正文、正常结束与后续请求完整，八组官方／网关逐流 DATA 分片全部相等。采样夹具同步修正已结束 DATA 仍须补连接窗口的判断，避免把其他流缺窗误判为客户端挂起。该边界差异已确认并修正，但尚未证明它是历史偶发向量变化的唯一根因；有限轮次通过不代表所有并发调度已完全对齐。TTNet、任意生产块映射、非 Unix 和真实代理业务路径等仍按多平台待办验收。

根模块普通全量、upstream／providers 完整包 race、固定 req 全量 race、build、vet、lint 和改动文件格式检查通过，未排除旧测试。台湾时间 11:51 保数据更新 dev，实际容器内 Work／Code 流式正文与正常结束均通过。重建前后配置、auth 文件、完整凭据、账号设置和绑定摘要一致；实测后两条活跃绑定只续期，其他被清理绑定均已在对应请求开始前到期，其中一条在两次验收请求之间到期。严格差异报告保留，已用停机备份、请求日志、续期时间及清理源码独立核对，没有回填旧凭据。前端未改，未提交、未推送。

### TTNet 连接队列与 Cronet 异步读取（2026-11-18）

官方 Trae 原生运行日志确认 TTNet 为 `4.2.243.19-trae`，`core_ver=107.0.5273.2`。对应 Chromium 基线的 [SpdySession](https://chromium.googlesource.com/chromium/src/+/refs/tags/107.0.5273.2/net/spdy/spdy_session.cc) 使用连接写队列，SSL 默认写缓冲为 17 KiB，控制队列默认上限为 10000。此处用同版本公开源码解释基线，再用未修改的官方 TTNet 包独立采样核实；不把公开 Chromium 与 TTNet 私有改动视为完全相同。

旧网关的 TTNet 仍由请求 goroutine 分别争用写锁，也没有接入 TLS pending 通知。实际压力样本确认另一项差异：积压 10002 个 PING 时，官方排空前 10001 个 ACK 后关闭，旧网关却发送全部 10002 个 ACK 并完成上传。现给 TTNet 接入已有的连接级 FIFO、17 KiB 有界密文缓冲、异步读取／补窗事件和控制队列排空，直连及 Unix 代理均按同一接线处理。TTNet 仍保留原有的表大小处理及不可回放 POST 语义，没有开启 Work／Auth 的两次重发。

读取边界另有关键区别：[CronetUploadDataStream::ReadInternal](https://chromium.googlesource.com/chromium/src/+/refs/tags/107.0.5273.2/components/cronet/cronet_upload_data_stream.cc) 总是返回 `ERR_IO_PENDING`，[UploadDataSink](https://chromium.googlesource.com/chromium/src/+/refs/tags/107.0.5273.2/components/cronet/native/upload_data_sink.cc) 经 executor 调用 provider，再把读取结果投回网络线程。官方原生包的 `tokio_ttnet::client::executor::execute_fn` 也确认通过 Tokio 派发任务。因此 TTNet 即使消息已到达也经过异步完成事件，不借用 Work 的同步 `ReadAvailable` 快路径；provider 内部“残片加至多一条消息”的读取规则、三条消息背压及固定小正文的独立结束 DATA 保持。

最终版本两轮独立对照共 48 份样本：16 份多流取消样本均完成正文、结束与后续同连接请求；8 份 socket 背压样本在超过 1 MiB 的 TCP 积压下均为控制 ACK 后先恢复 B，且 B 在 A 结束前恢复；24 份 PING／SETTINGS／混合压力样本均匹配 10001／10002 边界、ACK 数量及顺序和关闭行为。官方采样进程遇到请求错误后等待服务端完成信号才退出，避免宿主提前退出截断待排空 ACK。

逐帧验收仍未完成：新增官方样本推翻了“TTNet 固定正文、连接零窗口只有一个稳定向量”的假设，最终八组官方／网关中仅一组逐流 DATA 长度完全相等。该假设对应本轮新加的 TTNet 单向量断言已撤回，原有 Work／Auth 严格向量未放宽；不能用此前单向量重复通过代替全部 TTNet 调度验收。剩余分片差异、任意生产块映射、其他控制帧与异常路径的官方独立对照、非 Unix 及真实代理业务仍继续核对。

2026-11-19（台湾时间）补采 36 份固定正文样本。拆分或合并取消后的三个补窗帧，连接窗口归零时六对样本的逐流 DATA 长度一致，跨流发送顺序仍有差异。流窗口归零时六对样本均复现官方先发恢复流、网关先发新流。受控样本将新流首帧和存活流补窗分开，先后两种顺序各六份，两侧均按受控顺序发出第一帧，后续跨流顺序仍有差异。18 份完整官方 NetLog 的 DATA 大小和流编号均与服务端帧记录一致。网关诊断记录中，流窗口归零时新流第一次正文读取完成，随后存活流才收到可用窗口。该差异仍需定位至异步事件的交付时序。

同日改用相同的 HTTP CONNECT 回环隧道，补采修复前后共 48 份固定正文，24 份官方 NetLog 与线上 DATA 逐帧相符。未控制首帧事件的 8 份官方样本中，旧流补窗先于新流读取完成的 7 份均先发送旧流，另 1 份先发送新流。网关的 8 份均先发送新流。两种出站顺序都已在官方运行时观测到，运行时何时交付这些事件仍待核对。

同一回环隧道另采 32 份。TTNet 八份 NetLog 均记录先收到包含补窗的 61 字节密文，解出三个共 39 字节的 WINDOW_UPDATE 帧，应用旧流补窗后才收到新流首次正文读取完成，首帧均来自旧流。网关八份普通样本和八份诊断样本均先发送新流。诊断样本中新流正文完成均先于 socket 读调用取得这批密文并投递 `readReady`，其中七份正文在服务端发送补窗前已读完。五份诊断样本在新流写出 DATA 前已从 socket 读到密文，此时新流的正文完成任务已入队。

八份受控网关样本在应用旧流补窗后才放行新流的正文完成通知，首帧均恢复为旧流。全部样本验证两条未取消流的正文、结束和同连接复用。受控条件只在诊断夹具使用。TTNet 原生正文桥何时投递读取完成仍待确认。

受控场景先让两个待发流的窗口同时归零，分别按 B→C 和 C→B 顺序下发两轮单流补窗。双方都按指定顺序发送每流 1024 字节。接下来 B 发出 12293 字节并耗尽连接窗口。服务端补连接窗口后，8 组旧网关先发 C 的 14326 字节，再发 B 剩余的 2034 字节，8 组配对的官方样本均为 B 后 C。Chromium 107 和 142 都在旧流因单流补窗恢复时保留其连接等待条目，待连接补窗时按原队列扫描。[Chromium 107 流控源码](https://raw.githubusercontent.com/chromium/chromium/107.0.5273.2/net/spdy/spdy_session.cc#L3464) [Chromium 142 流控源码](https://raw.githubusercontent.com/chromium/chromium/142.0.7444.265/net/spdy/spdy_session.cc#L3048)

网关现为每个流保留一个等待位置，单流补窗后再次阻塞时复用该位置。连接补窗按队列恢复，额度耗尽时尚未扫描的条目留在队首。流结束或取消时移除其条目。修复后 8 组配对样本的连接补窗帧序都与对应官方采样一致，均收齐剩余正文、完成单次取消及同连接探针。离线回归按确定的补窗事件验证出站帧序，任意 socket 调度的完整逐帧对齐继续待验。

离线回归扩到 TTNet 的实际 TLS pending、直连及 HTTP／HTTPS CONNECT、SOCKS5／SOCKS5H 背压、控制超限排空与取消，并覆盖普通／同步内存两种正文来源的结束帧及已到达消息仍须异步完成。旧 pending 回归超时、旧压力行为、同步读取的失败回归与中间样本均保留在仓库外，最终相关回归通过三轮 race。

根模块普通全量、providers 完整包 race、upstream 完整包 race 复跑及 build／vet／lint／格式检查通过，没有跳过测试。首轮完整 upstream race 失败，输出截断未能确定具体用例；原因仍未确认，不能将复跑通过解释为该偶发问题已解决。台湾时间 12:21 保数据更新 dev，实际容器 Work／Code 流式正文及正常结束通过，容器健康。配置、auth 文件、完整凭据、设备及账号设置保持，两条绑定只正常续期，三条已到期绑定由既有清理逻辑移除，停机备份、请求时间与源码已逐项核对；原严格差异报告保留，没有回填旧数据。前端未改，未提交、未推送。

### HTTP/2 并发请求的优先级依赖（2026-11-18）

官方 Work Chrome 156、授权 Electron 39.2.7 和 TTNet 在同优先级并发请求中，将新流依赖设为最近建立且仍存活的流，`exclusive=true`；Work／TTNet 权重为 220，Auth 为 110。之前只比较单请求头帧，固定副本把所有请求的父流都写成 `0`，遗漏了这个并发字段。对应三个 Chromium 基线的 `SpdySession::CreateHeaders` 均通过 `priority_dependency_state_.OnStreamCreation` 计算父流。

现于初始 HEADERS 出队并分配流编号后计算父流，排除已取消及两端都已结束的流。仅远端结束、上传尚未结束的流仍参与依赖；本端 END_STREAM 的完成状态由连接写循环持锁记录，覆盖 DATA 和 trailers，避免依赖请求 goroutine 迟到的完成标志。未开启 Chromium 上传队列的路径及显式指定的非零父流继续按原参数发送。

两轮独立回环共 48 份官方／网关样本，覆盖三条链路、流／连接零窗口和取消最早／最近请求。逐份检查父流、权重、exclusive、取消后回退、全部结束后回到根流及正文／同连接复用，全部匹配。新增离线回归在旧实现的第二条并发流稳定复现父流错误；另覆盖取消最近流、远端先结束、最后 DATA／trailers 完成后的回退。三平台原有多流测试增加仍在上传时的父流断言；Auth 收完整份未知正文时，其他流可能已经结束，按实际存活边界核对。原 Work／Auth 的严格 DATA 分片断言保持。

TTNet 事件采样现通过临时包裹层，在首条请求前调用官方 `Cronet_Engine_StartNetLogToFile`，退出前调用 `StopNetLog`，请求参数原样传递。原生网络库二进制保持官方版本；八份新 TTNet NetLog 的父流参数与原始帧交叉核对。初次 GDB 采样未正常运行、Auth 夹具旧 driver 引用及窗口初值遗漏的失败记录均保留，已修正采样或回归夹具。这些夹具失败不归因于平台运行时。

该项解决 HEADERS 依赖字段。其余 DATA 分片差异、任意生产块映射、其他异常与真实代理业务等待办仍继续核对，不能据此宣称完全对齐。

固定 req 全量 race、根模块普通全量、完整 upstream／providers race、build、vet、lint 与格式检查通过；完整日志保留，没有跳过测试。新增优先级回归中的 Auth 未知正文场景曾错误假定 B 一直存活，已按先收完整正文的真实行为修正断言边界，原 DATA 严格断言保持。台湾时间 12:59 保数据更新 dev，实际容器内 Work／Code 流式正文与正常结束均通过，容器健康。配置、auth 文件、完整凭据、设备及账号设置保持；两条活跃绑定只正常续期，一条已过期绑定依既有逻辑清理，严格差异报告及停机备份／请求时间的独立核对保留，没有回填旧数据。前端未改，未提交、未推送。

### 同批补窗与 PING 的读写顺序（2026-11-18）

新增回环夹具在连接发送窗口归零、取消一个上传流后，将已取消流的迟到 `WINDOW_UPDATE`、连接补窗、PING 和存活流补窗合在一次 TLS 写入中发送。三轮官方 TTNet／Work Chrome 156／Electron Auth 与当前网关共 18 份样本均完整完成正文、取消及后续同连接请求，但九份官方样本全部先发送 PING ACK 再恢复非空 DATA，九份当前网关样本全部反序。由服务端原始帧记录确认物理顺序；不能仅以 NetLog 的 DATA 创建事件当作写出顺序。

三版 Chromium 基线的 `SpdySession::DoReadLoop` 连续处理同步可读输入，实际读取阻塞、累计超过 32 KiB 或超过 20 毫秒时才让出；每次读取缓冲为 8 KiB。阈值亦已独立核对 [Chromium 142 头文件](https://chromium.googlesource.com/chromium/src/+/refs/tags/142.0.7444.235/net/spdy/spdy_session.h)和 [Chromium 156 头文件](https://chromium.googlesource.com/chromium/src/+/refs/tags/156.0.8078.79/net/spdy/spdy_session.h)。旧固定 req 副本逐帧处理时即可启动写 goroutine，缺少这个读批次与写任务的交接边界。此处有官方样本和公开基线依据，但完整任务调度的等价性仍须独立验证。

本轮试验过按实际 socket 读取阻塞划分批次、延后写任务及外部异步正文完成的原型。两轮六份网关新样本均恢复 ACK 在 DATA 前，另一个原采样条件下 Work／Auth 的逐流分片也与各自官方新样本相等；但原有严格多流回归的 Auth 固定正文在单流／连接零窗口两项失败。因此原型没有通过验收，已从产品源码撤回，原补丁、失败断言与采样材料保存在仓库外；没有放宽原有 Work／Auth 的严格 DATA 断言。后续仍须核对 socket／TLS 读取完成、正文完成和写任务之间的 FIFO 关系，不能将局部阻塞标志视为完整事件调度。

进一步用 Go `tls.Server` 与 `golang.org/x/net/http2` 构建独立原始帧采集器，复用同一补窗、取消与连接复用流程。三轮 Work／Auth 共六份官方固定正文、单流零窗口样本均完整，其中五份与原来的唯一 DATA 向量不同，包含原型回归中出现的 `70/16305` 等切分；原始帧和官方 NetLog 交叉核对一致。因此“真实 socket 时序下 Work／Auth 只有一个稳定逐流向量”的假设同样不成立。此前六轮匹配的结论只适用于那些样本，不再用该向量判断所有读批次，也没有增加允许向量白名单。

固定 req 现按每次 8 KiB 明文读取及真正的 Unix socket pending 交接读写：同步读批次期间不启动写任务，外部异步完成延后处理；已经 pending 的写入恢复后，也先等读批次让出，再执行 HEADERS／DATA 写完成回调及后续正文读取。读取阻塞、字节预算、时间预算、EOF 和读循环错误退出都释放批次。低层先释放 `RawConn.Read` 的 fd 引用，再通知 pending，回调中关闭连接不会等待自身引用。直连、HTTP／HTTPS CONNECT 和 SOCKS 隧道沿已有 BIO 接线；非 Unix 仍保留原路径，不能据此称其已对齐。

确定性回归明确控制同批／分批补窗与 PING，逐字节锁定 ACK、两流 DATA、正文和结束标志；另覆盖读取预算及写完成不越过读批次。真实 TLS 回归在旧实现的三个 profile 上均复现同批 ACK 过晚，修正后通过，同时验证实际 pending 时能让出并完成后续上传。多流 socket 回归继续严格检查窗口、分片上限、完整正文、结束、取消、迟到补窗与连接复用；依赖外部读批次的唯一向量断言已由上述受控回归替代。原 TTNet 生产块读取边界断言保持。

这项修正只验收有证据的读批次交接。异步正文完成与其他已投递任务的完整 FIFO、跨读取缓冲的 DATA 交付、任意生产块映射、其他异常与非 Unix 等仍须继续核对，不能宣称完整传输完全一致。

最终源码独立复采 36 份：三套官方运行时与对应网关，在单流／连接零窗口下分别覆盖固定正文取消最早流、固定正文取消最近流、未知正文取消最早流。全部通过受补窗约束的存活流 ACK 顺序、完整正文、取消、结束与单连接复用检查；单流零窗口下已在途的其他流 DATA 不算该批补窗触发的上传。未按不可控跨流逐帧完全相等解释这些结果。

固定 req 全量 race、根模块普通全量、完整 upstream／providers race、相关三轮 race、build、vet、lint 和格式检查通过；未跳过测试。Linux ARM64、Darwin ARM64、Windows AMD64 测试包仅交叉编译通过，不代表实机验收。台湾时间 13:52 保数据更新 dev，实际容器 Work／Code 流式正文及正常结束通过，容器健康。配置、auth 文件、完整最新凭据、设备及账号设置保持；两条绑定仅续期，账号及会话保持，无绑定新增或删除。停机备份与重建前摘要一致，原严格差异报告和续期独立核对均保留，没有回填旧数据。前端未改，未提交、未推送。

### DATA 尚未收齐时交付首块（2026-11-18）

官方 Chromium 107／142／156 的 `SpdySession::OnStreamFrameData` 按 decoder 已收到的片段交付正文，单次不超过 8 KiB，并按该片段降低连接接收窗口；`OnStreamPadding` 按实际到达的 padding 立即扣除再归还，`OnStreamEnd` 在当前帧结束后才通知正文 EOF。网关旧 `Framer` 先以 `io.ReadFull` 收完整个 payload，导致应用交付、接收流控和取消时机落后。

独立回环在一帧中声明 12000 字节正文，先发头部和 6000 字节，再等待应用确认首块已消费，之后才发余下 payload；分别采普通 DATA 与带 padding 的 DATA。首轮三套官方／网关共十二份样本均最终完整，但六份官方都在尾部发送前交付首块，六份旧网关都未交付，直到服务端的四秒取样期限届满才恢复。此处期限只是确定性采样闸门，不是生产请求的延时策略。

固定 req 现为内部 Chromium 接收路径保留物理 DATA 的剩余状态，按已收到的正文片段继续返回，每段按实际消费量记账。padding 长度字节、正文和尾部 padding 分开处理，最后一个片段才携带 `END_STREAM`；帧大小限制、流零、头块连续性和 padding 长度校验仍由解析层执行。普通无连接上下文的 `Framer` 保持每次返回完整一帧。声明长度超过当前窗口时，不预扣尚未到达的 payload；实际收到超窗片段时拒绝，未越窗的已到达正文仍可交付。

回归覆盖普通／零／少量／最大 padding、空正文、截断、非法流与 padding、后续物理帧边界和逐片超窗；真实 TLS 覆盖三个 profile、普通／padded、正常完成／半帧取消。取消期间不需要等余下 payload，迟到字节正确丢弃，随后请求复用同一连接。旧实现的三个 profile 均由新回归复现首块未交付，修正后相关三轮 race 通过。测试服务同时保留取消前可能已经到达的下一请求 HEADERS，避免把 context 允许提前返回的调度误判为连接失效。

完整异步任务 FIFO、其他异常路径、真实代理业务、非 Unix 及原生 ARM64 仍按多平台待办继续验证；该修正不代表所有输入分段和时序均已完成验收。

最终源码两轮官方／网关共 24 份普通与 padded 样本，全部在尾部发送前交付首块，并完成正文及结束检查。固定 req 全量 race、根模块普通全量、完整 upstream／providers race、相关三轮 race、build、vet、lint 和格式检查通过，未跳过旧测试。三平台测试包仅交叉编译通过，不代表实机验证。

台湾时间 14:21 保数据更新 dev，实际容器内 Work／Code 流式通过，容器健康。初始严格核对发现 Qoder 凭据对象变化，报告原样保留；与停机备份、官方 UMID 持久缓存及源码独立比对后，确认国内／国际只更新了到期的 `machine_token`、`machine_type`、`machine_code`。停机备份与重建前完整凭据摘要一致，新字段与新缓存匹配，旧缓存已到刷新期，新缓存有效期符合一小时 ± 五分钟规则；access token、refresh token、固定设备身份和其余凭据字段保持。配置、auth 文件与账号设置保持，两条会话绑定只续期，账号及会话未改变，无绑定新增或删除。没有回填旧数据，前端未改，未提交、未推送。

### 已投递正文完成与写任务的先后（2026-11-18）

Chromium 的 `SpdySession::MaybePostWriteLoop` 在从 idle 启动写循环时向同一网络任务序列 `PostTask`。Cronet 的上传读取完成也投递回网络序列；因此读批次中触发的写任务不能越过此前已经投递的正文完成任务。旧网关虽延后了正文事件，却会在当前可写帧全部排空后才处理它，仍与官方不同。

新增官方 TTNet 受控夹具：A 只生产首个字节，B 持续上传至连接窗口归零；服务端随后在同一 TLS 写入中补连接窗口 1 与 32768。临时采样 shim 在官方网络线程已进入 socket 读取、尚未处理这批输入时暂停，通知 A 生产第二字节，确认 `Cronet_UploadDataSink_OnReadSucceeded` 已调用原官方函数、完成事件已投递后再恢复网络线程。请求正文、网络库和回调参数保持，暂停仅用于明确任务顺序；不把这种采样控制作为产品策略。

三轮官方样本均先发送 B 的 1 字节，再发送 A 的 1 字节，然后发送 B 当前缓冲的 16342 字节。旧网关在相同暂停与正文完成标记下，先发送 B 的 1／16342，再发 A；新增离线回归也在旧实现复现同一差异。修正后三轮网关与官方的该场景完整 DATA 序列均相同，正文、结束和后续同连接请求完整。官方 NetLog 与服务端原始帧记录均保留，物理发送顺序以服务端为准。

固定 req 现将正文读取完成、外部开始读取、初始 HEADERS／控制事件、读恢复、首次写任务及 pending 后的逻辑写完成纳入同一连接任务 FIFO。同步读批次和同步写循环各自执行至让出时，才处理下一个已投递任务；读批次内已解码的补窗与控制帧仍在当前任务中处理。同步可用的 Work／Auth 正文继续立即读取，TTNet 始终经过异步完成；没有加入 sleep 或允许向量白名单。

TLS BIO 的一次写可多次 pending。网络任务所有权在第一次 pending 时让出，整个 TLS Flush 返回并释放 TLS 内部写锁后，才投递逻辑写完成；不能在低层 BIO 回调中阻塞等待任务，否则 TLS 读取自身需要写入时可能互相等待。HEADERS、DATA、ACK 的后续处理遵守相同交接。最后一条流释放、主动关闭、读失败与协议排空都等待或唤醒相应任务，避免 RST_STREAM 仍在任务队列时连接提前关闭。

回归同时锁定正文完成早于／晚于写任务时的两种精确帧序，覆盖关闭唤醒读写恢复、待发 HEADERS 释放与原始错误、已取消正文事件不占窗口。既有 pending、零字节重试和流控溢出测试改为明确建立当前网络任务与让出点，保留实际帧序、错误、窗口归还及排空数量检查。这项统一只覆盖当前已接线事件；其他任务来源、异常与真实业务网络路径仍须独立核对，不能直接宣称完整传输完全一致。

最终源码三轮 TTNet 受控对照共六份样本逐 DATA 序列相等，另复采三套运行时的读批次 36 份、输入 DATA 分段 24 份与控制压力 36 份，均通过各自的 ACK、窗口、正文、取消、复用和 10001／10002 排空关闭检查。后面三组只表示对应属性通过，不外推逐帧完全相等。固定 req 全量 race、根模块普通全量、完整 upstream／providers race、HTTP/2 三轮 race、build、vet、lint 和格式检查通过，未跳过测试；三平台测试包仅交叉编译。

台湾时间 15:19 保数据更新 dev，实际容器 Work／Code 流式正文与正常结束通过，容器健康。停机备份与重建前完整凭据摘要一致；重建后 Qoder 国内仅到期更新三个临时 UMID 字段，已独立核对旧／新缓存、刷新时间、有效期和其他字段保持。Qoder 国际本轮未变，所有授权令牌、固定设备身份、配置、auth 文件及账号设置保持；两条绑定只续期，账号与会话保持，无绑定增删。原严格差异与独立核对保留，未回填旧数据，前端未改，未提交、未推送。

### 响应小块合并与读取完成回调（2026-11-18）

Chromium [107](https://chromium.googlesource.com/chromium/src/+/refs/tags/107.0.5273.2/net/spdy/spdy_http_stream.cc)／[142](https://chromium.googlesource.com/chromium/src/+/refs/tags/142.0.7444.235/net/spdy/spdy_http_stream.cc)／[156](https://chromium.googlesource.com/chromium/src/+/refs/tags/156.0.8078.79/net/spdy/spdy_http_stream.cc) 的 `SpdyHttpStream::ReadResponseBody` 在已有缓冲时立即读取；调用时没有数据则挂起。后续 `MaybeScheduleBufferedReadCallback` 在缓冲足够填满调用方读取长度时投递零延迟回调，不足时重设一毫秒定时回调；后续片段再次重设。正常 `OnClose` 立即交付剩余正文，错误或取消结束挂起读取，不让旧回调再次访问调用方缓冲。该毫秒值来自三个官方基线源码，不是为匹配单个样本加入的等待策略。

独立回环先提供一段预热正文，让三个运行时完成 fetch 与 reader 初始化，再在一条 TLS 记录中发送 600 个单字节 DATA，等待客户端首读报告后才发送结束帧。两轮共十二份官方／旧网关样本，六份官方首读均为 600，六份旧网关仅为 194～265，其余正文虽最终完整，读取合并与消费回调仍不同。夹具最初仅用 HEADERS 或一个预热字节时，Auth 的 read-ready 控制尚未触发；改为 8 KiB 预热后采样正常，初始失败保留，不将其解释为上游推理故障。

固定 req 的 Chromium 响应读取现经连接任务 FIFO 开始，已有缓冲保持同步消费语义；没有数据时记录一个挂起读取。DATA 到达后按上述规则安排读取完成任务，流结束立即完成；定时任务使用版本标记，后续片段、取消、正常结束、读失败和连接关闭都使旧任务失效并停止计时。正文消费及 WINDOW_UPDATE 在同一网络任务内记账，避免另一个 goroutine 在帧处理过程中提前归还窗口。

新增确定性回归覆盖同读批次合并 600 个 DATA、缓冲填满时的零延迟异步回调、已有缓冲立即读取、旧回调失效、结束／取消清理、连接关闭唤醒，以及 Content-Length 超长与提前 EOF。真实 TLS 在三套 profile 中检查明确的一条记录输入、首读、完整正文与结束；新回归在旧实现的三套 profile 均复现首读过早，修正后通过三轮 race。定时器只服务当前挂起读取，没有常驻轮询或固定全局延时。

这项修正不证明任意应用读取长度、跨批次事件、异常及真实代理业务已全部一致，完整待办继续保留。

最终三轮共 18 份官方／网关小块样本，首读均为 600 字节，正文、结束及结束帧前交付均通过；另复采读批次 36 份、DATA 首块 24 份和控制压力 36 份，均通过原有属性检查。固定 req 全量 race、根模块普通全量、完整 upstream／providers race、相关三轮 race、build、vet、lint 与格式检查通过；三平台测试包仅交叉编译。

完整 upstream race 首两次均定位到旧 `TestListingModelsDevFailuresSilent/超大响应` 的两秒期限。独立 CPU profile 中约 78% 的样本位于 race 内存访问检查，测试夹具 `repeatReader` 逐字节生成 32 MiB 的正文。现仅将相同内容的生成改为成倍复制，保留原大小、期限及断言；三次独立 profile 检查总耗时约 5.45 秒降至 4.21 秒，随后普通全量和完整包 race 通过。原失败与 profile 保留；这次已定位的夹具开销优化不证明之前输出截断等历史偶发失败的唯一原因，也未修改生产目录读取逻辑。

台湾时间 15:51 保数据更新 dev，实际容器 Work／Code 流式正文与正常结束通过，容器健康。重建前后及实测后的配置、auth 文件、完整最新凭据、固定设备身份及账号设置保持；两条已有绑定仅到期时间续期，账号与会话保持，无新增或删除。停机备份、严格差异报告及独立续期核对保留，没有回填旧数据；前端未改，未提交、未推送。

### 已关闭流的正文消费与补窗（2026-11-18）

Chromium [142](https://chromium.googlesource.com/chromium/src/+/refs/tags/142.0.7444.235/net/spdy/spdy_stream.cc)／[156](https://chromium.googlesource.com/chromium/src/+/refs/tags/156.0.8078.79/net/spdy/spdy_stream.cc) 的 `SpdyStream::IncreaseRecvWindowSize` 先检查流仍在 session 的活动表内。正文缓冲的消费回调可能晚于流关闭，流级回调此时不再增加窗口；连接级缓冲消费仍归还连接窗口。远端 `END_STREAM` 只关闭一个方向，本端仍上传时流仍可能活动，不能仅据远端结束就停止所有流补窗。

新增回环先消费 8 KiB 预热正文，等待超过官方五秒补窗间隔后，再发送最后一个正文字节。三套官方运行时与旧网关共六份样本中，官方在双向结束后仅发送连接级 `WINDOW_UPDATE(8193)`，旧网关另发送同量的流级补窗。另一组三份官方样本将该正文字节保持为未结束 DATA，等待消费报告后才发送结束帧，三套均同时发送连接及流补窗。差异因此由活动状态触发，连接消费总量与阈值并未改变；夹具等待仅用于跨过已确认的五秒阈值。

网关现复用已有的本端结束标志、远端结束信号、活动流表与取消状态，统一判断流是否活动。双向结束、移除或取消后不再给流窗口记账；本端仍在上传的远端半关闭流继续参与。相同状态也用于并发 HEADERS 父流选择，避免两处对活动流的定义分歧。padding 归还仍按当前实际活动状态分别处理连接与流，已排队的合法补窗不会因稍后关闭而任意删除。

确定性回归在旧实现复现双向结束和流移除后的多余补窗，修正后逐帧检查连接与流的 ID、增量、数量，并保留活动响应及远端半关闭两种补窗。原响应读取、定时回调、上传取消、优先级与控制排空回归继续执行；真实业务代理、其他异常和全部半关闭业务时序仍待独立验收。

最终源码两轮、两种结束时机、三套官方及对应网关共 24 份样本，补窗流编号、数量、数值和完整响应均匹配；首轮六份官方 NetLog 的发送事件与服务端原始帧亦一致。固定 req 全量 race、根模块普通全量、完整 upstream／providers race、HTTP/2 三轮 race、build、vet、lint 与格式检查通过，未跳过旧测试；Linux ARM64、Darwin ARM64、Windows AMD64 测试包仅交叉编译。

台湾时间 16:06 保数据更新 dev，实际容器内 Work／Code 流式正文及正常结束通过，容器健康。配置、auth 文件、完整最新凭据、固定设备身份和账号设置在重建与实测后均保持；两条绑定只续期，账号与会话未变，无绑定增删。停机备份、原严格差异及独立核对保留，没有回填旧数据。前端未改，未提交、未推送。

### 接收超窗的流与连接错误范围（2026-11-18）

三套 Chromium 基线的 `SpdySession::OnStreamFrameData` 先扣连接窗口，再由 `SpdyStream::DecreaseRecvWindowSize` 检查流窗口。超出连接窗口时关闭连接；仅超出流窗口时发送该流的 `RST_STREAM(FLOW_CONTROL_ERROR)`，违规片段释放时归还连接额度，其余流继续使用该连接。旧网关用一次 `takeInflows` 合并检查，误把单流超窗升级成连接级 GOAWAY，连带中止其他请求。

固定 req 副本现分别检查两级窗口。单流失败标记为接收中止，通过已有清理路径发送错误码 3；违规片段不交付正文，只归还连接额度，迟到 DATA 不复活该流。真正的连接超窗仍保留连接级错误。新增离线回归在旧实现的 Work、Auth、TTNet 三条路径均复现另一流被 GOAWAY 中断，修正后检查准确的 RST 流编号与错误码、存活流完整响应及后续请求只拨号一次；固定副本另覆盖 padding、迟到片段和活动／已移除流的连接超窗。

官方独立采样保留原来的 6 MiB 声明窗口，并同时打开一条存活流。首个 Work 网页夹具仅停止调用 Reader，浏览器仍预读并补窗，未真正触发超窗；该次超时及后续夹具错误保留为未完成样本。最终 Work 用同步控制请求暂停页面任务，服务端等暂停点到达后才发送正文；Auth／TTNet 使用异步控制点，存活请求先发起、错误触发后再消费，不等待该请求的正文交付。没有修改官方网络库、窗口或添加任意延时。

最终两轮、三套官方与对应网关共 12 份原始帧样本，均仅重置违规流，不发送 GOAWAY；存活流与后续请求完整，并始终只建立一条连接。六份官方 NetLog 与服务端原始 RST 帧独立核对错误码及流编号。回归用较小窗口确定触发条件，不能把该设置当成官方参数。其他协议错误的调试字段、异常时序、缓存压力、非 Unix 及真实代理业务仍按多平台待办继续验收。

根模块普通全量、完整 upstream／providers race、固定 req 全量 race、HTTP/2 三轮 race、构建、vet、lint 与格式检查均通过，未跳过旧测试。台湾时间 16:31 保数据更新 dev，实际容器 Work／Code 流式正文和正常结束均通过，容器健康。严格摘要发现 Qoder 国内三个临时 UMID 字段更新；停机备份确认旧缓存已于 16:22 到期，新值与持久缓存一致，属于既有按需刷新。授权令牌、固定设备身份、配置、auth 文件及账号设置保留，两条绑定仅续期，无增删。原严格差异与独立核对均保留，没有回填旧凭据。前端未改，未提交、未推送。

### 异常帧与发送窗口的关闭行为（2026-11-18）

继续调用未修改的官方 Chrome 156、Electron 39.2.7／Chromium 142、TTNet 4.2.243.19／Chromium 107，对照 28 类帧与设置。每次先取得响应头和 8 KiB 预热正文，再注入测试帧，避免把自动重发误当作原连接继续工作。设置被忽略时，后续 PING 必须往返且正文正常结束；连接错误必须返回相同 GOAWAY、关闭且不交付非法正文。脱敏输入与三套官方一致的预期保存在 `clienthello/trae-http2-protocol-errors.json`，测试只连接本地假服务器。

实现另对照 Chromium 156 固定 DEPS 指向的 [Quiche decoder](https://quiche.googlesource.com/quiche/+/a1628192cf32ba40d21e0dd80f485d68c24ff87a/quiche/http2/core/http2_frame_decoder_adapter.cc)，以及三个 Chromium 基线的 `SpdySession`：

- 接收帧大小按本端实际声明的 `SETTINGS_MAX_FRAME_SIZE` 限制，未声明时为 16384。原库默认可接受接近 16 MiB，旧网关会继续接收 16385 字节 DATA；现在仅收到非法帧头就拒绝，不等待其正文。该声明不再反向改变发送帧大小。
- 帧的流编号、固定长度、padding、DATA flags 和 CONTINUATION 连续性使用官方 decoder 的错误分类与调试文本。CONTINUATION 的流编号为零时，先报告无效流编号，再检查其流间顺序；不把该错误覆盖为帧顺序错误。短 GOAWAY 在官方属于 `PROTOCOL_ERROR / INVALID_CONTROL_FRAME`，不能照原库发 `FRAME_SIZE_ERROR`。大 DATA 与大控制帧分别报告 `OVERSIZED_PAYLOAD` 和 `CONTROL_PAYLOAD_TOO_LARGE`。
- 连接发送窗口加法溢出时，官方发 `PROTOCOL_ERROR`，旧网关发 `FLOW_CONTROL_ERROR`；现同时校准错误码和包含当前额度的调试文本。零增量留给 session 区分连接与活动流，已关闭流继续忽略；未请求的 PING ACK 按官方关闭连接。
- 官方忽略超范围的 `SETTINGS_INITIAL_WINDOW_SIZE`，原网关会断连；现在保留原合法窗口并继续响应。`SETTINGS_MAX_FRAME_SIZE` 按该客户端基线不改变发送分帧。合法初始窗口变化导致活动流额度溢出时，现发连接级 `FLOW_CONTROL_ERROR`，按官方 `std::map` 的流编号顺序报告首个失败流；旧网关漏查该溢出。

以上行为仅接入现有 `ChromiumUpload` 路径。普通 req 路径保留原错误和窗口策略，没有增加使用者配置项。协议错误向上保留具体调试原因，并可继续用 `errors.As` 取得连接错误码。新增矩阵回归核对准确的 GOAWAY 流编号、错误码、调试字段、关闭、非法正文隔离，以及应忽略设置后的完整正文；另有仅帧头、默认路径不变和多流窗口溢出顺序回归。两轮三套官方与对应网关共 336 份样本，并以最终源码重编译采集程序复查 84 份网关样本。原有单流超窗隔离、取消、流控及控制排空回归继续保留。其他未采样的 HPACK 错误、异常交错、扩展帧、缓存压力、非 Unix 与真实业务代理仍待验收。

168 份官方 NetLog 的 session 关闭原因与服务端原始 GOAWAY 字段交叉核对。首批 TTNet 未开启日志的样本及五份被进程终止截断的 Work 日志均保留，未把缺失日志判作通过；TTNet 启用官方日志接口后重新采集，Work 改为经 `Browser.close` 正常退出后补采。补齐后的日志均为完整 JSON，异常码／描述、忽略设置后的 PING 与完整正文均匹配。

根模块普通全量、完整 upstream／providers race、固定 req 全量 race、HTTP/2 三轮 race、构建、vet、lint 和格式检查通过，未跳过原回归。台湾时间 17:27 保数据更新最终 dev，实际容器 Work／Code 流式正文及正常结束通过，容器健康。重建与实测后的完整最新凭据、固定设备身份、配置、auth 文件及账号设置一致，本轮没有 UMID 刷新；两条绑定仅续期，无增删，停机备份、原严格报告及独立字段核对均保留。前端未改，未提交、未推送。

### HPACK 解码错误分类与动态表状态（2026-11-18）

三套官方 Trae 运行时的隔离回环新增响应 HPACK 对照。先消费 8 KiB 预热响应，再发送构造的 HEADERS，确保错误发生前请求已被接受，避免重试掩盖原连接的关闭行为。样本区分无效字段／名称索引、索引／名称长度／值长度整数溢出、名称／值的 Huffman padding 与 EOS、截断头块、超出声明限制的字符串长度，以及动态表更新的范围、次数和位置。脱敏输入和一致回复保存于 `clienthello/trae-http2-hpack-errors.json`；其中合法的块首两次表更新使用完整结束的空 trailers，检查不会误断连。

实现复用固定 req 中已有的 x/net HPACK 副本，保留原版权、动态表和 Huffman 实现。`NewChromiumDecoder` 仅用于 `ChromiumUpload` 的入站头块，普通 req 路径继续使用原 x/net decoder。分类依据为官方原始 GOAWAY，以及固定版本 Quiche 的 [HpackDecoderState](https://quiche.googlesource.com/quiche/+/a1628192cf32ba40d21e0dd80f485d68c24ff87a/quiche/http2/hpack/decoder/hpack_decoder_state.cc)／[HpackWholeEntryBuffer](https://quiche.googlesource.com/quiche/+/a1628192cf32ba40d21e0dd80f485d68c24ff87a/quiche/http2/hpack/decoder/hpack_whole_entry_buffer.cc)，不是对 Go 错误字符串作猜测映射：

- 初始表容量为 4096，本端允许的最大容量为 65536；对端须显式更新才能保留更大的条目。合法更新后跨头块引用保留，不能只根据本端声明提前扩容。
- 每块在普通字段之前最多允许两次动态表更新。静态字段后更新也必须拒绝，不能以当前动态表为空为由放行；每块结束后次数重置。
- 名称索引在值解码完成后查找：同一字段还含坏 Huffman、值长度整数错误或截断时，先报告值错误。名称本身的 Huffman 已失败时则不继续等值。表更新的整数须先解码，截断／整数溢出优先于更新位置错误。
- 接收字符串限制来自本端声明的 262144 字节头部限制。65537 字节的声明本身未越限，缺少内容时仍报告截断；真正越限的名称／值声明分别归类，不能混同截断。头块／CONTINUATION 之间保留解码状态，错误向上保留官方编号、名称及连接压缩错误码。

离线矩阵复用原始输入，检查准确 GOAWAY、连接关闭、错误正文隔离和合法更新后的正常结束；固定副本另覆盖逐字节输入、复合错误优先级、跨块次数重置、明确扩容后的跨块索引及默认解码策略。旧网关已复现笼统调试字段、表更新位置与初始容量等差异。验证范围仍限已采场景，整帧未收齐时的 HPACK 交付、其他头部语义与聚合限制、后续设置缩表的完整状态、扩展帧及异常交错继续待验收。

最终 30 类场景、两轮三套官方与对应网关共 360 份样本，GOAWAY 的流编号、错误码、完整调试文本及关闭行为均匹配；合法控制的 PING 与空 trailers 正常结束。180 份完整官方 NetLog 与服务端原始帧交叉核对，最终源码重新编译采集程序后另复采 90 份网关，全部通过。最初的合法控制样本把响应头伪字段与未结束 trailers 混在同一块，不能单独证明表更新行为；该版本保留，最终使用明确结束的空 trailers，不把旧控制通过扩大为其余头部语义已对齐。

根模块普通全量、完整 upstream／providers race、固定 req 全量 race、HPACK／HTTP/2 三轮 race、构建、vet、lint 与格式检查通过，未跳过原回归。台湾时间 17:50 保数据更新 dev，实际容器 Work／Code 流式正文和正常结束均通过，容器健康。Qoder 国内三个临时 UMID 字段按已到期缓存刷新，新值与持久缓存核对一致；完整授权令牌、固定设备身份、配置、auth 文件与账号设置保留，两条绑定仅续期，无增删。停机备份、原严格差异及独立字段核对保留，没有恢复旧数据；前端未改，未提交、未推送。

### HPACK 半帧的提前解码与错误（2026-11-18）

前节的整帧矩阵不能证明物理帧未收齐时的行为。本轮三套官方运行时仅收到 HEADERS 或 CONTINUATION 的前缀时，已经确定的无效索引立即触发 `COMPRESSION_ERROR`，调试字段为 `Framer error: 19 (HPACK_INVALID_INDEX).`，不等待声明的剩余 payload。带 padding 或 priority 的 HEADERS 也遵循这一边界。名称 Huffman 声明 100 字节而只收到四个 `ff` 时，非法 EOS 已可确定，立即报告 `Framer error: 16 (HPACK_NAME_HUFFMAN_ERROR).`；不能等完整字符串才校验。旧网关在三条链路均等待余下物理帧，新增矩阵的无效索引子项已通过旧源码 overlay 复现超时。

固定副本新增 `read_headers.go`，在 `ChromiumUpload` 路径按实际到达的片段交付 HPACK；单次至多读取 8 KiB，padding 长度和 priority 前缀先解析，padding 全部消费后才交付该物理帧的结束标记。逻辑头块继续等待最终 CONTINUATION，首个 HEADERS 的原始 flags 保留。HPACK 复用已有 Huffman 实现检查未完整字符串的前缀：仅拒绝已确定的非法编码，不把合法但尚未结束的编码前缀误判为 padding 错误。截断物理帧保留 EOF 原因，普通 req 路径保持原策略。

脱敏输入保存于 `clienthello/trae-http2-hpack-fragments.json`，共六类非法前缀及四类合法控制。非法样本不发送尾部、不附加健康 PING，要求连接在只收到前缀时关闭；合法样本在明确补齐点之前不能完成，随后补齐 payload、padding 和 CONTINUATION，要求空 trailers 正常结束。两轮三套官方与对应网关共 120 份样本、60 份完整官方 NetLog 已独立核对错误码、完整调试文本、关闭及正文；合法控制同时检查服务端的提前完成观测点。该等待只用于控制采样边界，生产代码没有新增固定等待。

本轮完整 race 首次挂在 TLS 票据测试等待下一次 ClientHello 的 channel；SIGQUIT 栈及失败日志保留。另以受控任务回归确认：请求已结束但 ACK 等连接任务尚未排空时，`CloseIdleConnections` 原先直接返回，既没有禁止复用，也没有安排稍后关闭。现先禁止该空闲连接接受新请求，异步等待既有任务排空后关闭，活动流、预留请求和待分配流继续保留。重复回收不重复启动等待，显式关闭和错误排空可中断等待；票据测试增加明确失败期限，保留四次新握手、恢复状态与业务请求次数断言，没有放宽这些要求。此受控漏洞不等于已经证明它是首次挂起的唯一原因。

最终源码重新编译后另复采 30 份网关样本，全部通过。根模块普通全量、完整 upstream／providers race、固定 req 全量 race、HPACK／HTTP/2 三轮 race、TLS 票据二十轮 race、构建、vet、lint 与格式检查通过，未跳过旧测试。首次完整 race 的 SIGQUIT 失败及修正前受控回归失败均保留，没有将其覆盖为成功日志。

台湾时间 18:18 保数据更新 dev，容器健康；实际容器内 Work／Code 流式正文与正常结束均通过。配置、auth 文件、全部完整最新凭据、固定设备身份与账号设置一致，本轮没有临时 UMID 变化；两条绑定仅到期时间前进，账号及会话未变，无增删。停机备份、原严格差异和独立字段核对均保留，没有恢复旧数据。前端未改，未提交、未推送。

其他头部语义与聚合限制、后续设置缩表的完整状态、扩展帧、异常交错、缓存压力、非 Unix 与真实代理业务仍未全部验收，不能以本节有限矩阵推导所有传输完全一致。

### 响应头语义、聚合限制与流错误（2026-11-18）

本轮对 Chrome 156、Electron 39／Chromium 142 和 TTNet／Chromium 107 的真实运行时新增 47 类响应头样本。初始响应与 trailers 分别采样，后者先交付 8 KiB 预热正文，避免重试及读取预取混淆错误范围；畸形头块后发送探测 PING，核对 GOAWAY、RST、响应结果及控制帧顺序。脱敏原始输入保存于 `clienthello/trae-http2-header-semantics.json`，连续重复测试字节用无损游程表示，回归重组后验证 SHA-256。三套官方基线共 141 份样本及 141 份完整 NetLog 已交叉核对。

官方固定版本的 [HeaderCoalescer](https://chromium.googlesource.com/chromium/src/+/156.0.8078.79/net/spdy/header_coalescer.cc)、[SpdyStream](https://chromium.googlesource.com/chromium/src/+/156.0.8078.79/net/spdy/spdy_stream.cc) 和 [SpdyHttpUtils](https://chromium.googlesource.com/chromium/src/+/156.0.8078.79/net/spdy/spdy_http_utils.cc)，以及 142／107 对应文件，与样本共同确认以下行为：

- 响应伪字段可以是请求类或未知名称，只要冒号后是合法小写 token、位于普通字段之前；响应层读取 `:status`，其他伪字段不参与响应转换。缺少状态、重复状态合并后无法解析，或状态超过有符号 32 位整数范围，均只重置该流。两位、四位和 `099` 这些样本在传输层可接受，不能凭 HTTP/1 常规状态码范围收紧。
- 头名为空、大写、含空格，字段值含 CR／LF／NUL，或普通字段之后再出现伪字段，只重置该流。水平制表符与两端空白可接受。语义错误后仍必须消费剩余 HPACK 并更新动态表；若后续又遇压缩错误，则升级为相应连接级压缩错误，不用先前语义错误掩盖。
- 聚合上限为未压缩字段名、值及每字段 32 字节开销的总和 262144。加上固定 CORS／正文类型字段后，边界减一、等于及加一均实测；连续大字段或多次较小字段越限都只重置该流，不能沿用 Go 为避免浪费解码而提前关闭整个连接的策略。单字符串 HPACK 解码长度限制仍与前节保持。
- 初始最终响应中的 `transfer-encoding` 导致单流协议错误；`connection`、`upgrade`、`te` 等已采控制不触发该错误。已采相同／不同重复 Content-Length 和无效长度不据此升级连接错误，其他未采组合不作结论。
- trailers 中合法拼写的伪字段可接受。未带 END_STREAM 的 trailers 继续等结束；随后空 DATA 可以结束，有正文的 DATA 或又一块 HEADERS 则只重置该流。原有头名／值校验仍应用于 trailers。
- 连续六次 `100` 或 `103` 后正常最终响应可用，Chromium 路径去掉 Go 的第五次上限；普通 req 路径保留。其他无限信息响应、101 及更多结束时序尚未全部验收。

原始输入和首个探测 PING 在同一次 TLS 写入，与官方采样保持一致；大头块跨越已有读预算时仍依实际批次让出。读取错误触发的 RST 现于通知业务错误之前纳入网络序列，之后请求清理通过同一标记避免重复发送，不再依赖请求 goroutine 稍后排入取消帧。原 race 首次发现 RST 落后于后续探测的失败日志保留，修正后该子项连续 300 轮 race 通过；受控队列回归另检查错误通知前已入队、流删除后仍写出且只发一次。原异步正文／补窗回归把任意同流帧都计作正文，因新增正确 RST 而失败，现分别检查 DATA 与 FLOW_CONTROL_ERROR RST，保留无正文断言并增加准确重置数量。

Electron `net.fetch` 的非常规状态码样本超时，原日志保留；本轮使用同一官方网络栈的 `net.request` 直接消费响应事件，未把包装层未返回视为 HTTP/2 已失败。TTNet 官方 JS 桥还将已采的 `20`／`2000`／`099` 暴露为 `200`；不改变返回值的 Cronet C API getter 采样确认底层分别为 20／2000／99。夹具分别记录传输层 status 与 `application_statuses`，网关 HTTP/2 回归核对前者，不把 JS 桥的转换误当成协议行为。两套包装层的其他状态与业务影响仍待单独验收。

旧源码 overlay 已复现额外伪字段拒绝、聚合越限升级 GOAWAY、trailers 升级 GOAWAY、信息响应数量上限以及状态溢出误接受。新矩阵同时检查单流错误后的同连接新请求，保留原压缩错误、异常帧、半帧及窗口回归；普通 decoder 的伪字段校验保持。

最终六套运行时另采 282 份样本，连同官方基线共 423 份样本及 282 份官方 NetLog，准确错误、顺序、正文、传输状态和输入摘要逐项匹配。最终源码重新编译后另复采 141 份网关，累计 564 份样本核对通过。新旧协议矩阵与固定副本相关三轮 race、根模块普通全量、完整 upstream／providers race、固定 req 全量 race、构建、vet、lint、格式与差异检查通过，未跳过旧测试。

台湾时间 19:00 已保数据更新 dev，容器健康，实际容器 Work／Code 流式正文与结束均通过。Qoder 国内临时 UMID 三字段按到期缓存刷新，已核对新缓存与到期时间；完整授权令牌、固定设备身份、配置、auth 文件及账号设置保持。两条绑定只续期，无增删；停机备份、原严格差异和独立字段审计保留，没有回填旧令牌或身份。前端未改，未提交、未推送。

其他响应转换、101、内容长度与压缩组合、扩展帧、缓存压力、非 Unix 和真实代理业务继续待验，不能以这 47 类样本宣称全部响应语义一致。

### 信息响应结束与 Content-Length（2026-11-18）

本轮延续三套官方运行时，新增 37 类生命周期对照：`101` 的空流／正文／后一块响应头，`100`／`103`／`199` 的 END_STREAM 与随后最终响应，信息响应中的 transfer-encoding，Content-Length 的零／正数／负数／符号／空白／前导零／溢出，分片正文长短不符，以及 `204`／`205`／`304` 的空流和正文。三套官方基线共 111 份原始帧样本及完整 NetLog 已核对，脱敏输入和分平台结果保存于 `clienthello/trae-http2-response-lifecycle.json`。

固定版本 [SpdyHttpStream](https://chromium.googlesource.com/chromium/src/+/156.0.8078.79/net/spdy/spdy_http_stream.cc) 与 [HttpNetworkTransaction](https://chromium.googlesource.com/chromium/src/+/156.0.8078.79/net/http/http_network_transaction.cc) 及 142／107 对应文件显示，HTTP/2 正文以实际 DATA 和流结束为准，不用 Content-Length 截断读取或在正常 END_STREAM 后生成长度不符错误。Work／Auth 的真实样本确认声明为 0、3 或 5 时仍完整交付四字节，声明为 5 但 HEADERS 直接结束时正常空正文。固定副本已移除仅在 `chromiumResponseRead` 路径继承的 Go 长度校验；响应头与 `Response.ContentLength` 元数据继续保留，普通 req 路径仍检查超长及提前结束，新增回归分别确认这两类策略。此前响应合并一节所谓“长度错误保留”只描述当时沿用 Go 行为，本轮官方样本已推翻该行为作为 Chromium 对齐要求的假设。

TTNet 原生桥另有消费阶段差异：Content-Length 为零时不启动 Cronet 正文读取，直接完成空正文。只读 Cronet 回调记录确认该样本没有 `OnReadCompleted`，其他已采正长度不符则仍交付实际 DATA，例如声明三字节时回调累计四字节。Trae Code 的 profile 现对 HTTP/2 零长度响应关闭原正文并暴露 `http.NoBody`，释放仍由现有流结束／取消路径负责；Work／Auth 不应用这条桥接规则。原 `@aha-kit/net` 的 WHATWG Response 包装在 `204`／`205`／`304` 加上非空 ReadableStream 时会抛构造错误，即使没有实际字节；本轮使用官方 `rawFetch` 的原始正文流隔离这层包装，并保留原失败日志，不把构造器异常转写成传输错误。

三套官方在隔离页面策略后均将非 WebSocket 请求的 `101` 暴露为默认 `200`：`SpdyStream` 先接受 `101` 为响应，事务层为跳过信息响应清空头后重读，`SpdyHttpStream` 仍记录响应头完成，空 `HttpResponseHeaders` 按默认值转换。固定副本按这个阶段处理，不把 `101` 当普通 `100` 等待另一份最终响应，也不保留被事务层清空的原字段；之后第二块 HEADERS 是 trailers，随后的非空 DATA 仍触发单流协议错误。该规则不用于普通 req，也不宣称 WebSocket 握手已支持。

最初 Work 网页的跨域样本在 `101` 后取消，RST 为 CANCEL；在回环采样中移除跨域检查后，同一 HTTP/2 输入正常得到默认 200。原样本保留，最终 Chrome 仅在隔离回环中禁用页面安全策略以采传输行为，生产配置不变；不能把跨域策略导致的取消混为底层协议拒绝。Auth 使用 `net.request`，TTNet 使用原生 `rawFetch`，其 C API getter 和读取／成功／失败回调均只记录并原样转发，分别核对状态、累计正文、完成与错误。原包装层差异仍是独立边界，不以本节替代完整网页／JS 业务验收。

旧源码 overlay 已复现 `101` 误拒绝、超长正文被截断、短正文及空流误报 EOF；修改后的矩阵继续验证准确 RST／GOAWAY、正文、状态、输入摘要和同连接新请求，原 47 类头语义、HPACK、半帧、窗口及默认 req 检查保持。

最终六套运行时另采 222 份样本，连同官方基线共 333 份及 222 份完整官方 NetLog，逐项核对准确错误、控制顺序、正文、状态和输入摘要；TTNet 同时检查 C API 读取累计与成功／失败回调。相关新旧矩阵与固定副本三轮 race、根模块普通全量、完整 upstream／providers race、固定 req 全量 race、构建、vet、lint、格式和差异检查通过，没有跳过旧回归。

最终源码重新编译后另采 111 份网关，累计 444 份样本核对通过。台湾时间 19:26 保数据更新 dev，容器健康；实际容器 Work／Code 流式正文与正常结束均通过。配置、auth 文件、完整最新凭据、固定设备身份及账号设置保持，两条绑定只续期，无增删；本轮没有临时 UMID 差异。停机备份、原严格差异与独立字段审计保留，前端未改，未提交、未推送。

其他信息响应、任意状态转换、内容编码与缓存组合、真实代理业务、非 Unix 及原生 ARM64 继续待验，不能据此宣称所有响应行为完全一致。

### 压缩响应的分段读取和取消（2026-11-18）

在只有回环接口的命名空间中，使用 HTTP/2 假上游分别调用 Chrome 156、Electron 39 的 `net.request` 和 TTNet 4.2.243.19 的 `rawFetch`。假上游把首段压缩字节拆成两个 DATA，收到客户端已经读到首段的确认后才发送结束事件与 END_STREAM。三套运行时都能读完单层 gzip、deflate 和双层 `gzip, deflate`。双层编码分别使用单行逗号列表和两行同名字段，顺序等价，符合 [RFC 9110 § 5.3 与 § 8.4](https://www.rfc-editor.org/rfc/rfc9110.html#section-8.4)。

旧网关拒绝单行双层编码，并且在两行字段里只按第一行选解压器。平台响应现合并全部 `Content-Encoding` 字段，再从最外层开始惰性解压。网关三种 Trae 路径均在结束前交付首段并正确读取结束事件。取消覆盖服务端已发响应头但尚未发 DATA、gzip 首段后、双层首段后三种时序，均核对取消错误、单条 `RST_STREAM(CANCEL)` 和同连接探测 PING 往返。共享解压器还修正了关闭顺序，先关闭原始响应体以打断读操作，再释放解压器。旧版 Zstandard 回归复现卡住及数据竞态，修正后定向 race 连续五轮通过。WorkBuddy 与入站请求仍采用原有的单编码选择方式。

72 份复采覆盖上述读、取消及结束场景，保留了 36 份完整的官方 NetLog。Work／Auth 可解 Brotli、Zstandard 和两种 `gzip, br` 表示。TTNet 在线上声明 `gzip, deflate`，对三组含 Brotli 的响应返回 `-330` 并重置流；单层 Zstandard 返回原始压缩正文，Cronet 读取回调累计 64 字节与线上长度一致。Chromium 107 公开基线的[编码校验](https://chromium.googlesource.com/chromium/src/+/107.0.5304.110/net/http/http_network_transaction.cc)与[解码器识别范围](https://chromium.googlesource.com/chromium/src/+/107.0.5304.110/net/filter/filter_source_stream.cc)与这些样本一致。

2026-11-19（台湾时间）将 Trae Code 的目录与推理请求接入 TTNet 的响应编码规则。含未声明 Brotli 的三种表示均拒绝并关闭正文，回环观察到单条 `RST_STREAM(CANCEL)`，同一连接继续响应 PING。Zstandard 和 `gzip, zstd` 保留线上正文及原响应头，网关读取的 SHA-256 精确匹配服务端实际发送字节，Cronet 读取回调累计分别为 64 和 87 字节。对应的 37 份网关样本与一份新增的 TTNet 混合编码样本完成审核。三套官方运行时都完整交付了缺少 gzip 尾部八字节的正文，网关仍在交付已解出的字节后报告 `unexpected EOF`，该差异继续待验。

同日补采 16 份 gzip 边界样本，其中 12 份带官方 NetLog。Chrome、Electron、TTNet 在缺四字节尾部或 CRC 错误时交付完整输出并正常结束，在 DEFLATE 负载实际截断时也正常结束，交付的正文缺三字节。追加有效的第二个 gzip 成员时，三套运行时只交付第一成员。网关 Code 在前两种缺字节情形保留 `unexpected EOF`，在 CRC 错误时保留 `gzip: invalid checksum`，追加成员时继续解码第二成员。是否将校验失败或截断正文作为成功属于独立的业务决策，当前保留 Go 的校验及多成员读取行为。

同日补验 gzip 头部与零填充。Chrome、Electron、TTNet 共保留 12 份完整 NetLog。头部截断时只收到空正文，错误头部和误标为 gzip 的 zlib 正文返回解码错误，有效 gzip 后的零填充正常结束。网关修复了解压器初始化失败后关闭响应体时的空指针崩溃。Trae Work／Auth／Code 的 `gzip` 和 `x-gzip` 按声明读取 gzip，支持最后一个完整成员后的零填充，完整读取时仍校验各成员的 CRC 和长度。最终网关 21 份回环样本覆盖头部、填充、双层编码、CRC 错误和多成员。误标 zlib 的三条链路均发出一条 `RST_STREAM(CANCEL)`，各组均完成同一连接的两次 PING。[RFC 9110 的 gzip 定义](https://www.rfc-editor.org/rfc/rfc9110.html#section-8.4.1.3) [Chromium 156 解码器源码](https://raw.githubusercontent.com/chromium/chromium/156.0.8078.79/net/filter/gzip_source_stream.cc)

平台 JSON 接口在 8 MiB 上限内读完整个响应，再解析目录、授权或额度数据。回环回归覆盖 JSON 本身有效但 gzip CRC 错误的目录，确认错误传到调用方。

### TLS 票据过期与内存压力（2026-11-18）

同一回环假上游给 TLS 1.2／1.3 票据设置四秒寿命，服务端同时记录 ClientHello 是否携带票据及实际是否恢复。Chrome 156、Electron 39／Chromium 142、TTNet 4.2.243.19 与对应网关共 12 组、96 次握手。TLS 1.3 在过期后均不再发送该票据，首次完整握手后可恢复。TLS 1.2 在四秒票据提示后仍发送旧票据，服务端拒绝恢复，接着发出新票据；六套路径一致。

Chrome 和 Electron 共 19 组、3217 次握手。通过 DevTools `Memory.simulatePressureNotification` 触发通知，在同一进程内重新建立实际 TLS 连接。Linux Chrome 156 收到 moderate 后的容量为 512，第 513 个端点使最久未访问的有效票据淘汰。critical 清除旧票据，并在维持压力期间停止缓存新票据，切回 moderate 后恢复缓存。关闭 `StatefulMemoryPressure` 的对照组和 Electron 142 都在 critical 清空旧缓存后立即接受新票据。Electron 的 moderate 保留已测的 513 个未过期端点。Chromium 107 公开基线也有 moderate 清扫和 critical 清空。Trae 所带 TTNet 的 Linux 压力来源已另外采样，结果见下节。[Chrome 156 缓存实现](https://github.com/chromium/chromium/blob/156.0.8078.79/net/ssl/ssl_client_session_cache.cc) · [Chromium 142 缓存实现](https://github.com/chromium/chromium/blob/142.0.7444.235/net/ssl/ssl_client_session_cache.cc)

桌面 Linux 的 Chromium 监听系统 LowMemoryMonitor 或桌面门户的 D-Bus `LowMemoryWarning`。实际 dev 容器能读取自身 cgroup 的内存事件，并使用独立内存上限。网关服务器在 Linux 上每秒读取所属 cgroup v2 的 `memory.events`。从启动时的计数继续，`high` 增加触发中度压力，`max` 增加触发严重压力，同时增加时以严重压力为准。连续 20 秒无新事件后恢复。内核没有提供 `memory.high` 软限制的部署，通常只会经过严重压力和正常两个阶段。桌面 D-Bus 通知、cgroup v1 和非 Linux 压力来源仍待覆盖。[Chromium Linux 压力事件来源](https://chromium.googlesource.com/chromium/src/+/main/chrome/browser/dbus_memory_pressure_evaluator_linux.cc) · [Linux cgroup v2 的内存事件](https://docs.kernel.org/admin-guide/cgroup-v2.html#memory-interface-files)

网关 Work 在中度压力时按 LRU 缩到 512 项，严重压力时清空并暂停缓存，压力解除后恢复 1024 项。Auth 在中度压力时清扫过期票据，严重压力时清空并继续接收后续票据。Auth 按 Chromium 142 源码跳过清空前开始的连接传来的晚到票据。Code 在两种压力下均清理过期项，保留有效票据及原有容量上限。

晚到票据补采两组控制和两组压力场景。回环服务端收到首条连接的 TLS 1.3 Finished 后暂缓发送两张票据，再触发压力通知。Chrome 156 在严重压力降到中度后收到晚到票据，下一条连接恢复成功。Electron 142 在严重压力清空后收到晚到票据，下一条连接完整握手。两套运行时的无压力控制都恢复成功，结果与网关的同顺序回归一致。

2026-11-19（台湾时间）在两个独立限额、仅有回环网络的 Docker 容器中写入可回收文件页。软限制组只出现 `high` 增量，三个 profile 的已有票据均用于恢复握手，Work 容量由 1024 降至 512，再回到 1024。硬限制组出现 `max` 增量，三个 profile 的首次重连均完整握手。下一次 Auth 和 Code 成功恢复，Work 在严重压力期间持续完整握手，压力解除后恢复缓存。这是当时网关的容器验收记录。Code 后续按官方 Linux TTNet 的硬限制样本修正，见下节。

正常内存条件下，先注册一个长寿命端点和 16 个短寿命端点，等待短票据过期后补足容量。修复前三个官方运行时各做 1028 次握手，跨过容量边界后均能恢复最初的有效端点。对应的三条网关路径各做同样的 1028 次握手，均提前淘汰了这张有效票据。网关现按三个公开 Chromium 基线，在每第 256 次查找时先清扫过期票据，查找未命中也计数。普通查找先取出首张单次票据再清理该端点，首张到期时本次完整握手，仍有效的备用票据可留给下次。全局清扫发现首张已到期时移除整个端点。

TLS 1.2 采用 Chromium 的一小时本地期限。TLS 1.3 采用服务端票据期限和 BoringSSL 默认两天可续期期限中的较短值。网关用 TLS 配置的同一时钟判断到期。固定的 uTLS v1.8.2 没有时间 getter，缓存写入时从 `ResumptionState` 返回的结构反射只读 `createdAt` 和 `useBy`，字段不可读时走完整握手。升级 uTLS 时需要验证这两个字段及其语义。连续恢复时的认证期限及 TTNet 长期到期边界已在下节补验。[Chrome 156 TLS 配置](https://github.com/chromium/chromium/blob/156.0.8078.79/net/socket/ssl_client_socket_impl.cc#L195) · [BoringSSL 票据期限](https://github.com/google/boringssl/blob/2ea3200c3a3ec176613ab7a0b9a4ffc6db846120/ssl/tls13_client.cc#L1158) · [uTLS v1.8.2 会话接口](https://github.com/refraction-networking/utls/blob/v1.8.2/u_public.go)

2026-11-19（台湾时间）三条网关路径重新采样 3132 次握手。跨端点矩阵最后依次访问长寿命、过期、长寿命端点，三条路径的 ClientHello 票据发送及服务端恢复结果均为「是／否／是」，与原官方基线一致。六组四秒短寿命复测仍与原官方基线一致，另用受控时钟覆盖到期秒和时钟回退。

另用两条并发握手的确定事件顺序，复现旧连接的延后失效通知误删另一条连接刚写入的新票据。网关现只允许通知清理该握手取票时的缓存版本。TLS 1.2／1.3 均有回归，同时保留两张单次票据、现有 1024 项上限及早期失败重连行为。

定向竞态测试另确认，底层 Transport 会把拨号与发起请求的取消信号分离。客户端取消后遇到恢复阶段 EOF 时，三条网关路径原本均会在后台额外拨号。现由发起请求的上下文约束恢复失败后的重连；回归等待握手回调结束再检查拨号次数。

### ClientHello 会话恢复的长期到期边界（2026-11-19）

在仅有回环网络的 Linux x64 命名空间中，分别运行 Chrome 156.0.8078.79、Electron 39.2.7 和 Trae 自带的 TTNet。测试专用证书有效期覆盖整个场景。每次连接前推进客户端进程的墙钟，单调时钟继续正常运行。假上游分别记录 ClientHello 是否携带票据以及服务端是否实际恢复。网关测试通过真实 Transport 连接信任同一类测试证书的回环服务端，以同样的时间点检查这两个结果。九组场景包含官方 84 次和网关 84 次握手，逐次对照一致。另做 12 次 TLS 1.2 官方握手，要求假上游在恢复时重发非空票据，三套运行时均在最初认证的第 3600 秒完整握手。

| 场景 | 官方运行时与网关的结果 |
|---|---|
| TLS 1.2 一小时期限 | 第 3599 秒可恢复，第 3600 秒不带旧票据，下一秒可用新票据恢复。Go 假上游在恢复时重发票据，修复前网关在第 3600 秒仍携带旧认证的票据。 |
| TLS 1.3 两天可续期窗口 | 在两天前一秒恢复，此后恰好两天再连接时完整握手，下一秒可恢复。 |
| TLS 1.3 七天认证期限 | 每天恢复并更换票据，第七天前一秒仍可恢复。第七天整不发送 PSK。修复前三条网关路径在该秒都发送了 PSK，服务端也接受了恢复。 |
| 服务端拒绝恢复 | 第八天在服务端更换票据密钥，客户端发送 PSK 后完成证书握手。其后连续换票，第十五天前一秒可恢复，第十五天整重新完整握手。 |

网关在每条票据上保留所属连接最初认证的时间。只有握手实际恢复才继承这个时间。TLS 1.3 还按本次握手的两天窗口和服务端声明的票据寿命取最早期限。单元回归另覆盖延迟到达的票据，已到期连接收到的新票据不能覆盖另一条连接的新认证。[RFC 8446 §4.6.1](https://www.rfc-editor.org/rfc/rfc8446.html#section-4.6.1) · [BoringSSL 的七天认证期限](https://github.com/google/boringssl/blob/2ea3200c3a3ec176613ab7a0b9a4ffc6db846120/include/openssl/ssl.h) · [BoringSSL 的会话续期](https://github.com/google/boringssl/blob/2ea3200c3a3ec176613ab7a0b9a4ffc6db846120/ssl/ssl_session.cc)

### Linux TTNet 的内存压力与恢复握手（2026-11-19）

在仅有回环网络的 Linux x64 环境中，用 Trae 的 Electron 39.2.7 同时调用 `electron.net.fetch` 和官方 `@aha-kit/net`。确认主进程已经加载随包的 `libsscronet.so`。每条路径访问两个独立端口，每次响应均关闭连接。触发事件前先确认两个端点都能恢复；事件后的首次重连分别延迟约 350 毫秒和 1.85 秒，再各重连一次。假上游独立记录 ClientHello 是否携带 PSK、TLS 是否实际恢复，并核对正文确实来自对应端点。

七种场景各跑三轮，合计 336 次握手。两种 DevTools 通知均收到成功回执。两个私有 D-Bus 服务分别发送 `LowMemoryWarning(255)`，独立监听端逐次收到，查询到的相关订阅规则均来自验收监听端。两个 cgroup 场景分别用可回收文件页触发真实 `high` 或 `max` 增量，均未发生 OOM。[DevTools Memory 域](https://chromedevtools.github.io/devtools-protocol/tot/Memory/#method-simulatePressureNotification) · [桌面门户 LowMemoryWarning 信号](https://flatpak.github.io/xdg-desktop-portal/docs/doc-org.freedesktop.portal.MemoryMonitor.html#signals)

| 事件 | Electron 首次重连 | 同一进程的 TTNet 首次重连 |
|---|---|---|
| 无通知、DevTools moderate | 两个端点均恢复 | 两个端点均恢复 |
| DevTools critical | 两个端点均完整握手，此后的重连恢复 | 两个端点均恢复 |
| 系统 LowMemoryMonitor、会话桌面门户 | 两个端点均恢复 | 两个端点均恢复 |
| Linux cgroup v2 的软限制、硬限制 | 两个端点均恢复 | 两个端点均恢复 |

另以 Trae 自带的 Electron Node 单独加载 TTNet，复测无通知、两种 D-Bus 通知及两种 cgroup 限制，合计 40 次握手。两个端点在每次事件后均携带 PSK 并成功恢复。

网关此前将 Code 的硬限制事件映射到 Chromium 107 的严重压力清空策略。旧版在真实 `max` 增量后，Code 首次重连完整握手；修正后保留有效票据、清扫过期项，容量仍为 1024 个端点。这里只确认 Linux 进程外部事件下的握手结果。随包 JS binding 和 Cronet 导出接口中尚未找到可直接向 TTNet 内部投递压力通知的入口；内部监听器接到通知时的行为仍未确认。

### 账号绑定代理访问真实上游（2026-11-18）

23:05（台湾时间），复用正在运行的 dev 镜像另启隔离网关。容器仅有 loopback、没有直连路由。每轮把验收副本的账号显式绑定到一个要求认证的代理，启用「只许走号的代理」。代理只转发到本次验收所需的官方地址，记录确认四种线路合计 33 条隧道实际双向交换 TLS 数据。临时实例只使用当时有效的访问令牌和已有设备字段，未传入刷新令牌、PAT 或签名私钥。

HTTP CONNECT、HTTPS CONNECT、SOCKS5、SOCKS5H 四种绑定均通过 Trae Work／Code 实时目录、Trae Auth 用户查询、Qoder 国内目录及国内／国际官方额度。四种线路的 Qoder 国际目录都报告上游模型停用。HTTPS CONNECT 的 Trae Work `deepseek-v4.1-flash` 和 SOCKS5H 的 Trae Code `Doubao-Seed-Code` 各一次流式请求均完成，正文和结束事件正确。首轮临时代理的启动健康检查受测试端点白名单影响，记录保留。正式轮将启动探测移出业务采样窗口，代理认证和真实上游请求仍逐条核对。

Trae Auth 此轮调用的是官方 `GetUserInfo`，代理上的授权码交换和账号令牌刷新继续待验。Qoder UMID helper 的四种代理另有[真机交互记录](qoder-device-identity.md#实际验收记录)。隔离容器、网关和临时代理均退出，实际 dev 继续运行，未变更账号绑定和配置。

23:36（台湾时间），将当前源码编译出的网关放进同一 dev 镜像的隔离副本。第一条 HTTP CONNECT 校验了预期认证头并注入一次 407，第二条绑定线路与官方 Trae Code 完成 TLS 双向传输。单次 `Doubao-Seed-Code` 请求返回 HTTP 200、预期正文及 SSE 结束事件。请求记录显示两次尝试并归属至成功的账号和代理，粘性也落到成功账号。首条代理转为不可用，两个账号均无新增冷却且已释放在途槽位，隔离副本的临时访问凭证保持一致。首轮夹具提前断开连接，第二轮才取得明确的 407 证据。两轮容器均已退出。

离线回归分别在目录、推理和刷新 CONNECT 阶段注入拒绝，同时核对取消、部分输出和全部线路不可用的 503。TraeWork 创建会话后、Qoder 接受队列请求后，以及刷新已收到重定向再遇到线路故障时均不换号。Qoder 目录的线路故障不淘汰已发现的节点。

2026-11-19 13:48 至 13:51（台湾时间），在无直连路由的隔离实例中补验七种真实故障切换。Trae Work 覆盖 HTTP CONNECT、HTTPS CONNECT、SOCKS5、SOCKS5H，Trae Code 补齐后三种。第一条绑定代理均拒绝一次认证，其中 CONNECT 返回 407，SOCKS 返回用户名／密码认证失败。第二条与官方上游交换 TLS 数据，返回 HTTP 200、预期正文和 SSE 结束。七次请求均在第二次尝试成功，日志和会话绑定归属正确，账号无新增冷却、在途槽位均释放，验收副本的访问凭据一致。隔离实例均已退出。Trae 代理授权码兑换和刷新、其他平台的真实故障切换继续待验。

### 账号额度的展示口径

Trae 官方 Work 网页 `work-1402.b3bc0495.js` 的商业领域转换保留 `quota.credits_limit`、`usage.credits_amount`，`credits_limit=-1` 表示不限量，缺失的用量保持未知；时间字段为 `entitlement_base_info.start_time/end_time`，另保留包返回的 `expire_time/next_billing_time`。网关只投影这些已知字段和样本中的 `total_balance`、`usage_summary.consumed_amount/total_amount`，不透传 user_id／entitlement_id 等身份信息，不按逐包汇总替换官方账号数字。真实样本的包用量与汇总增量未完全对齐，尚未确认包状态枚举。

2026-11-18 核对官网已加载的 `90247.082df4f81d.js`，其中专属积分升级通用积分的告知配置自 2026-09-22 20:00（台湾时间）生效。[当前套餐与计费](https://docs.trae.cn/ide_plans-and-billing)按会员积分与奖励积分说明余额和消耗顺序。读取到的账号快照尚不能证明存在现行 Work／Code 独立额度池或区分某笔扣费所用的包。

Qoder 国内／国际的 `qoderUsage.userQuota` 的 total、used、remaining 和 unit 分别保留；期限使用返回的 expiresAt／refreshAt，不推算周期。未知 displayMode／缺少已确认数字时报告协议未支持，不补零；enterprise 响应仍待独立核实。国内查询继续使用已实测的 `Cosy-ClientType: 10`、`User-Agent: Qoder`；国际版使用 CLI OpenAPI 身份，两者不与 COSY 推理请求混用。


### 单次扣费字段与归属（2026-11-17）

- Qoder CLI `1.1.89` 的 session credits 计量器按 request ID 去重，合并 `usage.credits`／`original_credits`／`billable`。本仓库与官方计量器一致：`billable=false` 实际扣费为零；`billable=true` 时仅使用有限非负的 `credits`；缺完整字段则未知。`original_credits` 不等于实际扣费，SDK 的 `total_credits` 是多次模型调用累计，也不能当单次扣费。
- Trae Work 网页 `work-main.3b45430b.js` 的 `getCnSessionUsage` 用 `userMessageId` 查询 `POST ${getCoreHost()}/api/v1/commercial/get_session_usage`，正文为 `{"session_id": userMessageId}`。国内部署的 `CORE_HOST` 为 `https://trae-api-cn.mchost.guru`，鉴权头为 `Authorization: Cloud-IDE-JWT <token>`；使用网页运行时的 UA／TLS／HTTP2，JSON Content-Type。费用取 `user_usage_group_by_session.credits_float`（支持可选 `data` 外包装），网页对非正数不展示费用。
- 容器真实取样证实，Work 创建会话响应的 `data.message_id` 可取得单消息已结算费用，费用响应的 `session_id` 与该 ID 相等；不能替换为 `chat_session_id`。网关额外要求响应回显 ID 一致，拒绝误归属。
- 同轮 Trae Code `llm_utils_chat` 原始 SSE 包含 metadata／timing_cost／output／extra_info／token_usage／done，没有 fee_usage 或其他已确认扣费字段。Work 原始流亦无单次费用，须查官方消息费用接口。不能根据客户端定义了 `fee_usage` 枚举便宣称当前路径已提供扣费。
- 2026-11-18（台湾时间）复核归档及只读查询。Code 样本的 metadata 回显请求传入的 `session_id`，`prompt_completion_id` 为空。用同一账号从 dev 查询四个 ID：Work 用户消息得到正数费用及一条明细；Code 会话得到零金额及一条明细；Work 聊天会话和一个新生成、未使用的 ID 得到相同的无明细响应。Code 的明细因此可与未命中区分，但 `usage_group_details` 及附加信息没有可与单次生成关联的请求或消息 ID，也没有确认结算状态的字段。继续将 Code 的逐请求费用记为未知。此检查未发起新的模型推理。
- 2026-11-18 23:07（台湾时间）核对[官网个人用量管理](https://www.trae.cn/dashboard#usage)当时加载的官方 `dashboard/page.6e98c37aaa.js`。积分明细通过 `POST https://api.trae.cn/trae/api/v1/pay/query_user_usage_group_by_session` 获取，授权使用 `Cloud-IDE-JWT`，积分筛选为 `usage_type: [7]`，时间和页码由页面传入。实际 dev 容器从官方取得 11 月 17 日至 18 日的数据，分页完整，分别按旧 Code 会话 ID 和旧 Work 用户消息 ID 命中。已观察的账单行、`usage_group_details` 与可解析的 `extra_info` 中都没有可绑定到单次生成的请求或消息标识。官网能回查会话积分，单次 Code 扣费继续记未知。页面入口见[官方订阅管理](https://docs.trae.cn/ide_subscription-management)。
- 2026-11-19（台湾时间）核对企业版用量 OpenAPI。按会话查询需要 `statistics:read`，按会话返回 `total_cost` 和聚合后的 `model_call_count`。官方响应示例的一条明细包含两次模型调用，顶层 `request_id` 标识这次用量查询。个人版 Code 路径的单次扣费仍缺可用的关联字段。[企业版会话用量接口](https://docs.trae.cn/enterprise_query-usage-details-by-session-id?lang=zh)
- Work 网页的 `getSettlementDelay` 为新消息安排两秒等待，历史消息可立即查。网关在流结束或中断时立即查询，第一次为空或为零时于查询开始两秒后补查一次，共用三秒期限。流中的 `usage.credit` 不作为费用来源，Token 用量照常处理。到期尚未结算时记未知，真实额度池隔离与逐包差额仍未确认。

真实原始样本及消息归属校验保存在仓库外的私有验收材料中，文档不包含账号身份、令牌或实际额度数字。
