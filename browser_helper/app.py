import json
import os
import queue
import re
import threading
import time
from dataclasses import dataclass, field
from typing import Any, Optional
from urllib.parse import urlparse

import requests
from fastapi import FastAPI, Header, HTTPException
from fastapi.responses import JSONResponse, PlainTextResponse
from patchright.sync_api import Browser, BrowserContext, Error as PlaywrightError, sync_playwright
from pydantic import BaseModel, Field


TARGET_URL = "https://chat.z.ai/"
IP_CHECK_URL = "https://api.ipify.org?format=json"
CONFIG_CHECK_URL = "https://chat.z.ai/api/config"


def env(name: str, default: str = "") -> str:
    return os.environ.get(name, default).strip()


def env_int(name: str, default: int) -> int:
    raw = os.environ.get(name, "").strip()
    if not raw:
        return default
    try:
        return int(raw)
    except ValueError:
        return default


def parse_proxy_urls() -> list[str]:
    explicit = env("HELPER_PROXY_URLS_JSON")
    if explicit:
        data = json.loads(explicit)
        return [str(item).strip() for item in data if str(item).strip()]

    template = env("HELPER_PROXY_TEMPLATE")
    buckets_raw = env("HELPER_PROXY_BUCKETS", "user_1,user_2,user_3,user_4")
    buckets = [item.strip() for item in buckets_raw.split(",") if item.strip()]
    if not template:
        raise RuntimeError("HELPER_PROXY_TEMPLATE or HELPER_PROXY_URLS_JSON is required")
    return [template.replace("{bucket}", bucket) for bucket in buckets]


def proxy_template_value() -> str:
    return env("HELPER_PROXY_TEMPLATE")


def slot_count() -> int:
    return max(1, env_int("HELPER_SLOT_COUNT", 4))


def proxy_bucket_name_stem() -> str:
    raw = env("HELPER_PROXY_BUCKET_PREFIX", "user")
    if not raw:
        return "user_"
    if raw[-1].isalnum():
        return raw + "_"
    return raw


def proxy_bucket_values() -> list[str]:
    buckets_raw = env("HELPER_PROXY_BUCKETS")
    if buckets_raw:
        return [item.strip() for item in buckets_raw.split(",") if item.strip()]
    stem = proxy_bucket_name_stem()
    return [f"{stem}{idx}" for idx in range(1, slot_count() + 1)]


def proxy_settings(raw: str) -> dict[str, str]:
    parsed = urlparse(raw)
    if not parsed.scheme or not parsed.hostname:
        raise RuntimeError(f"invalid proxy URL: {raw}")
    server = f"{parsed.scheme}://{parsed.hostname}"
    if parsed.port:
        server += f":{parsed.port}"
    settings = {"server": server}
    if parsed.username:
        settings["username"] = parsed.username
    if parsed.password:
        settings["password"] = parsed.password
    return settings


def browser_request_auth_token() -> str:
    return env("HELPER_AUTH_TOKEN")


def browser_launch_timeout_ms() -> int:
    return env_int("HELPER_BROWSER_TIMEOUT_MS", 120000)


def page_wait_ms() -> int:
    return env_int("HELPER_PAGE_WAIT_MS", 6000)


def request_timeout_seconds() -> int:
    return env_int("HELPER_REQUEST_TIMEOUT_SECONDS", 90)


def slot_request_timeout_seconds() -> int:
    return env_int("HELPER_SLOT_REQUEST_TIMEOUT_SECONDS", 20)


def process_recycle_requests() -> int:
    return env_int("HELPER_PROCESS_RECYCLE_REQUESTS", 20)


def process_max_age_seconds() -> int:
    return env_int("HELPER_PROCESS_MAX_AGE_SECONDS", 3600)


def slot_cooldown_seconds() -> int:
    return env_int("HELPER_SLOT_COOLDOWN_SECONDS", 90)


def slot_hard_cooldown_seconds() -> int:
    return env_int("HELPER_SLOT_HARD_COOLDOWN_SECONDS", 300)


