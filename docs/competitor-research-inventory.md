# 同类项目初筛清单

调研日期：2026-11-16。重点分析与官方产品对照见[多平台接入调研](competitor-research.md)。

本清单来自 [misakano7545/workbuddy2api-panel 的 AutoBot.yml](https://github.com/misakano7545/workbuddy2api-panel/blob/74456c87f2eabc02ca1b1d24c5681063c67db9c7/AutoBot.yml)，共 90 项，包含本仓库作为对照。保留原清单顺序，不按热度或质量排名；它不是全部友商的权威目录。

本次 86 个仓库可访问，85 个取得根目录 `README.md`。其中 `pandarick916/workbuddy_checkin` 可访问但没有取得根 README；另 4 个仓库本次不可访问。不可访问不等于已删除或停止维护，缺根 README 也不等于没有代码或其他文档。

“初筛定位”归纳自仓库 README／简介，不代表每一项功能已经实测。表中的快照链接固定初筛时的提交；少数较早获取的仓库未记录提交，明确标出。本仓库使用本轮工作开始时的本地提交作对照。重点源码研究可能稍早于批量初筛，具体以主报告的固定版本表为准。

通用网关、Magpie、Trae2api-cn、Qoder-2API-Go 和其他平台专项项目不都在这 90 项里，已在主报告补充；不能用本表数量代表全部研究对象，也不能将签到工具、宿主插件与独立网关合计为“90 个多平台适配器”。

| 序号 | 仓库 | 初筛定位（仓库自述归纳） | 阅读范围／快照 |
|---|---|---|---|
| 1 | [yreidev/workbuddy2api-panel](https://github.com/yreidev/workbuddy2api-panel/tree/fe61668b58522b1d0274b6b9e63d097f6feedc32) | 本仓库对照：WorkBuddy 多账号网关、三协议与管理面板 | 本仓库源码对照；`fe61668b5852` |
| 2 | [ardeyouxipianyi/workbuddy2api-hub](https://github.com/ardeyouxipianyi/workbuddy2api-hub) | 本次不可访问；不据此判断删除、私有或暂时故障 | 未取得公开内容 |
| 3 | [yuechuya/WorkBuddy2API-Gateway](https://github.com/yuechuya/WorkBuddy2API-Gateway/tree/0a1b3c3c6e604f829f8e66e8480e2e48723fce18) | WorkBuddy 账号池；OpenAI／Anthropic／Responses 多协议 | README 初筛；`0a1b3c3c6e60` |
| 4 | [HUIdada1/AgentHub](https://github.com/HUIdada1/AgentHub/tree/edaff4e0cdb36d3af4fb237514ceb68afa5daa26) | 多平台桌面账号池／本地网关，另含技能与用量管理 | README 初筛；`edaff4e0cdb3` |
| 5 | [3155498108/workbuddy-one](https://github.com/3155498108/workbuddy-one/tree/9ea151d27508f821b665ea90b023d0aaa398249f) | 基于原面板底座，增加 WorkBuddy 桌面一键切号 | README 初筛；`9ea151d27508` |
| 6 | [OHCJ099/BuddyHub](https://github.com/OHCJ099/BuddyHub/tree/d0c6bdf2f11d6da086ce31a5fef9f304973a6212) | Windows 本地账号管理、分区账号池与兼容反代 | README 初筛；`d0c6bdf2f11d` |
| 7 | [hj01857655/WorkBuddy2api](https://github.com/hj01857655/WorkBuddy2api/tree/6e08a1a3abb974a22662e69d140dbad44bf1d8cb) | WorkBuddy 转 OpenAI／Anthropic 的 Go 网关 | README 初筛；`6e08a1a3abb9` |
| 8 | [JackAIStudio/dsh-workbuddy-dual](https://github.com/JackAIStudio/dsh-workbuddy-dual/tree/47579e53c8d2d9164fe2f3a335df9d854b400884) | DSH 插件：WorkBuddy 国内／国际，复用桌面登录 | README 初筛；`47579e53c8d2` |
| 9 | [smart-open/TraeWorkAssistant](https://github.com/smart-open/TraeWorkAssistant/tree/a50d65f7a2f0d3dd3b0b8ae4179199276bd93466) | 多平台桌面账号、设备隔离、签到、额度与网关 | README 初筛；`a50d65f7a2f0` |
| 10 | [deltrivx/AutoBuddy](https://github.com/deltrivx/AutoBuddy/tree/a5e5eb30b6be9e2669ba333c5b068097010ec9de) | WorkBuddy／CodeBuddy 切号、签到保活与网关 | README 初筛；`a5e5eb30b6be` |
| 11 | [techysy/CreditDaddy](https://github.com/techysy/CreditDaddy/tree/1f86546ce0842f4aa53060df1ff9a03395205d0d) | 多平台账号与签到；网关及用量接入 10Router | README 初筛；`1f86546ce084` |
| 12 | [spdw666/multi-agent-checkin-dsh](https://github.com/spdw666/multi-agent-checkin-dsh/tree/ef045be0c1085f43327e8f9ac6736e80d69d7c7b) | 多平台签到、WorkBuddy 账号池与 DSH 网关／桌面界面 | README 初筛；`ef045be0c108` |
| 13 | [1557335006/workbuddy-checkin-tool](https://github.com/1557335006/workbuddy-checkin-tool/tree/1bf2dde10a3fdc1237128ba634c15797beb6a809) | WorkBuddy 签到与成长任务 Windows 工具 | README 初筛；`1bf2dde10a3f` |
| 14 | [Yhx888/dsh-free-egg-credits](https://github.com/Yhx888/dsh-free-egg-credits/tree/094c42b69706eadde93d62425c7e651d8990fae7) | DSH 积分侧栏，关联 WorkBuddy／Qoder 现有网关 | README 初筛；`094c42b69706` |
| 15 | [Costben/workbuddy2api](https://github.com/Costben/workbuddy2api/tree/58482149fea459548d4c3ac4d58b9909f6ab5d18) | CodeBuddy 多账号 OpenAI 网关；README 明确不内嵌面板 | README 初筛；`58482149fea4` |
| 16 | [zr987459/workbuddy-app](https://github.com/zr987459/workbuddy-app/tree/e028d61f9f29af80cfd79a7487ef1480894c730e) | WorkBuddy 跨平台应用，国际版模型与双协议中控 | README 初筛；`e028d61f9f29` |
| 17 | [bhh3349/workbuddy2api-prime](https://github.com/bhh3349/workbuddy2api-prime/tree/5dd223eede296af87eff1f26088288a9009ba4fa) | Go 网关整合选号、限额与台账；README 仍列流式等 TODO | README 初筛；`5dd223eede29` |
| 18 | [dhdbvcg/dsh-workbuddy-console](https://github.com/dhdbvcg/dsh-workbuddy-console/tree/8762d443608b86dccb8beb7a2fd36e2f9ca96721) | DSH 内 WorkBuddy 多账号签到、任务、会话状态与历史 | README 初筛；`8762d443608b` |
| 19 | [chengzhi-c/pi-desktop-wtq-pool](https://github.com/chengzhi-c/pi-desktop-wtq-pool/tree/bac0c9420c65953731b65c2ebbfb3cae086576bf) | PI-Desktop 宿主：WorkBuddy／Trae／Qoder 本地账号桥接 | README 初筛；`bac0c9420c65` |
| 20 | [chengzhi-c/dsh-wtq-pool](https://github.com/chengzhi-c/dsh-wtq-pool/tree/bc002f0a9c67db4576b397331232adace6cf7b29) | DSH 宿主：WorkBuddy／Trae／Qoder 本地账号桥接 | README 初筛；`bc002f0a9c67` |
| 21 | [liberation8/hermes-workbuddy-provider](https://github.com/liberation8/hermes-workbuddy-provider/tree/8472b8c923fceee9b33fabefabb86b2f49363df7) | 面向 Hermes／SDK 的 WorkBuddy 本地兼容反代 | README 初筛；`8472b8c923fc` |
| 22 | [Misaka10091/workbuddy2api-panel](https://github.com/Misaka10091/workbuddy2api-panel/tree/d24360cd1c27993eaed65bf493cc5486dcb295aa) | WorkBuddy 网关增强分支：管理面板、账号池与成长任务 | README 初筛；`d24360cd1c27` |
| 23 | [xibai6668/Android-AI-gateway](https://github.com/xibai6668/Android-AI-gateway/tree/f7413b098091f7f9db1316d77b9b6dbb02662d41) | Android 自托管多平台兼容网关 | README 初筛；`f7413b098091` |
| 24 | [pandarick916/workbuddy_checkin](https://github.com/pandarick916/workbuddy_checkin/tree/c3710ee227f447744d6f992bc9c596df88dd326e) | 仓库可访问；未取得根目录 README.md，不推断实现 | 仅元信息，无根 README；`c3710ee227f4` |
| 25 | [2861292267/DSH-Official-WorkBuddy-Credit-Proxy](https://github.com/2861292267/DSH-Official-WorkBuddy-Credit-Proxy/tree/17a02c085af35ab08ceb3075b003a981afa76d5c) | 面向 DSH 的本地账号同步、积分池与故障切换 | README 初筛；`17a02c085af3` |
| 26 | [ithtelab/workbuddy-manager](https://github.com/ithtelab/workbuddy-manager) | CodeBuddy 账号池、网关、密钥分组、白名单与用量 | README 初筛；未固定提交 |
| 27 | [caigee-cmd/cli2api](https://github.com/caigee-cmd/cli2api/tree/5224c100c6412d08ce56895e68ddadd6d7979e5d) | 多平台网关；provider／region、grants 与可选能力另读源码 | README 初筛；`5224c100c641` |
| 28 | [FireOpalus/dsh-workbuddy2api](https://github.com/FireOpalus/dsh-workbuddy2api) | 本次不可访问；不据此判断删除、私有或暂时故障 | 未取得公开内容 |
| 29 | [NextAgentX/qoder-trae-workbuddy-switch](https://github.com/NextAgentX/qoder-trae-workbuddy-switch/tree/bfe250c552d6284b5fdbea722e0bda8990cf1fa7) | WorkBuddy／Trae／Qoder 国内国际桌面切号与转 API | README 初筛；`bfe250c552d6` |
| 30 | [sofs2005/w2a](https://github.com/sofs2005/w2a) | WorkBuddy 国内兼容反代、OAuth、多号、工具与流式 | README 初筛；未固定提交 |
| 31 | [dingminhua/dsh-connect-workbuddy](https://github.com/dingminhua/dsh-connect-workbuddy/tree/b421c8af978001c28f6e0c982245a5625a1a4ce3) | DSH 复用 WorkBuddy 登录，模型与只读额度展示 | README 初筛；`b421c8af9780` |
| 32 | [newtypepan12-svg/workbuddy-qiandao](https://github.com/newtypepan12-svg/workbuddy-qiandao/tree/8dd30363393eac866e87b8ec016cb23300b0f22d) | GitHub Actions 定时签到与 Pages 统计 | README 初筛；`8dd30363393e` |
| 33 | [pkqzzt/workbuddy2api-panel-plus](https://github.com/pkqzzt/workbuddy2api-panel-plus/tree/bf8f3686d7f1c3030392d6cc9688c12d63b8f0e3) | 面板衍生：多 key、模型白名单、出口绑定与逐 key 台账 | README 初筛；`bf8f3686d7f1` |
| 34 | [rcwowhoo/workbuddy-checkin](https://github.com/rcwowhoo/workbuddy-checkin/tree/229582a086a4e0be0779a7c423eb5447494cf43e) | GitHub Actions 多账号签到、令牌文件与结果汇总推送 | README 初筛；`229582a086a4` |
| 35 | [lehuaner/SSHMonitor](https://github.com/lehuaner/SSHMonitor/tree/27e18b202380e11ba2edc0f3521cc3a943021133) | Android／Termux 监控及多平台签到、凭证巡检 | README 初筛；`27e18b202380` |
| 36 | [Faide-cyber/dsh-workbuddy](https://github.com/Faide-cyber/dsh-workbuddy/tree/b3db340b5e92d8810420efe1208b352e8236acc0) | DSH 插件：WorkBuddy 国内／国际模型 | README 初筛；`b3db340b5e92` |
| 37 | [wefewe/workbuddy2api](https://github.com/wefewe/workbuddy2api/tree/e9356a0634b874611bafa2700524a0ae6eba5ca7) | workbuddy-manager 代码打包与多架构镜像 CI | README 初筛；`e9356a0634b8` |
| 38 | [ChaYeWuu/dsh-hyperworkbuddy](https://github.com/ChaYeWuu/dsh-hyperworkbuddy/tree/a404aca7c68919e52d95b749d85926c9fc9a05bb) | DSH 内 WorkBuddy 多账号管理及兼容端点 | README 初筛；`a404aca7c689` |
| 39 | [wicm84266964/Buddy2api](https://github.com/wicm84266964/Buddy2api/tree/84cc9de2d3805fdb1ab01fc2772680ccd46877c0) | 多通道统一网关；显式通道／模型与自动选择 | README 初筛；`84cc9de2d380` |
| 40 | [HXX-ita/workbuddy-checkin](https://github.com/HXX-ita/workbuddy-checkin/tree/99faa082486f7de32c777084670a13e5b6bd088d) | WorkBuddy 每日签到与 Windows 定时任务 | README 初筛；`99faa082486f` |
| 41 | [thintsing/agent-checkin](https://github.com/thintsing/agent-checkin/tree/e3cedac65ed8c7a9e9420238060fe133ce2d3e0a) | 配方与 CDP 驱动的多平台签到 | README 初筛；`e3cedac65ed8` |
| 42 | [stevenwang288/WorkBuddy-Daily](https://github.com/stevenwang288/WorkBuddy-Daily/tree/0b35fd0ebfde23a6c6cfe17e02d336adbe30d87d) | WorkBuddy 成长签到与定时运行 | README 初筛；`0b35fd0ebfde` |
| 43 | [study-yang/workbuddy2codex](https://github.com/study-yang/workbuddy2codex/tree/702c6d51b408cc4a4b406f9c5ebc1570c0df32d7) | WorkBuddy 接 Codex／Claude Code 与逐对话积分账本 | README 初筛；`702c6d51b408` |
| 44 | [gray0128/pi-workbuddy-connect](https://github.com/gray0128/pi-workbuddy-connect/tree/15fdabcdfd05af48755056de2b69cdc460e82499) | Pi 扩展：复用桌面登录，国内国际独立 provider，直连上游 | README 初筛；`15fdabcdfd05` |
| 45 | [autumnsentiment/workbuddy-helper](https://github.com/autumnsentiment/workbuddy-helper/tree/11808853e53ae195a329182f5655baa1783de64c) | WorkBuddy 多账号签到与积分看板 | README 初筛；`11808853e53a` |
| 46 | [GeekFong/traework-checkin-assistant](https://github.com/GeekFong/traework-checkin-assistant/tree/8ac6ebe2d029a9bbd855b2de84e9482b386bb1f8) | TraeWork／WorkBuddy 签到、定时运行与消息推送 | README 初筛；`8ac6ebe2d029` |
| 47 | [jerry9692/dsh-workbuddy2api-panel](https://github.com/jerry9692/dsh-workbuddy2api-panel/tree/b2d669f5a4937873147cdab4bc57068c10687448) | DSH 插件：同步网关模型、倍率与额度用量 | README 初筛；`b2d669f5a493` |
| 48 | [babygoton/WorkDaddy](https://github.com/babygoton/WorkDaddy/tree/638a01a860ebb811444ab0a0b9ab0d2cecd6290e) | WorkBuddy 桌面增强：签到、切号、会话迁移与任务辅助 | README 初筛；`638a01a860eb` |
| 49 | [xbmaxx/cli2openai](https://github.com/xbmaxx/cli2openai/tree/3eaffd55cbfba829de5ff1105e348e72b574dcf6) | 通过 CLI 驱动编程 Agent 的 OpenAI 协议桥接 | README 初筛；`3eaffd55cbfb` |
| 50 | [csszyft/wb2api](https://github.com/csszyft/wb2api/tree/2f01b24be079f9ff391c9388eb85a6f67d908472) | WorkBuddy 网关及面板的容器／原生程序部署与运维 | README 初筛；`2f01b24be079` |
| 51 | [PBY-CMYK/workbuddy-daily](https://github.com/PBY-CMYK/workbuddy-daily/tree/458dcd464825045e7a9ea475b8ec1e918f50aa63) | WorkBuddy 签到、猫猫旅行与云端台账 | README 初筛；`458dcd464825` |
| 52 | [interli3/workbuddy-cloud-worker](https://github.com/interli3/workbuddy-cloud-worker/tree/a7543b03a32d97e93b84a1378761c9ed8d107ca1) | 云端 WorkBuddy 签到、成长、旅行与令牌续期 | README 初筛；`a7543b03a32d` |
| 53 | [lxpkwwz/ql_scripts](https://github.com/lxpkwwz/ql_scripts/tree/9f55bcc96e75f5fb5efe59678a0d985184a406d1) | 含 Trae／WorkBuddy 等签到的综合脚本集合 | README 初筛；`9f55bcc96e75` |
| 54 | [luoxh8/workbuddy-connect](https://github.com/luoxh8/workbuddy-connect/tree/3213c35ebc9baea4fc74c207d6c4c9f06743e527) | 本机 WorkBuddy 国内／国际账号接 ZCode 的桥接插件 | README 初筛；`3213c35ebc9b` |
| 55 | [maiphucgiang/codebuddy2api](https://github.com/maiphucgiang/codebuddy2api/tree/58fc829d701150b8d323875cb1a7a3d5eab8d710) | CodeBuddy 订阅转本地 OpenAI 接口 | README 初筛；`58fc829d7011` |
| 56 | [a1007479270/aidaka](https://github.com/a1007479270/aidaka/tree/7bf15f9330e7ce7114aa519880b4bc71b11c4275) | ima／WorkBuddy／Qoder／TraeWork 四平台签到 | README 初筛；`7bf15f9330e7` |
| 57 | [fancylear/dsh-workbuddy-credits](https://github.com/fancylear/dsh-workbuddy-credits/tree/efa3698b4c716d9263f238387f0a8357bbf3ccdb) | DSH 内 WorkBuddy 积分侧栏展示 | README 初筛；`efa3698b4c71` |
| 58 | [Libra1337/Reso2api](https://github.com/Libra1337/Reso2api/tree/ac37e45635610e60199502c9153e9787d365ebcb) | WorkBuddy／TraeWork／Qoder，Go 网关与嵌入控制台 | README 初筛；`ac37e4563561` |
| 59 | [jin66god/workbuddy2api-panel](https://github.com/jin66god/workbuddy2api-panel/tree/dfae08aca72a21c055e6431b936cf352fe37a391) | WorkBuddy 网关／管理面板与成长任务衍生项目 | README 初筛；`dfae08aca72a` |
| 60 | [HanawaBanana/workbuddy2api](https://github.com/HanawaBanana/workbuddy2api/tree/af5e77947c93fd6b7f8b3815ee047d64e0b97752) | 原 WorkBuddy2API 的延续仓库与账号池网关 | README 初筛；`af5e77947c93` |
| 61 | [CangShui/workbuddy-gateway](https://github.com/CangShui/workbuddy-gateway/tree/e142dd7640fc5e87541a3e5a51d3ae2e52c4317e) | Go CodeBuddy 账号池 CLI／Web 控制台与兼容网关 | README 初筛；`e142dd7640fc` |
| 62 | [Practice019/multi2api](https://github.com/Practice019/multi2api/tree/c69a3e70b5b711a9ed28fbff6290bb2897fc8089) | CodeBuddy／WorkBuddy 反代、账号池、熔断与会话粘性 | README 初筛；`c69a3e70b5b7` |
| 63 | [zhangdailin/API-Console](https://github.com/zhangdailin/API-Console/tree/87220fadd4786301a9b6f22d38f69e293d4b262e) | 多平台 Go 网关；简介平台名单与现行源码有差异，见主报告 | README 初筛；`87220fadd478` |
| 64 | [aimod-cc/agent2api](https://github.com/aimod-cc/agent2api/tree/e8bed7397aaf9d8962489074557c2f1f1a8d4f97) | 多客户端登录复用、统一路由网关与扩展适配 | README 初筛；`e8bed7397aaf` |
| 65 | [orangeboyChen/codebuddy2api](https://github.com/orangeboyChen/codebuddy2api/tree/6fd34a35bcf669402875bc97e6c2c9509ec9aa01) | CodeBuddy 的 OpenAI／Anthropic 协议代理 | README 初筛；`6fd34a35bcf6` |
| 66 | [Zhengyuuuui/workbuddy2api](https://github.com/Zhengyuuuui/workbuddy2api/tree/b54efcbd297e1af27282d86205326187b96b32d6) | WorkBuddy 网关、限流与三协议客户端适配 | README 初筛；`b54efcbd297e` |
| 67 | [cty1735/wb-proxy](https://github.com/cty1735/wb-proxy/tree/9ada3e85f677b4dd8b4942989493b6507daf5f8f) | WorkBuddy／Qoder／Trae，多账号、用量与限额 | README 初筛；`9ada3e85f677` |
| 68 | [mmqz/cpa-multi-plugins](https://github.com/mmqz/cpa-multi-plugins) | 本次不可访问；不据此判断删除、私有或暂时故障 | 未取得公开内容 |
| 69 | [changexbc/workbuddy-switch](https://github.com/changexbc/workbuddy-switch/tree/22b318c1f44b335c2222e444abcc2d0f74e160f7) | WorkBuddy 切号、积分到期与用量展示 | README 初筛；`22b318c1f44b` |
| 70 | [88lin/workbuddy-auto-signin](https://github.com/88lin/workbuddy-auto-signin/tree/4ee159f0e4d1472f51135b9d72519a191c88fdec) | WorkBuddy／Qoder 国内签到与成长奖励 | README 初筛；`4ee159f0e4d1` |
| 71 | [baiyea/workbuddy2api-ui](https://github.com/baiyea/workbuddy2api-ui/tree/483c6a21919a27598a216f536b52e950f358fdd3) | WorkBuddy 双协议、国际版、自动签到与多号轮转 | README 初筛；`483c6a21919a` |
| 72 | [L0NE-6/WorkBuddy-Daily](https://github.com/L0NE-6/WorkBuddy-Daily/tree/6f7d65e1f25bc1899e7c8a42740ebb222c3f67a8) | WorkBuddy 成长签到、进度回读与自动补领 | README 初筛；`6f7d65e1f25b` |
| 73 | [hawklithm/workbuddy2api](https://github.com/hawklithm/workbuddy2api/tree/d5847723dc95474e2ab56e2aadb2bd1f32cc30ca) | CodeBuddy 国内／国际反代与三协议兼容 | README 初筛；`d5847723dc95` |
| 74 | [DistPub/workbuddy2api](https://github.com/DistPub/workbuddy2api/tree/1fb14000b1cfe46ea6408e84809179e09e7393bc) | 本地／局域网 WorkBuddy 网关、独立 key 与配额台账 | README 初筛；`1fb14000b1cf` |
| 75 | [xu20060420fan-cell/workbuddy2api-panel](https://github.com/xu20060420fan-cell/workbuddy2api-panel) | 本次不可访问；不据此判断删除、私有或暂时故障 | 未取得公开内容 |
| 76 | [jiangwanying/wb2api-panel-plus](https://github.com/jiangwanying/wb2api-panel-plus/tree/cb2ca4d94fd4f612aa5bc2667d7498fb6a0a0c82) | WorkBuddy 三协议多账号网关、管理面板与成长任务 | README 初筛；`cb2ca4d94fd4` |
| 77 | [Ashfmit/workbuddy-scheduled-tasks](https://github.com/Ashfmit/workbuddy-scheduled-tasks/tree/700074dc198004bb2d0c0035345d46e8180a0c6d) | 通过客户端 WBIPC／CDP 执行签到与奖励任务 | README 初筛；`700074dc1980` |
| 78 | [9Ashwin/flow-one](https://github.com/9Ashwin/flow-one/tree/6698a39817a3caba72e82e5f281a5af16e990918) | WorkBuddy／OpenCode 接 Claude Code 的本地反代与续期 | README 初筛；`6698a39817a3` |
| 79 | [zhuweiyou/codearts2api](https://github.com/zhuweiyou/codearts2api/tree/541a121747b21672e7ffdc038565cc19ada7e95a) | 独立网关宿主；多平台实现来自装载的 provider 插件 | README 初筛；`541a121747b2` |
| 80 | [kevinzgai/wd-hub](https://github.com/kevinzgai/wd-hub/tree/4e858334e6129f6aeeef2df77475eeed461788d9) | ESP32-S3 蓝牙硬件状态看板；非 HTTP 网关或推理适配器 | README 初筛；`4e858334e612` |
| 81 | [HimA6652/dsh-workbuddy2api-hub](https://github.com/HimA6652/dsh-workbuddy2api-hub/tree/38d7fcba584f823350744f06372c96462821e0ef) | DSH 对接已有 workbuddy2api-hub，模型路由与额度徽标 | README 初筛；`38d7fcba584f` |
| 82 | [yang9582/workbuddy-connect](https://github.com/yang9582/workbuddy-connect/tree/e0c5c8ef89bb11e5a1f231bc0a939c2e22fb35a2) | DSH 桌面登录桥接、积分条与模型菜单 | README 初筛；`e0c5c8ef89bb` |
| 83 | [Ianzhyh/workbuddy-to-dsh](https://github.com/Ianzhyh/workbuddy-to-dsh/tree/34c43d84212c0193a344199f8f5535b831c0dee7) | 本地 WorkBuddy 登录转兼容接口，面向 DSH 等客户端 | README 初筛；`34c43d84212c` |
| 84 | [EchoPing07/Buddy-2API-Go](https://github.com/EchoPing07/Buddy-2API-Go/tree/9591b7e06f2ed483e96af7e34239559b9658212b) | WorkBuddy／CodeBuddy 转 OpenAI 的 Go 项目 | README 初筛；`9591b7e06f2e` |
| 85 | [bluechonk/modelmux](https://github.com/bluechonk/modelmux/tree/8e3bf47a219ab1fc240f551c2c6cc9850ee8f5be) | 少数公开模型族聚合多渠道，按额度择路 | README 初筛；`8e3bf47a219a` |
| 86 | [RapidAI/RapidProxy](https://github.com/RapidAI/RapidProxy/tree/eed5b7be7c417ac458c5d8699daab93c60225a6f) | WorkBuddy 简易代理 | README 初筛；`eed5b7be7c41` |
| 87 | [wbhubai/wb-desktop](https://github.com/wbhubai/wb-desktop/tree/62b7b7bd9162a91cd77356721b9c386cb431405c) | WorkBuddy 桌面账号助手、签到、旅行与本地网关 | README 初筛；`62b7b7bd9162` |
| 88 | [HonorC05/workbuddy-auto-checkin](https://github.com/HonorC05/workbuddy-auto-checkin/tree/26b5ac9f218bd2074607ba8bb985c7be81f28efb) | WorkBuddy 无人值守签到与成长任务 | README 初筛；`26b5ac9f218b` |
| 89 | [onlyveyou/wb-checkin](https://github.com/onlyveyou/wb-checkin/tree/05de7c00beef6450ff2be4e9d6a62bdfcced229b) | macOS 多账号签到、积分趋势、切号与状态监控 | README 初筛；`05de7c00beef` |
| 90 | [2439810550/buddy-deck](https://github.com/2439810550/buddy-deck/tree/120ca31e63b3b07baa86c4cf3a2793094ae1a0b8) | 桌面账号转本地网关，多号、签到、续期与多协议 | README 初筛；`120ca31e63b3` |
