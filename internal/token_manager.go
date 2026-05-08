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
	Token       string    `json:"token"`
	Email       string    `json:"email"`
	UserID      string    `json:"user_id"`
	Valid       bool      `json:"valid"`
	LastChecked time.Time `json:"last_checked"`
	UseCount    int64     `json:"use_count"`
}

// TokenManager 管理所有用户 token
type TokenManager struct {
	mu              sync.RWMutex
	tokens          map[string]*TokenInfo // token -> TokenInfo
	validTokens     []string              // 有效 token 列表
	fileTokens      []string              // tokens.txt 中的 token 顺序
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

func NewTokenManager(dataDir string) *TokenManager {
	if dataDir == "" {
		dataDir = "data"
	}
	return &TokenManager{
		tokens:        make(map[string]*TokenInfo),
		validTokens:   make([]string, 0),
		fileTokens:    make([]string, 0),
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
		Token:       info.Token,
		Email:       info.Email,
		UserID:      info.UserID,
		Valid:       info.Valid,
		LastChecked: info.LastChecked,
		UseCount:    info.UseCount,
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

	file, err := os.Open(tokenFile)
	if err != nil {
		if os.IsNotExist(err) {
			tm.createExampleTokenFile(tokenFile)
			tm.mu.Lock()
			tm.tokens = make(map[string]*TokenInfo)
			tm.validTokens = make([]string, 0)
			tm.fileTokens = make([]string, 0)
			tm.mu.Unlock()
			return nil
		}
		return err
	}
	defer file.Close()

	tm.mu.Lock()
	defer tm.mu.Unlock()

	// 保留旧的统计数据
	oldTokens := tm.tokens
	tm.tokens = make(map[string]*TokenInfo)
	tm.validTokens = make([]string, 0)
	tm.fileTokens = make([]string, 0)

	seen := make(map[string]bool)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		trimmedLine := strings.TrimSpace(line)
		if trimmedLine == "" || strings.HasPrefix(trimmedLine, "#") {
			continue
		}

		token := normalizeTokenValue(line)
		if token == "" {
			continue
		}
		if seen[token] {
			continue
		}
		seen[token] = true
		tm.fileTokens = append(tm.fileTokens, token)

		// 复用旧的 TokenInfo 如果存在
		if oldInfo, exists := oldTokens[token]; exists {
			tm.tokens[token] = oldInfo
			if oldInfo.Valid {
				tm.validTokens = append(tm.validTokens, token)
			}
		} else {
			// 新 token，解析并标记为待验证
			info := &TokenInfo{
				Token: token,
				Valid: true, // 初始假设有效，验证时会更新
			}
			// 尝试解析 JWT 获取信息
			if payload, err := DecodeJWTPayload(token); err == nil && payload != nil {
				info.Email = payload.Email
				info.UserID = payload.ID
			}
			tm.tokens[token] = info
			tm.validTokens = append(tm.validTokens, token)
		}
	}

	validN := len(tm.validTokens)
	LogInfo("已加载 %d 个 token", validN)
	scanErr := scanner.Err()
	return scanErr
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
	tm.mu.RLock()
	tokens := make([]string, 0, len(tm.tokens))
	for token := range tm.tokens {
		tokens = append(tokens, token)
	}
	tm.mu.RUnlock()

	LogInfo("开始验证 %d 个 token...", len(tokens))
	invalidCount := 0

	for _, token := range tokens {
		valid := tm.validateToken(token)
		tm.mu.Lock()
		if info, exists := tm.tokens[token]; exists {
			info.Valid = valid
			info.LastChecked = time.Now()
			if !valid {
				invalidCount++
			}
		}
		tm.mu.Unlock()
		time.Sleep(500 * time.Millisecond) // 避免请求过快
	}

	// 更新有效 token 列表
	tm.rebuildValidTokens()
	LogInfo("Token 验证完成，失效 %d 个，剩余有效 %d 个", invalidCount, len(tm.validTokens))

	// 自动删除失效 token
	if invalidCount > 0 {
		tm.removeInvalidTokens()
	}
}

// validateToken 验证单个 token
func (tm *TokenManager) validateToken(token string) bool {
	req, err := fhttp.NewRequest("GET", "https://chat.z.ai/api/v1/auths/", nil)
	if err != nil {
		return false
	}

	ApplyBrowserFingerprintHeaders(req.Header)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "zh-CN")
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
		LogDebug("Token 验证 tls client: %v", err)
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		LogDebug("Token 验证请求失败: %v", err)
		return false
	}
	defer resp.Body.Close()

	// 读取响应
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		LogDebug("Token 验证失败，状态码: %d", resp.StatusCode)
		return false
	}

	// 尝试解析响应获取新 token
	var authResp struct {
		Token string `json:"token"`
		Email string `json:"email"`
		ID    string `json:"id"`
	}
	if err := json.Unmarshal(body, &authResp); err == nil && authResp.Token != "" {
		// 更新 token 信息
		tm.mu.Lock()
		if info, exists := tm.tokens[token]; exists {
			if authResp.Email != "" {
				info.Email = authResp.Email
			}
			if authResp.ID != "" {
				info.UserID = authResp.ID
			}
		}
		tm.mu.Unlock()
	}

	return true
}

// rebuildValidTokens 重建有效 token 列表
func (tm *TokenManager) rebuildValidTokens() {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	tm.validTokens = make([]string, 0)
	for _, token := range tm.fileTokens {
		info, exists := tm.tokens[token]
		if !exists {
			continue
		}
		if info.Valid {
			tm.validTokens = append(tm.validTokens, token)
		}
	}
}

// removeInvalidTokens 从文件中移除失效 token
func (tm *TokenManager) removeInvalidTokens() {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	invalidFile := tm.invalidTokenFilePath()

	// 收集失效 token
	var invalidTokens []string
	for token, info := range tm.tokens {
		if !info.Valid {
			invalidTokens = append(invalidTokens, token)
			delete(tm.tokens, token)
		}
	}

	if len(invalidTokens) == 0 {
		return
	}

	// 追加到失效文件
	f, err := os.OpenFile(invalidFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err == nil {
		defer f.Close()
		timestamp := time.Now().Format("2006-01-02 15:04:05")
		for _, token := range invalidTokens {
			f.WriteString(fmt.Sprintf("# 失效于 %s\n%s\n", timestamp, token))
		}
	}

	// 重写有效 token 文件
	var validTokenLines []string
	for _, token := range tm.validTokens {
		validTokenLines = append(validTokenLines, token)
	}

	tm.fileTokens = append([]string(nil), validTokenLines...)
	if err := tm.writeTokenEntries(validTokenLines); err != nil {
		LogError("重写 token 文件失败: %v", err)
	}
	LogInfo("已移除 %d 个失效 token 到 %s", len(invalidTokens), invalidFile)
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
	if token := GetTokenManager().GetToken(); token != "" {
		return token
	}
	return GetBackupToken()
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
