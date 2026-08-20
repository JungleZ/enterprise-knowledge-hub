# 生产部署指南（EC2 / 云服务器）

> 本文档记录 `enterprise-knowledge-hub` 在远程服务器（8.215.92.160，Alibaba Cloud Linux 3，2C/1.8G RAM）上的一次完整部署过程，含所有用到/修改的文件、路径、以及依赖项说明，供后续重复部署参考。

---

## 0. 部署前确认

### 所需文件（本地 → 服务器）

| 文件 | 本地路径 | 远程路径 |
| --- | --- | --- |
| 整个项目（打包为 tar.gz） | `D:\ai_projects\enterprise-knowledge-hub` → `C:\Users\Administrator\AppData\Local\Temp\opencode\ekh.tar.gz` | `/opt/enterprise-knowledge-hub/` |
| docker-compose.yml | 项目根目录 | `/opt/enterprise-knowledge-hub/docker-compose.yml` |
| backend/（含 Dockerfile） | 项目根目录 | `/opt/enterprise-knowledge-hub/backend/` |
| frontend/（含 Dockerfile） | 项目根目录 | `/opt/enterprise-knowledge-hub/frontend/` |

**打包时排除**（避免体积过大/污染）：`node_modules`、`dist`、`.git`、`*.log`

```bash
cd /mnt/d/ai_projects
tar czf ekh.tar.gz --exclude='node_modules' --exclude='dist' --exclude='.git' --exclude='*.log' enterprise-knowledge-hub
```

### 服务器前置要求
- Docker 26.x + Docker Compose v2（已在服务器上，无需安装）
- 磁盘 ≥ 15G 空闲（本项目镜像约 3-4G）
- 内存 ≥ 1G（本产品 4 容器空闲约 84MB，负载 300-500MB，1.8G 富余）

---

## 1. SSH 免密配置（一次性）

WSL 里生成密钥，密码登录一次写入公钥：

```bash
# 生成密钥（如无）
ssh-keygen -t ed25519 -N '' -f ~/.ssh/id_ed25519 -q

# 用密码把公钥写入服务器（仅一次，之后全部免密）
sshpass -p '<ROOT_PASSWORD>' ssh -o StrictHostKeyChecking=no root@<SERVER_IP> \
  'mkdir -p ~/.ssh && chmod 700 ~/.ssh && touch ~/.ssh/authorized_keys && \
   grep -qxF "$(cat ~/.ssh/id_ed25519.pub)" ~/.ssh/authorized_keys || cat >> ~/.ssh/authorized_keys <<EOF
   $(cat ~/.ssh/id_ed25519.pub)
   EOF
   chmod 600 ~/.ssh/authorized_keys'
```

> 注意：WSL 里需要 `apt-get install sshpass`。若 `dpkg` 报 interrupted，先 `dpkg --configure -a`。

---

## 2. 传输代码

```bash
scp -o BatchMode=yes -o StrictHostKeyChecking=no ekh.tar.gz root@<SERVER_IP>:/tmp/
# 传输后务必校验 md5，避免截断
md5sum ekh.tar.gz && ssh root@<SERVER_IP> 'md5sum /tmp/ekh.tar.gz'

ssh root@<SERVER_IP> 'mkdir -p /opt/enterprise-knowledge-hub && tar xzf /tmp/ekh.tar.gz -C /opt/'
```

---

## 3. 修改配置（重要：不能直接用本地默认值）

远程文件：`/opt/enterprise-knowledge-hub/.env`

新版 compose 的密钥全部走环境变量插值（`${VAR:-默认值}`），**不需要再改 docker-compose.yml 文本**，只需在服务器上创建 `.env`：

### 必须改的默认值

| 项 | 默认值 | 改为 | 说明 |
| --- | --- | --- | --- |
| `POSTGRES_PASSWORD` | `change-me` | 随机 24 hex | 数据库密码（+容器内 postgres 密码） |
| `JWT_SECRET` | `change-me-in-production` | 随机 48 hex | 会话签名密钥 |
| `MEILI_MASTER_KEY` | `change-me-in-production` | 随机 48 hex | Meilisearch 主密钥 |
| `MEILI_API_KEY` | `change-me-in-production` | 与 master 相同 | 后端访问 Meilisearch |
| `AUTH_ALLOW_REGISTER` | `true` | 首次初始化后可 `false` | 关闭公开自注册（企业内网建议） |
| `SEED_DATA` | `true` | 首次初始化后可 `false` | 关闭演示数据自动创建 |

在服务器上生成密钥并写入 `.env`（权限 600，仅 root 可读）： |

生成密钥并写入 .env：

```bash
PGPW=$(openssl rand -hex 12); JWTS=$(openssl rand -hex 24); MEILIKEY=$(openssl rand -hex 24)
umask 077
cat > /opt/enterprise-knowledge-hub/.env <<EOF
POSTGRES_PASSWORD=$PGPW
JWT_SECRET=$JWTS
MEILI_MASTER_KEY=$MEILIKEY
MEILI_API_KEY=$MEILIKEY
LLM_API_KEY=<OpenRouter 密钥>
WEB_SEARCH_API_KEY=<Tavily 密钥，可留空>
BOT_PLATFORM=wecom
WECOM_BOT_ID=<企微 BotID>
WECOM_BOT_SECRET=<企微 Secret>
AUTH_ALLOW_REGISTER=false
EOF
cp -f /opt/enterprise-knowledge-hub/docker-compose.yml /opt/enterprise-knowledge-hub/docker-compose.yml.bak
cat > /root/.ekh_secrets <<EOF
PGPW=$PGPW
JWT_SECRET=$JWTS
MEILI_MASTER_KEY=$MEILIKEY
MEILI_API_KEY=$MEILIKEY
EOF
```

