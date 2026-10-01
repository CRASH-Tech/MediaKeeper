package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// With -debug the server tells what the Jellyfin apps ask and what it
// answers: a line per request on the console, and in jellyfin-debug.log
// (next to the settings) the bodies of requests and JSON answers in full —
// the device profile an app sends with PlaybackInfo, the media sources it
// gets back — and for video the range asked for and the bytes sent. Login
// tokens are blanked out.

type debugLog struct {
	mu   sync.Mutex
	file *os.File
	path string
}

func openDebugLog() (*debugLog, error) {
	path := filepath.Join(filepath.Dir(configPath()), "jellyfin-debug.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &debugLog{file: f, path: path}, nil
}

func (d *debugLog) write(text string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.file.WriteString(text)
}

// debugBodyLimit keeps one answer from filling the file: a long list of
// items says nothing more after the first few dozen kilobytes.
const debugBodyLimit = 64 << 10

// debugRecorder passes an answer through and keeps what the log needs.
type debugRecorder struct {
	http.ResponseWriter
	status int
	sent   int64
	body   bytes.Buffer // of JSON and text answers, up to the limit
}

func (r *debugRecorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *debugRecorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(p)
	r.sent += int64(n)
	if ct := r.Header().Get("Content-Type"); (strings.Contains(ct, "json") || strings.HasPrefix(ct, "text/")) && r.body.Len() < debugBodyLimit {
		r.body.Write(p[:min(n, debugBodyLimit-r.body.Len())])
	}
	return n, err
}

func (r *debugRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

var reSecret = regexp.MustCompile(`(?i)(\b(?:api_key|ApiKey|Token|AccessToken|Pw|Password)"?\s*[=:]\s*"?)[^"&,\s]+`)

// hideSecrets blanks out tokens and passwords in a logged text.
func hideSecrets(s string) string { return reSecret.ReplaceAllString(s, "${1}***") }

// debugJellyfin logs one request of a Jellyfin app around its handling.
func (s *Server) debugJellyfin(w http.ResponseWriter, r *http.Request, handle func(http.ResponseWriter, *http.Request)) {
	start := time.Now()
	var reqBody []byte
	if r.Body != nil && r.Method != http.MethodGet && r.Method != http.MethodHead {
		reqBody, _ = io.ReadAll(io.LimitReader(r.Body, debugBodyLimit))
		r.Body = io.NopCloser(bytes.NewReader(reqBody))
	}
	rec := &debugRecorder{ResponseWriter: w}
	handle(rec, r)
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	took := time.Since(start).Round(time.Millisecond)
	client := strings.TrimSpace(jfClient(r, "Client") + " " + jfClient(r, "Version"))
	if client == "" {
		client = r.UserAgent()
	}
	target := r.URL.Path
	if r.URL.RawQuery != "" {
		if q, err := url.QueryUnescape(r.URL.RawQuery); err == nil {
			target += "?" + q
		} else {
			target += "?" + r.URL.RawQuery
		}
	}
	target = hideSecrets(target)
	s.log("JF %s %s → %d, %s, %v  [%s]", r.Method, shorten(target, 160), rec.status, sizeText(rec.sent), took, client)

	var b strings.Builder
	fmt.Fprintf(&b, "=== %s  %s %s\n    → %d, %d bytes, %v  [%s, %s]\n",
		start.Format("15:04:05.000"), r.Method, target, rec.status, rec.sent, took, client, clientIP(r))
	if rg := r.Header.Get("Range"); rg != "" {
		fmt.Fprintf(&b, "    Range: %s   Content-Type: %s   Content-Range: %s\n", rg, rec.Header().Get("Content-Type"), rec.Header().Get("Content-Range"))
	} else if ct := rec.Header().Get("Content-Type"); ct != "" && rec.body.Len() == 0 {
		fmt.Fprintf(&b, "    Content-Type: %s\n", ct)
	}
	if len(reqBody) > 0 {
		fmt.Fprintf(&b, "  > %s\n", hideSecrets(string(reqBody)))
	}
	if rec.body.Len() > 0 {
		fmt.Fprintf(&b, "  < %s\n", hideSecrets(rec.body.String()))
	}
	s.debug.write(b.String() + "\n")
}

func shorten(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func sizeText(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
