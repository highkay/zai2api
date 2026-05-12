# zai2api Agent Notes

## 项目定位

`zai2api` 是一个用 Go 实现的 OpenAI 兼容代理层。它把外部客户端发来的 OpenAI 风格请求转换成 z.ai Web 侧实际使用的请求格式，再把上游返回的 SSE/JSON 重新整理为 OpenAI SDK 可消费的响应。

本文档只描述主代理服务本身：HTTP 入口、请求转换、上游转发、响应适配、模型系统和运行配置。任何非主代理链路的历史辅助代码都不作为这里的项目主线。

## 主要功能

- 提供 OpenAI 兼容接口：
  - `GET /`
  - `GET /console`
  - `GET /v1/models`
  - `GET/POST/PUT/DELETE /v1/tokens`
  - `POST /v1/chat/completions`
- 兼容流式和非流式聊天补全返回
- 将 OpenAI `messages` 转换为 z.ai Web 上游消息格式
- 支持模型映射、思考模式、搜索模式和动态模型同步
- 支持工具调用适配：把 OpenAI tools 注入为提示协议，再将模型输出提取回 OpenAI `tool_calls`
- 支持图片/视频输入：先上传到 z.ai 文件接口，再把文件 ID 回填到上游消息
- 支持基于 SQLite token ledger 的 token 管理 API，并通过 `/api/v1/auths/` 滚动刷新 z.ai bearer
- 提供精简 `/console` 控制台，展示调用统计、成功/失败数量、模型统计和 token ledger 管理
- 维护遥测、日志、请求重试和基础服务状态输出

## 技术栈

- 语言：Go `1.25`
- HTTP 服务：标准库 `net/http`
- 上游 HTTP/TLS 指纹模拟：
  - `github.com/bogdanfinn/tls-client`
  - `github.com/bogdanfinn/fhttp`
- 配置加载：`github.com/joho/godotenv`
- SQLite token ledger：`modernc.org/sqlite`
- ID 生成：`github.com/google/uuid`
- CI：GitHub Actions，按 `linux/windows/darwin` 和 `amd64/arm64` 交叉构建

## 关键文件

- `cmd/main.go`
  - 服务入口
  - 初始化配置、日志、上游 token 管理、前端版本同步、模型同步
  - 注册 `/`、`/console`、`/v1/models`、`/v1/tokens`、`/v1/chat/completions`
- `internal/console.go`
  - 返回无构建步骤的控制台单页 HTML
  - 页面复用 `/` 遥测数据和 `/v1/tokens` 受保护 CRUD API
- `internal/chat.go`
  - 主请求处理入口 `HandleChatCompletions`
  - 上游请求构造 `makeUpstreamRequest`
  - 流式/非流式响应适配
  - `/v1/models` 的处理函数 `HandleModels`
- `internal/token_api.go`
  - SQLite token ledger 的增删改查 API
- `internal/models.go`
  - OpenAI 请求结构和响应结构定义
  - 消息内容解析
  - 多模态消息转上游消息
- `internal/model_fetcher.go`
  - 内置模型映射
  - z.ai 模型列表拉取
  - 动态模型注册与后缀扩展
- `internal/tools.go`
  - tools 提示注入
  - assistant/tool 消息改写
  - 工具调用结果提取与回填
- `internal/upload.go`
  - 图片/视频下载、base64 解析、上传到 z.ai 文件接口
- `internal/tls_http.go`
  - 统一 TLS 指纹和浏览器请求头
- `internal/version.go`
  - 抓取 z.ai 前端版本，给上游请求补 `X-FE-Version`
- `internal/config.go`
  - `.env` 配置加载
- `internal/token_manager.go`
  - 上游 token 加载、校验、轮询和统计
- `internal/token_store.go`
  - SQLite token ledger、legacy 文件导入和状态持久化
- `internal/telemetry.go`
  - 请求量和 token 用量统计

## 启动流程

服务启动入口在 `cmd/main.go`，顺序是：

1. `LoadConfig()` 读取 `.env`
2. `InitLogger()` 初始化日志
3. `GetTokenManager().Start()` 启动上游 token 管理器
4. `StartVersionUpdater()` 定时抓取 z.ai 前端版本号
5. `StartModelFetcher()` 初始化内置模型并周期拉取最新模型列表
6. 注册 HTTP 路由和中间件
7. `http.ListenAndServe(":"+Cfg.Port, nil)` 启动服务

## 核心代码流程

### 1. 客户端请求进入

`POST /v1/chat/completions` 由 `internal.HandleChatCompletions` 处理。

处理顺序：

1. 校验请求方法必须为 `POST`
2. 从 `Authorization: Bearer ...` 读取客户端 API Key
3. 按 `AUTH_TOKEN` / `SKIP_AUTH_TOKEN` 规则做 OpenAI 风格鉴权
4. 取一个当前可用的上游 z.ai token
   - 优先 SQLite token ledger 中的 `active` token
   - 其次启动时由 `BACKUP_TOKEN` 导入的 `env_backup` 管理副本
   - 如果两者都没有，直接返回 `503 upstream_token_unavailable`
