#!/usr/bin/env bash
#
# enterprise-knowledge-hub 一键部署脚本（WSL / Linux 环境运行）
#
# 用法：
#   ./deploy.sh <SERVER_IP> [ROOT_PASSWORD]
#   例： ./deploy.sh 8.215.92.160 zcjjgm302   # 首次部署（带密码，用于配置免密）
#        ./deploy.sh 8.215.92.160             # 后续部署（已免密，无需密码）
#
# 功能：免密配置(首次) → 打包传输 → 密钥复用/生成 → 修改compose → 构建启动 → 验证
# 前置：sshpass（WSL 内 `apt-get install sshpass`）、docker/compose(服务器侧已装)

set -euo pipefail

SERVER_IP="${1:-}"
ROOT_PW="${2:-}"

# 本地项目根目录（脚本所在目录）
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LOCAL_PROJ="$SCRIPT_DIR"
PROJ_NAME="$(basename "$LOCAL_PROJ")"

REMOTE_BASE="/opt/${PROJ_NAME}"
REMOTE_COMPOSE="${REMOTE_BASE}/docker-compose.yml"
REMOTE_SECRETS="/root/.ekh_secrets"
TARBALL="/tmp/${PROJ_NAME}.tar.gz"

if [[ -z "$SERVER_IP" ]]; then
  echo "用法: $0 <SERVER_IP> [ROOT_PASSWORD]"
  exit 1
fi

SSH="ssh -o BatchMode=yes -o StrictHostKeyChecking=no -o ConnectTimeout=15"
SCP="scp -o BatchMode=yes -o StrictHostKeyChecking=no -o ConnectTimeout=15"

# 远程执行（免密；如不可用且给了密码，则用 sshpass 走一次密码并配置免密）
ssh_run() {
  if $SSH "root@${SERVER_IP}" "$1" >/dev/null 2>&1; then
    return 0
  fi
  if [[ -n "$ROOT_PW" ]]; then
    echo ">> [1/6] 首次部署：配置 SSH 免密登录"
    PUBKEY="$(cat ~/.ssh/id_ed25519.pub 2>/dev/null || { ssh-keygen -t ed25519 -N '' -f ~/.ssh/id_ed25519 -q; cat ~/.ssh/id_ed25519.pub; })"
    sshpass -p "$ROOT_PW" ssh -o StrictHostKeyChecking=no -o PubkeyAuthentication=no -o PreferredAuthentications=password \
      "root@${SERVER_IP}" "mkdir -p ~/.ssh && chmod 700 ~/.ssh && touch ~/.ssh/authorized_keys && \
       grep -qxF '$PUBKEY' ~/.ssh/authorized_keys || echo '$PUBKEY' >> ~/.ssh/authorized_keys; chmod 600 ~/.ssh/authorized_keys"
    echo ">> 免密配置完成"
    return 0
  fi
  echo "!! 无法免密连接 ${SERVER_IP}，且未提供密码。请先运行: $0 $SERVER_IP <ROOT_PASSWORD>"
  return 1
}

echo "======================================================"
echo " 部署 ${PROJ_NAME} → ${SERVER_IP}"
echo " 本地: ${LOCAL_PROJ}   远程: ${REMOTE_BASE}"
echo "======================================================"

# ---------- [1/6] 免密检查/配置 ----------
ssh_run "echo ok" || exit 1

# ---------- [2/6] 打包并传输 ----------
echo ">> [2/6] 打包（排除 node_modules/dist/.git/log）并上传"
rm -f "$TARBALL"
tar czf "$TARBALL" -C "$LOCAL_PROJ" \
  --exclude='node_modules' --exclude='dist' --exclude='.git' --exclude='*.log' \
  .
LOCAL_MD5="$(md5sum "$TARBALL" | awk '{print $1}')"
$SCP "$TARBALL" "root@${SERVER_IP}:/tmp/"
REMOTE_MD5="$($SSH "root@${SERVER_IP}" "md5sum /tmp/${PROJ_NAME}.tar.gz | awk '{print \$1}'")"
if [[ "$LOCAL_MD5" != "$REMOTE_MD5" ]]; then
  echo "!! md5 不一致，传输失败，请重试"
  exit 1
