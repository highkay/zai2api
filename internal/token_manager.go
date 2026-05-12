package internal

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
)

type TokenInfo struct {
	ID            int64     `json:"id,omitempty"`
	Token         string    `json:"token,omitempty"`
	TokenHash     string    `json:"token_hash,omitempty"`
	TokenPreview  string    `json:"token_preview,omitempty"`
	Source        string    `json:"source,omitempty"`
	Status        string    `json:"status,omitempty"`
	Email         string    `json:"email"`
	UserID        string    `json:"user_id"`
	Valid         bool      `json:"valid"`
	LastChecked   time.Time `json:"last_checked"`
	LastRefreshed time.Time `json:"last_refreshed"`
	InvalidatedAt time.Time `json:"invalidated_at,omitempty"`
	InvalidReason string    `json:"invalid_reason,omitempty"`
	ReplacedByID  int64     `json:"replaced_by_id,omitempty"`
	CreatedAt     time.Time `json:"created_at,omitempty"`
	UpdatedAt     time.Time `json:"updated_at,omitempty"`
	UseCount      int64     `json:"use_count"`
	source        tokenSource
}

type tokenSource string

const (
	tokenSourceFile   tokenSource = TokenSourceLegacyFile
	tokenSourceBackup tokenSource = TokenSourceEnvBackup
	tokenSourceAPI    tokenSource = TokenSourceAPI
	tokenRefreshURL               = "https://chat.z.ai/api/v1/auths/"
)

type authSessionResponse struct {
	Token string `json:"token"`
	Email string `json:"email"`
	ID    string `json:"id"`
}

type tokenCheckResult struct {
	oldToken          string
	newToken          string
	email             string
	userID            string
	checkedAt         time.Time
	valid             bool
	definitiveInvalid bool
	transientErr      error
	statusCode        int
}

type TokenRefreshOutcome struct {
	Token     string
	Valid     bool
	Refreshed bool
}

type TokenManager struct {
	mu              sync.RWMutex
	tokens          map[string]*TokenInfo
	validTokens     []string
	currentIndex    int
	dataDir         string
	dbPath          string
	store           TokenStore
	checkInterval   time.Duration
	stopChan        chan struct{}
	stopOnce        sync.Once
	multimodalCount int64
	totalCalls      int64
	successCalls    int64
	totalTokenCount int
	invalidCount    int
	disabledCount   int
	rotatedCount    int
}

var (
	tokenManager *TokenManager
	tokenOnce    sync.Once
)

var (
	ErrNoUpstreamToken    = errors.New("no upstream token available")
	ErrTokenNotFound      = errors.New("token not found")
	ErrTokenAlreadyExists = errors.New("token already exists")
)

var fetchAuthSessionFunc = fetchAuthSession

func NewTokenManager(dataDir string) *TokenManager {
	if dataDir == "" {
		dataDir = "data"
	}
	return &TokenManager{
		tokens:        make(map[string]*TokenInfo),
		validTokens:   make([]string, 0),
		dataDir:       dataDir,
		dbPath:        filepath.Join(dataDir, "tokens.db"),
		checkInterval: 5 * time.Minute,
		stopChan:      make(chan struct{}),
	}
}

func GetTokenManager() *TokenManager {
	tokenOnce.Do(func() {
		tokenManager = NewTokenManager("data")
	})
	return tokenManager
}

func normalizeTokenValue(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "token=") {
		raw = strings.TrimSpace(strings.TrimPrefix(raw, "token="))
	}
	return raw
}

func normalizeTokenInputs(rawTokens []string) []string {
	var tokens []string
	seen := make(map[string]bool)
	for _, raw := range rawTokens {
		token := normalizeTokenValue(raw)
		if token == "" || seen[token] {
			continue
		}
		seen[token] = true
		tokens = append(tokens, token)
	}
	return tokens
}

