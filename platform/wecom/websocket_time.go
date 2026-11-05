package wecom

import (
	"strings"
	"time"
)

// wsMessageTime accepts the official Unix-seconds format and the Unix-
// milliseconds variant used by some compatible gateways.
func wsMessageTime(timestamp int64) time.Time {
	if timestamp >= 1_000_000_000_000 {
		return time.UnixMilli(timestamp)
	}
	return time.Unix(timestamp, 0)
}

// formatWSInboundContext adds model-facing metadata, not a platform UI message.
// Keep this separate from user text so commands, mentions and quoted attachments
// retain their existing behavior. Never substitute the callback's create_time
// for an unavailable original-message timestamp.
func formatWSInboundContext(body *wsMsgCallbackBody, quoted wsInboundParts) string {
	quotedContent := formatWSQuotedContent(quoted)
	if body == nil {
		return quotedContent
	}
	var metadata []string
	if body.CreateTime > 0 {
		metadata = append(metadata, "current_message_sent_at: "+wsMessageTime(body.CreateTime).Local().Format(time.RFC3339Nano))
	}
	if body.Quote != nil {
		if body.Quote.CreateTime > 0 {
			metadata = append(metadata, "quoted_message_sent_at: "+wsMessageTime(body.Quote.CreateTime).Local().Format(time.RFC3339Nano))
		} else {
			metadata = append(metadata, "quoted_message_sent_at: unavailable (not supplied by WeCom)")
		}
		metadata = append(metadata,
			"current_message_sent_at refers only to this @/reply, not to the quoted message or an alert's trigger time.",
			"A quoted message's sending time may also differ from an alert's trigger time. Use an event time in the quoted text or look up its alert ID/link when accessible; do not invent a missing timestamp.")
	}
	if len(metadata) == 0 {
		return quotedContent
	}
	return "[WeCom message metadata]:\n" + strings.Join(metadata, "\n") + "\n\n" + quotedContent
}
