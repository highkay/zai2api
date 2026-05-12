package internal

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	TokenStatusActive   = "active"
	TokenStatusInvalid  = "invalid"
	TokenStatusDisabled = "disabled"
	TokenStatusRotated  = "rotated"

	TokenSourceLegacyFile = "legacy_file"
	TokenSourceAPI        = "api"
	TokenSourceEnvBackup  = "env_backup"
)

const (
	tokenStoreMetaLegacyImportDone = "legacy_import_completed"
)

type TokenListOptions struct {
	Status       string
	Source       string
	IncludeToken bool
}

type TokenStoreStats struct {
	Total    int
	Active   int
	Invalid  int
	Disabled int
	Rotated  int
}

type TokenImportSummary struct {
	LegacyActive  int `json:"legacy_active"`
	LegacyInvalid int `json:"legacy_invalid"`
	Backup        int `json:"backup"`
}

type TokenStore interface {
	Init() error
	Close() error
	ImportLegacyFilesOnce(activeTokens, invalidTokens []string) (TokenImportSummary, error)
	SyncBackupTokens(tokens []string) (int, error)
	ListTokens(options TokenListOptions) ([]TokenInfo, error)
	GetToken(token string) (TokenInfo, bool, error)
	GetTokenByID(id int64) (TokenInfo, bool, error)
	CreateTokens(tokens []string, source string) ([]TokenInfo, []string, error)
	ReplaceToken(oldToken, newToken, email, userID string, checkedAt time.Time, source string) (TokenInfo, bool, error)
	MarkTokenChecked(token, email, userID string, checkedAt time.Time, valid bool) (TokenInfo, error)
	MarkTokenInvalid(token, reason string, checkedAt time.Time) (TokenInfo, error)
	SetTokenStatus(token, status, reason string) (TokenInfo, error)
	SetTokenStatusByID(id int64, status, reason string) (TokenInfo, error)
	DeleteToken(token string, hard bool) (TokenInfo, error)
	DeleteTokenByID(id int64, hard bool) (TokenInfo, error)
	IncrementUse(token string) error
}

type SQLiteTokenStore struct {
	path string
	db   *sql.DB
}

func NewSQLiteTokenStore(path string) *SQLiteTokenStore {
	return &SQLiteTokenStore{path: path}
}

func (s *SQLiteTokenStore) Init() error {
	if s.path == "" {
		return fmt.Errorf("token db path is required")
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}

	db, err := sql.Open("sqlite", s.path)
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	s.db = db
	if err := s.configure(); err != nil {
		db.Close()
		s.db = nil
		return err
	}
	if err := s.migrate(); err != nil {
		db.Close()
		s.db = nil
		return err
	}
	_ = os.Chmod(s.path, 0600)
	return nil
}

func (s *SQLiteTokenStore) configure() error {
	stmts := []string{
		`PRAGMA busy_timeout = 5000`,
		`PRAGMA foreign_keys = ON`,
		`PRAGMA journal_mode = WAL`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLiteTokenStore) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *SQLiteTokenStore) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS token_store_metadata (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS tokens (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			token TEXT NOT NULL,
			token_hash TEXT NOT NULL UNIQUE,
			token_preview TEXT NOT NULL,
			source TEXT NOT NULL,
			status TEXT NOT NULL,
			email TEXT NOT NULL DEFAULT '',
			user_id TEXT NOT NULL DEFAULT '',
			use_count INTEGER NOT NULL DEFAULT 0,
			last_checked_at TEXT NOT NULL DEFAULT '',
			last_refreshed_at TEXT NOT NULL DEFAULT '',
			invalidated_at TEXT NOT NULL DEFAULT '',
			invalid_reason TEXT NOT NULL DEFAULT '',
			replaced_by_id INTEGER,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			FOREIGN KEY(replaced_by_id) REFERENCES tokens(id) ON DELETE SET NULL
		)`,
		`CREATE TABLE IF NOT EXISTS token_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			token_id INTEGER,
			event_type TEXT NOT NULL,
			status_from TEXT NOT NULL DEFAULT '',
			status_to TEXT NOT NULL DEFAULT '',
			detail TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			FOREIGN KEY(token_id) REFERENCES tokens(id) ON DELETE SET NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_tokens_status_source ON tokens(status, source)`,
		`CREATE INDEX IF NOT EXISTS idx_tokens_email ON tokens(email)`,
		`CREATE INDEX IF NOT EXISTS idx_token_events_token_id ON token_events(token_id)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLiteTokenStore) ImportLegacyFilesOnce(activeTokens, invalidTokens []string) (TokenImportSummary, error) {
	var summary TokenImportSummary
	done, err := s.metadataExists(tokenStoreMetaLegacyImportDone)
	if err != nil {
		return summary, err
	}
	if done {
		return summary, nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return summary, err
	}
	defer tx.Rollback()

	now := time.Now()
	for _, token := range normalizeTokenInputs(activeTokens) {
		inserted, err := insertTokenTx(tx, token, TokenSourceLegacyFile, TokenStatusActive, "", "", "", "", "", now)
		if err != nil {
			return summary, err
		}
		if inserted {
			summary.LegacyActive++
		}
	}
	for _, token := range normalizeTokenInputs(invalidTokens) {
		inserted, err := insertTokenTx(tx, token, TokenSourceLegacyFile, TokenStatusInvalid, "", "", "", formatTimeForStore(now), "legacy invalid token file", now)
		if err != nil {
			return summary, err
		}
		if inserted {
			summary.LegacyInvalid++
		}
	}

	if err := setMetadataTx(tx, tokenStoreMetaLegacyImportDone, "true", now); err != nil {
		return summary, err
	}
	return summary, tx.Commit()
}

