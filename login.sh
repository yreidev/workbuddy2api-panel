#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
# SPDX-License-Identifier: AGPL-3.0-only
# 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

# login.sh — WorkBuddy CN OAuth 登录 → 落盘 auth 文件
#
# 用法:
#   ./login.sh [--realm=global] [--pool=池名]
#
#   参数原样交给 cmd/login。用了代理池（data/proxies.json，--proxies 改路径）时，登录请求走 --pool 选的池
#   （不写 = 这个域的默认池，--pool=- = 不绑）里分到的代理，新号绑在它上面；账号原来就有凭证文件的沿用
#   文件里原来的绑定。
#
# 流程:
#   1. POST /v2/plugin/auth/state 拿授权 URL（无 PKCE，state 由服务端签发）
#   2. 你在浏览器打开 URL 完成登录
#   3. 回到这里按 y → poll 拿 token+uid+nickname → 签到（绑了代理的号不在这里签，交给网关走代理签）
#      → 落盘 auths/workbuddy-<uid>.json
#   4. 重启 workbuddy2api 容器加载新账号
set -euo pipefail

cd "$(dirname "$0")"
AUTH_DIR="./auths"
CONTAINER="workbuddy2api"

mkdir -p "$AUTH_DIR"

# login 工具：不存在才编译（源码改动后手动 go build -o login ./cmd/login）
LOGIN_BIN="./login"
if [[ ! -x "$LOGIN_BIN" ]]; then
    go build -o "$LOGIN_BIN" ./cmd/login
fi

echo "============================================================"
echo "  WorkBuddy OAuth 登录"
echo "============================================================"
echo ""

AUTH_URL=$("$LOGIN_BIN" "$@" url)

echo "请在浏览器中打开以下链接完成登录："
echo ""
echo "  $AUTH_URL"
echo ""

if command -v xclip &>/dev/null; then
    echo -n "$AUTH_URL" | xclip -selection clipboard 2>/dev/null && echo "(已复制到剪贴板)"
elif command -v xsel &>/dev/null; then
    echo -n "$AUTH_URL" | xsel --clipboard 2>/dev/null && echo "(已复制到剪贴板)"
fi

echo ""
read -rp "完成登录后按 y 继续: " ans
if [[ "$ans" != "y" && "$ans" != "Y" ]]; then
    echo "已取消"
    exit 1
fi

echo ""
echo "正在获取 token..."

RESULT=$("$LOGIN_BIN" "$@" poll) || {
    echo ""
    echo "获取 token 失败。可能原因："
    echo "  - 登录还没完成就按了 y（重新运行 ./login.sh 再试）"
    echo "  - 登录页报错（把报错截图发出来排查）"
    exit 1
}

TOKEN=$(echo "$RESULT" | python3 -c "import json,sys; print(json.load(sys.stdin)['access_token'])")
REFRESH=$(echo "$RESULT" | python3 -c "import json,sys; print(json.load(sys.stdin)['refresh_token'])")
EXPIRES_IN=$(echo "$RESULT" | python3 -c "import json,sys; print(json.load(sys.stdin)['expires_in'])")
DOMAIN=$(echo "$RESULT" | python3 -c "import json,sys; print(json.load(sys.stdin).get('domain',''))")
USER_ID=$(echo "$RESULT" | python3 -c "import json,sys; print(json.load(sys.stdin).get('uid',''))")
ENT_ID=$(echo "$RESULT" | python3 -c "import json,sys; print(json.load(sys.stdin).get('enterprise_id',''))")
NICKNAME=$(echo "$RESULT" | python3 -c "import json,sys; print(json.load(sys.stdin).get('nickname',''))")
# 部门全名与官方刷新时核对身份用的字段：poll 输出里有才写
EXTRA=$(echo "$RESULT" | python3 -c "import json,sys; d=json.load(sys.stdin); print(json.dumps({k: d[k] for k in ('department_full_name','account_type','oneid_account_id','idp','token_type') if k in d}))")

