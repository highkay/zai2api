# zai2api

将 Z.AI 转换为 OpenAI 兼容 API 的代理服务。

[![Build](https://github.com/highkay/zai2api/actions/workflows/build.yml/badge.svg)](https://github.com/highkay/zai2api/actions/workflows/build.yml)
[![License: GPL v3](https://img.shields.io/badge/License-GPLv3-blue.svg)](https://www.gnu.org/licenses/gpl-3.0)

## 功能特性

- **OpenAI 兼容 API** - 支持 `/v1/chat/completions`、`/v1/models` 和 `/v1/tokens`
- **轻量控制台** - 提供 `/console` 查看调用统计、模型统计、运行时代理配置和管理上游 token
- **多模型支持** - 内置常用模型别名，并自动从云端同步最新模型列表
- **流式响应** - 支持 SSE 流式输出
- **工具调用** - 支持 Function Calling
- **多模态** - 支持图片输入
- **思考模式** - 支持 Thinking 模型的思考过程处理
- **Token 管理** - 使用 SQLite token ledger 自动刷新、轮换、隔离和审计上游 Token
- **遥测统计** - 请求计数、Token 统计、成功/失败数量、成功率等

## 快速开始

### 从 Release 下载

前往 [Releases](https://github.com/highkay/zai2api/releases) 下载对应平台的二进制文件。

### 从源码构建

```bash
git clone https://github.com/highkay/zai2api.git
cd zai2api
go build -o zai2api ./cmd/main.go
```

### 使用 Docker Compose

```bash
cp .env.example .env
docker compose up --build -d
```

默认会把本地 `./data` 挂载到容器内 `/app/data`，`PORT` 会同时决定容器监听端口和宿主机映射端口。

### 配置

复制配置文件并修改：

```bash
cp .env.example .env
```

编辑 `.env` 文件，设置必要的配置项：

```env
PORT=8000
AUTH_TOKEN=your-api-key
```

再准备至少一种上游 z.ai token 来源：

1. 推荐：通过 `/console` 或 `/v1/tokens` 写入 SQLite token ledger，默认路径是 `data/tokens.db`
2. 兼容导入：首次启动时会从历史 `data/tokens.txt` 导入 active token，从 `data/tokens_invalid.txt` 导入 invalid token
3. 或者：在 `.env` 中设置 `BACKUP_TOKEN`，启动时会导入为 `env_backup` 来源的管理副本

当前版本不再提供匿名 token 或自动注册 fallback。如果 SQLite token ledger 和 `BACKUP_TOKEN` 都为空，`/v1/chat/completions` 会返回 `503 upstream_token_unavailable`。

### 运行

```bash
./zai2api
```

服务将在 `http://localhost:8000` 启动。

控制台地址是 `http://localhost:8000/console`。页面本身不需要额外构建；运行时配置和 Token 管理操作会要求输入 `.env` 中配置的 `AUTH_TOKEN`。

## API 端点

| 端点 | 方法 | 描述 |
|------|------|------|
| `/` | GET | 服务状态和遥测数据 |
| `/console` | GET | 精简控制台，查看调用统计、运行时配置并管理上游 token |
| `/v1/models` | GET | 获取可用模型列表 |
| `/v1/config` | `GET` / `PUT` / `PATCH` | 查看和更新在线运行时配置 |
| `/v1/tokens` | `GET` / `POST` / `PUT` / `DELETE` | 管理 SQLite token ledger 中的上游 token |
| `/v1/chat/completions` | POST | 聊天补全接口 |

## 配置项

| 配置项 | 默认值 | 描述 |
|--------|--------|------|
| `PORT` | 8000 | 服务端口 |
| `UPSTREAM_PROXY` | - | 上游 z.ai 请求使用的出口代理，支持 `http://`、`https://`、`socks5://`、`socks5h://` |
| `AUTH_TOKEN` | - | API 认证令牌（支持多个，逗号分隔） |
| `BACKUP_TOKEN` | - | 备用上游令牌（支持多个，逗号分隔） |
| `TOKEN_DB_PATH` | `data/tokens.db` | SQLite token ledger 路径 |
| `RUNTIME_CONFIG_PATH` | `data/runtime_config.json` | `/v1/config` 持久化在线运行时配置的文件路径 |
| `TOKEN_API_ALLOW_REVEAL` | false | 是否允许 `GET /v1/tokens?reveal=true` 返回完整 bearer |
| `DEBUG_LOGGING` | false | 调试日志 |
| `TOOL_SUPPORT` | true | 工具调用支持 |
| `RETRY_COUNT` | 5 | 请求失败时的重试次数（不含首次请求） |
| `LOG_LEVEL` | info | 日志级别：debug/info/warn/error |

完整配置请参考 [.env.example](.env.example)

## 使用示例

### cURL

```bash
curl http://localhost:8000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer your-api-key" \
  -d '{
    "model": "GLM-5.1",
    "messages": [{"role": "user", "content": "Hello!"}],
    "stream": true
  }'
```

### Token 管理 API

列出当前 token ledger 中的 token。默认返回所有状态，但不会返回完整 bearer：

```bash
curl "http://localhost:8000/v1/tokens?status=all" \
  -H "Authorization: Bearer your-api-key"
```

新增一个 token：

```bash
curl http://localhost:8000/v1/tokens \
  -H "Authorization: Bearer your-api-key" \
  -H "Content-Type: application/json" \
  -d '{"token":"your-zai-token"}'
```

更新一个 token：

```bash
curl http://localhost:8000/v1/tokens \
  -X PUT \
  -H "Authorization: Bearer your-api-key" \
  -H "Content-Type: application/json" \
  -d '{"old_token":"old-token","new_token":"new-token"}'
```

禁用一个 token。`DELETE` 默认是软删除，会把 token 标为 `disabled` 并从上游轮询池移除；需要物理删除时显式加 `hard=true`：

```bash
curl "http://localhost:8000/v1/tokens?id=1" \
  -X DELETE \
  -H "Authorization: Bearer your-api-key"
```

### 在线配置 API

查看当前运行时配置。代理地址只返回遮罩预览，不回传完整密码：

```bash
curl http://localhost:8000/v1/config \
  -H "Authorization: Bearer your-api-key"
```

在线更新上游出口代理，立即影响新的上游请求，并写入 `RUNTIME_CONFIG_PATH`：

```bash
curl http://localhost:8000/v1/config \
  -X PUT \
  -H "Authorization: Bearer your-api-key" \
  -H "Content-Type: application/json" \
  -d '{"upstream_proxy":"http://user:password@host:port"}'
```

清空代理改为直连：

```bash
curl http://localhost:8000/v1/config \
  -X PUT \
  -H "Authorization: Bearer your-api-key" \
  -H "Content-Type: application/json" \
  -d '{"upstream_proxy":""}'
```

## 上游 Token 规则

- 主上游 token 池来自 SQLite token ledger，默认文件是 `data/tokens.db`。
- 历史 `data/tokens.txt` 和 `data/tokens_invalid.txt` 只在 SQLite ledger 初始化时导入一次，后续不再作为运行态真源。
- `BACKUP_TOKEN` 会导入为 `env_backup` 来源的管理副本，后续刷新结果写入 SQLite；建议最终把长期 token 迁入 ledger。
- `/v1/tokens` 默认返回 `active`、`invalid`、`disabled` 等状态记录，但不会回传完整 bearer。
- Token 刷新使用 z.ai Web 侧滚动会话机制：`GET https://chat.z.ai/api/v1/auths/` 返回新 token 后会写入 SQLite，并自动物理删除旧 token。
- 聊天调用前会尽量先刷新一次上游 token；如果刷新遇到临时网络失败，会继续使用仍处于 active 状态的旧 token。
- 临时网络失败只更新检查时间，不删除 token；只有上游明确返回 `401/403` 时才会判定 token 失效并标为 `invalid`。
- 当前版本没有匿名 token 获取，也没有自动注册链路。
- 当没有任何上游 token 可用时，`/v1/chat/completions` 会返回 `503`。

## 控制台

`/console` 是一个无前端构建步骤的单页控制台，直接由 Go 服务返回。

- 统计区读取 `/` 的遥测数据，展示总调用、成功调用、失败调用、成功率、RPM、Token 用量和模型维度统计。
- 运行时配置区使用 `/v1/config`，可在线切换 `UPSTREAM_PROXY`，代理密码只显示遮罩预览。
- Token 区使用 `/v1/tokens?status=all`，展示 active、invalid、disabled、source、使用次数、刷新时间和失效原因。
- 默认只展示 `token_preview`，不会从 API 拉取完整 bearer；只有 `TOKEN_API_ALLOW_REVEAL=true` 且请求显式 `reveal=true` 时才会返回完整 token。
- `AUTH_TOKEN` 默认只保存在当前浏览器的 `sessionStorage`；勾选 remember this browser 后才会写入 `localStorage`。

### Python (OpenAI SDK)

```python
from openai import OpenAI

client = OpenAI(
    api_key="your-api-key",
    base_url="http://localhost:8000/v1"
)

response = client.chat.completions.create(
    model="GLM-5.1",
    messages=[{"role": "user", "content": "Hello!"}]
)
print(response.choices[0].message.content)
```

## 支持的模型

- 启动后会从云端拉取最新模型列表，并自动补充到 `/v1/models`
- 当前只保留 `GLM-5`、`GLM-5V`、`GLM-5.1` 系列；旧的 `4.x` 系列不会再对外暴露
- 大多数基础模型会自动提供以下后缀变体：
  `-thinking`、`-search`、`-thinking-search`
- 当前已返回的基础模型包括：
  `GLM-5`
  `GLM-5-Turbo`
  `GLM-5v-Turbo`
  `GLM-5.1`

## Z.ai 聊天对接契约

- 当前聊天入口是 `POST https://chat.z.ai/api/v2/chat/completions`；同路径 `GET` / `OPTIONS` 会返回 `405`。
- 当前最小成功 header 是 `Authorization: Bearer <fresh user token>`、`Content-Type: application/json`、`X-FE-Version`。
- 代理不再向聊天接口重放浏览器 query token、cookie、`X-Signature` 或旧 `captcha_verify_param`。
- 如果部署机器直连 `chat.z.ai` 被边缘网络拦截，可设置 `UPSTREAM_PROXY` 让刷新 token、模型同步、文件上传和聊天请求统一走同一个出口。
- 使用本仓库 Docker Compose 时，如果代理在宿主机上，可设置 `UPSTREAM_PROXY=http://host.docker.internal:7890`。
- 即使上游请求体 `stream=false`，z.ai 当前仍返回 `text/event-stream`，所以非流式路径也按 SSE 汇总后再输出 OpenAI JSON。

## 项目结构

```
zai2api/
├── cmd/
│   └── main.go           # 主程序入口
├── internal/
│   ├── chat.go           # 聊天补全处理
│   ├── console.go        # 内置控制台页面
│   ├── config.go         # 配置管理
│   ├── models.go         # 模型定义
│   ├── telemetry.go      # 遥测统计
│   ├── token_api.go      # Token ledger 管理 API
│   ├── token_manager.go  # Token 轮询、刷新和运行态管理
│   ├── token_store.go    # SQLite token ledger
│   ├── tools.go          # 工具调用
│   └── ...
├── .env.example          # 配置示例
├── Dockerfile
├── docker-compose.yml
└── README.md
```

## 许可证

本项目采用 [GNU General Public License v3.0](LICENSE) 许可证。
