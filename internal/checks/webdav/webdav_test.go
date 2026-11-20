package webdav

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Gevatter-Tod/vshm/internal/logging"
	"github.com/Gevatter-Tod/vshm/internal/scheduler"
)

const fixtureCreds = "../../../tests/fixtures/webdav/basic.ini"

const validMultistatus = `<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:">
  <D:response>
    <D:href>/dav/</D:href>
    <D:propstat>
      <D:prop><D:displayname>dav</D:displayname></D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>
  <D:response>
    <D:href>/dav/readme.txt</D:href>
    <D:propstat>
      <D:prop><D:getcontentlength>42</D:getcontentlength></D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
    <D:propstat>
      <D:prop><D:lockdiscovery/></D:prop>
      <D:status>HTTP/1.1 404 Not Found</D:status>
    </D:propstat>
  </D:response>
</D:multistatus>`

// capturedRequest records what the fake server actually received.
type capturedRequest struct {
	method      string
	depth       string
	auth        string
	contentType string
}

// fakeDAV returns an httptest server that answers every request with the given
// status/body and records the last request. If redirectTo is set, the server
// answers 302 to redirectTo instead.
func fakeDAV(t *testing.T, status int, body string, redirectTo string) (*httptest.Server, *capturedRequest) {
	t.Helper()
	cap := &capturedRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.method = r.Method
		cap.depth = r.Header.Get("Depth")
		cap.auth = r.Header.Get("Authorization")
		cap.contentType = r.Header.Get("Content-Type")
		if redirectTo != "" {
			http.Redirect(w, r, redirectTo, http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, cap
}

func testLogger() *logging.Logger { return logging.New("webdav-test: ") }

// credFixture copies the credential fixture into a temp file with mode 0600:
// the runtime loader fails closed on group/world-readable files.
func credFixture(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(fixtureCreds)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "webdav.ini")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunSuccessDepth1(t *testing.T) {
	srv, cap := fakeDAV(t, http.StatusMultiStatus, validMultistatus, "")
	c := New(testLogger())
	res := c.Run(context.Background(), NewConfig("dav1", srv.URL+"/dav", 1, "", 5*time.Second))
	if !res.Success {
		t.Fatalf("expected success, got %+v", res)
	}
	if res.ReasonCode != reasonSuccess {
		t.Errorf("reason_code = %q, want %q", res.ReasonCode, reasonSuccess)
	}
	if res.CheckID != "dav1" || res.CheckType != "webdav" {
		t.Errorf("bad ids: %+v", res)
	}
	if res.StartedAt.Location() != time.UTC || res.CompletedAt.Location() != time.UTC {
		t.Errorf("timestamps not UTC: %+v", res)
	}
	if res.Duration < 0 || res.CompletedAt.Before(res.StartedAt) {
		t.Errorf("bad duration: %+v", res)
	}
	// Read-only method and bounded depth actually sent on the wire.
	if cap.method != "PROPFIND" {
		t.Errorf("method = %q, want PROPFIND", cap.method)
	}
	if cap.depth != "1" {
		t.Errorf("Depth header = %q, want \"1\"", cap.depth)
	}
	if !strings.HasPrefix(cap.contentType, "application/xml") {
		t.Errorf("Content-Type = %q, want application/xml", cap.contentType)
	}
	if cap.auth != "" {
		t.Errorf("Authorization header unexpectedly set without credential_file: %q", cap.auth)
	}
}

func TestRunSuccessDepth0(t *testing.T) {
	srv, cap := fakeDAV(t, http.StatusMultiStatus, validMultistatus, "")
	c := New(testLogger())
	res := c.Run(context.Background(), NewConfig("dav0", srv.URL, 0, "", 5*time.Second))
	if !res.Success {
		t.Fatalf("expected success, got %+v", res)
	}
	if cap.depth != "0" {
		t.Errorf("Depth header = %q, want \"0\"", cap.depth)
	}
}

func TestRunViaSchedulerSpec(t *testing.T) {
	srv, cap := fakeDAV(t, http.StatusMultiStatus, validMultistatus, "")
	c := New(testLogger())
	spec := &scheduler.Spec{
		ID:      "dav-spec",
		Typ:     "webdav",
		Timeout: 5 * time.Second,
		WebDAV:  &scheduler.WebDAVSpec{URL: srv.URL + "/dav", Depth: 1},
	}
	res := c.Run(context.Background(), spec)
	if !res.Success || res.ReasonCode != reasonSuccess {
		t.Fatalf("spec path failed: %+v", res)
	}
	if res.CheckID != "dav-spec" || res.CheckType != "webdav" {
		t.Errorf("bad ids from spec: %+v", res)
	}
	if cap.method != "PROPFIND" || cap.depth != "1" {
		t.Errorf("captured method=%q depth=%q", cap.method, cap.depth)
	}
}

func TestRunWrongStatus(t *testing.T) {
	for _, status := range []int{200, 204, 401, 500} {
		srv, _ := fakeDAV(t, status, validMultistatus, "")
		c := New(testLogger())
		res := c.Run(context.Background(), NewConfig("dav", srv.URL, 1, "", 5*time.Second))
		if res.Success {
			t.Fatalf("status %d: expected failure", status)
		}
		if res.ReasonCode != reasonBadStatus {
			t.Errorf("status %d: reason_code = %q, want %q", status, res.ReasonCode, reasonBadStatus)
		}
		if !strings.Contains(res.Reason, fmt.Sprintf("%d", status)) {
			t.Errorf("status %d: reason %q should carry the status code only", status, res.Reason)
		}
	}
}

func TestRunMalformedXML(t *testing.T) {
	srv, _ := fakeDAV(t, http.StatusMultiStatus, `<D:multistatus xmlns:D="DAV:"><D:response>`, "")
	c := New(testLogger())
	res := c.Run(context.Background(), NewConfig("dav", srv.URL, 1, "", 5*time.Second))
	if res.Success || res.ReasonCode != reasonMalformedXML {
		t.Fatalf("want malformed_xml, got %+v", res)
	}
}

func TestRunHTMLBodyIsWrongRoot(t *testing.T) {
	srv, _ := fakeDAV(t, http.StatusMultiStatus, `<html><body>error</body></html>`, "")
	c := New(testLogger())
	res := c.Run(context.Background(), NewConfig("dav", srv.URL, 1, "", 5*time.Second))
	if res.Success || res.ReasonCode != reasonWrongRoot {
		t.Fatalf("want wrong_root for HTML body, got %+v", res)
	}
	if strings.Contains(res.Reason, "error") || strings.Contains(res.Reason, "<") {
		t.Errorf("reason leaks body content: %q", res.Reason)
	}
}

func TestRunWrongRoot(t *testing.T) {
	bodies := []string{
		`<D:foo xmlns:D="DAV:"/>`,                                             // wrong local name, DAV ns
		`<multistatus xmlns="urn:example:not-dav"></multistatus>`,             // wrong namespace
		`<?xml version="1.0"?><D:multistatus xmlns:D="DAV:"></D:multistatus>`, // no response
	}
	want := []string{reasonWrongRoot, reasonWrongRoot, reasonNoResponse}
	for i, body := range bodies {
		srv, _ := fakeDAV(t, http.StatusMultiStatus, body, "")
		c := New(testLogger())
		res := c.Run(context.Background(), NewConfig("dav", srv.URL, 1, "", 5*time.Second))
		if res.Success || res.ReasonCode != want[i] {
			t.Errorf("body %d: want %q, got %+v", i, want[i], res)
		}
	}
}

func TestRunNoHref(t *testing.T) {
	body := `<D:multistatus xmlns:D="DAV:"><D:response><D:propstat><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response></D:multistatus>`
	srv, _ := fakeDAV(t, http.StatusMultiStatus, body, "")
	c := New(testLogger())
	res := c.Run(context.Background(), NewConfig("dav", srv.URL, 1, "", 5*time.Second))
	if res.Success || res.ReasonCode != reasonNoHref {
		t.Fatalf("want no_href, got %+v", res)
	}
}

func TestRunNoSuccessfulPropstat(t *testing.T) {
	body := `<D:multistatus xmlns:D="DAV:"><D:response><D:href>/dav/</D:href><D:propstat><D:status>HTTP/1.1 404 Not Found</D:status></D:propstat></D:response></D:multistatus>`
	srv, _ := fakeDAV(t, http.StatusMultiStatus, body, "")
	c := New(testLogger())
	res := c.Run(context.Background(), NewConfig("dav", srv.URL, 1, "", 5*time.Second))
	if res.Success || res.ReasonCode != reasonNoSuccessfulPropsta {
		t.Fatalf("want no_successful_propstat, got %+v", res)
	}
}

func TestRunMixedPropstatSucceeds(t *testing.T) {
	// One 2xx propstat is enough even if another propstat is 404 (covered by
	// the second response in validMultistatus).
	srv, _ := fakeDAV(t, http.StatusMultiStatus, validMultistatus, "")
	c := New(testLogger())
	res := c.Run(context.Background(), NewConfig("dav", srv.URL, 1, "", 5*time.Second))
	if !res.Success {
		t.Fatalf("mixed propstat should succeed, got %+v", res)
	}
}

func TestRunBodyTooLarge(t *testing.T) {
	big := `<D:multistatus xmlns:D="DAV:"><D:response><D:href>/` + strings.Repeat("a", maxBodySize) + `</D:href></D:response></D:multistatus>`
	srv, _ := fakeDAV(t, http.StatusMultiStatus, big, "")
	c := New(testLogger())
	res := c.Run(context.Background(), NewConfig("dav", srv.URL, 1, "", 10*time.Second))
	if res.Success || res.ReasonCode != reasonBodyTooLarge {
		t.Fatalf("want body_too_large, got %+v", res)
	}
}

func TestRunTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusMultiStatus)
	}))
	t.Cleanup(srv.Close)
	c := New(testLogger())
	res := c.Run(context.Background(), NewConfig("dav", srv.URL, 1, "", 100*time.Millisecond))
	if res.Success || res.ReasonCode != reasonTimeout {
		t.Fatalf("want timeout, got %+v", res)
	}
}

func TestRunContextDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusMultiStatus)
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	c := New(testLogger())
	res := c.Run(ctx, NewConfig("dav", srv.URL, 1, "", 10*time.Second))
	if res.Success || res.ReasonCode != reasonTimeout {
		t.Fatalf("want timeout from ctx deadline, got %+v", res)
	}
}

func TestRunRedirectFollowed(t *testing.T) {
	// User-confirmed WP-08 §7 decision: WebDAV follows redirects (unlike the
	// HTTP check). A 307/308 preserves the PROPFIND method; 301/302/303 would
	// downgrade it to GET per Go's redirect rules (and then fail with
	// bad_status, since a GET does not return 207 — safe).
	var mu sync.Mutex
	finalHit := false
	finalMethod := ""
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/dav", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PROPFIND" {
			t.Errorf("redirect source got method %q", r.Method)
		}
		w.Header().Set("Location", "/dav-final")
		w.WriteHeader(http.StatusTemporaryRedirect) // 307: method preserved
	})
	mux.HandleFunc("/dav-final", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		finalHit = true
		finalMethod = r.Method
		mu.Unlock()
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusMultiStatus)
		fmt.Fprint(w, validMultistatus)
	})
	c := New(testLogger())
	res := c.Run(context.Background(), NewConfig("dav", srv.URL+"/dav", 1, "", 5*time.Second))
	if !res.Success {
		t.Fatalf("expected success after following redirect, got %+v", res)
	}
	if !finalHit {
		t.Fatal("redirect target was never hit")
	}
	if finalMethod != "PROPFIND" {
		t.Errorf("final handler got method %q, want PROPFIND preserved by 307", finalMethod)
	}
}

