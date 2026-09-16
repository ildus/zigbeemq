package web

import (
	"github.com/ildus/zigbeemq/internal/hub"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testServer(t *testing.T) http.Handler {
	t.Helper()
	return New(hub.New(slog.New(slog.NewTextHandler(io.Discard, nil)), t.TempDir())).Handler()
}
func req(t *testing.T, h http.Handler, m, p, b string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(m, p, strings.NewReader(b))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestDevicesResponse(t *testing.T) {
	w := req(t, testServer(t), "GET", "/api/devices", "")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Type") != "application/json" || !strings.Contains(w.Body.String(), "devices") {
		t.Fatalf("response: %d %v %s", w.Code, w.Header(), w.Body.String())
	}
}
func TestMethodNotAllowed(t *testing.T) {
	if w := req(t, testServer(t), "POST", "/api/devices", ""); w.Code != http.StatusNotFound {
		t.Fatalf("status=%d", w.Code)
	}
}
func TestMalformedJSON(t *testing.T) {
	h := testServer(t)
	for _, x := range []struct{ m, p string }{{"POST", "/api/devices/x/brightness"}, {"PATCH", "/api/devices/x"}} {
		w := req(t, h, x.m, x.p, "{")
		if w.Code != 400 || w.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("status=%d headers=%v", w.Code, w.Header())
		}
	}
}
func TestMissingDeviceUpdate(t *testing.T) {
	w := req(t, testServer(t), "PATCH", "/api/devices/00124b0000000001", "{\"name\":\"x\",\"kind\":\"light\"}")
	if w.Code != 404 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}
func TestStaticIndex(t *testing.T) {
	w := req(t, testServer(t), "GET", "/", "")
	if w.Code != 200 || !strings.Contains(strings.ToLower(w.Body.String()), "<!doctype html>") {
		t.Fatalf("status=%d", w.Code)
	}
}