func cloneTokenInfo(info *TokenInfo) TokenInfo {
	if info == nil {
		return TokenInfo{}
	}
	return *info
}

func (tm *TokenManager) tokenFilePath() string {
	return filepath.Join(tm.dataDir, "tokens.txt")
}

func (tm *TokenManager) invalidTokenFilePath() string {
	return filepath.Join(tm.dataDir, "tokens_invalid.txt")
}

func (tm *TokenManager) tokenDBPath() string {
	if Cfg != nil && Cfg.TokenDBPath != "" {
		return Cfg.TokenDBPath
	}
	if tm.dbPath != "" {
		return tm.dbPath
	}
	return filepath.Join(tm.dataDir, "tokens.db")
}

func (tm *TokenManager) ensureStore() error {
	if tm.store != nil {
		return nil
	}
	store := NewSQLiteTokenStore(tm.tokenDBPath())
	if err := store.Init(); err != nil {
		return err
	}
	tm.store = store
	return nil
}

func (tm *TokenManager) readTokenEntriesFromFile() ([]string, error) {
	return readTokenEntries(tm.tokenFilePath())
}

func (tm *TokenManager) readInvalidTokenEntriesFromFile() ([]string, error) {
	return readTokenEntries(tm.invalidTokenFilePath())
}

func readTokenEntries(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}
	defer file.Close()

	var tokens []string
	seen := make(map[string]bool)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		raw := strings.TrimSpace(scanner.Text())
		token := normalizeTokenValue(raw)
		if token == "" || seen[token] || strings.HasPrefix(raw, "#") {
			continue
		}
		seen[token] = true
		tokens = append(tokens, token)
	}
	return tokens, scanner.Err()
}

func (tm *TokenManager) writeTokenEntries(tokens []string) error {
	if err := os.MkdirAll(tm.dataDir, 0700); err != nil {
		return err
	}

	content := "# Legacy token file. zai2api now stores tokens in data/tokens.db.\n"
	content += "# This file is imported only once when the SQLite store is initialized.\n"
	content += fmt.Sprintf("# Updated: %s\n\n", time.Now().Format("2006-01-02 15:04:05"))
	content += strings.Join(tokens, "\n")
	if len(tokens) > 0 {
		content += "\n"
	}
	return os.WriteFile(tm.tokenFilePath(), []byte(content), 0600)
}

func fetchAuthSession(token string) (authSessionResponse, int, error) {
	req, err := fhttp.NewRequest(http.MethodGet, tokenRefreshURL, nil)
	if err != nil {
		return authSessionResponse{}, 0, err
	}

	ApplyBrowserFingerprintHeaders(req.Header)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "zh-CN")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("DNT", "1")
	req.Header.Set("Pragma", "no-cache")
	req.Header.Set("Referer", "https://chat.z.ai/")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("sec-gpc", "1")
	req.AddCookie(&fhttp.Cookie{Name: "token", Value: token})

	client, err := TLSHTTPClient(10 * time.Second)
	if err != nil {
		return authSessionResponse{}, 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return authSessionResponse{}, 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return authSessionResponse{}, resp.StatusCode, err
	}
	if resp.StatusCode != http.StatusOK {
		return authSessionResponse{}, resp.StatusCode, nil
	}

	var authResp authSessionResponse
	if err := json.Unmarshal(body, &authResp); err != nil {
		return authSessionResponse{}, resp.StatusCode, err
	}
	return authResp, resp.StatusCode, nil
}

func isDefinitiveAuthFailure(statusCode int) bool {
	return statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden
}

func (tm *TokenManager) Start() error {
	if err := os.MkdirAll(tm.dataDir, 0700); err != nil {
		return fmt.Errorf("创建 data 目录失败: %v", err)
	}
	if err := tm.loadTokens(); err != nil {
		LogWarn("初始加载 token 失败: %v", err)
	}

	go tm.startValidator()

	LogInfo("TokenManager 已启动，当前有效 token 数: %d", tm.ValidTokenCount())
	return nil
}

