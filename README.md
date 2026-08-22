# InnoRAG知识库

多租户企业知识库 MVP：上传文档 → 分块 → 索引 → RAG 问答（带引用）→ 反馈闭环 + 管理后台。

技术栈：Go (Fiber) + PostgreSQL/pgvector + Meilisearch + React (Vite/Tailwind)。AI 可插拔，默认本地离线检索，配置 Key 后自动启用向量检索与 LLM 生成。

## 一键启动（Docker Compose）

**真实一键启动**：compose 内置 PostgreSQL(pgvector 镜像) + Meilisearch + 后端 + 前端 4 个服务，无需在宿主机预装 PG/pgvector。

前置：Docker + Docker Compose（WSL2 下用 `wsl -d <distro> -e docker compose ...`）。

```bash
cp .env.example .env                # 首次：复制默认配置（.env 已被 gitignore）
docker compose up -d --build
```

启动后访问：**http://localhost:3002**（前端，nginx 反代 /api 到 backend）

| 服务 | 容器端口 | 宿主端口 |
| --- | --- | --- |
| 前端 (nginx) | 80 | 3002 |
| 后端 (Fiber) | 8080 | 8081 |
| PostgreSQL 16 (pgvector) | 5432 | 5433 |
| Meilisearch | 7700 | 7701 |

> 注意：宿主端口已避开 obsidian-enterprise 栈（8080/3000/5432/7700）。密钥走环境变量（`.env`，不入库），生产环境务必改默认值。

### 演示账号（SEED_DATA=true 自动创建）

| 角色 | 账号 | 密码 |
| --- | --- | --- |
| 超级管理员 (super_admin) | admin@demo.local | demo123 |
| 知识管理员 (knowledge_admin) | rd@demo.local | rd123 |
| 普通成员 (member) | finance@demo.local | fin123 |
| 普通成员 (member) | sales@demo.local | sales123 |

### 常用命令

```bash
docker compose logs -f backend     # 查看后端日志
docker compose ps                  # 状态
docker compose down                # 停止（保留数据卷）
docker compose down -v             # 停止并清空数据卷
```

## 启用 AI（可选）

默认 `EMBEDDING_PROVIDER` / `LLM_PROVIDER` 留空 = 本地离线 BM25 + 抽取式回答（无需任何外部服务）。

在 `docker-compose.yml` 的 backend 环境变量中填入即可启用：

- **向量检索**：`EMBEDDING_PROVIDER=openai`（或 siliconflow），并填 `EMBEDDING_API_KEY` / `EMBEDDING_MODEL` / `EMBEDDING_BASE_URL`（注意维度须为 1024，否则与库表 `vector(1024)` 不匹配）
- **LLM 生成**：`LLM_PROVIDER=openrouter`（或 deepseek/openai），并填 `LLM_API_KEY` / `LLM_MODEL` / `LLM_BASE_URL`

OpenRouter 示例（只开 LLM，检索保持离线 BM25）：

```yaml
LLM_PROVIDER: "openrouter"
LLM_API_KEY: "sk-or-v1-..."
LLM_MODEL: "openrouter/free"   # OpenRouter 免费模型；也可用 openai/gpt-4o-mini 等付费模型
LLM_BASE_URL: "https://openrouter.ai/api/v1"
```

> 注：OpenRouter 无 embedding 接口，需留空 `EMBEDDING_*`；换用非 1024 维的 embedding 模型需同步改 `models.go` 的 `vector(1024)` 并重建数据库。

改完后 `docker compose up -d backend` 重建生效。

## 用户注册与登录安全

- **注册开关**：`AUTH_ALLOW_REGISTER=false` 关闭公开注册（企业内网建议关闭，由管理员在「成员管理」添加账号）。关闭后前端注册入口会收到 403。
- **登录限流**：`/auth/login` 按 IP 每分钟最多 10 次；服务端按邮箱做失败锁定（默认 5 次失败锁定 15 分钟，`AUTH_LOGIN_MAX_FAILURES` / `AUTH_LOGIN_LOCK_MINUTES`）。
- **密码强度**：默认最少 8 位且必须含字母+数字（`AUTH_MIN_PASSWORD_LEN`），注册与成员创建共用校验。

## 问答流式输出（SSE）

`POST /api/chat/ask` 带 `Accept: text/event-stream` 时走 SSE 流式：先发 `meta`（会话/引用），再持续 `delta`（token），最后 `done`（最终答案+命中判定）。未联网/离线模式会自动回退为一次性返回；nginx 已关闭 `/api` 代理缓冲以支持流式。

## 运维/维护

- **重建索引**：`POST /api/admin/reindex`（管理员）重新处理租户内全部文档，用于 PostgreSQL 与 Meilisearch 不一致（处理中途崩溃）后的对账；历史文档重挂后可用。
- **审计清理**：`AUDIT_RETENTION_DAYS`（默认 180）控制审计日志保留期，启动时与每 24h 自动清理。
- **优雅停机**：后端接收 SIGINT/SIGTERM 后等待在途请求完成（10s 上限）再退出，IM 机器人长连接随根 context 一并回收。

## 联网搜索（可选）

知识库未命中时，Web 端用户可手动开启「联网搜索」重试；回答会附带网络来源链接。

```yaml
WEB_SEARCH_ENABLED: "true"
WEB_SEARCH_API_KEY: "tvly-xxxxxxxxxxxxxxxx"          # Tavily API Key（兼容 Tavily JSON 契约的均可）
WEB_SEARCH_BASE_URL: "https://api.tavily.com/search" # 可选，默认即此
WEB_SEARCH_MAX_RESULTS: "4"                          # 可选，默认 4 条
```

## IM 机器人（可选）

