package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	"github.com/google/uuid"
)

type UpstreamChatPayload struct {
	Stream               bool                     `json:"stream"`
	Model                string                   `json:"model"`
	Messages             []map[string]interface{} `json:"messages"`
	Features             map[string]interface{}   `json:"features,omitempty"`
	MCPServers           []string                 `json:"mcp_servers,omitempty"`
	Files                []map[string]interface{} `json:"files,omitempty"`
	CurrentUserMessageID string                   `json:"current_user_message_id,omitempty"`
}

type browserHelperChatRequest struct {
	Token   string              `json:"token"`
	Payload UpstreamChatPayload `json:"payload"`
}

func makeDirectUpstreamRequest(ctx context.Context, token string, messages []Message, model string, stream bool, imageURLs, videoURLs []string, hasTools bool) (*fhttp.Response, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	requestCtx, cancel := deriveCancelableRequestContext(ctx)

	req, targetModel, err := buildUpstreamChatRequest(requestCtx, token, messages, model, stream, imageURLs, videoURLs, hasTools)
	if err != nil {
		cancel()
		return nil, "", err
	}

	client, err := TLSHTTPClient(300 * time.Second)
	if err != nil {
		cancel()
		return nil, "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		cancel()
		return nil, "", err
	}
	if resp.Body != nil {
		resp.Body = &cancelOnCloseReadCloser{
			ReadCloser: resp.Body,
			cancel:     cancel,
		}
	} else {
		cancel()
	}

	LogDebug("Direct upstream response: status=%d", resp.StatusCode)
	return resp, targetModel, nil
}