fi
echo ">> 传输校验通过 ($LOCAL_MD5)"
$SSH "root@${SERVER_IP}" "mkdir -p ${REMOTE_BASE} && tar xzf /tmp/${PROJ_NAME}.tar.gz -C ${REMOTE_BASE}"

# ---------- [3/6] 密钥：已有则复用，无则生成 ----------
echo ">> [3/6] 检查/生成密钥"
HAS_SECRETS="$($SSH "root@${SERVER_IP}" "test -f ${REMOTE_SECRETS} && echo yes || echo no")"
if [[ "$HAS_SECRETS" == "yes" ]]; then
  echo ">> 复用已有密钥 ${REMOTE_SECRETS}（保持数据库密码不变）"
else
  echo ">> 生成新密钥并写入 ${REMOTE_SECRETS}"
  $SSH "root@${SERVER_IP}" "umask 077; printf 'PGPW=%s\nJWT_SECRET=%s\nMEILI_MASTER_KEY=%s\nMEILI_API_KEY=%s\n' \
    \$(openssl rand -hex 12) \$(openssl rand -hex 24) \$(openssl rand -hex 24) \$(openssl rand -hex 24) > ${REMOTE_SECRETS}"
fi

# ---------- [4/6] 应用密钥到 compose（幂等：值已替换则跳过） ----------
echo ">> [4/6] 应用密钥到 docker-compose.yml"
$SSH "root@${SERVER_IP}" "bash -s" <<'EOS'
set -euo pipefail
source /root/.ekh_secrets
C=/opt/enterprise-knowledge-hub/docker-compose.yml
python3 - "$C" "$PGPW" "$JWT_SECRET" "$MEILI_MASTER_KEY" <<'PYEOF'
import sys
c, pgpw, jwt, meili = sys.argv[1:5]
s = open(c, encoding='utf-8').read()
orig = s

def rep(a, b):
    global s
    if a in s:
        s = s.replace(a, b)

# 只在仍含默认值时替换（幂等）
rep('POSTGRES_PASSWORD: postgres', f'POSTGRES_PASSWORD: {pgpw}')
rep('JWT_SECRET: "change-me-in-production"', f'JWT_SECRET: "{jwt}"')
rep('MEILI_MASTER_KEY: change-me-in-production', f'MEILI_MASTER_KEY: {meili}')
rep('MEILI_API_KEY: "change-me-in-production"', f'MEILI_API_KEY: "{meili}"')

old_dsn = 'postgres://postgres:postgres@postgres'
new_dsn = f'postgres://postgres:{pgpw}@postgres'
if old_dsn in s:
    s = s.replace(old_dsn, new_dsn)

if s != orig:
    open(c, 'w', encoding='utf-8').write(s)
    print("compose 密钥已更新")
else:
    print("compose 已是生产密钥，无需改动")
PYEOF
EOS

# ---------- [5/6] 构建并启动 ----------
echo ">> [5/6] 构建并启动容器（首次构建可能需 10-20 分钟）"
$SSH "root@${SERVER_IP}" "cd ${REMOTE_BASE} && docker compose build && docker compose up -d"

# ---------- [6/6] 验证 ----------
echo ">> [6/6] 等待启动并验证"
sleep 8
$SSH "root@${SERVER_IP}" "cd ${REMOTE_BASE} && docker compose ps"

echo
echo "== 后端健康 =="
$SSH "root@${SERVER_IP}" "curl -s -m 8 http://localhost:8081/api/health || echo FAIL"
echo
echo "== 前端 =="
$SSH "root@${SERVER_IP}" "curl -s -m 8 -o /dev/null -w 'frontend HTTP %{http_code}\n' http://localhost:3002/ || true"
echo "== 企微机器人 =="
$SSH "root@${SERVER_IP}" "docker logs enterprise-knowledge-hub-backend-1 2>&1 | grep -E 'wecom.*(subscribed|ack)' | tail -2 || echo '机器人日志暂无'"
echo
echo "✅ 部署完成: http://${SERVER_IP}:3002"
