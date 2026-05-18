package session

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Isites/anyai/internal/runtime/input"
	"github.com/Isites/anyai/internal/runtime/llm"
)

// ImageRefs returns compact image metadata for session/replay persistence.
func ImageRefs(images []llm.ImageContent) []ImageData {
	if len(images) == 0 {
		return nil
	}
	out := make([]ImageData, 0, len(images))
	for i, img := range images {
		if len(img.Data) == 0 && strings.TrimSpace(img.MimeType) == "" && strings.TrimSpace(img.Path) == "" {
			continue
		}
		size := img.Size
		if size <= 0 {
			size = len(img.Data)
		}
		if size <= 0 && strings.TrimSpace(img.Path) != "" {
			if info, err := os.Stat(strings.TrimSpace(img.Path)); err == nil && !info.IsDir() {
				size = int(info.Size())
			}
		}
		name := strings.TrimSpace(img.Name)
		if name == "" && strings.TrimSpace(img.Path) != "" {
			name = filepath.Base(strings.TrimSpace(img.Path))
		}
		out = append(out, ImageData{
			ID:       firstNonEmptyImageString(strings.TrimSpace(img.ID), fmt.Sprintf("image_%d", i+1)),
			MimeType: strings.TrimSpace(img.MimeType),
			Path:     strings.TrimSpace(img.Path),
			Name:     name,
			Size:     size,
		})
	}
	return out
}

// ImageRefsFromBlocks returns compact references to persisted image input
// blocks. It is used for session/events durability so large image bytes stay
// in the assets store instead of JSONL records.
func ImageRefsFromBlocks(blocks []input.InputBlock) []ImageData {
	if len(blocks) == 0 {
		return nil
	}
	out := make([]ImageData, 0, len(blocks))
	for _, block := range blocks {
		if strings.TrimSpace(block.Type) != "image" {
			continue
		}
		path := strings.TrimSpace(block.Path)
		id := strings.TrimSpace(block.AttachmentID)
		if id == "" && block.Meta != nil {
			id, _ = block.Meta["attachment_id"].(string)
			id = strings.TrimSpace(id)
		}
		image := ImageData{
			ID:       id,
			MimeType: strings.TrimSpace(block.MimeType),
			Path:     path,
			Name:     strings.TrimSpace(block.Name),
		}
		if image.Name == "" && path != "" {
			image.Name = filepath.Base(path)
		}
		if size, ok := block.Meta["size"].(int64); ok && size > 0 {
			image.Size = int(size)
		} else if size, ok := block.Meta["size"].(int); ok && size > 0 {
			image.Size = size
		} else if size, ok := block.Meta["size"].(float64); ok && size > 0 {
			image.Size = int(size)
		} else if path != "" {
			if info, err := os.Stat(path); err == nil && !info.IsDir() {
				image.Size = int(info.Size())
			}
		}
		if image.ID == "" && image.MimeType == "" && image.Path == "" && image.Name == "" && image.Size == 0 {
			continue
		}
		out = append(out, image)
	}
	return out
}

func firstNonEmptyImageString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// ImagePayloads returns legacy base64 image payloads for in-memory LLM turns.
func ImagePayloads(images []llm.ImageContent) []ImageData {
	if len(images) == 0 {
		return nil
	}
	out := make([]ImageData, 0, len(images))
	for _, img := range images {
		if len(img.Data) == 0 {
			continue
		}
		out = append(out, ImageData{
			MimeType: strings.TrimSpace(img.MimeType),
			Data:     base64.StdEncoding.EncodeToString(img.Data),
			Size:     len(img.Data),
		})
	}
	return out
}