def proxy_preflight_enabled() -> bool:
    return env("HELPER_PROXY_PREFLIGHT_ENABLED", "true").lower() != "false"


def proxy_preflight_timeout_seconds() -> float:
    raw = env("HELPER_PROXY_PREFLIGHT_TIMEOUT_SECONDS", "10")
    try:
        return float(raw)
    except ValueError:
        return 10.0


def proxy_preflight_max_attempts() -> int:
    return env_int("HELPER_PROXY_PREFLIGHT_MAX_ATTEMPTS", 4)


class UpstreamChatPayload(BaseModel):
    stream: bool
    model: str
    messages: list[dict[str, Any]]
    features: dict[str, Any] | None = None
    mcp_servers: list[str] | None = None
    files: list[dict[str, Any]] | None = None
    current_user_message_id: str | None = None


class BrowserChatRequest(BaseModel):
    token: str
    payload: UpstreamChatPayload


def supported_prompt_from_payload(payload: UpstreamChatPayload) -> str:
    if payload.files:
        raise ValueError("browser helper does not support multimodal payloads yet")
    parts: list[str] = []
    for message in payload.messages:
        role = str(message.get("role", "")).strip().lower()
        content = message.get("content", "")
        if isinstance(content, list):
            raise ValueError("browser helper only supports text-only messages")
        if not isinstance(content, str):
            raise ValueError("browser helper only supports string message content")
        text = content.strip()
        if not text:
            continue
        if role == "system":
            parts.append(f"System instructions:\n{text}")
        elif role == "user":
            parts.append(text if len(parts) == 0 else f"User:\n{text}")
        else:
            raise ValueError(f"browser helper does not support role {role!r} yet")
    if not parts:
        raise ValueError("browser helper could not derive a prompt from messages")
    return "\n\n".join(parts)


@dataclass
class SlotResult:
    ok: bool
    status_code: int
    content_type: str
    body: str
    error_code: str = ""
    error_message: str = ""
    slot_id: str = ""
    proxy_url: str = ""
    debug: dict[str, Any] = field(default_factory=dict)


@dataclass
class BrowserSlot:
    slot_id: str
    proxy_url: str
    browser: Browser
    created_at: float
    proxy_template: str = ""
    bucket_name: str = ""
    requests_served: int = 0
    consecutive_failures: int = 0
    last_error: str = ""
    last_error_code: str = ""
    last_result_status: int = 0
    last_result_at: float = 0.0
    last_success_at: float = 0.0
    last_failure_at: float = 0.0
    cooldown_until: float = 0.0
    last_preflight_ok: bool = False
    last_preflight_ip: str = ""
    last_preflight_status: int = 0
    last_preflight_at: float = 0.0
    busy: bool = False

    def should_recycle(self) -> bool:
        if self.requests_served >= process_recycle_requests():
            return True
        if time.time() - self.created_at >= process_max_age_seconds():
            return True
        return False

    def cooldown_remaining_seconds(self, now: float | None = None) -> int:
        current = now if now is not None else time.time()
        if self.cooldown_until <= current:
            return 0
        return int(self.cooldown_until - current + 0.999)


@dataclass
class SlotFailurePolicy:
    recycle: bool
    cooldown_seconds: int = 0
    rotate_identity: bool = False


def extract_json_string_field(text: str, field_name: str) -> str:
    pattern = rf'"{re.escape(field_name)}"\s*:\s*"([^"\\]*(?:\\.[^"\\]*)*)"'
    match = re.search(pattern, text)
    if not match:
        return ""
    try:
        return json.loads(f'"{match.group(1)}"')
    except json.JSONDecodeError:
        return match.group(1)


def is_upstream_edge_blocked_response(status_code: int, content_type: str, body: str) -> bool:
    if status_code != 405:
        return False
    content_type_lower = (content_type or "").lower()
    body_lower = (body or "")[:1000].lower()
    return (
        "text/html" in content_type_lower
        or "errors.aliyun.com" in body_lower
        or "<title>405</title>" in body_lower
        or "<!doctypehtml" in body_lower
    )