func (tm *TokenManager) Stop() {
	tm.stopOnce.Do(func() {
		close(tm.stopChan)
		if tm.store != nil {
			_ = tm.store.Close()
		}
	})
}

func (tm *TokenManager) loadTokens() error {
	if err := os.MkdirAll(tm.dataDir, 0700); err != nil {
		return err
	}
	if err := tm.ensureStore(); err != nil {
		return err
	}

	legacyActive, err := tm.readTokenEntriesFromFile()
	if err != nil {
		return err
	}
	legacyInvalid, err := tm.readInvalidTokenEntriesFromFile()
	if err != nil {
		return err
	}
	importSummary, err := tm.store.ImportLegacyFilesOnce(legacyActive, legacyInvalid)
	if err != nil {
		return err
	}
	if importSummary.LegacyActive > 0 || importSummary.LegacyInvalid > 0 {
		LogInfo("已导入 legacy token: active=%d invalid=%d", importSummary.LegacyActive, importSummary.LegacyInvalid)
	}

	var backupTokens []string
	if Cfg != nil {
		backupTokens = normalizeTokenInputs(Cfg.BackupTokens)
	}
	backupImported, err := tm.store.SyncBackupTokens(backupTokens)
	if err != nil {
		return err
	}
	if backupImported > 0 {
		LogInfo("已导入 BACKUP_TOKEN 到 SQLite 管理副本: %d", backupImported)
	}

	return tm.reloadFromStore()
}

