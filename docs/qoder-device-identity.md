# Qoder 设备身份

从未运行过官方 Qoder 客户端时，没有需要迁移的「原设备 UMID」。网关在实际服务环境第一次处理 Qoder 授权、导入或账号请求时，调用官方组件生成设备身份，保存后再发送业务请求。账号原有的 MachineID、DeviceID 和授权令牌不会因初始化而更换。

## 官方依据与组件取得

依据为官方 `@qodercn-ai/qoderclicn` 1.1.96（与 1.1.89 的 UMID helper ELF 完全一致）的 UMID 服务、hostname 规范化函数及 Linux helper。网关从[固定官方 npm 包](https://registry.npmjs.org/@qodercn-ai/qoderclicn/-/qoderclicn-1.1.96.tgz)中读取 `package/bundle/qoderclicn.js`，只提取与固定 SHA-256 相符的 ELF 常量，不执行 CLI、npm 安装脚本或授权命令。组件没有随本仓库和镜像分发；缓存存在且校验通过时不再下载。

| 架构 | helper SHA-256 | 运行验收 |
|---|---|---|
| Linux x64 | `e30b307eaae12680d2f4e220d4d34b0ea4f70355a624d6d55ee916e03f7d7478` | Alpine 3.20 镜像（`gcompat` + `libstdc++` + `proxychains4`）直连与代理双地区（国内／国际）均已实测返回有效 UMID |
| Linux ARM64 | `2df9d2c5aa44cb6c45ad27f493bf3dca2aaf1e067fa0ad6e88c6cad0153329fe` | QEMU 用户态 + glibc 双地区已实测 `ec=200`；Dockerfile `linux/arm64` Go 交叉编译（`EM_AARCH64`）、Alpine 3.20 `aarch64` 8 个运行依赖包及 `gcompat`+`musl` 符号表均已核验与 x64 对称；Alpine 3.20 ARM64 实际运行及原生 ARM64 物理机尚未实测（未确认） |

自动初始化目前限 Linux x64／ARM64。其他系统继续导入官方设备身份；不生成替代硬件值。独立二进制部署需有 glibc 及 `libstdc++`；Alpine 需另装 `gcompat libstdc++`。需要经代理初始化或刷新时，还需 `proxychains-ng` 提供的 `proxychains4`。多架构 Docker 镜像（`linux/amd64`、`linux/arm64`）已内置上述依赖，其中 `linux/amd64` 已完整构建并实测服务 `/readyz` 与双地区直连／`proxychains4` helper，`linux/arm64` 已验证 Go 交叉编译阶段、Alpine 3.20 `aarch64` 依赖包及 `gcompat` ABI 对称性，但 Alpine 3.20 ARM64 容器内实际运行官方 helper 及原生 ARM64 硬件尚未实测（未确认），若遇不兼容可改为导入官方设备身份。

## 生命周期与保存位置

默认容器配置下，以下路径均相对于 `providers.json` 所在的 `data/` 目录：

| 路径 | 内容 |
|---|---|
| `qoder-umid/.bin/runtime-info-linux-<arch>-<摘要前16位>` | 官方组件，运行前校验完整 SHA-256，权限 `0500` |
| `qoder-umid/environment-0.json` | 国内环境缓存，权限 `0600` |
| `qoder-umid/environment-4.json` | 国际 SINGAPORE 环境缓存，权限 `0600` |
| `qoder-umid/sdk/` | 官方 SDK 自己的状态，目录权限 `0700`；容器里的用户 `.config` 指向这里 |
| `providers.json` 中的 credential | 生效的 `machine_token`、`machine_code`、`machine_type`、`machine_os`、`machine_hostname`，以及本机生命周期标记 `machine_local` |

Linux 调用仅传环境数字：国内 `0`，国际 `4`；不传账号或授权令牌。组件文件名与官方 `prepareBinary` 一致，摘要取 ELF 的 SHA-256 前 16 位。读取首行设备 JSON，输出上限 1 MiB、执行超时 25 秒。并发初始化／刷新合并，国内与国际缓存分别校验环境、系统及架构。到期后的下一次账号操作按一小时 ± 五分钟刷新，不启动后台定时器或常驻子进程。取消或超时后按已启动 PID 及核验过启动时间的后代 PID 回收，不按名字或进程组结束其他进程。

缓存和账号文件均原子替换并同步落盘。首次失败不伪造 UMID，不继续消费一次性刷新令牌；一分钟后可重试。刷新失败或写盘失败有日志，并保留已经保存的有效旧快照。缓存格式损坏或环境不匹配会明确报错，不自动清空设备资料。

已有原客户端设备身份时继续使用导入值，不自动覆盖。本机生成的身份通过 `machine_local` 标记参与刷新；普通账号列表不返回设备字段。管理员导出包含设备字段，但去掉本机管理标记，使迁入另一实例时保留原身份。重新导入授权而未指定设备字段时，沿用已有 MachineID、DeviceID 和 UMID。

hostname 按实际运行环境规范化：安全 ASCII 直接保留，域名转 IDNA，无法转换的字符替换并附 SHA-256 前八位；最长 96 字节。不会复制采样主机的 hostname 或机器码。

## 代理与容器数据

实际跟踪确认官方 Linux helper 忽略 `HTTP_PROXY`、`HTTPS_PROXY`，并直接发起自己的网络连接。账号绑定代理优先于默认出站代理；网关通过 `proxychains-ng` 适配 helper 的 DNS／connect，再经单次调用期间的本机 CONNECT 桥转发到所选代理，下载缺失组件时也走同一线路。支持 HTTP、HTTPS、SOCKS5、SOCKS5h 代理；代理解析、认证、连接或下载失败均不回落直连。

本机桥只监听回环随机端口，使用随机 Basic 认证，只允许国内 `pum.m.alibaba.com:443` 和国际 `sg-pum.alibabachengdun.com:443`，透明转发官方字节。临时配置权限 `0600`，只包含本机桥的临时认证，不保存上游代理密码；结束后关闭连接并删除配置。缺少 `proxychains4` 时不启动 helper。必须经桥完成一次带响应的网络交互才采用本次输出，防止把组件的离线回退身份当作初始化成功；刷新失败继续保留已落盘的旧快照。若只有 SDK 状态而没有有效 JSON 快照，首次调用没有网络交互时仍会报错。

官方 SDK 通过 `getpwuid(getuid())` 读取用户目录的 `.config/.locale_cfg`，不采用 `XDG_CONFIG_HOME` 也不读取 `HOME` 环境变量，且不按国内／国际区分文件名。镜像与 Linux 一键安装脚本（`scripts/install-linux.sh`，新建系统用户主目录指向安装目录）均通过 `.config` 符号链接把 SDK 状态放入 `data/qoder-umid/sdk`，兼容默认用户、root 及 Docker 自定义 UID；网关在 `data/qoder-umid/sdk/` 内按环境拆分为 `.locale_cfg.env-0` 与 `.locale_cfg.env-4` 独立持久化并在单次运行期间切换，防止先初始化的地区被另一地区误复用而跳过外网交互（旧版共享 `.locale_cfg` 会自动无损迁移）。备份、迁移、重建容器或重装服务时保留整个 `data/`，不能只保存 `providers.json` 或只复制 JSON 设备缓存。手动运行的独立二进制还需保留运行用户原有的 `.config/.locale_cfg`。

组件及环境缓存始终跟随 `providers.json` 的目录；镜像中的 SDK 链接固定指向 `/app/data/qoder-umid/sdk`。自定义数据布局时也要持久化这个链接目标，不能只迁走账号文件所在目录。

首次运行需要访问官方包源及官方 UMID 服务。2026-11-17 对 x64／ARM64、国内／国际的网络采样显示：helper 向上述 443 端口发送明文 `POST /repPc.json?... HTTP/1.0`，服务返回 HTTP 200、`ec=200`，没有 TLS ClientHello。包内虽包含 HTTPS URL，不能据此认定实际使用 TLS；本记录更正早先“组件自己承担 TLS”的说法。桥保留其原始传输，网关业务请求仍使用独立的 Qoder Bun TLS profile。成功生成 UMID 仅说明设备生命周期正常，不表示账号额度、推理权限或所有网络特征均已验收；上游业务错误继续保留原状态码。

## 实际验收记录

2026-11-17（台湾时间），在保留原数据的 dev 容器完成：

- 国内 `0`、国际 `4` 分别运行官方组件初始化，返回有效 token／type／code，账号生效字段与环境缓存一致，缓存权限 `0600`，SDK 状态非空。
- 替换容器后重新读取模型目录，账号文件、两个环境缓存及 SDK 状态的摘要均保持一致，没有重新生成身份。
- 停机备份后，仅提前两个 UMID 缓存的刷新时间，再创建容器并发起账号操作；两地区均调用官方组件完成刷新、更新缓存。没有修改账号到期时间或消费账号刷新令牌，原 MachineID、DeviceID、私钥、client ID 及授权令牌摘要均保持一致。
- 运行结束后容器仅保留网关主程序和 init，没有遗留 helper 子进程。长期 dev 按使用者要求保留。
- 同轮真实业务回归：TraeWork `deepseek-v4.1-flash`、Trae Code `Doubao-Seed-Code` 正常流式完成；Qoder 国内模型目录正常，但 `auto` 仍返回 `10605 / RESOURCE_LIMIT`；Qoder 国际账号已经存在，模型目录仍为空。后两项仍待处理，不能据 UMID 成功标记推理已验收。

同日补采及代理验收：

- 官方 x64 与 QEMU 用户态 ARM64 helper，在各自独立 SDK 状态目录分别完成国内／国际网络交互，首行 JSON 有效，四组均观察到 HTTP/1.0 明文及 `ec=200`。没有修改系统 binfmt 或安装全局模拟环境；ARM64 原生硬件仍未验收。
- Alpine 3.20 的 `proxychains-ng 4.17-r1` 配合当前源码的执行与 CONNECT 桥，在 `--network none` 容器中分别完成国内／国际初始化。外部受控隧道记录官方服务 HTTP 200、`ec=200`；容器无法直接访问外网。诊断副本保留源文件摘要。此项实际选用 HTTP 代理，其他所选代理协议由离线拨号回归覆盖。
- 离线回归覆盖首次初始化、并发合并、地区隔离、重启恢复、刷新失败／写盘失败保留旧快照、绑定代理优先、组件下载使用同一代理、失败不直连、拒绝离线回退结果、桥认证与目标限制、导入身份保留、令牌刷新单飞、取消／超时回收、组件摘要及输出校验。

本轮根模块普通全量、providers／upstream 完整包 race、build、vet、lint、Linux ARM64 交叉编译和格式检查通过。已保数据更新 dev，镜像包含 `proxychains4`，容器仅有 init 和网关常驻。真实 Work／Code 流式回归通过，当前 token、设备字段、账号设置、配置和原 auth 文件保留；没有回填旧备份。Qoder 国内生成与国际空目录仍按前述边界保留，不因 UMID 代理验收而标成通过。

2026-11-18 23:07（台湾时间），用当前源码构建的 Linux x64 探针、校验过的官方 helper 和 dev 镜像，在没有直连路由的隔离容器完成四种代理的双地区复验。HTTP CONNECT、HTTPS CONNECT、SOCKS5、SOCKS5H 均要求认证，每轮使用新 SDK 状态，不携带账号凭证。共八组均取得有效设备字段，代理侧确认官方 HTTP/1.0 请求、HTTP 200 和 `ec=200`。首次夹具的临时数据卷禁止执行 helper，任何隧道均未建立，修正挂载选项后八组通过。

随后清空四组目标缓存，从相同隔离环境分别经四种代理下载固定官方 npm 包。均确认与 npm 包源建立 TLS、提取成功且 Linux x64 helper 的 SHA-256 与固定值相同。隔离容器已退出，原生 ARM64 硬件仍待运行验收。

2026-11-24（本地多架构构建与 Alpine 3.20 复验）：基于多架构 `Dockerfile` 完整构建 `linux/amd64` 镜像并实测 `wb2api` 启动 `/readyz` 返回 HTTP 200，容器内以 `uid=10001` 验证官方 x64 helper 直连与 `proxychains4` 认证 CONNECT 隧道双地区（`pum.m.alibaba.com:443`、`sg-pum.alibabachengdun.com:443`）均退出码 0 并产出有效 JSON；同轮实测 `Dockerfile` `linux/arm64` 交叉编译阶段（命中双架构缓存共享，4 个二进制均为 `EM_AARCH64`）、Alpine 3.20 `aarch64` 仓库全部 8 个运行依赖包及 `helper-arm64` 与 `gcompat 1.1.0-r4` + `musl 1.2.5` 的 ELF 符号表对称核对均通过。同时实测发现官方 helper 通过 `getpwuid` 读写固定 `.config/.locale_cfg` 且不区分地区，已改为在 `data/qoder-umid/sdk/` 内按 `.locale_cfg.env-0`／`.locale_cfg.env-4` 隔离并支持旧版无损迁移。因本机无 `qemu-aarch64` binfmt 及 ARM64 硬件，Alpine 3.20 `linux/arm64` 实际运行与原生 ARM64 物理机继续保持未确认。