def is_frontend_captcha_required(body: str) -> bool:
    body_upper = (body or "").upper()
    body_lower = (body or "").lower()
    if "FRONTEND_CAPTCHA_REQUIRED" in body_upper:
        return True
    if "captcha_error_type" in body_lower and "missing_param" in body_lower:
        return True
    if "captcha" in body_lower and ("f018" in body_lower or "f019" in body_lower):
        return True
    return False


def classify_chat_response(slot: BrowserSlot, chat_response: dict[str, Any], network_events: list[dict[str, Any]], verify_seen: bool) -> SlotResult | None:
    status_code = int(chat_response.get("status") or 0)
    content_type = str(chat_response.get("content_type") or "")
    body = str(chat_response.get("text_prefix") or "")
    if is_upstream_edge_blocked_response(status_code, content_type, body):
        return SlotResult(
            ok=False,
            status_code=502,
            content_type="application/json",
            body=json.dumps({"error": {"message": "upstream edge blocked current proxy bucket", "code": "upstream_edge_blocked"}}),
            error_code="upstream_edge_blocked",
            error_message="upstream edge blocked current proxy bucket",
            slot_id=slot.slot_id,
            proxy_url=slot.proxy_url,
            debug={"verify_seen": verify_seen, "network": network_events},
        )
    if is_frontend_captcha_required(body):
        message = extract_json_string_field(body, "detail") or extract_json_string_field(body, "message") or "chat captcha required in browser session"
        return SlotResult(
            ok=False,
            status_code=403,
            content_type="application/json",
            body=json.dumps({"error": {"message": message, "code": "frontend_captcha_required"}}),
            error_code="frontend_captcha_required",
            error_message=message,
            slot_id=slot.slot_id,
            proxy_url=slot.proxy_url,
            debug={"verify_seen": verify_seen, "network": network_events},
        )
    if status_code >= 400:
        message = extract_json_string_field(body, "detail") or extract_json_string_field(body, "message") or f"browser helper observed completion status {status_code}"
        return SlotResult(
            ok=False,
            status_code=502,
            content_type="application/json",
            body=json.dumps({"error": {"message": message, "code": "browser_helper_completion_failed"}}),
            error_code="browser_helper_completion_failed",
            error_message=message,
            slot_id=slot.slot_id,
            proxy_url=slot.proxy_url,
            debug={"verify_seen": verify_seen, "network": network_events},
        )
    return None


def failure_policy_for_result(result: SlotResult) -> SlotFailurePolicy:
    if result.ok:
        return SlotFailurePolicy(recycle=False, cooldown_seconds=0, rotate_identity=False)

    code = (result.error_code or "").strip().lower()
    if code in {"browser_helper_auth_failed", "browser_helper_unsupported_request"}:
        return SlotFailurePolicy(recycle=False, cooldown_seconds=0, rotate_identity=False)
    if code in {"upstream_edge_blocked", "browser_helper_no_completion"}:
        return SlotFailurePolicy(recycle=True, cooldown_seconds=slot_hard_cooldown_seconds(), rotate_identity=True)
    if code in {"frontend_captcha_required", "browser_helper_captcha_stuck"}:
        return SlotFailurePolicy(recycle=True, cooldown_seconds=slot_cooldown_seconds(), rotate_identity=True)
    if code in {"browser_helper_playwright_error", "browser_helper_send_disabled", "browser_helper_completion_failed"}:
        return SlotFailurePolicy(recycle=True, cooldown_seconds=slot_cooldown_seconds(), rotate_identity=True)
    return SlotFailurePolicy(recycle=True, cooldown_seconds=slot_cooldown_seconds(), rotate_identity=True)


