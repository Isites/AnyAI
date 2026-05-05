package gateway

import (
	"fmt"
	"strings"

	"github.com/Isites/anyai/internal/runtime/input"
	runtimeport "github.com/Isites/anyai/internal/runtime/runtimeport"
)

func runtimeChatType(chatType ChatType) runtimeport.ChatType {
	switch chatType {
	case ChatTypeGroup:
		return runtimeport.ChatTypeGroup
	case ChatTypeDirect:
		fallthrough
	default:
		return runtimeport.ChatTypeDirect
	}
}

func ingressRequestForInboundMessage(channelName string, msg InboundMessage) runtimeport.IngressRequest {
	sessionID := sessionIDForInboundMessage(channelName, msg)
	envelope := envelopeFromInboundMessage(msg, sessionID)
	return runtimeport.IngressRequest{
		Channel:   strings.TrimSpace(channelName),
		SenderID:  strings.TrimSpace(msg.SenderID),
		AccountID: strings.TrimSpace(msg.AccountID),
		ChatType:  runtimeChatType(msg.ChatType),
		Envelope:  envelope,
		SessionID: sessionID,
	}
}

func envelopeFromInboundMessage(msg InboundMessage, sessionID string) input.InputEnvelope {
	blocks := make([]input.InputBlock, 0, len(msg.Blocks)+len(msg.Media)+1)
	if strings.TrimSpace(msg.Text) != "" {
		blocks = append(blocks, input.InputBlock{Type: "text", Text: msg.Text})
	}
	for _, media := range msg.Media {
		blockType := mediaAttachmentBlockType(media)
		if blockType == "" {
			continue
		}
		blocks = append(blocks, input.InputBlock{
			Type:     blockType,
			Name:     media.FileName,
			MimeType: media.MimeType,
			Data:     media.Data,
		})
	}
	if len(msg.Blocks) > 0 {
		blocks = append(blocks, msg.Blocks...)
	}
	return input.InputEnvelope{
		SessionID: strings.TrimSpace(sessionID),
		Blocks:    blocks,
	}
}

func sessionIDForInboundMessage(channelName string, msg InboundMessage) string {
	conversationID := sanitizeSessionIDPart(firstNonEmpty(strings.TrimSpace(msg.AccountID), strings.TrimSpace(msg.SenderID)))
	if msg.ChatType == ChatTypeGroup {
		return fmt.Sprintf("%s_group_%s", strings.TrimSpace(channelName), conversationID)
	}
	return fmt.Sprintf("%s_%s", strings.TrimSpace(channelName), conversationID)
}

func mediaAttachmentBlockType(media MediaAttachment) string {
	switch strings.ToLower(strings.TrimSpace(media.Type)) {
	case "image":
		return "image"
	case "pdf":
		return "pdf"
	case "file", "document":
		return "file"
	default:
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(media.MimeType)), "image/") {
			return "image"
		}
		if strings.EqualFold(strings.TrimSpace(media.MimeType), "application/pdf") {
			return "pdf"
		}
		if len(media.Data) > 0 || strings.TrimSpace(media.FileName) != "" {
			return "file"
		}
		return ""
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func sanitizeSessionIDPart(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "unknown"
	}

	var out []rune
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z':
			out = append(out, r)
		case r >= 'A' && r <= 'Z':
			out = append(out, r+'a'-'A')
		case r >= '0' && r <= '9':
			out = append(out, r)
		case r == '-' || r == '_' || r == '.':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}

	key := strings.Trim(string(out), "_.")
	if key == "" {
		return "unknown"
	}
	return key
}
