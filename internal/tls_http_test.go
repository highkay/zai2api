package internal

import (
	"path/filepath"
	"testing"
	"time"

	tls_client "github.com/bogdanfinn/tls-client"
)

func TestTLSHTTPClientUsesConfiguredUpstreamProxy(t *testing.T) {
	oldCfg := Cfg
	tlsClientMu.Lock()
	oldCache := tlsByKey
	tlsByKey = map[tlsClientCacheKey]tls_client.HttpClient{}
	tlsClientMu.Unlock()
	t.Cleanup(func() {
		Cfg = oldCfg
		tlsClientMu.Lock()
		tlsByKey = oldCache
		tlsClientMu.Unlock()
	})

	Cfg = &Config{UpstreamProxy: "http://127.0.0.1:7890"}
	proxied, err := TLSHTTPClient(2 * time.Second)
	if err != nil {
		t.Fatalf("build proxied client: %v", err)
	}
	if got := proxied.GetProxy(); got != "http://127.0.0.1:7890" {
		t.Fatalf("unexpected proxy URL: %q", got)
	}

	Cfg = &Config{}
	direct, err := TLSHTTPClient(2 * time.Second)
	if err != nil {
		t.Fatalf("build direct client: %v", err)
	}
	if got := direct.GetProxy(); got != "" {
		t.Fatalf("expected direct client, got proxy %q", got)
	}
}

func TestTLSHTTPClientUsesRuntimeUpdatedUpstreamProxy(t *testing.T) {
	oldCfg := Cfg
	tlsClientMu.Lock()
	oldCache := tlsByKey
	tlsByKey = map[tlsClientCacheKey]tls_client.HttpClient{}
	tlsClientMu.Unlock()
	t.Cleanup(func() {
		Cfg = oldCfg
		tlsClientMu.Lock()
		tlsByKey = oldCache
		tlsClientMu.Unlock()
	})

	Cfg = &Config{
		UpstreamProxy:     "http://127.0.0.1:7890",
		RuntimeConfigPath: filepath.Join(t.TempDir(), "runtime_config.json"),
	}
	first, err := TLSHTTPClient(2 * time.Second)
	if err != nil {
		t.Fatalf("build first client: %v", err)
	}
	if got := first.GetProxy(); got != "http://127.0.0.1:7890" {
		t.Fatalf("unexpected first proxy: %q", got)
	}

	nextProxy := "http://127.0.0.1:7891"
	if _, err := UpdateRuntimeConfig(RuntimeConfigUpdate{UpstreamProxy: &nextProxy}); err != nil {
		t.Fatalf("update runtime config: %v", err)
	}
	second, err := TLSHTTPClient(2 * time.Second)
	if err != nil {
		t.Fatalf("build second client: %v", err)
	}
	if got := second.GetProxy(); got != nextProxy {
		t.Fatalf("expected runtime-updated proxy %q, got %q", nextProxy, got)
	}
}
