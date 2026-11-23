package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nex-harness/nex/pkg/ai"
)

type stubImageProvider struct {
	lastReq *ai.ImageRequest
	resp    *ai.ImageResponse
}

func (s *stubImageProvider) GenerateImages(_ context.Context, req *ai.ImageRequest) (*ai.ImageResponse, error) {
	s.lastReq = req
	return s.resp, nil
}

func TestGenerateImageToolSavesAndReturnsInlineBlock(t *testing.T) {
	dir := t.TempDir()
	refPath := filepath.Join(dir, "ref.png")
	if err := os.WriteFile(refPath, []byte("fake-ref-png"), 0o600); err != nil {
		t.Fatal(err)
	}

	outImgB64 := base64.StdEncoding.EncodeToString([]byte("generated-png-bytes"))
	stub := &stubImageProvider{
		resp: &ai.ImageResponse{
			Provider:   "openrouter",
			Model:      "google/gemini-2.5-flash-image",
			StopReason: "stop",
			Output: []ai.ContentBlock{
				ai.ImageBase64Block("image/png", outImgB64),
				ai.TextBlock("Done!"),
			},
		},
	}

	tool := NewGenerateImageTool(func(_ context.Context, requested string) (ai.ImageProvider, string, error) {
		if requested != "" {
			return stub, requested, nil
		}
		return stub, "google/gemini-2.5-flash-image", nil
	})

	args, _ := json.Marshal(map[string]any{
		"prompt":           "A red fox in the snow",
		"reference_images": []string{"ref.png"},
		"save_path":        "out/fox.png",
		"aspect_ratio":     "16:9",
	})
	res, err := tool.Execute(context.Background(), dir, args, nil)
	if err != nil {
		t.Fatalf("Execute err: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", res.Output)
	}
	if len(res.ExtraBlocks) != 1 || res.ExtraBlocks[0].Type != ai.BlockImage || res.ExtraBlocks[0].DataB64 != outImgB64 {
		t.Fatalf("expected 1 inline image block, got %+v", res.ExtraBlocks)
	}
	if len(stub.lastReq.Input) != 2 || stub.lastReq.Input[1].Type != ai.BlockImage {
		t.Fatalf("expected text + reference image input, got %+v", stub.lastReq.Input)
	}

	savedPath := filepath.Join(dir, "out", "fox.png")
	data, readErr := os.ReadFile(savedPath)
	if readErr != nil {
		t.Fatalf("expected saved file at %s: %v", savedPath, readErr)
	}
	if string(data) != "generated-png-bytes" {
		t.Fatalf("unexpected saved bytes: %q", string(data))
	}
	if !strings.Contains(res.Output, savedPath) {
		t.Fatalf("expected output to mention saved path, got: %s", res.Output)
	}
}
