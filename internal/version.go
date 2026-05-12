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
	feVersion   = DefaultFeVersion
	versionLock sync.RWMutex
)

func GetFeVersion() string {
	versionLock.RLock()
	defer versionLock.RUnlock()
	if feVersion == "" {
		return DefaultFeVersion
	}
	return feVersion
}

func fetchFeVersion() {
	client, err := TLSHTTPClient(15 * time.Second)
	if err != nil {
		LogError("Failed to create tls client for fe version: %v", err)
		return
	}
	req, err := fhttp.NewRequest("GET", "https://chat.z.ai/", nil)
	if err != nil {
		LogError("Failed to create fe version request: %v", err)
		return
	}
	ApplyBrowserFingerprintHeaders(req.Header)
	resp, err := client.Do(req)
	if err != nil {
		LogError("Failed to fetch fe version: %v", err)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		LogError("Failed to read fe version response: %v", err)
		return
	}

	re := regexp.MustCompile(`prod-fe-[\.\d]+`)
	match := re.FindString(string(body))
	if match != "" {
		versionLock.Lock()
		feVersion = match
		versionLock.Unlock()
		LogInfo("Updated fe version: %s", match)
	}
}

func StartVersionUpdater() {
	fetchFeVersion()

	ticker := time.NewTicker(1 * time.Hour)
	go func() {
		for range ticker.C {
			fetchFeVersion()
		}
	}()
}
