package core

import (
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const progressEventLimit = 96
const progressSessionLimit = 64

type ProgressEvent struct {
	Kind   string    `json:"kind"`
	Time   time.Time `json:"time"`
	Tool   string    `json:"tool,omitempty"`
	Text   string    `json:"text,omitempty"`
	Status string    `json:"status,omitempty"`
}

type ProgressSession struct {
	Key        string          `json:"key"`
	SessionID  string          `json:"session_id"`
	SessionKey string          `json:"session_key"`
	Name       string          `json:"name"`
	Platform   string          `json:"platform"`
	UserID     string          `json:"user_id"`
	UserName   string          `json:"user_name"`
	ChatName   string          `json:"chat_name"`
	Status     string          `json:"status"`
	StartedAt  time.Time       `json:"started_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
	Turn       uint64          `json:"turn"`
	ToolCount  int             `json:"tool_count"`
	Finished   bool            `json:"finished"`
	Request    string          `json:"request,omitempty"`
	Preview    string          `json:"preview,omitempty"`
	Result     string          `json:"result,omitempty"`
	Events     []ProgressEvent `json:"events,omitempty"`
	History    []HistoryEntry  `json:"history,omitempty"`
}

type progressRun struct {
	view       ProgressSession
	generation uint64
}

// A bounded, read-only projection of events the engine already consumes.
// It never reads AgentSession.Events itself, and never sends platform messages.
type progressStore struct {
	mu   sync.Mutex
	runs map[*Session]*progressRun
	seq  uint64
}

var progressSecretPattern = regexp.MustCompile(`(?i)((?:authorization:\s*bearer\s+)|(?:(?:api[_-]?key|token|secret|password)["']?\s*[:=]\s*["']?)|(?:--(?:api-key|token|secret|password)\s+))[^\s"',;]+`)

func progressText(text string, max int) string {
	return truncateIf(progressSecretPattern.ReplaceAllString(text, "${1}[REDACTED]"), max)
}

func (p *progressStore) begin(msg *Message, session *Session, gen uint64, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.runs == nil {
		p.runs = make(map[*Session]*progressRun)
	}
	p.seq++
	key := fmt.Sprintf("live-%d", p.seq)
	turn := uint64(1)
	if old := p.runs[session]; old != nil {
		key, turn = old.view.Key, old.view.Turn+1
	}
	p.runs[session] = &progressRun{generation: gen, view: ProgressSession{
		Key: key, SessionID: session.ID, SessionKey: msg.SessionKey, Name: session.GetName(),
		Platform: msg.Platform, UserID: msg.UserID, UserName: msg.UserName, ChatName: msg.ChatName,
		Status: "starting", StartedAt: now, UpdatedAt: now, Turn: turn,
		Request: progressText(msg.Content, 1000), Events: []ProgressEvent{},
	}}
	p.pruneLocked()
}

func (p *progressStore) beginQueued(q queuedMessage, session *Session, gen uint64, now time.Time) {
	p.begin(&Message{Platform: q.msgPlatform, UserID: q.userID, UserName: q.userName, SessionKey: q.msgSessionKey, Content: q.content}, session, gen, now)
}

func (p *progressStore) pruneLocked() {
	for len(p.runs) > progressSessionLimit {
		var oldest *Session
		var timestamp time.Time
		for session, run := range p.runs {
			if run.view.Finished && (oldest == nil || run.view.UpdatedAt.Before(timestamp)) {
				oldest, timestamp = session, run.view.UpdatedAt
			}
		}
		if oldest == nil {
			return
		} // retain all in-flight sessions
		delete(p.runs, oldest)
	}
}

func (p *progressStore) observe(session *Session, gen uint64, event Event, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	run := p.runs[session]
	if run == nil || run.generation != gen || run.view.Finished {
		return
	}
	v := &run.view
	item := ProgressEvent{Kind: string(event.Type), Time: now, Tool: progressText(event.ToolName, 100)}
	switch event.Type {
	case EventThinking:
		v.Status = "thinking" // status only; never retain private reasoning payloads
	case EventToolUse:
		v.Status = "working"
		v.ToolCount++
		item.Text = progressText(event.ToolInput, 1000)
	case EventToolResult:
		v.Status = "working"
		item.Text = progressText(event.ToolResult, 1500)
		item.Status = progressText(event.ToolStatus, 60)
	case EventText:
		v.Status = "responding"
		preview := []rune(v.Preview + event.Content)
		if len(preview) > 16000 {
			preview = preview[len(preview)-16000:]
		}
		v.Preview = string(preview)
		v.UpdatedAt = now
		return
	case EventHookRejected:
		v.Preview = ""
		v.Status = "working"
	case EventPermissionRequest:
		v.Status = "waiting_permission"
	case EventUserInputRequest:
		v.Status = "waiting_input"
	case EventError:
		v.Status, v.Finished = "error", true
		item.Text = event.Content
		if event.Error != nil {
			item.Text = event.Error.Error()
		}
		item.Text = progressText(item.Text, 1500)
	case EventResult:
		return // terminal text is recorded after the engine resolves the final answer
	default:
		return
	}
	v.UpdatedAt = now
	if item.Kind == string(EventThinking) && len(v.Events) > 0 && v.Events[len(v.Events)-1].Kind == item.Kind {
		return
	}
	v.Events = append(v.Events, item)
	if len(v.Events) > progressEventLimit {
		v.Events = append([]ProgressEvent(nil), v.Events[len(v.Events)-progressEventLimit:]...)
	}
}

func (p *progressStore) resume(session *Session, gen uint64, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if run := p.runs[session]; run != nil && run.generation == gen && !run.view.Finished {
		run.view.Status, run.view.UpdatedAt = "working", now
	}
}

func (p *progressStore) result(session *Session, gen uint64, content string, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	run := p.runs[session]
	if run == nil || run.generation != gen {
		return
	}
	v := &run.view
	v.Status, v.Finished, v.UpdatedAt = "completed", true, now
	if isSilentReply(content) {
		content = ""
	}
	v.Result = truncateIf(content, 32000)
	v.Preview = ""
	p.pruneLocked()
}

func (p *progressStore) end(session *Session, gen uint64, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if run := p.runs[session]; run != nil && run.generation == gen && !run.view.Finished {
		run.view.Status, run.view.Finished, run.view.UpdatedAt = "interrupted", true, now
	}
	p.pruneLocked()
}

type progressCopy struct {
	view       ProgressSession
	session    *Session
	generation uint64
}

func (p *progressStore) snapshot() []progressCopy {
	p.mu.Lock()
	defer p.mu.Unlock()
	rows := make([]progressCopy, 0, len(p.runs))
	for session, run := range p.runs {
		v := run.view
		v.Events = append([]ProgressEvent{}, v.Events...)
		rows = append(rows, progressCopy{v, session, run.generation})
	}
	return rows
}

// No Bridge connection, agent process, or new request is created by this GET.
func (m *ManagementServer) handleProjectProgress(w http.ResponseWriter, r *http.Request, e *Engine) {
	if r.Method != http.MethodGet {
		mgmtError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	rows := e.progress.snapshot()
	seen := make(map[*Session]bool)
	for i := range rows {
		row := &rows[i]
		seen[row.session] = true
		row.session.mu.Lock()
		active := row.session.busy && row.session.lockGen == row.generation
		row.session.mu.Unlock()
		if !row.view.Finished && !active {
			row.view.Status, row.view.Finished = "interrupted", true
		}
	}
	idToKey, _ := e.sessions.SessionKeyMap()
	for _, session := range e.sessions.AllSessions() {
		if seen[session] {
			continue
		}
		key := idToKey[session.ID]
		v := ProgressSession{Key: "saved:" + session.ID, SessionID: session.ID, SessionKey: key, Name: session.GetName(), Status: "idle", Finished: true, UpdatedAt: session.GetUpdatedAt()}
		parts := strings.SplitN(key, ":", 3)
		if len(parts) > 0 {
			v.Platform = parts[0]
		}
		if len(parts) == 3 {
			v.UserID = parts[2]
		}
		if meta := e.sessions.GetUserMeta(key); meta != nil {
			v.UserName, v.ChatName = meta.UserName, meta.ChatName
		}
		rows = append(rows, progressCopy{view: v, session: session})
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i].view, rows[j].view
		if a.Finished != b.Finished {
			return !a.Finished
		}
		return a.UpdatedAt.After(b.UpdatedAt)
	})
	filtered := rows[:0]
	for _, row := range rows {
		if user := r.URL.Query().Get("user"); user != "" && row.view.UserID != user {
			continue
		}
		if platform := r.URL.Query().Get("platform"); platform != "" && row.view.Platform != platform {
			continue
		}
		filtered = append(filtered, row)
	}
	rows = filtered
	var selected *ProgressSession
	list := make([]ProgressSession, 0, len(rows))
	wanted := r.URL.Query().Get("key")
	for _, row := range rows {
		v := row.view
		if (wanted == "" && selected == nil) || v.Key == wanted {
			history := row.session.GetHistory(50)
			for i := range history {
				history[i].Content = truncateIf(history[i].Content, 32000)
			}
			v.History = history
			selected = &v
		}
		summary := row.view
		summary.Events, summary.History = nil, nil
		summary.Preview, summary.Result = "", ""
		list = append(list, summary)
	}
	if wanted != "" && selected == nil {
		mgmtError(w, http.StatusNotFound, "progress session not found")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	mgmtJSON(w, http.StatusOK, map[string]any{"sampled_at": time.Now(), "sessions": list, "selected": selected, "read_only": true})
}
