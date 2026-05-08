package internal

import (
	"encoding/json"
	"errors"
	"net/http"
)

type tokenCreateRequest struct {
	Token  string   `json:"token"`
	Tokens []string `json:"tokens"`
}

type tokenUpdateRequest struct {
	OldToken string `json:"old_token"`
	NewToken string `json:"new_token"`
}

type tokenDeleteRequest struct {
	Token string `json:"token"`
}

type tokenListResponse struct {
	Object string      `json:"object"`
	Data   []TokenInfo `json:"data"`
	Count  int         `json:"count"`
}

type tokenResponse struct {
	Object  string    `json:"object"`
	Data    TokenInfo `json:"data"`
	Message string    `json:"message,omitempty"`
}

type tokenCreateResponse struct {
	Object  string      `json:"object"`
	Data    []TokenInfo `json:"data"`
	Count   int         `json:"count"`
	Message string      `json:"message,omitempty"`
	Skipped []string    `json:"skipped,omitempty"`
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
	token := r.URL.Query().Get("token")
	if token == "" {
		tokens := GetTokenManager().ListTokens()
		writeJSON(w, http.StatusOK, tokenListResponse{
			Object: "list",
			Data:   tokens,
			Count:  len(tokens),
		})
		return
	}

	info, exists := GetTokenManager().GetTokenInfo(token)
	if !exists {
		writeError(w, http.StatusNotFound, ErrTypeNotFound, "Token not found", "token_not_found")
		return
	}

	writeJSON(w, http.StatusOK, tokenResponse{
		Object: "token",
		Data:   info,
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

	writeJSON(w, status, tokenCreateResponse{
		Object:  "list",
		Data:    added,
		Count:   len(added),
		Message: "Tokens updated",
		Skipped: skipped,
	})
}

func handleTokenUpdate(w http.ResponseWriter, r *http.Request) {
	var req tokenUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeInvalidRequestError(w, "Invalid JSON body")
		return
	}

	info, err := GetTokenManager().UpdateToken(req.OldToken, req.NewToken)
	if err != nil {
		writeTokenMutationError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, tokenResponse{
		Object:  "token",
		Data:    info,
		Message: "Token updated",
	})
}

func handleTokenDelete(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		var req tokenDeleteRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			token = req.Token
		}
	}
	if token == "" {
		writeInvalidRequestError(w, "token is required")
		return
	}

	info, err := GetTokenManager().DeleteToken(token)
	if err != nil {
		writeTokenMutationError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, tokenResponse{
		Object:  "token",
		Data:    info,
		Message: "Token deleted",
	})
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
