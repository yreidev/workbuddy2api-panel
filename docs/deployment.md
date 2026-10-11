# 部署与运维

本篇覆盖四种部署方式、添加账号、升级与备份，以及随仓库附带的工具脚本。只想先跑起来的话，看 README 的[快速开始](../README.md#快速开始)就够了。

- [部署方式一览](#部署方式一览)
- [Docker 镜像（推荐）](#docker-镜像推荐)
- [Docker Compose](#docker-compose)
- [单文件二进制](#单文件二进制)
- [从源码构建](#从源码构建)
- [添加账号](#添加账号)
- [升级、备份与停机](#升级备份与停机)
- [工具脚本](#工具脚本)
- [探测模型真实输出上限](#探测模型真实输出上限)

## 部署方式一览

| 方式 | 适合 | 需要 |
|---|---|---|
| Docker 镜像 | 服务器、NAS，免克隆免构建 | Docker；镜像只有 `linux/amd64` |
| Docker Compose | 想改源码再部署、或习惯 compose 管理 | Docker + Compose，需要克隆仓库 |
| 单文件二进制 | Windows / macOS / Linux，不想装 Docker | 无；面板前端已打进二进制 |
| 从源码构建 | 开发调试 | Go 1.22+、Node.js 22+ |

所有方式都只需要两个可写目录：`auths/`（账号凭证）和 `data/`（池状态、用量、请求归档）。

## Docker 镜像（推荐）

镜像由 CI 发布在 GHCR：`ghcr.io/yreidev/workbuddy2api-panel`。

```bash
mkdir -p workbuddy2api/auths workbuddy2api/data && cd workbuddy2api

docker run -d --name workbuddy2api --restart unless-stopped \
  --user "$(id -u):$(id -g)" \
  -p 7863:7863 -e TZ=Asia/Shanghai \
  -v "$PWD/auths:/app/auths" -v "$PWD/data:/app/data" \
  ghcr.io/yreidev/workbuddy2api-panel:latest
```

几点说明：

- **配置文件在 `data/config.json`**：镜像默认读取 `/app/data/config.json`，也就是挂载的 `data/` 目录。首次启动时文件不存在，程序会自动生成一份推荐配置，并用随机值填好 `api_key`；面板「配置」页的修改也会写回这里，重建容器不丢。想放到别处，在镜像名后面加 `-config <容器内路径>`。
- **查看生成的密钥**：`grep api_key data/config.json`，或 `docker logs workbuddy2api 2>&1 | grep api_key`。
- **`--user "$(id -u):$(id -g)"`**：让容器以你自己的身份运行，刚建的 `auths/`、`data/` 就是你的，不会遇到写入权限问题。不加这一项时容器默认以 uid 10001 运行，见[常见问题](faq.md#docker-部署登录后报写入-auths-失败-permission-denied)。
- 账号为空时 `/healthz` 返回 503，容器状态会显示 `unhealthy`，添加账号后自动恢复。

镜像标签：`latest`（同 `main`）是 `main` 分支最新提交；每次提交另有一个 `sha-xxxxxxx` 标签，想固定版本就用它。

## Docker Compose

```bash
git clone https://github.com/yreidev/workbuddy2api-panel.git
cd workbuddy2api-panel

# compose 挂载的是 config.json 这个文件，宿主机上必须先有它，
# 否则 Docker 会把挂载点建成目录，容器起不来
cp config.example.json config.json
# 把 config.json 里的 api_key 改成你自己的随机字符串

PUID=$(id -u) PGID=$(id -g) docker compose up -d --build
```

首次构建约 1–2 分钟。`PUID` / `PGID` 也可以写进 `.env` 长期生效（`.env` 已被 `.gitignore` 忽略）。常用命令：

```bash
docker compose logs -f      # 跟踪日志
docker compose restart      # 重启
docker compose down         # 停止并移除容器，./auths 与 ./data 不受影响
```

## 单文件二进制

二进制自带面板前端，拷到任意机器即可运行，只要当前目录下的 `auths/`、`data/` 可写。

从 [Releases 的「最新构建」](https://github.com/yreidev/workbuddy2api-panel/releases/tag/latest)下载对应平台的包。它随 `main` 分支自动更新，与 Docker 的 `latest` 镜像同步；文件名不带版本号，下面的链接一直有效：

| 平台 | 文件 |
|---|---|
| Windows x64 | [`wb2api-panel-windows-amd64.zip`](https://github.com/yreidev/workbuddy2api-panel/releases/download/latest/wb2api-panel-windows-amd64.zip) |
| Linux x64 | [`wb2api-panel-linux-amd64.tar.gz`](https://github.com/yreidev/workbuddy2api-panel/releases/download/latest/wb2api-panel-linux-amd64.tar.gz) |
| Linux ARM64 | [`wb2api-panel-linux-arm64.tar.gz`](https://github.com/yreidev/workbuddy2api-panel/releases/download/latest/wb2api-panel-linux-arm64.tar.gz) |
| macOS Intel | [`wb2api-panel-darwin-amd64.tar.gz`](https://github.com/yreidev/workbuddy2api-panel/releases/download/latest/wb2api-panel-darwin-amd64.tar.gz) |
| macOS Apple 芯片 | [`wb2api-panel-darwin-arm64.tar.gz`](https://github.com/yreidev/workbuddy2api-panel/releases/download/latest/wb2api-panel-darwin-arm64.tar.gz) |

```bash
# 以 Linux x64 为例
curl -fLO https://github.com/yreidev/workbuddy2api-panel/releases/download/latest/wb2api-panel-linux-amd64.tar.gz
tar xzf wb2api-panel-linux-amd64.tar.gz
./wb2api                 # Windows：解压后运行 .\wb2api.exe
# 首次启动在当前目录生成 config.json（含随机 api_key，日志里打印一次）
# 浏览器打开 http://127.0.0.1:7863/panel/
```

每个包里是 `wb2api`、`config.example.json` 和 `README.md`；同一个 Release 里的 `checksums.txt` 可用 `sha256sum -c --ignore-missing checksums.txt` 校验。二进制没有签名，在 macOS 上用浏览器下载后运行可能被系统拦截，执行 `xattr -d com.apple.quarantine wb2api` 解除即可（用上面的 `curl` 下载不会被标记）。

## 从源码构建

```bash
# 1. 构建面板前端（Node.js 22+）：类型检查 + ESLint + 单元测试 + 打包到 internal/panel/dist
(cd web && npm ci && npm run build)

# 2. 构建并运行网关（Go 1.22+）
go test ./...
go run ./cmd/server -config config.json
```

前端产物 `internal/panel/dist` 不进版本库，`go build` 时经 `go:embed` 打进二进制。没构建前端也能编译通过，只是打开面板会看到「前端还没有构建」的提示，网关和 `/panel/api/*` 不受影响。

前端开发时用 `npm run dev`，它会把 `/panel/api` 转发到本机 `:7863` 的网关。

日常只需要主程序 `wb2api`。另外三个是 `signin.sh`、`login.sh`、`credit.sh` 调用的辅助程序（脚本首次运行时会自动编译，Docker 镜像里已预编译），需要手动构建时：

```bash
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o wb2api     ./cmd/server
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o signin_bin ./cmd/signin
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o login      ./cmd/login
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o credit     ./cmd/credit
```

## 添加账号

**方式 A：面板（推荐，各平台通用）**

打开 `http://<主机>:7863/panel/`，点右上角「添加账号」：

1. 选择国内版或国际版，面板显示授权链接；
2. 在浏览器里打开链接并登录；
3. 面板自动检测到登录完成，凭证写入 `auths/workbuddy-<uid>.json`，账号**热加载进池，不用重启**，并顺带完成首次签到。

也可以在同一个对话框里导入 cockpit tools 导出的 JSON，失败项会逐条列出。

**方式 B：命令行（Linux / macOS，依赖 bash 与 python3）**

```bash
./login.sh
# 按提示在浏览器打开授权链接 → 回到终端确认 → 凭证落盘 auths/workbuddy-<uid>.json
```

账号池启动时会和 `auths/` 目录对齐，新增的凭证文件会被自动发现。多台机器之间迁移账号，复制 `auths/workbuddy-<uid>.json` 即可。

## 升级、备份与停机

**升级**

```bash
docker pull ghcr.io/yreidev/workbuddy2api-panel:latest
docker rm -f workbuddy2api
# 再执行一次上面的 docker run
```

Compose 部署：`git pull && docker compose up -d --build`。

> [!NOTE]
> 旧版镜像默认读取镜像里自带的样例配置（`api_key` 为公开的 `test_key`）。如果你以前用 `docker run` 部署时没加 `-config`，升级后会在 `data/config.json` 生成新配置和新密钥，客户端里的密钥要换成新的；以前在面板里改过的配置也要重新设置一次。

**备份**

| 内容 | 位置 |
|---|---|
| 账号凭证（明文 token，务必妥善保管） | `auths/` |
| 池状态（积分、冷却、计数、暂停标记） | `data/state.json` |
| 用量统计与请求归档 | `data/usage.json`、`data/request-logs/` |
| 配置 | `config.json`（或 `data/config.json`） |

配置了 Upstash Redis 后，池状态和会话绑定还会异步镜像到 Redis（7 天 TTL），重启时取较新的一份。

**优雅停机**

收到停止信号后，网关先让 `/readyz` 返回 503、把状态落盘，再停止接新连接并等在途请求（含流式对话）结束，超时才强制退出。等待上限只能用环境变量 `WB2A_SHUTDOWN_TIMEOUT` 设置，默认 `5s`。

在 k3s / Kubernetes 上滚动更新时，建议：

- `WB2A_SHUTDOWN_TIMEOUT=570s`；
- `preStop` 先 `sleep 10`，让负载均衡摘掉这个实例；
- `terminationGracePeriodSeconds` 不小于 600；
- 就绪探针用 `/readyz`，不要用 `/healthz`（后者在账号全忙时也是 503）。

## 工具脚本

| 脚本 | 用途 |
|---|---|
| `./login.sh` | OAuth 登录 → 凭证落盘 → 重启容器 |
| `./signin.sh [auths_dir]` | 批量签到（token 过期先刷新） |
| `./credit.sh` / `./credit.sh -json` | 积分日报（美化输出 / 原始 JSON） |
| `python3 scripts/probe_active.py` | 活跃上报诊断与补跑：probe 只读，report / unlock 为写操作，默认 dry-run，加 `--yes` 才执行 |
| `python3 scripts/probe_max_tokens.py` | 探测各模型真实输出上限，结果可直接给面板展示（见下节） |

脚本首次使用时会自动 `go build` 对应的 `cmd/*`；Docker 镜像里已经预编译好。

## 探测模型真实输出上限

上游模型目录里的 `max_output_tokens` 是**声称值**，普遍虚高：很多模型请求更大的 `max_tokens` 也不报错，而是输出到某个真实上限就静默截断。探测脚本会逐个模型实测，并区分「被钳制截断」和「模型主动收尾」：

```bash
# 在网关所在机器上运行（会真实消耗积分；单模型预算默认 600s，并行 4）
python3 scripts/probe_max_tokens.py \
  --base http://127.0.0.1:7863/v1 --key <你的 api_key> \
  --panel-out data/output_probes.json

# 断点续测 / 只测指定模型 / 只看计划不执行
python3 scripts/probe_max_tokens.py ... --resume
python3 scripts/probe_max_tokens.py ... --models cn:glm-5.2
python3 scripts/probe_max_tokens.py ... --dry-run
```

结果写到 state 文件同目录的 `output_probes.json`，面板「模型与档位」的最大输出列会叠加实测标注，下次查询即生效，不用重启：

| 标注 | 含义 |
|---|---|
| `32K ⚠ 钳制 12×` | 实测在 32K 处被截断，只有声称值的 1/12 |
| `48K ✓` / `64K ↑` | 实测与声称一致 / 实际比声称更大 |
| `≥40K` / `?` | 满额未触顶（只是下界）/ 模型主动收尾，未测出 |

判据的设计细节见脚本头部注释。
