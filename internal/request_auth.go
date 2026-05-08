package internal

import (
	"net/http"
	"strings"
)

func extractRequestAPIKey(r *http.Request) string {
	return strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
}

func requireAPIKey(w http.ResponseWriter, r *http.Request) bool {
	apiKey := extractRequestAPIKey(r)

	if !Cfg.SkipAuthToken {
		if apiKey == "" {
			LogDebug("Missing Authorization header")
			writeError(w, http.StatusUnauthorized, ErrTypeAuthentication, "Missing or invalid Authorization header", "invalid_api_key")
			return false
		}
		if !ValidateAuthToken(apiKey) {
			LogDebug("Invalid API key: %s...", apiKey[:min(8, len(apiKey))])
			writeError(w, http.StatusUnauthorized, ErrTypeAuthentication, "Invalid API key", "invalid_api_key")
			return false
		}
		LogDebug("API key validated: %s...", apiKey[:min(8, len(apiKey))])
		return true
	}

	LogDebug("SKIP_AUTH_TOKEN enabled, skipping API key validation")
	return true
}
