package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

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
		}, "GLM-4.6", nil
	}

	body := map[string]interface{}{
		"model": "GLM-4.6",
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
