package jobs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alanaktion/xwebfm/internal/fsops"
)

func newManager(t *testing.T, workers int) *Manager {
	t.Helper()
	m := New(workers, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.Start()
	t.Cleanup(m.Stop)
	return m
}

// waitState polls until job id reaches state.
func waitState(t *testing.T, m *Manager, id string, state State) Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		j, err := m.Get(id)
		if err == nil && j.State == state {
			return j
		}
		time.Sleep(time.Millisecond)
	}
	j, _ := m.Get(id)
	t.Fatalf("job %s: state %s, want %s", id, j.State, state)
	return j
}

// blocker returns a Run func that blocks until released or cancelled.
func blocker(started *atomic.Int32, release <-chan struct{}) func(context.Context, *fsops.Progress) []error {
	return func(ctx context.Context, p *fsops.Progress) []error {
		started.Add(1)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return []error{ctx.Err()}
		}
	}
}

func TestWorkerLimitAndFIFO(t *testing.T) {
	m := newManager(t, 2)
	var started atomic.Int32
	release := make(chan struct{})
	var ids []string
	for range 3 {
		ids = append(ids, m.Submit(Spec{Kind: "test", Run: blocker(&started, release)}).ID)
	}
	waitState(t, m, ids[0], Running)
	waitState(t, m, ids[1], Running)
	time.Sleep(20 * time.Millisecond)
	if started.Load() != 2 {
		t.Fatalf("%d jobs running, want 2", started.Load())
	}
	if j, _ := m.Get(ids[2]); j.State != Queued {
		t.Fatalf("third job %s, want queued", j.State)
	}
	close(release)
	for _, id := range ids {
		j := waitState(t, m, id, Done)
		if j.Started == nil || j.Finished == nil {
			t.Errorf("timestamps missing: %+v", j)
		}
	}
}

func TestCancel(t *testing.T) {
	m := newManager(t, 1)
	var started atomic.Int32
	release := make(chan struct{})
	defer close(release)
	running := m.Submit(Spec{Kind: "test", Run: blocker(&started, release)})
	queued := m.Submit(Spec{Kind: "test", Run: blocker(&started, release)})
	waitState(t, m, running.ID, Running)

	if _, err := m.Cancel(queued.ID); err != nil {
		t.Fatal(err)
	}
	if j, _ := m.Get(queued.ID); j.State != Cancelled || j.Started != nil {
		t.Errorf("queued job after cancel: %+v", j)
	}
	if _, err := m.Cancel(running.ID); err != nil {
		t.Fatal(err)
	}
	j := waitState(t, m, running.ID, Cancelled)
	if len(j.Errors) != 0 {
		t.Errorf("context.Canceled reported as an error: %v", j.Errors)
	}
	if _, err := m.Cancel(running.ID); !errors.Is(err, ErrState) {
		t.Errorf("cancel finished job: %v", err)
	}
	if _, err := m.Cancel("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("cancel unknown job: %v", err)
	}
	if started.Load() != 1 {
		t.Errorf("cancelled queued job ran")
	}
}

func TestFailuresAndPanics(t *testing.T) {
	m := newManager(t, 1)
	failed := m.Submit(Spec{Kind: "test", Run: func(context.Context, *fsops.Progress) []error {
		return []error{errors.New("item 2 broke")}
	}})
	j := waitState(t, m, failed.ID, Failed)
	if len(j.Errors) != 1 || j.Errors[0] != "item 2 broke" {
		t.Errorf("errors %v", j.Errors)
	}
	panicked := m.Submit(Spec{Kind: "test", Run: func(context.Context, *fsops.Progress) []error { panic("boom") }})
	waitState(t, m, panicked.ID, Failed)
	ok := m.Submit(Spec{Kind: "test", Run: func(context.Context, *fsops.Progress) []error { return nil }})
	waitState(t, m, ok.ID, Done) // the worker survived the panic
}

func TestSubscribe(t *testing.T) {
	old := ProgressInterval
	ProgressInterval = 5 * time.Millisecond
	defer func() { ProgressInterval = old }()
	m := newManager(t, 1)

	snap, events, unsubscribe := m.Subscribe()
	defer unsubscribe()
	if len(snap) != 0 {
		t.Fatalf("snapshot %v", snap)
	}
	step := make(chan struct{})
	job := m.Submit(Spec{Kind: "copy", Dirs: []string{"/dst"}, Run: func(ctx context.Context, p *fsops.Progress) []error {
		p.AddTotal(10, 100)
		<-step
		return nil
	}})

	var states []State
	sawProgress, sawChanged := false, false
	timeout := time.After(5 * time.Second)
	for !sawChanged {
		select {
		case ev := <-events:
			switch ev.Type {
			case "job":
				if len(states) == 0 || states[len(states)-1] != ev.Job.State {
					states = append(states, ev.Job.State)
				}
				if ev.Job.State == Running && ev.Job.Progress.TotalBytes == 100 && !sawProgress {
					sawProgress = true
					close(step)
				}
			case "changed":
				if len(ev.Dirs) != 1 || ev.Dirs[0] != "/dst" {
					t.Errorf("changed dirs %v", ev.Dirs)
				}
				sawChanged = true
			}
		case <-timeout:
			t.Fatalf("timed out; states so far %v", states)
		}
	}
	if want := []State{Queued, Running, Done}; !slices.Equal(states, want) {
		t.Errorf("states %v, want %v", states, want)
	}

	if err := m.Dismiss(job.ID); err != nil {
		t.Fatal(err)
	}
	if ev := <-events; ev.Type != "removed" || ev.ID != job.ID {
		t.Errorf("event after dismiss: %+v", ev)
	}
	if len(m.List()) != 0 {
		t.Errorf("list after dismiss: %v", m.List())
	}
}

func TestSlowSubscriberDropped(t *testing.T) {
	m := newManager(t, 1)
	_, events, unsubscribe := m.Subscribe()
	defer unsubscribe()
	for range SubscriberBuffer + 1 {
		m.Notify("/x")
	}
	n := 0
	for range events {
		n++
	}
	if n != SubscriberBuffer {
		t.Errorf("received %d events before close, want %d", n, SubscriberBuffer)
	}
}

func TestClearFinishedAndStop(t *testing.T) {
	m := New(1, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.Start()
	done := m.Submit(Spec{Kind: "test", Run: func(context.Context, *fsops.Progress) []error { return nil }})
	waitState(t, m, done.ID, Done)
	var started atomic.Int32
	running := m.Submit(Spec{Kind: "test", Run: blocker(&started, nil)})
	waitState(t, m, running.ID, Running)
	queued := m.Submit(Spec{Kind: "test", Run: blocker(&started, nil)})

	m.ClearFinished()
	if jobs := m.List(); len(jobs) != 2 {
		t.Errorf("after clear: %v", jobs)
	}
	_, events, _ := m.Subscribe()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range events {
		}
	}()
	m.Stop()
	wg.Wait() // subscription closed by Stop
	for _, id := range []string{running.ID, queued.ID} {
		if j, _ := m.Get(id); j.State != Cancelled {
			t.Errorf("%s after stop: %s", id, j.State)
		}
	}
}
