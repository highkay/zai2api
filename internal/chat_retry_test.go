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
	oldFetch := fetchAuthSessionFunc
	oldRefreshFeVersion := refreshFeVersionFunc
	oldFeVersion := GetFeVersion()
	Cfg = &Config{}
	InitLogger()
	versionLock.Lock()
	feVersion = DefaultFeVersion
	versionLock.Unlock()
	fetchAuthSessionFunc = func(token string) (authSessionResponse, int, error) {
		return authSessionResponse{Token: token}, http.StatusOK, nil
	}
	refreshFeVersionFunc = func() (string, bool) {
		return DefaultFeVersion, false
	}
	t.Cleanup(func() {
		fetchAuthSessionFunc = oldFetch
		refreshFeVersionFunc = oldRefreshFeVersion
		versionLock.Lock()
		feVersion = oldFeVersion
		versionLock.Unlock()
	})
}

func setupChatRetryTokenManager() {
	tm := NewTokenManager("")
	tm.tokens = map[string]*TokenInfo{
		"upstream-token": {Token: "upstream-token", Valid: true},
	}
	tm.validTokens = []string{"upstream-token"}
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
	makeUpstreamRequestFunc = func(ctx context.Context, token string, messages []Message, model string, stream bool, imageURLs, videoURLs []string, hasTools bool) (*fhttp.Response, string, error) {
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
	makeUpstreamRequestFunc = func(ctx context.Context, token string, messages []Message, model string, stream bool, imageURLs, videoURLs []string, hasTools bool) (*fhttp.Response, string, error) {
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
	t.Cleanup(tm.Stop)
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
	refreshCalls := 0
	fetchAuthSessionFunc = func(token string) (authSessionResponse, int, error) {
		if token != "stale.one.two" {
			t.Fatalf("unexpected refresh token: %s", token)
		}
		refreshCalls++
		if refreshCalls == 1 {
			return authSessionResponse{Token: "stale.one.two"}, http.StatusOK, nil
		}
		return authSessionResponse{Token: "fresh.one.two"}, http.StatusOK, nil
	}
	t.Cleanup(func() {
		Cfg = oldCfg
		makeUpstreamRequestFunc = oldMakeUpstreamRequest
		fetchAuthSessionFunc = oldFetch
	})

	attempts := 0
	makeUpstreamRequestFunc = func(ctx context.Context, token string, messages []Message, model string, stream bool, imageURLs, videoURLs []string, hasTools bool) (*fhttp.Response, string, error) {
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

	tokens := tm.ListTokenRecords(TokenListOptions{Status: TokenStatusActive, IncludeToken: true})
	if len(tokens) != 1 || tokens[0].Token != "fresh.one.two" {
		t.Fatalf("expected refreshed token to persist, got %+v", tokens)
	}
}

func TestHandleChatCompletionsRefreshesTokenAfterUpstreamSessionRefreshError(t *testing.T) {
	initChatRetryTests(t)

	tempDir := t.TempDir()
	tokenFile := filepath.Join(tempDir, "tokens.txt")
	if err := os.WriteFile(tokenFile, []byte("stale.one.two\n"), 0644); err != nil {
		t.Fatalf("write token file: %v", err)
	}

	tm := NewTokenManager(tempDir)
	t.Cleanup(tm.Stop)
	if err := tm.loadTokens(); err != nil {
		t.Fatalf("load tokens: %v", err)
	}
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
	refreshCalls := 0
	fetchAuthSessionFunc = func(token string) (authSessionResponse, int, error) {
		if token != "stale.one.two" {
			t.Fatalf("unexpected refresh token: %s", token)
		}
		refreshCalls++
		if refreshCalls == 1 {
			return authSessionResponse{Token: "stale.one.two"}, http.StatusOK, nil
		}
		return authSessionResponse{Token: "fresh.one.two"}, http.StatusOK, nil
	}
	t.Cleanup(func() {
		Cfg = oldCfg
		makeUpstreamRequestFunc = oldMakeUpstreamRequest
		fetchAuthSessionFunc = oldFetch
	})

	attempts := 0
	makeUpstreamRequestFunc = func(ctx context.Context, token string, messages []Message, model string, stream bool, imageURLs, videoURLs []string, hasTools bool) (*fhttp.Response, string, error) {
		attempts++
		if attempts == 1 {
			if token != "stale.one.two" {
				t.Fatalf("expected stale token on first attempt, got %s", token)
			}
			sse := "data: {\"type\":\"chat:completion\",\"data\":{\"done\":true,\"error\":{\"code\":\"APP_VERSION_OUTDATED\",\"detail\":\"Please refresh the page to update the app, then try again.\"}}}\n\n" +
				"data: [DONE]\n\n"
			return &fhttp.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewBufferString(sse)),
			}, "GLM-5.1", nil
		}
		if token != "fresh.one.two" {
			t.Fatalf("expected refreshed token on second attempt, got %s", token)
		}
		sse := "data: {\"type\":\"chat:completion\",\"data\":{\"phase\":\"answer\",\"delta_content\":\"session refresh ok\"}}\n\n" +
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
	if refreshCalls != 2 {
		t.Fatalf("expected preflight plus forced refresh, got %d refresh calls", refreshCalls)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "session refresh ok") {
		t.Fatalf("expected refreshed response content, got %s", rec.Body.String())
	}

	activeTokens := tm.ListTokenRecords(TokenListOptions{Status: TokenStatusActive, IncludeToken: true})
	if len(activeTokens) != 1 || activeTokens[0].Token != "fresh.one.two" {
		t.Fatalf("expected refreshed token to persist, got %+v", activeTokens)
	}
	rotatedTokens := tm.ListTokenRecords(TokenListOptions{Status: TokenStatusRotated, IncludeToken: true})
	if len(rotatedTokens) != 0 {
		t.Fatalf("expected old token to be pruned, got %+v", rotatedTokens)
	}
}

func TestHandleChatCompletionsRefreshesFeVersionAfterUpstreamSessionRefreshError(t *testing.T) {
	initChatRetryTests(t)

	tempDir := t.TempDir()
	tokenFile := filepath.Join(tempDir, "tokens.txt")
	if err := os.WriteFile(tokenFile, []byte("stable.one.two\n"), 0644); err != nil {
		t.Fatalf("write token file: %v", err)
	}

	tm := NewTokenManager(tempDir)
	t.Cleanup(tm.Stop)
	if err := tm.loadTokens(); err != nil {
		t.Fatalf("load tokens: %v", err)
	}
	tokenManager = tm
	tokenOnce = sync.Once{}
	tokenOnce.Do(func() {})

	oldCfg := Cfg
	oldMakeUpstreamRequest := makeUpstreamRequestFunc
	Cfg = &Config{
		AuthTokens: []string{"admin-key"},
		RetryCount: 1,
	}
	feRefreshCalls := 0
	refreshFeVersionFunc = func() (string, bool) {
		feRefreshCalls++
		versionLock.Lock()
		feVersion = "prod-fe-9.9.9"
		versionLock.Unlock()
		return "prod-fe-9.9.9", true
	}
	t.Cleanup(func() {
		Cfg = oldCfg
		makeUpstreamRequestFunc = oldMakeUpstreamRequest
	})

	attempts := 0
	makeUpstreamRequestFunc = func(ctx context.Context, token string, messages []Message, model string, stream bool, imageURLs, videoURLs []string, hasTools bool) (*fhttp.Response, string, error) {
		attempts++
		if token != "stable.one.two" {
			t.Fatalf("expected stable token, got %s", token)
		}
		if attempts == 1 {
			if GetFeVersion() != DefaultFeVersion {
				t.Fatalf("expected default fe version before refresh, got %s", GetFeVersion())
			}
			sse := "data: {\"type\":\"chat:completion\",\"data\":{\"done\":true,\"error\":{\"code\":\"APP_VERSION_OUTDATED\",\"detail\":\"Please refresh the page to update the app, then try again.\"}}}\n\n" +
				"data: [DONE]\n\n"
			return &fhttp.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewBufferString(sse)),
			}, "GLM-5.1", nil
		}
		if GetFeVersion() != "prod-fe-9.9.9" {
			t.Fatalf("expected refreshed fe version on retry, got %s", GetFeVersion())
		}
		sse := "data: {\"type\":\"chat:completion\",\"data\":{\"phase\":\"answer\",\"delta_content\":\"fe refresh ok\"}}\n\n" +
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
	if feRefreshCalls != 1 {
		t.Fatalf("expected one fe version refresh, got %d", feRefreshCalls)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "fe refresh ok") {
		t.Fatalf("expected refreshed response content, got %s", rec.Body.String())
	}
}

func TestHandleChatCompletionsRefreshesTokenBeforeChatCall(t *testing.T) {
	initChatRetryTests(t)

	tempDir := t.TempDir()
	tokenFile := filepath.Join(tempDir, "tokens.txt")
	if err := os.WriteFile(tokenFile, []byte("stored.one.two\n"), 0644); err != nil {
		t.Fatalf("write token file: %v", err)
	}

	tm := NewTokenManager(tempDir)
	t.Cleanup(tm.Stop)
	if err := tm.loadTokens(); err != nil {
		t.Fatalf("load tokens: %v", err)
	}
	tokenManager = tm
	tokenOnce = sync.Once{}
	tokenOnce.Do(func() {})

	oldCfg := Cfg
	oldMakeUpstreamRequest := makeUpstreamRequestFunc
	oldFetch := fetchAuthSessionFunc
	Cfg = &Config{
		AuthTokens: []string{"admin-key"},
		RetryCount: 0,
	}
	fetchAuthSessionFunc = func(token string) (authSessionResponse, int, error) {
		if token != "stored.one.two" {
			t.Fatalf("unexpected refresh token: %s", token)
		}
		return authSessionResponse{Token: "fresh.one.two"}, http.StatusOK, nil
	}
	t.Cleanup(func() {
		Cfg = oldCfg
		makeUpstreamRequestFunc = oldMakeUpstreamRequest
		fetchAuthSessionFunc = oldFetch
	})

	makeUpstreamRequestFunc = func(ctx context.Context, token string, messages []Message, model string, stream bool, imageURLs, videoURLs []string, hasTools bool) (*fhttp.Response, string, error) {
		if token != "fresh.one.two" {
			t.Fatalf("expected refreshed token before upstream call, got %s", token)
		}
		sse := "data: {\"type\":\"chat:completion\",\"data\":{\"phase\":\"answer\",\"delta_content\":\"preflight ok\"}}\n\n" +
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

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "preflight ok") {
		t.Fatalf("expected response content, got %s", rec.Body.String())
	}
}
