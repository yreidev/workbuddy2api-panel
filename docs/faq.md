# 常见问题

- [Docker 部署登录后报「写入 auths/… 失败: permission denied」](#docker-部署登录后报写入-auths-失败-permission-denied)
- [容器一直显示 unhealthy](#容器一直显示-unhealthy)
- [忘了 api_key](#忘了-api_key)
- [429 code=6004 是什么](#429-code6004-是什么)
- [请求被上游内容审核拦截](#请求被上游内容审核拦截)
- [大上下文请求报 read body: i/o timeout](#大上下文请求报-read-body-io-timeout)
- [账号被禁用了怎么恢复](#账号被禁用了怎么恢复)
- [多个账号共用一个 IP，怎么一次只用一个](#多个账号共用一个-ip怎么一次只用一个)
- [官网积分记录的「使用端」显示什么](#官网积分记录的使用端显示什么)

## Docker 部署登录后报「写入 auths/… 失败: permission denied」

容器默认以 uid 10001 运行，而宿主机上挂载的 `auths/`、`data/` 属于你自己，容器写不进去。任选一种解法：

```bash
# 方案 1（推荐）：让容器以你自己的身份运行
docker run --user "$(id -u):$(id -g)" ...
# Compose：
PUID=$(id -u) PGID=$(id -g) docker compose up -d --force-recreate

# 方案 2：把目录交给容器默认用户（需要 sudo）
sudo chown -R 10001:10001 ./auths ./data ./config.json

# 方案 3：以 root 运行（NAS 不方便 chown 时用，自担风险）
docker run --user 0:0 ...
```

## 容器一直显示 unhealthy

镜像的健康检查访问 `/healthz`，而没有可用账号时它返回 503。添加账号后会自动变成 healthy。

## 忘了 api_key

打开配置文件看 `api_key` 字段（Docker 快速开始的配置在 `data/config.json`）。首次自动生成时，启动日志里也打印过一次。

## 429 code=6004 是什么

这是**某个模型**的用量超限，不是账号整体被限流。上游的提示通常带「将在 YYYY-MM-DD HH:MM:SS 重置」：

- 网关把该账号的冷却时间设到上游给的重置时刻（封顶 `cooldown.soft_rate_max`，默认 2 小时）；
- 同一个账号**换别的模型**照样可用；
- 面板「账号池 → 模型锁池」能看到哪些模型被限、还剩几个号、多久解锁。

## 请求被上游内容审核拦截

Claude Code、Codex 等客户端的系统提示词里有固定模板句，可能被上游审核误拦：

- 默认 `prompt.mode=passthrough`：遇到拦截，网关会在同一请求内换成中性提示词自动重试一次，当天后续请求直接用中性提示词。
- 仍频繁被拦时，改成 `custom`（用网关自带提示词替换）或 `append`（在客户端提示词后追加）。
- 如果换了提示词仍被拦，说明是对话内容本身触发了审核。

详见[网关与选号 · 系统提示词与内容审核](gateway.md#系统提示词与内容审核)。

## 大上下文请求报 read body: i/o timeout

入站请求体上传超过了 `server.read_timeout`（默认 300 秒），常见于经过多层反代转发的大请求。调大这个值（`0` = 不限制）后重启。网关本身不限制请求体大小。

## 账号被禁用了怎么恢复

- session 连续 3 次失效会被自动禁用，原因写在面板账号状态与 `/status` 的 `disabled_reason` 里。
- 面板账号行点「解冻」可以直接复活；token 确实失效的，用「添加账号」重新登录同一个账号覆盖凭证。

## 多个账号共用一个 IP，怎么一次只用一个

在面板里对暂时不用的账号点「**暂停选号**」：它们不再接请求，但签到、保活、余额刷新、成长任务照常进行。需要时点「恢复选号」立即回池。详见[禁用、暂停与企业版账号](tasks.md#禁用暂停与企业版账号)。

## 官网积分记录的「使用端」显示什么

官网按出站请求的 User-Agent 归因。默认 UA 已对齐官方桌面端 `WorkBuddy/5.5.4 WorkBuddy/5.5.4 CLI/2.137.1`；需要时可用 `upstream.user_agent` 整体覆盖，或用 `upstream.client_version` / `upstream.cli_version` 只改版本号。改完需要重启。