class SlotPool:
    def __init__(self, playwright) -> None:
        self._playwright = playwright
        self._lock = threading.Lock()
        self._cond = threading.Condition(self._lock)
        self._slots: list[BrowserSlot] = []
        self._next_index = 0
        self._bucket_name_stem = self._infer_bucket_name_stem()
        self._bucket_counter = self._infer_bucket_counter_start()
        explicit = env("HELPER_PROXY_URLS_JSON")
        if explicit:
            for idx, proxy_url in enumerate(parse_proxy_urls(), start=1):
                slot = self._launch_slot(f"slot-{idx}", proxy_url)
                self._slots.append(slot)
            return

        template = proxy_template_value()
        buckets = proxy_bucket_values()
        for idx, bucket_name in enumerate(buckets, start=1):
            proxy_url = template.replace("{bucket}", bucket_name)
            slot = self._launch_slot(f"slot-{idx}", proxy_url, proxy_template=template, bucket_name=bucket_name)
            self._slots.append(slot)

    def _infer_bucket_counter_start(self) -> int:
        highest = 0
        for bucket_name in proxy_bucket_values():
            match = re.search(r"(\d+)$", bucket_name)
            if match:
                highest = max(highest, int(match.group(1)))
        return max(highest + 1, 1)

    def _infer_bucket_name_stem(self) -> str:
        for bucket_name in proxy_bucket_values():
            match = re.search(r"^(.*?)(\d+)$", bucket_name)
            if match:
                return match.group(1)
        return proxy_bucket_name_stem()

    def _next_bucket_name_locked(self) -> str:
        bucket_name = f"{self._bucket_name_stem}{self._bucket_counter}"
        self._bucket_counter += 1
        return bucket_name

    def _preflight_proxy(self, proxy_url: str) -> dict[str, Any]:
        started = time.time()
        result = {
            "ok": False,
            "exit_ip": "",
            "config_status": 0,
            "config_type": "",
            "error": "",
            "checked_at": time.time(),
        }
        proxies = {
            "http": proxy_url,
            "https": proxy_url,
        }
        timeout_seconds = proxy_preflight_timeout_seconds()
        try:
            ip_resp = requests.get(IP_CHECK_URL, proxies=proxies, timeout=timeout_seconds)
            if ip_resp.ok:
                try:
                    result["exit_ip"] = str(ip_resp.json().get("ip", "")).strip()
                except Exception:
                    result["exit_ip"] = ip_resp.text[:200]
            config_resp = requests.get(CONFIG_CHECK_URL, proxies=proxies, timeout=timeout_seconds)
            result["config_status"] = config_resp.status_code
            result["config_type"] = config_resp.headers.get("content-type", "")
            body_prefix = config_resp.text[:200]
            result["ok"] = ip_resp.ok and config_resp.ok and "completion_version" in body_prefix
            if not result["ok"]:
                result["error"] = body_prefix
        except Exception as exc:  # noqa: BLE001
            result["error"] = str(exc)
        result["elapsed_ms"] = int((time.time() - started) * 1000)
        return result

    def _launch_slot(self, slot_id: str, proxy_url: str, proxy_template: str = "", bucket_name: str = "") -> BrowserSlot:
        preflight = {
            "ok": False,
            "exit_ip": "",
            "config_status": 0,
            "checked_at": 0.0,
        }
        target_proxy_url = proxy_url
        target_bucket_name = bucket_name
        attempts = proxy_preflight_max_attempts() if proxy_template and "{bucket}" in proxy_template else 1
        if proxy_preflight_enabled():
            for attempt in range(attempts):
                preflight = self._preflight_proxy(target_proxy_url)
                if preflight["ok"]:
                    break
                if not (proxy_template and "{bucket}" in proxy_template) or attempt == attempts - 1:
                    break
                target_bucket_name = self._next_bucket_name_locked()
                target_proxy_url = proxy_template.replace("{bucket}", target_bucket_name)

        browser = self._playwright.chromium.launch(
            headless=env("HELPER_HEADLESS", "true").lower() != "false",
            proxy=proxy_settings(target_proxy_url),
        )
        return BrowserSlot(
            slot_id=slot_id,
            proxy_url=target_proxy_url,
            browser=browser,
            created_at=time.time(),
            proxy_template=proxy_template,
            bucket_name=target_bucket_name,
            last_preflight_ok=bool(preflight["ok"]),
            last_preflight_ip=str(preflight["exit_ip"]),
            last_preflight_status=int(preflight["config_status"] or 0),
            last_preflight_at=float(preflight["checked_at"] or 0.0),
        )

    def _replace_slot_locked(self, old_slot: BrowserSlot, rotate_identity: bool) -> BrowserSlot:
        try:
            old_slot.browser.close()
        except Exception:
            pass
        proxy_url = old_slot.proxy_url
        bucket_name = old_slot.bucket_name
        if rotate_identity and old_slot.proxy_template and "{bucket}" in old_slot.proxy_template:
            bucket_name = self._next_bucket_name_locked()
            proxy_url = old_slot.proxy_template.replace("{bucket}", bucket_name)
        new_slot = self._launch_slot(
            old_slot.slot_id,
            proxy_url,
            proxy_template=old_slot.proxy_template,
            bucket_name=bucket_name,
        )
        new_slot.last_error = old_slot.last_error
        new_slot.last_error_code = old_slot.last_error_code
        new_slot.last_result_status = old_slot.last_result_status
        new_slot.last_result_at = old_slot.last_result_at
        new_slot.last_success_at = old_slot.last_success_at
        new_slot.last_failure_at = old_slot.last_failure_at
        new_slot.cooldown_until = old_slot.cooldown_until
        for idx, current in enumerate(self._slots):
            if current.slot_id == old_slot.slot_id:
                self._slots[idx] = new_slot
                break
        return new_slot

    def acquire(self, timeout_seconds: int, exclude_slot_ids: set[str] | None = None) -> BrowserSlot:
        excluded = exclude_slot_ids or set()
        deadline = time.time() + timeout_seconds
        with self._cond:
            while True:
                now = time.time()
                slot_count = len(self._slots)
                next_cooldown_wait: float | None = None
                for offset in range(slot_count):
                    idx = (self._next_index + offset) % slot_count
                    slot = self._slots[idx]
                    if slot.slot_id in excluded or slot.busy:
                        continue
                    if slot.cooldown_until > now:
                        wait_seconds = slot.cooldown_until - now
                        if next_cooldown_wait is None or wait_seconds < next_cooldown_wait:
                            next_cooldown_wait = wait_seconds
                        continue
                    slot.busy = True
                    self._next_index = (idx + 1) % slot_count
                    return slot

                remaining = deadline - now
                if remaining <= 0:
                    raise TimeoutError("no browser slot available")
                wait_for = min(remaining, 0.5)
                if next_cooldown_wait is not None:
                    wait_for = min(wait_for, max(0.05, next_cooldown_wait))
                self._cond.wait(timeout=wait_for)

    def release(self, slot: BrowserSlot, result: SlotResult) -> None:
        policy = failure_policy_for_result(result)
        now = time.time()
        with self._cond:
            slot.busy = False
            slot.last_result_status = result.status_code
            slot.last_result_at = now
            if result.ok:
                slot.consecutive_failures = 0
                slot.last_error = ""
                slot.last_error_code = ""
                slot.last_success_at = now
                slot.cooldown_until = 0
            else:
                slot.consecutive_failures += 1
                slot.last_error = result.error_message
                slot.last_error_code = result.error_code
                slot.last_failure_at = now
                if policy.cooldown_seconds > 0:
                    slot.cooldown_until = max(slot.cooldown_until, now + policy.cooldown_seconds)
            maintenance_recycle = slot.should_recycle()
            rotate_identity = policy.rotate_identity or (slot.consecutive_failures >= 2 and not result.ok)
            recycle = maintenance_recycle or slot.consecutive_failures >= 2 or policy.recycle
            if recycle:
                self._replace_slot_locked(slot, rotate_identity=rotate_identity)
            self._cond.notify_all()

    def health(self) -> dict[str, Any]:
        with self._lock:
            now = time.time()
            cooling = 0
            available = 0
            busy = 0
            slots = []
            for slot in self._slots:
                cooldown_remaining = slot.cooldown_remaining_seconds(now)
                if slot.busy:
                    busy += 1
                elif cooldown_remaining > 0:
                    cooling += 1
                else:
                    available += 1
                slots.append(
                    {
                        "slot_id": slot.slot_id,
                        "proxy_url": slot.proxy_url,
                        "bucket_name": slot.bucket_name,
                        "requests_served": slot.requests_served,
                        "consecutive_failures": slot.consecutive_failures,
                        "last_error": slot.last_error,
                        "last_error_code": slot.last_error_code,
                        "last_result_status": slot.last_result_status,
                        "last_preflight_ok": slot.last_preflight_ok,
                        "last_preflight_ip": slot.last_preflight_ip,
                        "last_preflight_status": slot.last_preflight_status,
                        "busy": slot.busy,
                        "age_seconds": int(now - slot.created_at),
                        "cooldown_remaining_seconds": cooldown_remaining,
                        "last_result_at": int(slot.last_result_at) if slot.last_result_at else 0,
                        "last_success_at": int(slot.last_success_at) if slot.last_success_at else 0,
                        "last_failure_at": int(slot.last_failure_at) if slot.last_failure_at else 0,
                        "last_preflight_at": int(slot.last_preflight_at) if slot.last_preflight_at else 0,
                    }
                )
            return {
                "slot_count": len(self._slots),
                "available_count": available,
                "cooling_count": cooling,
                "busy_count": busy,
                "slots": slots,
            }


