package wecom

import (
	"fmt"
	"strings"
	"unicode"
)

// Defaults apply in the factory; reject misspelled TOML types instead of
// silently disabling attribution.
func wsBoolOption(opts map[string]any, key string, fallback bool) (bool, error) {
	v, exists := opts[key]
	if !exists {
		return fallback, nil
	}
	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("wecom-ws: %s must be a boolean", key)
	}
	return b, nil
}

// Keep a snapshot on the reply context, never a platform-wide "last message":
// several users (or queued questions from one user) can be in flight at once.
func wsQuestionPreview(body *wsMsgCallbackBody, current, quoted wsInboundParts, botID string) string {
	parts := append([]string(nil), current.content...)
	if body.MsgType == "voice" {
		parts = append([]string{wsVoiceText(body.Voice)}, parts...)
	}
	if len(current.images) > 0 {
		parts = append(parts, "📷")
	}
	if len(current.files) > 0 {
		parts = append(parts, "📎")
	}
	text := stripWeComAtMentions(strings.Join(parts, " "), botID, body.AibotID)
	if len(quoted.content) > 0 {
		text += " “" + strings.Join(quoted.content, " ") + "”"
	}
	// Escape copied user content so it cannot create new mentions, links,
	// images or markdown blocks in the bot's answer. Limit after escaping.
	text = strings.Join(strings.Fields(text), " ")
	var preview strings.Builder
	const maxBytes = 512
	for _, r := range text {
		if unicode.IsControl(r) {
			continue
		}
		escaped := string(r)
		switch r {
		case '&':
			escaped = "&amp;"
		case '<':
			escaped = "&lt;"
		case '>':
			escaped = "&gt;"
		case '\\', '`', '*', '_', '[', ']', '(', ')', '!', '|', '#':
			escaped = "\\" + escaped
		}
		if preview.Len()+len(escaped) > maxBytes-len("…") {
			preview.WriteString("…")
			break
		}
		preview.WriteString(escaped)
	}
	return preview.String()
}

func (p *WSPlatform) wsGroupReplyPrefix(rc wsReplyContext, mention bool) string {
	// Reconstructed cron/relay contexts have no originating message and must
	// never acquire a quote or @ from an unrelated historical callback.
	if rc.chatType != "group" || rc.reqID == "" {
		return ""
	}
	var prefix strings.Builder
	if p.groupReplyMention && mention && wsMentionableUser(rc.userID) {
		// WeCom's member-mention extension; actual rendering/notification support
		// depends on the smart-bot endpoint/client and needs live verification.
		prefix.WriteString("<@" + rc.userID + ">\n\n")
	}
	if p.groupReplyQuote && rc.question != "" {
		prefix.WriteString("> " + rc.question + "\n\n")
	}
	return prefix.String()
}

func wsMentionableUser(userID string) bool {
	if userID == "" || len(userID) > 256 || strings.EqualFold(userID, "all") {
		return false
	}
	for _, r := range userID {
		if unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune("<>&", r) {
			return false
		}
	}
	return true
}
