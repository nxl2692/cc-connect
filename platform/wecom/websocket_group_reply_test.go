package wecom

import (
	"context"
	"encoding/json"
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
		msg    *core.Message
		answer string
	}{
		{b, "回答问题乙"}, {a, "回答问题甲"}, {a2, "回答下个问题"},
	} {
		if err := p.Send(context.Background(), item.msg.ReplyCtx, item.answer); err != nil {
			t.Fatal(err)
		}
		frame, text := groupReplyTestFrame(t, frames)
		if frame.Cmd != "aibot_respond_msg" || frame.Headers.ReqID != "req-"+item.msg.MessageID {
			t.Errorf("follow-up lost originating callback: %+v", frame)
		}
		if text != item.answer {
			t.Errorf("group answer = %q, want %q", text, item.answer)
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
	if frame.Cmd != "aibot_respond_msg" || frame.Headers.ReqID != "req-reply" || text != "处理结果" {
		t.Fatalf("reply attribution = %q, frame=%+v", text, frame)
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
		if frame.Cmd != "aibot_respond_msg" || frame.Headers.ReqID != "req-oversized" || len(text) > 2000 || !utf8.ValidString(text) {
			t.Fatalf("invalid overflow frame: %+v", frame)
		}
		joined.WriteString(text)
	}
	if joined.String() != answer {
		t.Fatal("oversized reply truncated or corrupted")
	}
}

// Regression: smart-bot Markdown replies render <@opaque-userid> literally.
// Default replies must preserve the question/request without leaking that marker.
func TestWSGroupReply_DefaultDoesNotExposeOpaqueUserIDAsMention(t *testing.T) {
	p, frames, messages := groupReplyTestPlatform(t, nil)
	const opaqueID = "wo_opaque_sender_123"
	msg := groupReplyTestMessage(t, p, messages, "opaque", opaqueID, "@bot 原问题", "group")
	for _, send := range []func(context.Context, any, string) error{p.Reply, p.Send} {
		if err := send(context.Background(), msg.ReplyCtx, "最终结果"); err != nil {
			t.Fatal(err)
		}
		frame, text := groupReplyTestFrame(t, frames)
		if frame.Cmd != "aibot_respond_msg" || frame.Headers.ReqID != "req-opaque" || text != "最终结果" {
			t.Fatalf("default reply has invalid mention or lost attribution: frame=%+v text=%q", frame, text)
		}
		if strings.Contains(text, opaqueID) || strings.Contains(text, "<@") {
			t.Fatalf("default reply exposed opaque mention: %q", text)
		}
	}
}

// Regression: the client already displays a native quote for aibot_respond_msg.
// Copying the current question and its nested quote into the body duplicates a
// potentially long chain of previous conversations above every answer.
func TestWSGroupReply_DefaultDoesNotDuplicateNativeQuote(t *testing.T) {
	p, frames, messages := groupReplyTestPlatform(t, nil)
	for _, item := range []struct{ id, user string }{{"alice-question", "alice"}, {"bob-question", "bob"}} {
		body := wsMsgCallbackBody{MsgID: item.id, ChatID: "group1", ChatType: "group", MsgType: "text",
			Quote: &wsQuoteBlock{MsgType: "text"}}
		body.From.UserID = item.user
		body.Text.Content = "@bot 这个删除任务几点完成的？"
		body.Quote.Text = &struct {
			Content string `json:"content"`
		}{Content: "@bot 这次是由哪次同步引起的？引用了更早的告警和分析结果"}
		p.handleMsgCallback(wsCallbackFrame(t, "req-"+item.id, body))
		var msg *core.Message
		select {
		case msg = <-messages:
		case <-time.After(time.Second):
			t.Fatal("callback did not deliver message")
		}
		if !strings.Contains(msg.ExtraContent, body.Quote.Text.Content) {
			t.Fatal("removing the outbound preview must preserve the agent's quoted context")
		}
		for _, send := range []func(context.Context, any, string) error{p.Reply, p.Send} {
			const answer = "截至当前，删除任务仍在运行。"
			if err := send(context.Background(), msg.ReplyCtx, answer); err != nil {
				t.Fatal(err)
			}
			frame, text := groupReplyTestFrame(t, frames)
			if frame.Cmd != "aibot_respond_msg" || frame.Headers.ReqID != "req-"+item.id || text != answer {
				t.Fatalf("native quote lost or body duplicated question: frame=%+v body=%q", frame, text)
			}
		}
	}
}

func TestWSGroupReply_DefaultLongAnswerKeepsOnlyAnswerAndRequestAssociation(t *testing.T) {
	p, frames, messages := groupReplyTestPlatform(t, nil)
	msg := groupReplyTestMessage(t, p, messages, "long-native-quote", "alice", "@bot 原问题和历史引用链", "group")
	// Legitimate quotes inside the answer must be preserved, not stripped.
	answer := "> 分析结果中有意保留的引用\n\n" + strings.Repeat("中文😀", 2500)
	if err := p.Reply(context.Background(), msg.ReplyCtx, answer); err != nil {
		t.Fatal(err)
	}
	var joined strings.Builder
	for n := len(frames); n > 0; n-- {
		frame, text := groupReplyTestFrame(t, frames)
		if frame.Cmd != "aibot_respond_msg" || frame.Headers.ReqID != "req-long-native-quote" || len(text) > 2000 || !utf8.ValidString(text) {
			t.Fatalf("invalid long-answer frame: %+v", frame)
		}
		joined.WriteString(text)
	}
	if joined.String() != answer {
		t.Fatal("answer was modified, truncated, or prefixed with a question preview")
	}
}
