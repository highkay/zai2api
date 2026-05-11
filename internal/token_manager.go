package internal

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"

	fhttp "github.com/bogdanfinn/fhttp"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
)

// TokenInfo 存储单个 token 的信息
type TokenInfo struct {
	Token         string    `json:"token"`
	Email         string    `json:"email"`
	UserID        string    `json:"user_id"`
	Valid         bool      `json:"valid"`
	LastChecked   time.Time `json:"last_checked"`
	LastRefreshed time.Time `json:"last_refreshed"`
	UseCount      int64     `json:"use_count"`
	source        tokenSource
}

type tokenSource string

const (
	tokenSourceFile   tokenSource = "file"
	tokenSourceBackup tokenSource = "backup"
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

// TokenManager 管理所有用户 token
type TokenManager struct {
	mu              sync.RWMutex
	tokens          map[string]*TokenInfo // token -> TokenInfo
	validTokens     []string              // 有效 token 列表
	fileTokens      []string              // tokens.txt 中的 token 顺序
	backupTokens    []string              // BACKUP_TOKEN 中的 token 顺序
	currentIndex    int                   // 轮询索引
	dataDir         string
	watcher         *fsnotify.Watcher
	checkInterval   time.Duration
	stopChan        chan struct{}
	multimodalCount int64 // 多模态请求计数
	totalCalls      int64 // 累计调用次数
	successCalls    int64 // 成功调用次数
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
		fileTokens:    make([]string, 0),
		backupTokens:  make([]string, 0),
		dataDir:       dataDir,
		checkInterval: 5 * time.Minute,
		stopChan:      make(chan struct{}),
	}
}

// GetTokenManager 获取单例 TokenManager
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

func cloneTokenInfo(info *TokenInfo) TokenInfo {
	if info == nil {
		return TokenInfo{}
	}
	return TokenInfo{
		Token:         info.Token,
		Email:         info.Email,
		UserID:        info.UserID,
		Valid:         info.Valid,
		LastChecked:   info.LastChecked,
		LastRefreshed: info.LastRefreshed,
		UseCount:      info.UseCount,
	}
}

func (tm *TokenManager) tokenFilePath() string {
	return filepath.Join(tm.dataDir, "tokens.txt")
}

func (tm *TokenManager) invalidTokenFilePath() string {
	return filepath.Join(tm.dataDir, "tokens_invalid.txt")
}

func (tm *TokenManager) readTokenEntriesFromFile() ([]string, error) {
	file, err := os.Open(tm.tokenFilePath())
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
		token := normalizeTokenValue(scanner.Text())
		if token == "" || seen[token] || strings.HasPrefix(strings.TrimSpace(scanner.Text()), "#") {
			continue
		}
		seen[token] = true
		tokens = append(tokens, token)
	}
	return tokens, scanner.Err()
}

