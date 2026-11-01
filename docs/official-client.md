# 官方客户端对照

网关的出站请求按官方 WorkBuddy 桌面端的取值发送。这里记下依据从哪里来、对齐到了哪一步，以及官方升级后怎么重新核对。

## 依据

| 项 | 国内版 | 国际版 |
|---|---|---|
| 桌面端安装包 | WorkBuddy 5.9.3（macOS arm64） | WorkBuddy AI 5.9.4（macOS arm64） |
| 内置 CLI（`@tencent-ai/codebuddy-code`） | 2.165.0 | 2.165.0 |
| `applicationName`（product.json） | `WorkBuddy` | `workbuddy-ai` |

2026-10-20 从官网下载两个安装包（当时国内版是 5.8.3、国际版是 5.8.4），只解包阅读，没有运行；国内版 5.9.3（2026-10-28）、国际版 5.9.4（2026-10-31）出来后照同样的做法重新对过，见[与桌面端 5.9.3 的对照](#与桌面端-593-的对照)和[与国际版桌面端 5.9.4 的对照](#与国际版桌面端-594-的对照)。要看的文件有三处：

- `app.asar.unpacked/cli/dist/codebuddy-lite-wb.mjs`：内置 CLI，对话、模型目录、联网搜索、备用入口切换都由它做；
- `app.asar.unpacked/cli/product.json`：版本、`applicationName`、产品开关与备用入口配置；
- `app.asar` 里的 `main/`：桌面端后台服务，积分、签到、登录、刷新令牌、上报、外观、专家列表由它发（积分、签到在 `authority-snapshot.js`，5.8.3 是 `tar.js`；登录、刷新令牌在 `common2.js`；压缩后的文件名会随版本变）；每次提问前它还给内置 CLI 的模型请求另加几个头，见[几类请求](#几类请求)里的对话。

成长中心、积分补偿、新手礼包、试用只在官网网页上，同日读了 `www.workbuddy.cn` 成长中心页的前端代码。

npm 上更新的 CLI 只作对照，取值以安装包内置的版本为准：用户实际跑的是安装包里的 CLI，网关在 UA 里报的也是它的版本号。npm 上的 CLI 几乎每天发一版，和桌面端不同步；只把 UA 里的 `CLI/<版本>` 换成 npm 的新版本，会凑出官方不存在的组合（5.8.3 的桌面端不会带 2.174.0 的 CLI），新版 CLI 多发或改发的头也对不上。

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
- `Accept-Encoding: gzip, compress, deflate, br`：axios 的 http 适配器自动加的（内置 CLI 的 `advertiseZstdAcceptEncoding` 默认关，不带 zstd），官方没有在哪个请求上改写。网关按 `Content-Encoding` 解压（`encoding.go`）。

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
- 请求体带了思考档位（`reasoning_effort` 或 `reasoning.effort`）时补 `reasoning_summary: "auto"`（`injectReasoningSummaryIfNeeded`）。2.161.0、2.165.0 都不发 `cache_retention`，也不发 `prompt_cache_key`；
- 任务中心的专家对话就是一次普通对话，只多带 `X-Expert-Id`（专家插件激活时追加）；团队专家（专家列表里 `expertType` 为 `team`）另带 `X-Expert-Team-Task: true`（后台服务的 `applyPreparedExpertSettings`），网关按专家列表里的类型带。

模型目录：网关用对话的 UA，按 GET 拉 `/v3/config`，带 `Connection: close`。官方后台服务也拉（`CloudProductProvider`，另带查询参数 `repos`），UA 跟时机有关，未确认，见[与国际版桌面端 5.9.4 的对照](#与国际版桌面端-594-的对照)。企业号另拉企业模型，见[企业号模型目录](#企业号模型目录)。

## 联网搜索

内置 CLI 的 WebSearch 工具（2.165.0 的 `performWebSearch`，2026-10-31 读国内版 5.9.3 安装包核对；国际版 5.9.4 内置的是同一份 CLI）不经请求框架，用 Node 原生 `fetch` 发 `POST <对话入口>/agenttool/v1/search`，20 秒超时。网关照这份发（`upstream/search.go`），不复用对话的头（原版 linguo2625469 是复用 `ChatHeaders` 再改几项）：

- 请求头只有这些：`Accept: application/json`、`Content-Type: application/json;charset=UTF-8`、`X-Requested-With: XMLHttpRequest`、`User-Agent: CLI/<版本>`（版本是客户端信息里的 `platformVersion`，取产品配置的 `productVersion`，在桌面端里就是桌面端版本；`CLI` 是默认平台名常量）、`Authorization`、`X-Agent-Intent`（会话的模式，默认 `craft`）、`X-Agent-Runtime`（同对话，见[几类请求](#几类请求)）、`X-Conversation-ID`、`X-Conversation-Request-ID`（没有时是空串）、`X-Conversation-Message-ID` 与同值的 `X-Request-ID`（每次新生成的 `uuidv7()` 去连字符）、`X-Product`、`X-IDE-Type`、`X-IDE-Name`、`X-IDE-Version`（同 UA 里的版本）、`X-User-Id`、`X-Department-Info`（总是带，没有部门时是空串）。不带企业头（`X-Enterprise-Id`、`X-Tenant-Id`）、`X-Domain`、`X-Stainless-*` 和链路头；`Accept-Language: *`、`Sec-Fetch-Mode: cors`、`Accept-Encoding: br, gzip, deflate` 是原生 fetch（undici）自己加的。内网版（IOA）的默认头、专家会话的自定义头官方另加，网关都不涉及。`client_name` 配成 `SaaS`（旧行为）时同对话，不带 `X-IDE-*`；
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
- 签到状态和签到另带 `X-Device-Token`（`buildHeadersWithTuringToken`，令牌和错误码的规则同对话，见[几类请求](#几类请求)）；积分查询另带 `Accept-Language`（`resolveAcceptLanguage`：界面语言是中文发 `zh`、英文发 `en`，都不是时国际版 `en`、国内版 `zh`，网关按域取）。

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
- 成长中心接口（地址含 `/activity/growth`，包括 `/v2/activity/growth/...`）另设 `Accept-Language`：官网 `config-*.js` 的请求拦截器对这类地址显式设语言头（2026-10-31 读 `ef3f6107` 这一版），取值只有 `zh-CN` 和 `en-US`，页面地址带 `lang=en…` 时才是 `en-US`，桌面端打开成长中心的地址不带 `lang`。网关国内版发 `zh-CN`，国际版按英文页面发 `en-US`（网关不对国际版调成长中心，国际版页面怎么带 `lang` 未确认）。1.8.0 及以前发的是 Chrome 自动带的 `zh-CN,zh;q=0.9`；
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
- 查询：`POST {对话入口}/v2/chat/queue/status`，请求体 `{"model":模型}`，头是 `Authorization: Bearer <token>`、`Content-Type: application/json`、`X-Conversation-Request-ID`（排进队的那一轮），有会话 id 时加 `X-Conversation-ID`。主进程用 axios 1.16.1 的默认实例发，没有官方请求框架的那些头，只多 axios 自带的 `Accept: application/json, text/plain, */*`、`Accept-Encoding: gzip, compress, deflate, br` 和 `User-Agent: axios/1.16.1`；超时 15 秒。回 `code 0` 且带 `data` 时看 `data.status`：`waiting`（带 `queue_position`）接着等，`ready` 到号，`expired` 过期；`code` 不是 0 或没有 `data` 都当过期，说明取 `msg`，没有就是 `Unknown error`。
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
- 传输层：网关出站一律 HTTP/1.1（有意禁了 HTTP/2，见 `transport.go`）。TLS 握手 2026-10-28 起照官方桌面端伪装（`tlsmimic.go`，默认开，此前是 Go 标准库握手）：
  - 桌面端请求官方走 HTTP/1.1，TLS 握手由 Electron 37.10.3 里的 Node `v22.21.1`（BoringSSL）发。它的 ClientHello——密码套件顺序、扩展顺序、不带 GREASE、ALPN 只 `http/1.1`——既不同于 Go 标准库，也不同于任何浏览器预设，服务端能据此分出客户端类型，所以只能照抓到的真实握手复刻。
  - 用 [uTLS](https://github.com/refraction-networking/utls) v1.8.2（BSD，许可相容）：`tlsmimic.go` 里 embed 一份本机实测抓到的官方 ClientHello 原始字节（`clientHelloHex`），每次拨号用 uTLS 的 `Fingerprinter` 现解析出一份独立的 `ClientHelloSpec`（避免扩展对象跨连接共享），再用 `HelloCustom` + `ApplyPreset` 按它握手。SNI、随机数、`key_share` 等每连接的值由 uTLS 现生成，embed 里抓包时的具体值（含当时 SNI）不会泄漏到实际连接；证书校验与 SNI 仍按请求域名，钉 IP 时也如此。
  - 抓取办法：2026-10-28 用桌面端内置 Node（`ELECTRON_RUN_AS_NODE` 跑 Electron 的 node）向本机回环发一次握手、抓下 ClientHello。JA3 只看 BoringSSL 与 Node 的 TLS 配置，与宿主系统无关，所以在 Linux 本机抓到的即是官方 Mac 桌面端的指纹。目标 `JA3 = 771,4865-4866-4867-49199-49195-49200-49196-49191-52393-52392-49161-49171-49162-49172-156-157-47-53,0-23-65281-10-11-35-16-13-51-45-43-21,29-23-24,0`，`JA3 MD5 = 1a3153f314dc13e133dc71d113a81b16`，`JA4 = t13d1812h1_5d04281c6031_ef7df7f74e48`（JA4 比 JA3 还多算进签名算法扩展的内容）；uTLS 复刻已逐字节核对一致，单元测试 `tlsmimic_test.go` 把 JA3、JA4 都照同一份握手锁死，指纹一漂移就红。桌面端升级后重抓替换 `clientHelloHex` 即可。
  - 开关：配置 `upstream.tls_fingerprint`（默认开，改后重启生效），关掉回落 Go 标准库握手（旧行为）。
  - 仍没对齐（有意）：官网网页在浏览器里走 HTTP/2 和 Chrome 的 TLS 握手，HTTP/2 才有的 `priority` 头网关也没有；网关伪装的是桌面端（HTTP/1.1），不是浏览器，所以不去对齐网页那套指纹。
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

## 官方升级后怎么核对

1. 从官网下载两个域的新安装包，各自解到一个空目录，只读，不运行。
2. 在 `cli/product.json` 里读版本、`applicationName`、`commit`、`date` 与 `productFeatures`。
3. 在内置 CLI 和 `main/` 里搜上文表格列出的函数名，逐项对照取值。压缩后的变量名、文件名每版都变，直接比文件看不出东西：先去掉变量名、按目录逐个文件比，有差别的文件再按函数切开比（2026-10-31 对国际版 5.9.4 就是这样比出 `main/` 只有三处不同）。
4. 有变化就改这几处常量，再按改动补测试、写更新日志：
   - `internal/upstream/headers.go` 的 `defaultClientVersionCN`、`defaultClientVersionGlobal`、`defaultCliVersionCN`、`defaultCliVersionGlobal`（两个域的桌面端不一定同时升级，CLI 的版本各取各的安装包）；
   - 对话请求头在 `internal/upstream/headers.go` 的 `ChatHeaders`：内置 CLI 里搜 `getCustomHeaders`、`resolveAgentRuntime`、`axiosToFetchAdapter`，`X-Private-Data` 搜 `resolveModelOptimizationEnabled`、`isEnterpriseEditionAccount`；后台服务每次提问前加的头搜 `BeforePromptExpertActivation`、`getTuringDeviceToken`、`shouldProtectSession`；OpenAI SDK 的 `X-Stainless-*` 在 `lazy-lite-wb/` 里搜 `X-Stainless-Package-Version`（版本号）、`buildHeaders`（取值）、`normalizePlatform` / `normalizeArch`（OS / Arch 映射），`X-Stainless-Runtime-Version` 要按 Electron 内置的 Node 定——`unzip -p … 'Electron Framework' | strings | grep '^v2'` 读 `Electron Framework.framework`，`Info.plist` 里是 Electron 版本（改 `nodeRuntimeVersionCN` / `nodeRuntimeVersionGlobal`）；
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