5. 解析请求体为 `ChatRequest`
6. 补默认模型 `GLM-5.1`
7. 通过 `IsValidModel` / `GetUpstreamConfig` 校验模型是否可用
8. 从 `messages` 中提取图片和视频 URL，识别是否是多模态请求
9. 如果传入 `tools`，先经 `ProcessMessagesWithTools` 注入工具协议
10. 统计输入 token，生成本次 completion ID
11. 带重试地调用 `makeUpstreamRequest`
12. 根据 `stream` 分流到：
  - `handleStreamResponseWithRetry`
  - `handleNonStreamResponseWithRetry`
13. 记录 telemetry 和 token 调用结果

### 2. OpenAI 请求转 z.ai Web 请求

`makeUpstreamRequest` 是核心桥接逻辑。

它会：

1. 基于请求模型查 `GetUpstreamConfig`，得到：
  - 实际上游模型 ID
  - 是否开启 thinking
  - 是否开启自动搜索
  - 需要附带的 `mcp_servers`
2. 拼出上游 URL：
  - 默认 `API_ENDPOINT=https://chat.z.ai/api/v2/chat/completions`
  - 当前聊天调用使用裸 `POST` endpoint，不再重放浏览器 query token、`current_url` 或签名参数
3. 若消息包含图片/视频：
  - 调 `UploadImages` / `UploadVideos`
  - 先把媒体上传到 `https://chat.z.ai/api/v1/files/`
  - 建立 `原始 URL -> z.ai file ID` 映射
4. 调每条消息的 `ToUpstreamMessage`，把 OpenAI 内容改成上游可接受结构
5. 组装最终 JSON body：
  - `stream`
  - `model`
  - `messages`
  - 按能力需要附带 `features` / `mcp_servers`
  - 多模态时附带 `files` / `current_user_message_id`
6. 给请求补充 z.ai Web 所需头：
  - `Authorization`
  - `X-FE-Version`
  - `Content-Type`
7. 通过 `TLSHTTPClient()` 发送请求，保持浏览器风格 TLS/HTTP2 指纹

### 3. 流式响应适配

上游返回的是 SSE。`handleStreamResponse` 会逐行扫描 `data: ...` 事件，并把它改写成 OpenAI chunk。

关键适配逻辑：

1. 先发一个带 `delta.role=assistant` 的首块
2. 解析上游 `phase`
3. `thinking` 阶段：
  - 过滤和拼接思考内容
  - 输出到 OpenAI 扩展字段 `reasoning_content`
4. `answer` 阶段：
  - 把 `delta_content` 或 `edit_content` 转成普通 `content`
5. 搜索结果 / 图片搜索 / MCP 片段：
  - 转成 Markdown 引用或可读文本
6. 如果本次启用了 `tools`：
  - 不立即把正文直接发给客户端
  - 等全量内容拼完后运行 `ExtractToolInvocations`
  - 将识别出的调用转成 OpenAI `tool_calls` chunk
7. 最后发送：
  - 带 `finish_reason` 的结束块
  - 可选 usage 块
  - `data: [DONE]`

### 4. 非流式响应适配

非流式路径会消费完整上游 SSE，再汇总成单个 OpenAI JSON：

- `object=chat.completion`
- `choices[0].message.content`
- `choices[0].message.reasoning_content`
- `choices[0].message.tool_calls`
- `usage`
- `system_fingerprint`

## 模型系统

模型能力由两层组成：

1. `internal/model_fetcher.go` 内置 `GLM-5`、`GLM-5-Turbo`、`GLM-5v-Turbo`、`GLM-5.1`
2. 启动后访问 `https://chat.z.ai/api/models` 拉取最新模型，并只补充 `glm-5*` 家族动态映射

模型对外暴露时会自动扩展后缀变体：

- `-thinking`
- `-search`
- `-thinking-search`

因此，`/v1/models` 返回的是“GLM-5 家族基础模型 + GLM-5 家族动态模型 + 后缀变体”的合集。旧的 `4.x` 系列不会再对外暴露。

## 控制台

`GET /console` 返回内置单页控制台，不需要额外前端构建。它只复用现有后端状态源：

- `/`：服务状态、总调用数、成功/失败调用数、成功率、RPM、token 用量、模型维度统计
- `/v1/tokens?status=all`：SQLite token ledger 的列表、批量新增、状态变更和删除

控制台页面本身公开可访问，但 token 管理请求仍走 `AUTH_TOKEN` / `SKIP_AUTH_TOKEN` 规则。页面中的 `AUTH_TOKEN` 默认保存在当前浏览器 `sessionStorage`，只有用户勾选 remember this browser 才写入 `localStorage`。

## 上游 Token 刷新

Z.ai bearer 被当作可滚动续签的会话凭证处理：

