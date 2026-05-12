package internal

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandleConfigUpdatesAndMasksUpstreamProxy(t *testing.T) {
	oldCfg := Cfg
	tempDir := t.TempDir()
	Cfg = &Config{
		APIEndpoint:       "https://chat.z.ai/api/v2/chat/completions",
		AuthTokens:        []string{"admin-key"},
		RuntimeConfigPath: filepath.Join(tempDir, "runtime_config.json"),
	}
	t.Cleanup(func() {
		Cfg = oldCfg
	})

	call := func(method, target string, body interface{}, auth bool) *httptest.ResponseRecorder {
		var payload []byte
		if body != nil {
			payload, _ = json.Marshal(body)
		}
		req := httptest.NewRequest(method, target, bytes.NewReader(payload))
		if auth {
			req.Header.Set("Authorization", "Bearer admin-key")
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		HandleConfig(rec, req)
		return rec
	}

	rec := call(http.MethodGet, "/v1/config", nil, false)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized, got %d", rec.Code)
	}

	proxy := "http://fast:secret@192.168.1.18:2260"
	rec = call(http.MethodPut, "/v1/config", map[string]string{"upstream_proxy": proxy}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected update OK, got %d: %s", rec.Code, rec.Body.String())
	}
	if Cfg.UpstreamProxy != proxy {
		t.Fatalf("runtime proxy was not updated: %q", Cfg.UpstreamProxy)
	}
	if strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("response leaked proxy password: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "fast:***@192.168.1.18:2260") {
		t.Fatalf("response did not include masked proxy preview: %s", rec.Body.String())
	}
	data, err := os.ReadFile(Cfg.RuntimeConfigPath)
	if err != nil {
		t.Fatalf("read runtime config: %v", err)
	}
	if !strings.Contains(string(data), proxy) {
		t.Fatalf("runtime config did not persist full proxy: %s", string(data))
	}

	Cfg.UpstreamProxy = ""
	if err := loadRuntimeConfigOverrides(); err != nil {
		t.Fatalf("reload runtime config: %v", err)
	}
	if Cfg.UpstreamProxy != proxy {
		t.Fatalf("runtime config did not reload proxy: %q", Cfg.UpstreamProxy)
	}

	rec = call(http.MethodPut, "/v1/config", map[string]string{"upstream_proxy": ""}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected direct update OK, got %d: %s", rec.Code, rec.Body.String())
	}
	if Cfg.UpstreamProxy != "" {
		t.Fatalf("expected proxy to be cleared, got %q", Cfg.UpstreamProxy)
	}
}

func TestHandleConfigRejectsInvalidProxy(t *testing.T) {
	oldCfg := Cfg
	Cfg = &Config{
		AuthTokens:        []string{"admin-key"},
		RuntimeConfigPath: filepath.Join(t.TempDir(), "runtime_config.json"),
	}
	t.Cleanup(func() {
		Cfg = oldCfg
	})

	body, _ := json.Marshal(map[string]string{"upstream_proxy": "ftp://127.0.0.1:21"})
	req := httptest.NewRequest(http.MethodPut, "/v1/config", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer admin-key")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	HandleConfig(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected bad request, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleConfigUpdatesBrowserHelperSettings(t *testing.T) {
	oldCfg := Cfg
	tempDir := t.TempDir()
	Cfg = &Config{
		APIEndpoint:       "https://chat.z.ai/api/v2/chat/completions",
		AuthTokens:        []string{"admin-key"},
		RuntimeConfigPath: filepath.Join(tempDir, "runtime_config.json"),
	}
	t.Cleanup(func() {
		Cfg = oldCfg
	})

	payload, _ := json.Marshal(map[string]string{
		"chat_backend":       "browser_helper",
		"browser_helper_url": "http://host.docker.internal:39090/v1/browser-chat/completions",
	})
	req := httptest.NewRequest(http.MethodPut, "/v1/config", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer admin-key")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	HandleConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if Cfg.ChatBackend != "browser_helper" {
		t.Fatalf("expected browser_helper backend, got %q", Cfg.ChatBackend)
	}
	if Cfg.BrowserHelperURL != "http://host.docker.internal:39090/v1/browser-chat/completions" {
		t.Fatalf("unexpected browser helper URL: %q", Cfg.BrowserHelperURL)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"chat_backend":"browser_helper"`) {
		t.Fatalf("response missing browser_helper backend: %s", body)
	}
	if !strings.Contains(body, `"browser_helper_url_set":true`) {
		t.Fatalf("response missing browser helper URL flag: %s", body)
	}
	if !strings.Contains(body, `"browser_helper_ready":true`) {
		t.Fatalf("response missing browser helper ready flag: %s", body)
	}
}