func TestCredentialsRejectedOverPlainHTTP(t *testing.T) {
	srv, cap := fakeDAV(t, http.StatusMultiStatus, validMultistatus, "")
	c := New(testLogger())
	res := c.Run(context.Background(), NewConfig("dav", srv.URL, 1, credFixture(t), 5*time.Second))
	if res.Success || res.ReasonCode != reasonCredentialNotHTTPS {
		t.Fatalf("want credential_not_https over http, got %+v", res)
	}
	if cap.auth != "" {
		t.Errorf("Basic credentials were sent over plain HTTP: %q", cap.auth)
	}
}

func TestCredentialsOverVerifiedHTTPS(t *testing.T) {
	var cap capturedRequest
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.method = r.Method
		cap.depth = r.Header.Get("Depth")
		cap.auth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusMultiStatus)
		fmt.Fprint(w, validMultistatus)
	}))
	t.Cleanup(srv.Close)

	c := NewWithTransport(testLogger(), srv.Client().Transport)
	res := c.Run(context.Background(), NewConfig("dav", srv.URL+"/dav", 1, credFixture(t), 5*time.Second))
	if !res.Success {
		t.Fatalf("expected success over verified HTTPS, got %+v", res)
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("vshm_monitor:example-only-not-a-real-secret"))
	if cap.auth != want {
		t.Errorf("Authorization = %q, want %q", cap.auth, want)
	}
	if cap.method != "PROPFIND" || cap.depth != "1" {
		t.Errorf("captured method=%q depth=%q", cap.method, cap.depth)
	}
}

func TestTLSVerificationMandatory(t *testing.T) {
	// A TLS server with a certificate the default transport does not trust must
	// fail (InsecureSkipVerify is false).
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMultiStatus)
	}))
	t.Cleanup(srv.Close)
	c := New(testLogger()) // default transport: does not trust the test CA
	res := c.Run(context.Background(), NewConfig("dav", srv.URL, 1, "", 5*time.Second))
	if res.Success {
		t.Fatalf("expected TLS verification failure, got %+v", res)
	}
	if res.ReasonCode != reasonRequestFailed && res.ReasonCode != reasonTimeout {
		t.Errorf("reason_code = %q, want request_failed", res.ReasonCode)
	}
}

