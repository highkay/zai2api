# zai2api

将 Z.AI 转换为 OpenAI 兼容 API 的代理服务。

[![Build](https://github.com/highkay/zai2api/actions/workflows/build.yml/badge.svg)](https://github.com/highkay/zai2api/actions/workflows/build.yml)
[![License: GPL v3](https://img.shields.io/badge/License-GPLv3-blue.svg)](https://www.gnu.org/licenses/gpl-3.0)

## 功能特性

- **OpenAI 兼容 API** - 支持 `/v1/chat/completions`、`/v1/models` 和 `/v1/tokens`
- **轻量控制台** - 提供 `/console` 查看调用统计、模型统计和管理上游 token
- **多模型支持** - 内置常用模型别名，并自动从云端同步最新模型列表
- **流式响应** - 支持 SSE 流式输出
- **工具调用** - 支持 Function Calling
- **多模态** - 支持图片输入
- **思考模式** - 支持 Thinking 模型的思考过程处理
- **Token 管理** - 自动刷新并轮换上游 Token，提供兼容 `data/tokens.txt` 的 CRUD API
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

1. 推荐：把 token 写入 `data/tokens.txt`
2. 或者：在 `.env` 中设置 `BACKUP_TOKEN`

当前版本不再提供匿名 token 或自动注册 fallback。如果 `data/tokens.txt` 和 `BACKUP_TOKEN` 都为空，`/v1/chat/completions` 会返回 `503 upstream_token_unavailable`。

### 运行

```bash
./zai2api
```

服务将在 `http://localhost:8000` 启动。

控制台地址是 `http://localhost:8000/console`。页面本身不需要额外构建；Token 管理操作会要求输入 `.env` 中配置的 `AUTH_TOKEN`。

## API 端点

| 端点 | 方法 | 描述 |
|------|------|------|
| `/` | GET | 服务状态和遥测数据 |
| `/console` | GET | 精简控制台，查看调用统计并管理上游 token |
| `/v1/models` | GET | 获取可用模型列表 |
| `/v1/tokens` | `GET` / `POST` / `PUT` / `DELETE` | 管理 `data/tokens.txt` 中的上游 token |
| `/v1/chat/completions` | POST | 聊天补全接口 |

## 配置项

| 配置项 | 默认值 | 描述 |
|--------|--------|------|
| `PORT` | 8000 | 服务端口 |
| `AUTH_TOKEN` | - | API 认证令牌（支持多个，逗号分隔） |
| `BACKUP_TOKEN` | - | 备用上游令牌（支持多个，逗号分隔） |
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

列出当前 `data/tokens.txt` 中的 token：

```bash
curl http://localhost:8000/v1/tokens \
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

删除一个 token：

```bash
curl "http://localhost:8000/v1/tokens?token=your-zai-token" \
  -X DELETE \
  -H "Authorization: Bearer your-api-key"
```

## 上游 Token 规则

- 主上游 token 池来自 `data/tokens.txt`，支持历史格式 `token=...`、空行和注释行。
- `BACKUP_TOKEN` 会并入同一个内存池，但只有文件池 token 会被 `/v1/tokens` 直接增删改。
- `/v1/tokens` 直接管理同一份 `data/tokens.txt`，不会引入额外数据库。
- Token 刷新使用 z.ai Web 侧滚动会话机制：`GET https://chat.z.ai/api/v1/auths/` 返回新 token 后会替换旧 token，并把文件来源的新 token 写回 `data/tokens.txt`。
- 临时网络失败不会删除 token；只有上游明确返回 `401/403` 时才会判定 token 失效。
- 当前版本没有匿名 token 获取，也没有自动注册链路。
- 当没有任何上游 token 可用时，`/v1/chat/completions` 会返回 `503`。

## 控制台

`/console` 是一个无前端构建步骤的单页控制台，直接由 Go 服务返回。

- 统计区读取 `/` 的遥测数据，展示总调用、成功调用、失败调用、成功率、RPM、Token 用量和模型维度统计。
- Token 区使用 `/v1/tokens`，支持列出、批量新增和删除 `data/tokens.txt` 中的上游 token。
- `AUTH_TOKEN` 只保存在当前浏览器的 `localStorage`，请求时作为 `Authorization: Bearer ...` 发送给受保护接口。

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
│   ├── token_api.go      # tokens.txt 管理 API
│   ├── token_manager.go  # Token 管理
│   ├── tools.go          # 工具调用
│   └── ...
├── .env.example          # 配置示例
├── Dockerfile
├── docker-compose.yml
└── README.md
```

## 许可证

本项目采用 [GNU General Public License v3.0](LICENSE) 许可证。
