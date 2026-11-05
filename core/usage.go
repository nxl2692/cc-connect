package core

import (
	"net/http"
	"sort"
	"sync"
	"time"
)

const usageWindow = 5 * time.Minute

type usageIdentity struct {
	platform string
	userID   string
}

type usagePerson struct {
	identity usageIdentity
	name     string
	lastSeen time.Time
}

type usageTurn struct {
	person     usagePerson
	generation uint64
	startedAt  time.Time
}

// Track only real inbound messages and the turns they start. An idle live
// agent, historical session, cron, heartbeat or timer is not a human user.
// The zero value is ready to use; metadata never includes message contents.
type usageTracker struct {
	mu         sync.Mutex
	people     map[usageIdentity]usagePerson
	turns      map[*Session]usageTurn
	states     map[string]*interactiveState
	lastPruned time.Time
}

func usageSender(msg *Message) usagePerson {
	return usagePerson{
		identity: usageIdentity{platform: msg.Platform, userID: msg.UserID},
		name:     msg.UserName,
	}
}

func (u *usageTracker) record(msg *Message, now time.Time) {
	if msg.UserID == "" || msg.Platform == "" {
		return
	}
	person := usageSender(msg)
	person.lastSeen = now
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.people == nil {
		u.people = make(map[usageIdentity]usagePerson)
	}
	u.people[person.identity] = person
	if now.Sub(u.lastPruned) >= time.Hour {
		u.pruneLocked(now)
	}
}

func (u *usageTracker) begin(msg *Message, session *Session, gen uint64, now time.Time) {
	if msg.UserID == "" || msg.Platform == "" {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.turns == nil {
		u.turns = make(map[*Session]usageTurn)
	}
	u.turns[session] = usageTurn{person: usageSender(msg), generation: gen, startedAt: now}
}

func (u *usageTracker) beginQueued(q queuedMessage, session *Session, gen uint64, now time.Time) {
	u.begin(&Message{Platform: q.msgPlatform, UserID: q.userID, UserName: q.userName}, session, gen, now)
}

// Lock generation prevents a later automated turn on the same session from
// reviving the human owner of a previous, already-completed turn.
func (u *usageTracker) pruneLocked(now time.Time) []usageTurn {
	turns := make([]usageTurn, 0, len(u.turns))
	busy := make(map[usageIdentity]bool)
	for session, turn := range u.turns {
		session.mu.Lock()
		active := session.busy && session.lockGen == turn.generation
		session.mu.Unlock()
		if !active {
			delete(u.turns, session)
			continue
		}
		turns = append(turns, turn)
		busy[turn.person.identity] = true
	}
	for id, person := range u.people {
		if now.Sub(person.lastSeen) > 24*time.Hour && !busy[id] {
			delete(u.people, id)
		}
	}
	u.lastPruned = now
	return turns
}

func (u *usageTracker) snapshot(now time.Time) ([]usagePerson, []usageTurn) {
	u.mu.Lock()
	defer u.mu.Unlock()
	turns := u.pruneLocked(now)
	people := make([]usagePerson, 0, len(u.people))
	for _, person := range u.people {
		people = append(people, person)
	}
	return people, turns
}

// Keep a metadata-only view of state pointers, updated while the engine owns
// interactiveMu. Reading usage must never wait for that lock: it is held
// across external agent startup, which can take seconds or minutes.
func (u *usageTracker) observeState(key string, state *interactiveState) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.states == nil {
		u.states = make(map[string]*interactiveState)
	}
	if state == nil {
		delete(u.states, key)
	} else {
		u.states[key] = state
	}
}

func (u *usageTracker) observedStates() []*interactiveState {
	u.mu.Lock()
	defer u.mu.Unlock()
	states := make([]*interactiveState, 0, len(u.states))
	for _, state := range u.states {
		states = append(states, state)
	}
	return states
}

// UsageUser identifies a sender within one platform. IDs on different
// platforms cannot be assumed to identify the same person.
type UsageUser struct {
	Platform           string     `json:"platform"`
	UserID             string     `json:"user_id"`
	UserName           string     `json:"user_name"`
	LastSeen           time.Time  `json:"last_seen"`
	StartedAt          *time.Time `json:"started_at,omitempty"`
	Projects           []string   `json:"projects"`
	ProcessingSessions int        `json:"processing_sessions"`
	QueuedMessages     int        `json:"queued_messages"`
}

