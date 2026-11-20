package agent

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"time"

	"github.com/nex-harness/nex/pkg/ai"
	"github.com/nex-harness/nex/pkg/store"
)

// Standard 6 Context Windows categories matching docs/06-storage-and-observability-spec.md.
const (
	CatMessages     = "messages"
	CatSystemTools  = "system_tools"
	CatSystemPrompt = "system_prompt"
	CatMCPTools     = "mcp_tools"
	CatSkills       = "skills"
	CatMetaContext  = "meta_context"
)

var orderedCategories = []string{
	CatMessages,
	CatSystemTools,
	CatSystemPrompt,
	CatMCPTools,
	CatSkills,
	CatMetaContext,
}

type rawTraceSnapshot struct {
	Model          string              `json:"model"`
	SystemPrompt   string              `json:"system_prompt,omitempty"`
	SystemSections []ai.SystemSection  `json:"system_sections,omitempty"`
	Tools          []ai.ToolDefinition `json:"tools,omitempty"`
	Messages       []ai.Message        `json:"messages"`
	Response       *ai.Message         `json:"response,omitempty"`
	Usage          ai.Usage            `json:"usage"`
}

// BuildTurnTelemetry computes the exact 6-category Context Windows breakdown,
// stores deduplicated prompt/trace blobs in st (when st != nil), and calculates cost & cache hit metrics.
func BuildTurnTelemetry(
	ctx context.Context,
	st *store.Store,
	meta TurnMeta,
	req *ai.StreamRequest,
	respMsg *ai.Message,
	usage ai.Usage,
	ttftMS, durationMS int64,
) store.TurnTelemetry {
	bytesByCat := map[string]int{
		CatMessages:     0,
		CatSystemTools:  0,
		CatSystemPrompt: 0,
		CatMCPTools:     0,
		CatSkills:       0,
		CatMetaContext:  0,
	}
	contentByCat := map[string]*strings.Builder{
		CatSystemTools:  {},
		CatSystemPrompt: {},
		CatMCPTools:     {},
		CatSkills:       {},
		CatMetaContext:  {},
	}

	if req.SystemPrompt != "" {
		bytesByCat[CatSystemPrompt] += len(req.SystemPrompt)
		contentByCat[CatSystemPrompt].WriteString(req.SystemPrompt)
	}
	for _, sec := range req.SystemSections {
		cat := sec.Category
		switch cat {
		case CatSystemPrompt, CatSkills, CatMCPTools, CatMetaContext:
		default:
			cat = CatMetaContext
		}
		bytesByCat[cat] += len(sec.Content)
		if b := contentByCat[cat]; b != nil {
			if b.Len() > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString(sec.Content)
		}
	}

	for _, td := range req.Tools {
		raw, _ := json.Marshal(td)
		cat := CatSystemTools
		if td.Category == "mcp" || td.Name == "codemode" {
			cat = CatMCPTools
		}
		bytesByCat[cat] += len(raw)
		if b := contentByCat[cat]; b != nil {
			b.Write(raw)
			b.WriteByte('\n')
		}
	}

	msgBytes, _ := json.Marshal(req.Messages)
	bytesByCat[CatMessages] = len(msgBytes)

	totalBytes := 0
	for _, cat := range orderedCategories {
		totalBytes += bytesByCat[cat]
	}

	totalPromptTokens := usage.TotalInputTokens()
	if totalPromptTokens <= 0 && totalBytes > 0 {
		// Fallback char/4 heuristic if upstream provider omitted token count
		totalPromptTokens = (totalBytes + 3) / 4
	}

	var sysPromptHash, rawTraceHash string
	blobByCat := make(map[string]string)

	if st != nil {
		if len(msgBytes) > 0 && string(msgBytes) != "null" && string(msgBytes) != "[]" {
			if h, _, err := st.PutBlob(ctx, msgBytes, "application/json", 0); err == nil {
				blobByCat[CatMessages] = h
			}
		}
		for _, cat := range []string{CatSystemPrompt, CatSystemTools, CatMCPTools, CatSkills, CatMetaContext} {
			txt := contentByCat[cat].String()
			if txt != "" {
				if h, _, err := st.PutBlob(ctx, []byte(txt), "text/plain; charset=utf-8", 0); err == nil {
					blobByCat[cat] = h
					if cat == CatSystemPrompt {
						sysPromptHash = h
					}
				}
			}
		}
		traceObj := rawTraceSnapshot{
			Model:          req.Model,
			SystemPrompt:   req.SystemPrompt,
			SystemSections: req.SystemSections,
			Tools:          req.Tools,
			Messages:       req.Messages,
			Response:       respMsg,
			Usage:          usage,
		}
		if traceBytes, err := json.Marshal(traceObj); err == nil {
			if h, _, err := st.PutBlob(ctx, traceBytes, "application/json", 0); err == nil {
				rawTraceHash = h
			}
		}
	}

	breakdown := make([]store.ContextCategoryItem, 0, len(orderedCategories))
	allocatedTokens := 0
	for i, cat := range orderedCategories {
		bLen := bytesByCat[cat]
		var pct float64
		var tok int
		if totalBytes > 0 {
			pct = math.Round((float64(bLen)*1000.0/float64(totalBytes))) / 10.0
			if i == len(orderedCategories)-1 {
				tok = totalPromptTokens - allocatedTokens
				if tok < 0 {
					tok = 0
				}
			} else {
				tok = int(math.Round(float64(totalPromptTokens) * float64(bLen) / float64(totalBytes)))
				allocatedTokens += tok
			}
		}
		breakdown = append(breakdown, store.ContextCategoryItem{
			Category: cat,
			Bytes:    bLen,
			Tokens:   tok,
			Pct:      pct,
			BlobHash: blobByCat[cat],
		})
	}

	ctxWindow := meta.ContextWindow
	if ctxWindow <= 0 {
		ctxWindow = 200000
	}
	winPct := math.Round(float64(totalPromptTokens)*1000.0/float64(ctxWindow)) / 10.0
	costEst := ai.CalculateCost(usage, meta.Pricing)

	return store.TurnTelemetry{
		SessionID:            meta.SessionID,
		TurnIdx:              meta.TurnIdx,
		RunnerID:             meta.RunnerID,
		ProviderID:           meta.ProviderID,
		ModelID:              req.Model,
		TriggerSource:        meta.TriggerSource,
		ContextWindow:        ctxWindow,
		TotalContextTokens:   totalPromptTokens,
		WindowUtilizationPct: winPct,
		InputTokens:          usage.InputTokens,
		OutputTokens:         usage.OutputTokens,
		ReasoningTokens:      usage.ReasoningTokens,
		CacheReadTokens:      usage.CacheReadTokens,
		CacheWriteTokens:     usage.CacheWriteTokens,
		CacheHitRatePct:      math.Round(usage.CacheHitRatePct()*10.0) / 10.0,
		EstimatedCostUSD:     costEst.EstimatedCostUSD,
		CacheSavedUSD:        costEst.CacheSavedUSD,
		Breakdown:            breakdown,
		SystemPromptBlobHash: sysPromptHash,
		RawTraceBlobHash:     rawTraceHash,
		TTFTMS:               ttftMS,
		DurationMS:           durationMS,
		RecordedAt:           time.Now().Unix(),
	}
}

// TurnMeta carries session & model metadata needed for telemetry accounting.
type TurnMeta struct {
	SessionID     string
	TurnIdx       int
	RunnerID      string
	ProviderID    string
	TriggerSource string // "chat" | "subagent" | "cron"
	ContextWindow int
	Pricing       ai.ModelPricing
}
