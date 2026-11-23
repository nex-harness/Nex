package runner

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/nex-harness/nex/pkg/ai"
	"github.com/nex-harness/nex/pkg/store"
)

func inferImageDriverAndDefaultModel(idOrDriver, baseURL string) (ai.ImageDriver, string) {
	key := strings.ToLower(strings.TrimSpace(idOrDriver))
	url := strings.ToLower(strings.TrimSpace(baseURL))
	switch {
	case key == string(ai.ImageDriverOpenRouter) || strings.Contains(key, "openrouter") || strings.Contains(url, "openrouter.ai"):
		return ai.ImageDriverOpenRouter, "google/gemini-2.5-flash-image"
	case key == string(ai.ImageDriverXAI) || strings.Contains(key, "xai") || strings.Contains(key, "grok") || strings.Contains(url, "api.x.ai"):
		return ai.ImageDriverXAI, "grok-2-image"
	case key == string(ai.ImageDriverMiniMax) || strings.Contains(key, "minimax") || strings.Contains(url, "minimax"):
		return ai.ImageDriverMiniMax, "image-01"
	default:
		return ai.ImageDriverOpenAI, "gpt-image-1"
	}
}

// resolveImageProvider resolves an ImageProvider and default model ID from UserPreferences, stored providers, or env vars.
func (n *Node) resolveImageProvider(ctx context.Context, requestedModel string) (ai.ImageProvider, string, error) {
	reqModel := strings.TrimSpace(requestedModel)
	var prefs store.UserPreferences
	hasPrefs := false
	if n != nil && n.store != nil {
		if p, err := n.store.GetUserPreferences(ctx); err == nil {
			prefs = p
			hasPrefs = true
			if reqModel == "" {
				reqModel = strings.TrimSpace(prefs.ImageModelID)
			}
		}
	}
	if reqModel == "" {
		reqModel = strings.TrimSpace(os.Getenv("NEX_IMAGE_MODEL"))
	}

	// 1. Standalone image configuration from UserPreferences
	if hasPrefs && (prefs.ImageMode == "standalone" || (prefs.ImageMode == "" && strings.TrimSpace(prefs.ImageBaseURL) != "")) {
		driver, defaultModel := inferImageDriverAndDefaultModel(prefs.ImageDriver, prefs.ImageBaseURL)
		model := reqModel
		if model == "" {
			model = defaultModel
		}
		return ai.NewImageProvider(driver, ai.ImageProviderConfig{
			ProviderID: string(driver),
			BaseURL:    strings.TrimSpace(prefs.ImageBaseURL),
			APIKey:     strings.TrimSpace(prefs.ImageAPIKey),
		}), model, nil
	}

	// 2. Stored providers (explicit ImageProviderID first, then auto-detect OpenRouter -> OpenAI -> xAI/Grok -> MiniMax)
	if n != nil && n.store != nil {
		if providers, err := n.store.ListProviders(ctx); err == nil {
			prefProviderID := strings.TrimSpace(prefs.ImageProviderID)
			if prefProviderID != "" {
				for _, p := range providers {
					if !p.Enabled || p.ID != prefProviderID || strings.TrimSpace(p.APIKey) == "" {
						continue
					}
					driverKey := strings.TrimSpace(prefs.ImageDriver)
					if driverKey == "" {
						driverKey = p.ID
					}
					driver, defaultModel := inferImageDriverAndDefaultModel(driverKey, p.BaseURL)
					model := reqModel
					if model == "" {
						model = defaultModel
					}
					return ai.NewImageProvider(driver, ai.ImageProviderConfig{
						ProviderID: p.ID,
						BaseURL:    p.BaseURL,
						APIKey:     p.APIKey,
					}), model, nil
				}
			}

			// Auto-detect priority: OpenRouter -> OpenAI -> xAI/Grok -> MiniMax
			for _, match := range []ai.ImageDriver{
				ai.ImageDriverOpenRouter,
				ai.ImageDriverOpenAI,
				ai.ImageDriverXAI,
				ai.ImageDriverMiniMax,
			} {
				for _, p := range providers {
					if !p.Enabled || strings.TrimSpace(p.APIKey) == "" {
						continue
					}
					idLower := strings.ToLower(p.ID)
					urlLower := strings.ToLower(p.BaseURL)
					matched := false
					switch match {
					case ai.ImageDriverOpenRouter:
						matched = strings.Contains(idLower, "openrouter") || strings.Contains(urlLower, "openrouter.ai")
					case ai.ImageDriverOpenAI:
						matched = idLower == "openai" || strings.Contains(urlLower, "api.openai.com")
					case ai.ImageDriverXAI:
						matched = strings.Contains(idLower, "xai") || strings.Contains(idLower, "grok") || strings.Contains(urlLower, "api.x.ai")
					case ai.ImageDriverMiniMax:
						matched = strings.Contains(idLower, "minimax") || strings.Contains(urlLower, "minimax")
					}
					if matched {
						_, defaultModel := inferImageDriverAndDefaultModel(string(match), p.BaseURL)
						model := reqModel
						if model == "" {
							model = defaultModel
						}
						return ai.NewImageProvider(match, ai.ImageProviderConfig{
							ProviderID: p.ID,
							BaseURL:    p.BaseURL,
							APIKey:     p.APIKey,
						}), model, nil
					}
				}
			}
		}
	}

	// 3. Environment variable fallbacks
	if key := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY")); key != "" {
		model := reqModel
		if model == "" {
			model = "google/gemini-2.5-flash-image"
		}
		return ai.NewOpenRouterImagesProvider(ai.ImageProviderConfig{ProviderID: "openrouter", APIKey: key}), model, nil
	}
	if key := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")); key != "" {
		model := reqModel
		if model == "" {
			model = "gpt-image-1"
		}
		return ai.NewOpenAIImagesProvider(ai.ImageProviderConfig{ProviderID: "openai", APIKey: key}), model, nil
	}
	if key := strings.TrimSpace(os.Getenv("XAI_API_KEY")); key != "" {
		model := reqModel
		if model == "" {
			model = "grok-2-image"
		}
		return ai.NewXAIImagesProvider(ai.ImageProviderConfig{ProviderID: "xai", APIKey: key}), model, nil
	}
	if key := strings.TrimSpace(os.Getenv("MINIMAX_API_KEY")); key != "" {
		model := reqModel
		if model == "" {
			model = "image-01"
		}
		return ai.NewMiniMaxImagesProvider(ai.ImageProviderConfig{ProviderID: "minimax", APIKey: key}), model, nil
	}
	return nil, "", fmt.Errorf("no image generation provider configured (configure in Settings → Providers & Models → Image Generation, or set OPENROUTER_API_KEY / OPENAI_API_KEY / XAI_API_KEY / MINIMAX_API_KEY)")
}

