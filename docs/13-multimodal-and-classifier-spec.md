# 13. Image Generation & Classifier (Jev / System One) Specification

## 1. Overview

This specification defines how Nex-v3 introduces two non-chat model modalities—**Image Generation (`image`)** and **Structured Classifiers (`classifier`, e.g., TypeSafe Jev / System One)**—while preserving clean isolation from chat model selection, reusing existing provider credentials, and exposing ergonomic interfaces for both built-in tools and `codemode` scripts.

### Goals
1. **Unified Provider Credentials, Isolated Model Types**: Image and classifier models share provider configs (`api_key`, `base_url`, headers) with chat models, but are tagged with `ModelType` (`chat` | `image` | `classifier`) so non-chat models never leak into chat model selectors.
2. **Protocol-First `pkg/ai` Layer**:
   - `ImageProvider`: OpenAI Images API (`/v1/images/generations`, `/v1/images/edits`) and OpenRouter Image Generation (`/v1/chat/completions` with `modalities: ["image", "text"]`).
   - `ClassifierProvider`: TypeSafe System One protocol (`/v1/systemone`), which natively covers TypeSafe (`jev-latest`), OpenRouter (`typesafe/jev-*`), Cloudflare Workers AI (`typesafe/jev`), OpenCode Zen, and local `llama.cpp` (`/v1/systemone`).
3. **Dual Surface for Agents**:
   - **Built-in `generate_image` tool** (`pkg/harness/tools/generate_image.go`): Direct, reliable image generation/editing for any session (no MCP/codemode required). Saves generated images to disk and returns both file paths and inline `BlockImage` payloads for multimodal model inspection and Web/App UI rendering.
   - **Codemode Sandbox (`pkg/harness/codemode`)**: Exposes `models.generateImages()`, `models.classify()`, and `image(block)` for concurrent batch classification, visual inspection, and scripted workflows.
4. **Native Runner Integration (Jev Guardrail / Router)**:
   - Optional Jev-backed **Smart Permission Gate** in `pkg/runner` / `pkg/harness/tools` that evaluates gray-area tool calls (e.g. non-trivial bash commands) between deterministic static policy and interactive user approval.
   - Optional Jev-backed **Auto Model Router** hook that classifies turn complexity and risk to select a target model tier and `ThinkingEffort`.

---

## 2. `pkg/ai` Layer Design

### 2.1 Model Types
Extend `ModelMeta` in `pkg/ai/models_catalog.go`:
```go
type ModelType string

const (
    ModelTypeChat       ModelType = "chat"
    ModelTypeImage      ModelType = "image"
    ModelTypeClassifier ModelType = "classifier"
)
```
- Empty `Type` defaults to `ModelTypeChat` for full backwards compatibility with existing `models_seed.json`, `models.dev`, and user `providers` configs.
- Helper `EffectiveModelType(m ModelMeta) ModelType`.

### 2.2 Image Generation (`pkg/ai/image.go`)
```go
type ImageRequest struct {
    Model          string         `json:"model"`
    Input          []ContentBlock `json:"input"` // BlockText + optional BlockImage (references/edits)
    Size           string         `json:"size,omitempty"`
    AspectRatio    string         `json:"aspect_ratio,omitempty"`
    Quality        string         `json:"quality,omitempty"`
    Count          int            `json:"count,omitempty"`
}

type ImageResponse struct {
    Provider     string         `json:"provider,omitempty"`
    Model        string         `json:"model"`
    Output       []ContentBlock `json:"output"` // BlockImage (base64) + optional BlockText
    Usage        Usage          `json:"usage"`
    StopReason   string         `json:"stop_reason"` // "stop" | "error" | "aborted"
    ErrorMessage string         `json:"error_message,omitempty"`
}

type ImageProvider interface {
    GenerateImages(ctx context.Context, req *ImageRequest) (*ImageResponse, error)
}
```
Supported drivers:
- **`openai_images`**: `POST <baseURL>/images/generations` (text-only) or `POST <baseURL>/images/edits` (when reference images are provided). Handles both `b64_json` and `url` responses.
- **`openrouter_images`**: `POST <baseURL>/chat/completions` with `modalities: ["image", "text"]` and multimodal `messages` (text + `image_url` data URLs). Parses `choices[0].message.images[]` and `choices[0].message.content` into `ContentBlock`s.

### 2.3 Classifier / System One (`pkg/ai/classifier.go`)
```go
type ClassifierQuestionType string

const (
    QuestionTypeChoice ClassifierQuestionType = "choice"
    QuestionTypeBool   ClassifierQuestionType = "bool"
    QuestionTypeScore  ClassifierQuestionType = "score"
)

type ClassifierQuestion struct {
    Type         ClassifierQuestionType `json:"type"`
    Instructions string                 `json:"instructions,omitempty"`
    Choices      []string               `json:"choices,omitempty"`  // for "choice"
    Criteria     map[string]string      `json:"criteria,omitempty"` // for "bool" ("true"/"false") or "choice"
    Min          *float64               `json:"min,omitempty"`      // for "score"
    Max          *float64               `json:"max,omitempty"`      // for "score"
}

type ClassifierAnswer struct {
    Type          ClassifierQuestionType `json:"type"`
    Choice        string                 `json:"choice,omitempty"`
    Bool          *bool                  `json:"bool,omitempty"`
    Score         *float64               `json:"score,omitempty"`
    Probability   float64                `json:"probability,omitempty"`
    Probabilities map[string]float64     `json:"probabilities,omitempty"`
}

type ClassifyRequest struct {
    Model     string                        `json:"model"`
    State     any                           `json:"state"`
    Images    []ContentBlock                `json:"images,omitempty"`
    Questions map[string]ClassifierQuestion `json:"questions"`
}

type ClassifyResponse struct {
    Provider     string                      `json:"provider,omitempty"`
    Model        string                      `json:"model"`
    Answers      map[string]ClassifierAnswer `json:"answers"`
    Usage        Usage                       `json:"usage"`
    StopReason   string                      `json:"stop_reason"` // "stop" | "error" | "aborted"
    ErrorMessage string                      `json:"error_message,omitempty"`
}

type ClassifierProvider interface {
    Classify(ctx context.Context, req *ClassifyRequest) (*ClassifyResponse, error)
}
```
Supported driver:
- **`typesafe_system_one`**: `POST <baseURL>/systemone`.
  - Translates public `bool` questions to TypeSafe's `noul` wire format (`criteria: { true, false }`) and maps responses back to `{ type: "bool", bool, probability }`.
  - Supports optional image input via `assets` map (`TypeSafe-Danger-Zone-Images: true`) or `state-image-urls` (OpenRouter).
  - Never panics on malformed provider responses; returns structured `StopReason: "error"` with `ErrorMessage` while preserving reported `Usage`.

