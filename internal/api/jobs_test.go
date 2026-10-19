//go:build linux

package api

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
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

// waitJob polls the job list until id finishes.
func waitJob(t *testing.T, url, id string) jobs.Job {
	t.Helper()
	for range 1000 {
		var list []jobs.Job
		doJSON(t, "GET", url+"/api/v1/jobs", "", &list)
		for _, j := range list {
			if j.ID == id && j.Finished != nil {
				return j
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("job %s did not finish", id)
	return jobs.Job{}
}

func TestArchiveJobs(t *testing.T) {
	ts, dir := newTestServer(t)
	os.MkdirAll(filepath.Join(dir, "src/sub"), 0o755)
	os.WriteFile(filepath.Join(dir, "src/a.txt"), []byte("alpha"), 0o644)
	os.WriteFile(filepath.Join(dir, "src/sub/b.txt"), []byte("bravo"), 0o644)
	os.Mkdir(filepath.Join(dir, "out"), 0o755)

	var info infoResponse
	doJSON(t, "GET", ts.URL+"/api/v1/info", "", &info)
	if len(info.Archive.Create) < 5 || info.Archive.Create[0] != "zip" {
		t.Errorf("info %+v", info)
	}

	var j jobs.Job
	code := doJSON(t, "POST", ts.URL+"/api/v1/jobs",
		`{"kind":"archive","paths":["/src/a.txt","/src/sub"],"dst":"/out/both.tar.gz","format":"tar.gz","deleteOriginals":true}`, &j)
	if code != http.StatusAccepted || j.Title != "Archive 2 items → /out/both.tar.gz" {
		t.Fatalf("archive submit: %d %+v", code, j)
	}
	if j = waitJob(t, ts.URL, j.ID); j.State != jobs.Done {
		t.Fatalf("archive job: %+v", j)
	}
	if _, err := os.Stat(filepath.Join(dir, "src/a.txt")); !os.IsNotExist(err) {
		t.Errorf("originals not deleted: %v", err)
	}

	code = doJSON(t, "POST", ts.URL+"/api/v1/jobs",
		`{"kind":"extract","items":[{"src":"/out/both.tar.gz","dst":"/out"}],"password":"hunter2","deleteOriginals":true}`, &j)
	if code != http.StatusAccepted {
		t.Fatalf("extract submit: %d", code)
	}
	j = waitJob(t, ts.URL, j.ID)
	if j.State != jobs.Done || j.Progress.Entries != 3 {
		t.Fatalf("extract job: %+v", j)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "out/both/sub/b.txt")); string(got) != "bravo" {
		t.Errorf("extracted b.txt = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "out/both.tar.gz")); !os.IsNotExist(err) {
		t.Errorf("archive not deleted: %v", err)
	}

	res, _ := http.Get(ts.URL + "/api/v1/jobs")
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if strings.Contains(string(body), "hunter2") {
		t.Error("password leaked into the job list")
	}

	for _, body := range []string{
		`{"kind":"extract","items":[{"src":"/out/notes.txt","dst":"/out"}]}`,
		`{"kind":"archive","paths":["/out/both"],"dst":"/x.rar","format":"rar"}`,
		`{"kind":"archive","paths":["/out/both"],"format":"zip"}`,
	} {
		if code := doJSON(t, "POST", ts.URL+"/api/v1/jobs", body, nil); code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", body, code)
		}
	}
}

func TestUsageAndFindJobs(t *testing.T) {
	ts, dir := newTestServer(t)
	os.MkdirAll(filepath.Join(dir, "d/sub"), 0o755)
	os.WriteFile(filepath.Join(dir, "d/sub/big.iso"), make([]byte, 4096), 0o644)
	os.WriteFile(filepath.Join(dir, "d/notes.txt"), []byte("x"), 0o644)

	var j jobs.Job
	if code := doJSON(t, "POST", ts.URL+"/api/v1/jobs", `{"kind":"usage","paths":["/d"]}`, &j); code != http.StatusAccepted {
		t.Fatalf("usage submit %d", code)
	}
	if j = waitJob(t, ts.URL, j.ID); j.State != jobs.Done || !j.HasResult {
		t.Fatalf("usage job %+v", j)
	}
	var rep struct {
		Size     int64 `json:"size"`
		Children []struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
		} `json:"children"`
	}
	doJSON(t, "GET", ts.URL+"/api/v1/jobs/"+j.ID+"/result", "", &rep)
	if rep.Size != 4097 || len(rep.Children) != 2 {
		t.Errorf("usage report %+v", rep)
	}

	doJSON(t, "POST", ts.URL+"/api/v1/jobs", `{"kind":"find","paths":["/d"],"query":{"name":"*.iso"}}`, &j)
	j = waitJob(t, ts.URL, j.ID)
	var found struct {
		Entries []struct {
			Path string `json:"path"`
		} `json:"entries"`
	}
	doJSON(t, "GET", ts.URL+"/api/v1/jobs/"+j.ID+"/result", "", &found)
	if len(found.Entries) != 1 || found.Entries[0].Path != "/d/sub/big.iso" {
		t.Errorf("find result %+v", found)
	}
	if code := doJSON(t, "POST", ts.URL+"/api/v1/jobs", `{"kind":"find","paths":["/d"],"query":{"name":"["}}`, nil); code != http.StatusBadRequest {
		t.Errorf("bad pattern: %d", code)
	}

	var lr listResponse
	doJSON(t, "GET", ts.URL+"/api/v1/fs/list?path=/d", "", &lr)
	if lr.Dir == nil || lr.Dir.Path != "/d" || lr.FS == nil || lr.FS.Total == 0 {
		t.Errorf("list extras: dir=%+v fs=%+v", lr.Dir, lr.FS)
	}
}

func TestPermissionEndpoints(t *testing.T) {
	ts, dir := newTestServer(t)
	os.MkdirAll(filepath.Join(dir, "home/bob/sub"), 0o700)
	os.WriteFile(filepath.Join(dir, "home/bob/sub/f"), nil, 0o600)
	if os.Geteuid() == 0 {
		os.Chown(filepath.Join(dir, "home/bob"), 1001, 1001)
	}

	// chmod job with separate folder/file modes.
	var j jobs.Job
	doJSON(t, "POST", ts.URL+"/api/v1/jobs", `{"kind":"chmod","paths":["/home/bob/sub"],"dirMode":"755","fileMode":"644","recursive":true}`, &j)
	if j = waitJob(t, ts.URL, j.ID); j.State != jobs.Done {
		t.Fatalf("chmod job %+v", j)
	}
	if st, _ := os.Stat(filepath.Join(dir, "home/bob/sub/f")); st.Mode().Perm() != 0o644 {
		t.Errorf("file mode %v", st.Mode())
	}
	if code := doJSON(t, "POST", ts.URL+"/api/v1/jobs", `{"kind":"chmod","paths":["/home"],"dirMode":"755"}`, nil); code != http.StatusBadRequest {
		t.Errorf("dirMode without fileMode: %d", code)
	}

	if os.Geteuid() == 0 {
		doJSON(t, "POST", ts.URL+"/api/v1/jobs", `{"kind":"fixowner","paths":["/home/bob"],"match":"self"}`, &j)
		if j = waitJob(t, ts.URL, j.ID); j.State != jobs.Done {
			t.Fatalf("fixowner job %+v", j)
		}
		var e struct{ UID uint32 }
		doJSON(t, "GET", ts.URL+"/api/v1/fs/stat?path=/home/bob/sub/f", "", &e)
		if e.UID != 1001 {
			t.Errorf("fixowner left uid %d", e.UID)
		}
	}

	var props propsResponse
	code := doJSON(t, "POST", ts.URL+"/api/v1/fs/acl", `{"path":"/home/bob/sub","acl":{"named":[{"kind":"user","id":1000,"perm":5}]}}`, &props)
	if code != 200 || props.ACL == nil || len(props.ACL.Named) != 1 || !props.Entry.ACL {
		t.Errorf("acl: %d %+v", code, props)
	}
	if code := doJSON(t, "POST", ts.URL+"/api/v1/fs/link", `{"target":"/home/bob/sub/f","path":"/home/bob/f-link","kind":"symlink","relative":true}`, nil); code != 200 {
		t.Errorf("link: %d", code)
	}
	if target, _ := os.Readlink(filepath.Join(dir, "home/bob/f-link")); target != "sub/f" {
		t.Errorf("link target %q", target)
	}
}

func TestViewAndEdit(t *testing.T) {
	ts, dir := newTestServer(t)
	os.WriteFile(filepath.Join(dir, "evil.html"), []byte("<script>alert(1)</script>"), 0o644)
	os.WriteFile(filepath.Join(dir, "pic.png"), []byte("\x89PNG"), 0o644)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hello"), 0o644)

	for name, want := range map[string]string{"evil.html": "text/plain; charset=utf-8", "pic.png": "image/png"} {
		res, err := http.Get(ts.URL + "/api/v1/fs/download?inline=1&path=/" + name)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if got := res.Header.Get("Content-Type"); got != want {
			t.Errorf("%s: content type %q, want %q", name, got, want)
		}
		if res.Header.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(res.Header.Get("Content-Security-Policy"), "sandbox") {
			t.Errorf("%s: missing safety headers %v", name, res.Header)
		}
		if !strings.HasPrefix(res.Header.Get("Content-Disposition"), "inline") {
			t.Errorf("%s: disposition %q", name, res.Header.Get("Content-Disposition"))
		}
	}

	var e struct {
		Size  int64     `json:"size"`
		Mtime time.Time `json:"mtime"`
	}
	doJSON(t, "GET", ts.URL+"/api/v1/fs/stat?path=/notes.txt", "", &e)
	body := `{"path":"/notes.txt","content":"edited","expectSize":` + strconv.FormatInt(e.Size, 10) + `,"expectMtime":"` + e.Mtime.Format(time.RFC3339) + `"}`
	if code := doJSON(t, "POST", ts.URL+"/api/v1/fs/write", body, nil); code != 200 {
		t.Fatalf("write: %d", code)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "notes.txt")); string(got) != "edited" {
		t.Errorf("contents %q", got)
	}
	// The same expectations are now stale.
	if code := doJSON(t, "POST", ts.URL+"/api/v1/fs/write", body, nil); code != http.StatusConflict {
		t.Errorf("stale write: %d", code)
	}
}