通过长连接（WebSocket）接入 IM 机器人（飞书 / 企业微信），用户在 IM 里 @机器人 提问，回答按绑定账号的角色/部门做权限隔离（与 Web 端一致），无需公网回调地址。

### 平台切换

`docker-compose.yml` 的 backend 环境变量里**同时只能启用一个平台**，切到另一个时把当前平台注释掉：

```yaml
# 飞书
BOT_PLATFORM: "feishu"
FEISHU_APP_ID: "cli_xxxxxxxxxxxxxxxx"
FEISHU_APP_SECRET: "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"

# 企业微信（当前默认）
BOT_PLATFORM: "wecom"
WECOM_BOT_ID: "<企微 BotID，见 .env>"
WECOM_BOT_SECRET: "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
```

- 飞书：开放平台侧需完成添加**机器人**能力 → 权限管理开通 `im:message` 相关权限（如 `im:message.p2p_msg:readonly`、`im:message.group_at_msg:readonly`）→ 事件与回调订阅**长连接**并添加**接收消息**事件（`im.message.receive_v1`）→ 发布新版本。
- 企业微信：管理后台智能机器人开启 **API 模式** 并选择**长连接**方式，拿到 BotID / Secret 填入即可。

### 绑定与授权流程（绑定后按系统角色/部门隔离权限）

所有平台命令一致（无需管理员角色即可用）：

| 命令 | 说明 |
| --- | --- |
| `绑定 <系统邮箱>` | 提交绑定申请（如 `绑定 admin@demo.local`），生成 pending 申请 |
| `批准 <系统邮箱>` | 管理员批准申请 |
| `拒绝 <系统邮箱>` | 管理员拒绝申请 |
| `解绑` | 解除自己的绑定 |
| `绑定状态` / `状态` | 查看当前绑定状态 |

标准流程：

1. 用户在 IM 里给机器人发：`绑定 <系统邮箱>`
2. 管理员批准：Web 管理后台 **「机器人绑定」** 页面批准，或直接在 IM 里发 `批准 <系统邮箱>`
3. 批准后用户直接 @机器人 提问即可；`解绑` 命令可解除绑定

### 分级批准（越权防护）

为防"自己绑定 admin 账号再自己批准"的提权攻击，批准操作做了**角色分级校验**：

- **只有 super_admin 角色可以批准「目标为 super_admin 账号」的绑定**
- 目标为 knowledge_admin / member 的绑定，super_admin 和 knowledge_admin 均可批准
- 校验同时作用于**机器人命令批准**（bot.go / wecom.go 的 `canApproveRole`）和 **Web 后台批准**（bot_handler.go）两条路径
- 绑定申请只校验邮箱存在于系统，**不校验 IM 身份与邮箱的对应关系**：管理员批准前请确认申请人是本人，避免误批导致账号被冒绑

> 首次绑定存在"先有鸡还是先有蛋"问题（批准人须是已绑定管理员），用 Web 后台批准第一条绑定即可。系统按 `message_id`/`msgid` 去重，IM 重复推送不会导致重复回复。回复带 `[回复 HH:MM:SS]` 时间戳便于区分。
>
> 企业微信 `aibot_respond_msg` 的 `headers.req_id` 必须透传消息回调中的 req_id，消息类型用 `markdown`（不支持 `text`）。

## 本地开发

### 后端

Windows 下 Go 模块需走镜像源（官方 proxy.golang.org 不可达）：

```powershell
$env:GOPROXY = "https://goproxy.cn,direct"
cd backend
go run ./cmd/server        # 监听 8080，需本机 postgres/meili 或改 DATABASE_DSN/MEILI_HOST
```

### 前端

```powershell
$env:npm_config_registry = "https://registry.npmmirror.com"
cd frontend
npm install
npm run dev                # Vite dev，/api 代理到 http://localhost:8080
```

## 功能

- **问答**：多会话、KB 范围过滤、**SSE 流式输出**、历史消息、引用溯源（命中分块 + 文档定位链接）、答案满意度反馈（顶/踩 + 留言）、"未命中"标记
  - 中文检索增强：Meilisearch 对中文按字切分、长句常召回为空，后端已内置查询扩展（去停用词 + 中文二元组 + 多候选合并），自然语言长问句可正常命中；LLM 通过结构化标记（`[[HIT:…]]`）如实判定"知识库无相关答案"并标注未命中，不靠关键词猜。
- **知识库**：CRUD、多格式上传（.md/.txt/.pdf/.docx 等）、文档级删除、可见性（公开/仅成员/仅管理员），上传/嵌入并发受控（分批 32/并发 4）
- **成员**：添加、角色切换（super_admin / knowledge_admin / member），密码强度校验，登录失败锁定
- **管理后台**：统计总览（租户/文档/分块/会话/问答，卡片可点击下钻）、检索缺口分析、反馈列表、审计日志、会话记录、机器人绑定审批、重建索引
- **IM 机器人（飞书/企业微信）**：长连接接收 @提问，按绑定账号角色/部门隔离权限，绑定/审批（分级批准，super_admin 绑定仅 super_admin 可批准）/解绑全流程支持

## 测试

```bash
cd backend
go test ./...        # 纯函数单测（分块/可见性/权限矩阵/查询扩展/密码校验）

## 目录结构

```
backend/
  cmd/server/main.go       # 应用装配与路由
  internal/api/            # Fiber handlers
  internal/models/         # GORM 模型
  internal/repositories/   # 数据访问
  internal/services/       # chat / ingest / search / llm / pdf / seed
  internal/database/       # Postgres 连接 + 自动迁移
frontend/
  src/api/client.ts        # API 客户端（token、401 跳转）
  src/stores/auth.ts       # zustand 会话状态
  src/pages/               # Login / Chat / KB / Members / Admin
docker-compose.yml
```
