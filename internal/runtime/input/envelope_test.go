package input

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestInputBlockValid(t *testing.T) {
	tests := []struct {
		name   string
		block  InputBlock
		wantOK bool
	}{
		{"text block", InputBlock{Type: "text", Text: "hello"}, true},
		{"file block", InputBlock{Type: "file", Path: "/tmp/test.txt", Name: "test.txt"}, true},
		{"dir block", InputBlock{Type: "dir", Path: "/tmp/mydir", Name: "mydir"}, true},
		{"image block", InputBlock{Type: "image", Name: "photo.jpg", MimeType: "image/jpeg"}, true},
		{"pdf block", InputBlock{Type: "pdf", Name: "doc.pdf", MimeType: "application/pdf"}, true},
		{"url block", InputBlock{Type: "url", URL: "https://example.com"}, true},
		{"invalid block type", InputBlock{Type: "unknown"}, false},
		{"empty type", InputBlock{Type: ""}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantOK, tt.block.Valid())
		})
	}
}

func TestInputEnvelopeTextContent(t *testing.T) {
	env := InputEnvelope{
		SessionID: "sess_1",
		Blocks: []InputBlock{
			{Type: "text", Text: "hello"},
			{Type: "text", Text: " world"},
		},
	}
	assert.Equal(t, "hello world", env.TextContent())
}

func TestInputEnvelopeTextContentEmpty(t *testing.T) {
	env := InputEnvelope{Blocks: []InputBlock{{Type: "image", Name: "a.jpg"}}}
	assert.Equal(t, "", env.TextContent())
}

func TestInputEnvelopeImageBlocks(t *testing.T) {
	env := InputEnvelope{
		Blocks: []InputBlock{
			{Type: "text", Text: "look at this"},
			{Type: "image", Name: "a.jpg", MimeType: "image/jpeg"},
			{Type: "image", Name: "b.png", MimeType: "image/png"},
		},
	}
	imgs := env.ImageBlocks()
	assert.Len(t, imgs, 2)
	assert.Equal(t, "a.jpg", imgs[0].Name)
	assert.Equal(t, "b.png", imgs[1].Name)
}

func TestInputEnvelopeImageBlocksEmpty(t *testing.T) {
	env := InputEnvelope{Blocks: []InputBlock{{Type: "text", Text: "hello"}}}
	imgs := env.ImageBlocks()
	assert.Nil(t, imgs)
}

func TestNewEnvelopeFromText(t *testing.T) {
	env := NewEnvelopeFromText("sess_1", "hello")
	assert.Equal(t, "sess_1", env.SessionID)
	assert.Len(t, env.Blocks, 1)
	assert.Equal(t, "text", env.Blocks[0].Type)
	assert.Equal(t, "hello", env.Blocks[0].Text)
	assert.Equal(t, "hello", env.TextContent())
}
