package gateway

import (
	"fmt"
	"strings"

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

func gatewayChatType(chatType runtimeport.ChatType) ChatType {
	switch chatType {
	case runtimeport.ChatTypeGroup:
		return ChatTypeGroup
	case runtimeport.ChatTypeDirect:
		fallthrough
	default:
		return ChatTypeDirect
	}
}

func (s *Service) RuntimeIngressResolver() runtimeport.IngressAgentResolver {
	return func(req runtimeport.IngressRequest) string {
		return s.ResolveIngressAgent(gatewayIngressRequest(req))
	}
}

func ingressRequestForInboundMessage(channelName string, msg InboundMessage) IngressRequest {
	sessionID := sessionIDForInboundMessage(channelName, msg)
	blocks := inputBlocksFromInboundMessage(msg)
	return IngressRequest{
		Channel:   strings.TrimSpace(channelName),
		SenderID:  strings.TrimSpace(msg.SenderID),
		AccountID: strings.TrimSpace(msg.AccountID),
		ChatType:  msg.ChatType,
		Text:      msg.Text,
		MessageID: strings.TrimSpace(msg.MessageID),
		Inputs:    blocks,
		SessionID: sessionID,
	}
}

func inputBlocksFromInboundMessage(msg InboundMessage) []InputBlock {
	blocks := make([]InputBlock, 0, len(msg.Blocks)+len(msg.Media)+1)
	if strings.TrimSpace(msg.Text) != "" {
		blocks = append(blocks, InputBlock{Type: "text", Text: msg.Text})
	}
	for _, media := range msg.Media {
		blockType := mediaAttachmentBlockType(media)
		if blockType == "" {
			continue
		}
		blocks = append(blocks, InputBlock{
			Type:     blockType,
			Name:     media.FileName,
			MimeType: media.MimeType,
			Data:     media.Data,
		})
	}
	if len(msg.Blocks) > 0 {
		blocks = append(blocks, msg.Blocks...)
	}
	return blocks
}

func sessionIDForInboundMessage(channelName string, msg InboundMessage) string {
	if sessionID := strings.TrimSpace(msg.SessionID); sessionID != "" {
		return sessionID
	}
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
