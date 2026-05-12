package internal

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestBuildUpstreamChatRequestUsesCurrentMinimalChatContract(t *testing.T) {
	InitLogger()
	oldCfg := Cfg
	Cfg = &Config{APIEndpoint: "https://chat.z.ai/api/v2/chat/completions"}
	t.Cleanup(func() {
		Cfg = oldCfg
	})

	req, modelName, err := buildUpstreamChatRequest(
		context.Background(),
		"fresh.user.token",
		[]Message{{Role: "user", Content: "hello"}},
		"GLM-5.1",
		false,
		nil,
		nil,
		false,
	)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if modelName != "GLM-5.1" {
		t.Fatalf("unexpected model name: %s", modelName)
	}
	if req.Method != "POST" {
		t.Fatalf("expected POST, got %s", req.Method)
	}
	if req.URL.String() != "https://chat.z.ai/api/v2/chat/completions" {
		t.Fatalf("expected bare chat endpoint, got %s", req.URL.String())
	}
	if req.URL.RawQuery != "" {
		t.Fatalf("expected no browser replay query string, got %q", req.URL.RawQuery)
	}

	if got := req.Header.Get("Authorization"); got != "Bearer fresh.user.token" {
		t.Fatalf("unexpected authorization header: %q", got)
	}
	if got := req.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("unexpected content-type: %q", got)
	}
	if got := req.Header.Get("X-FE-Version"); got == "" {
		t.Fatalf("expected X-FE-Version header")
	}
	for _, name := range []string{"X-Signature", "Cookie", "X-Forwarded-For", "X-Real-IP"} {
		if got := req.Header.Get(name); got != "" {
			t.Fatalf("expected %s to be omitted, got %q", name, got)
		}
	}

	bodyBytes, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	bodyText := string(bodyBytes)
	for _, staleField := range []string{"captcha_verify_param", "signature_prompt", "chat_id", "current_url"} {
		if strings.Contains(bodyText, staleField) {
			t.Fatalf("body contains stale browser replay field %q: %s", staleField, bodyText)
		}
	}

	var body map[string]any
	if err := json.Unmarshal(bodyBytes, &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["stream"] != false {
		t.Fatalf("expected upstream stream=false for non-stream request, got %#v", body["stream"])
	}
	if body["model"] != "GLM-5.1" {
		t.Fatalf("unexpected upstream model: %#v", body["model"])
	}
	if _, ok := body["messages"].([]any); !ok {
		t.Fatalf("expected messages array, got %#v", body["messages"])
	}
}
