package internal

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func initTokenTests(t *testing.T) {
	t.Helper()
	Cfg = &Config{}
	InitLogger()
}

func TestTokenManagerKeepsTokensTxtCompatibility(t *testing.T) {
	initTokenTests(t)

	tempDir := t.TempDir()
	tokenFile := filepath.Join(tempDir, "tokens.txt")
	content := "# comment\n\ntoken=alpha.one.two\nbeta.one.two\nbeta.one.two\n"
	if err := os.WriteFile(tokenFile, []byte(content), 0644); err != nil {
		t.Fatalf("write token file: %v", err)
	}

	tm := NewTokenManager(tempDir)
	if err := tm.loadTokens(); err != nil {
		t.Fatalf("load tokens: %v", err)
	}

	listed := tm.ListTokens()
	if len(listed) != 2 || listed[0].Token != "alpha.one.two" || listed[1].Token != "beta.one.two" {
		t.Fatalf("unexpected initial tokens: %+v", listed)
	}

	added, skipped, err := tm.AddTokens([]string{"token=beta.one.two", "gamma.one.two"})
	if err != nil {
		t.Fatalf("add tokens: %v", err)
	}
	if len(added) != 1 || added[0].Token != "gamma.one.two" {
		t.Fatalf("unexpected added tokens: %+v", added)
	}
	if len(skipped) != 1 || skipped[0] != "beta.one.two" {
		t.Fatalf("unexpected skipped tokens: %+v", skipped)
	}

	updated, err := tm.UpdateToken("token=alpha.one.two", "delta.one.two")
	if err != nil {
		t.Fatalf("update token: %v", err)
	}
	if updated.Token != "delta.one.two" {
		t.Fatalf("unexpected updated token: %+v", updated)
	}

	deleted, err := tm.DeleteToken("beta.one.two")
	if err != nil {
		t.Fatalf("delete token: %v", err)
	}
	if deleted.Token != "beta.one.two" {
		t.Fatalf("unexpected deleted token: %+v", deleted)
	}

	finalTokens, err := tm.readTokenEntriesFromFile()
	if err != nil {
		t.Fatalf("read final tokens: %v", err)
	}
	expected := []string{"delta.one.two", "gamma.one.two"}
	if len(finalTokens) != len(expected) {
		t.Fatalf("unexpected final token count: %+v", finalTokens)
	}
	for i, token := range expected {
		if finalTokens[i] != token {
			t.Fatalf("unexpected token order: %+v", finalTokens)
		}
	}
}

func TestHandleTokensCRUD(t *testing.T) {
	initTokenTests(t)

	tempDir := t.TempDir()
	tm := NewTokenManager(tempDir)
	if err := tm.writeTokenEntries(nil); err != nil {
		t.Fatalf("write empty token file: %v", err)
	}
	if err := tm.loadTokens(); err != nil {
		t.Fatalf("load tokens: %v", err)
	}

	oldCfg := Cfg
	Cfg = &Config{AuthTokens: []string{"admin-key"}}
	t.Cleanup(func() {
		Cfg = oldCfg
	})

	tokenManager = tm
	tokenOnce = sync.Once{}
	tokenOnce.Do(func() {})

	req := httptest.NewRequest(http.MethodGet, "/v1/tokens", nil)
	rec := httptest.NewRecorder()
	HandleTokens(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized, got %d", rec.Code)
	}

	call := func(method, target string, body interface{}) *httptest.ResponseRecorder {
		var payload []byte
		if body != nil {
			payload, _ = json.Marshal(body)
		}
		req := httptest.NewRequest(method, target, bytes.NewReader(payload))
		req.Header.Set("Authorization", "Bearer admin-key")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		HandleTokens(rec, req)
		return rec
	}

	rec = call(http.MethodPost, "/v1/tokens", map[string]interface{}{"token": "one.two.three"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected created, got %d: %s", rec.Code, rec.Body.String())
	}

	rec = call(http.MethodGet, "/v1/tokens", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "one.two.three") {
		t.Fatalf("unexpected list response: %d %s", rec.Code, rec.Body.String())
	}

	rec = call(http.MethodPut, "/v1/tokens", map[string]interface{}{
		"old_token": "one.two.three",
		"new_token": "four.five.six",
	})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "four.five.six") {
		t.Fatalf("unexpected update response: %d %s", rec.Code, rec.Body.String())
	}

	rec = call(http.MethodDelete, "/v1/tokens?token=four.five.six", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected delete response: %d %s", rec.Code, rec.Body.String())
	}

	finalTokens, err := tm.readTokenEntriesFromFile()
	if err != nil {
		t.Fatalf("read final tokens: %v", err)
	}
	if len(finalTokens) != 0 {
		t.Fatalf("expected empty tokens, got %+v", finalTokens)
	}
}

func TestHandleChatCompletionsWithoutUpstreamToken(t *testing.T) {
	initTokenTests(t)

	tempDir := t.TempDir()
	tm := NewTokenManager(tempDir)
	if err := tm.writeTokenEntries(nil); err != nil {
		t.Fatalf("write empty token file: %v", err)
	}
	if err := tm.loadTokens(); err != nil {
		t.Fatalf("load tokens: %v", err)
	}

	oldCfg := Cfg
	Cfg = &Config{
		AuthTokens: []string{"admin-key"},
	}
	t.Cleanup(func() {
		Cfg = oldCfg
	})

	tokenManager = tm
	tokenOnce = sync.Once{}
	tokenOnce.Do(func() {})

	body := map[string]interface{}{
		"model": "GLM-5.1",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
	}
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer admin-key")
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	HandleChatCompletions(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "upstream_token_unavailable") {
		t.Fatalf("unexpected response body: %s", rec.Body.String())
	}
}