1. `TokenManager` 从 SQLite token ledger 加载 `active` token 到内存轮询池。
2. 首次初始化时会从历史 `data/tokens.txt` 导入 active token，从 `data/tokens_invalid.txt` 导入 invalid token；导入只执行一次。
3. `BACKUP_TOKEN` 会导入为 `env_backup` 来源的管理副本，后续刷新结果写入 SQLite。
4. 定时或鉴权失败时调用 `GET https://chat.z.ai/api/v1/auths/`。
5. 如果返回新 token，就写入新的 active 记录，并把旧 token 标为 `rotated`。
6. 临时网络失败只记录检查时间和日志，不删除 token。
7. 只有明确的 `401/403` 会把 token 标为 `invalid` 并移出内存轮询池。

## 工具调用实现方式

这里的 tools 不是直接透传给 z.ai 原生函数调用接口，而是兼容层方案：

1. `GenerateToolPrompt` 把 OpenAI tool schema 注入到 system 指令
2. `ProcessMessagesWithTools` 把历史 assistant/tool 消息改写为模型可理解的文本协议
3. 模型输出后，`ExtractToolInvocations` 从 XML / JSON / 内联函数格式里提取调用
4. 代理层再把它包装回 OpenAI `tool_calls`

改这部分时，必须同时检查：

- 流式 `tool_calls` chunk 输出
- 非流式 `message.tool_calls`
- `tool_choice=none/auto/required`

## 多模态实现方式

图片和视频不会直接把原 URL 交给上游。

真实流程是：

1. 从 OpenAI `messages[].content[]` 中找出 `image_url` / `video_url`
2. 支持两类输入：
  - 普通 URL
  - `data:` base64
3. 下载或解码文件
4. 上传到 z.ai 文件接口
5. 把返回的文件 ID 回填到消息内容
6. 在上游请求体里附带 `files`

所以，任何多模态问题都应同时检查：

- 输入内容解析
- 文件下载/解码
- 文件上传
- URL 与 file ID 映射
- 上游消息回填

## 重要配置

主代理链路常用配置：

- `PORT`
- `API_ENDPOINT`
- `UPSTREAM_PROXY`
- `AUTH_TOKEN`
- `BACKUP_TOKEN`
- `TOKEN_DB_PATH`
- `TOKEN_API_ALLOW_REVEAL`
- `DEBUG_LOGGING`
- `TOOL_SUPPORT`
- `RETRY_COUNT`
- `SKIP_AUTH_TOKEN`
- `SCAN_LIMIT`
- `LOG_LEVEL`
- `NOTE`

补充说明：

- 当前仓库不再包含匿名 token 获取逻辑
- 当前仓库不再包含自动注册/自动发号逻辑

## 修改注意事项

1. 这是一个“协议转换器”，不能只看本地结构，必须同时守住两侧契约：
   - 下游 OpenAI SDK 兼容性
   - 上游 z.ai Web 请求形态
2. 修改聊天主链路时，优先检查这些文件是否一起受影响：
   - `internal/chat.go`
   - `internal/models.go`
   - `internal/model_fetcher.go`
   - `internal/tools.go`
   - `internal/upload.go`
   - `internal/tls_http.go`
   - `internal/version.go`
3. 最新 live 合约结论：
   - `POST https://chat.z.ai/api/v2/chat/completions` 是当前聊天入口，`GET` / `OPTIONS` 仍会 `405`
   - 最小成功调用只要求 `Authorization`、`Content-Type`、`X-FE-Version`
   - `X-Signature`、query token、cookie、`X-Region` 不是当前最小成功调用的硬门槛
   - 不要重放旧浏览器 `captcha_verify_param`， stale 值会触发 `F018` / `F019`
   - 如果部署机器直连 `chat.z.ai` 返回边缘拦截页，应优先验证并配置 `UPSTREAM_PROXY`，不要把这种 405 误判为 OpenAI `/v1` 路由问题
   - 即使上游 `stream=false`，当前返回仍是 `text/event-stream`，下游非流式适配也要按 SSE 消费
4. 改 streaming 逻辑时，必须同步验证：
   - reasoning 内容
   - 普通 content
   - tool_calls
   - usage chunk
   - `[DONE]`
5. 改模型映射时，必须同时检查：
   - `IsValidModel`
   - `GetUpstreamConfig`
   - `/v1/models` 输出
   - thinking/search 后缀行为
6. 改媒体逻辑时，不要只改消息解析；上传接口、`files` 结构和回填映射必须一起验证
7. 改上游请求头或 TLS 逻辑时，`X-FE-Version`、最小 header 契约和 `TLSHTTPClient()` 需要一起看，不能只改单点
8. 改控制台时，不要新增独立状态源；优先复用 `/` 遥测和 `/v1/tokens`，并保持 token 本体在页面上遮罩显示

## 构建与验证

- 本地构建主程序：
  - `go build ./cmd`
- 运行全部测试：
  - `go test ./...`
- Docker Compose 启动：
  - `docker compose up --build -d`
- CI 构建入口：
  - `.github/workflows/build.yml`
  - `.github/workflows/release-tag.yml`

如果未来新增接口或重写主链路，更新本文件时应继续围绕“OpenAI 兼容代理”这条主线，不要把非主服务历史路径重新写回项目总说明。
