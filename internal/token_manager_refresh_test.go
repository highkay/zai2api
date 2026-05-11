package internal

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestTokenManagerRefreshRotatesAndPersistsFileToken(t *testing.T) {
	initTokenTests(t)

	oldCfg := Cfg
	oldFetch := fetchAuthSessionFunc
	Cfg = &Config{}
	fetchAuthSessionFunc = func(token string) (authSessionResponse, int, error) {
		if token != "old.one.two" {
			t.Fatalf("unexpected token: %s", token)
		}
		return authSessionResponse{
			Token: "new.one.two",
			Email: "user@example.com",
			ID:    "user-1",
		}, 200, nil
	}
	t.Cleanup(func() {
		Cfg = oldCfg
		fetchAuthSessionFunc = oldFetch
	})

	tempDir := t.TempDir()
	tokenFile := filepath.Join(tempDir, "tokens.txt")
	if err := os.WriteFile(tokenFile, []byte("old.one.two\n"), 0644); err != nil {
		t.Fatalf("write token file: %v", err)
	}

	tm := NewTokenManager(tempDir)
	if err := tm.loadTokens(); err != nil {
		t.Fatalf("load tokens: %v", err)
	}

	outcome := tm.RefreshToken("old.one.two", true)
	if !outcome.Valid || !outcome.Refreshed || outcome.Token != "new.one.two" {
		t.Fatalf("unexpected refresh outcome: %+v", outcome)
	}

	if _, exists := tm.GetTokenInfo("old.one.two"); exists {
		t.Fatalf("old token should have been replaced")
	}
	info, exists := tm.GetTokenInfo("new.one.two")
	if !exists {
		t.Fatalf("new token not found in manager")
	}
	if info.Email != "user@example.com" || info.UserID != "user-1" {
		t.Fatalf("unexpected refreshed token metadata: %+v", info)
	}
	if info.LastRefreshed.IsZero() {
		t.Fatalf("expected LastRefreshed to be set")
	}

	tokens, err := tm.readTokenEntriesFromFile()
	if err != nil {
		t.Fatalf("read token file: %v", err)
	}
	if len(tokens) != 1 || tokens[0] != "new.one.two" {
		t.Fatalf("unexpected persisted tokens: %+v", tokens)
	}
}

func TestTokenManagerRefreshKeepsTokenOnTransientFailure(t *testing.T) {
	initTokenTests(t)

	oldCfg := Cfg
	oldFetch := fetchAuthSessionFunc
	Cfg = &Config{}
	fetchAuthSessionFunc = func(token string) (authSessionResponse, int, error) {
		if token != "alpha.one.two" {
			t.Fatalf("unexpected token: %s", token)
		}
		return authSessionResponse{}, 0, errors.New("temporary network failure")
	}
	t.Cleanup(func() {
		Cfg = oldCfg
		fetchAuthSessionFunc = oldFetch
	})

	tempDir := t.TempDir()
	tokenFile := filepath.Join(tempDir, "tokens.txt")
	if err := os.WriteFile(tokenFile, []byte("alpha.one.two\n"), 0644); err != nil {
		t.Fatalf("write token file: %v", err)
	}

	tm := NewTokenManager(tempDir)
	if err := tm.loadTokens(); err != nil {
		t.Fatalf("load tokens: %v", err)
	}

	outcome := tm.RefreshToken("alpha.one.two", true)
	if !outcome.Valid || outcome.Refreshed || outcome.Token != "alpha.one.two" {
		t.Fatalf("unexpected refresh outcome on transient failure: %+v", outcome)
	}

	info, exists := tm.GetTokenInfo("alpha.one.two")
	if !exists || !info.Valid {
		t.Fatalf("token should remain valid after transient failure: %+v exists=%v", info, exists)
	}
	if info.LastChecked.IsZero() {
		t.Fatalf("expected LastChecked to be updated")
	}

	tokens, err := tm.readTokenEntriesFromFile()
	if err != nil {
		t.Fatalf("read token file: %v", err)
	}
	if len(tokens) != 1 || tokens[0] != "alpha.one.two" {
		t.Fatalf("unexpected persisted tokens after transient failure: %+v", tokens)
	}
}

func TestTokenManagerLoadsBackupTokensIntoPool(t *testing.T) {
	initTokenTests(t)

	oldCfg := Cfg
	Cfg = &Config{BackupTokens: []string{"backup.one.two"}}
	t.Cleanup(func() {
		Cfg = oldCfg
	})

	tm := NewTokenManager(t.TempDir())
	if err := tm.loadTokens(); err != nil {
		t.Fatalf("load tokens: %v", err)
	}

	if !tm.HasValidUpstreamTokens() {
		t.Fatalf("expected backup token to be available")
	}
	token := tm.GetToken()
	if token != "backup.one.two" {
		t.Fatalf("unexpected token from pool: %s", token)
	}

	info, exists := tm.GetTokenInfo("backup.one.two")
	if !exists || !info.Valid {
		t.Fatalf("backup token missing from manager: %+v exists=%v", info, exists)
	}
	if !info.LastChecked.IsZero() || !info.LastRefreshed.IsZero() {
		t.Fatalf("backup token should not be marked refreshed before use: %+v", info)
	}
}
