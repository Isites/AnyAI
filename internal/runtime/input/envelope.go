package input

import "strings"

// ValidBlockTypes lists all recognized input block types.
var ValidBlockTypes = map[string]bool{
	"text":  true,
	"file":  true,
	"dir":   true,
	"image": true,
	"pdf":   true,
	"url":   true,
}

// InputEnvelope is the unified input model for all external entry points.
type InputEnvelope struct {
	SessionID string
	Blocks    []InputBlock
}

// InputBlock represents a single typed input element.
type InputBlock struct {
	ID           string         `json:"id,omitempty"`
	Type         string         `json:"type"`
	Name         string         `json:"name,omitempty"`
	Text         string         `json:"text,omitempty"`
	Path         string         `json:"path,omitempty"`
	AttachmentID string         `json:"attachment_id,omitempty"`
	URL          string         `json:"url,omitempty"`
	MimeType     string         `json:"mime_type,omitempty"`
	Data         []byte         `json:"data,omitempty"`
	Meta         map[string]any `json:"meta,omitempty"`
}

// Valid returns true if the block type is recognized.
func (b InputBlock) Valid() bool {
	return ValidBlockTypes[b.Type]
}

// TextContent returns the concatenation of all text block contents.
func (e InputEnvelope) TextContent() string {
	var sb strings.Builder
	for _, b := range e.Blocks {
		if b.Type == "text" && b.Text != "" {
			sb.WriteString(b.Text)
		}
	}
	return sb.String()
}

// ImageBlocks returns all image-type blocks.
func (e InputEnvelope) ImageBlocks() []InputBlock {
	var imgs []InputBlock
	for _, b := range e.Blocks {
		if b.Type == "image" {
			imgs = append(imgs, b)
		}
	}
	return imgs
}

// NewEnvelopeFromText creates a single-block text envelope.
func NewEnvelopeFromText(sessionID, text string) InputEnvelope {
	return InputEnvelope{
		SessionID: sessionID,
		Blocks:    []InputBlock{{Type: "text", Text: text}},
	}
}