func (tm *TokenManager) writeTokenEntries(tokens []string) error {
	if err := os.MkdirAll(tm.dataDir, 0755); err != nil {
		return err
	}

	content := "# 用户 Token 文件（自动更新）\n"
	content += "# 兼容历史格式：读取时支持 token=...、空行和注释行\n"
	content += fmt.Sprintf("# 更新时间: %s\n\n", time.Now().Format("2006-01-02 15:04:05"))
	content += strings.Join(tokens, "\n")
	if len(tokens) > 0 {
		content += "\n"
	}
	return os.WriteFile(tm.tokenFilePath(), []byte(content), 0644)
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

// Start 启动 token 管理器
func (tm *TokenManager) Start() error {
	// 确保 data 目录存在
	if err := os.MkdirAll(tm.dataDir, 0755); err != nil {
		return fmt.Errorf("创建 data 目录失败: %v", err)
	}

	// 初始加载 token
	if err := tm.loadTokens(); err != nil {
		LogWarn("初始加载 token 失败: %v", err)
	}

	// 启动文件监听
	if err := tm.startWatcher(); err != nil {
		LogWarn("启动文件监听失败: %v", err)
	}

	// 启动定期验证
	go tm.startValidator()

	LogInfo("TokenManager 已启动，当前有效 token 数: %d", len(tm.validTokens))
	return nil
}

// Stop 停止 token 管理器
func (tm *TokenManager) Stop() {
	close(tm.stopChan)
	if tm.watcher != nil {
		tm.watcher.Close()
	}
}

// loadTokens 从 data 目录加载所有 token
func (tm *TokenManager) loadTokens() error {
	tokenFile := tm.tokenFilePath()
	if _, err := os.Stat(tokenFile); os.IsNotExist(err) {
		tm.createExampleTokenFile(tokenFile)
	}

	fileTokens, err := tm.readTokenEntriesFromFile()
	if err != nil {
		return err
	}

	tm.mu.Lock()
	defer tm.mu.Unlock()

	oldTokens := tm.tokens
	tm.tokens = make(map[string]*TokenInfo)
	tm.validTokens = make([]string, 0)
	tm.fileTokens = make([]string, 0)
	tm.backupTokens = make([]string, 0)

	seen := make(map[string]bool)
	for _, token := range fileTokens {
		tm.registerLoadedTokenLocked(token, tokenSourceFile, oldTokens, seen)
	}
	var backupTokens []string
	if Cfg != nil {
		backupTokens = normalizeTokenInputs(Cfg.BackupTokens)
	}
	for _, token := range backupTokens {
		tm.registerLoadedTokenLocked(token, tokenSourceBackup, oldTokens, seen)
	}

	tm.rebuildValidTokensLocked()
	LogInfo("已加载 %d 个 token", len(tm.validTokens))
	return nil
}

// createExampleTokenFile 创建示例 token 文件
func (tm *TokenManager) createExampleTokenFile(path string) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		LogWarn("创建示例 token 文件目录失败: %v", err)
		return
	}
	content := `# 用户 Token 文件
# 每行一个 token，支持以下格式：
# 1. 直接写 token
# 2. token=xxx 格式
# 以 # 开头的行为注释

# 示例:
# eyJhbGciOiJFUzI1NiIsInR5cCI6IkpXVCJ9.xxxxx
`
	os.WriteFile(path, []byte(content), 0644)
	LogInfo("已创建示例 token 文件: %s", path)
}

func (tm *TokenManager) registerLoadedTokenLocked(token string, source tokenSource, oldTokens map[string]*TokenInfo, seen map[string]bool) {
	token = normalizeTokenValue(token)
	if token == "" || seen[token] {
		return
	}
	seen[token] = true

	switch source {
	case tokenSourceFile:
		tm.fileTokens = append(tm.fileTokens, token)
	case tokenSourceBackup:
		tm.backupTokens = append(tm.backupTokens, token)
	}

	if oldInfo, exists := oldTokens[token]; exists {
		oldInfo.source = source
		tm.tokens[token] = oldInfo
		return
	}

	info := &TokenInfo{
		Token:  token,
		Valid:  true,
		source: source,
	}
	if payload, err := DecodeJWTPayload(token); err == nil && payload != nil {
		info.Email = payload.Email
		info.UserID = payload.ID
	}
	tm.tokens[token] = info
}

// startWatcher 启动文件变化监听
func (tm *TokenManager) startWatcher() error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	tm.watcher = watcher

	go func() {
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if event.Op&(fsnotify.Write|fsnotify.Create) != 0 {
					if strings.HasSuffix(event.Name, "tokens.txt") {
						LogInfo("检测到 token 文件变化，重新加载...")
						time.Sleep(100 * time.Millisecond) // 等待文件写入完成
						tm.loadTokens()
					}
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				LogError("文件监听错误: %v", err)
			case <-tm.stopChan:
				return
			}
		}
	}()

	return watcher.Add(tm.dataDir)
}

// startValidator 启动定期验证
func (tm *TokenManager) startValidator() {
	// 首次延迟验证
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

// validateAllTokens 验证所有 token
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
	LogInfo("Token 刷新完成，轮换 %d 个，失效 %d 个，剩余有效 %d 个", refreshedCount, invalidCount, len(tm.validTokens))
}

