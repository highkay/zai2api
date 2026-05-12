package internal

import (
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
