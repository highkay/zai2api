package internal

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

type tokenCreateRequest struct {
	Token  string   `json:"token"`
	Tokens []string `json:"tokens"`
}

type tokenUpdateRequest struct {
	ID       int64  `json:"id"`
	Token    string `json:"token"`
	OldToken string `json:"old_token"`
	NewToken string `json:"new_token"`
	Status   string `json:"status"`
	Reason   string `json:"reason"`
}

type tokenDeleteRequest struct {
	ID    int64  `json:"id"`
	Token string `json:"token"`
	Hard  bool   `json:"hard"`
}

type tokenListSummary struct {
	Total    int `json:"total"`
	Active   int `json:"active"`
	Invalid  int `json:"invalid"`
	Disabled int `json:"disabled"`
	Rotated  int `json:"rotated"`
}

type tokenListResponse struct {
	Object  string           `json:"object"`
	Data    []TokenInfo      `json:"data"`
	Count   int              `json:"count"`
	Summary tokenListSummary `json:"summary"`
	Reveal  bool             `json:"reveal"`
}

type tokenResponse struct {
	Object  string    `json:"object"`
	Data    TokenInfo `json:"data"`
	Message string    `json:"message,omitempty"`
	Reveal  bool      `json:"reveal"`
}

type tokenCreateResponse struct {
	Object  string      `json:"object"`
	Data    []TokenInfo `json:"data"`
	Count   int         `json:"count"`
	Message string      `json:"message,omitempty"`
	Skipped []string    `json:"skipped,omitempty"`
	Reveal  bool        `json:"reveal"`
}

func HandleTokens(w http.ResponseWriter, r *http.Request) {
	if !requireAPIKey(w, r) {
		return
	}

	switch r.Method {
	case http.MethodGet:
		handleTokenGet(w, r)
	case http.MethodPost:
		handleTokenCreate(w, r)
	case http.MethodPut:
		handleTokenUpdate(w, r)
	case http.MethodDelete:
		handleTokenDelete(w, r)
	default:
		writeInvalidRequestError(w, "Only GET, POST, PUT and DELETE methods are allowed")
	}
}

func handleTokenGet(w http.ResponseWriter, r *http.Request) {
	reveal := tokenRevealAllowed(r)
	token := r.URL.Query().Get("token")
	id := parseInt64Query(r, "id")
	if token != "" || id > 0 {
		var info TokenInfo
		var exists bool
		if id > 0 {
			info, exists = GetTokenManager().GetTokenInfoByID(id)
		} else {
			info, exists = GetTokenManager().GetTokenInfo(token)
		}
		if !exists {
			writeError(w, http.StatusNotFound, ErrTypeNotFound, "Token not found", "token_not_found")
			return
		}
		writeJSON(w, http.StatusOK, tokenResponse{
			Object: "token",
			Data:   sanitizeTokenInfo(info, reveal),
			Reveal: reveal,
		})
		return
	}

	status := r.URL.Query().Get("status")
	if status == "" {
		status = "all"
	}
	tokens := GetTokenManager().ListTokenRecords(TokenListOptions{
		Status:       status,
		Source:       r.URL.Query().Get("source"),
		IncludeToken: reveal,
	})
	writeJSON(w, http.StatusOK, tokenListResponse{
		Object:  "list",
		Data:    sanitizeTokenInfos(tokens, reveal),
		Count:   len(tokens),
		Summary: summarizeTokens(tokens),
		Reveal:  reveal,
	})
}

func handleTokenCreate(w http.ResponseWriter, r *http.Request) {
	var req tokenCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeInvalidRequestError(w, "Invalid JSON body")
		return
	}

	inputs := make([]string, 0, len(req.Tokens)+1)
	if req.Token != "" {
		inputs = append(inputs, req.Token)
	}
	inputs = append(inputs, req.Tokens...)

	added, skipped, err := GetTokenManager().AddTokens(inputs)
	if err != nil {
		writeInvalidRequestError(w, err.Error())
		return
	}

	status := http.StatusOK
	if len(added) > 0 {
		status = http.StatusCreated
	}

	reveal := tokenRevealAllowed(r)
	writeJSON(w, status, tokenCreateResponse{
		Object:  "list",
		Data:    sanitizeTokenInfos(added, reveal),
		Count:   len(added),
		Message: "Tokens updated",
		Skipped: maskTokenValues(skipped),
		Reveal:  reveal,
	})
}