def set_token_state(context: BrowserContext, page, token: str) -> None:
    context.add_cookies(
        [
            {
                "name": "token",
                "value": token,
                "domain": "chat.z.ai",
                "path": "/",
                "httpOnly": False,
                "secure": True,
                "sameSite": "Lax",
            }
        ]
    )
    page.evaluate(
        """
        (token) => {
          localStorage.setItem("token", token);
          sessionStorage.setItem("token", token);
        }
        """,
        token,
    )


def run_slot_request(slot: BrowserSlot, token: str, prompt: str, model: str) -> SlotResult:
    slot.requests_served += 1
    context = slot.browser.new_context(ignore_https_errors=True, viewport={"width": 1440, "height": 960})
    network_events: list[dict[str, Any]] = []
    chat_response: dict[str, Any] | None = None
    verify_seen = False
    slot_deadline = time.time() + slot_request_timeout_seconds()

    def remaining_timeout_ms() -> int:
        remaining_seconds = slot_deadline - time.time()
        if remaining_seconds <= 0:
            return 1000
        return min(browser_launch_timeout_ms(), max(1000, int(remaining_seconds * 1000)))

    try:
        page = context.new_page()

        def handle_response(response) -> None:
            nonlocal chat_response, verify_seen
            url = response.url
            if any(marker in url for marker in ("/api/v1/chats/new", "/api/v2/chat/completions", "InitCaptchaV3", "VerifyCaptchaV3")):
                item = {
                    "status": response.status,
                    "url": url,
                    "content_type": response.headers.get("content-type", ""),
                }
                try:
                    item["text_prefix"] = response.text()[:3000]
                except Exception as exc:
                    item["text_error"] = str(exc)
                network_events.append(item)
                if "VerifyCaptchaV3" in url:
                    verify_seen = True
                if "/api/v2/chat/completions" in url and chat_response is None:
                    chat_response = item

        page.on("response", handle_response)
        page.goto(TARGET_URL, wait_until="domcontentloaded", timeout=remaining_timeout_ms())
        page.wait_for_timeout(min(page_wait_ms(), remaining_timeout_ms()))
        set_token_state(context, page, token)
        page.reload(wait_until="domcontentloaded", timeout=remaining_timeout_ms())
        page.wait_for_timeout(min(page_wait_ms(), remaining_timeout_ms()))

        auth_check = page.evaluate(
            """
            async (token) => {
              const resp = await fetch("/api/v1/auths/", {
                method: "GET",
                credentials: "include",
                headers: {
                  authorization: "Bearer " + token,
                },
              });
              const text = await resp.text();
              return { status: resp.status, text_prefix: text.slice(0, 1000) };
            }
            """,
            token,
        )
        if auth_check["status"] != 200:
            return SlotResult(
                ok=False,
                status_code=401,
                content_type="application/json",
                body=json.dumps({"error": {"message": "upstream auth failed in browser helper", "code": "browser_helper_auth_failed"}}),
                error_code="browser_helper_auth_failed",
                error_message="upstream auth failed in browser helper",
                slot_id=slot.slot_id,
                proxy_url=slot.proxy_url,
                debug={"auth_check": auth_check, "network": network_events},
            )

        page.locator("#chat-input").fill(prompt, timeout=5000)
        page.wait_for_timeout(500)
        send_state = page.evaluate(
            """
            () => {
              const send = document.querySelector('#send-message-button');
              return { send_disabled: send ? !!send.disabled : null };
            }
            """
        )
        if send_state["send_disabled"]:
            return SlotResult(
                ok=False,
                status_code=500,
                content_type="application/json",
                body=json.dumps({"error": {"message": "send button remained disabled", "code": "browser_helper_send_disabled"}}),
                error_code="browser_helper_send_disabled",
                error_message="send button remained disabled",
                slot_id=slot.slot_id,
                proxy_url=slot.proxy_url,
                debug={"network": network_events},
            )

        page.locator("#send-message-button").click(timeout=5000)

        while time.time() < slot_deadline:
            if chat_response is not None:
                break
            page.wait_for_timeout(500)

        if chat_response is None:
            error_code = "browser_helper_captcha_stuck" if any("InitCaptchaV3" in item["url"] for item in network_events) else "browser_helper_no_completion"
            error_message = "captcha did not advance to completion" if error_code == "browser_helper_captcha_stuck" else "browser helper did not observe completion response"
            return SlotResult(
                ok=False,
                status_code=502 if error_code != "browser_helper_captcha_stuck" else 403,
                content_type="application/json",
                body=json.dumps({"error": {"message": error_message, "code": error_code}}),
                error_code=error_code,
                error_message=error_message,
                slot_id=slot.slot_id,
                proxy_url=slot.proxy_url,
                debug={"verify_seen": verify_seen, "network": network_events},
            )

        classified = classify_chat_response(slot, chat_response, network_events, verify_seen)
        if classified is not None:
            return classified

        return SlotResult(
            ok=True,
            status_code=200,
            content_type=chat_response["content_type"] or "text/event-stream; charset=utf-8",
            body=chat_response.get("text_prefix", ""),
            slot_id=slot.slot_id,
            proxy_url=slot.proxy_url,
            debug={"verify_seen": verify_seen, "network": network_events},
        )
    except PlaywrightError as exc:
        return SlotResult(
            ok=False,
            status_code=502,
            content_type="application/json",
            body=json.dumps({"error": {"message": str(exc), "code": "browser_helper_playwright_error"}}),
            error_code="browser_helper_playwright_error",
            error_message=str(exc),
            slot_id=slot.slot_id,
            proxy_url=slot.proxy_url,
            debug={"network": network_events},
        )
    finally:
        context.close()