// resolveClassifierProvider resolves a TypeSafe System One (Jev) ClassifierProvider and default model ID.
func (n *Node) resolveClassifierProvider(ctx context.Context, requestedModel string) (ai.ClassifierProvider, string, error) {
	reqModel := strings.TrimSpace(requestedModel)
	var prefs store.UserPreferences
	hasPrefs := false
	if n != nil && n.store != nil {
		if p, err := n.store.GetUserPreferences(ctx); err == nil {
			prefs = p
			hasPrefs = true
			if reqModel == "" {
				reqModel = strings.TrimSpace(prefs.ClassifierModelID)
			}
		}
	}
	if reqModel == "" {
		reqModel = strings.TrimSpace(os.Getenv("NEX_CLASSIFIER_MODEL"))
	}

	// 1. Standalone classifier configuration from UserPreferences
	if hasPrefs && (prefs.ClassifierMode == "standalone" || (prefs.ClassifierMode == "" && strings.TrimSpace(prefs.ClassifierBaseURL) != "")) {
		baseURL := strings.TrimSpace(prefs.ClassifierBaseURL)
		imgMode := ai.ClassifierImageAssets
		defaultModel := "jev-latest"
		if prefs.ClassifierDriver == "openrouter_system_one" || strings.Contains(strings.ToLower(baseURL), "openrouter.ai") {
			imgMode = ai.ClassifierImageStateImageURL
			defaultModel = "typesafe/jev-1.14"
			if baseURL == "" {
				baseURL = "https://openrouter.ai/api/v1"
			}
		} else if baseURL == "" {
			baseURL = "https://api.typesafe.ai/v1"
		}
		model := reqModel
		if model == "" {
			model = defaultModel
		}
		apiKey := strings.TrimSpace(prefs.ClassifierAPIKey)
		if apiKey == "" {
			apiKey = strings.TrimSpace(os.Getenv("NEX_CLASSIFIER_API_KEY"))
		}
		return ai.NewSystemOneClassifierProvider(ai.SystemOneConfig{
			ProviderID: "custom-classifier",
			BaseURL:    baseURL,
			APIKey:     apiKey,
			ImageMode:  imgMode,
		}), model, nil
	}

	// 2. Explicit UserPreferences.ClassifierProviderID
	if hasPrefs && strings.TrimSpace(prefs.ClassifierProviderID) != "" && n != nil && n.store != nil {
		prefProviderID := strings.TrimSpace(prefs.ClassifierProviderID)
		if providers, err := n.store.ListProviders(ctx); err == nil {
			for _, p := range providers {
				if !p.Enabled || p.ID != prefProviderID || strings.TrimSpace(p.APIKey) == "" {
					continue
				}
				idLower := strings.ToLower(p.ID)
				urlLower := strings.ToLower(p.BaseURL)
				imgMode := ai.ClassifierImageAssets
				defaultModel := "jev-latest"
				if prefs.ClassifierDriver == "openrouter_system_one" || strings.Contains(idLower, "openrouter") || strings.Contains(urlLower, "openrouter.ai") {
					imgMode = ai.ClassifierImageStateImageURL
					defaultModel = "typesafe/jev-1.14"
				}
				model := reqModel
				if model == "" {
					model = defaultModel
				}
				return ai.NewSystemOneClassifierProvider(ai.SystemOneConfig{
					ProviderID: p.ID,
					BaseURL:    p.BaseURL,
					APIKey:     p.APIKey,
					ImageMode:  imgMode,
				}), model, nil
			}
		}
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

	return nil, "", fmt.Errorf("no classifier provider configured (configure in Settings → Providers & Models → Classifier (Jev), or set TYPESAFE_API_KEY / NEX_CLASSIFIER_BASE_URL / OPENROUTER_API_KEY)")
}
