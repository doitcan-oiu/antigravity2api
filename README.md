# Antigravity2API

把 Google Antigravity / Cloud Code 账号转成 OpenAI、Claude、Gemini 兼容接口的反代服务。

后端 Go，控制台 React + HeroUI。一次导入多个账号会形成一个批次，购买日起默认 30 天有效，到期后会明确标记，并默认不再进入代理池。

## 功能

- 用 `refresh_token` 管理账号，自动刷新 `access_token`
- 调用 Cloud Code `loadCodeAssist` / `fetchAvailableModels` / `retrieveUserQuotaSummary`
- 兼容 OpenAI Chat、Responses、Legacy Completions、Claude Messages、Gemini；计数接口独立调用 countTokens
- Images 生成/编辑和音频转录（支持范围见下方修复说明）
- 转换成 Cloud Code `v1internal` 的 `generateContent` / `streamGenerateContent`
- 支持流式、工具调用、thinking
- 按模型、配额和进行中请求公平调度账号；429 解析等待提示、短时同号重试、换号及跨请求冷却，冷却到期自动恢复
- 批次导入，可手动选择购买日期，到期时间 = 购买日 + 30 天
- 独立配额：OSS、Gemini Pro、Gemini Flash、Claude

并发设计、协议支持边界和本次修复验证见 [反代修复与运行说明](docs/proxy-hardening.md)。

## 生产环境（Docker）

需要已安装 [Docker](https://docs.docker.com/get-docker/) 和 Docker Compose。

```bash
git clone <你的仓库地址> antigravity2api
cd antigravity2api

cp .env.example .env
# 务必改掉默认密钥
# ADMIN_TOKEN=你的控制台令牌
# API_KEY=你的反代密钥
# PORT=8080

docker compose up --build -d
```

打开 [http://127.0.0.1:8080](http://127.0.0.1:8080)，用 `.env` 里的 `ADMIN_TOKEN` 登录控制台。

数据保存在 `./data`，容器重启不会丢账号。升级：

```bash
git pull
docker compose up --build -d
```

常用命令：

```bash
docker compose logs -f          # 看日志
docker compose ps               # 看状态
docker compose down             # 停止（保留 ./data）
```

## 开发环境

### 方式一：Docker（和生产一致，推荐先这样跑通）

```bash
cp .env.example .env
docker compose up --build
```

改代码后重新 `docker compose up --build`。SQLite 数据仍在 `./data`。

### 方式二：本地热更新

需要 Node.js 22+、pnpm、Go 1.23+。

```bash
cp .env.example .env

# 终端 1：前端
cd web
pnpm install
pnpm dev

# 终端 2：后端（首次或前端有改动时先构建一次嵌入资源）
cd web && pnpm build && cd ../server
set -a && source ../.env && set +a
go run .
```

- 前端开发地址：http://127.0.0.1:5173 ，会把 `/api`、`/v1` 等请求代理到后端
- 后端地址：http://127.0.0.1:8080
- 本地 `go run` 依赖 `web` 构建产物嵌入到 `server/web`，所以改完前端要再执行一次 `pnpm build`

也可以一键构建后运行：

```bash
make run
```

## 登录和密钥

这是两套不同的密钥，不要混用。

| 用途 | 变量 | 默认值 | 用在哪 |
| --- | --- | --- | --- |
| 控制台登录 | `ADMIN_TOKEN` | `admin-token` | 浏览器打开后台 |
| 接口调用 | `API_KEY` | `sk-antigravity` | OpenAI / Claude / Gemini 客户端 |

客户端请求头：

```
Authorization: Bearer <API_KEY>
```

如果提示 `unauthorized`，通常是把控制台令牌当成了接口密钥，或 `.env` 改完没有重建/重启容器。

## 调用地址

把客户端的 Base URL 指到本服务：

- OpenAI：`http://127.0.0.1:8080/v1`
- Claude：`http://127.0.0.1:8080`
- Gemini：`http://127.0.0.1:8080/v1beta`

生产环境把主机和端口换成你的服务器即可。

## 导入账号

同一批次可粘贴：

- JSON 数组，对象里包含 `refresh_token`
- 每行一个 `1//` 开头的 token
- 任意文本，自动提取 `1//` token

创建批次时可手动选择购买日期，默认今天。到期时间 = 购买日期 + 30 天。导入后会换取邮箱、项目 ID 和配额。

## 环境变量

复制 `.env.example` 为 `.env` 后修改。Docker Compose 会读取同目录下的 `.env`。

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `PORT` | `8080` | 宿主机端口（仅 Docker Compose） |
| `LISTEN_ADDR` | `:8080` | 容器/进程监听地址 |
| `DATA_DIR` | `./data` | SQLite 数据目录，Docker 中固定为 `/app/data` |
| `ADMIN_TOKEN` | `admin-token` | 控制台登录令牌 |
| `API_KEY` | `sk-antigravity` | 反代调用密钥 |
| `BATCH_VALIDITY_DAYS` | `30` | 批次有效天数 |
| `SKIP_EXPIRED_ACCOUNTS` | `true` | 过期账号不进入代理池 |
| `MAX_CONCURRENT_REQUESTS` | `128` | 单进程活跃 API 请求上限，最大 4096；含 SSE 和计数请求 |
| `MAX_CONCURRENT_PER_ACCOUNT` | `4` | 单账号同时进行的上游请求上限 |
| `MAX_RETRY_ATTEMPTS` | `5` | 账号轮换尝试上限，最大 20；短时重试和 401 刷新各账号最多额外一次 |
| `ADMISSION_TIMEOUT_SECONDS` | `5` | 入口等待并发名额的最长秒数 |
| `REQUEST_TIMEOUT_SECONDS` | `600` | 一条已受理代理请求的总超时，含选号、重试和流读取 |
| `SHUTDOWN_TIMEOUT_SECONDS` | `30` | 停机时等待在途请求的最长秒数 |

生产环境请务必修改 `ADMIN_TOKEN` 和 `API_KEY`。