func handleTokenUpdate(w http.ResponseWriter, r *http.Request) {
	var req tokenUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeInvalidRequestError(w, "Invalid JSON body")
		return
	}

	var info TokenInfo
	var err error
	switch {
	case req.Status != "":
		if req.ID > 0 {
			info, err = GetTokenManager().SetTokenStatusByID(req.ID, req.Status, req.Reason)
		} else {
			token := req.Token
			if token == "" {
				token = req.OldToken
			}
			info, err = GetTokenManager().SetTokenStatus(token, req.Status, req.Reason)
		}
	default:
		info, err = GetTokenManager().UpdateToken(req.OldToken, req.NewToken)
	}
	if err != nil {
		writeTokenMutationError(w, err)
		return
	}

	reveal := tokenRevealAllowed(r)
	writeJSON(w, http.StatusOK, tokenResponse{
		Object:  "token",
		Data:    sanitizeTokenInfo(info, reveal),
		Message: "Token updated",
		Reveal:  reveal,
	})
}

func handleTokenDelete(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	id := parseInt64Query(r, "id")
	hard := parseBoolQuery(r, "hard")

	if token == "" && id == 0 {
		var req tokenDeleteRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			token = req.Token
			id = req.ID
			hard = hard || req.Hard
		}
	}
	if token == "" && id == 0 {
		writeInvalidRequestError(w, "token or id is required")
		return
	}

	var info TokenInfo
	var err error
	if id > 0 {
		info, err = GetTokenManager().DeleteTokenByID(id, hard)
	} else {
		info, err = GetTokenManager().DeleteTokenWithMode(token, hard)
	}
	if err != nil {
		writeTokenMutationError(w, err)
		return
	}

	reveal := tokenRevealAllowed(r)
	writeJSON(w, http.StatusOK, tokenResponse{
		Object:  "token",
		Data:    sanitizeTokenInfo(info, reveal),
		Message: "Token deleted",
		Reveal:  reveal,
	})
}

func tokenRevealAllowed(r *http.Request) bool {
	if !parseBoolQuery(r, "reveal") {
		return false
	}
	return Cfg != nil && Cfg.TokenAPIAllowReveal
}

func sanitizeTokenInfos(tokens []TokenInfo, reveal bool) []TokenInfo {
	result := make([]TokenInfo, 0, len(tokens))
	for _, token := range tokens {
		result = append(result, sanitizeTokenInfo(token, reveal))
	}
	return result
}

func sanitizeTokenInfo(info TokenInfo, reveal bool) TokenInfo {
	if info.TokenPreview == "" && info.Token != "" {
		info.TokenPreview = tokenPreview(info.Token)
	}
	if info.TokenHash == "" && info.Token != "" {
		info.TokenHash = tokenHash(info.Token)
	}
	if !reveal {
		info.Token = ""
	}
	return info
}

func summarizeTokens(tokens []TokenInfo) tokenListSummary {
	var summary tokenListSummary
	for _, token := range tokens {
		summary.Total++
		switch token.Status {
		case TokenStatusActive:
			summary.Active++
		case TokenStatusInvalid:
			summary.Invalid++
		case TokenStatusDisabled:
			summary.Disabled++
		case TokenStatusRotated:
			summary.Rotated++
		}
	}
	return summary
}

func maskTokenValues(tokens []string) []string {
	if len(tokens) == 0 {
		return nil
	}
	result := make([]string, 0, len(tokens))
	for _, token := range tokens {
		result = append(result, tokenPreview(token))
	}
	return result
}

func parseInt64Query(r *http.Request, key string) int64 {
	value := strings.TrimSpace(r.URL.Query().Get(key))
	if value == "" {
		return 0
	}
	parsed, _ := strconv.ParseInt(value, 10, 64)
	return parsed
}

func parseBoolQuery(r *http.Request, key string) bool {
	value := strings.ToLower(strings.TrimSpace(r.URL.Query().Get(key)))
	return value == "1" || value == "true" || value == "yes"
}

func writeTokenMutationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrTokenNotFound):
		writeError(w, http.StatusNotFound, ErrTypeNotFound, "Token not found", "token_not_found")
	case errors.Is(err, ErrTokenAlreadyExists):
		writeError(w, http.StatusConflict, ErrTypeInvalidRequest, "Token already exists", "token_exists")
	default:
		writeInvalidRequestError(w, err.Error())
	}
}

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(payload)
}
