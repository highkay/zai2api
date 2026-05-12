package internal

import (
	"io"
	"regexp"
	"sync"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
)

const DefaultFeVersion = "prod-fe-1.1.29"

var (
	feVersion            = DefaultFeVersion
	versionLock          sync.RWMutex
	refreshFeVersionFunc = refreshFeVersion
)

func GetFeVersion() string {
	versionLock.RLock()
	defer versionLock.RUnlock()
	if feVersion == "" {
		return DefaultFeVersion
	}
	return feVersion
}

func RefreshFeVersion() (string, bool) {
	return refreshFeVersionFunc()
}

func refreshFeVersion() (string, bool) {
	client, err := TLSHTTPClient(15 * time.Second)
	if err != nil {
		LogError("Failed to create tls client for fe version: %v", err)
		return GetFeVersion(), false
	}
	req, err := fhttp.NewRequest("GET", "https://chat.z.ai/", nil)
	if err != nil {
		LogError("Failed to create fe version request: %v", err)
		return GetFeVersion(), false
	}
	ApplyBrowserFingerprintHeaders(req.Header)
	resp, err := client.Do(req)
	if err != nil {
		LogError("Failed to fetch fe version: %v", err)
		return GetFeVersion(), false
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		LogError("Failed to read fe version response: %v", err)
		return GetFeVersion(), false
	}

	re := regexp.MustCompile(`prod-fe-[\.\d]+`)
	match := re.FindString(string(body))
	if match != "" {
		oldVersion := GetFeVersion()
		versionLock.Lock()
		feVersion = match
		versionLock.Unlock()
		LogInfo("Updated fe version: %s", match)
		return match, match != oldVersion
	}
	return GetFeVersion(), false
}

func StartVersionUpdater() {
	RefreshFeVersion()

	ticker := time.NewTicker(1 * time.Hour)
	go func() {
		for range ticker.C {
			RefreshFeVersion()
		}
	}()
}
