//go:build linux

package api

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/alanaktion/xwebfm/internal/jobs"
)

func dialWS(t *testing.T, url string) (*websocket.Conn, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(url, "http")+"/api/v1/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.CloseNow() })
	return c, ctx
}

func TestJobOverWebSocket(t *testing.T) {
	ts, dir := newTestServer(t)
	os.MkdirAll(filepath.Join(dir, "src/sub"), 0o755)
	os.WriteFile(filepath.Join(dir, "src/sub/f"), []byte("12345"), 0o644)

	c, ctx := dialWS(t, ts.URL)
	var snap struct {
		Type string     `json:"type"`
		Jobs []jobs.Job `json:"jobs"`
	}
	if err := wsjson.Read(ctx, c, &snap); err != nil || snap.Type != "snapshot" || snap.Jobs == nil {
		t.Fatalf("first message: %+v %v", snap, err)
	}

	var j jobs.Job
	code := doJSON(t, "POST", ts.URL+"/api/v1/jobs",
		`{"kind":"copy","items":[{"src":"/src","dst":"/dst"}],"reflink":"never"}`, &j)
	if code != http.StatusAccepted || j.State != jobs.Queued || j.Title != "Copy src → /dst" {
		t.Fatalf("submit: %d %+v", code, j)
	}

	sawChanged := false
	for !sawChanged {
		var ev jobs.Event
		if err := wsjson.Read(ctx, c, &ev); err != nil {
			t.Fatal(err)
		}
		switch ev.Type {
		case "job":
			if ev.Job.State == jobs.Done {
				j = *ev.Job
			}
		case "changed":
			sawChanged = true
		}
	}
	if j.State != jobs.Done || j.Progress.Bytes != 5 || j.Progress.TotalBytes != 5 || j.Progress.Entries != 3 {
		t.Errorf("finished job: %+v", j)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "dst/sub/f")); string(got) != "12345" {
		t.Errorf("copied contents %q", got)
	}

	var list []jobs.Job
	if doJSON(t, "GET", ts.URL+"/api/v1/jobs", "", &list); len(list) != 1 {
		t.Errorf("list: %+v", list)
	}
	if code := doJSON(t, "POST", ts.URL+"/api/v1/jobs/"+j.ID+"/cancel", `{}`, nil); code != http.StatusConflict {
		t.Errorf("cancel finished job: %d", code)
	}
	if code := doJSON(t, "POST", ts.URL+"/api/v1/jobs/"+j.ID+"/dismiss", `{}`, nil); code != http.StatusNoContent {
		t.Errorf("dismiss: %d", code)
	}
	if code := doJSON(t, "POST", ts.URL+"/api/v1/jobs/nope/dismiss", `{}`, nil); code != http.StatusNotFound {
		t.Errorf("dismiss unknown: %d", code)
	}
}

func TestJobKinds(t *testing.T) {
	ts, dir := newTestServer(t)
	os.MkdirAll(filepath.Join(dir, "a/b"), 0o755)
	os.WriteFile(filepath.Join(dir, "a/b/f"), nil, 0o644)

	wait := func(body string) jobs.Job {
		t.Helper()
		var j jobs.Job
		if code := doJSON(t, "POST", ts.URL+"/api/v1/jobs", body, &j); code != http.StatusAccepted {
			t.Fatalf("%s: status %d", body, code)
		}
		for range 500 {
			var list []jobs.Job
			doJSON(t, "GET", ts.URL+"/api/v1/jobs", "", &list)
			for _, l := range list {
				if l.ID == j.ID && l.Finished != nil {
					return l
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("%s: did not finish", body)
		return j
	}

	if j := wait(`{"kind":"chmod","paths":["/a"],"mode":"700","recursive":true}`); j.State != jobs.Done || j.Progress.Entries != 3 {
		t.Errorf("chmod: %+v", j)
	}
	if st, _ := os.Stat(filepath.Join(dir, "a/b/f")); st.Mode().Perm() != 0o700 {
		t.Errorf("chmod not recursive: %v", st.Mode())
	}
	if j := wait(`{"kind":"move","items":[{"src":"/a","dst":"/moved"}]}`); j.State != jobs.Done {
		t.Errorf("move: %+v", j)
	}
	if j := wait(`{"kind":"delete","paths":["/moved","/missing"]}`); j.State != jobs.Failed || len(j.Errors) != 1 {
		t.Errorf("delete with one missing path: %+v", j)
	}
	if _, err := os.Stat(filepath.Join(dir, "moved")); !os.IsNotExist(err) {
		t.Errorf("delete: %v", err)
	}
}

func TestJobValidation(t *testing.T) {
	ts, _ := newTestServer(t)
	for _, body := range []string{
		`{"kind":"explode","paths":["/"]}`,
		`{"kind":"copy","items":[]}`,
		`{"kind":"copy","items":[{"src":"/a","dst":"/b"}],"reflink":"sometimes"}`,
		`{"kind":"move","items":[{"src":"/a","dst":"/b"}],"owner":"set"}`,
		`{"kind":"chmod","paths":["/a"],"mode":"9"}`,
		`{"kind":"chown","paths":["/a"]}`,
		`{"kind":"chown","paths":["/a"],"user":"no-such-user-xyz"}`,
	} {
		if code := doJSON(t, "POST", ts.URL+"/api/v1/jobs", body, nil); code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", body, code)
		}
	}
}

func TestWebSocketRejectsCrossOrigin(t *testing.T) {
	ts, _ := newTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	h := http.Header{"Origin": []string{"https://evil.example"}}
	_, res, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/api/v1/ws", &websocket.DialOptions{HTTPHeader: h})
	if err == nil || res == nil || res.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin dial: err=%v res=%v", err, res)
	}
}

func TestSyncOpsNotify(t *testing.T) {
	ts, dir := newTestServer(t)
	os.WriteFile(filepath.Join(dir, "a"), nil, 0o644)
	c, ctx := dialWS(t, ts.URL)
	var ev jobs.Event
	wsjson.Read(ctx, c, &ev) // snapshot
	doJSON(t, "POST", ts.URL+"/api/v1/fs/rename", `{"path":"/a","name":"b"}`, nil)
	if err := wsjson.Read(ctx, c, &ev); err != nil || ev.Type != "changed" || len(ev.Dirs) != 1 || ev.Dirs[0] != "/" {
		t.Errorf("event after rename: %+v %v", ev, err)
	}
}
