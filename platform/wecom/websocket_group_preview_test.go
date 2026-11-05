package wecom

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestWSQuestionPreview_VoiceMixedMediaAndQuotedMessage(t *testing.T) {
	for _, tc := range []struct{ name, raw, want string }{
		{"voice", `{"msgtype":"voice","voice":{"content":"@bot 语音问题"}}`, "语音问题"},
		{"media", `{"msgtype":"mixed","mixed":{"msg_item":[{"msgtype":"text","text":{"content":"@bot 看附件"}},{"msgtype":"image","image":{"url":"https://example.com/i"}},{"msgtype":"file","file":{"url":"https://example.com/f"}}]}}`, "看附件 📷 📎"},
		{"quote-only", `{"msgtype":"text","text":{"content":"@bot"},"quote":{"msgtype":"text","text":{"content":"原始告警"}}}`, "“原始告警”"},
		{"instruction-and-quote", `{"msgtype":"text","text":{"content":"@bot 分析这个"},"quote":{"msgtype":"text","text":{"content":"原始告警"}}}`, "分析这个 “原始告警”"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body wsMsgCallbackBody
			if err := json.Unmarshal([]byte(tc.raw), &body); err != nil {
				t.Fatal(err)
			}
			current, quoted := wsCollectInboundParts(&body)
			if got := wsQuestionPreview(&body, current, quoted, "bot"); got != tc.want {
				t.Fatalf("preview = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWSQuestionPreview_EscapesCopiedMentionsAndMarkdown(t *testing.T) {
	body := &wsMsgCallbackBody{MsgType: "text"}
	body.Text.Content = "@bot <@all>\n![image](https://example.com)\n> **bold** & `code`\x00"
	current, quoted := wsCollectInboundParts(body)
	got := wsQuestionPreview(body, current, quoted, "bot")
	want := "&lt;@all&gt; \\!\\[image\\]\\(https://example.com\\) &gt; \\*\\*bold\\*\\* &amp; \\`code\\`"
	if got != want {
		t.Fatalf("preview = %q, want %q", got, want)
	}
	for _, question := range []string{strings.Repeat("😀", 300), strings.Repeat("<&*", 300)} {
		body.Text.Content = question
		current, quoted = wsCollectInboundParts(body)
		got = wsQuestionPreview(body, current, quoted, "bot")
		if len(got) > 512 || !utf8.ValidString(got) || !strings.HasSuffix(got, "…") {
			t.Fatalf("invalid bounded preview: bytes=%d, %q", len(got), got)
		}
	}
}

func TestWSGroupReply_DoesNotMentionAllOrMalformedUserIDs(t *testing.T) {
	p := &WSPlatform{groupReplyMention: true}
	for _, user := range []string{"", "all", "ALL", "bad>\n<@all", "space user", "bad\x00user", strings.Repeat("a", 257)} {
		if got := p.wsGroupReplyPrefix(wsReplyContext{chatType: "group", reqID: "req", userID: user}, true); got != "" {
			t.Fatalf("unexpected mention for %q: %q", user, got)
		}
	}
}

func TestWSGroupReply_RejectsInvalidOptionTypes(t *testing.T) {
	for _, key := range []string{"group_reply_quote", "group_reply_mention"} {
		_, err := newWebSocket(map[string]any{"bot_id": "bot", "bot_secret": "secret", key: "true"})
		if err == nil || !strings.Contains(err.Error(), key) {
			t.Fatalf("invalid %s accepted: %v", key, err)
		}
	}
}
