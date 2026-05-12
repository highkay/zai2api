package internal

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMakeUpstreamRequestUsesBrowserHelperBackend(t *testing.T) {
	InitLogger()

	var (
		gotAuthHeader string
		gotToken      string
		gotPayload    UpstreamChatPayload
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthHeader = r.Header.Get("Authorization")
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Fatalf("unexpected content-type: %s", ct)
		}
		var req browserHelperChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode helper request: %v", err)
		}
		gotToken = req.Token
		gotPayload = req.Payload
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"chat:completion\",\"data\":{\"phase\":\"answer\",\"delta_content\":\"hello from helper\"}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	oldCfg := Cfg
	Cfg = &Config{
		ChatBackend:       "browser_helper",
		BrowserHelperURL:  server.URL,
		BrowserHelperAuth: "helper-admin",
	}
	t.Cleanup(func() {
		Cfg = oldCfg
	})

	resp, modelName, err := makeUpstreamRequest(
		context.Background(),
		"upstream-token",
		[]Message{{Role: "user", Content: "hello"}},
		"GLM-5.1",
		true,
		nil,
		nil,
		false,
	)
	if err != nil {
		t.Fatalf("makeUpstreamRequest: %v", err)
	}
	defer resp.Body.Close()

	if gotAuthHeader != "Bearer helper-admin" {
		t.Fatalf("unexpected helper auth header: %q", gotAuthHeader)
	}
	if gotToken != "upstream-token" {
		t.Fatalf("unexpected upstream token: %q", gotToken)
	}
	if modelName != "GLM-5.1" {
		t.Fatalf("unexpected model name: %q", modelName)
	}
	if gotPayload.Model != "GLM-5.1" {
		t.Fatalf("unexpected helper payload model: %q", gotPayload.Model)
	}
	if len(gotPayload.Messages) != 1 {
		t.Fatalf("expected one message, got %+v", gotPayload.Messages)
	}
	if gotPayload.Messages[0]["content"] != "hello" {
		t.Fatalf("unexpected helper payload content: %+v", gotPayload.Messages[0])
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected helper response status: %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("unexpected helper response content-type: %s", ct)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read helper response body: %v", err)
	}
	if !strings.Contains(string(body), "hello from helper") {
		t.Fatalf("unexpected helper response body: %s", string(body))
	}
}
