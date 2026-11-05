package wecom

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestWSGroupReply_FollowupsKeepOriginatingMessage(t *testing.T) {
	frames := make(chan wsFrame, 3)
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
			if err := conn.WriteJSON(map[string]any{"headers": frame.Headers, "errcode": 0}); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	p, messages := newCapturedWSPlatform()
	p.conn = conn
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			var ack wsFrame
			if err := conn.ReadJSON(&ack); err != nil {
				return
			}
			p.handleFrame(ack)
		}
	}()
	t.Cleanup(func() { conn.Close(); <-readerDone })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var replies []any
	for _, item := range []struct{ id, user string }{{"a", "alice"}, {"b", "bob"}, {"c", "alice"}} {
		body := wsMsgCallbackBody{MsgID: item.id, ChatID: "group1", ChatType: "group", MsgType: "text"}
		body.From.UserID, body.Text.Content = item.user, "@bot question "+item.id
		p.handleMsgCallback(wsCallbackFrame(t, "req-"+item.id, body))
		select {
		case msg := <-messages:
			replies = append(replies, msg.ReplyCtx)
		case <-ctx.Done():
			t.Fatal("callback did not deliver message")
		}
	}
	// Reply in a different order after all questions have arrived.
	for _, i := range []int{1, 0, 2} {
		reqID := []string{"req-a", "req-b", "req-c"}[i]
		answer := "answer for " + reqID
		if err := p.Send(ctx, replies[i], answer); err != nil {
			t.Fatal(err)
		}
		select {
		case frame := <-frames:
			var body struct {
				ChatID   string `json:"chatid"`
				Markdown struct {
					Content string `json:"content"`
				} `json:"markdown"`
			}
			if err := json.Unmarshal(frame.Body, &body); err != nil {
				t.Fatal(err)
			}
			if frame.Cmd != "aibot_respond_msg" || frame.Headers.ReqID != reqID || body.ChatID != "" || body.Markdown.Content != answer {
				t.Fatalf("reply lost originating message or changed answer: %+v body=%s", frame, frame.Body)
			}
		case <-ctx.Done():
			t.Fatal("outbound reply missing")
		}
	}
}

func TestWSGroupReply_LateACKCannotConfirmNextSend(t *testing.T) {
	for _, mode := range []string{"timeout", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			firstCtx, cancelFirst := context.WithCancel(context.Background())
			defer cancelFirst()
			frames := make(chan wsFrame, 1)
			upgrader := websocket.Upgrader{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer conn.Close()
				var first, next wsFrame
				if err := conn.ReadJSON(&first); err != nil {
					return
				}
				if mode == "cancel" {
					cancelFirst()
				}
				// Hold the old ACK until the next message has been sent.
				if err := conn.ReadJSON(&next); err != nil {
					return
				}
				frames <- next
				if err := conn.WriteJSON(map[string]any{"headers": first.Headers, "errcode": 0}); err != nil {
					return
				}
				_ = conn.WriteJSON(map[string]any{"headers": next.Headers, "errcode": 40001})
			}))
			t.Cleanup(server.Close)
			conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
			if err != nil {
				t.Fatal(err)
			}
			p, messages := newCapturedWSPlatform()
			p.conn = conn
			readerDone := make(chan struct{})
			go func() {
				defer close(readerDone)
				for {
					var ack wsFrame
					if err := conn.ReadJSON(&ack); err != nil {
						return
					}
					p.handleFrame(ack)
				}
			}()
			t.Cleanup(func() { conn.Close(); <-readerDone })
			body := wsMsgCallbackBody{MsgID: "late-ack", ChatID: "group1", ChatType: "group", MsgType: "text"}
			body.From.UserID, body.Text.Content = "alice", "question"
			p.handleMsgCallback(wsCallbackFrame(t, "req-late-ack", body))
			var replyCtx any
			select {
			case msg := <-messages:
				replyCtx = msg.ReplyCtx
			case <-time.After(time.Second):
				t.Fatal("callback did not deliver message")
			}
			first, next := p.Reply, p.Send
			if mode == "cancel" {
				first, next = p.Send, p.Reply
			}
			if err := first(firstCtx, replyCtx, "first answer"); err == nil {
				t.Fatal("missing ACK must return an error")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := next(ctx, replyCtx, "next answer"); err == nil || !strings.Contains(err.Error(), "40001") {
				t.Fatalf("late ACK hid the next message's rejection: %v", err)
			}
			select {
			case frame := <-frames:
				if frame.Cmd != "aibot_send_msg" || frame.Headers.ReqID == "req-late-ack" {
					t.Fatalf("failed callback ACK key was reused: %+v", frame)
				}
			case <-ctx.Done():
				t.Fatal("fallback message missing")
			}
		})
	}
}