if [[ -z "$USER_ID" ]]; then
    echo "无法获取 uid，请检查 token 是否有效"
    exit 1
fi

EXPIRES_AT=$(( $(date +%s) + EXPIRES_IN ))

# ─── 代理绑定：登录走了代理池里的代理时 poll 输出带 proxy；账号原来就有凭证文件的沿用文件里原来的绑定
# （同面板登录：重新登录只是换凭证）───
AUTH_FILE="$AUTH_DIR/workbuddy-${USER_ID}.json"
PROXY=$(echo "$RESULT" | python3 -c "import json,sys; p=json.load(sys.stdin).get('proxy'); print(json.dumps(p) if p else '')")
if [[ -f "$AUTH_FILE" ]]; then
    PROXY=$(python3 -c "import json,sys; p=json.load(open(sys.argv[1])).get('proxy'); print(json.dumps(p) if p else '')" "$AUTH_FILE")
fi

# ─── 签到（CN：POST codebuddy.cn/v2/billing/meter/daily-checkin，幂等不阻塞）───
# 绑了代理的号不在这里签：这一步从本机直接发，会让号的请求从本机 IP 出去；交给网关的定时签到走号的代理。
if [[ -n "$PROXY" ]]; then
    echo "签到: 号绑了代理，交给网关的定时签到（走号的代理）"
else
# OAuth 返回字段一律通过环境变量传入 Python，并用带引号 heredoc：
# 昵称/domain/token 等值可含引号或换行，拼进 Python 源码会造成注入。
WB2A_LOGIN_TOKEN="$TOKEN" \
WB2A_LOGIN_USER_ID="$USER_ID" \
WB2A_LOGIN_ENT_ID="$ENT_ID" \
WB2A_LOGIN_DOMAIN="$DOMAIN" \
python3 - <<'PYEOF'
import json, os, urllib.request, urllib.error

token = os.environ["WB2A_LOGIN_TOKEN"]
user_id = os.environ["WB2A_LOGIN_USER_ID"]
ent_id = os.environ["WB2A_LOGIN_ENT_ID"]
domain = os.environ["WB2A_LOGIN_DOMAIN"]

req = urllib.request.Request(
    "https://www.codebuddy.cn/v2/billing/meter/daily-checkin",
    method="POST", data=b"{}",
    headers={
        "Authorization": "Bearer " + token,
        "Accept": "application/json",
        "Content-Type": "application/json",
        "X-User-Id": user_id,
        **({"X-Enterprise-Id": ent_id, "X-Tenant-Id": ent_id} if ent_id else {}),
        **({"X-Domain": domain} if domain else {}),
    })
try:
    with urllib.request.urlopen(req, timeout=15) as r:
        body = json.loads(r.read().decode() or "{}")
    if body.get("code") == 0:
        data = body.get("data") or {}
        print(f"签到: 成功 {json.dumps(data, ensure_ascii=False)[:150]}")
    else:
        print(f"签到: {body.get('msg', json.dumps(body)[:150])}")
except urllib.error.HTTPError as e:
    # 已签到等业务错误也走 4xx（实测 code=10001 "今天已签到"）
    try:
        body = json.loads(e.read().decode() or "{}")
        print(f"签到: {body.get('msg', 'http %d' % e.code)}")
    except Exception:
        print(f"签到: http {e.code}")
except Exception as e:
    print(f"签到: {e}")
PYEOF
fi

# ─── 落盘 auth 文件（与 internal/auth 读取格式一致）─────────────────
if [[ -f "$AUTH_FILE" ]]; then
    echo "账号已存在（uid=${USER_ID}），将覆盖更新凭证"
    ACTION="覆盖"
else
    echo "新账号（uid=${USER_ID}），新增 auth 文件"
    ACTION="新增"
