package internal

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleConsoleServesSelfContainedPage(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/console", nil)
	rec := httptest.NewRecorder()

	HandleConsole(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	contentType := rec.Header().Get("Content-Type")
	if !strings.Contains(contentType, "text/html") {
		t.Fatalf("expected html content type, got %q", contentType)
	}
	body := rec.Body.String()
	for _, expected := range []string{"zai2api", "/v1/tokens", "total-calls", "failed-calls"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("console page missing %q", expected)
		}
	}
}

func TestHandleConsoleRejectsNonGET(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/console", nil)
	rec := httptest.NewRecorder()

	HandleConsole(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestHandleConsoleAllowsHEADProbe(t *testing.T) {
	req := httptest.NewRequest(http.MethodHead, "/console", nil)
	rec := httptest.NewRecorder()

	HandleConsole(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("expected empty HEAD response body, got %q", rec.Body.String())
	}
}
