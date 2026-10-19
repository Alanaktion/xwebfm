//go:build linux

package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alanaktion/xwebfm/internal/fsops"
	"github.com/alanaktion/xwebfm/internal/jobs"
)

func newTestServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	f, err := fsops.New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := jobs.New(1, log)
	m.Start()
	s := &Server{FS: f, Jobs: m, Log: log}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() {
		m.Stop() // ends WebSocket handlers before the server waits for them
		ts.Close()
	})
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
	if code := doJSON(t, "POST", ts.URL+"/api/v1/fs/copy", `{"src":"/a","dst":"/b"}`, &cr); code != 200 {
		t.Fatalf("copy: %d", code)
	}
	if cr.Bytes != 3 || cr.Entry.Mode != 0o600 || cr.Entry.Path != "/b" {
		t.Errorf("copy result %+v", cr)
	}
	if code := doJSON(t, "POST", ts.URL+"/api/v1/fs/copy", `{"src":"/a","dst":"/b"}`, &eb); code != 409 {
		t.Errorf("copy conflict: %d", code)
	}
	if code := doJSON(t, "POST", ts.URL+"/api/v1/fs/copy", `{"src":"/a","dst":"/c","owner":"bogus"}`, &eb); code != 400 {
		t.Errorf("bad owner policy: %d", code)
	}
}

func TestFileManagement(t *testing.T) {
	ts, dir := newTestServer(t)
	os.WriteFile(filepath.Join(dir, "a"), []byte("hello world"), 0o644)

	var e fsops.Entry
	if code := doJSON(t, "POST", ts.URL+"/api/v1/fs/mkdir", `{"path":"/d"}`, &e); code != 200 || e.Type != "dir" {
		t.Fatalf("mkdir: %d %+v", code, e)
	}
	var mr moveResponse
	if code := doJSON(t, "POST", ts.URL+"/api/v1/fs/move", `{"src":"/a","dst":"/d/a"}`, &mr); code != 200 || mr.Entry.Path != "/d/a" {
		t.Fatalf("move: %d %+v", code, mr)
	}
	if code := doJSON(t, "POST", ts.URL+"/api/v1/fs/rename", `{"path":"/d/a","name":"b"}`, &e); code != 200 || e.Path != "/d/b" {
		t.Fatalf("rename: %d %+v", code, e)
	}

	res, err := http.Get(ts.URL + "/api/v1/fs/download?path=/d/b")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if string(body) != "hello world" || !strings.Contains(res.Header.Get("Content-Disposition"), `filename=b`) {
		t.Errorf("download: %q %v", body, res.Header)
	}
	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/fs/download?path=/d/b", nil)
	req.Header.Set("Range", "bytes=6-")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 206 || string(body) != "world" {
		t.Errorf("range download: %d %q", res.StatusCode, body)
	}
	res, err = http.Get(ts.URL + "/api/v1/fs/download?path=/d")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.Header.Get("Content-Type") != "application/gzip" {
		t.Errorf("dir download content type %q", res.Header.Get("Content-Type"))
	}

	if code := doJSON(t, "POST", ts.URL+"/api/v1/fs/delete", `{"path":"/d"}`, nil); code != 204 {
		t.Errorf("delete: %d", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "d")); !os.IsNotExist(err) {
		t.Errorf("delete left %v", err)
	}
}

func TestUpload(t *testing.T) {
	ts, dir := newTestServer(t)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "up.txt")
	fw.Write([]byte("uploaded"))
	mw.Close()

	post := func(header bool) *http.Response {
		req, _ := http.NewRequest("POST", ts.URL+"/api/v1/fs/upload?dir=/", bytes.NewReader(buf.Bytes()))
		req.Header.Set("Content-Type", mw.FormDataContentType())
		if header {
			req.Header.Set("X-Requested-With", "xwebfm")
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	if res := post(false); res.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("upload without CSRF header: %d", res.StatusCode)
	}
	res := post(true)
	var entries []fsops.Entry
	json.NewDecoder(res.Body).Decode(&entries)
	res.Body.Close()
	if res.StatusCode != 200 || len(entries) != 1 || entries[0].Path != "/up.txt" {
		t.Fatalf("upload: %d %+v", res.StatusCode, entries)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "up.txt")); string(got) != "uploaded" {
		t.Errorf("uploaded contents %q", got)
	}
	if res := post(true); res.StatusCode != 409 {
		t.Errorf("upload conflict: %d", res.StatusCode)
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