func (tm *TokenManager) createExampleTokenFile(path string) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		LogWarn("创建示例 token 文件目录失败: %v", err)
		return
	}
	content := `# Legacy token file
# zai2api now uses data/tokens.db as the token source of truth.
# Existing tokens.txt content is imported only once when the SQLite store is initialized.
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		LogWarn("创建示例 token 文件失败: %v", err)
		return
	}
	LogInfo("已创建 legacy token 文件: %s", path)
}

func (tm *TokenManager) reloadFromStore() error {
	if tm.store == nil {
		return nil
	}
	records, err := tm.store.ListTokens(TokenListOptions{Status: "all", IncludeToken: true})
	if err != nil {
		return err
	}

	tm.mu.Lock()
	defer tm.mu.Unlock()

	tm.tokens = make(map[string]*TokenInfo)
	tm.validTokens = make([]string, 0, len(records))
	tm.totalTokenCount = len(records)
	tm.invalidCount = 0
	tm.disabledCount = 0
	tm.rotatedCount = 0

	for _, record := range records {
		info := record
		info.Valid = info.Status == TokenStatusActive
		info.source = tokenSource(info.Source)
		switch info.Status {
		case TokenStatusActive:
			tm.tokens[info.Token] = &info
			tm.validTokens = append(tm.validTokens, info.Token)
		case TokenStatusInvalid:
			tm.invalidCount++
		case TokenStatusDisabled:
			tm.disabledCount++
		case TokenStatusRotated:
			tm.rotatedCount++
		}
	}
	if tm.currentIndex >= len(tm.validTokens) && len(tm.validTokens) > 0 {
		tm.currentIndex = tm.currentIndex % len(tm.validTokens)
	}
	LogInfo("已加载 SQLite token: active=%d total=%d invalid=%d disabled=%d rotated=%d",
		len(tm.validTokens), tm.totalTokenCount, tm.invalidCount, tm.disabledCount, tm.rotatedCount)
	return nil
}

func (tm *TokenManager) startValidator() {
	time.Sleep(10 * time.Second)
	tm.validateAllTokens()

	ticker := time.NewTicker(tm.checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			tm.validateAllTokens()
		case <-tm.stopChan:
			return
		}
	}
}

func (tm *TokenManager) validateAllTokens() {
	tokens := tm.tokensNeedingRefresh()
	if len(tokens) == 0 {
		return
	}

	LogInfo("开始刷新 %d 个 token...", len(tokens))
	results := make([]tokenCheckResult, 0, len(tokens))
	for _, token := range tokens {
		results = append(results, tm.checkToken(token))
		time.Sleep(500 * time.Millisecond)
	}

	refreshedCount, invalidCount := tm.applyTokenCheckResults(results)
	LogInfo("Token 刷新完成，轮换 %d 个，失效 %d 个，剩余有效 %d 个", refreshedCount, invalidCount, tm.ValidTokenCount())
}

func (tm *TokenManager) tokensNeedingRefresh() []string {
	now := time.Now()
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	tokens := make([]string, 0, len(tm.validTokens))
	for _, token := range tm.validTokens {
		if info, exists := tm.tokens[token]; exists && tm.shouldRefreshLocked(info, now) {
			tokens = append(tokens, token)
		}
	}
	return tokens
}

func (tm *TokenManager) shouldRefreshLocked(info *TokenInfo, now time.Time) bool {
	if info == nil || !info.Valid {
		return false
	}
	lastAttempt := info.LastChecked
	if info.LastRefreshed.After(lastAttempt) {
		lastAttempt = info.LastRefreshed
	}
	return lastAttempt.IsZero() || now.Sub(lastAttempt) >= tm.checkInterval
}

func (tm *TokenManager) checkToken(token string) tokenCheckResult {
	result := tokenCheckResult{
		oldToken:  token,
		newToken:  token,
		checkedAt: time.Now(),
	}

	authResp, statusCode, err := fetchAuthSessionFunc(token)
	result.statusCode = statusCode
	if err != nil {
		result.transientErr = err
		return result
	}
	if statusCode == http.StatusOK {
		if authResp.Token != "" {
			result.newToken = authResp.Token
		}
		result.email = authResp.Email
		result.userID = authResp.ID
		result.valid = true
		return result
	}
	if isDefinitiveAuthFailure(statusCode) {
		result.definitiveInvalid = true
		return result
	}

	result.transientErr = fmt.Errorf("refresh status %d", statusCode)
	return result
}

func (tm *TokenManager) applyTokenCheckResults(results []tokenCheckResult) (refreshedCount, invalidCount int) {
	if tm.store == nil {
		return 0, 0
	}
	for _, result := range results {
		info, exists := tm.GetTokenInfo(result.oldToken)
		if !exists {
			continue
		}

		if result.definitiveInvalid {
			if info.Valid {
				invalidCount++
			}
			reason := fmt.Sprintf("refresh status %d", result.statusCode)
			if _, err := tm.store.MarkTokenInvalid(result.oldToken, reason, result.checkedAt); err != nil {
				LogWarn("标记 token 失效失败: %v", err)
			}
			continue
		}
		if result.transientErr != nil {
			LogWarn("Token 刷新暂时失败: status=%d err=%v", result.statusCode, result.transientErr)
			if _, err := tm.store.MarkTokenChecked(result.oldToken, "", "", result.checkedAt, false); err != nil {
				LogWarn("更新 token 检查时间失败: %v", err)
			}
			continue
		}
		if !result.valid {
			continue
		}

		if result.newToken != "" && result.newToken != result.oldToken {
			source := info.Source
			if source == "" {
				source = string(info.source)
			}
			if _, _, err := tm.store.ReplaceToken(result.oldToken, result.newToken, result.email, result.userID, result.checkedAt, source); err != nil {
				LogWarn("轮换 token 持久化失败: %v", err)
				continue
			}
			refreshedCount++
			continue
		}
		if _, err := tm.store.MarkTokenChecked(result.oldToken, result.email, result.userID, result.checkedAt, true); err != nil {
			LogWarn("更新 token 刷新状态失败: %v", err)
		}
	}
	if err := tm.reloadFromStore(); err != nil {
		LogWarn("重新加载 token store 失败: %v", err)
	}
	return refreshedCount, invalidCount
}

func (tm *TokenManager) ListTokens() []TokenInfo {
	return tm.ListTokenRecords(TokenListOptions{Status: TokenStatusActive, IncludeToken: true})
}

func (tm *TokenManager) ListTokenRecords(options TokenListOptions) []TokenInfo {
	if tm.store != nil {
		records, err := tm.store.ListTokens(options)
		if err != nil {
			LogWarn("列出 token 失败: %v", err)
			return []TokenInfo{}
		}
		return records
	}

	tm.mu.RLock()
	defer tm.mu.RUnlock()
	result := make([]TokenInfo, 0, len(tm.validTokens))
	for _, token := range tm.validTokens {
		info, exists := tm.tokens[token]
		if !exists {
			continue
		}
		result = append(result, cloneTokenInfo(info))
	}
	return result
}

func (tm *TokenManager) GetTokenInfo(token string) (TokenInfo, bool) {
	token = normalizeTokenValue(token)
	if token == "" {
		return TokenInfo{}, false
	}

	if tm.store != nil {
		info, exists, err := tm.store.GetToken(token)
		if err != nil {
			LogWarn("查询 token 失败: %v", err)
			return TokenInfo{}, false
		}
		return info, exists
	}

	tm.mu.RLock()
	defer tm.mu.RUnlock()

	info, exists := tm.tokens[token]
	if !exists {
		return TokenInfo{}, false
	}
	return cloneTokenInfo(info), true
}

func (tm *TokenManager) GetTokenInfoByID(id int64) (TokenInfo, bool) {
	if id <= 0 {
		return TokenInfo{}, false
	}
	if tm.store != nil {
		info, exists, err := tm.store.GetTokenByID(id)
		if err != nil {
			LogWarn("按 ID 查询 token 失败: %v", err)
			return TokenInfo{}, false
		}
		return info, exists
	}
	return TokenInfo{}, false
}

func (tm *TokenManager) AddTokens(rawTokens []string) ([]TokenInfo, []string, error) {
	if err := tm.ensureStore(); err != nil {
		return nil, nil, err
	}
	added, skipped, err := tm.store.CreateTokens(rawTokens, TokenSourceAPI)
	if err != nil {
		return nil, nil, err
	}
	if err := tm.reloadFromStore(); err != nil {
		return nil, nil, err
	}
	return added, skipped, nil
}

func (tm *TokenManager) UpdateToken(oldToken, newToken string) (TokenInfo, error) {
	if err := tm.ensureStore(); err != nil {
		return TokenInfo{}, err
	}
	info, _, err := tm.store.ReplaceToken(oldToken, newToken, "", "", time.Now(), TokenSourceAPI)
	if err != nil {
		return TokenInfo{}, err
	}
	if err := tm.reloadFromStore(); err != nil {
		return TokenInfo{}, err
	}
	return info, nil
}

func (tm *TokenManager) DeleteToken(token string) (TokenInfo, error) {
	return tm.DeleteTokenWithMode(token, false)
}

func (tm *TokenManager) DeleteTokenWithMode(token string, hard bool) (TokenInfo, error) {
	if err := tm.ensureStore(); err != nil {
		return TokenInfo{}, err
	}
	info, err := tm.store.DeleteToken(token, hard)
	if err != nil {
		return TokenInfo{}, err
	}
	if err := tm.reloadFromStore(); err != nil {
		return TokenInfo{}, err
	}
	return info, nil
}

func (tm *TokenManager) DeleteTokenByID(id int64, hard bool) (TokenInfo, error) {
	if err := tm.ensureStore(); err != nil {
		return TokenInfo{}, err
	}
	info, err := tm.store.DeleteTokenByID(id, hard)
	if err != nil {
		return TokenInfo{}, err
	}
	if err := tm.reloadFromStore(); err != nil {
		return TokenInfo{}, err
	}
	return info, nil
}

func (tm *TokenManager) SetTokenStatus(token, status, reason string) (TokenInfo, error) {
	if err := tm.ensureStore(); err != nil {
		return TokenInfo{}, err
	}
	info, err := tm.store.SetTokenStatus(token, status, reason)
	if err != nil {
		return TokenInfo{}, err
	}
	if err := tm.reloadFromStore(); err != nil {
		return TokenInfo{}, err
	}
	return info, nil
}

func (tm *TokenManager) SetTokenStatusByID(id int64, status, reason string) (TokenInfo, error) {
	if err := tm.ensureStore(); err != nil {
		return TokenInfo{}, err
	}
	info, err := tm.store.SetTokenStatusByID(id, status, reason)
	if err != nil {
		return TokenInfo{}, err
	}
	if err := tm.reloadFromStore(); err != nil {
		return TokenInfo{}, err
	}
	return info, nil
}

func GetUpstreamToken() string {
	tm := GetTokenManager()
	attempts := tm.ValidTokenCount()
	for i := 0; i < attempts; i++ {
		token := tm.GetToken()
		if token == "" {
			break
		}
		outcome := tm.RefreshToken(token, false)
		switch {
		case outcome.Valid && outcome.Token != "":
			return outcome.Token
		case outcome.Valid:
			return token
		}
	}
	return ""
}

func GetFreshUpstreamToken() string {
	tm := GetTokenManager()
	attempts := tm.ValidTokenCount()
	for i := 0; i < attempts; i++ {
		token := tm.GetToken()
		if token == "" {
			break
		}
		outcome := tm.RefreshToken(token, true)
		switch {
		case outcome.Valid && outcome.Token != "":
			return outcome.Token
		case outcome.Valid:
			return token
		}
	}
	return ""
}

func GetUpstreamTokenForModelAPI() (string, error) {
	token := GetUpstreamToken()
	if token == "" {
		return "", ErrNoUpstreamToken
	}
	return token, nil
}

func (tm *TokenManager) HasValidUpstreamTokens() bool {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	return len(tm.validTokens) > 0
}

func (tm *TokenManager) ValidTokenCount() int {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	return len(tm.validTokens)
}

func (tm *TokenManager) GetToken() string {
	tm.mu.Lock()
	if len(tm.validTokens) == 0 {
		tm.mu.Unlock()
		return ""
	}

	token := tm.validTokens[tm.currentIndex%len(tm.validTokens)]
	tm.currentIndex++
	if info, exists := tm.tokens[token]; exists {
		info.UseCount++
	}
	store := tm.store
	tm.mu.Unlock()

	if store != nil {
		if err := store.IncrementUse(token); err != nil {
			LogWarn("更新 token 使用计数失败: %v", err)
		}
	}
	return token
}

func (tm *TokenManager) GetAlternativeToken(exclude string) string {
	tm.mu.Lock()
	if len(tm.validTokens) == 0 {
		tm.mu.Unlock()
		return ""
	}
	var token string
	for i := 0; i < len(tm.validTokens); i++ {
		candidate := tm.validTokens[(tm.currentIndex+i)%len(tm.validTokens)]
		if candidate == "" || candidate == exclude {
			continue
		}
		token = candidate
		tm.currentIndex = (tm.currentIndex + i + 1) % len(tm.validTokens)
		if info, exists := tm.tokens[token]; exists {
			info.UseCount++
		}
		break
	}
	store := tm.store
	tm.mu.Unlock()

	if token != "" && store != nil {
		if err := store.IncrementUse(token); err != nil {
			LogWarn("更新备用 token 使用计数失败: %v", err)
		}
	}
	return token
}

func (tm *TokenManager) RefreshToken(token string, force bool) TokenRefreshOutcome {
	token = normalizeTokenValue(token)
	if token == "" {
		return TokenRefreshOutcome{}
	}

	if !force {
		tm.mu.RLock()
		info := tm.tokens[token]
		shouldRefresh := tm.shouldRefreshLocked(info, time.Now())
		valid := info != nil && info.Valid
		tm.mu.RUnlock()
		if !valid {
			return TokenRefreshOutcome{Token: token, Valid: false}
		}
		if !shouldRefresh {
			return TokenRefreshOutcome{Token: token, Valid: true}
		}
	}

	result := tm.checkToken(token)
	refreshedCount, invalidCount := tm.applyTokenCheckResults([]tokenCheckResult{result})
	if invalidCount > 0 {
		return TokenRefreshOutcome{Token: token, Valid: false}
	}

	currentToken := result.newToken
	if currentToken == "" {
		currentToken = token
	}
	info, exists := tm.GetTokenInfo(currentToken)
	if !exists && currentToken != token {
		info, exists = tm.GetTokenInfo(token)
	}
	if !exists {
		return TokenRefreshOutcome{Token: currentToken, Valid: invalidCount == 0}
	}
	return TokenRefreshOutcome{
		Token:     info.Token,
		Valid:     info.Valid,
		Refreshed: refreshedCount > 0,
	}
}

func (tm *TokenManager) RecordCall(success bool, isMultimodal bool) {
	atomic.AddInt64(&tm.totalCalls, 1)
	if success {
		atomic.AddInt64(&tm.successCalls, 1)
	}
	if isMultimodal {
		atomic.AddInt64(&tm.multimodalCount, 1)
	}
}

func (tm *TokenManager) GetStats() TokenManagerStats {
	tm.mu.RLock()
	validCount := len(tm.validTokens)
	totalTokenCount := tm.totalTokenCount
	invalidCount := tm.invalidCount
	disabledCount := tm.disabledCount
	rotatedCount := tm.rotatedCount
	if totalTokenCount == 0 && len(tm.tokens) > 0 {
		totalTokenCount = len(tm.tokens)
	}
	tm.mu.RUnlock()

	total := atomic.LoadInt64(&tm.totalCalls)
	success := atomic.LoadInt64(&tm.successCalls)
	multimodal := atomic.LoadInt64(&tm.multimodalCount)

	var successRate float64
	if total > 0 {
		successRate = float64(success) / float64(total) * 100
	}
	failed := total - success
	if failed < 0 {
		failed = 0
	}

	return TokenManagerStats{
		ValidTokenCount:    validCount,
		TotalTokenCount:    totalTokenCount,
		InvalidTokenCount:  invalidCount,
		DisabledTokenCount: disabledCount,
		RotatedTokenCount:  rotatedCount,
		MultimodalCount:    multimodal,
		TotalCalls:         total,
		SuccessCalls:       success,
		FailedCalls:        failed,
		SuccessRate:        successRate,
	}
}

type TokenManagerStats struct {
	ValidTokenCount    int     `json:"valid_token_count"`
	TotalTokenCount    int     `json:"total_token_count"`
	InvalidTokenCount  int     `json:"invalid_token_count"`
	DisabledTokenCount int     `json:"disabled_token_count"`
	RotatedTokenCount  int     `json:"rotated_token_count"`
	MultimodalCount    int64   `json:"multimodal_count"`
	TotalCalls         int64   `json:"total_calls"`
	SuccessCalls       int64   `json:"success_calls"`
	FailedCalls        int64   `json:"failed_calls"`
	SuccessRate        float64 `json:"success_rate"`
}

func GetClientIP(r *http.Request) string {
	xff := r.Header.Get("X-Forwarded-For")
	if xff != "" {
		ips := strings.Split(xff, ",")
		if len(ips) > 0 {
			ip := strings.TrimSpace(ips[0])
			if ip != "" {
				return ip
			}
		}
	}

	xri := r.Header.Get("X-Real-IP")
	if xri != "" {
		return xri
	}

	ip := r.RemoteAddr
	if idx := strings.LastIndex(ip, ":"); idx != -1 {
		ip = ip[:idx]
	}
	return ip
}