> 密钥保存在 `/root/.ekh_secrets` 与 `/opt/enterprise-knowledge-hub/.env`（均 600 仅 root 可读）。**不要提交到 git、不要外泄。**

### 端口（默认已避开服务器现有服务，通常无需改）

| 服务 | 端口 | 说明 |
| --- | --- | --- |
| frontend | 3002 | 对外访问入口 |
| backend | 8081 | 仅内网/按需开放 |
| postgres | 5433 | 仅内网 |
| meilisearch | 7701 | 仅内网 |

> 部署前检查端口冲突：`ss -tlnp | grep -E ':3002|:8081|:5433|:7701'`

---

## 4. 构建与启动

```bash
cd /opt/enterprise-knowledge-hub
docker compose build          # 首次构建需拉取基础镜像，可能 10-20 分钟
docker compose up -d
docker compose ps             # 4 个容器都应为 Up
```

---

## 5. 验证

```bash
# 后端健康
curl -s http://localhost:8081/api/health          # {"status":"ok"}
# 前端
curl -s -o /dev/null -w "%{http_code}\n" http://localhost:3002/   # 200
# Meilisearch
curl -s http://localhost:7701/health              # {"status":"available"}
# 企微机器人长连接
docker logs enterprise-knowledge-hub-backend-1 2>&1 | grep -E 'wecom' | tail -5
#  期望：subscribe ack errcode 0 + subscribed, waiting for messages + 心跳正常
# seed 数据
docker logs enterprise-knowledge-hub-backend-1 2>&1 | grep -E 'seed'
```

从公网访问：`http://<SERVER_IP>:3002`（需在云控制台安全组放行 3002 端口）。

演示账号：`admin@demo.local / demo123`、`rd@demo.local / rd123`、`finance@demo.local / fin123`、`sales@demo.local / sales123`。

---

## 6. 涉及的文件与第三方依赖说明

### 本项目用到的文件（均随代码包上传）

| 路径 | 作用 | 本次是否修改 |
| --- | --- | --- |
| `docker-compose.yml` | 编排 4 服务（postgres/meilisearch/backend/frontend） | **修改**：密钥改为 `${VAR:-默认}` 插值，密钥实际值走 `.env` |
| `backend/Dockerfile` | Go 后端镜像构建 | 未修改 |
| `backend/internal/**` | 后端代码 | 未修改（仅此前功能开发时改过 bot 相关） |
| `frontend/Dockerfile` | 前端镜像构建 | 未修改 |
| `frontend/nginx.conf` | 前端静态服务 + /api 反代 | 未修改 |
| `README.md` | 项目文档 | 未修改 |

### 依赖的第三方工具/镜像（Docker Hub 拉取）

| 镜像/工具 | 版本 | 用途 |
| --- | --- | --- |
| `pgvector/pgvector:pg16` | pg16 | Postgres + 向量扩展（本项目当前用离线 BM25，向量预留） |
| `getmeili/meilisearch:v1.8` | v1.8 | 全文检索 |
| `node:22-bookworm` / `node:22-bookworm-slim` | 22 | 前端构建（构建期） |
| `debian:bookworm-slim` | — | 前端运行期 |
| `ubuntu:24.04` | 24.04 | 后端运行期 |
| Go 模块 | 见 `backend/go.mod` | 后端依赖，构建时下载 |

### 非本项目的第三方服务（服务器上已存在，未改动）

> 服务器上原本就运行着其他服务，与本次部署无关，**不要动它们**：
> - `openclaw-gateway`（占用约 477MB 内存）
> - searxng、x-ui、PM2 及多个 Python 应用（8082/8083/8088/5000/5001/5002 端口）
> - shadowsocks（8388）、nginx（80/8089）
>
> 若内存紧张（可用 < 500MB），可考虑停用其中非关键项，但**不是本产品部署的必要步骤**。

---

## 7. 常见问题

| 问题 | 原因 | 解决 |
| --- | --- | --- |
| scp 传过去文件只有几 MB | 传输中断/截断 | 重新 scp 并用 md5 校验两端一致 |
| `no host` / 连不上 postgres | DATABASE_DSN 密码与 POSTGRES_PASSWORD 不一致 | 修改 DSN 为新密码后重建 backend |
| 后端日志反复 `the database system is starting up` | postgres 健康检查等待 | 等 postgres healthy 后 backend 自动重试，或 `docker compose restart backend` |
| 企微机器人没反应 | 长连接未订阅成功 或 req_id/消息类型错误 | 见 `wecom.go`：回复必须透传回调 req_id、msgtype 用 markdown |
| 内存不足导致构建失败 | 前端 npm/Go 编译吃内存 | 关闭其他服务或用 4G 实例；也可在本地构建镜像后 `docker save/load` |

---

## 8. 本次部署记录（8.215.92.160）

- 部署时间：2026-08-02
- 访问入口：`http://8.215.92.160:3002`
- 容器：postgres(pgvector:pg16) / meilisearch(v1.8) / backend(Go) / frontend(nginx)
- 企微机器人：已连接（BotID 见 `.env` / `/root/.ekh_secrets`），订阅成功、心跳正常
- 密钥位置：`/root/.ekh_secrets`（root 600）
- compose 备份：`/opt/enterprise-knowledge-hub/docker-compose.yml.bak`
