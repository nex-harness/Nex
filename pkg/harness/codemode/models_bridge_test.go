package codemode

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nex-harness/nex/pkg/ai"
)

type mockClassifier struct{}

func (m *mockClassifier) Classify(_ context.Context, req *ai.ClassifyRequest) (*ai.ClassifyResponse, error) {
	b := true
	return &ai.ClassifyResponse{
		Provider:   "typesafe",
		Model:      req.Model,
		StopReason: "stop",
		Answers: map[string]ai.ClassifierAnswer{
			"approved": {Type: ai.QuestionTypeBool, Bool: &b, Probability: 0.95},
		},
	}, nil
}

type mockImageGen struct{}

func (m *mockImageGen) GenerateImages(_ context.Context, req *ai.ImageRequest) (*ai.ImageResponse, error) {
	return &ai.ImageResponse{
		Provider:   "openrouter",
		Model:      req.Model,
		StopReason: "stop",
		Output: []ai.ContentBlock{
			ai.ImageBase64Block("image/png", "iVBORw0KGgo="),
		},
	}, nil
}

func TestCodemodeModelsClassifyAndGenerateImages(t *testing.T) {
	tool := NewTool(NewPool(nil), NewSessionKV(), nil)
	tool.SetModelBridge(&ModelBridge{
		ResolveClassifier: func(_ context.Context, modelID string) (ai.ClassifierProvider, string, error) {
			if modelID == "" {
				modelID = "jev-latest"
			}
			return &mockClassifier{}, modelID, nil
		},
		ResolveImage: func(_ context.Context, modelID string) (ai.ImageProvider, string, error) {
			if modelID == "" {
				modelID = "google/gemini-2.5-flash-image"
			}
			return &mockImageGen{}, modelID, nil
		},
	})

	script := `
		const c = await models.classify("jev-latest", {
			state: { message: "looks great" },
			questions: { approved: { type: "bool", instructions: "approved?" } }
		});
		const g = await models.generateImages("google/gemini-2.5-flash-image", {
			input: [{ type: "text", text: "a fox" }]
		});
		for (const blk of g.output) {
			if (blk.type === "image") image(blk);
		}
		return { approved: c.answers.approved.bool, prob: c.answers.approved.probability, imgs: g.output.length };
	`
	args, _ := json.Marshal(map[string]string{"code": script})
	res, err := tool.Execute(context.Background(), "", args, nil)
	if err != nil {
		t.Fatalf("Execute err: %v", err)
	}
	if res.IsError {
		t.Fatalf("script error: %s", res.Output)
	}
	if !strings.Contains(res.Output, `"approved": true`) {
		t.Fatalf("expected approved:true in output, got: %s", res.Output)
	}
	if len(res.ExtraBlocks) != 1 || res.ExtraBlocks[0].Type != ai.BlockImage || res.ExtraBlocks[0].DataB64 != "iVBORw0KGgo=" {
		t.Fatalf("expected 1 image extra block, got: %+v", res.ExtraBlocks)
	}
}
