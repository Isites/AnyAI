package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/Isites/anyai/internal/runtime/input"
	"github.com/Isites/anyai/internal/runtime/llm"
	tools "github.com/Isites/anyai/internal/runtime/tool"
)

func persistToolResultImages(ctx context.Context, toolCallID string, result tools.ToolResult) tools.ToolResult {
	if len(result.Images) == 0 {
		return result
	}
	meta := tools.RuntimeContextFrom(ctx)
	baseDir := strings.TrimSpace(meta.AssetsBaseDir)
	runID := strings.TrimSpace(meta.RunID)
	toolCallID = firstNonEmpty(strings.TrimSpace(toolCallID), strings.TrimSpace(meta.ToolCallID), "tool_call")
	if baseDir == "" || runID == "" {
		return result
	}

	store := input.NewAttachmentStore(baseDir)
	persisted := make([]llm.ImageContent, 0, len(result.Images))
	for i, img := range result.Images {
		if len(img.Data) == 0 {
			if strings.TrimSpace(img.Path) != "" {
				persisted = append(persisted, img)
			}
			continue
		}
		mimeType := input.ImageMIMEFromBytes(img.Data, img.MimeType)
		name := strings.TrimSpace(img.Name)
		if name == "" {
			name = fmt.Sprintf("image_%d%s", i+1, input.ExtensionForMIME(mimeType))
		}
		attachmentID := firstNonEmpty(strings.TrimSpace(img.ID), fmt.Sprintf("%s_image_%d", toolCallID, i+1))
		relativePath := filepath.Join(runID, "tool_results", toolCallID, name)
		att, err := store.SaveAt(attachmentID, relativePath, mimeType, img.Data)
		if err != nil {
			persisted = append(persisted, img)
			continue
		}
		img.ID = att.ID
		img.Name = att.Name
		img.MimeType = att.MimeType
		img.Path = att.Path
		img.Size = int(att.Size)
		persisted = append(persisted, img)
	}
	result.Images = persisted
	return result
}
