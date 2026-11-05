package wecom

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func contextTimestamp(t *testing.T, context, field string) time.Time {
	t.Helper()
	for _, line := range strings.Split(context, "\n") {
		if value, ok := strings.CutPrefix(line, field+": "); ok {
			parsed, err := time.Parse(time.RFC3339Nano, value)
			if err != nil {
				t.Fatalf("%s has no valid explicit timezone: %q: %v", field, value, err)
			}
			return parsed
		}
	}
	t.Fatalf("%s missing from context: %q", field, context)
	return time.Time{}
}

func TestFormatWSInboundContext_PreservesDistinctCurrentAndOriginalTimes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		raw  string
		want time.Time
	}{
		{"seconds", `{"create_time":1793354400,"quote":{"msgtype":"text","create_time":1793350800,"text":{"content":"alert"}}}`, time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)},
		{"milliseconds", `{"create_time":1793354400123,"quote":{"msgtype":"text","create_time":1793350800123,"text":{"content":"alert"}}}`, time.Date(2026, 10, 30, 10, 0, 0, 123000000, time.UTC)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var body wsMsgCallbackBody
			if err := json.Unmarshal([]byte(tc.raw), &body); err != nil {
				t.Fatal(err)
			}
			_, quoted := wsCollectInboundParts(&body)
			context := formatWSInboundContext(&body, quoted)
			current := contextTimestamp(t, context, "current_message_sent_at")
			original := contextTimestamp(t, context, "quoted_message_sent_at")
			if !current.Equal(tc.want) || !original.Equal(tc.want.Add(-time.Hour)) {
				t.Fatalf("current=%v original=%v, want %v and one hour earlier", current, original, tc.want)
			}
			if !strings.HasSuffix(context, "[Quoted message]:\nalert\n\n") {
				t.Fatalf("quote changed: %q", context)
			}
		})
	}
}

func TestFormatWSInboundContext_MissingTimeIsNeverInvented(t *testing.T) {
	t.Parallel()
	for _, timestamp := range []int64{0, -1} {
		body := &wsMsgCallbackBody{CreateTime: timestamp, Quote: &wsQuoteBlock{CreateTime: timestamp}}
		context := formatWSInboundContext(body, wsInboundParts{content: []string{"alert"}})
		if strings.Contains(context, "current_message_sent_at:") || strings.Contains(context, "1970-") || !strings.Contains(context, "quoted_message_sent_at: unavailable") {
			t.Fatalf("invented or ambiguous time: %q", context)
		}
	}
	if got := formatWSInboundContext(&wsMsgCallbackBody{}, wsInboundParts{}); got != "" {
		t.Fatalf("missing timestamps and quote should have no metadata: %q", got)
	}
}

func TestDeliverWSMediaInbound_TimestampAloneDoesNotCreateMessage(t *testing.T) {
	t.Parallel()
	p, captured := newCapturedWSPlatform()
	p.deliverWSMediaInbound(
		&wsMsgCallbackBody{MsgID: "empty-media", CreateTime: time.Now().Unix()},
		"wecom:group:user", "", wsReplyContext{}, wsInboundParts{}, wsInboundParts{}, false,
	)
	select {
	case msg := <-captured:
		t.Fatalf("metadata alone dispatched an empty message: %+v", msg)
	default:
	}
}

func TestHandleMsgCallback_TextAndVoiceIncludeCurrentMessageTime(t *testing.T) {
	t.Parallel()
	for _, msgType := range []string{"text", "voice"} {
		t.Run(msgType, func(t *testing.T) {
			t.Parallel()
			p, captured := newCapturedWSPlatform()
			body := wsMsgCallbackBody{
				MsgID: "timestamp-" + msgType, ChatID: "group", ChatType: "group",
				MsgType: msgType, CreateTime: time.Now().Unix(),
			}
			body.From.UserID = "user"
			body.Text.Content = "investigate"
			body.Voice.Content = "investigate"
			p.handleMsgCallback(wsCallbackFrame(t, "timestamp-"+msgType, body))
			select {
			case msg := <-captured:
				if got := contextTimestamp(t, msg.ExtraContent, "current_message_sent_at").Unix(); got != body.CreateTime {
					t.Fatalf("callback timestamp changed: %d, want %d", got, body.CreateTime)
				}
				if msg.Content != "investigate" || strings.Contains(msg.ExtraContent, "quoted_message_sent_at:") {
					t.Fatalf("unexpected text or invented quote: %+v", msg)
				}
			case <-time.After(time.Second):
				t.Fatal("handler not called")
			}
		})
	}
}

// Regression: a quoted alert has no original timestamp in the official payload.
// Its surrounding callback timestamp must not silently become the alert time.
func TestHandleMsgCallback_QuotedMessageTimeIsNotCurrentTime(t *testing.T) {
	t.Parallel()
	p, captured := newCapturedWSPlatform()
	body := wsMsgCallbackBody{
		MsgID: "quote-time-regression", ChatID: "group", ChatType: "group",
		MsgType: "text", CreateTime: time.Now().Unix(),
		Quote: &wsQuoteBlock{
			MsgType: "text",
			Text: &struct {
				Content string `json:"content"`
			}{Content: "CPU alert 10740"},
		},
	}
	body.From.UserID = "user"
	body.Text.Content = "investigate"
	p.handleMsgCallback(wsCallbackFrame(t, "quote-time-regression", body))
	select {
	case msg := <-captured:
		if !strings.Contains(msg.ExtraContent, "quoted_message_sent_at: unavailable") {
			t.Fatalf("missing explicit original-time availability: %q", msg.ExtraContent)
		}
		if !strings.Contains(msg.ExtraContent, "current_message_sent_at:") {
			t.Fatalf("current message timestamp was lost: %q", msg.ExtraContent)
		}
		if msg.Content != "investigate" || !strings.Contains(msg.ExtraContent, "[Quoted message]:\nCPU alert 10740\n\n") {
			t.Fatalf("message/quote separation changed: %+v", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("handler not called")
	}
}
