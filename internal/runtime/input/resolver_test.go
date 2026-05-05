package input

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolveTextBlock(t *testing.T) {
	assert.Equal(t, "hello", ResolveBlock(InputBlock{Type: "text", Text: "hello"}))
}

func TestResolveFileBlock(t *testing.T) {
	summary := ResolveBlock(InputBlock{Type: "file", Name: "test.txt", MimeType: "text/plain"})
	assert.Contains(t, summary, "test.txt")
	assert.Contains(t, summary, "text/plain")
}

func TestResolveDirBlock(t *testing.T) {
	summary := ResolveBlock(InputBlock{Type: "dir", Name: "mydir"})
	assert.Contains(t, summary, "mydir")
}

func TestResolveURLBlock(t *testing.T) {
	summary := ResolveBlock(InputBlock{Type: "url", URL: "https://example.com/page"})
	assert.Contains(t, summary, "https://example.com/page")
}

func TestResolvePDFBlock(t *testing.T) {
	summary := ResolveBlock(InputBlock{Type: "pdf", Name: "report.pdf"})
	assert.Contains(t, summary, "report.pdf")
}

func TestResolveImageBlock(t *testing.T) {
	summary := ResolveBlock(InputBlock{Type: "image", Name: "photo.jpg"})
	assert.Contains(t, summary, "photo.jpg")
}

func TestResolveEnvelope(t *testing.T) {
	env := InputEnvelope{
		Blocks: []InputBlock{
			{Type: "text", Text: "hello"},
			{Type: "url", URL: "https://example.com"},
		},
	}
	summaries := ResolveEnvelope(env)
	assert.Len(t, summaries, 2)
	assert.Equal(t, "hello", summaries[0])
	assert.Contains(t, summaries[1], "https://example.com")
}
