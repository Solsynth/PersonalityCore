package agent

import (
	"strings"

	"src.solsynth.dev/sosys/persona/internal/config"
)

// imageModalityPresets are the models known to read images that deployments
// routinely list without saying so.
//
// A model entry that declares `modalities` states its own answer and is never
// overridden by this table, and so is an explicit `supportsVision` on the
// provider. This only fills the gap for a model whose image support is a
// documented fact of the endpoint serving it.
//
// The DeepSeek Flash line is the case that prompted it: V4.1-Flash reads
// images natively — JPEG, PNG, GIF and WebP through the ordinary `image_url`
// content part — and the retired names are still accepted, with requests
// served by the current Flash model. A deployment that predates the rename
// lists `deepseek-v4-flash` and, without this, has its images silently
// replaced by a description written by the vision model.
//
// The endpoint matters as much as the name: other hosts serve `deepseek-v4-*`
// slugs without image input, so each entry names the provider it is true of.
var imageModalityPresets = []struct {
	// providerIDs are provider ids this holds for.
	providerIDs []string
	// baseURLFragments are hosts this holds for, for deployments that name
	// their DeepSeek provider something else.
	baseURLFragments []string
	// models are the exact model names known to take image input there.
	models []string
}{
	{
		providerIDs:      []string{"deepseek"},
		baseURLFragments: []string{"api.deepseek.com"},
		models: []string{
			"deepseek-flash",
			"deepseek-v4-flash",
			"deepseek-v4.1-flash",
			"deepseek-v4-flash-vision-exp",
		},
	},
}

// presetSupportsImage reports whether a preset knows that the given provider's
// model takes image input. Matching on the model name is exact: a modality is
// a fact about one version of a model, and a guessing prefix would hand an
// image to a text-only snapshot.
func presetSupportsImage(provider config.ProviderConfig, modelName string) bool {
	name := strings.ToLower(strings.TrimSpace(modelName))
	if name == "" {
		return false
	}
	providerID := strings.ToLower(strings.TrimSpace(provider.ID))
	baseURL := strings.ToLower(strings.TrimSpace(provider.BaseURL))

	for _, preset := range imageModalityPresets {
		if !presetMatchesProvider(preset.providerIDs, preset.baseURLFragments, providerID, baseURL) {
			continue
		}
		for _, known := range preset.models {
			if name == known {
				return true
			}
		}
	}
	return false
}

func presetMatchesProvider(providerIDs, baseURLFragments []string, providerID, baseURL string) bool {
	for _, id := range providerIDs {
		if providerID != "" && providerID == id {
			return true
		}
	}
	for _, fragment := range baseURLFragments {
		if baseURL != "" && strings.Contains(baseURL, fragment) {
			return true
		}
	}
	return false
}
