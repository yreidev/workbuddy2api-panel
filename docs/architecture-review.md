# 公开同类网关检索与架构审查

2026-11-19（台湾时间）。本地审查基线为 v1.13.2（`fe61668`）加当前工作区。Trae／Qoder 结论针对工作区中的多平台实现。下列复现全部使用临时目录、假上游和测试凭据。

## 当前工作区修复

审查时的故障记录保留在下文。后续工作区已调整模型资格判断、用量保存、平台账本和本地排队。回归覆盖同一账号依次请求 A、B、A，面板写盘错误、无新增请求时自动补写、旧数据迁移、快照重复重放、账号删除后重导、三协议账本失败和排队上限。[当前行为与恢复方法](multi-platform.md#文件升级与恢复) · [平台账本回归](../internal/providers/activity_test.go) · [排队与模型回归](../internal/providers/queue_test.go)

## 审查结论

审查时发现两个正确性问题，以及两个需要明确策略的架构问题。现有单二进制、嵌入式面板和三种协议入口可以继续沿用。证据集中在选号和用量落盘，与重新实现 ClientHello 无关。

| 优先级 | 行为与影响 | 证据 |
|---|---|---|
| 高 | 新平台账号的目录缺一个模型，整号会冷却 30 秒。一个原本能用的模型随即收到 `503 no_available_account`。 | 单个假 Qoder 账号，依次请求有效模型 A、目录外的 B、原来的 A。真实 Chat Completions 入口依次返回 200、404、503。[选号与冷却](../internal/providers/execute.go) |
| 高 | 用量快照写失败后，只要没有新增用量，下一次周期刷新就跳过。面板的立即保存还会向操作者报告成功。 | 临时目录模拟写失败，恢复写入条件后，周期刷新仍未生成文件。另走面板真实 HTTP 入口，目标不存在时得到 `200 {"ok":true}`。v1.13.2 的记录器也有提前清除待写标记的逻辑。[记录器](../internal/usage/usage.go) · [面板处理函数](../internal/panel/panel.go) |
| 中 | 新平台要求本地状态保存成功后才能给出正常结束。上游已完成、已返回用量时，保存出错仍会让非流式客户端收到 502，已开始的流缺少正常结束事件。 | 临时故障复现出内存计数与上次持久化计数不同。仓库现有测试覆盖 Chat Completions、Messages、Responses 各自的流式与非流式场景，六项均符合这项现有约定。[执行结束](../internal/providers/execute.go) · [三协议回归](../internal/server/providers_test.go) |
| 中 | 新平台等本地账号空位时，只受整次请求的十分钟期限、客户端取消或账号状态变化约束。所有账号忙时，本地等待者没有独立时间和数量上限。 | 按服务端入口和选号循环核对。WorkBuddy 已有默认 30 秒的独立等位预算。这里说的是网关等账号槽位，Qoder 上游自己的资源队列有独立实现。线上排队规模和尾延迟未测量。[新平台等待](../internal/providers/execute.go) · [请求期限](../internal/server/providers.go) · [WorkBuddy 等位](../internal/pool/slotwait.go) · [Qoder 上游队列](../internal/providers/qoder_queue.go) |

### 先改模型冷却和用量保存

选号循环先占账号槽位、递增尝试数，再取该账号的模型目录。目录不包含请求模型时返回可重试错误。通用错误路径随后对整个账号设置冷却，把“这个号没有 B”和“这个号暂时不可用”合并了。应该按已知目录跳过不合格候选，过期或未获取的目录照常刷新，遇到目录不匹配时继续选其他账号。验收还要覆盖两个账号分别只支持 A、B，第一账号不匹配仍能在有限轮换次数内选到第二账号。WorkBuddy 自己已经有按模型判断健康和冷却的逻辑。友商也分别保存凭证下的各模型状态，或在调用前把请求模型纳入候选资格检查。[本仓库按模型判断](../internal/pool/entry.go) · [CLIProxyAPI 的 ModelStates](https://github.com/router-for-me/CLIProxyAPI/blob/db7f177d6b0e51eb237daed718de4d25e7a893f5/sdk/cliproxy/auth/types.go#L111) · [Sub2API 的候选复核](https://github.com/Wei-Shaw/sub2api/blob/2af3f1e15c98c893a89255a037f551046883b8c0/backend/internal/service/openai_account_scheduler.go#L545)

用量记录器在磁盘写成功前清除了待写标记。建议串行执行快照落盘，保留失败时的待写状态，只有确认该版快照保存成功才清标记。写入期间产生的新用量也要留到下一轮。让手动保存把实际错误返回给面板。验收时检查没有新流量且磁盘恢复后能自动补写，并检查手动保存失败时 HTTP 响应与磁盘状态一致。[用量快照](../internal/usage/usage.go) · [手动保存入口](../internal/panel/panel.go)

### 平台账本与对话交付

新平台每次账号尝试结束都在同一把管理器锁内，把账号及凭证、全部用量汇总、会话绑定一起序列化、写临时文件、同步文件、重命名并同步目录。随后才唤醒等账号的请求。这里已经采用原子替换，问题在于一次高频计量写入牵连的状态太多。成功对话遇到这一步失败，上游尝试记为成功，客户端记录失败，内存状态又领先于磁盘。这个后果在本地可复现，吞吐损失的大小未压测。[保存的状态结构与磁盘操作](../internal/providers/store.go) · [两套用量更新](../internal/providers/execute.go) · [客户端错误映射](../internal/server/providers.go)

建议先保留“账本确认才正常结束”的现有约定，把“上游已完成、账本写入失败”作为独立结果记录，便于管理员找出待核对的请求。再把用量写入与凭证、账号配置分开，用同一条带请求／尝试标识的持久记录更新或重建汇总。请求日志可以批写，硬配额预留和结算需要各自的原子边界。友商的日志异步写入只适合借鉴日志路径，不能直接套到扣费语义上。[WorkBuddy2API-Hub 日志写入器](https://github.com/niyouqianma/workbuddy2api-hub/blob/5a95e48859930a0a68e6047025a39fd8142128ad/wb_logstore.py) · [New API 数据库条件更新与额度预留](https://github.com/QuantumNous/new-api/blob/3a407cc9b064fe79a4ffac4146b2690c465dc929/model/quota_reserve.go#L143)

### 本地排队和平台边界

当 Trae／Qoder 账号都在使用时，HTTP 请求会继续占着连接等待。本仓库现有的 WorkBuddy 等位预算可以作为第一步，再按需要限制全局与每个密钥的等待数量，区分“队列满”“没有合格账号”和“上游正在排队”。`xiaofan6ya/wb2api` 已有全局、每密钥、等待时间三类限制。`CangShui/workbuddy-gateway` 的并发实现同时取得全局和账号容量，避免忙账号先占全局槽位。这两种做法解决的是不同问题。[wb2api 容量队列](https://github.com/xiaofan6ya/wb2api/blob/21276d214667eeb05c703d5d2de0abbea3a0fdaa/admin/capacity_queue.py#L76) · [WorkBuddy Gateway 并发控制](https://github.com/CangShui/workbuddy-gateway/blob/5250f894ec051edc28db56f9a9975f0f12209fc1/concurrent.go#L57)

公开模型名携带平台和地区，客户端密钥也按目标授权。三种入口复用协议翻译，平台适配器负责各自上游。建议保留这些边界，并保留 transport 与平台调度之间的接口。ClientHello 和本地固定的 HTTP 依赖另有版本与样本维护成本。此次审查未重新做握手采样、真实账号推理或生产负载测试，此前的传输验收范围见现有记录。[目标与命名](../internal/targets/targets.go) · [入口翻译](../internal/server/bridge.go) · [传输配置](../internal/upstream/client_profile.go) · [依赖与样本记录](upstream-req-hpack-alignment.md)

## 最值得逐项参考的项目

| 项目 | 本次查到的实现及适用范围 |
|---|---|
| xiaofan6ya/wb2api 与 jzh-1228/wb2api | 前者实现按密钥排队、队满拒绝与独立超时。后者 README 指向前者，本次取到的队列文件 SHA-256 相同，作为一份实现对照。[主仓队列](https://github.com/xiaofan6ya/wb2api/blob/21276d214667eeb05c703d5d2de0abbea3a0fdaa/admin/capacity_queue.py) · [另一仓快照](https://github.com/jzh-1228/wb2api/blob/ca67f320454a5dff61ba1fdab11ee6cc3639fdc6/admin/capacity_queue.py) |
| shuishuipingan/workbuddy-qoder-trae-hub | 注册了 WorkBuddy、Qoder、Trae 三套引擎。合并路由先用各引擎及地区的模型快照筛选，再按配置与就绪数排序。其显式地区仍允许跨区兜底，本仓库有密钥目标授权，需要遵守已授权的范围。[三引擎注册表](https://github.com/shuishuipingan/workbuddy-qoder-trae-hub/blob/6673845cd7bd2edf96ae8dc1e95efba3b435d022/hub/__init__.py#L25) · [候选生成](https://github.com/shuishuipingan/workbuddy-qoder-trae-hub/blob/6673845cd7bd2edf96ae8dc1e95efba3b435d022/hub/core/router.py#L133) |
| zouzhenglu/modelwharf、caigee-cmd/cli2api、aimod-cc/Agent2API | 三种不同语言都把 provider 身份或能力放在适配边界。Modelwharf 拒绝重复插件 ID。cli2api 把签到等能力作可选能力。Agent2API 的核心通过统一 trait 获取 provider ID 和模型。可以继续使用本仓库的小接口，增加平台时按实际能力扩展。[Modelwharf 注册](https://github.com/zouzhenglu/modelwharf/blob/98115a03a0e20b77d5ac302326542da3180028c8/src/core/registry.js#L34) · [cli2api 能力](https://github.com/caigee-cmd/cli2api/blob/563c9ca05718a1843ce178c9eda643bb962ab39f/internal/providers/interfaces.go#L275) · [Agent2API trait](https://github.com/aimod-cc/agent2api/blob/e8bed7397aaf9d8962489074557c2f1f1a8d4f97/desktop-tauri/src-tauri/server/src/server/core/providers/adapter.rs#L393) |
| CangShui/workbuddy-gateway、Sub2API、CLIProxyAPI | 分别有按模型与账号策略先拒绝的回归、按请求模型复核候选的调度、凭证下独立的模型运行状态。适合交叉检查“拒绝模型、账号冷却、线路故障”的作用范围。[按模型选号回归](https://github.com/CangShui/workbuddy-gateway/blob/5250f894ec051edc28db56f9a9975f0f12209fc1/model_account_test.go#L241) · [Sub2API 调度](https://github.com/Wei-Shaw/sub2api/blob/2af3f1e15c98c893a89255a037f551046883b8c0/backend/internal/service/openai_account_scheduler.go) · [CLIProxyAPI 模型状态](https://github.com/router-for-me/CLIProxyAPI/blob/db7f177d6b0e51eb237daed718de4d25e7a893f5/sdk/cliproxy/auth/types.go#L389) |
| jyao0708/qoder2api、cty1735/wb-proxy、autumnsentiment/Trae2api-cn | 分别提供可单独阅读的 Qoder 协议入口、Qoder 上游队列测试和 Trae 工具调用选路测试。可对照取消、流结束以及所选通道能否执行该请求。[Qoder Bridge](https://github.com/jyao0708/qoder2api/blob/0b17d787742b383b01cf81f2ebcf41c14a4055a0/internal/bridge/handler.go) · [上游队列回归](https://github.com/cty1735/wb-proxy/blob/54c68a65248d1c3d7f2af0ba92ece006f0cf4805/tests/_test_qoder_queue.py) · [Trae 通道回归](https://github.com/autumnsentiment/Trae2api-cn/blob/5c87d6c309499974f5dc786ba2fe22d3c64db1cf/tests/test_auto_route.py#L255) |

## 公开搜索的覆盖范围

从仓库既有两份调研、生态清单和公开网页出发，实际执行了 104 组 GitHub 查询（包含分段），检索仓库名、简介、README、网关主题，以及 QCCG、hy3api、DSH 等别名。GitHub 地址优先按仓库 ID 去重。缺 ID 的历史链接按大小写无关的仓库地址保留。检索与补充线索合并后得到 **16,860 个 GitHub 仓库地址**，其中 5,194 个有 GitHub fork 标记。另列 8 份跨站或产品官方资料。宽泛查询会收进普通 WorkBuddy 技能、无关同名软件和纯部署副本。

- 单独检索 `workbuddy2api in:name fork:true` 时超过 GitHub 单次搜索窗口，按创建日期分段并补查分页重复项。接口报告 4,068 个，实际取得 4,067 个不同仓库 ID，其中 3,906 个标为 fork。宽口径 `workbuddy in:name,description` 同样补做了日期分段，取得 8,335 个 ID。其他宽泛查询仍受窗口与动态索引限制，各检索式的行数、去重数及缺口均单独保留。
- 当前 AutoBot 清单仍有 90 项（含本仓库）。其中 86 个地址本次取得 README，4 个 GitHub 地址未取得。与旧清单相比，WorkBuddy Hub 换了列出的地址，旧 GitHub 地址另有 GitCode 镜像记录。[当前清单快照](https://github.com/misakano7545/workbuddy2api-panel/blob/27b7458f7251942066f912c5677a0f9bc0c3ebdd/AutoBot.yml) · [上一轮清单](competitor-research-inventory.md) · [上一轮横向研究](competitor-research.md)
- 全部线索中，有 **1,974 个 GitHub 地址取得 README**，另对 **14 个仓库的 23 份固定版本源码／测试**做了架构对照。32 个有 README 的重点仓库不在旧的两份调研文档里，其中包括专项适配、宿主插件与通用网关。相同 README 和 fork 上游只作为血缘线索。未逐一核对的分支保留原地址。
- 可筛选的生态索引共有 1,501 行，包含固定源码、重点 README、生态工具、通用网关和待核线索。专项 README 初筛候选有 673 行，由产品名、网关名称、简介或 README 信号选出，尚需核对其中是否只消费网关、是否与已有实现同源。完整候选表同时保留宽泛命中与普通 fork，不能把表的行数当作独立厂商数。

除了表中抽查源码的项目，本次还在旧资料之外查到多种直接相关实现。多平台网关有 yunqiasen/Agent2API 和 xu20060420fan-cell 的扩展面板。宿主插件有 lovingfish/workbuddy-cliproxy 和 chengshenyangtai 的 DSH 插件。Trae 侧还有 JeffHu0912 的 SOLO 网关、laojichao 的本地 IDE 网关，RobbsLuo/Coding2API 同时接 CodeBuddy、TRAE SOLO 和 Qoder。Qoder 侧有 shuishuipingan 的双区账号池、基于 QCCG 的 Zhengyuuuui 服务端版本，以及基于 Qoder CN Agent SDK 的 Qoder-Chat。这些条目已核对 README，项目自述的上游可用性需要独立验收。[四渠道 Agent2API](https://github.com/yunqiasen/Agent2API) · [扩展面板](https://github.com/xu20060420fan-cell/workbuddy2api-trae-qoder-minimax-panel) · [CPA 插件](https://github.com/lovingfish/workbuddy-cliproxy) · [DSH 多平台插件](https://github.com/chengshenyangtai/dsh-workbuddy-trae-qoder-connect) · [TRAE SOLO 网关](https://github.com/JeffHu0912/trae2api) · [Trae 本地网关](https://github.com/laojichao/trae-local-api) · [Coding2API](https://github.com/RobbsLuo/Coding2API) · [Qoder 双区网关](https://github.com/shuishuipingan/qoder2api-hub) · [QCCG 派生服务](https://github.com/Zhengyuuuui/qoder2api) · [Qoder-Chat](https://github.com/40kuai/Qoder-Chat)

通用或托管网关另查了 LLM Gateway、Envoy AI Gateway、Cloudflare AI Gateway、Kong AI Gateway、Azure API Management 等。它们提供 provider 配置、路由、治理或观测的对照，公开产品文档不能验证 WorkBuddy／Qoder 订阅账号、积分或 ClientHello 的行为。[LLM Gateway](https://github.com/theopenco/llmgateway) · [Envoy AI Gateway](https://github.com/envoyproxy/ai-gateway) · [Cloudflare](https://developers.cloudflare.com/ai-gateway/) · [Kong 架构](https://developer.konghq.com/ai-gateway/architecture/) · [Azure](https://learn.microsoft.com/en-us/azure/api-management/genai-gateway-capabilities)

跨站搜索中，GitCode／AtomGit 留有旧 WorkBuddy2API-Hub 的公开镜像，也能查到面板派生分支。Gitee 读到了 aimod.cc/Agent2API 的公开 README。GitLab 的 ZcodeAIModels 是消费现有网关的配置工具。Gitee 原生仓库搜索超时、Codeberg 原生搜索返回 429，无法从这两个端点证明全站覆盖。公开搜索同样无法判断私有仓库、失效旧址和未被索引的项目。[GitCode Hub 镜像](https://gitcode.com/gh_mirrors/wo/workbuddy2api-hub) · [GitCode 面板分支](https://gitcode.com/sherloc29451/workbuddy2api-panel) · [Gitee Agent2API](https://gitee.com/aimodcc/agent2api/blob/main/README.md) · [GitLab ZcodeAIModels](https://gitlab.com/M.Venn/ZcodeAIModels)