class HelperRuntime:
    def __init__(self) -> None:
        self._thread = threading.Thread(target=self._run, name="browser-helper-runtime", daemon=True)
        self._ready = threading.Event()
        self._jobs: "queue.Queue[tuple[str, BrowserChatRequest, queue.Queue[SlotResult]]]" = queue.Queue()
        self._health_lock = threading.Lock()
        self._health: dict[str, Any] = {"ready": False, "error": "", "slots": []}
        self._thread.start()
        if not self._ready.wait(timeout=60):
            raise RuntimeError("browser helper runtime did not start within 60 seconds")

    def _run(self) -> None:
        try:
            playwright = sync_playwright().start()
            pool = SlotPool(playwright)
            with self._health_lock:
                self._health = {"ready": True, "error": "", **pool.health()}
            self._ready.set()
            while True:
                _, req, reply_q = self._jobs.get()
                try:
                    prompt = supported_prompt_from_payload(req.payload)
                except ValueError as exc:
                    reply_q.put(
                        SlotResult(
                            ok=False,
                            status_code=400,
                            content_type="application/json",
                            body=json.dumps({"error": {"message": str(exc), "code": "browser_helper_unsupported_request"}}),
                            error_code="browser_helper_unsupported_request",
                            error_message=str(exc),
                        )
                    )
                    continue

                last_result: SlotResult | None = None
                attempted_slots: set[str] = set()
                for _ in range(len(pool._slots)):
                    slot = pool.acquire(timeout_seconds=5, exclude_slot_ids=attempted_slots)
                    attempted_slots.add(slot.slot_id)
                    result = run_slot_request(slot, req.token, prompt, req.payload.model)
                    last_result = result
                    pool.release(slot, result)
                    with self._health_lock:
                        self._health = {"ready": True, "error": "", **pool.health()}
                    if result.ok:
                        reply_q.put(result)
                        break
                else:
                    reply_q.put(
                        last_result
                        or SlotResult(
                            ok=False,
                            status_code=503,
                            content_type="application/json",
                            body=json.dumps({"error": {"message": "no browser slot available", "code": "browser_helper_no_slot"}}),
                            error_code="browser_helper_no_slot",
                            error_message="no browser slot available",
                        )
                    )
        except Exception as exc:  # noqa: BLE001
            with self._health_lock:
                self._health = {"ready": False, "error": str(exc), "slots": []}
            self._ready.set()

    def submit(self, req: BrowserChatRequest) -> SlotResult:
        reply_q: "queue.Queue[SlotResult]" = queue.Queue(maxsize=1)
        self._jobs.put((req.payload.model, req, reply_q))
        try:
            return reply_q.get(timeout=request_timeout_seconds() + 45)
        except queue.Empty as exc:
            raise TimeoutError("browser helper runtime timed out waiting for slot result") from exc

    def health(self) -> dict[str, Any]:
        with self._health_lock:
            return dict(self._health)


