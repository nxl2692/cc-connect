package wecom

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/chenhg5/cc-connect/core"
	"github.com/gorilla/websocket"
)

// Exercise real callback parsing and the actual outbound WebSocket frames.
// No real bot connection or messages to a live group are needed.
func groupReplyTestPlatform(t *testing.T, opts map[string]any, ackCodes ...int) (*WSPlatform, <-chan wsFrame, <-chan *core.Message) {
	t.Helper()
	frames := make(chan wsFrame, 100)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			var frame wsFrame
			if err := conn.ReadJSON(&frame); err != nil {
				return
			}
			frames <- frame
			code := 0
			if len(ackCodes) > 0 {
				code = ackCodes[0]
			}
			if err := conn.WriteJSON(map[string]any{"headers": frame.Headers, "errcode": code}); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if opts == nil {
		opts = map[string]any{}
	}
	opts["bot_id"], opts["bot_secret"] = "bot", "test-secret"
	platform, err := newWebSocket(opts)
	if err != nil {
		t.Fatal(err)
	}
	p := platform.(*WSPlatform)
	p.conn = conn
	messages := make(chan *core.Message, 10)
	p.handler = func(_ core.Platform, msg *core.Message) { messages <- msg }
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			var frame wsFrame
			if err := conn.ReadJSON(&frame); err != nil {
				return
			}
			p.handleFrame(frame)
		}
	}()
	t.Cleanup(func() { conn.Close(); <-readerDone })
	return p, frames, messages
}

func groupReplyTestMessage(t *testing.T, p *WSPlatform, messages <-chan *core.Message, id, user, text, chatType string) *core.Message {
	t.Helper()
	body := wsMsgCallbackBody{MsgID: id, ChatID: "group1", ChatType: chatType, MsgType: "text"}
	body.From.UserID, body.Text.Content = user, text
	p.handleMsgCallback(wsCallbackFrame(t, "req-"+id, body))
	select {
	case msg := <-messages:
		return msg
	case <-time.After(time.Second):
		t.Fatal("callback did not deliver a message")
		return nil
	}
}

func groupReplyTestFrame(t *testing.T, frames <-chan wsFrame) (wsFrame, string) {
	t.Helper()
	select {
	case frame := <-frames:
		var body struct {
			Markdown struct {
				Content string `json:"content"`
			} `json:"markdown"`
			Stream struct {
				Content string `json:"content"`
			} `json:"stream"`
		}
		if err := json.Unmarshal(frame.Body, &body); err != nil {
			t.Fatal(err)
		}
		return frame, body.Markdown.Content + body.Stream.Content
	case <-time.After(time.Second):
		t.Fatal("outbound frame missing")
		return wsFrame{}, ""
	}
}

func TestWSGroupReply_FollowupsKeepEachQuestionAndSender(t *testing.T) {
	p, frames, messages := groupReplyTestPlatform(t, nil)
	a := groupReplyTestMessage(t, p, messages, "a", "alice", "@bot 问题甲", "group")
	b := groupReplyTestMessage(t, p, messages, "b", "bob", "@bot 问题乙", "group")
	a2 := groupReplyTestMessage(t, p, messages, "a2", "alice", "@bot 下个问题", "group")
	// Answer out of order, after later messages have already arrived.
	for _, item := range []struct {
		msg            *core.Message
		user, question string
	}{
		{b, "bob", "问题乙"}, {a, "alice", "问题甲"}, {a2, "alice", "下个问题"},
	} {
		if err := p.Send(context.Background(), item.msg.ReplyCtx, "回答"); err != nil {
			t.Fatal(err)
		}
		frame, text := groupReplyTestFrame(t, frames)
		if frame.Cmd != "aibot_respond_msg" || frame.Headers.ReqID != "req-"+item.msg.MessageID {
			t.Errorf("follow-up lost originating callback: %+v", frame)
		}
		want := "<@" + item.user + ">\n\n> " + item.question + "\n\n回答"
		if text != want {
			t.Errorf("group answer = %q, want %q", text, want)
		}
	}
}

func TestWSGroupReply_ReplyAlsoAttributesQuestion(t *testing.T) {
	p, frames, messages := groupReplyTestPlatform(t, nil)
	msg := groupReplyTestMessage(t, p, messages, "reply", "alice", "@bot 原问题", "group")
	if err := p.Reply(context.Background(), msg.ReplyCtx, "处理结果"); err != nil {
		t.Fatal(err)
	}
	frame, text := groupReplyTestFrame(t, frames)
	if frame.Headers.ReqID != "req-reply" || text != "<@alice>\n\n> 原问题\n\n处理结果" {
		t.Fatalf("reply attribution = %q, frame=%+v", text, frame)
	}
}

