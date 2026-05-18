package tools

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/Isites/anyai/internal/runtime/input"
	"github.com/Isites/anyai/internal/runtime/llm"
)

// RunInputAdapter exposes the current run's inputs as input/attachment tools.
type RunInputAdapter struct {
	manifest    []InputBlockInfo
	attachments map[string]AttachmentInfo
}

// NewRunInputAdapter builds tool-facing input metadata from an input envelope.
func NewRunInputAdapter(env input.InputEnvelope) *RunInputAdapter {
	adapter := &RunInputAdapter{
		manifest:    make([]InputBlockInfo, 0, len(env.Blocks)),
		attachments: map[string]AttachmentInfo{},
	}

	for _, block := range env.Blocks {
		info := InputBlockInfo{
			ID:           strings.TrimSpace(block.ID),
			Type:         block.Type,
			Name:         block.Name,
			Text:         block.Text,
			Path:         block.Path,
			AttachmentID: strings.TrimSpace(block.AttachmentID),
			URL:          block.URL,
			MimeType:     block.MimeType,
			Meta:         block.Meta,
		}

		if info.Name == "" {
			info.Name = defaultInputName(block)
		}
		if isAttachmentBlock(block.Type) {
			attachmentID := strings.TrimSpace(firstNonEmpty(info.AttachmentID, info.ID))
			if attachmentID == "" {
				attachmentID = NewOpaqueID("att")
			}
			info.AttachmentID = attachmentID
			if info.ID == "" {
				info.ID = attachmentID
			}
			adapter.attachments[attachmentID] = AttachmentInfo{
				ID:       attachmentID,
				Name:     info.Name,
				MimeType: resolveBlockMIME(block),
				Size:     blockSize(block),
				Path:     block.Path,
				Source:   stringMeta(block.Meta, "source_path"),
			}
		}

		adapter.manifest = append(adapter.manifest, info)
	}

	return adapter
}

// InputManifest returns the current run's input manifest.
func (a *RunInputAdapter) InputManifest() []InputBlockInfo {
	if a == nil {
		return nil
	}
	return append([]InputBlockInfo(nil), a.manifest...)
}

// GetAttachment resolves a referenced attachment from the current run.
func (a *RunInputAdapter) GetAttachment(id string) (AttachmentInfo, bool) {
	if a == nil {
		return AttachmentInfo{}, false
	}
	info, ok := a.attachments[id]
	return info, ok
}

// ResolveEnvelopeForRuntime converts a structured envelope into the runtime's
// text-plus-images input shape.
func ResolveEnvelopeForRuntime(env input.InputEnvelope) (string, []llm.ImageContent) {
	var parts []string
	var images []llm.ImageContent

	for _, block := range env.Blocks {
		if summary := strings.TrimSpace(input.ResolveBlock(block)); summary != "" {
			parts = append(parts, summary)
		}
		if block.Type != "image" {
			continue
		}
		data := block.Data
		if len(data) == 0 && block.Path != "" {
			fileData, err := os.ReadFile(block.Path)
			if err == nil {
				data = fileData
			}
		}
		if len(data) == 0 {
			continue
		}
		images = append(images, llm.ImageContent{
			MimeType: resolveBlockMIME(block),
			Data:     data,
		})
	}

	return strings.TrimSpace(strings.Join(parts, "\n")), images
}

func isAttachmentBlock(blockType string) bool {
	switch blockType {
	case "file", "image", "pdf", "dir":
		return true
	default:
		return false
	}
}

func defaultInputName(block input.InputBlock) string {
	switch {
	case block.Name != "":
		return block.Name
	case block.Path != "":
		return filepath.Base(block.Path)
	case block.URL != "":
		return block.URL
	case block.Type != "":
		return block.Type
	default:
		return "input"
	}
}

func resolveBlockMIME(block input.InputBlock) string {
	return input.ResolveMIME(block)
}

func blockSize(block input.InputBlock) int64 {
	if len(block.Data) > 0 {
		return int64(len(block.Data))
	}
	if block.Path == "" {
		return 0
	}
	info, err := os.Stat(block.Path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func stringMeta(meta map[string]any, key string) string {
	if len(meta) == 0 {
		return ""
	}
	if value, ok := meta[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}
