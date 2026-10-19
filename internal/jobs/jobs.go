// Package jobs runs long filesystem operations in the background with a
// bounded number of workers, tracking progress and supporting cancellation.
// State is kept in memory only.
package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/alanaktion/xwebfm/internal/fsops"
)

// State is a job's lifecycle state.
type State string

const (
	Queued    State = "queued"
	Running   State = "running"
	Done      State = "done"
	Failed    State = "failed" // finished, but at least one item failed
	Cancelled State = "cancelled"
)

func (s State) finished() bool { return s == Done || s == Failed || s == Cancelled }

// ErrNotFound is returned for unknown job IDs.
var ErrNotFound = errors.New("job not found")

// ErrState is returned when an action doesn't apply to a job's state, such
// as cancelling a finished job.
var ErrState = errors.New("job is not in a state that allows this")

// Item is one source (and destination, for copy/move) within a job.
type Item struct {
	Src string `json:"src"`
	Dst string `json:"dst,omitempty"`
}

// Job is a snapshot of a job, safe to serialise.
type Job struct {
	ID       string                 `json:"id"`
	Kind     string                 `json:"kind"`
	Title    string                 `json:"title"`
	State    State                  `json:"state"`
	Items    []Item                 `json:"items"`
	Created  time.Time              `json:"created"`
	Started  *time.Time             `json:"started,omitempty"`
	Finished *time.Time             `json:"finished,omitempty"`
	Progress fsops.ProgressSnapshot `json:"progress"`
	Errors   []string               `json:"errors,omitempty"`
	// Dirs are the directories the job changes, so clients can refresh
	// views of them when it finishes.
	Dirs []string `json:"dirs,omitempty"`
}

// Spec describes a job to submit. Run performs the work, reporting through
// the Progress, and returns any per-item errors; it must return promptly
// once ctx is cancelled.
type Spec struct {
	Kind  string
	Title string
	Items []Item
	Dirs  []string
	Run   func(ctx context.Context, p *fsops.Progress) []error
}

// Event is pushed to subscribers. (The WebSocket endpoint first sends a
// {"type":"snapshot","jobs":[...]} message from Subscribe's snapshot.)
type Event struct {
	Type string   `json:"type"` // "job", "removed" or "changed"
	Job  *Job     `json:"job,omitempty"`
	ID   string   `json:"id,omitempty"`
	Dirs []string `json:"dirs,omitempty"`
}

type entry struct {
	job    Job
	spec   Spec
	prog   *fsops.Progress
	cancel context.CancelFunc // set while running
}

// Manager owns the queue and workers.
type Manager struct {
	workers int
	keep    int // finished jobs retained
	log     *slog.Logger

	mu      sync.Mutex
	cond    *sync.Cond
	jobs    map[string]*entry
	order   []string // all jobs, oldest first
	queue   []*entry
	subs    map[chan Event]struct{}
	stopped bool

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// SubscriberBuffer is how many events a subscriber may fall behind by
// before it is dropped (and expected to reconnect for a fresh snapshot).
const SubscriberBuffer = 256

// ProgressInterval is how often progress of running jobs is published.
var ProgressInterval = 250 * time.Millisecond

// New returns a Manager running up to workers jobs at once.
func New(workers int, log *slog.Logger) *Manager {
	if workers < 1 {
		workers = 1
	}
	m := &Manager{
		workers: workers,
		keep:    100,
		log:     log,
		jobs:    map[string]*entry{},
		subs:    map[chan Event]struct{}{},
	}
	m.cond = sync.NewCond(&m.mu)
	return m
}

// Start launches the workers and the progress publisher.
func (m *Manager) Start() {
	m.ctx, m.cancel = context.WithCancel(context.Background())
	for range m.workers {
		m.wg.Add(1)
		go m.worker()
	}
	m.wg.Add(1)
	go m.ticker()
}

// Stop cancels running jobs, waits for workers to exit and closes all
// subscriptions. Queued jobs are marked cancelled.
func (m *Manager) Stop() {
	m.mu.Lock()
	m.stopped = true
	for _, e := range m.queue {
		m.finishLocked(e, Cancelled, nil)
	}
	m.queue = nil
	m.cond.Broadcast()
	m.mu.Unlock()
	m.cancel()
	m.wg.Wait()

	m.mu.Lock()
	for ch := range m.subs {
		close(ch)
		delete(m.subs, ch)
	}
	m.mu.Unlock()
}

// Submit queues a job and returns its snapshot.
func (m *Manager) Submit(s Spec) Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := &entry{
		spec: s,
		prog: &fsops.Progress{},
		job: Job{
			ID:      newID(),
			Kind:    s.Kind,
			Title:   s.Title,
			State:   Queued,
			Items:   s.Items,
			Created: time.Now(),
			Dirs:    s.Dirs,
		},
	}
	m.jobs[e.job.ID] = e
	m.order = append(m.order, e.job.ID)
	if m.stopped {
		m.finishLocked(e, Cancelled, nil)
		return e.job
	}
	m.queue = append(m.queue, e)
	m.publishJobLocked(e)
	m.cond.Signal()
	return e.job
}

// Get returns one job.
func (m *Manager) Get(id string) (Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.jobs[id]
	if !ok {
		return Job{}, ErrNotFound
	}
	return e.job, nil
}

// List returns all jobs, oldest first.
func (m *Manager) List() []Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.listLocked()
}