func (tm *TokenManager) tokensNeedingRefresh() []string {
	now := time.Now()
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	tokens := make([]string, 0, len(tm.fileTokens)+len(tm.backupTokens))
	for _, token := range tm.fileTokens {
		if info, exists := tm.tokens[token]; exists && tm.shouldRefreshLocked(info, now) {
			tokens = append(tokens, token)
		}
	}
	for _, token := range tm.backupTokens {
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

func replaceTokenInSlice(tokens []string, oldToken, newToken string) []string {
	if oldToken == "" || newToken == "" {
		return append([]string(nil), tokens...)
	}
	result := make([]string, 0, len(tokens))
	seen := make(map[string]bool, len(tokens))
	for _, token := range tokens {
		if token == oldToken {
			token = newToken
		}
		if token == "" || seen[token] {
			continue
		}
		seen[token] = true
		result = append(result, token)
	}
	return result
}

func (tm *TokenManager) ensureUniqueTokenOrdersLocked() {
	normalizedFile := make([]string, 0, len(tm.fileTokens))
	fileSeen := make(map[string]bool, len(tm.fileTokens))
	for _, token := range tm.fileTokens {
		if token == "" || fileSeen[token] {
			continue
		}
		fileSeen[token] = true
		normalizedFile = append(normalizedFile, token)
	}
	tm.fileTokens = normalizedFile

	normalizedBackup := make([]string, 0, len(tm.backupTokens))
	backupSeen := make(map[string]bool, len(tm.backupTokens))
	for _, token := range tm.backupTokens {
		if token == "" || fileSeen[token] || backupSeen[token] {
			continue
		}
		backupSeen[token] = true
		normalizedBackup = append(normalizedBackup, token)
	}
	tm.backupTokens = normalizedBackup
}

func chooseLaterTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func mergeTokenInfo(dst, src *TokenInfo) {
	if dst == nil || src == nil {
		return
	}
	if src.Email != "" {
		dst.Email = src.Email
	}
	if src.UserID != "" {
		dst.UserID = src.UserID
	}
	dst.Valid = dst.Valid || src.Valid
	dst.UseCount += src.UseCount
	dst.LastChecked = chooseLaterTime(dst.LastChecked, src.LastChecked)
	dst.LastRefreshed = chooseLaterTime(dst.LastRefreshed, src.LastRefreshed)
	if src.source == tokenSourceFile {
		dst.source = tokenSourceFile
	}
}

func (tm *TokenManager) replaceTokenLocked(oldToken, newToken string) *TokenInfo {
	info, exists := tm.tokens[oldToken]
	if !exists {
		return nil
	}
	if newToken == "" || newToken == oldToken {
		info.Token = oldToken
		return info
	}

	if existing, exists := tm.tokens[newToken]; exists {
		mergeTokenInfo(existing, info)
		delete(tm.tokens, oldToken)
		tm.fileTokens = replaceTokenInSlice(tm.fileTokens, oldToken, newToken)
		tm.backupTokens = replaceTokenInSlice(tm.backupTokens, oldToken, newToken)
		tm.ensureUniqueTokenOrdersLocked()
		return existing
	}

	delete(tm.tokens, oldToken)
	info.Token = newToken
	tm.tokens[newToken] = info
	tm.fileTokens = replaceTokenInSlice(tm.fileTokens, oldToken, newToken)
	tm.backupTokens = replaceTokenInSlice(tm.backupTokens, oldToken, newToken)
	tm.ensureUniqueTokenOrdersLocked()
	return info
}

func (tm *TokenManager) rebuildValidTokensLocked() {
	tm.validTokens = make([]string, 0, len(tm.fileTokens)+len(tm.backupTokens))
	for _, token := range tm.fileTokens {
		info, exists := tm.tokens[token]
		if exists && info.Valid {
			tm.validTokens = append(tm.validTokens, token)
		}
	}
	for _, token := range tm.backupTokens {
		info, exists := tm.tokens[token]
		if exists && info.Valid {
			tm.validTokens = append(tm.validTokens, token)
		}
	}
}

// rebuildValidTokens 重建有效 token 列表
func (tm *TokenManager) rebuildValidTokens() {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.rebuildValidTokensLocked()
}

// removeInvalidTokens 从文件中移除失效 token
func (tm *TokenManager) removeInvalidTokens() {
	tm.mu.Lock()
	invalidFileTokens := make([]string, 0)

	newFileTokens := make([]string, 0, len(tm.fileTokens))
	for _, token := range tm.fileTokens {
		info, exists := tm.tokens[token]
		if exists && info.Valid {
			newFileTokens = append(newFileTokens, token)
			continue
		}
		invalidFileTokens = append(invalidFileTokens, token)
		delete(tm.tokens, token)
	}
	tm.fileTokens = newFileTokens

	newBackupTokens := make([]string, 0, len(tm.backupTokens))
	for _, token := range tm.backupTokens {
		info, exists := tm.tokens[token]
		if exists && info.Valid {
			newBackupTokens = append(newBackupTokens, token)
			continue
		}
		delete(tm.tokens, token)
	}
	tm.backupTokens = newBackupTokens
	tm.rebuildValidTokensLocked()
	fileSnapshot := append([]string(nil), tm.fileTokens...)
	tm.mu.Unlock()

	if len(invalidFileTokens) == 0 {
		if err := tm.writeTokenEntries(fileSnapshot); err != nil {
			LogError("重写 token 文件失败: %v", err)
		}
		return
	}

	invalidFile := tm.invalidTokenFilePath()
	f, err := os.OpenFile(invalidFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err == nil {
		defer f.Close()
		timestamp := time.Now().Format("2006-01-02 15:04:05")
		for _, token := range invalidFileTokens {
			f.WriteString(fmt.Sprintf("# 失效于 %s\n%s\n", timestamp, token))
		}
	}
	if err := tm.writeTokenEntries(fileSnapshot); err != nil {
		LogError("重写 token 文件失败: %v", err)
	}
	LogInfo("已移除 %d 个失效 token 到 %s", len(invalidFileTokens), invalidFile)
}

func (tm *TokenManager) applyTokenCheckResults(results []tokenCheckResult) (refreshedCount, invalidCount int) {
	tm.mu.Lock()
	for _, result := range results {
		info, exists := tm.tokens[result.oldToken]
		if !exists {
			continue
		}

		info.LastChecked = result.checkedAt

		if result.definitiveInvalid {
			if info.Valid {
				invalidCount++
			}
			info.Valid = false
			continue
		}
		if result.transientErr != nil {
			LogWarn("Token 刷新暂时失败: status=%d err=%v", result.statusCode, result.transientErr)
			continue
		}
		if !result.valid {
			continue
		}

		info.Valid = true
		info.LastRefreshed = result.checkedAt
		if result.email != "" {
			info.Email = result.email
		}
		if result.userID != "" {
			info.UserID = result.userID
		}

		if result.newToken != "" && result.newToken != result.oldToken {
			info = tm.replaceTokenLocked(result.oldToken, result.newToken)
			refreshedCount++
		}
		if info != nil {
			info.Token = result.newToken
		}
	}
	tm.rebuildValidTokensLocked()
	tm.mu.Unlock()

	if refreshedCount > 0 || invalidCount > 0 {
		tm.removeInvalidTokens()
		return refreshedCount, invalidCount
	}
	return refreshedCount, invalidCount
}

func (tm *TokenManager) ListTokens() []TokenInfo {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	result := make([]TokenInfo, 0, len(tm.fileTokens))
	for _, token := range tm.fileTokens {
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

	tm.mu.RLock()
	defer tm.mu.RUnlock()

	info, exists := tm.tokens[token]
	if !exists {
		return TokenInfo{}, false
	}
	return cloneTokenInfo(info), true
}

func (tm *TokenManager) AddTokens(rawTokens []string) ([]TokenInfo, []string, error) {
	requestedTokens := normalizeTokenInputs(rawTokens)
	if len(requestedTokens) == 0 {
		return nil, nil, fmt.Errorf("token is required")
	}

	tm.mu.Lock()
	fileTokens, err := tm.readTokenEntriesFromFile()
	if err != nil {
		tm.mu.Unlock()
		return nil, nil, err
	}

	existing := make(map[string]bool, len(fileTokens))
	for _, token := range fileTokens {
		existing[token] = true
	}

	var addedTokens []string
	var skippedTokens []string
	for _, token := range requestedTokens {
		if existing[token] {
			skippedTokens = append(skippedTokens, token)
			continue
		}
		existing[token] = true
		fileTokens = append(fileTokens, token)
		addedTokens = append(addedTokens, token)
	}

	if len(addedTokens) > 0 {
		err = tm.writeTokenEntries(fileTokens)
	}
	tm.mu.Unlock()
	if err != nil {
		return nil, nil, err
	}
	if len(addedTokens) == 0 {
		return []TokenInfo{}, skippedTokens, nil
	}
	if err := tm.loadTokens(); err != nil {
		return nil, nil, err
	}

	result := make([]TokenInfo, 0, len(addedTokens))
	for _, token := range addedTokens {
		if info, exists := tm.GetTokenInfo(token); exists {
			result = append(result, info)
		}
	}
	return result, skippedTokens, nil
}

func (tm *TokenManager) UpdateToken(oldToken, newToken string) (TokenInfo, error) {
	oldToken = normalizeTokenValue(oldToken)
	newToken = normalizeTokenValue(newToken)
	if oldToken == "" || newToken == "" {
		return TokenInfo{}, fmt.Errorf("old_token and new_token are required")
	}

	tm.mu.Lock()
	fileTokens, err := tm.readTokenEntriesFromFile()
	if err != nil {
		tm.mu.Unlock()
		return TokenInfo{}, err
	}

	index := -1
	for i, token := range fileTokens {
		if token == oldToken {
			index = i
			break
		}
	}
	if index == -1 {
		tm.mu.Unlock()
		return TokenInfo{}, ErrTokenNotFound
	}
	if oldToken != newToken {
		for _, token := range fileTokens {
			if token == newToken {
				tm.mu.Unlock()
				return TokenInfo{}, ErrTokenAlreadyExists
			}
		}
		fileTokens[index] = newToken
		if err := tm.writeTokenEntries(fileTokens); err != nil {
			tm.mu.Unlock()
			return TokenInfo{}, err
		}
	}
	tm.mu.Unlock()

	if err := tm.loadTokens(); err != nil {
		return TokenInfo{}, err
	}
	info, exists := tm.GetTokenInfo(newToken)
	if !exists {
		return TokenInfo{}, ErrTokenNotFound
	}
	return info, nil
}

func (tm *TokenManager) DeleteToken(token string) (TokenInfo, error) {
	token = normalizeTokenValue(token)
	if token == "" {
		return TokenInfo{}, fmt.Errorf("token is required")
	}

	deletedInfo, exists := tm.GetTokenInfo(token)
	if !exists {
		deletedInfo = TokenInfo{Token: token}
	}

	tm.mu.Lock()
	fileTokens, err := tm.readTokenEntriesFromFile()
	if err != nil {
		tm.mu.Unlock()
		return TokenInfo{}, err
	}

	index := -1
	for i, item := range fileTokens {
		if item == token {
			index = i
			break
		}
	}
	if index == -1 {
		tm.mu.Unlock()
		return TokenInfo{}, ErrTokenNotFound
	}

	fileTokens = append(fileTokens[:index], fileTokens[index+1:]...)
	if err := tm.writeTokenEntries(fileTokens); err != nil {
		tm.mu.Unlock()
		return TokenInfo{}, err
	}
	tm.mu.Unlock()

	if err := tm.loadTokens(); err != nil {
		return TokenInfo{}, err
	}
	return deletedInfo, nil
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

func GetUpstreamTokenForModelAPI() (string, error) {
	token := GetUpstreamToken()
	if token == "" {
		return "", ErrNoUpstreamToken
	}
	return token, nil
}

// HasValidUpstreamTokens 是否存在可用的 z.ai 上游 token（TokenManager 轮询用）
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

// GetToken 获取一个有效 token（轮询）
func (tm *TokenManager) GetToken() string {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if len(tm.validTokens) == 0 {
		return ""
	}

	token := tm.validTokens[tm.currentIndex%len(tm.validTokens)]
	tm.currentIndex++

	// 增加使用计数
	if info, exists := tm.tokens[token]; exists {
		info.UseCount++
	}

	return token
}

func (tm *TokenManager) GetAlternativeToken(exclude string) string {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if len(tm.validTokens) == 0 {
		return ""
	}
	for i := 0; i < len(tm.validTokens); i++ {
		token := tm.validTokens[(tm.currentIndex+i)%len(tm.validTokens)]
		if token == "" || token == exclude {
			continue
		}
		tm.currentIndex = (tm.currentIndex + i + 1) % len(tm.validTokens)
		if info, exists := tm.tokens[token]; exists {
			info.UseCount++
		}
		return token
	}
	return ""
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
	tm.mu.RLock()
	info, exists := tm.tokens[currentToken]
	if !exists && currentToken != token {
		info, exists = tm.tokens[token]
	}
	tm.mu.RUnlock()
	if !exists {
		return TokenRefreshOutcome{Token: currentToken, Valid: invalidCount == 0}
	}
	return TokenRefreshOutcome{
		Token:     info.Token,
		Valid:     info.Valid,
		Refreshed: refreshedCount > 0,
	}
}

// RecordCall 记录调用
func (tm *TokenManager) RecordCall(success bool, isMultimodal bool) {
	atomic.AddInt64(&tm.totalCalls, 1)
	if success {
		atomic.AddInt64(&tm.successCalls, 1)
	}
	if isMultimodal {
		atomic.AddInt64(&tm.multimodalCount, 1)
	}
}

// GetStats 获取统计数据
func (tm *TokenManager) GetStats() TokenManagerStats {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	total := atomic.LoadInt64(&tm.totalCalls)
	success := atomic.LoadInt64(&tm.successCalls)
	multimodal := atomic.LoadInt64(&tm.multimodalCount)

	var successRate float64
	if total > 0 {
		successRate = float64(success) / float64(total) * 100
	}

	return TokenManagerStats{
		ValidTokenCount: len(tm.validTokens),
		TotalTokenCount: len(tm.tokens),
		MultimodalCount: multimodal,
		TotalCalls:      total,
		SuccessCalls:    success,
		SuccessRate:     successRate,
	}
}

// TokenManagerStats token 管理器统计数据
type TokenManagerStats struct {
	ValidTokenCount int     `json:"valid_token_count"`
	TotalTokenCount int     `json:"total_token_count"`
	MultimodalCount int64   `json:"multimodal_count"`
	TotalCalls      int64   `json:"total_calls"`
	SuccessCalls    int64   `json:"success_calls"`
	SuccessRate     float64 `json:"success_rate"`
}

// GetClientIP 从请求中获取客户端 IP
func GetClientIP(r *http.Request) string {
	// 优先从 X-Forwarded-For 获取
	xff := r.Header.Get("X-Forwarded-For")
	if xff != "" {
		// X-Forwarded-For 可能包含多个 IP，取第一个
		ips := strings.Split(xff, ",")
		if len(ips) > 0 {
			ip := strings.TrimSpace(ips[0])
			if ip != "" {
				return ip
			}
		}
	}

	// 尝试 X-Real-IP
	xri := r.Header.Get("X-Real-IP")
	if xri != "" {
		return xri
	}

	// 最后使用 RemoteAddr
	ip := r.RemoteAddr
	// 去除端口
	if idx := strings.LastIndex(ip, ":"); idx != -1 {
		ip = ip[:idx]
	}
	return ip
}
