package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type progressTestSnapshot struct {
	ReadOnly bool                    `json:"read_only"`
	Queue    []ProgressQueuedRequest `json:"queue"`
	Sessions []ProgressSession       `json:"sessions"`
	Selected *ProgressSession        `json:"selected"`
}

func progressTestRead(t *testing.T, baseURL, query string) progressTestSnapshot {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, baseURL+"/api/v1/projects/receipt/progress"+query, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer usage-test-token")
	resp, err := (&http.Client{Timeout: time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var envelope struct {
		OK   bool                 `json:"ok"`
		Data progressTestSnapshot `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || !envelope.OK || !envelope.Data.ReadOnly {
		t.Fatalf("invalid progress response: %d %+v", resp.StatusCode, envelope)
	}
	return envelope.Data
}

func TestLiveProgress_IsolationBoundsAndPrivatePayloads(t *testing.T) {
	var p progressStore
	now := time.Now()
	a, b := &Session{ID: "a"}, &Session{ID: "b"}
	p.begin(&Message{Content: "first task", UserID: "alice"}, a, 1, now)
	p.begin(&Message{Content: "second task", UserID: "bob"}, b, 1, now)
	p.observe(a, 1, Event{Type: EventThinking, Content: "private reasoning"}, now)
	p.observe(a, 1, Event{Type: EventPermissionRequest, ToolName: "shell", ToolInputRaw: map[string]any{"private": "raw permission arguments"}}, now)
	p.resume(a, 1, now)
	p.observe(a, 1, Event{Type: EventToolUse, ToolName: "shell", ToolInput: "token=secret-value --password another-secret"}, now)
	p.observe(a, 1, Event{Type: EventToolResult, ToolName: "shell", ToolResult: "Authorization: Bearer private-token", ToolStatus: "success"}, now)
	p.observe(a, 1, Event{Type: EventText, Content: strings.Repeat("中", 16001)}, now)
	p.observe(a, 1, Event{Type: EventText, Content: "latest"}, now)
	var got ProgressSession
	for _, row := range p.snapshot() {
		if row.session == a {
			got = row.view
		} else if row.view.Preview != "" || len(row.view.Events) != 0 {
			t.Fatal("events leaked to another person")
		}
	}
	if got.Status != "responding" || got.ToolCount != 1 || len([]rune(got.Preview)) != 16000 || !strings.HasSuffix(got.Preview, "latest") {
		t.Fatalf("invalid bounded live output: status=%s count=%d len=%d", got.Status, got.ToolCount, len([]rune(got.Preview)))
	}
	encoded, _ := json.Marshal(got)
	for _, secret := range []string{"private reasoning", "raw permission arguments", "secret-value", "another-secret", "private-token"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("progress retained private payload %q", secret)
		}
	}
	// A caller cannot mutate the cached event timeline through its snapshot.
	got.Events[0].Text = "mutated"
	for i := 0; i < progressEventLimit+10; i++ {
		p.observe(a, 1, Event{Type: EventToolUse, ToolName: "shell"}, now)
	}
	for _, row := range p.snapshot() {
		if row.session == a && (len(row.view.Events) != progressEventLimit || row.view.Events[0].Text == "mutated") {
			t.Fatal("event buffer is unbounded or aliased")
		}
	}
	p.result(a, 1, "canonical answer", now)
	p.end(a, 1, now)
	p.beginQueued(queuedMessage{content: "follow-up", userID: "bob"}, a, 2, now)
	p.observe(a, 1, Event{Type: EventText, Content: "stale output"}, now)
	p.result(a, 1, "stale answer", now)
	p.end(a, 1, now)
	for _, row := range p.snapshot() {
		if row.session == a && (row.view.Key != got.Key || row.view.Turn != 2 || row.view.UserID != "bob" || row.view.Finished || row.view.Preview != "" || row.view.Result != "" || len(row.view.Events) != 0) {
			t.Fatalf("new turn contaminated by old generation: %+v", row.view)
		}
	}
	p.end(a, 2, now)
	for _, row := range p.snapshot() {
		if row.session == a && row.view.Status != "interrupted" {
			t.Fatal("aborted turn was not interrupted")
		}
	}
}

func TestLiveProgress_RetentionAndConcurrentPolling(t *testing.T) {
	var p progressStore
	now := time.Now()
	active := &Session{ID: "active"}
	p.begin(&Message{}, active, 1, now)
	for i := 0; i < progressSessionLimit+10; i++ {
		s := &Session{ID: fmt.Sprint(i)}
		p.begin(&Message{}, s, 1, now.Add(time.Duration(i)*time.Second))
		p.result(s, 1, "done", now.Add(time.Duration(i)*time.Second))
	}
	rows := p.snapshot()
	if len(rows) > progressSessionLimit {
		t.Fatalf("retained %d sessions", len(rows))
	}
	found := false
	for _, row := range rows {
		if row.session == active {
			found = true
		}
	}
	if !found {
		t.Fatal("active session evicted by completed sessions")
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				p.observe(active, 1, Event{Type: EventText, Content: "chunk"}, now)
				_ = p.snapshot()
			}
		}()
	}
	wg.Wait()
	p.observe(active, 1, Event{Type: EventError, Content: "token=private-error"}, now)
	for _, row := range p.snapshot() {
		if row.session == active && (row.view.Status != "error" || !row.view.Finished) {
			t.Fatal("failure not captured")
		}
	}
}

func TestManagementProgress_ReadOnlyAuthenticatedWithoutBridge(t *testing.T) {
	e := usageTestEngine(t, "receipt")
	s := e.sessions.GetOrCreateActive("test:group:alice")
	s.AddHistory("user", "saved request")
	m := NewManagementServer(0, "test-token", nil)
	m.RegisterEngine("receipt", e)
	handler := m.buildHandler(http.NewServeMux())
	path := "/api/v1/projects/receipt/progress"
	for _, tc := range []struct {
		method, path, token string
		code                int
	}{
		{"GET", path, "", 401}, {"GET", path, "wrong", 401}, {"POST", path, "test-token", 405},
		{"GET", path, "test-token", 200}, {"GET", path + "?key=saved:" + s.ID, "test-token", 200},
		{"GET", path + "?key=missing", "test-token", 404}, {"GET", "/api/v1/projects/missing/progress", "test-token", 404},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Header.Set("Authorization", "Bearer "+tc.token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != tc.code {
			t.Fatalf("%s %s: got %d want %d: %s", tc.method, tc.path, w.Code, tc.code, w.Body.String())
		}
		if w.Code == 200 && (w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Body.String(), `"read_only":true`)) {
			t.Fatal("missing read-only/no-store contract")
		}
	}
	// Selection filtering must not reveal another user's history.
	srv := usageTestServer(t, e)
	if got := progressTestRead(t, srv.URL, "?user=bob&platform=test"); len(got.Sessions) != 0 || got.Selected != nil {
		t.Fatalf("cross-user filtering failed: %+v", got)
	}
	if got := progressTestRead(t, srv.URL, ""); got.Selected == nil || got.Selected.Status != "idle" || len(got.Selected.History) != 1 {
		t.Fatalf("saved history unavailable: %+v", got)
	}
}

// External IM boundary offering both preview and card capabilities. Any use in
// final-only mode is a regression, even when ordinary Sends remain quiet.
type progressTestPlatform struct {
	cujStreamingPlatform
	cards atomic.Int32
}

func (p *progressTestPlatform) CreateStreamingCard(context.Context, any) (StreamingCard, error) {
	p.cards.Add(1)
	return &stubStreamingCard{}, nil
}

func TestLiveProgress_FailedAndCancelledTurnsNeverDeliverPartialAnswers(t *testing.T) {
	for _, scenario := range []string{"error", "exit", "cancel", "startup"} {
		t.Run(scenario, func(t *testing.T) {
			var startupErr error
			if scenario == "startup" {
				startupErr = fmt.Errorf("agent unavailable")
			}
			env := newReceiptAckEnv(t, startupErr)
			env.engine.SetDisplayConfig(DisplayCfg{FinalOnly: true})
			srv := usageTestServer(t, env.engine)
			send := func(content string) {
				env.engine.ReceiveMessage(env.plat, &Message{SessionKey: "test:progress-user", Platform: "test", UserID: "alice", Content: content, ReplyCtx: "ctx"})
			}
			send("start task")
			env.awaitStartup()
			close(env.startup.release)
			if scenario != "startup" {
				env.awaitSendCount(1)
				env.startup.session.events <- Event{Type: EventText, Content: "partial answer"}
				env.waitFor("web preview captured", 3*time.Second, func() bool {
					v := progressTestRead(t, srv.URL, "").Selected
					return v != nil && v.Preview == "partial answer"
				})
				switch scenario {
				case "error":
					env.startup.session.events <- Event{Type: EventError, Error: fmt.Errorf("agent failed")}
				case "exit":
					_ = env.startup.session.Close()
				case "cancel":
					send("/stop")
				}
			}
			want := "interrupted"
			if scenario == "error" || scenario == "startup" {
				want = "error"
			}
			env.waitFor("terminal failure on web", 3*time.Second, func() bool {
				v := progressTestRead(t, srv.URL, "").Selected
				return v != nil && v.Finished && v.Status == want
			})
			env.waitFor("visible IM failure or stop", 3*time.Second, func() bool { return len(env.plat.getSent()) > 0 })
			for _, message := range env.plat.getSent() {
				if strings.Contains(message, "partial answer") {
					t.Fatalf("partial answer delivered after %s: %q", scenario, message)
				}
			}
		})
	}
}

func TestManagementProgress_QueueFilteringPositionsAndCancellation(t *testing.T) {
	e := usageTestEngine(t, "receipt")
	now := time.Now().UTC()
	first := &interactiveState{pendingMessages: []queuedMessage{
		{progressKey: "q-alice", queuedAt: now.Add(-3 * time.Minute), userID: "alice", userName: "Alice", msgPlatform: "test", msgSessionKey: "test:shared", content: "password=private-value " + strings.Repeat("x", 1200), images: []ImageAttachment{{Data: []byte("private-image-data")}}},
		{progressKey: "q-bob", queuedAt: now.Add(-2 * time.Minute), userID: "bob", userName: "Bob", msgPlatform: "test", msgSessionKey: "test:shared", content: "second request"},
	}}
	other := &interactiveState{pendingMessages: []queuedMessage{{progressKey: "q-other", userID: "bob", msgPlatform: "other", content: "independent request"}}}
	stopped := &interactiveState{stopped: true, pendingMessages: []queuedMessage{{userID: "bob", msgPlatform: "test", content: "cancelled request"}}}
	e.usage.observeState("first", first)
	e.usage.observeState("other", other)
	e.usage.observeState("stopped", stopped)
	srv := usageTestServer(t, e)
	// Management reads must not wait on the agent-startup lock.
	var all progressTestSnapshot
	func() {
		e.interactiveMu.Lock()
		defer e.interactiveMu.Unlock()
		all = progressTestRead(t, srv.URL, "")
	}()
	if len(all.Queue) != 3 {
		t.Fatalf("wrong pending queue: %+v", all.Queue)
	}
	encoded, _ := json.Marshal(all.Queue)
	for _, secret := range []string{"private-value", "private-image-data", "cancelled request"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("queue leaked %q", secret)
		}
	}
	if len([]rune(all.Queue[0].Request)) > 1003 || all.Queue[0].ImageCount != 1 {
		t.Fatal("queue preview limit or attachment count is invalid")
	}
	filtered := progressTestRead(t, srv.URL, "?user=bob&platform=test")
	if len(filtered.Queue) != 1 || filtered.Queue[0].Position != 2 || filtered.Queue[0].Key != "q-bob" || filtered.Queue[0].QueuedAt == nil || !filtered.Queue[0].QueuedAt.Equal(now.Add(-2*time.Minute)) {
		t.Fatalf("filter renumbered queue or lost metadata: %+v", filtered.Queue)
	}
	first.mu.Lock()
	first.pendingMessages = first.pendingMessages[1:]
	first.mu.Unlock()
	filtered = progressTestRead(t, srv.URL, "?user=bob&platform=test")
	if len(filtered.Queue) != 1 || filtered.Queue[0].Position != 1 || filtered.Queue[0].Key != "q-bob" {
		t.Fatalf("dequeue did not advance position with a stable key: %+v", filtered.Queue)
	}
	first.mu.Lock()
	first.stopped = true
	first.mu.Unlock()
	if got := progressTestRead(t, srv.URL, "?user=bob&platform=test").Queue; len(got) != 0 {
		t.Fatalf("cancelled queue remained visible: %+v", got)
	}
	e.usage.observeState("other", nil)
	if got := progressTestRead(t, srv.URL, "").Queue; len(got) != 0 {
		t.Fatalf("removed queue remained visible: %+v", got)
	}
}

func TestManagementProgress_QueueConcurrentPolling(t *testing.T) {
	e := usageTestEngine(t, "receipt")
	state := &interactiveState{}
	e.usage.observeState("queue", state)
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = e.progressQueue("", "")
			}
		}()
	}
	for i := 0; i < 100; i++ {
		state.mu.Lock()
		state.pendingMessages = []queuedMessage{{progressKey: "stable", userID: "alice", content: "request"}}
		state.mu.Unlock()
		state.mu.Lock()
		state.pendingMessages = nil
		state.mu.Unlock()
	}
	wg.Wait()
}
