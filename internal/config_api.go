package internal

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

type configUpdateRequest struct {
	UpstreamProxy *string `json:"upstream_proxy"`
}

type publicConfig struct {
	APIEndpoint          string `json:"api_endpoint"`
	UpstreamProxySet     bool   `json:"upstream_proxy_set"`
	UpstreamProxyPreview string `json:"upstream_proxy_preview"`
	RuntimeConfigPath    string `json:"runtime_config_path"`
}

type configResponse struct {
	Object  string       `json:"object"`
	Data    publicConfig `json:"data"`
	Message string       `json:"message,omitempty"`
}

func HandleConfig(w http.ResponseWriter, r *http.Request) {
	if !requireAPIKey(w, r) {
		return
	}

	switch r.Method {
	case http.MethodGet:
		writeConfigResponse(w, http.StatusOK, "", GetRuntimeConfigSnapshot())
	case http.MethodPut, http.MethodPatch:
		handleConfigUpdate(w, r)
	default:
		writeInvalidRequestError(w, "Only GET, PUT and PATCH methods are allowed")
	}
}

func handleConfigUpdate(w http.ResponseWriter, r *http.Request) {
	var req configUpdateRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeInvalidRequestError(w, "Invalid JSON body")
		return
	}
	if req.UpstreamProxy == nil {
		writeInvalidRequestError(w, "upstream_proxy is required")
		return
	}

	snapshot, err := UpdateRuntimeConfig(RuntimeConfigUpdate{UpstreamProxy: req.UpstreamProxy})
	if err != nil {
		writeInvalidRequestError(w, err.Error())
		return
	}
	writeConfigResponse(w, http.StatusOK, "Config updated", snapshot)
}

func writeConfigResponse(w http.ResponseWriter, status int, message string, snapshot RuntimeConfigSnapshot) {
	writeJSON(w, status, configResponse{
		Object:  "config",
		Message: message,
		Data: publicConfig{
			APIEndpoint:          snapshot.APIEndpoint,
			UpstreamProxySet:     strings.TrimSpace(snapshot.UpstreamProxy) != "",
			UpstreamProxyPreview: maskProxyURL(snapshot.UpstreamProxy),
			RuntimeConfigPath:    snapshot.RuntimeConfigPath,
		},
	})
}

func validateUpstreamProxy(proxy string) error {
	if proxy == "" {
		return nil
	}
	parsed, err := url.Parse(proxy)
	if err != nil {
		return fmt.Errorf("invalid upstream_proxy URL")
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("upstream_proxy must include scheme and host")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "socks5", "socks5h":
		return nil
	default:
		return fmt.Errorf("upstream_proxy scheme must be http, https, socks5, or socks5h")
	}
}

func maskProxyURL(proxy string) string {
	if proxy == "" {
		return ""
	}
	parsed, err := url.Parse(proxy)
	if err != nil || parsed.User == nil {
		return proxy
	}
	username := parsed.User.Username()
	masked := *parsed
	masked.User = nil
	base := masked.String()
	prefix := parsed.Scheme + "://"
	base = strings.TrimPrefix(base, prefix)
	userInfo := url.User(username).String()
	if _, hasPassword := parsed.User.Password(); hasPassword {
		userInfo += ":***"
	}
	return prefix + userInfo + "@" + base
}