fi
WB2A_LOGIN_TOKEN="$TOKEN" \
WB2A_LOGIN_REFRESH="$REFRESH" \
WB2A_LOGIN_EXPIRES_AT="$EXPIRES_AT" \
WB2A_LOGIN_DOMAIN="$DOMAIN" \
WB2A_LOGIN_USER_ID="$USER_ID" \
WB2A_LOGIN_ENT_ID="$ENT_ID" \
WB2A_LOGIN_NICKNAME="$NICKNAME" \
WB2A_LOGIN_EXTRA="$EXTRA" \
WB2A_LOGIN_PROXY="$PROXY" \
WB2A_LOGIN_AUTH_FILE="$AUTH_FILE" \
WB2A_LOGIN_ACTION="$ACTION" \
python3 - <<'PYEOF'
import json, os

auth = {
    "account": {
        "uid": os.environ["WB2A_LOGIN_USER_ID"],
        "enterpriseId": os.environ["WB2A_LOGIN_ENT_ID"],
        "nickname": os.environ["WB2A_LOGIN_NICKNAME"],
    },
    "auth": {
        "accessToken": os.environ["WB2A_LOGIN_TOKEN"],
        "refreshToken": os.environ["WB2A_LOGIN_REFRESH"],
        "expiresAt": int(os.environ["WB2A_LOGIN_EXPIRES_AT"]),
        "domain": os.environ["WB2A_LOGIN_DOMAIN"],
    },
}
# 部门全名（企业号才有，官方请求头 X-Department-Info 的值）与官方刷新时核对身份用的字段
extra = json.loads(os.environ["WB2A_LOGIN_EXTRA"])
for src, dst in (("department_full_name", "departmentFullName"), ("account_type", "type"),
                 ("oneid_account_id", "oneidAccountId"), ("idp", "idp")):
    if src in extra:
        auth["account"][dst] = extra[src]
if "token_type" in extra:
    auth["auth"]["tokenType"] = extra["token_type"]
# 号绑的代理（代理池，见 docs/proxy.md）
if os.environ["WB2A_LOGIN_PROXY"]:
    auth["proxy"] = json.loads(os.environ["WB2A_LOGIN_PROXY"])
with open(os.environ["WB2A_LOGIN_AUTH_FILE"], "w") as f:
    json.dump(auth, f, indent=1)
print(f"已保存（{os.environ['WB2A_LOGIN_ACTION']}）: {os.environ['WB2A_LOGIN_AUTH_FILE']}")
PYEOF

# ─── 重启服务 ────────────────────────────────────────────
echo ""
if docker ps --format '{{.Names}}' | grep -q "^${CONTAINER}$"; then
    echo "重启 $CONTAINER 加载新账号..."
    docker restart "$CONTAINER" >/dev/null
    sleep 2
    # API_KEY 从 config.json 读取（该变量在脚本中未定义，fallback 仅为占位，不会通过鉴权）
    API_KEY=$(python3 -c "import json; print(json.load(open('config.json')).get('api_key',''))" 2>/dev/null)
    COUNT=$(curl -s http://127.0.0.1:7863/status -H "Authorization: Bearer ${API_KEY:-test_key}" 2>/dev/null | python3 -c "import json,sys; print(len(json.load(sys.stdin).get('accounts',[])))" 2>/dev/null || echo "?")
    echo "服务已重启，当前账号数: $COUNT"
else
    echo "容器 $CONTAINER 未运行，auth 文件已保存，下次启动自动加载"
fi

echo ""
echo "============================================================"
echo "  登录完成！"
echo "  UID: $USER_ID"
echo "  Nickname: ${NICKNAME:-（未获取到）}"
echo "  Token: ${TOKEN:0:30}..."
echo "  有效期: $(date -d "@$EXPIRES_AT" '+%Y-%m-%d %H:%M' 2>/dev/null || echo "$EXPIRES_AT")"
echo "============================================================"