runtime = HelperRuntime()
app = FastAPI()


@app.get("/healthz")
def healthz() -> dict[str, Any]:
    return {
        "ok": True,
        "auth_required": bool(browser_request_auth_token()),
        **runtime.health(),
    }


@app.post("/v1/browser-chat/completions", response_model=None)
def browser_chat(req: BrowserChatRequest, authorization: Optional[str] = Header(default=None)):
    required = browser_request_auth_token()
    if required:
        expected = f"Bearer {required}"
        if authorization != expected:
            raise HTTPException(status_code=401, detail="invalid helper auth token")

    try:
        result = runtime.submit(req)
    except TimeoutError as exc:
        return JSONResponse(
            status_code=504,
            content={"error": {"message": str(exc), "code": "browser_helper_timeout"}},
        )
    except Exception as exc:  # noqa: BLE001
        return JSONResponse(
            status_code=500,
            content={"error": {"message": str(exc), "code": "browser_helper_runtime_error"}},
        )

    if result.ok:
        response = PlainTextResponse(result.body, status_code=200, media_type="text/event-stream")
        response.headers["X-Browser-Slot"] = result.slot_id
        return response
    return JSONResponse(
        status_code=result.status_code,
        headers={"X-Browser-Slot": result.slot_id} if result.slot_id else None,
        content={
            "error": {
                "message": result.error_message or "browser helper request failed",
                "code": result.error_code or "browser_helper_failed",
            },
            "debug": {
                "slot_id": result.slot_id,
            },
        },
    )
