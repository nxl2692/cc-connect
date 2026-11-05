package core

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func usageTestEngine(t *testing.T, name string) *Engine {
	t.Helper()
	e := NewEngine(name, &stubAgent{}, nil, "", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	return e
}

func usageTestServer(t *testing.T, e *Engine) *httptest.Server {
	t.Helper()
	m := NewManagementServer(0, "usage-test-token", nil)
	m.RegisterEngine(e.name, e)
	srv := httptest.NewServer(m.buildHandler(http.NewServeMux()))
	t.Cleanup(srv.Close)
	return srv
}

func usageTestRead(t *testing.T, baseURL string) UsageSnapshot {
	t.Helper()
	req, err := http.NewRequest("GET", baseURL+"/api/v1/usage", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer usage-test-token")
	response, err := (&http.Client{Timeout: time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var r mgmtResponse
	if err := json.NewDecoder(response.Body).Decode(&r); err != nil {
		t.Fatal(err)
	}
	if !r.OK {
		t.Fatalf("usage request failed: %+v", r)
	}
	var result UsageSnapshot
	if err := json.Unmarshal(r.Data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func usageTestTurn(t *testing.T, e *Engine, key, user, platform string, at time.Time) (*Session, uint64) {
	t.Helper()
	msg := &Message{Platform: platform, UserID: user, UserName: "Same display name", SessionKey: key}
	e.usage.record(msg, at)
	session := e.sessions.GetOrCreateActive(key)
	gen, ok := session.TryLock()
	if !ok {
		t.Fatal("session unexpectedly busy")
	}
	e.usage.begin(msg, session, gen, at)
	return session, gen
}

func TestUsageSnapshot_DeduplicatesUsersAcrossProjectsAndChats(t *testing.T) {
	now := time.Now()
	m := NewManagementServer(0, "", nil)
	a, b := usageTestEngine(t, "a"), usageTestEngine(t, "b")
	m.RegisterEngine("a", a)
	m.RegisterEngine("b", b)
	usageTestTurn(t, a, "test:group1:alice", "alice", "test", now)
	usageTestTurn(t, a, "test:group2:alice", "alice", "test", now)
	usageTestTurn(t, b, "test:group3:alice", "alice", "test", now)
	// Identical display names are not identities, nor are equal IDs across platforms.
	usageTestTurn(t, a, "test:group1:bob", "bob", "test", now)
	usageTestTurn(t, b, "other:group:alice", "alice", "other", now)
	got := m.usageSnapshot(now)
	if got.UsingUsers != 3 || got.RecentUsers != 3 || got.ProcessingSessions != 5 || len(got.Users) != 3 {
		t.Fatalf("incorrect distinct-user counts: %+v", got)
	}
	for _, user := range got.Users {
		if user.UserID == "alice" && user.Platform == "test" {
			if user.ProcessingSessions != 3 || strings.Join(user.Projects, ",") != "a,b" {
				t.Fatalf("alice was not merged across projects: %+v", user)
			}
		}
	}
}

func TestUsageSnapshot_CompletedSessionsAndAutomatedTurnsDoNotCountAsPeople(t *testing.T) {
	now := time.Now()
	e := usageTestEngine(t, "test")
	m := NewManagementServer(0, "", nil)
	m.RegisterEngine("test", e)
	session, gen := usageTestTurn(t, e, "test:alice", "alice", "test", now.Add(-10*time.Minute))
	// Long-running human work stays visible even outside the activity window.
	if got := m.usageSnapshot(now); got.UsingUsers != 1 || got.RecentUsers != 0 {
		t.Fatalf("long-running user missing: %+v", got)
	}
	session.Unlock(gen)
	// A cron/heartbeat can acquire the same session before the next refresh.
	// It must not revive the previous human owner.
	newGen, ok := session.TryLock()
	if !ok {
		t.Fatal("automated turn could not acquire session")
	}
	if got := m.usageSnapshot(now); got.UsingUsers != 0 || got.ProcessingSessions != 0 || len(got.Users) != 0 {
		t.Fatalf("automated turn counted as a person: %+v", got)
	}
	session.Unlock(newGen)
	e.usage.record(&Message{Platform: "test", UserID: "alice"}, now.Add(-time.Minute))
	if got := m.usageSnapshot(now); got.UsingUsers != 0 || got.RecentUsers != 1 {
		t.Fatalf("recent activity confused with active processing: %+v", got)
	}
	if got := m.usageSnapshot(now.Add(usageWindow)); got.RecentUsers != 0 || len(got.Users) != 0 {
		t.Fatalf("activity did not expire: %+v", got)
	}
}

func TestUsageSnapshot_QueueCancellationAndUnknownSenders(t *testing.T) {
	now := time.Now()
	e := usageTestEngine(t, "test")
	m := NewManagementServer(0, "", nil)
	m.RegisterEngine("test", e)
	e.usage.record(&Message{Platform: "test", UserName: "Unknown"}, now)
	e.usage.record(&Message{Platform: "test", UserID: "queued", UserName: "Queued person"}, now)
	state := &interactiveState{pendingMessages: []queuedMessage{
		{userID: "queued", msgPlatform: "test"}, {userID: "queued", msgPlatform: "test"},
	}}
	e.interactiveStates["shared"] = state
	e.usage.observeState("shared", state)
	if got := m.usageSnapshot(now); got.UsingUsers != 1 || got.QueuedMessages != 2 || got.RecentUsers != 1 {
		t.Fatalf("queue deduplication incorrect: %+v", got)
	}
	state.mu.Lock()
	state.stopped = true
	state.mu.Unlock()
	if got := m.usageSnapshot(now); got.UsingUsers != 0 || got.QueuedMessages != 0 {
		t.Fatalf("cancelled queue stayed active: %+v", got)
	}
}

func TestManagementUsage_AuthenticationMethodsAndEmptySnapshot(t *testing.T) {
	m := NewManagementServer(0, "private-test-token", nil)
	handler := m.buildHandler(http.NewServeMux())
	for _, tc := range []struct {
		method, token string
		code          int
	}{
		{"GET", "", http.StatusUnauthorized},
		{"POST", "private-test-token", http.StatusMethodNotAllowed},
		{"GET", "private-test-token", http.StatusOK},
	} {
		req := httptest.NewRequest(tc.method, "/api/v1/usage", nil)
		if tc.token != "" {
			req.Header.Set("Authorization", "Bearer "+tc.token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != tc.code {
			t.Fatalf("%s with token=%t: status %d, want %d", tc.method, tc.token != "", w.Code, tc.code)
		}
		if w.Code == http.StatusOK {
			var result struct {
				Data UsageSnapshot `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Data.Users == nil || result.Data.UsingUsers != 0 || result.Data.WindowSeconds != 300 || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("invalid empty snapshot: %s", w.Body.String())
			}
		}
	}
}

func TestUsageSnapshot_ConcurrentMessageTrackingAndPolling(t *testing.T) {
	now := time.Now()
	e := usageTestEngine(t, "test")
	m := NewManagementServer(0, "", nil)
	m.RegisterEngine("test", e)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 40; j++ {
				e.usage.record(&Message{Platform: "test", UserID: "alice"}, now)
				_ = m.usageSnapshot(now)
			}
		}()
	}
	wg.Wait()
	if got := m.usageSnapshot(now); got.RecentUsers != 1 {
		t.Fatalf("duplicate user under concurrency: %+v", got)
	}
}

func TestUsageSnapshot_FailedAgentStartupReleasesPerson(t *testing.T) {
	env := newReceiptAckEnv(t, errors.New("test agent unavailable"))
	srv := usageTestServer(t, env.engine)
	env.send("failure", "private request body", 1000)
	env.awaitStartup()
	if got := usageTestRead(t, srv.URL); got.UsingUsers != 1 {
		t.Fatalf("startup not counted: %+v", got)
	}
	close(env.startup.release)
	env.waitFor("failed startup releases usage", 3*time.Second, func() bool { return usageTestRead(t, srv.URL).UsingUsers == 0 })
	got := usageTestRead(t, srv.URL)
	if got.RecentUsers != 1 {
		t.Fatalf("failed request lost recent activity: %+v", got)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private request body") {
		t.Fatal("dashboard leaked message contents")
	}
}
