package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

// makeAmbientEntries builds n deterministic ambient entries with distinct
// request ids so the server-side dedup key is unique per entry.
func makeAmbientEntries(n int) []AmbientUsageEntry {
	out := make([]AmbientUsageEntry, n)
	for i := 0; i < n; i++ {
		out[i] = AmbientUsageEntry{
			SessionID:   "S",
			MessageID:   fmt.Sprintf("m%d", i),
			RequestID:   fmt.Sprintf("r%d", i),
			Provider:    "claude",
			Model:       "claude-opus-4-7",
			EventAt:     "2026-06-15T01:30:00Z",
			InputTokens: 1,
			Source:      "claude",
		}
	}
	return out
}

// usageBatchRecorder is an httptest handler that records the size of each ambient
// usage POST body and optionally fails a chosen 1-based call index with 500.
type usageBatchRecorder struct {
	mu         sync.Mutex
	batchLens  []int
	calls      int
	failOnCall int // 1-based; 0 = never fail
}

func (h *usageBatchRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Usage []AmbientUsageEntry `json:"usage"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	h.mu.Lock()
	h.calls++
	call := h.calls
	h.batchLens = append(h.batchLens, len(body.Usage))
	h.mu.Unlock()
	if h.failOnCall != 0 && call == h.failOnCall {
		http.Error(w, "boom", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *usageBatchRecorder) snapshot() ([]int, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	lens := append([]int(nil), h.batchLens...)
	return lens, h.calls
}

func newUsageClient(t *testing.T, h http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL)
	c.SetToken("test-token")
	return c
}

// TestReportRuntimeUsage_Batches proves entries are POSTed in fixed-size chunks:
// 2500 entries → 1000 + 1000 + 500 across three calls.
func TestReportRuntimeUsage_Batches(t *testing.T) {
	h := &usageBatchRecorder{}
	c := newUsageClient(t, h)

	if err := c.ReportRuntimeUsage(context.Background(), "rt-1", makeAmbientEntries(2500)); err != nil {
		t.Fatalf("ReportRuntimeUsage: %v", err)
	}
	lens, calls := h.snapshot()
	if calls != 3 {
		t.Fatalf("expected 3 batched POSTs for 2500 entries, got %d", calls)
	}
	want := []int{ambientUsageBatchSize, ambientUsageBatchSize, 500}
	for i, n := range want {
		if lens[i] != n {
			t.Errorf("batch %d size = %d, want %d", i, lens[i], n)
		}
	}
}

// TestReportRuntimeUsage_SingleBatch proves a small set still goes in one POST.
func TestReportRuntimeUsage_SingleBatch(t *testing.T) {
	h := &usageBatchRecorder{}
	c := newUsageClient(t, h)
	if err := c.ReportRuntimeUsage(context.Background(), "rt-1", makeAmbientEntries(10)); err != nil {
		t.Fatalf("ReportRuntimeUsage: %v", err)
	}
	if _, calls := h.snapshot(); calls != 1 {
		t.Fatalf("expected 1 POST for 10 entries, got %d", calls)
	}
}

// TestReportRuntimeUsage_StopsOnBatchFailure proves the k-th batch failing
// returns an error immediately and the later batches are NOT sent.
func TestReportRuntimeUsage_StopsOnBatchFailure(t *testing.T) {
	h := &usageBatchRecorder{failOnCall: 2} // fail the 2nd of 3 batches
	c := newUsageClient(t, h)

	err := c.ReportRuntimeUsage(context.Background(), "rt-1", makeAmbientEntries(2500))
	if err == nil {
		t.Fatal("expected an error when a batch returns 500")
	}
	_, calls := h.snapshot()
	// Calls 1 (ok) and 2 (500) happen; call 3 must NOT be sent.
	if calls != 2 {
		t.Fatalf("expected exactly 2 POSTs (stop after the failed batch), got %d", calls)
	}
}

// fakeCollector is a deterministic Collector for runAmbientUsage tests. It emits
// a fixed entry set and a fixed nextState, and records every prevState it is
// handed so the test can assert whether the watermark was committed.
type fakeCollector struct {
	source     string
	entries    []AmbientUsageEntry
	nextState  json.RawMessage
	mu         sync.Mutex
	prevStates []string
}

func (f *fakeCollector) Source() string { return f.source }

func (f *fakeCollector) Scan(_ context.Context, prevState json.RawMessage) ([]AmbientUsageEntry, json.RawMessage, error) {
	f.mu.Lock()
	f.prevStates = append(f.prevStates, string(prevState))
	f.mu.Unlock()
	return f.entries, f.nextState, nil
}

func (f *fakeCollector) seenPrevStates() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.prevStates...)
}

// newAmbientTestDaemon wires a Daemon with a temp HOME (so the state file lands
// in an isolated dir), a registered claude runtime to attribute to, the given
// HTTP handler, and the given fake collector.
func newAmbientTestDaemon(t *testing.T, h http.Handler, fc *fakeCollector) *Daemon {
	t.Helper()
	t.Setenv("HOME", t.TempDir()) // isolate ~/.multica/ambient-usage-state.json

	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	d := New(Config{AmbientUsageEnabled: true, AmbientBackfillDays: 7}, slog.Default())
	d.client = NewClient(srv.URL)
	d.client.SetToken("test-token")
	d.collectorsFn = func() []Collector { return []Collector{fc} }
	// A registered runtime of the collector's provider so selectAmbientRuntime
	// returns a non-empty target.
	d.runtimeIndex["rt-claude"] = Runtime{ID: "rt-claude", Provider: fc.source}
	return d
}

// TestRunAmbientUsage_BatchFailureLeavesWatermarkUncommitted is the integration
// invariant for W2d: when a batch fails mid-upload, runAmbientUsage does NOT
// commit nextState, so the next tick re-scans from the SAME prevState and
// re-sends the whole set. Once the server accepts, the watermark commits and a
// further tick re-scans from the committed state.
func TestRunAmbientUsage_BatchFailureLeavesWatermarkUncommitted(t *testing.T) {
	// 2500 entries → 3 batches. Fail the 2nd batch on the first cycle only.
	var failing atomic.Bool
	failing.Store(true)
	var calls atomic.Int64
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		// During the failing cycle, the 2nd POST (n==2) returns 500.
		if failing.Load() && n == 2 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	fc := &fakeCollector{
		source:    "claude",
		entries:   makeAmbientEntries(2500),
		nextState: json.RawMessage(`{"watermark":1}`),
	}
	d := newAmbientTestDaemon(t, h, fc)

	// Cycle 1: batch 2 fails → state must NOT be committed.
	d.runAmbientUsage(context.Background())
	store := d.loadCollectorStore()
	if _, ok := store.Collectors["claude"]; ok {
		t.Fatalf("watermark must NOT be committed after a failed batch, but state was saved: %s", store.Collectors["claude"])
	}

	// Cycle 2: collector is re-scanned from the SAME (empty) prevState — proving
	// no partial advance — and now the server accepts every batch.
	failing.Store(false)
	d.runAmbientUsage(context.Background())

	prevStates := fc.seenPrevStates()
	if len(prevStates) != 2 {
		t.Fatalf("expected 2 scans, got %d", len(prevStates))
	}
	if prevStates[0] != "" || prevStates[1] != "" {
		t.Fatalf("both scans must run from the uncommitted (empty) prevState; got %q then %q", prevStates[0], prevStates[1])
	}

	// After the successful cycle, the watermark is committed.
	store = d.loadCollectorStore()
	got, ok := store.Collectors["claude"]
	if !ok {
		t.Fatal("watermark must be committed after the successful cycle")
	}
	if string(got) != `{"watermark":1}` {
		t.Fatalf("committed watermark = %s, want the collector's nextState", got)
	}

	// Cycle 3: now the scan must run from the COMMITTED state, not empty.
	d.runAmbientUsage(context.Background())
	prevStates = fc.seenPrevStates()
	if len(prevStates) != 3 || prevStates[2] != `{"watermark":1}` {
		t.Fatalf("third scan must start from committed watermark, got %q", prevStates[len(prevStates)-1])
	}
}

// TestRunAmbientUsage_NoRuntimeSkipsWithoutCommit proves that with no matching
// runtime registered, the collector is not even scanned and nothing is
// committed — the watermark is preserved for when a runtime appears.
func TestRunAmbientUsage_NoRuntimeSkipsWithoutCommit(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	fc := &fakeCollector{source: "claude", entries: makeAmbientEntries(5), nextState: json.RawMessage(`{"w":1}`)}
	d := newAmbientTestDaemon(t, h, fc)
	// Remove the runtime so selectAmbientRuntime returns "".
	delete(d.runtimeIndex, "rt-claude")

	d.runAmbientUsage(context.Background())
	if got := fc.seenPrevStates(); len(got) != 0 {
		t.Fatalf("collector must not be scanned with no runtime to attribute to, scans=%d", len(got))
	}
	store := d.loadCollectorStore()
	if _, ok := store.Collectors["claude"]; ok {
		t.Fatal("nothing should be committed when there is no runtime")
	}
}