---

## 3. Built-in `generate_image` Tool (`pkg/harness/tools/generate_image.go`)

- **Tool Name**: `generate_image` (Category: `media`)
- **Parameters**:
  - `prompt` (string, required): Text description of the image to generate or edit.
  - `reference_images` ([]string, optional): Workspace-relative or absolute paths to local PNG/JPEG/WebP/GIF images used as editing/reference inputs.
  - `save_path` (string, optional): Output path relative to workspace. If omitted, saves to `<workspace>/.nex/generated/<timestamp>-<n>.<ext>` (or OS temp dir if workspace is read-only).
  - `size` (string, optional): e.g. `1024x1024`, `1536x1024`.
  - `aspect_ratio` (string, optional): e.g. `16:9`, `1:1`.
  - `quality` (string, optional): e.g. `standard`, `hd`, `high`.
- **Execution**:
  - Resolves configured `ImageProvider` + default image model.
  - Decodes returned base64 images, writes them to disk (`0600`), and returns a concise text summary with saved file path(s) + `ExtraBlocks: []ai.ContentBlock{...}` containing `ImageBase64Block` so both the LLM and Web/App UI render the generated image inline.
- **Availability**: Registered in the runner toolset whenever an image provider/model is configured (via provider config or env vars such as `NEX_IMAGE_MODEL` / `OPENROUTER_API_KEY` / `OPENAI_API_KEY`).

---

## 4. Codemode Sandbox Extensions (`pkg/harness/codemode`)

Extend the `goja` sandbox with optional `ModelBridge`:
- `models.classify(modelRef, { state, images?, questions }) -> Promise<ClassifyResponse>`
- `models.generateImages(modelRef, { input, size?, aspect_ratio?, quality? }) -> Promise<ImageResponse>`
- `image(blockOrDataUrl)`: Captures an image block (`{ type: "image", data, mimeType }` or `data:...` URL), writes it to a session temp file, appends its path to text output, and attaches the `ai.ImageBase64Block` to `ToolResult.ExtraBlocks`.
- Concurrency cap: max 4 concurrent `models.*` calls per script execution via semaphore.

---

## 5. Native Runner Integration (Jev Guardrail & Router)

### 5.1 Smart Bash / Tool Guardrail (`pkg/harness/tools/jev_guardrail.go`)
- Sits between deterministic `EvaluateBashPolicy()` and interactive `runnerApprover.RequestApproval()`.
- Deterministic `allow` and `deny` rules always run first (fast path, zero latency).
- For `ask` (gray-area) commands, if a `ClassifierProvider` is configured and enabled:
  - Sends `{ command, cwd, tool_name }` to Jev with a `bool` question `safe_to_auto_approve` (high bar: no destructive deletes, no credential exfiltration, no irreversible system mutations).
  - If `safe_to_auto_approve.bool == true && probability >= 0.85`, auto-approves without interrupting the user; otherwise falls through to interactive approval.

### 5.2 Auto Model Router (`pkg/runner/jev_router.go`)
- Optional pre-turn classifier hook when the session model is set to `auto` (or routing is enabled).
- Classifies the user prompt with Jev on 3 dimensions: `complexity` (`simple` | `moderate` | `complex`), `mistake_cost` (`low` | `high`), and `task_kind` (`qa` | `edit` | `architecture`).
- Maps the typed judgment to a configured model tier (`fast` / `balanced` / `reasoning`) and `ThinkingEffort`.

---

## 6. Phased Implementation Plan

1. **Phase 1 (`pkg/ai`)**:
   - Add `ModelType` to `ModelMeta` + `EffectiveModelType`.
   - Implement `pkg/ai/image.go` (`OpenAIImagesProvider`, `OpenRouterImagesProvider`) + unit tests.
   - Implement `pkg/ai/classifier.go` (`SystemOneClassifierProvider`) + unit tests.
2. **Phase 2 (`pkg/harness/tools` & `pkg/runner`)**:
   - Implement `pkg/harness/tools/generate_image.go` + unit tests.
   - Wire `ImageProvider` resolution in `pkg/runner/wiring.go` / `execute.go` so `generate_image` is automatically available when configured.
3. **Phase 3 (`pkg/harness/codemode`)**:
   - Add `ModelBridge`, `models.classify()`, `models.generateImages()`, and `image()` to `pkg/harness/codemode/sandbox.go` + unit tests.
4. **Phase 4 (Jev Guardrail & Router)**:
   - Implement `pkg/harness/tools/jev_guardrail.go` and `pkg/runner/jev_router.go` + unit tests.
   - Run full Go test suite (`go test ./...`) and commit/push.
