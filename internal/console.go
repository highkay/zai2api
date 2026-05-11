package internal

import (
	"net/http"
)

const consoleHTML = `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>zai2api Console</title>
  <style>
    :root {
      --bg: #f4f6f1;
      --ink: #11150f;
      --muted: #687064;
      --line: #cbd3c4;
      --panel: #fbfcf7;
      --accent: #126b4e;
      --accent-2: #d9462f;
      --accent-3: #0b7285;
      --shadow: rgba(17, 21, 15, 0.08);
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      min-height: 100vh;
      background:
        linear-gradient(90deg, rgba(17,21,15,0.04) 1px, transparent 1px),
        linear-gradient(180deg, rgba(17,21,15,0.035) 1px, transparent 1px),
        var(--bg);
      background-size: 28px 28px;
      color: var(--ink);
      font-family: "Cascadia Code", "IBM Plex Mono", "JetBrains Mono", monospace;
      letter-spacing: 0;
    }
    button, input, textarea { font: inherit; }
    .shell { width: min(1180px, calc(100vw - 32px)); margin: 0 auto; padding: 28px 0 40px; }
    .topbar {
      display: grid;
      grid-template-columns: minmax(0, 1fr) auto;
      gap: 20px;
      align-items: end;
      padding-bottom: 18px;
      border-bottom: 2px solid var(--ink);
    }
    h1 { margin: 0; font-size: clamp(28px, 4vw, 54px); line-height: 0.95; font-weight: 900; text-transform: uppercase; }
    .subtitle { margin-top: 10px; color: var(--muted); font-size: 13px; }
    .status-dot { display: inline-block; width: 10px; height: 10px; border-radius: 50%; background: var(--accent-2); margin-right: 8px; }
    .status-dot.ok { background: var(--accent); }
    .auth {
      display: grid;
      grid-template-columns: minmax(220px, 320px) auto;
      gap: 8px;
      align-items: center;
    }
    input, textarea {
      width: 100%;
      border: 1px solid var(--line);
      border-radius: 6px;
      background: var(--panel);
      color: var(--ink);
      padding: 10px 12px;
      outline: none;
    }
    input:focus, textarea:focus { border-color: var(--accent); box-shadow: 0 0 0 3px rgba(18, 107, 78, 0.12); }
    button {
      border: 1px solid var(--ink);
      border-radius: 6px;
      background: var(--ink);
      color: var(--panel);
      min-height: 39px;
      padding: 8px 13px;
      cursor: pointer;
      box-shadow: 3px 3px 0 var(--shadow);
    }
    button.secondary { background: var(--panel); color: var(--ink); }
    button.danger { background: var(--accent-2); border-color: var(--accent-2); }
    button:disabled { opacity: 0.45; cursor: not-allowed; }
    .grid { display: grid; grid-template-columns: repeat(12, 1fr); gap: 14px; margin-top: 18px; }
    .panel {
      background: var(--panel);
      border: 1px solid var(--line);
      border-radius: 8px;
      box-shadow: 0 12px 30px var(--shadow);
      padding: 16px;
    }
    .span-3 { grid-column: span 3; }
    .span-4 { grid-column: span 4; }
    .span-5 { grid-column: span 5; }
    .span-7 { grid-column: span 7; }
    .span-12 { grid-column: span 12; }
    .metric-label { color: var(--muted); font-size: 12px; text-transform: uppercase; }
    .metric-value { margin-top: 8px; font-size: clamp(26px, 4vw, 42px); font-weight: 900; }
    .section-head {
      display: flex;
      justify-content: space-between;
      gap: 12px;
      align-items: center;
      margin-bottom: 12px;
    }
    .section-title { margin: 0; font-size: 16px; text-transform: uppercase; }
    .hint { color: var(--muted); font-size: 12px; }
    .auth-banner {
      margin-top: 14px;
      padding: 12px 14px;
      border: 1px solid rgba(18, 107, 78, 0.28);
      border-left: 5px solid var(--accent);
      border-radius: 6px;
      background: rgba(18, 107, 78, 0.08);
      color: var(--ink);
      font-size: 13px;
    }
    .auth-banner.hidden { display: none; }
    .actions { display: flex; gap: 8px; flex-wrap: wrap; }
    table { width: 100%; border-collapse: collapse; font-size: 13px; }
    th, td { padding: 10px 8px; border-bottom: 1px solid var(--line); text-align: left; vertical-align: middle; }
    th { color: var(--muted); font-size: 11px; text-transform: uppercase; }
    .token { max-width: 280px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .badge { display: inline-flex; align-items: center; border-radius: 999px; padding: 3px 8px; font-size: 12px; border: 1px solid var(--line); }
    .badge.ok { color: var(--accent); border-color: rgba(18,107,78,0.35); }
    .badge.bad { color: var(--accent-2); border-color: rgba(217,70,47,0.35); }
    .error { color: var(--accent-2); }
    textarea { resize: vertical; min-height: 86px; }
    @media (max-width: 880px) {
      .topbar { grid-template-columns: 1fr; }
      .auth { grid-template-columns: 1fr; }
      .span-3, .span-4, .span-5, .span-7 { grid-column: span 12; }
      table { font-size: 12px; }
      th:nth-child(4), td:nth-child(4), th:nth-child(5), td:nth-child(5) { display: none; }
    }
  </style>
</head>
<body>
  <main class="shell">
    <header class="topbar">
      <div>
        <h1>zai2api<br>console</h1>
        <div class="subtitle"><span id="health-dot" class="status-dot"></span><span id="health-text">loading</span></div>
      </div>
      <div class="auth">
        <input id="api-key" type="password" autocomplete="off" placeholder="Paste AUTH_TOKEN to unlock token management">
        <button id="save-key" type="button">Save Key</button>
      </div>
    </header>
    <div id="auth-banner" class="auth-banner">
      Token management is locked. Paste the server AUTH_TOKEN and click Save Key. Metrics stay visible, but token list/add/delete require Authorization.
    </div>

    <section class="grid">
      <div class="panel span-3"><div class="metric-label">Total Calls</div><div id="total-calls" class="metric-value">0</div></div>
      <div class="panel span-3"><div class="metric-label">Success</div><div id="success-calls" class="metric-value">0</div></div>
      <div class="panel span-3"><div class="metric-label">Failed</div><div id="failed-calls" class="metric-value">0</div></div>
      <div class="panel span-3"><div class="metric-label">Success Rate</div><div id="success-rate" class="metric-value">0%</div></div>

      <div class="panel span-4"><div class="metric-label">Valid Tokens</div><div id="valid-tokens" class="metric-value">0</div><div id="total-tokens" class="hint">total 0</div></div>
      <div class="panel span-4"><div class="metric-label">RPM</div><div id="rpm" class="metric-value">0</div><div id="uptime" class="hint">uptime 0s</div></div>
      <div class="panel span-4"><div class="metric-label">Token Usage</div><div id="token-usage" class="metric-value">0</div><div class="hint">input + output</div></div>

      <div class="panel span-7">
        <div class="section-head">
          <h2 class="section-title">Tokens</h2>
          <div class="actions">
            <button id="reload" class="secondary" type="button">Refresh</button>
          </div>
        </div>
        <div id="token-error" class="hint"></div>
        <div style="overflow:auto">
          <table>
            <thead><tr><th>Token</th><th>Status</th><th>Email</th><th>Use</th><th>Last Refresh</th><th></th></tr></thead>
            <tbody id="tokens-body"></tbody>
          </table>
        </div>
      </div>

      <div class="panel span-5">
        <div class="section-head">
          <h2 class="section-title">Add Tokens</h2>
        </div>
        <textarea id="new-tokens" spellcheck="false" placeholder="one token per line"></textarea>
        <div class="actions" style="margin-top:10px">
          <button id="add-tokens" type="button">Add</button>
          <button id="clear-tokens" class="secondary" type="button">Clear</button>
        </div>
        <div id="mutation-status" class="hint" style="margin-top:10px"></div>
      </div>

      <div class="panel span-12">
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
    const state = { key: localStorage.getItem("zai2api_console_key") || "", tokens: [] };
    const $ = (id) => document.getElementById(id);

    $("api-key").value = state.key;
    $("save-key").addEventListener("click", () => {
      state.key = $("api-key").value.trim();
      localStorage.setItem("zai2api_console_key", state.key);
      setAuthState();
      loadAll();
    });
    $("reload").addEventListener("click", loadAll);
    $("clear-tokens").addEventListener("click", () => { $("new-tokens").value = ""; });
    $("add-tokens").addEventListener("click", addTokens);

    function number(value) {
      return new Intl.NumberFormat().format(Number(value || 0));
    }

    function percent(value) {
      return Number(value || 0).toFixed(1) + "%";
    }

    function maskToken(token) {
      if (!token) return "";
      if (token.length <= 22) return token;
      return token.slice(0, 10) + "..." + token.slice(-8);
    }

    function formatTime(value) {
      if (!value || value.startsWith("0001-")) return "never";
      const date = new Date(value);
      if (Number.isNaN(date.getTime())) return "never";
      return date.toLocaleString();
    }

    function authHeaders() {
      return state.key ? { "Authorization": "Bearer " + state.key } : {};
    }

    function setAuthState() {
      const hasKey = Boolean(state.key);
      $("auth-banner").classList.toggle("hidden", hasKey);
      $("add-tokens").disabled = !hasKey;
      $("new-tokens").disabled = !hasKey;
      $("mutation-status").textContent = hasKey ? "" : "AUTH_TOKEN required before adding tokens";
    }

    async function requestJSON(url, options = {}) {
      const response = await fetch(url, options);
      const text = await response.text();
      let payload = {};
      try {
        payload = text ? JSON.parse(text) : {};
      } catch {
        payload = { message: text || response.statusText };
      }
      if (!response.ok) {
        const message = payload?.error?.message || payload?.message || response.statusText;
        throw new Error(message);
      }
      return payload;
    }

    async function loadTelemetry() {
      const root = await requestJSON("/");
      const t = root.telemetry || {};
      const failed = t.failed_calls ?? Math.max(Number(t.total_calls || 0) - Number(t.success_calls || 0), 0);
      $("health-dot").classList.add("ok");
      $("health-text").textContent = "online - " + (root.version || "unknown");
      $("total-calls").textContent = number(t.total_calls);
      $("success-calls").textContent = number(t.success_calls);
      $("failed-calls").textContent = number(failed);
      $("success-rate").textContent = percent(t.success_rate);
      $("valid-tokens").textContent = number(t.valid_tokens);
      $("total-tokens").textContent = "total " + number(t.total_tokens ?? t.valid_tokens);
      $("rpm").textContent = number(t.rpm);
      $("uptime").textContent = "uptime " + (t.uptime || "0s");
      $("token-usage").textContent = number(Number(t.total_input_tokens || 0) + Number(t.total_output_tokens || 0));
      renderModels(t.model_stats || {});
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
        error.textContent = "AUTH_TOKEN required - paste the key above and click Save Key";
        renderTokens([]);
        return;
      }
      const payload = await requestJSON("/v1/tokens", { headers: authHeaders() });
      state.tokens = payload.data || [];
      renderTokens(state.tokens);
    }

    function renderTokens(tokens) {
      const body = $("tokens-body");
      body.replaceChildren();
      if (!tokens.length) {
        const row = body.insertRow();
        const cell = row.insertCell();
        cell.colSpan = 6;
        cell.className = "hint";
        cell.textContent = state.key ? "no file-backed tokens" : "locked until AUTH_TOKEN is saved";
        return;
      }
      tokens.forEach((item, index) => {
		const row = body.insertRow();
		const tokenCell = row.insertCell();
		tokenCell.className = "token";
		tokenCell.title = maskToken(item.token);
		tokenCell.textContent = maskToken(item.token);

        const statusCell = row.insertCell();
        const badge = document.createElement("span");
        badge.className = "badge " + (item.valid ? "ok" : "bad");
        badge.textContent = item.valid ? "valid" : "invalid";
        statusCell.appendChild(badge);

        [item.email || "-", number(item.use_count), formatTime(item.last_refreshed)].forEach((value) => {
          const cell = row.insertCell();
          cell.textContent = value;
        });

        const actionCell = row.insertCell();
        const del = document.createElement("button");
        del.type = "button";
        del.className = "danger";
        del.textContent = "Delete";
        del.addEventListener("click", () => deleteToken(index));
        actionCell.appendChild(del);
      });
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

    async function deleteToken(index) {
      const item = state.tokens[index];
      if (!item || !item.token) return;
      if (!confirm("Delete " + maskToken(item.token) + "?")) return;
      try {
        await requestJSON("/v1/tokens?token=" + encodeURIComponent(item.token), {
          method: "DELETE",
          headers: authHeaders()
        });
        $("mutation-status").textContent = "token deleted";
        await loadAll();
      } catch (err) {
        $("mutation-status").textContent = err.message;
      }
    }

    async function loadAll() {
      try {
        await loadTelemetry();
      } catch (err) {
        $("health-dot").classList.remove("ok");
        $("health-text").textContent = err.message;
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