func (m *Manager) listLocked() []Job {
	out := make([]Job, 0, len(m.order))
	for _, id := range m.order {
		out = append(out, m.jobs[id].job)
	}
	return out
}

// Cancel stops a queued or running job. A running job finishes in the
// cancelled state once its current step returns.
func (m *Manager) Cancel(id string) (Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.jobs[id]
	if !ok {
		return Job{}, ErrNotFound
	}
	switch e.job.State {
	case Queued:
		m.queue = slices.DeleteFunc(m.queue, func(q *entry) bool { return q == e })
		m.finishLocked(e, Cancelled, nil)
	case Running:
		e.cancel()
	default:
		return e.job, ErrState
	}
	return e.job, nil
}

// Dismiss removes a finished job from the list.
func (m *Manager) Dismiss(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.jobs[id]
	if !ok {
		return ErrNotFound
	}
	if !e.job.State.finished() {
		return ErrState
	}
	m.removeLocked(id)
	return nil
}

// ClearFinished removes all finished jobs.
func (m *Manager) ClearFinished() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range slices.Clone(m.order) {
		if m.jobs[id].job.State.finished() {
			m.removeLocked(id)
		}
	}
}

// Notify tells subscribers that the given directories changed outside a
// job (e.g. a synchronous rename).
func (m *Manager) Notify(dirs ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.publishLocked(Event{Type: "changed", Dirs: dirs})
}

// Subscribe returns a snapshot of all jobs and a channel of subsequent
// events. The channel is closed if the subscriber falls too far behind or
// the manager stops; call the returned function to unsubscribe.
func (m *Manager) Subscribe() ([]Job, <-chan Event, func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ch := make(chan Event, SubscriberBuffer)
	if m.stopped {
		close(ch)
	} else {
		m.subs[ch] = struct{}{}
	}
	return m.listLocked(), ch, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if _, ok := m.subs[ch]; ok {
			delete(m.subs, ch)
			close(ch)
		}
	}
}

func (m *Manager) worker() {
	defer m.wg.Done()
	for {
		m.mu.Lock()
		for len(m.queue) == 0 && !m.stopped {
			m.cond.Wait()
		}
		if m.stopped {
			m.mu.Unlock()
			return
		}
		e := m.queue[0]
		m.queue = m.queue[1:]
		ctx, cancel := context.WithCancel(m.ctx)
		e.cancel = cancel
		now := time.Now()
		e.job.State, e.job.Started = Running, &now
		m.publishJobLocked(e)
		m.mu.Unlock()

		errs := m.run(ctx, e)

		m.mu.Lock()
		state := Done
		switch {
		case ctx.Err() != nil:
			state = Cancelled
		case len(errs) > 0:
			state = Failed
		}
		cancel()
		m.finishLocked(e, state, errs)
		m.mu.Unlock()
	}
}

// run calls the job's Run function, converting a panic into an error so one
// bad job can't take down the server.
func (m *Manager) run(ctx context.Context, e *entry) (errs []error) {
	defer func() {
		if v := recover(); v != nil {
			m.log.Error("job panicked", "id", e.job.ID, "kind", e.job.Kind, "panic", v)
			errs = append(errs, fmt.Errorf("internal error: %v", v))
		}
	}()
	return e.spec.Run(ctx, e.prog)
}

func (m *Manager) finishLocked(e *entry, state State, errs []error) {
	now := time.Now()
	e.job.State, e.job.Finished = state, &now
	e.job.Progress = e.prog.Snapshot()
	for _, err := range errs {
		if !errors.Is(err, context.Canceled) {
			e.job.Errors = append(e.job.Errors, err.Error())
		}
	}
	e.cancel = nil
	m.publishJobLocked(e)
	if state != Cancelled || e.job.Started != nil {
		m.publishLocked(Event{Type: "changed", Dirs: e.job.Dirs})
	}
	m.pruneLocked()
}

// pruneLocked drops the oldest finished jobs beyond the retention limit.
func (m *Manager) pruneLocked() {
	finished := 0
	for _, id := range m.order {
		if m.jobs[id].job.State.finished() {
			finished++
		}
	}
	for _, id := range slices.Clone(m.order) {
		if finished <= m.keep {
			break
		}
		if m.jobs[id].job.State.finished() {
			m.removeLocked(id)
			finished--
		}
	}
}

func (m *Manager) removeLocked(id string) {
	delete(m.jobs, id)
	m.order = slices.DeleteFunc(m.order, func(o string) bool { return o == id })
	m.publishLocked(Event{Type: "removed", ID: id})
}

// ticker publishes progress of running jobs whenever it changes.
func (m *Manager) ticker() {
	defer m.wg.Done()
	t := time.NewTicker(ProgressInterval)
	defer t.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-t.C:
		}
		m.mu.Lock()
		for _, id := range m.order {
			e := m.jobs[id]
			if e.job.State != Running {
				continue
			}
			if snap := e.prog.Snapshot(); snap != e.job.Progress {
				e.job.Progress = snap
				m.publishJobLocked(e)
			}
		}
		m.mu.Unlock()
	}
}

func (m *Manager) publishJobLocked(e *entry) {
	j := e.job
	m.publishLocked(Event{Type: "job", Job: &j})
}

func (m *Manager) publishLocked(ev Event) {
	for ch := range m.subs {
		select {
		case ch <- ev:
		default:
			// Too far behind: drop it rather than block the manager.
			delete(m.subs, ch)
			close(ch)
		}
	}
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}
