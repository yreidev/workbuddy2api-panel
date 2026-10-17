# 安全与合规

## 凭证

- 账号凭证保存在 `auths/workbuddy-<uid>.json`，内容是**明文** access token、refresh token 与账号信息。拿到这个文件就等于拿到了账号。
- token 刷新时以 `0600` 权限原子写回；`login.sh` 首次落盘遵循当前 umask，建议手动 `chmod 600 auths/*.json`。
- 容器默认以非 root 用户运行。
- `.gitignore` 已排除 `auths/`、`data/`、`backups/`、`config.json`、`*.key`、`*.pem`、`*.env`，不要手动把它们加进版本库。

## 网络暴露

- 服务默认监听 `:7863`，只提供 HTTP，没有内置 TLS。
- **`api_key` 为空时不鉴权**。自动生成的配置会填好随机密钥；手写配置时请自己设置。
- 样例配置 `config.example.json` 里的 `api_key` 是公开的占位值 `test_key`，从它复制出的配置部署前务必改掉；程序自动生成的配置用的是随机密钥。
- 公网部署请放在 HTTPS 反向代理（Nginx、Caddy 等）之后，并在反代上做访问限流；只在本机或内网使用时可以直接运行。
- 除了访问 WorkBuddy 上游，网关每 6 小时还会向 `raw.githubusercontent.com` 发一个 GET，读取本仓库的更新日志用于[更新提醒](panel.md#更新提醒)，不带任何本机信息；不希望联网检查可把 `panel.update_check` 设为 `false`。
- [一键更新](panel.md#一键更新)只在面板里点了才会下载：地址固定是本仓库的 GitHub Release，版本必须是检查到的、比本机新的版本，安装包要和同一 Release 的 `checksums.txt` 对得上，重定向只跟 `https`。拿到面板密钥的人能做的只是把网关换成本仓库发布过的版本，或退回程序自带的版本。新程序放在数据目录的 `bin/` 下并会被执行，数据目录不要让其他人可写。

## 日志

- 终端日志只含请求序号、模型、状态码、UID 前 8 位、耗时与 token 数，不读取 `Authorization` 头，不含任何 token 或 `api_key` 明文。
- 请求归档只记元数据，不记提示词与响应正文。
- 调用来源（IP、User-Agent）属于个人信息，可用 `logging.request_client_info` 关闭。

## 使用边界

- 本项目是 WorkBuddy 的**非官方**网关，上游接口均为逆向所得的非公开接口。
- 仅限管理**你自己的**账号，在本机或私有环境使用。
- 不得共享、转售、违规分发，严禁批量注册账号做 API 业务、二次加壳或捆绑卡密售卖，详见 README 的[使用声明](../README.md#使用声明)。
- 使用目标平台的账号做 API 网关涉及其服务条款与账号风险，作者不对账号封禁、条款违约或任何使用后果负责。
