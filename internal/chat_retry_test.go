package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
)

func initChatRetryTests(t *testing.T) {
	t.Helper()
	Cfg = &Config{}
	InitLogger()
}

func setupChatRetryTokenManager() {
	tm := NewTokenManager("")
	tm.tokens = map[string]*TokenInfo{
		"upstream-token": {Token: "upstream-token", Valid: true},
	}
	tm.validTokens = []string{"upstream-token"}
	tm.fileTokens = []string{"upstream-token"}
	tokenManager = tm
	tokenOnce = sync.Once{}
	tokenOnce.Do(func() {})
}

func TestHandleChatCompletionsStopsRetryingOnUpstreamConcurrencyLimit(t *testing.T) {
	initChatRetryTests(t)
	setupChatRetryTokenManager()

	oldCfg := Cfg
	oldMakeUpstreamRequest := makeUpstreamRequestFunc
	Cfg = &Config{
		AuthTokens: []string{"admin-key"},
		RetryCount: 5,
	}
	t.Cleanup(func() {
		Cfg = oldCfg
		makeUpstreamRequestFunc = oldMakeUpstreamRequest
	})

	attempts := 0
	makeUpstreamRequestFunc = func(ctx context.Context, token string, messages []Message, model string, imageURLs, videoURLs []string, hasTools bool) (*fhttp.Response, string, error) {
		attempts++
		sse := "data: {\"type\":\"chat:completion\",\"data\":{\"done\":true,\"error\":{\"code\":429,\"detail\":\"Your current concurrent conversation limit has been reached. Please try again later.\"}}}\n\n" +
			"data: [DONE]\n\n"
		return &fhttp.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewBufferString(sse)),
		}, "GLM-5.1", nil
	}

	body := map[string]interface{}{
		"model": "GLM-5.1",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
		"stream": false,
	}
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer admin-key")
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	HandleChatCompletions(rec, req)

	if attempts != 1 {
		t.Fatalf("expected exactly one upstream attempt, got %d", attempts)
	}
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d: %s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("current concurrent conversation limit")) {
		t.Fatalf("expected concurrency-limit message, got %s", rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("rate_limit_exceeded")) {
		t.Fatalf("expected rate_limit_exceeded code, got %s", rec.Body.String())
	}
}

func TestHandleChatCompletionsStopsWhenRequestContextCanceled(t *testing.T) {
	initChatRetryTests(t)
	setupChatRetryTokenManager()

	oldCfg := Cfg
	oldMakeUpstreamRequest := makeUpstreamRequestFunc
	Cfg = &Config{
		AuthTokens: []string{"admin-key"},
		RetryCount: 5,
	}
	t.Cleanup(func() {
		Cfg = oldCfg
		makeUpstreamRequestFunc = oldMakeUpstreamRequest
	})

	attempts := 0
	cancelCtx, cancel := context.WithCancel(context.Background())
	makeUpstreamRequestFunc = func(ctx context.Context, token string, messages []Message, model string, imageURLs, videoURLs []string, hasTools bool) (*fhttp.Response, string, error) {
		attempts++
		cancel()
		return nil, "", context.Canceled
	}

	body := map[string]interface{}{
		"model": "GLM-5.1",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
		"stream": false,
	}
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(payload)).WithContext(cancelCtx)
	req.Header.Set("Authorization", "Bearer admin-key")
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	HandleChatCompletions(rec, req)

	if attempts != 1 {
		t.Fatalf("expected one upstream attempt before cancellation, got %d", attempts)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("expected no downstream body after cancellation, got %s", rec.Body.String())
	}
}

func TestHandleChatCompletionsRefreshesTokenAfterUpstreamAuthFailure(t *testing.T) {
	initChatRetryTests(t)

	tempDir := t.TempDir()
	tokenFile := filepath.Join(tempDir, "tokens.txt")
	if err := os.WriteFile(tokenFile, []byte("stale.one.two\n"), 0644); err != nil {
		t.Fatalf("write token file: %v", err)
	}

	tm := NewTokenManager(tempDir)
	if err := tm.loadTokens(); err != nil {
		t.Fatalf("load tokens: %v", err)
	}
	tm.mu.Lock()
	tm.tokens["stale.one.two"].LastChecked = time.Now()
	tm.mu.Unlock()
	tokenManager = tm
	tokenOnce = sync.Once{}
	tokenOnce.Do(func() {})

	oldCfg := Cfg
	oldMakeUpstreamRequest := makeUpstreamRequestFunc
	oldFetch := fetchAuthSessionFunc
	Cfg = &Config{
		AuthTokens: []string{"admin-key"},
		RetryCount: 1,
	}
	fetchAuthSessionFunc = func(token string) (authSessionResponse, int, error) {
		if token != "stale.one.two" {
			t.Fatalf("unexpected refresh token: %s", token)
		}
		return authSessionResponse{Token: "fresh.one.two"}, http.StatusOK, nil
	}
	t.Cleanup(func() {
		Cfg = oldCfg
		makeUpstreamRequestFunc = oldMakeUpstreamRequest
		fetchAuthSessionFunc = oldFetch
	})

	attempts := 0
	makeUpstreamRequestFunc = func(ctx context.Context, token string, messages []Message, model string, imageURLs, videoURLs []string, hasTools bool) (*fhttp.Response, string, error) {
		attempts++
		if attempts == 1 {
			if token != "stale.one.two" {
				t.Fatalf("expected stale token on first attempt, got %s", token)
			}
			return &fhttp.Response{
				StatusCode: http.StatusUnauthorized,
				Body:       io.NopCloser(bytes.NewBufferString(`{"error":{"message":"expired"}}`)),
			}, "GLM-5.1", nil
		}
		if token != "fresh.one.two" {
			t.Fatalf("expected refreshed token on second attempt, got %s", token)
		}
		sse := "data: {\"type\":\"chat:completion\",\"data\":{\"phase\":\"answer\",\"delta_content\":\"hello after refresh\"}}\n\n" +
			"data: {\"type\":\"chat:completion\",\"data\":{\"phase\":\"done\",\"done\":true}}\n\n" +
			"data: [DONE]\n\n"
		return &fhttp.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewBufferString(sse)),
		}, "GLM-5.1", nil
	}

	body := map[string]interface{}{
		"model": "GLM-5.1",
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
		"stream": false,
	}
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer admin-key")
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	HandleChatCompletions(rec, req)

	if attempts != 2 {
		t.Fatalf("expected two upstream attempts, got %d", attempts)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "hello after refresh") {
		t.Fatalf("expected refreshed response content, got %s", rec.Body.String())
	}

	tokens, err := tm.readTokenEntriesFromFile()
	if err != nil {
		t.Fatalf("read token file: %v", err)
	}
	if len(tokens) != 1 || tokens[0] != "fresh.one.two" {
		t.Fatalf("expected refreshed token to persist, got %+v", tokens)
	}
}
