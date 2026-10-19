//go:build linux

package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alanaktion/xwebfm/internal/fsops"
)

func newTestServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	f, err := fsops.New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	s := &Server{FS: f, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, dir
}

func doJSON(t *testing.T, method, url, body string, out any) int {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if out != nil {
		if err := json.NewDecoder(res.Body).Decode(out); err != nil {
			t.Fatalf("%s %s: decode: %v", method, url, err)
		}
	}
	return res.StatusCode
}

func TestList(t *testing.T) {
	ts, dir := newTestServer(t)
	os.WriteFile(filepath.Join(dir, "a"), []byte("x"), 0o644)

	var lr listResponse
	if code := doJSON(t, "GET", ts.URL+"/api/v1/fs/list?path=../..", "", &lr); code != 200 {
		t.Fatalf("status %d", code)
	}
	if lr.Path != "/" || len(lr.Entries) != 1 || lr.Entries[0].Name != "a" {
		t.Errorf("unexpected listing %+v", lr)
	}

	var eb errorBody
	if code := doJSON(t, "GET", ts.URL+"/api/v1/fs/list?path=/nope", "", &eb); code != 404 || eb.Error.Code != "not_found" {
		t.Errorf("missing dir: %d %+v", code, eb)
	}
	if code := doJSON(t, "GET", ts.URL+"/api/v1/fs/list?path=/a", "", &eb); code != 400 {
		t.Errorf("list file: %d %+v", code, eb)
	}
}

func TestChmodAndCopy(t *testing.T) {
	ts, dir := newTestServer(t)
	os.WriteFile(filepath.Join(dir, "a"), []byte("xyz"), 0o644)

	var e fsops.Entry
	if code := doJSON(t, "POST", ts.URL+"/api/v1/fs/chmod", `{"path":"/a","mode":"600"}`, &e); code != 200 || e.Mode != 0o600 {
		t.Errorf("chmod: %d %+v", code, e)
	}
	var eb errorBody
	if code := doJSON(t, "POST", ts.URL+"/api/v1/fs/chmod", `{"path":"/a","mode":"999"}`, &eb); code != 400 {
		t.Errorf("bad mode: %d", code)
	}

	var cr copyResponse
	if code := doJSON(t, "POST", ts.URL+"/api/v1/fs/copy", `{"src":"/a","dst":"/b","preserve":true}`, &cr); code != 200 {
		t.Fatalf("copy: %d", code)
	}
	if cr.Bytes != 3 || cr.Entry.Mode != 0o600 || cr.Entry.Path != "/b" {
		t.Errorf("copy result %+v", cr)
	}
	if code := doJSON(t, "POST", ts.URL+"/api/v1/fs/copy", `{"src":"/a","dst":"/b"}`, &eb); code != 409 {
		t.Errorf("copy conflict: %d", code)
	}
}

func TestChownByName(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("chown requires root")
	}
	ts, dir := newTestServer(t)
	os.WriteFile(filepath.Join(dir, "a"), nil, 0o644)
	var e fsops.Entry
	if code := doJSON(t, "POST", ts.URL+"/api/v1/fs/chown", `{"path":"/a","uid":1234,"group":"root"}`, &e); code != 200 || e.UID != 1234 || e.GID != 0 {
		t.Errorf("chown: %d %+v", code, e)
	}
	var eb errorBody
	if code := doJSON(t, "POST", ts.URL+"/api/v1/fs/chown", `{"path":"/a","user":"no-such-user-xyz"}`, &eb); code != 400 {
		t.Errorf("unknown user: %d", code)
	}
}

func TestRequireJSON(t *testing.T) {
	ts, _ := newTestServer(t)
	res, err := http.Post(ts.URL+"/api/v1/fs/chmod", "text/plain", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("status %d", res.StatusCode)
	}
}
