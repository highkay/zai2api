package internal

import (
	"fmt"
	"sync"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

// DefaultBrowserProfile 与 tls-client 内置 Chrome_133 一致（ClientHello、HTTP/2 SETTINGS/优先级/伪头序等），便于 JA3/JA4 与 HTTP/2 指纹对齐。
var DefaultBrowserProfile = profiles.Chrome_133

// BrowserUserAgent 必须与 DefaultBrowserProfile 同源，勿随机替换，否则与 TLS 指纹不一致。
const BrowserUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36"

var (
	tlsClientMu sync.Mutex
	tlsByKey    = map[tlsClientCacheKey]tls_client.HttpClient{}
)

type tlsClientCacheKey struct {
	timeoutSeconds int
	proxyURL       string
}

// TLSHTTPClient 返回按超时和上游代理配置复用的 tls-client 实例；所有出站 HTTPS 应通过此处以统一指纹。
func TLSHTTPClient(timeout time.Duration) (tls_client.HttpClient, error) {
	sec := int(timeout.Round(time.Second) / time.Second)
	if sec < 1 {
		sec = 1
	}
	proxyURL := ""
	if Cfg != nil {
		proxyURL = Cfg.UpstreamProxy
	}
	key := tlsClientCacheKey{timeoutSeconds: sec, proxyURL: proxyURL}
	tlsClientMu.Lock()
	defer tlsClientMu.Unlock()
	if c, ok := tlsByKey[key]; ok {
		return c, nil
	}
	opts := []tls_client.HttpClientOption{
		tls_client.WithTimeoutSeconds(sec),
		tls_client.WithClientProfile(DefaultBrowserProfile),
		tls_client.WithRandomTLSExtensionOrder(),
	}
	if proxyURL != "" {
		opts = append(opts, tls_client.WithProxyUrl(proxyURL))
	}
	c, err := tls_client.NewHttpClient(tls_client.NewNoopLogger(), opts...)
	if err != nil {
		return nil, fmt.Errorf("tls-client: %w", err)
	}
	tlsByKey[key] = c
	return c, nil
}

// ApplyBrowserFingerprintHeaders 设置与 Chrome 133 一致的 User-Agent 与 Sec-CH-UA（配合 TLSHTTPClient 的 TLS/H2 指纹）。
func ApplyBrowserFingerprintHeaders(h fhttp.Header) {
	h.Set("User-Agent", BrowserUserAgent)
	h.Set("sec-ch-ua", `"Google Chrome";v="133", "Chromium";v="133", "Not A(Brand";v="24"`)
	h.Set("sec-ch-ua-mobile", "?0")
	h.Set("sec-ch-ua-platform", `"Windows"`)
}
