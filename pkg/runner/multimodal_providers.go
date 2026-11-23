package runner

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/nex-harness/nex/pkg/ai"
)

// resolveImageProvider resolves an ImageProvider and default model ID from stored providers or environment variables.
// Precedence:
// 1. Explicit NEX_IMAGE_MODEL (with OpenRouter or OpenAI credentials)
// 2. Stored OpenRouter provider (default model: google/gemini-2.5-flash-image)
// 3. Stored OpenAI provider (default model: gpt-image-1)
// 4. OPENROUTER_API_KEY / OPENAI_API_KEY environment variables
func (n *Node) resolveImageProvider(ctx context.Context, requestedModel string) (ai.ImageProvider, string, error) {
	reqModel := strings.TrimSpace(requestedModel)
	if reqModel == "" {
		reqModel = strings.TrimSpace(os.Getenv("NEX_IMAGE_MODEL"))
	}

	if n != nil && n.store != nil {
		if providers, err := n.store.ListProviders(ctx); err == nil {
			// Prefer OpenRouter when configured or when model contains a slash (e.g. google/gemini-2.5-flash-image)
			for _, p := range providers {
				if !p.Enabled || strings.TrimSpace(p.APIKey) == "" {
					continue
				}
				idLower := strings.ToLower(p.ID)
				urlLower := strings.ToLower(p.BaseURL)
				if strings.Contains(idLower, "openrouter") || strings.Contains(urlLower, "openrouter.ai") {
					model := reqModel
					if model == "" {
						model = "google/gemini-2.5-flash-image"
					}
					return ai.NewOpenRouterImagesProvider(ai.ImageProviderConfig{
						ProviderID: p.ID,
						BaseURL:    p.BaseURL,
						APIKey:     p.APIKey,
					}), model, nil
				}
			}
			for _, p := range providers {
				if !p.Enabled || strings.TrimSpace(p.APIKey) == "" {
					continue
				}
				idLower := strings.ToLower(p.ID)
				urlLower := strings.ToLower(p.BaseURL)
				if idLower == "openai" || strings.Contains(urlLower, "api.openai.com") {
					model := reqModel
					if model == "" {
						model = "gpt-image-1"
					}
					return ai.NewOpenAIImagesProvider(ai.ImageProviderConfig{
						ProviderID: p.ID,
						BaseURL:    p.BaseURL,
						APIKey:     p.APIKey,
					}), model, nil
				}
			}
		}
	}

	if key := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY")); key != "" {
		model := reqModel
		if model == "" {
			model = "google/gemini-2.5-flash-image"
		}
		return ai.NewOpenRouterImagesProvider(ai.ImageProviderConfig{
			ProviderID: "openrouter",
			APIKey:     key,
		}), model, nil
	}
	if key := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")); key != "" {
		model := reqModel
		if model == "" {
			model = "gpt-image-1"
		}
		return ai.NewOpenAIImagesProvider(ai.ImageProviderConfig{
			ProviderID: "openai",
			APIKey:     key,
		}), model, nil
	}
	return nil, "", fmt.Errorf("no image generation provider configured (configure OpenRouter/OpenAI provider or set OPENROUTER_API_KEY / OPENAI_API_KEY)")
}

// resolveClassifierProvider resolves a TypeSafe System One (Jev) ClassifierProvider and default model ID.
// Precedence:
// 1. TYPESAFE_API_KEY -> https://api.typesafe.ai/v1 (jev-latest)
// 2. NEX_CLASSIFIER_BASE_URL (e.g. local llama.cpp http://127.0.0.1:9931/v1)
// 3. Stored OpenRouter provider or OPENROUTER_API_KEY -> https://openrouter.ai/api/v1 (typesafe/jev-1.14)
func (n *Node) resolveClassifierProvider(ctx context.Context, requestedModel string) (ai.ClassifierProvider, string, error) {
	reqModel := strings.TrimSpace(requestedModel)
	if reqModel == "" {
		reqModel = strings.TrimSpace(os.Getenv("NEX_CLASSIFIER_MODEL"))
	}

	if key := strings.TrimSpace(os.Getenv("TYPESAFE_API_KEY")); key != "" {
		model := reqModel
		if model == "" {
			model = "jev-latest"
		}
		return ai.NewSystemOneClassifierProvider(ai.SystemOneConfig{
			ProviderID: "typesafe",
			BaseURL:    strings.TrimSpace(os.Getenv("TYPESAFE_BASE_URL")),
			APIKey:     key,
			ImageMode:  ai.ClassifierImageAssets,
		}), model, nil
	}

	if baseURL := strings.TrimSpace(os.Getenv("NEX_CLASSIFIER_BASE_URL")); baseURL != "" {
		model := reqModel
		if model == "" {
			model = "jev-latest"
		}
		return ai.NewSystemOneClassifierProvider(ai.SystemOneConfig{
			ProviderID: "custom-classifier",
			BaseURL:    baseURL,
			APIKey:     strings.TrimSpace(os.Getenv("NEX_CLASSIFIER_API_KEY")),
		}), model, nil
	}

	if n != nil && n.store != nil {
		if providers, err := n.store.ListProviders(ctx); err == nil {
			for _, p := range providers {
				if !p.Enabled || strings.TrimSpace(p.APIKey) == "" {
					continue
				}
				idLower := strings.ToLower(p.ID)
				urlLower := strings.ToLower(p.BaseURL)
				if strings.Contains(idLower, "typesafe") || strings.Contains(urlLower, "typesafe.ai") {
					model := reqModel
					if model == "" {
						model = "jev-latest"
					}
					return ai.NewSystemOneClassifierProvider(ai.SystemOneConfig{
						ProviderID: p.ID,
						BaseURL:    p.BaseURL,
						APIKey:     p.APIKey,
						ImageMode:  ai.ClassifierImageAssets,
					}), model, nil
				}
				if strings.Contains(idLower, "openrouter") || strings.Contains(urlLower, "openrouter.ai") {
					model := reqModel
					if model == "" {
						model = "typesafe/jev-1.14"
					}
					return ai.NewSystemOneClassifierProvider(ai.SystemOneConfig{
						ProviderID: p.ID,
						BaseURL:    p.BaseURL,
						APIKey:     p.APIKey,
						ImageMode:  ai.ClassifierImageStateImageURL,
					}), model, nil
				}
			}
		}
	}

	if key := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY")); key != "" {
		model := reqModel
		if model == "" {
			model = "typesafe/jev-1.14"
		}
		return ai.NewSystemOneClassifierProvider(ai.SystemOneConfig{
			ProviderID: "openrouter",
			BaseURL:    "https://openrouter.ai/api/v1",
			APIKey:     key,
			ImageMode:  ai.ClassifierImageStateImageURL,
		}), model, nil
	}

	return nil, "", fmt.Errorf("no classifier provider configured (set TYPESAFE_API_KEY, NEX_CLASSIFIER_BASE_URL, or configure OpenRouter)")
}
