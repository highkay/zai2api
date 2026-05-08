package internal

import (
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	// Server
	Port string

	// API Configuration
	APIEndpoint  string
	AuthTokens   []string // 支持多个 API Key（逗号分隔）
	BackupTokens []string // 支持多个 Backup Token（用于多模态，逗号分隔）

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
		APIEndpoint:  getEnvString("API_ENDPOINT", "https://chat.z.ai/api/v2/chat/completions"),
		AuthTokens:   getEnvStringSlice("AUTH_TOKEN"),
		BackupTokens: getEnvStringSlice("BACKUP_TOKEN"),

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
