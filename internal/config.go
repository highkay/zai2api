package internal

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/joho/godotenv"
)

type Config struct {
	// Server
	Port string

	// API Configuration
	APIEndpoint         string
	UpstreamProxy       string
	AuthTokens          []string // 支持多个 API Key（逗号分隔）
	BackupTokens        []string // 支持多个 Backup Token（用于多模态，逗号分隔）
	TokenDBPath         string
	TokenAPIAllowReveal bool
	RuntimeConfigPath   string

	// Feature Configuration
	DebugLogging  bool
	ToolSupport   bool
	RetryCount    int
	SkipAuthToken bool
	ScanLimit     int
	LogLevel      string

	// Display
	Note []string // 多行备注，在 / 显示
}

var Cfg *Config
var runtimeConfigMu sync.RWMutex

type runtimeConfigFile struct {
	UpstreamProxy *string `json:"upstream_proxy,omitempty"`
}

type RuntimeConfigSnapshot struct {
	APIEndpoint       string
	UpstreamProxy     string
	RuntimeConfigPath string
}

type RuntimeConfigUpdate struct {
	UpstreamProxy *string
}

func getEnvString(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

func getEnvBool(key string, defaultVal bool) bool {
	val := os.Getenv(key)
	if val == "" {
		return defaultVal
	}
	return val == "true" || val == "1" || val == "yes"
}

func getEnvInt(key string, defaultVal int) int {
	val := os.Getenv(key)
	if val == "" {
		return defaultVal
	}
	if i, err := strconv.Atoi(val); err == nil {
		return i
	}
	return defaultVal
}

// getEnvStringSlice 解析逗号分隔的字符串为切片
func getEnvStringSlice(key string) []string {
	val := os.Getenv(key)
	if val == "" {
		return nil
	}
	parts := strings.Split(val, ",")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// parseNoteLines 解析多行备注，支持 \n 换行和 | 分隔
func parseNoteLines(note string) []string {
	if note == "" {
		return nil
	}
	// 支持 \n 和 | 作为换行符
	note = strings.ReplaceAll(note, "\\n", "\n")
	note = strings.ReplaceAll(note, "|", "\n")
	lines := strings.Split(note, "\n")
	var result []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			result = append(result, line)
		}
	}
	return result
}

func LoadConfig() {
	godotenv.Load()

	Cfg = &Config{
		// Server
		Port: getEnvString("PORT", "8000"),

		// API Configuration
		APIEndpoint:         getEnvString("API_ENDPOINT", "https://chat.z.ai/api/v2/chat/completions"),
		UpstreamProxy:       getEnvString("UPSTREAM_PROXY", ""),
		AuthTokens:          getEnvStringSlice("AUTH_TOKEN"),
		BackupTokens:        getEnvStringSlice("BACKUP_TOKEN"),
		TokenDBPath:         getEnvString("TOKEN_DB_PATH", ""),
		TokenAPIAllowReveal: getEnvBool("TOKEN_API_ALLOW_REVEAL", false),
		RuntimeConfigPath:   getEnvString("RUNTIME_CONFIG_PATH", "data/runtime_config.json"),

		// Feature Configuration
		DebugLogging:  getEnvBool("DEBUG_LOGGING", false),
		ToolSupport:   getEnvBool("TOOL_SUPPORT", true),
		RetryCount:    getEnvInt("RETRY_COUNT", 5),
		SkipAuthToken: getEnvBool("SKIP_AUTH_TOKEN", false),
		ScanLimit:     getEnvInt("SCAN_LIMIT", 200000),
		LogLevel:      getEnvString("LOG_LEVEL", "info"),

		// Display
		Note: parseNoteLines(getEnvString("NOTE", "")),
	}
	if err := loadRuntimeConfigOverrides(); err != nil {
		fmt.Fprintf(os.Stderr, "load runtime config: %v\n", err)
	}
}

func loadRuntimeConfigOverrides() error {
	runtimeConfigMu.Lock()
	defer runtimeConfigMu.Unlock()
	return loadRuntimeConfigOverridesLocked()
}

func loadRuntimeConfigOverridesLocked() error {
	if Cfg == nil || strings.TrimSpace(Cfg.RuntimeConfigPath) == "" {
		return nil
	}
	data, err := os.ReadFile(Cfg.RuntimeConfigPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var file runtimeConfigFile
	if err := json.Unmarshal(data, &file); err != nil {
		return err
	}
	if file.UpstreamProxy != nil {
		Cfg.UpstreamProxy = strings.TrimSpace(*file.UpstreamProxy)
	}
	return nil
}

func GetAPIEndpoint() string {
	runtimeConfigMu.RLock()
	defer runtimeConfigMu.RUnlock()
	if Cfg == nil {
		return ""
	}
	return Cfg.APIEndpoint
}

func GetUpstreamProxy() string {
	runtimeConfigMu.RLock()
	defer runtimeConfigMu.RUnlock()
	if Cfg == nil {
		return ""
	}
	return Cfg.UpstreamProxy
}

func GetRuntimeConfigSnapshot() RuntimeConfigSnapshot {
	runtimeConfigMu.RLock()
	defer runtimeConfigMu.RUnlock()
	return runtimeConfigSnapshotLocked()
}

func UpdateRuntimeConfig(update RuntimeConfigUpdate) (RuntimeConfigSnapshot, error) {
	runtimeConfigMu.Lock()
	defer runtimeConfigMu.Unlock()
	if Cfg == nil {
		return RuntimeConfigSnapshot{}, fmt.Errorf("config is not initialized")
	}
	if update.UpstreamProxy != nil {
		proxy := strings.TrimSpace(*update.UpstreamProxy)
		if err := validateUpstreamProxy(proxy); err != nil {
			return RuntimeConfigSnapshot{}, err
		}
		Cfg.UpstreamProxy = proxy
	}
	if err := saveRuntimeConfigLocked(); err != nil {
		return RuntimeConfigSnapshot{}, err
	}
	return runtimeConfigSnapshotLocked(), nil
}

func runtimeConfigSnapshotLocked() RuntimeConfigSnapshot {
	if Cfg == nil {
		return RuntimeConfigSnapshot{}
	}
	return RuntimeConfigSnapshot{
		APIEndpoint:       Cfg.APIEndpoint,
		UpstreamProxy:     Cfg.UpstreamProxy,
		RuntimeConfigPath: Cfg.RuntimeConfigPath,
	}
}

func saveRuntimeConfigLocked() error {
	if Cfg == nil || strings.TrimSpace(Cfg.RuntimeConfigPath) == "" {
		return fmt.Errorf("runtime config path is not configured")
	}
	if err := os.MkdirAll(filepath.Dir(Cfg.RuntimeConfigPath), 0700); err != nil {
		return err
	}
	payload := runtimeConfigFile{UpstreamProxy: &Cfg.UpstreamProxy}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(Cfg.RuntimeConfigPath, data, 0600)
}

func ValidateAuthToken(token string) bool {
	if Cfg.SkipAuthToken {
		return true
	}
	if len(Cfg.AuthTokens) == 0 {
		LogWarn("AUTH_TOKEN not configured, rejecting all requests")
		return false
	}
	for _, t := range Cfg.AuthTokens {
		if t == token {
			return true
		}
	}
	return false
}

var backupTokenIndex int

func GetBackupToken() string {
	if len(Cfg.BackupTokens) == 0 {
		return ""
	}
	token := Cfg.BackupTokens[backupTokenIndex%len(Cfg.BackupTokens)]
	backupTokenIndex++
	return token
}
