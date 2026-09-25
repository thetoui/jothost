package middleware

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/jothost/panel/api/internal/httpx"
)

// These tests run a real http.Server with short read and write timeouts and
// send requests through the Logger middleware, because that is where the bug
// lived: a handler's own context timeout cannot outlast the connection's
// deadlines, and the middleware's wrapper hid the connection from
// http.ResponseController. The control cases are the bug itself, kept so the
// tests prove they can tell the difference.

const (
	shortLimit = 200 * time.Millisecond
	slowWork   = 700 * time.Millisecond
)

func startServer(t *testing.T, readTimeout, writeTimeout time.Duration, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewUnstartedServer(Logger(quiet)(h))
	srv.Config.ReadTimeout = readTimeout
	srv.Config.WriteTimeout = writeTimeout
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

func slowResponse(useLongRequest bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var cancel context.CancelFunc
		if useLongRequest {
			_, cancel = httpx.LongRequest(w, r, 10*time.Second)
		} else {
			_, cancel = context.WithTimeout(r.Context(), 10*time.Second)
		}
		defer cancel()
		time.Sleep(slowWork) // a dump or an install: nothing written yet
		_, _ = io.WriteString(w, "done")
	}
}

func TestLongRequestOutlivesTheWriteTimeout(t *testing.T) {
	srv := startServer(t, 5*time.Second, shortLimit, slowResponse(true))

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("a long request was cut off by the server's write timeout: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil || string(body) != "done" {
		t.Fatalf("got body %q, err %v; want the full response", body, err)
	}
}

func TestAContextTimeoutAloneIsCutOff(t *testing.T) {
	// The bug: a thirty-minute context does nothing about a thirty-second
	// connection deadline.
	srv := startServer(t, 5*time.Second, shortLimit, slowResponse(false))

	resp, err := http.Get(srv.URL)
	if err == nil {
		body, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr == nil && string(body) == "done" {
			t.Fatal("control: a plain context timeout survived the write timeout, so these tests prove nothing")
		}
	}
}

// slowUpload streams a body in two halves with a pause between them, the way
// a large import arrives over a real connection.
func slowUpload(t *testing.T, url string) (*http.Response, error) {
	t.Helper()
	pr, pw := io.Pipe()
	go func() {
		_, _ = pw.Write(make([]byte, 1024))
		time.Sleep(slowWork)
		_, _ = pw.Write(make([]byte, 1024))
		_ = pw.Close()
	}()
	req, err := http.NewRequest(http.MethodPost, url, pr)
	if err != nil {
		t.Fatal(err)
	}
	return http.DefaultClient.Do(req)
}

func readBody(useLongRequest bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if useLongRequest {
			_, cancel := httpx.LongRequest(w, r, 10*time.Second)
			defer cancel()
		}
		n, err := io.Copy(io.Discard, r.Body)
		if err != nil {
			http.Error(w, "read failed: "+err.Error(), http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, strconv.FormatInt(n, 10))
	}
}

func TestLongRequestOutlivesTheReadTimeout(t *testing.T) {
	srv := startServer(t, shortLimit, 5*time.Second, readBody(true))

	resp, err := slowUpload(t, srv.URL)
	if err != nil {
		t.Fatalf("a slow upload was cut off by the server's read timeout: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "2048" {
		t.Fatalf("got %d %q; want 200 and the whole 2048-byte body", resp.StatusCode, body)
	}
}

func TestAContextTimeoutAloneStopsASlowUpload(t *testing.T) {
	srv := startServer(t, shortLimit, 5*time.Second, readBody(false))

	resp, err := slowUpload(t, srv.URL)
	if err != nil {
		return // the connection was dropped mid-body: the bug, as expected
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusOK && string(body) == "2048" {
		t.Fatal("control: a slow upload survived the read timeout without LongRequest, so these tests prove nothing")
	}
}

func TestStatusRecorderUnwrapsForResponseController(t *testing.T) {
	rec := &statusRecorder{ResponseWriter: httptest.NewRecorder()}
	var _ interface{ Unwrap() http.ResponseWriter } = rec
	if rec.Unwrap() != rec.ResponseWriter {
		t.Fatal("Unwrap must return the wrapped writer")
	}
}