func makeBrowserHelperUpstreamRequest(ctx context.Context, token string, messages []Message, model string, stream bool, imageURLs, videoURLs []string, hasTools bool) (*fhttp.Response, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	helperURL := strings.TrimSpace(GetBrowserHelperURL())
	if helperURL == "" {
		return nil, "", fmt.Errorf("browser helper URL is not configured")
	}

	payload, targetModel, err := buildUpstreamChatPayload(ctx, token, messages, model, stream, imageURLs, videoURLs, hasTools)
	if err != nil {
		return nil, "", err
	}
	bodyBytes, err := json.Marshal(browserHelperChatRequest{
		Token:   token,
		Payload: payload,
	})
	if err != nil {
		return nil, "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, helperURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if authToken := strings.TrimSpace(GetBrowserHelperAuthToken()); authToken != "" {
		req.Header.Set("Authorization", "Bearer "+authToken)
	}

	client := &http.Client{
		Timeout: time.Duration(GetBrowserHelperTimeout()) * time.Second,
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	LogDebug("Browser helper response: status=%d", resp.StatusCode)

	return &fhttp.Response{
		StatusCode: resp.StatusCode,
		Header:     fhttp.Header(resp.Header),
		Body:       resp.Body,
	}, targetModel, nil
}

func buildUpstreamChatPayload(ctx context.Context, token string, messages []Message, model string, stream bool, imageURLs, videoURLs []string, hasTools bool) (UpstreamChatPayload, string, error) {
	if strings.TrimSpace(token) == "" {
		return UpstreamChatPayload{}, "", fmt.Errorf("empty upstream token")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	userMsgID := uuid.New().String()
	mapping := GetUpstreamConfig(model)
	var targetModel string
	var enableThinking, autoWebSearch bool
	var mcpServers []string

	if mapping != nil {
		targetModel = mapping.UpstreamModelID
		enableThinking = mapping.EnableThinking
		autoWebSearch = mapping.AutoWebSearch
		mcpServers = mapping.MCPServers
		LogDebug("Model mapping: %s -> %s (thinking=%v, search=%v)", model, targetModel, enableThinking, autoWebSearch)
	} else {
		targetModel = GetTargetModel(model)
		enableThinking = IsThinkingModel(model)
		autoWebSearch = IsSearchModel(model)
		LogDebug("Using fallback model mapping: %s -> %s", model, targetModel)
	}

	if isVisionModelID(strings.ToLower(targetModel)) {
		autoWebSearch = false
	}
	if hasTools {
		autoWebSearch = false
		LogDebug("[Upstream] Disabled auto web search because custom tools were provided")
	}
	if len(imageURLs) > 0 || len(videoURLs) > 0 {
		vlmServers := []string{"vlm-image-search", "vlm-image-recognition", "vlm-image-processing"}
		existingSet := make(map[string]bool)
		for _, s := range mcpServers {
			existingSet[s] = true
		}
		for _, s := range vlmServers {
			if !existingSet[s] {
				mcpServers = append(mcpServers, s)
			}
		}
	}

	urlToFileID := make(map[string]string)
	var filesData []map[string]interface{}

	if len(imageURLs) > 0 {
		LogDebug("[Upstream] Uploading %d images...", len(imageURLs))
		imageFiles, err := UploadImages(ctx, token, imageURLs)
		if err != nil {
			return UpstreamChatPayload{}, "", err
		}
		for i, f := range imageFiles {
			if i < len(imageURLs) {
				urlToFileID[imageURLs[i]] = f.ID
			}
			filesData = append(filesData, map[string]interface{}{
				"type":            f.Type,
				"file":            f.File,
				"id":              f.ID,
				"url":             f.URL,
				"name":            f.Name,
				"status":          f.Status,
				"size":            f.Size,
				"error":           f.Error,
				"itemId":          f.ItemID,
				"media":           f.Media,
				"ref_user_msg_id": userMsgID,
			})
		}
	}
	if len(videoURLs) > 0 {
		LogDebug("[Upstream] Uploading %d videos...", len(videoURLs))
		videoFiles, err := UploadVideos(ctx, token, videoURLs)
		if err != nil {
			return UpstreamChatPayload{}, "", err
		}
		for i, f := range videoFiles {
			if i < len(videoURLs) {
				urlToFileID[videoURLs[i]] = f.ID
			}
			filesData = append(filesData, map[string]interface{}{
				"type":            f.Type,
				"file":            f.File,
				"id":              f.ID,
				"url":             f.URL,
				"name":            f.Name,
				"status":          f.Status,
				"size":            f.Size,
				"error":           f.Error,
				"itemId":          f.ItemID,
				"media":           f.Media,
				"ref_user_msg_id": userMsgID,
			})
		}
	}

	var upstreamMessages []map[string]interface{}
	for _, msg := range messages {
		upstreamMessages = append(upstreamMessages, msg.ToUpstreamMessage(urlToFileID))
	}

	payload := UpstreamChatPayload{
		Stream:   stream,
		Model:    targetModel,
		Messages: upstreamMessages,
	}

	features := make(map[string]interface{})
	if enableThinking {
		features["enable_thinking"] = true
	}
	if autoWebSearch && !hasTools {
		features["web_search"] = true
		features["auto_web_search"] = true
	}
	if len(imageURLs) > 0 || len(videoURLs) > 0 {
		features["image_generation"] = true
	}
	if len(features) > 0 {
		payload.Features = features
	}
	if len(mcpServers) > 0 {
		payload.MCPServers = mcpServers
	}
	if len(filesData) > 0 {
		payload.Files = filesData
		payload.CurrentUserMessageID = userMsgID
	}

	return payload, targetModel, nil
}

func encodeUpstreamChatPayload(payload UpstreamChatPayload) ([]byte, error) {
	return json.Marshal(payload)
}

func buildUpstreamChatRequest(ctx context.Context, token string, messages []Message, model string, stream bool, imageURLs, videoURLs []string, hasTools bool) (*fhttp.Request, string, error) {
	payload, targetModel, err := buildUpstreamChatPayload(ctx, token, messages, model, stream, imageURLs, videoURLs, hasTools)
	if err != nil {
		return nil, "", err
	}
	bodyBytes, err := encodeUpstreamChatPayload(payload)
	if err != nil {
		return nil, "", err
	}
	req, err := fhttp.NewRequestWithContext(ctx, "POST", chatAPIEndpoint(), bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-FE-Version", GetFeVersion())
	req.Header.Set("Content-Type", "application/json")
	return req, targetModel, nil
}

func readBrowserHelperError(resp *http.Response) string {
	if resp == nil || resp.Body == nil {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(body))
}