type UsageSnapshot struct {
	SampledAt          time.Time   `json:"sampled_at"`
	WindowSeconds      int         `json:"window_seconds"`
	UsingUsers         int         `json:"using_users"`
	RecentUsers        int         `json:"recent_users"`
	ProcessingSessions int         `json:"processing_sessions"`
	QueuedMessages     int         `json:"queued_messages"`
	Users              []UsageUser `json:"users"`
}

type usageSummary struct {
	users map[usageIdentity]*UsageUser
}

func (s *usageSummary) add(person usagePerson, project string) *UsageUser {
	user := s.users[person.identity]
	if user == nil {
		user = &UsageUser{Platform: person.identity.platform, UserID: person.identity.userID, Projects: []string{}}
		s.users[person.identity] = user
	}
	newer := person.lastSeen.After(user.LastSeen)
	if newer {
		user.LastSeen = person.lastSeen
	}
	if person.name != "" && (newer || user.UserName == "") {
		user.UserName = person.name
	}
	if project == "" {
		return user
	}
	for _, p := range user.Projects {
		if p == project {
			return user
		}
	}
	user.Projects = append(user.Projects, project)
	return user
}

func (s *usageSummary) collect(project string, e *Engine, now time.Time) {
	people, turns := e.usage.snapshot(now)
	for _, person := range people {
		if !person.lastSeen.Before(now.Add(-usageWindow)) {
			s.add(person, project)
		} else {
			s.add(person, "")
		}
	}
	for _, turn := range turns {
		user := s.add(turn.person, project)
		user.ProcessingSessions++
		if user.StartedAt == nil || turn.startedAt.Before(*user.StartedAt) {
			startedAt := turn.startedAt
			user.StartedAt = &startedAt
		}
	}
	// Queue state is read from the engine so dropped/cancelled requests
	// disappear immediately rather than leaving a separate stale counter.
	for _, state := range e.usage.observedStates() {
		state.mu.Lock()
		if !state.stopped {
			for _, q := range state.pendingMessages {
				if q.userID != "" && q.msgPlatform != "" {
					person := usagePerson{identity: usageIdentity{q.msgPlatform, q.userID}, name: q.userName}
					s.add(person, project).QueuedMessages++
				}
			}
		}
		state.mu.Unlock()
	}
}

func (m *ManagementServer) usageSnapshot(now time.Time) UsageSnapshot {
	summary := usageSummary{users: make(map[usageIdentity]*UsageUser)}
	m.mu.RLock()
	for name, e := range m.engines {
		summary.collect(name, e, now)
	}
	m.mu.RUnlock()
	result := UsageSnapshot{SampledAt: now, WindowSeconds: int(usageWindow.Seconds()), Users: []UsageUser{}}
	for _, user := range summary.users {
		inUse := user.ProcessingSessions > 0 || user.QueuedMessages > 0
		recent := !user.LastSeen.IsZero() && !user.LastSeen.Before(now.Add(-usageWindow))
		if !inUse && !recent {
			continue
		}
		if inUse {
			result.UsingUsers++
		}
		if recent {
			result.RecentUsers++
		}
		result.ProcessingSessions += user.ProcessingSessions
		result.QueuedMessages += user.QueuedMessages
		sort.Strings(user.Projects)
		result.Users = append(result.Users, *user)
	}
	sort.Slice(result.Users, func(i, j int) bool {
		a, b := result.Users[i], result.Users[j]
		activeA, activeB := a.ProcessingSessions+a.QueuedMessages > 0, b.ProcessingSessions+b.QueuedMessages > 0
		if activeA != activeB {
			return activeA
		}
		if !a.LastSeen.Equal(b.LastSeen) {
			return a.LastSeen.After(b.LastSeen)
		}
		if a.Platform != b.Platform {
			return a.Platform < b.Platform
		}
		return a.UserID < b.UserID
	})
	return result
}

func (m *ManagementServer) handleUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		mgmtError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	mgmtJSON(w, http.StatusOK, m.usageSnapshot(time.Now()))
}
