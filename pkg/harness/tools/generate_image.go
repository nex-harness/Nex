package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nex-harness/nex/pkg/ai"
)

// ImageModelResolver resolves an ImageProvider and default model ID at execution time.
type ImageModelResolver func(ctx context.Context, requestedModel string) (ai.ImageProvider, string, error)

// GenerateImageTool exposes one-shot image generation & editing directly to the agent.
type GenerateImageTool struct {
	resolve ImageModelResolver
}

// NewGenerateImageTool constructs a built-in generate_image tool backed by resolve.
func NewGenerateImageTool(resolve ImageModelResolver) *GenerateImageTool {
	return &GenerateImageTool{resolve: resolve}
}

func (t *GenerateImageTool) Name() string     { return "generate_image" }
func (t *GenerateImageTool) Category() string { return "media" }
func (t *GenerateImageTool) IsReadOnly(_ json.RawMessage) bool {
	return false // writes generated image file(s) to disk
}

func (t *GenerateImageTool) Definition() ai.ToolDefinition {
	return ai.ToolDefinition{
		Name:     "generate_image",
		Category: "media",
		Description: "Generate or edit images using the configured image generation model.\n\n" +
			"- Pass `prompt` describing the desired image, and optionally `reference_images` (local file paths) to edit or use as visual references.\n" +
			"- Generated images are saved to `save_path` (or `.nex/generated/` under the workspace) and returned inline so both you and the user can inspect them.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"prompt": {
					"type": "string",
					"description": "Text description of the image to generate or the edit to apply"
				},
				"reference_images": {
					"type": "array",
					"items": { "type": "string" },
					"description": "Optional workspace-relative or absolute paths to local PNG/JPEG/WebP/GIF images for editing or reference"
				},
				"save_path": {
					"type": "string",
					"description": "Optional destination path for the generated image (relative to workspace or absolute)"
				},
				"model": {
					"type": "string",
					"description": "Optional image model ID override"
				},
				"size": {
					"type": "string",
					"description": "Optional output size such as 1024x1024 or 1536x1024"
				},
				"aspect_ratio": {
					"type": "string",
					"description": "Optional aspect ratio such as 1:1, 16:9, or 4:3"
				},
				"quality": {
					"type": "string",
					"description": "Optional quality hint such as standard, hd, or high"
				}
			},
			"required": ["prompt"]
		}`),
	}
}

func (t *GenerateImageTool) Execute(ctx context.Context, workDir string, rawArgs json.RawMessage, _ UpdateFunc) (Result, error) {
	var args struct {
		Prompt          string   `json:"prompt"`
		ReferenceImages []string `json:"reference_images"`
		SavePath        string   `json:"save_path"`
		Model           string   `json:"model"`
		Size            string   `json:"size"`
		AspectRatio     string   `json:"aspect_ratio"`
		Quality         string   `json:"quality"`
	}
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return Result{Output: fmt.Sprintf("invalid arguments: %v", err), IsError: true}, nil
	}
	if strings.TrimSpace(args.Prompt) == "" {
		return Result{Output: "prompt is required", IsError: true}, nil
	}
	if t.resolve == nil {
		return Result{Output: "no image generation provider configured", IsError: true}, nil
	}

	provider, modelID, err := t.resolve(ctx, strings.TrimSpace(args.Model))
	if err != nil {
		return Result{Output: fmt.Sprintf("resolve image provider: %v", err), IsError: true}, nil
	}

	inputBlocks := []ai.ContentBlock{ai.TextBlock(args.Prompt)}
	for _, ref := range args.ReferenceImages {
		abs := ResolvePath(workDir, ref)
		raw, readErr := os.ReadFile(abs)
		if readErr != nil {
			return Result{Output: fmt.Sprintf("read reference image %q: %v", ref, readErr), IsError: true}, nil
		}
		mime := detectImageMimeForGen(abs, raw)
		inputBlocks = append(inputBlocks, ai.ImageBase64Block(mime, base64.StdEncoding.EncodeToString(raw)))
	}

	resp, err := provider.GenerateImages(ctx, &ai.ImageRequest{
		Model:       modelID,
		Input:       inputBlocks,
		Size:        strings.TrimSpace(args.Size),
		AspectRatio: strings.TrimSpace(args.AspectRatio),
		Quality:     strings.TrimSpace(args.Quality),
	})
	if err != nil {
		return Result{Output: fmt.Sprintf("image generation failed: %v", err), IsError: true}, nil
	}
	if resp.StopReason != "stop" {
		msg := resp.ErrorMessage
		if msg == "" {
			msg = "stop_reason=" + resp.StopReason
		}
		return Result{Output: fmt.Sprintf("image generation error (%s): %s", modelID, msg), IsError: true}, nil
	}

	var savedPaths []string
	var extraBlocks []ai.ContentBlock
	var notes []string
	imgIdx := 0

	for _, blk := range resp.Output {
		switch blk.Type {
		case ai.BlockText:
			if strings.TrimSpace(blk.Text) != "" {
				notes = append(notes, strings.TrimSpace(blk.Text))
			}
		case ai.BlockImage:
			imgIdx++
			extraBlocks = append(extraBlocks, blk)
			if blk.DataB64 != "" {
				rawImg, decErr := base64.StdEncoding.DecodeString(blk.DataB64)
				if decErr == nil {
					dst := chooseImageSavePath(workDir, args.SavePath, imgIdx, blk.MimeType)
					if mkErr := os.MkdirAll(filepath.Dir(dst), 0o755); mkErr == nil {
						if wErr := os.WriteFile(dst, rawImg, 0o600); wErr == nil {
							savedPaths = append(savedPaths, dst)
						}
					}
				}
			}
		}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Generated %d image(s) with %s.", imgIdx, modelID)
	for _, p := range savedPaths {
		fmt.Fprintf(&sb, "\nSaved: %s", p)
	}
	for _, n := range notes {
		fmt.Fprintf(&sb, "\nNote: %s", n)
	}
	return Result{
		Output:      sb.String(),
		ExtraBlocks: extraBlocks,
		Metadata: map[string]any{
			"model":       modelID,
			"provider":    resp.Provider,
			"saved_paths": savedPaths,
			"usage":       resp.Usage,
		},
	}, nil
}

func chooseImageSavePath(workDir, requested string, idx int, mime string) string {
	ext := ".png"
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "image/jpeg", "image/jpg":
		ext = ".jpg"
	case "image/webp":
		ext = ".webp"
	case "image/gif":
		ext = ".gif"
	}
	if strings.TrimSpace(requested) != "" {
		abs := ResolvePath(workDir, requested)
		if idx > 1 {
			e := filepath.Ext(abs)
			base := strings.TrimSuffix(abs, e)
			if e == "" {
				e = ext
			}
			return fmt.Sprintf("%s-%d%s", base, idx, e)
		}
		return abs
	}
	baseDir := os.TempDir()
	if strings.TrimSpace(workDir) != "" {
		baseDir = filepath.Join(workDir, ".nex", "generated")
	}
	return filepath.Join(baseDir, fmt.Sprintf("img-%d-%d%s", time.Now().UnixMilli(), idx, ext))
}

func detectImageMimeForGen(path string, raw []byte) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	case ".png":
		return "image/png"
	}
	if len(raw) >= 3 && raw[0] == 0xFF && raw[1] == 0xD8 && raw[2] == 0xFF {
		return "image/jpeg"
	}
	return "image/png"
}
