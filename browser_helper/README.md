# browser_helper

这是一个宿主机运行的可选 page-backed chat helper。

当前定位：

- 固定一组 browser process，每个 process 绑定一个固定代理出口
- 每次请求创建 fresh context/page
- 用真实页面 DOM `fill + send` 触发 chat 流程
- 主代理 `zai2api` 通过 `CHAT_BACKEND=browser_helper` 调它

当前能力边界：

- 只支持 text-only 请求
- 不支持多模态
- 不支持 tools
- 当前返回是“缓冲后的完整上游 SSE 文本”，不是逐 chunk 实时转发

## 环境变量

- `HELPER_AUTH_TOKEN`
- `HELPER_PROXY_TEMPLATE`
- `HELPER_PROXY_BUCKETS`
- `HELPER_SLOT_COUNT`
- `HELPER_PROXY_BUCKET_PREFIX`
- `HELPER_PROXY_URLS_JSON`
- `HELPER_HEADLESS`
- `HELPER_REQUEST_TIMEOUT_SECONDS`
- `HELPER_SLOT_REQUEST_TIMEOUT_SECONDS`
- `HELPER_PAGE_WAIT_MS`
- `HELPER_PROCESS_RECYCLE_REQUESTS`
- `HELPER_PROCESS_MAX_AGE_SECONDS`
- `HELPER_SLOT_COOLDOWN_SECONDS`
- `HELPER_SLOT_HARD_COOLDOWN_SECONDS`
- `HELPER_PROXY_PREFLIGHT_ENABLED`
- `HELPER_PROXY_PREFLIGHT_TIMEOUT_SECONDS`
- `HELPER_PROXY_PREFLIGHT_MAX_ATTEMPTS`

示例：

```bash
set HELPER_AUTH_TOKEN=helper-secret
set HELPER_PROXY_TEMPLATE=http://fast.{bucket}:highkay1844@192.168.1.18:2260
set HELPER_SLOT_COUNT=4
set HELPER_PROXY_BUCKET_PREFIX=user
python -m uvicorn browser_helper.app:app --host 127.0.0.1 --port 39090
```

然后把主代理切到：

```json
{
  "chat_backend": "browser_helper",
  "browser_helper_url": "http://host.docker.internal:39090/v1/browser-chat/completions"
}
```

## 当前轮换策略

- 固定数量的 browser process 作为 warm slot
- slot 数量可通过 `HELPER_SLOT_COUNT` 配置；如果显式提供 `HELPER_PROXY_BUCKETS`，则以列表长度为准
- 每次请求创建 fresh context/page
- 轻量 proxy preflight 先筛掉明显坏桶
- preflight 会先验证代理能否连通 `https://chat.z.ai/api/config`，并记录出口 IP
- 真正 chat warmup 使用更长的 `HELPER_SLOT_REQUEST_TIMEOUT_SECONDS`
- 如果 slot 失败：
  - 当前请求会切到剩余 slot 继续尝试
  - 原 slot 会进入 cooldown
  - 如果代理来自 `HELPER_PROXY_TEMPLATE` 且包含 `{bucket}`，重建时会自动换成新的 bucket 身份，而不是回到旧的 `user_n`

`/healthz` 里会暴露：

- `available_count`
- `cooling_count`
- `busy_count`
- 每个 slot 的：
  - `proxy_url`
  - `bucket_name`
  - `last_error_code`
  - `last_result_status`
  - `last_preflight_ok`
  - `last_preflight_ip`
  - `last_preflight_status`
  - `cooldown_remaining_seconds`

## Docker

推荐方案是单独把 helper 做成一个 Python + Chromium 容器，而不是往现有 Go 主服务镜像里硬塞 Chrome：

- 主服务继续使用当前轻量 Go 镜像
- `browser-helper` 使用官方 Playwright Python 基础镜像
- 容器里安装 `patchright` 并执行 `patchright install chromium`
- `docker-compose.yml` 里通过单独的 `browser-helper` service 暴露 `39090`

这样做的原因：

- Chromium 依赖链和系统库比较重，不适合塞进当前 Alpine Go 镜像
- helper 与主服务故障域分离，方便单独重启和观察 `/healthz`
- Patchright 实际需要的是稳定的 Chromium 运行时，不是必须装 Google Chrome stable