func TestWSGroupReply_LongAnswerReservesPrefixBytesAndMentionsOnce(t *testing.T) {
	p, frames, messages := groupReplyTestPlatform(t, nil)
	msg := groupReplyTestMessage(t, p, messages, "long", "alice", "@bot "+strings.Repeat("问题", 300), "group")
	answer := strings.Repeat("中文😀", 1300)
	if err := p.Send(context.Background(), msg.ReplyCtx, answer); err != nil {
		t.Fatal(err)
	}
	var joined strings.Builder
	mentions, chunks := 0, len(frames)
	for range chunks {
		frame, text := groupReplyTestFrame(t, frames)
		if len(text) > 2000 || !utf8.ValidString(text) || frame.Headers.ReqID != "req-long" {
			t.Fatalf("invalid chunk: bytes=%d, frame=%+v", len(text), frame)
		}
		mentions += strings.Count(text, "<@alice>")
		_, body, found := strings.Cut(text, "\n\n")
		if strings.HasPrefix(text, "<@") {
			_, body, found = strings.Cut(body, "\n\n")
		}
		if !found || !strings.Contains(text, "…") {
			t.Fatalf("missing bounded question preview: %q", text)
		}
		joined.WriteString(body)
	}
	if joined.String() != answer || mentions != 1 || chunks < 2 {
		t.Fatalf("answer corrupted: chunks=%d mentions=%d bytes=%d", chunks, mentions, joined.Len())
	}
}

func TestWSGroupReply_PrivateAndProactiveMessagesKeepPlainContent(t *testing.T) {
	p, frames, messages := groupReplyTestPlatform(t, nil)
	private := groupReplyTestMessage(t, p, messages, "single", "alice", "private", "single")
	proactive, err := p.ReconstructReplyCtx("wecom:group1:alice")
	if err != nil {
		t.Fatal(err)
	}
	for _, rc := range []any{private.ReplyCtx, proactive} {
		if err := p.Send(context.Background(), rc, "plain"); err != nil {
			t.Fatal(err)
		}
		frame, text := groupReplyTestFrame(t, frames)
		if frame.Cmd != "aibot_send_msg" || text != "plain" {
			t.Fatalf("non-group-callback message changed: %s %q", frame.Cmd, text)
		}
	}
}

func TestWSGroupReply_QuoteAndMentionCanBeDisabledIndependently(t *testing.T) {
	for _, tc := range []struct {
		quote, mention bool
		want           string
	}{
		{true, false, "> question\n\nanswer"},
		{false, true, "<@alice>\n\nanswer"},
		{false, false, "answer"},
	} {
		t.Run(fmt.Sprintf("%t-%t", tc.quote, tc.mention), func(t *testing.T) {
			p, frames, messages := groupReplyTestPlatform(t, map[string]any{"group_reply_quote": tc.quote, "group_reply_mention": tc.mention})
			msg := groupReplyTestMessage(t, p, messages, "options", "alice", "question", "group")
			if err := p.Send(context.Background(), msg.ReplyCtx, "answer"); err != nil {
				t.Fatal(err)
			}
			_, text := groupReplyTestFrame(t, frames)
			if text != tc.want {
				t.Fatalf("content = %q, want %q", text, tc.want)
			}
		})
	}
}

func TestWSGroupReply_ConcurrentSendsForOneCallbackPreserveACKs(t *testing.T) {
	p, frames, messages := groupReplyTestPlatform(t, nil)
	msg := groupReplyTestMessage(t, p, messages, "concurrent", "alice", "question", "group")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if i%2 == 0 {
				errs <- p.Reply(ctx, msg.ReplyCtx, "answer")
			} else {
				errs <- p.Send(ctx, msg.ReplyCtx, "answer")
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(frames) != 12 {
		t.Fatalf("sent %d frames, want 12", len(frames))
	}
}

func TestWSGroupReply_ServerRejectionIsReturned(t *testing.T) {
	p, _, messages := groupReplyTestPlatform(t, nil, 40001)
	msg := groupReplyTestMessage(t, p, messages, "rejected", "alice", "question", "group")
	for _, send := range []func(context.Context, any, string) error{p.Reply, p.Send} {
		if err := send(context.Background(), msg.ReplyCtx, "answer"); err == nil || !strings.Contains(err.Error(), "40001") {
			t.Fatalf("expected server rejection, got %v", err)
		}
	}
}

func TestWSGroupReply_OversizedReplyKeepsCompleteAnswer(t *testing.T) {
	p, frames, messages := groupReplyTestPlatform(t, nil)
	msg := groupReplyTestMessage(t, p, messages, "oversized", "alice", "question", "group")
	answer := strings.Repeat("😀中文", 2100)
	if err := p.Reply(context.Background(), msg.ReplyCtx, answer); err != nil {
		t.Fatal(err)
	}
	var joined strings.Builder
	for n := len(frames); n > 0; n-- {
		frame, text := groupReplyTestFrame(t, frames)
		if frame.Cmd != "aibot_respond_msg" || len(text) > 2000 || !utf8.ValidString(text) {
			t.Fatalf("invalid overflow frame: %+v", frame)
		}
		if strings.HasPrefix(text, "<@alice>\n\n") {
			text = strings.TrimPrefix(text, "<@alice>\n\n")
		}
		if !strings.HasPrefix(text, "> question\n\n") {
			t.Fatalf("missing question: %q", text)
		}
		joined.WriteString(strings.TrimPrefix(text, "> question\n\n"))
	}
	if joined.String() != answer {
		t.Fatal("oversized reply truncated or corrupted")
	}
}
