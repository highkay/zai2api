package internal

import "net/http"

const consoleHTML = `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>zai2api Console</title>
  <style>
    :root {
      --bg: #10130f;
      --paper: #efe7d2;
      --panel: #f8f0dc;
      --ink: #151812;
      --muted: #6c705f;
      --line: #303728;
      --line-soft: rgba(21, 24, 18, 0.16);
      --green: #19745a;
      --amber: #b26719;
      --red: #ba3b2d;
      --blue: #235a8f;
      --shadow: rgba(0, 0, 0, 0.22);
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      min-height: 100vh;
      background:
        radial-gradient(circle at 12% 8%, rgba(239, 231, 210, 0.18), transparent 26%),
        radial-gradient(circle at 82% 10%, rgba(25, 116, 90, 0.20), transparent 22%),
        linear-gradient(135deg, rgba(255,255,255,0.035) 25%, transparent 25%) 0 0 / 22px 22px,
        var(--bg);
      color: var(--paper);
      font-family: "Cascadia Code", "IBM Plex Mono", "JetBrains Mono", monospace;
    }
    button, input, textarea, select { font: inherit; }
    .shell { width: min(1380px, calc(100vw - 28px)); margin: 0 auto; padding: 26px 0 46px; }
    .topbar {
      display: grid;
      grid-template-columns: minmax(0, 1fr) minmax(320px, 520px);
      gap: 18px;
      align-items: end;
      border-bottom: 2px solid var(--paper);
      padding-bottom: 18px;
    }
    h1 { margin: 0; font-size: clamp(34px, 6vw, 82px); line-height: 0.86; letter-spacing: -0.08em; text-transform: uppercase; }
    .subtitle { color: rgba(239,231,210,0.76); margin-top: 12px; font-size: 13px; }
    .auth-box {
      background: rgba(239, 231, 210, 0.08);
      border: 1px solid rgba(239, 231, 210, 0.28);
      border-radius: 14px;
      padding: 12px;
      box-shadow: 0 18px 50px var(--shadow);
    }
    .auth-grid { display: grid; grid-template-columns: 1fr auto auto; gap: 8px; }
    .remember { display: flex; gap: 8px; align-items: center; margin-top: 9px; color: rgba(239,231,210,0.76); font-size: 12px; }
    input, textarea, select {
      width: 100%;
      border: 1px solid var(--line-soft);
      border-radius: 9px;
      background: rgba(255,255,255,0.72);
      color: var(--ink);
      padding: 10px 11px;
      outline: none;
    }
    input:focus, textarea:focus, select:focus { border-color: var(--green); box-shadow: 0 0 0 3px rgba(25,116,90,0.14); }
    button {
      border: 1px solid var(--line);
      border-radius: 9px;
      background: var(--line);
      color: var(--paper);
      min-height: 39px;
      padding: 8px 12px;
      cursor: pointer;
      transition: transform 120ms ease, box-shadow 120ms ease, opacity 120ms ease;
      box-shadow: 4px 4px 0 rgba(21,24,18,0.18);
    }
    button:hover:not(:disabled) { transform: translate(-1px, -1px); box-shadow: 6px 6px 0 rgba(21,24,18,0.20); }
    button.secondary { background: var(--panel); color: var(--ink); }
    button.good { background: var(--green); border-color: var(--green); }
    button.warn { background: var(--amber); border-color: var(--amber); }
    button.danger { background: var(--red); border-color: var(--red); }
    button:disabled { opacity: 0.45; cursor: not-allowed; }
    .grid { display: grid; grid-template-columns: repeat(12, minmax(0, 1fr)); gap: 14px; margin-top: 18px; }
    .panel {
      background: var(--panel);
      color: var(--ink);
      border: 1px solid rgba(21,24,18,0.22);
      border-radius: 16px;
      box-shadow: 0 18px 40px var(--shadow);
      padding: 16px;
      animation: rise 360ms ease both;
    }
    @keyframes rise { from { opacity: 0; transform: translateY(10px); } to { opacity: 1; transform: translateY(0); } }
    .span-3 { grid-column: span 3; }
    .span-4 { grid-column: span 4; }
    .span-5 { grid-column: span 5; }
    .span-7 { grid-column: span 7; }
    .span-12 { grid-column: span 12; }
    .metric-label { color: var(--muted); font-size: 12px; text-transform: uppercase; }
    .metric-value { margin-top: 7px; font-size: clamp(26px, 4vw, 46px); font-weight: 900; letter-spacing: -0.06em; }
    .hint { color: var(--muted); font-size: 12px; }
    .section-head { display: flex; justify-content: space-between; align-items: center; gap: 12px; margin-bottom: 12px; }
    .section-title { margin: 0; font-size: 15px; text-transform: uppercase; letter-spacing: 0.02em; }
    .actions { display: flex; gap: 8px; flex-wrap: wrap; align-items: center; }
    .auth-banner {
      margin-top: 14px;
      padding: 12px 14px;
      border: 1px solid rgba(239,231,210,0.24);
      border-left: 6px solid var(--amber);
      border-radius: 12px;
      background: rgba(178, 103, 25, 0.16);
      color: var(--paper);
      font-size: 13px;
    }
    .auth-banner.hidden { display: none; }
    .filters { display: grid; grid-template-columns: 170px 170px 1fr; gap: 8px; margin-bottom: 10px; }
    .pills { display: flex; flex-wrap: wrap; gap: 8px; }
    .pill { border: 1px solid var(--line-soft); border-radius: 999px; padding: 5px 9px; background: rgba(21,24,18,0.04); font-size: 12px; }
    table { width: 100%; border-collapse: collapse; font-size: 12px; }
    th, td { padding: 10px 8px; border-bottom: 1px solid var(--line-soft); text-align: left; vertical-align: middle; }
    th { color: var(--muted); font-size: 10px; text-transform: uppercase; letter-spacing: 0.06em; }
    .token { max-width: 250px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .badge { display: inline-flex; border-radius: 999px; padding: 3px 8px; border: 1px solid var(--line-soft); font-size: 11px; text-transform: uppercase; }
    .badge.active { color: var(--green); border-color: rgba(25,116,90,0.34); }
    .badge.invalid { color: var(--red); border-color: rgba(186,59,45,0.34); }
    .badge.disabled { color: var(--amber); border-color: rgba(178,103,25,0.34); }
    .badge.rotated { color: var(--blue); border-color: rgba(35,90,143,0.34); }
    .row-actions { display: flex; gap: 6px; flex-wrap: nowrap; }
    .row-actions button { min-height: 30px; padding: 5px 8px; font-size: 11px; }
    .error { color: var(--red); }
    textarea { resize: vertical; min-height: 102px; }
    @media (max-width: 980px) {
      .topbar { grid-template-columns: 1fr; }
      .auth-grid, .filters { grid-template-columns: 1fr; }
      .span-3, .span-4, .span-5, .span-7 { grid-column: span 12; }
      th:nth-child(4), td:nth-child(4), th:nth-child(6), td:nth-child(6), th:nth-child(7), td:nth-child(7) { display: none; }
    }
  </style>
</head>
<body>
  <main class="shell">
    <header class="topbar">
      <div>
        <h1>zai2api<br>ledger</h1>
        <div class="subtitle"><span id="health-text">loading</span></div>
      </div>
      <div class="auth-box">
        <div class="auth-grid">
          <input id="api-key" type="password" autocomplete="off" placeholder="AUTH_TOKEN">
          <button id="save-key" type="button" class="good">Unlock</button>
          <button id="clear-key" type="button" class="secondary">Clear</button>
        </div>
        <label class="remember"><input id="remember-key" type="checkbox"> remember this browser using localStorage; otherwise sessionStorage only</label>
      </div>
    </header>
    <div id="auth-banner" class="auth-banner">Token ledger is locked. Metrics are public, but token records and mutations require Authorization.</div>

    <section class="grid">
      <div class="panel span-3"><div class="metric-label">Total Calls</div><div id="total-calls" class="metric-value">0</div></div>
      <div class="panel span-3"><div class="metric-label">Success</div><div id="success-calls" class="metric-value">0</div></div>
      <div class="panel span-3"><div class="metric-label">Failed</div><div id="failed-calls" class="metric-value">0</div></div>
      <div class="panel span-3"><div class="metric-label">Success Rate</div><div id="success-rate" class="metric-value">0%</div></div>

      <div class="panel span-4"><div class="metric-label">Active Tokens</div><div id="valid-tokens" class="metric-value">0</div><div id="total-tokens" class="hint">total 0</div></div>
      <div class="panel span-4"><div class="metric-label">RPM</div><div id="rpm" class="metric-value">0</div><div id="uptime" class="hint">uptime 0s</div></div>
      <div class="panel span-4"><div class="metric-label">Token Usage</div><div id="token-usage" class="metric-value">0</div><div class="hint">input + output</div></div>

      <div class="panel span-12">
        <div class="section-head">
          <h2 class="section-title">Runtime Config</h2>
          <div id="config-current" class="hint">locked</div>
        </div>
        <div class="filters">
          <input id="upstream-proxy" type="password" autocomplete="off" placeholder="upstream proxy URL">
          <button id="save-config" type="button" class="good">Save Proxy</button>
          <button id="clear-proxy" type="button" class="secondary">Direct</button>
        </div>
        <div id="config-status" class="hint"></div>
      </div>

      <div class="panel span-12">
        <div class="section-head">
          <h2 class="section-title">Token Ledger</h2>
          <div class="actions">
            <button id="reload" class="secondary" type="button">Refresh</button>
          </div>
        </div>
        <div class="filters">
          <select id="status-filter">
            <option value="all">all statuses</option>
            <option value="active">active</option>
            <option value="invalid">invalid</option>
            <option value="disabled">disabled</option>
            <option value="rotated">rotated</option>
          </select>
          <select id="source-filter">
            <option value="">all sources</option>
            <option value="legacy_file">legacy file</option>
            <option value="api">api</option>
            <option value="env_backup">env backup</option>
          </select>
          <div id="token-summary" class="pills"></div>
        </div>
        <div id="token-error" class="hint"></div>
        <div style="overflow:auto">
          <table>
            <thead><tr><th>ID</th><th>Token</th><th>Status</th><th>Source</th><th>Email</th><th>Use</th><th>Checked</th><th>Refreshed</th><th>Reason</th><th></th></tr></thead>
            <tbody id="tokens-body"></tbody>
          </table>
        </div>
      </div>

      <div class="panel span-5">
        <div class="section-head"><h2 class="section-title">Add Tokens</h2></div>
        <textarea id="new-tokens" spellcheck="false" placeholder="one token per line"></textarea>
        <div class="actions" style="margin-top:10px">
          <button id="add-tokens" type="button" class="good">Add</button>
          <button id="clear-tokens" class="secondary" type="button">Clear</button>
        </div>
        <div id="mutation-status" class="hint" style="margin-top:10px"></div>
      </div>

      <div class="panel span-7">
        <div class="section-head">
          <h2 class="section-title">Models</h2>
          <div id="model-count" class="hint">0 active rows</div>
        </div>
        <div style="overflow:auto">
          <table>
            <thead><tr><th>Model</th><th>Requests</th><th>Input Tokens</th><th>Output Tokens</th></tr></thead>
            <tbody id="models-body"></tbody>
          </table>
        </div>
      </div>
    </section>
  </main>
  <script>
    const storageKey = "zai2api_console_key";
    const state = {
      key: sessionStorage.getItem(storageKey) || localStorage.getItem(storageKey) || "",
      config: null,
      tokens: []
    };
    const $ = (id) => document.getElementById(id);

    $("api-key").value = state.key;
    $("remember-key").checked = Boolean(localStorage.getItem(storageKey));
    $("save-key").addEventListener("click", saveKey);
    $("clear-key").addEventListener("click", clearKey);
    $("reload").addEventListener("click", loadAll);
    $("clear-tokens").addEventListener("click", () => { $("new-tokens").value = ""; });
    $("add-tokens").addEventListener("click", addTokens);
    $("save-config").addEventListener("click", saveConfig);
    $("clear-proxy").addEventListener("click", () => {
      $("upstream-proxy").value = "";
      saveConfig();
    });
    $("status-filter").addEventListener("change", loadTokens);
    $("source-filter").addEventListener("change", loadTokens);

    function saveKey() {
      state.key = $("api-key").value.trim();
      sessionStorage.setItem(storageKey, state.key);
      if ($("remember-key").checked) {
        localStorage.setItem(storageKey, state.key);
      } else {
        localStorage.removeItem(storageKey);
      }
      setAuthState();
      loadAll();
    }

    function clearKey() {
      state.key = "";
      $("api-key").value = "";
      sessionStorage.removeItem(storageKey);
      localStorage.removeItem(storageKey);
      setAuthState();
      loadAll();
    }

    function number(value) { return new Intl.NumberFormat().format(Number(value || 0)); }
    function percent(value) { return Number(value || 0).toFixed(1) + "%"; }
    function formatTime(value) {
      if (!value || String(value).startsWith("0001-")) return "never";
      const date = new Date(value);
      if (Number.isNaN(date.getTime())) return "never";
      return date.toLocaleString();
    }
    function authHeaders() { return state.key ? { "Authorization": "Bearer " + state.key } : {}; }
    function setAuthState() {
      const hasKey = Boolean(state.key);
      $("auth-banner").classList.toggle("hidden", hasKey);
      $("add-tokens").disabled = !hasKey;
      $("new-tokens").disabled = !hasKey;
      $("save-config").disabled = !hasKey;
      $("clear-proxy").disabled = !hasKey;
      $("upstream-proxy").disabled = !hasKey;
      $("mutation-status").textContent = hasKey ? "" : "AUTH_TOKEN required before mutating tokens";
      $("config-status").textContent = hasKey ? "" : "AUTH_TOKEN required before updating runtime config";
    }
    async function requestJSON(url, options = {}) {
      const response = await fetch(url, options);
      const text = await response.text();
      let payload = {};
      try { payload = text ? JSON.parse(text) : {}; } catch { payload = { message: text || response.statusText }; }
      if (!response.ok) {
        const message = payload && payload.error ? payload.error.message : payload.message || response.statusText;
        throw new Error(message);
      }
      return payload;
    }

    async function loadTelemetry() {
      const root = await requestJSON("/");
      const t = root.telemetry || {};
      const failed = t.failed_calls ?? Math.max(Number(t.total_calls || 0) - Number(t.success_calls || 0), 0);
      $("health-text").textContent = "online - " + (root.version || "unknown");
      $("total-calls").textContent = number(t.total_calls);
      $("success-calls").textContent = number(t.success_calls);
      $("failed-calls").textContent = number(failed);
      $("success-rate").textContent = percent(t.success_rate);
      $("valid-tokens").textContent = number(t.valid_tokens);
      $("total-tokens").textContent = "total " + number(t.total_tokens || 0) + " / invalid " + number(t.invalid_tokens || 0) + " / disabled " + number(t.disabled_tokens || 0) + " / rotated " + number(t.rotated_tokens || 0);
      $("rpm").textContent = number(t.rpm);
      $("uptime").textContent = "uptime " + (t.uptime || "0s");
      $("token-usage").textContent = number(Number(t.total_input_tokens || 0) + Number(t.total_output_tokens || 0));
      renderModels(t.model_stats || {});
    }

    async function loadConfig() {
      if (!state.key) {
        renderConfig(null);
        return;
      }
      const payload = await requestJSON("/v1/config", { headers: authHeaders() });
      state.config = payload.data || null;
      renderConfig(state.config);
    }

    function renderConfig(config) {
      if (!config) {
        $("config-current").textContent = "locked";
        return;
      }
      const proxy = config.upstream_proxy_set ? config.upstream_proxy_preview : "direct";
      $("config-current").textContent = "proxy " + proxy;
      $("config-status").textContent = "runtime file " + (config.runtime_config_path || "-");
    }

    async function saveConfig() {
      if (!state.key) {
        $("config-status").textContent = "AUTH_TOKEN required";
        return;
      }
      $("save-config").disabled = true;
      try {
        const payload = await requestJSON("/v1/config", {
          method: "PUT",
          headers: { ...authHeaders(), "Content-Type": "application/json" },
          body: JSON.stringify({ upstream_proxy: $("upstream-proxy").value.trim() })
        });
        state.config = payload.data || null;
        $("upstream-proxy").value = "";
        renderConfig(state.config);
        $("config-status").textContent = payload.message || "config updated";
      } catch (err) {
        $("config-status").textContent = err.message;
      } finally {
        $("save-config").disabled = false;
      }
    }

    function renderModels(stats) {
      const body = $("models-body");
      body.replaceChildren();
      const rows = Object.entries(stats).sort((a, b) => (b[1].requests || 0) - (a[1].requests || 0));
      $("model-count").textContent = rows.length + " active rows";
      if (!rows.length) {
        const row = body.insertRow();
        const cell = row.insertCell();
        cell.colSpan = 4;
        cell.className = "hint";
        cell.textContent = "no model calls yet";
        return;
      }
      rows.forEach(([model, stat]) => {
        const row = body.insertRow();
        [model, number(stat.requests), number(stat.input_tokens), number(stat.output_tokens)].forEach((value) => {
          const cell = row.insertCell();
          cell.textContent = value;
        });
      });
    }

    async function loadTokens() {
      const error = $("token-error");
      error.textContent = "";
      if (!state.key) {
        renderSummary({});
        renderTokens([]);
        error.textContent = "AUTH_TOKEN required - paste the key above and click Unlock";
        return;
      }
      const params = new URLSearchParams();
      params.set("status", $("status-filter").value);
      if ($("source-filter").value) params.set("source", $("source-filter").value);
      const payload = await requestJSON("/v1/tokens?" + params.toString(), { headers: authHeaders() });
      state.tokens = payload.data || [];
      renderSummary(payload.summary || {});
      renderTokens(state.tokens);
    }

    function renderSummary(summary) {
      const box = $("token-summary");
      box.replaceChildren();
      [["total", "total"], ["active", "active"], ["invalid", "invalid"], ["disabled", "disabled"], ["rotated", "rotated"]].forEach(([key, label]) => {
        const pill = document.createElement("span");
        pill.className = "pill";
        pill.textContent = label + " " + number(summary[key] || 0);
        box.appendChild(pill);
      });
    }

    function renderTokens(tokens) {
      const body = $("tokens-body");
      body.replaceChildren();
      if (!tokens.length) {
        const row = body.insertRow();
        const cell = row.insertCell();
        cell.colSpan = 10;
        cell.className = "hint";
        cell.textContent = state.key ? "no token records for current filter" : "locked until AUTH_TOKEN is saved";
        return;
      }
      tokens.forEach((item) => {
        const row = body.insertRow();
        [item.id || "-", item.token_preview || item.token || "-", item.status || "-", item.source || "-", item.email || "-", number(item.use_count), formatTime(item.last_checked), formatTime(item.last_refreshed), item.invalid_reason || "-"].forEach((value, index) => {
          const cell = row.insertCell();
          if (index === 1) cell.className = "token";
          if (index === 2) {
            const badge = document.createElement("span");
            badge.className = "badge " + (item.status || "");
            badge.textContent = value;
            cell.appendChild(badge);
          } else {
            cell.textContent = value;
          }
        });
        const actionCell = row.insertCell();
        actionCell.className = "row-actions";
        renderTokenActions(actionCell, item);
      });
    }

    function renderTokenActions(cell, item) {
      if (item.status === "invalid" || item.status === "disabled") {
        const activate = document.createElement("button");
        activate.type = "button";
        activate.className = "good";
        activate.textContent = "Activate";
        activate.addEventListener("click", () => setStatus(item.id, "active"));
        cell.appendChild(activate);
      }
      if (item.status === "active") {
        const disable = document.createElement("button");
        disable.type = "button";
        disable.className = "warn";
        disable.textContent = "Disable";
        disable.addEventListener("click", () => setStatus(item.id, "disabled"));
        cell.appendChild(disable);
      }
      const del = document.createElement("button");
      del.type = "button";
      del.className = "danger";
      del.textContent = "Hard Delete";
      del.addEventListener("click", () => deleteToken(item.id, item.token_preview || String(item.id)));
      cell.appendChild(del);
    }

    async function addTokens() {
      const tokens = $("new-tokens").value.split(/\r?\n/).map((t) => t.trim()).filter(Boolean);
      if (!tokens.length) {
        $("mutation-status").textContent = "nothing to add";
        return;
      }
      if (!state.key) {
        $("mutation-status").textContent = "AUTH_TOKEN required";
        return;
      }
      $("add-tokens").disabled = true;
      try {
        const payload = await requestJSON("/v1/tokens", {
          method: "POST",
          headers: { ...authHeaders(), "Content-Type": "application/json" },
          body: JSON.stringify({ tokens })
        });
        $("mutation-status").textContent = "added " + (payload.count || 0) + ", skipped " + ((payload.skipped || []).length);
        $("new-tokens").value = "";
        await loadAll();
      } catch (err) {
        $("mutation-status").textContent = err.message;
      } finally {
        $("add-tokens").disabled = false;
      }
    }

    async function setStatus(id, status) {
      if (!id) return;
      try {
        await requestJSON("/v1/tokens", {
          method: "PUT",
          headers: { ...authHeaders(), "Content-Type": "application/json" },
          body: JSON.stringify({ id, status })
        });
        $("mutation-status").textContent = "token " + id + " -> " + status;
        await loadAll();
      } catch (err) {
        $("mutation-status").textContent = err.message;
      }
    }

    async function deleteToken(id, label) {
      if (!id) return;
      if (!confirm("Permanently delete " + label + "?")) return;
      try {
        await requestJSON("/v1/tokens?id=" + encodeURIComponent(id) + "&hard=true", {
          method: "DELETE",
          headers: authHeaders()
        });
        $("mutation-status").textContent = "token hard deleted";
        await loadAll();
      } catch (err) {
        $("mutation-status").textContent = err.message;
      }
    }

    async function loadAll() {
      try {
        await loadTelemetry();
      } catch (err) {
        $("health-text").textContent = err.message;
      }
      try {
        await loadConfig();
      } catch (err) {
        $("config-status").textContent = err.message;
        renderConfig(null);
      }
      try {
        await loadTokens();
      } catch (err) {
        $("token-error").textContent = err.message;
        renderTokens([]);
      }
    }

    setAuthState();
    loadAll();
    setInterval(loadAll, 15000);
  </script>
</body>
</html>`

func HandleConsole(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeInvalidRequestError(w, "Only GET and HEAD methods are allowed")
		return
	}
	if r.URL.Path == "/console/" {
		http.Redirect(w, r, "/console", http.StatusPermanentRedirect)
		return
	}
	if r.URL.Path != "/console" {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write([]byte(consoleHTML))
}