func (s *SQLiteTokenStore) SyncBackupTokens(tokens []string) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	count := 0
	now := time.Now()
	for _, token := range normalizeTokenInputs(tokens) {
		inserted, err := insertTokenTx(tx, token, TokenSourceEnvBackup, TokenStatusActive, "", "", "", "", "", now)
		if err != nil {
			return 0, err
		}
		if inserted {
			count++
		}
	}
	return count, tx.Commit()
}

func (s *SQLiteTokenStore) metadataExists(key string) (bool, error) {
	var value string
	err := s.db.QueryRow(`SELECT value FROM token_store_metadata WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func setMetadataTx(tx *sql.Tx, key, value string, now time.Time) error {
	_, err := tx.Exec(
		`INSERT INTO token_store_metadata(key, value, updated_at)
		 VALUES(?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, formatTimeForStore(now),
	)
	return err
}

func (s *SQLiteTokenStore) ListTokens(options TokenListOptions) ([]TokenInfo, error) {
	status := normalizeTokenStatusFilter(options.Status)
	source := strings.TrimSpace(options.Source)
	query := `SELECT id, token, token_hash, token_preview, source, status, email, user_id, use_count,
		last_checked_at, last_refreshed_at, invalidated_at, invalid_reason, replaced_by_id, created_at, updated_at
		FROM tokens`
	var where []string
	var args []any
	if status != "" {
		where = append(where, "status = ?")
		args = append(args, status)
	}
	if source != "" {
		where = append(where, "source = ?")
		args = append(args, source)
	}
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY id ASC"

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []TokenInfo
	for rows.Next() {
		info, err := scanTokenInfo(rows)
		if err != nil {
			return nil, err
		}
		if !options.IncludeToken {
			info.Token = ""
		}
		result = append(result, info)
	}
	return result, rows.Err()
}

func (s *SQLiteTokenStore) GetToken(token string) (TokenInfo, bool, error) {
	token = normalizeTokenValue(token)
	if token == "" {
		return TokenInfo{}, false, nil
	}
	row := s.db.QueryRow(`SELECT id, token, token_hash, token_preview, source, status, email, user_id, use_count,
		last_checked_at, last_refreshed_at, invalidated_at, invalid_reason, replaced_by_id, created_at, updated_at
		FROM tokens WHERE token_hash = ?`, tokenHash(token))
	info, err := scanTokenInfo(row)
	if errors.Is(err, sql.ErrNoRows) {
		return TokenInfo{}, false, nil
	}
	if err != nil {
		return TokenInfo{}, false, err
	}
	return info, true, nil
}

func (s *SQLiteTokenStore) GetTokenByID(id int64) (TokenInfo, bool, error) {
	if id <= 0 {
		return TokenInfo{}, false, nil
	}
	row := s.db.QueryRow(`SELECT id, token, token_hash, token_preview, source, status, email, user_id, use_count,
		last_checked_at, last_refreshed_at, invalidated_at, invalid_reason, replaced_by_id, created_at, updated_at
		FROM tokens WHERE id = ?`, id)
	info, err := scanTokenInfo(row)
	if errors.Is(err, sql.ErrNoRows) {
		return TokenInfo{}, false, nil
	}
	if err != nil {
		return TokenInfo{}, false, err
	}
	return info, true, nil
}

func (s *SQLiteTokenStore) CreateTokens(tokens []string, source string) ([]TokenInfo, []string, error) {
	requested := normalizeTokenInputs(tokens)
	if len(requested) == 0 {
		return nil, nil, fmt.Errorf("token is required")
	}
	source = normalizeTokenSource(source)

	tx, err := s.db.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()

	now := time.Now()
	var added []TokenInfo
	var skipped []string
	for _, token := range requested {
		inserted, err := insertTokenTx(tx, token, source, TokenStatusActive, "", "", "", "", "", now)
		if err != nil {
			return nil, nil, err
		}
		if !inserted {
			skipped = append(skipped, token)
			continue
		}
		info, err := getTokenTx(tx, token)
		if err != nil {
			return nil, nil, err
		}
		added = append(added, info)
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return added, skipped, nil
}

func (s *SQLiteTokenStore) ReplaceToken(oldToken, newToken, email, userID string, checkedAt time.Time, source string) (TokenInfo, bool, error) {
	oldToken = normalizeTokenValue(oldToken)
	newToken = normalizeTokenValue(newToken)
	if oldToken == "" || newToken == "" {
		return TokenInfo{}, false, fmt.Errorf("old_token and new_token are required")
	}
	if checkedAt.IsZero() {
		checkedAt = time.Now()
	}

	tx, err := s.db.Begin()
	if err != nil {
		return TokenInfo{}, false, err
	}
	defer tx.Rollback()

	oldInfo, err := getTokenTx(tx, oldToken)
	if errors.Is(err, sql.ErrNoRows) {
		return TokenInfo{}, false, ErrTokenNotFound
	}
	if err != nil {
		return TokenInfo{}, false, err
	}
	if oldToken == newToken {
		info, err := markTokenCheckedTx(tx, oldToken, email, userID, checkedAt, true, true)
		if err != nil {
			return TokenInfo{}, false, err
		}
		return info, false, tx.Commit()
	}

	now := time.Now()
	newInfo, inserted, err := upsertReplacementTokenTx(tx, oldInfo, newToken, email, userID, checkedAt, normalizeTokenSource(source), now)
	if err != nil {
		return TokenInfo{}, false, err
	}
	_, err = tx.Exec(
		`UPDATE tokens
		 SET status = ?, last_checked_at = ?, last_refreshed_at = ?, replaced_by_id = ?, updated_at = ?
		 WHERE id = ?`,
		TokenStatusRotated, formatTimeForStore(checkedAt), formatTimeForStore(checkedAt), newInfo.ID, formatTimeForStore(now), oldInfo.ID,
	)
	if err != nil {
		return TokenInfo{}, false, err
	}
	if err := addTokenEventTx(tx, oldInfo.ID, "rotated", oldInfo.Status, TokenStatusRotated, tokenPreview(newToken), now); err != nil {
		return TokenInfo{}, false, err
	}
	return newInfo, inserted, tx.Commit()
}

func (s *SQLiteTokenStore) MarkTokenChecked(token, email, userID string, checkedAt time.Time, valid bool) (TokenInfo, error) {
	token = normalizeTokenValue(token)
	if token == "" {
		return TokenInfo{}, fmt.Errorf("token is required")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return TokenInfo{}, err
	}
	defer tx.Rollback()

	info, err := markTokenCheckedTx(tx, token, email, userID, checkedAt, valid, valid)
	if err != nil {
		return TokenInfo{}, err
	}
	return info, tx.Commit()
}

func (s *SQLiteTokenStore) MarkTokenInvalid(token, reason string, checkedAt time.Time) (TokenInfo, error) {
	token = normalizeTokenValue(token)
	if token == "" {
		return TokenInfo{}, fmt.Errorf("token is required")
	}
	if checkedAt.IsZero() {
		checkedAt = time.Now()
	}
	if strings.TrimSpace(reason) == "" {
		reason = "upstream auth rejected token"
	}
	tx, err := s.db.Begin()
	if err != nil {
		return TokenInfo{}, err
	}
	defer tx.Rollback()

	info, err := getTokenTx(tx, token)
	if errors.Is(err, sql.ErrNoRows) {
		return TokenInfo{}, ErrTokenNotFound
	}
	if err != nil {
		return TokenInfo{}, err
	}
	now := time.Now()
	_, err = tx.Exec(
		`UPDATE tokens
		 SET status = ?, last_checked_at = ?, invalidated_at = ?, invalid_reason = ?, updated_at = ?
		 WHERE id = ?`,
		TokenStatusInvalid, formatTimeForStore(checkedAt), formatTimeForStore(checkedAt), reason, formatTimeForStore(now), info.ID,
	)
	if err != nil {
		return TokenInfo{}, err
	}
	if err := addTokenEventTx(tx, info.ID, "invalidated", info.Status, TokenStatusInvalid, reason, now); err != nil {
		return TokenInfo{}, err
	}
	updated, err := getTokenTx(tx, token)
	if err != nil {
		return TokenInfo{}, err
	}
	return updated, tx.Commit()
}

func (s *SQLiteTokenStore) SetTokenStatus(token, status, reason string) (TokenInfo, error) {
	token = normalizeTokenValue(token)
	if token == "" {
		return TokenInfo{}, fmt.Errorf("token is required")
	}
	info, exists, err := s.GetToken(token)
	if err != nil {
		return TokenInfo{}, err
	}
	if !exists {
		return TokenInfo{}, ErrTokenNotFound
	}
	return s.setTokenStatusByInfo(info, status, reason)
}

func (s *SQLiteTokenStore) SetTokenStatusByID(id int64, status, reason string) (TokenInfo, error) {
	info, exists, err := s.GetTokenByID(id)
	if err != nil {
		return TokenInfo{}, err
	}
	if !exists {
		return TokenInfo{}, ErrTokenNotFound
	}
	return s.setTokenStatusByInfo(info, status, reason)
}

func (s *SQLiteTokenStore) setTokenStatusByInfo(info TokenInfo, status, reason string) (TokenInfo, error) {
	status = normalizeTokenStatus(status)
	if status == "" {
		return TokenInfo{}, fmt.Errorf("status is required")
	}
	if info.Status == TokenStatusRotated && status == TokenStatusActive {
		return TokenInfo{}, fmt.Errorf("rotated token cannot be reactivated")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return TokenInfo{}, err
	}
	defer tx.Rollback()

	now := time.Now()
	invalidatedAt := info.InvalidatedAt
	invalidReason := info.InvalidReason
	if status == TokenStatusInvalid {
		invalidatedAt = now
		if reason == "" {
			reason = "manually marked invalid"
		}
		invalidReason = reason
	}
	if status == TokenStatusActive {
		invalidatedAt = time.Time{}
		invalidReason = ""
	}
	_, err = tx.Exec(
		`UPDATE tokens
		 SET status = ?, invalidated_at = ?, invalid_reason = ?, updated_at = ?
		 WHERE id = ?`,
		status, formatTimeForStore(invalidatedAt), invalidReason, formatTimeForStore(now), info.ID,
	)
	if err != nil {
		return TokenInfo{}, err
	}
	if err := addTokenEventTx(tx, info.ID, "status_changed", info.Status, status, reason, now); err != nil {
		return TokenInfo{}, err
	}
	updated, err := getTokenByIDTx(tx, info.ID)
	if err != nil {
		return TokenInfo{}, err
	}
	return updated, tx.Commit()
}

func (s *SQLiteTokenStore) DeleteToken(token string, hard bool) (TokenInfo, error) {
	token = normalizeTokenValue(token)
	if token == "" {
		return TokenInfo{}, fmt.Errorf("token is required")
	}
	info, exists, err := s.GetToken(token)
	if err != nil {
		return TokenInfo{}, err
	}
	if !exists {
		return TokenInfo{}, ErrTokenNotFound
	}
	return s.deleteTokenByInfo(info, hard)
}

func (s *SQLiteTokenStore) DeleteTokenByID(id int64, hard bool) (TokenInfo, error) {
	info, exists, err := s.GetTokenByID(id)
	if err != nil {
		return TokenInfo{}, err
	}
	if !exists {
		return TokenInfo{}, ErrTokenNotFound
	}
	return s.deleteTokenByInfo(info, hard)
}

func (s *SQLiteTokenStore) deleteTokenByInfo(info TokenInfo, hard bool) (TokenInfo, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return TokenInfo{}, err
	}
	defer tx.Rollback()

	now := time.Now()
	if hard {
		if err := addTokenEventTx(tx, info.ID, "hard_deleted", info.Status, "", "hard delete requested", now); err != nil {
			return TokenInfo{}, err
		}
		if _, err := tx.Exec(`DELETE FROM tokens WHERE id = ?`, info.ID); err != nil {
			return TokenInfo{}, err
		}
		return info, tx.Commit()
	}
	_, err = tx.Exec(`UPDATE tokens SET status = ?, updated_at = ? WHERE id = ?`, TokenStatusDisabled, formatTimeForStore(now), info.ID)
	if err != nil {
		return TokenInfo{}, err
	}
	if err := addTokenEventTx(tx, info.ID, "disabled", info.Status, TokenStatusDisabled, "soft delete requested", now); err != nil {
		return TokenInfo{}, err
	}
	updated, err := getTokenByIDTx(tx, info.ID)
	if err != nil {
		return TokenInfo{}, err
	}
	return updated, tx.Commit()
}

func (s *SQLiteTokenStore) IncrementUse(token string) error {
	token = normalizeTokenValue(token)
	if token == "" {
		return nil
	}
	_, err := s.db.Exec(`UPDATE tokens SET use_count = use_count + 1, updated_at = ? WHERE token_hash = ?`, formatTimeForStore(time.Now()), tokenHash(token))
	return err
}

func insertTokenTx(tx *sql.Tx, token, source, status, email, userID, lastChecked, invalidatedAt, invalidReason string, now time.Time) (bool, error) {
	token = normalizeTokenValue(token)
	if token == "" {
		return false, nil
	}
	source = normalizeTokenSource(source)
	status = normalizeTokenStatus(status)
	if status == "" {
		status = TokenStatusActive
	}
	_, err := tx.Exec(
		`INSERT OR IGNORE INTO tokens(
			token, token_hash, token_preview, source, status, email, user_id,
			last_checked_at, invalidated_at, invalid_reason, created_at, updated_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		token, tokenHash(token), tokenPreview(token), source, status, email, userID,
		lastChecked, invalidatedAt, invalidReason, formatTimeForStore(now), formatTimeForStore(now),
	)
	if err != nil {
		return false, err
	}
	var changed int
	if err := tx.QueryRow(`SELECT changes()`).Scan(&changed); err != nil {
		return false, err
	}
	if changed > 0 {
		info, err := getTokenTx(tx, token)
		if err != nil {
			return false, err
		}
		if err := addTokenEventTx(tx, info.ID, "created", "", status, source, now); err != nil {
			return false, err
		}
	}
	return changed > 0, nil
}

func upsertReplacementTokenTx(tx *sql.Tx, oldInfo TokenInfo, newToken, email, userID string, checkedAt time.Time, source string, now time.Time) (TokenInfo, bool, error) {
	if email == "" {
		email = oldInfo.Email
	}
	if userID == "" {
		userID = oldInfo.UserID
	}
	if source == "" {
		source = oldInfo.Source
	}
	inserted, err := insertTokenTx(tx, newToken, source, TokenStatusActive, email, userID, formatTimeForStore(checkedAt), "", "", now)
	if err != nil {
		return TokenInfo{}, false, err
	}
	newInfo, err := getTokenTx(tx, newToken)
	if err != nil {
		return TokenInfo{}, false, err
	}
	_, err = tx.Exec(
		`UPDATE tokens
		 SET status = ?, email = ?, user_id = ?, use_count = use_count + ?,
			 last_checked_at = ?, last_refreshed_at = ?, updated_at = ?
		 WHERE id = ?`,
		TokenStatusActive, email, userID, oldInfo.UseCount,
		formatTimeForStore(checkedAt), formatTimeForStore(checkedAt), formatTimeForStore(now), newInfo.ID,
	)
	if err != nil {
		return TokenInfo{}, false, err
	}
	if err := addTokenEventTx(tx, newInfo.ID, "replacement_active", "", TokenStatusActive, oldInfo.TokenPreview, now); err != nil {
		return TokenInfo{}, false, err
	}
	newInfo, err = getTokenTx(tx, newToken)
	return newInfo, inserted, err
}

func markTokenCheckedTx(tx *sql.Tx, token, email, userID string, checkedAt time.Time, valid bool, refreshed bool) (TokenInfo, error) {
	if checkedAt.IsZero() {
		checkedAt = time.Now()
	}
	info, err := getTokenTx(tx, token)
	if errors.Is(err, sql.ErrNoRows) {
		return TokenInfo{}, ErrTokenNotFound
	}
	if err != nil {
		return TokenInfo{}, err
	}
	if email == "" {
		email = info.Email
	}
	if userID == "" {
		userID = info.UserID
	}
	status := info.Status
	invalidatedAt := info.InvalidatedAt
	invalidReason := info.InvalidReason
	if valid {
		status = TokenStatusActive
		invalidatedAt = time.Time{}
		invalidReason = ""
	}
	lastRefreshed := info.LastRefreshed
	if refreshed {
		lastRefreshed = checkedAt
	}
	now := time.Now()
	_, err = tx.Exec(
		`UPDATE tokens
		 SET status = ?, email = ?, user_id = ?, last_checked_at = ?, last_refreshed_at = ?,
			 invalidated_at = ?, invalid_reason = ?, updated_at = ?
		 WHERE id = ?`,
		status, email, userID, formatTimeForStore(checkedAt), formatTimeForStore(lastRefreshed),
		formatTimeForStore(invalidatedAt), invalidReason, formatTimeForStore(now), info.ID,
	)
	if err != nil {
		return TokenInfo{}, err
	}
	if err := addTokenEventTx(tx, info.ID, "checked", info.Status, status, "", now); err != nil {
		return TokenInfo{}, err
	}
	return getTokenTx(tx, token)
}

func getTokenTx(tx *sql.Tx, token string) (TokenInfo, error) {
	row := tx.QueryRow(`SELECT id, token, token_hash, token_preview, source, status, email, user_id, use_count,
		last_checked_at, last_refreshed_at, invalidated_at, invalid_reason, replaced_by_id, created_at, updated_at
		FROM tokens WHERE token_hash = ?`, tokenHash(token))
	return scanTokenInfo(row)
}

func getTokenByIDTx(tx *sql.Tx, id int64) (TokenInfo, error) {
	row := tx.QueryRow(`SELECT id, token, token_hash, token_preview, source, status, email, user_id, use_count,
		last_checked_at, last_refreshed_at, invalidated_at, invalid_reason, replaced_by_id, created_at, updated_at
		FROM tokens WHERE id = ?`, id)
	return scanTokenInfo(row)
}

func addTokenEventTx(tx *sql.Tx, tokenID int64, eventType, from, to, detail string, now time.Time) error {
	_, err := tx.Exec(
		`INSERT INTO token_events(token_id, event_type, status_from, status_to, detail, created_at)
		 VALUES(?, ?, ?, ?, ?, ?)`,
		tokenID, eventType, from, to, detail, formatTimeForStore(now),
	)
	return err
}

type tokenScanner interface {
	Scan(dest ...any) error
}

func scanTokenInfo(scanner tokenScanner) (TokenInfo, error) {
	var info TokenInfo
	var lastChecked, lastRefreshed, invalidated, created, updated string
	var replacedBy sql.NullInt64
	err := scanner.Scan(
		&info.ID,
		&info.Token,
		&info.TokenHash,
		&info.TokenPreview,
		&info.Source,
		&info.Status,
		&info.Email,
		&info.UserID,
		&info.UseCount,
		&lastChecked,
		&lastRefreshed,
		&invalidated,
		&info.InvalidReason,
		&replacedBy,
		&created,
		&updated,
	)
	if err != nil {
		return TokenInfo{}, err
	}
	info.Valid = info.Status == TokenStatusActive
	info.source = tokenSource(info.Source)
	info.LastChecked = parseTimeFromStore(lastChecked)
	info.LastRefreshed = parseTimeFromStore(lastRefreshed)
	info.InvalidatedAt = parseTimeFromStore(invalidated)
	info.CreatedAt = parseTimeFromStore(created)
	info.UpdatedAt = parseTimeFromStore(updated)
	if replacedBy.Valid {
		info.ReplacedByID = replacedBy.Int64
	}
	return info, nil
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(normalizeTokenValue(token)))
	return hex.EncodeToString(sum[:])
}

func tokenPreview(token string) string {
	token = normalizeTokenValue(token)
	if token == "" {
		return ""
	}
	if len(token) <= 22 {
		return token
	}
	return token[:10] + "..." + token[len(token)-8:]
}

func normalizeTokenSource(source string) string {
	source = strings.TrimSpace(source)
	switch source {
	case TokenSourceLegacyFile, TokenSourceAPI, TokenSourceEnvBackup:
		return source
	default:
		return TokenSourceAPI
	}
}

func normalizeTokenStatus(status string) string {
	status = strings.TrimSpace(strings.ToLower(status))
	switch status {
	case TokenStatusActive, TokenStatusInvalid, TokenStatusDisabled, TokenStatusRotated:
		return status
	default:
		return ""
	}
}

func normalizeTokenStatusFilter(status string) string {
	status = strings.TrimSpace(strings.ToLower(status))
	if status == "" || status == "all" {
		return ""
	}
	return normalizeTokenStatus(status)
}

func formatTimeForStore(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTimeFromStore(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return t
}