func TestCredentialFileFailClosed(t *testing.T) {
	// TLS server so the HTTPS gate passes and the credential loader is reached.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMultiStatus)
		fmt.Fprint(w, validMultistatus)
	}))
	t.Cleanup(srv.Close)
	cases := []struct {
		name    string
		content string
		mode    os.FileMode
	}{
		{"world-readable", "[basic]\nusername = u\npassword = p\n", 0o644},
		{"group-writable", "[basic]\nusername = u\npassword = p\n", 0o620},
		{"missing-basic-section", "[other]\nusername = u\n", 0o600},
		{"missing-password", "[basic]\nusername = u\n", 0o600},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "creds.ini")
			if err := os.WriteFile(path, []byte(tc.content), tc.mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, tc.mode); err != nil { // defeat umask
				t.Fatal(err)
			}
			c := NewWithTransport(testLogger(), srv.Client().Transport)
			res := c.Run(context.Background(), NewConfig("dav", srv.URL, 1, path, 5*time.Second))
			if res.Success || res.ReasonCode != reasonCredentialError {
				t.Fatalf("want credential_error, got %+v", res)
			}
			// The reason must not leak the path or any file content.
			if strings.Contains(res.Reason, path) || strings.Contains(res.Reason, "password") {
				t.Errorf("reason leaks secret material: %q", res.Reason)
			}
		})
	}
}

func TestInvalidDepthAndScheme(t *testing.T) {
	srv, _ := fakeDAV(t, http.StatusMultiStatus, validMultistatus, "")
	c := New(testLogger())
	if res := c.Run(context.Background(), NewConfig("dav", srv.URL, 2, "", 5*time.Second)); res.Success || res.ReasonCode != reasonInvalidConfig {
		t.Errorf("depth 2: want invalid_config, got %+v", res)
	}
	if res := c.Run(context.Background(), NewConfig("dav", srv.URL, -1, "", 5*time.Second)); res.Success || res.ReasonCode != reasonInvalidConfig {
		t.Errorf("depth -1: want invalid_config, got %+v", res)
	}
	if res := c.Run(context.Background(), NewConfig("dav", "ftp://example.com/dav", 1, "", 5*time.Second)); res.Success || res.ReasonCode != reasonInvalidURL {
		t.Errorf("ftp scheme: want invalid_url, got %+v", res)
	}
	if res := c.Run(context.Background(), NewConfig("dav", "://nope", 1, "", 5*time.Second)); res.Success || res.ReasonCode != reasonInvalidURL {
		t.Errorf("garbage URL: want invalid_url, got %+v", res)
	}
}

func TestReasonRedaction(t *testing.T) {
	// Failure reasons must never contain the URL (it may embed userinfo), the
	// password, or the credential path.
	srv, _ := fakeDAV(t, 500, "internal error page", "")
	c := New(testLogger())
	credPath := credFixture(t)
	res := c.Run(context.Background(), NewConfig("dav", srv.URL+"/dav", 1, credPath, 5*time.Second))
	for _, leak := range []string{"example-only-not-a-real-secret", credPath, srv.URL, "vshm_monitor"} {
		if strings.Contains(res.Reason, leak) {
			t.Errorf("reason leaks %q: %q", leak, res.Reason)
		}
	}
	res2 := c.Run(context.Background(), NewConfig("dav", "http://user:hunter2@"+strings.TrimPrefix(srv.URL, "http://")+"/dav", 1, "", 5*time.Second))
	if res2.Success {
		t.Fatal("expected failure on 500")
	}
	for _, leak := range []string{"hunter2", "user:hunter2"} {
		if strings.Contains(res2.Reason, leak) {
			t.Errorf("reason leaks URL userinfo %q: %q", leak, res2.Reason)
		}
	}
}

func TestNeverSendsModifyingMethod(t *testing.T) {
	// The fake server records every method; across all runs in this package it
	// must only ever see PROPFIND.
	var mu sync.Mutex
	methods := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		methods[r.Method]++
		mu.Unlock()
		w.WriteHeader(http.StatusMultiStatus)
		fmt.Fprint(w, validMultistatus)
	}))
	t.Cleanup(srv.Close)
	c := New(testLogger())
	for _, depth := range []int{0, 1} {
		if res := c.Run(context.Background(), NewConfig("dav", srv.URL, depth, "", 5*time.Second)); !res.Success {
			t.Fatalf("depth %d: %+v", depth, res)
		}
	}
	for m := range methods {
		if m != "PROPFIND" {
			t.Errorf("server saw non-PROPFIND method %q — target must never be modified", m)
		}
	}
}
