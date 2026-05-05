package factory

import (
	"fmt"
	runtimelogging "github.com/Isites/anyai/internal/runtime/logging"
	"os"
	"strings"
	"unicode"

	"github.com/Isites/anyai/internal/config"
	"github.com/Isites/anyai/internal/runtime/llm"
)

func ResolveProviderOpts(name string, cfg *config.Config) (llm.ProviderOptions, error) {
	pcfg := cfg.GetProvider(name)
	opts := llm.ProviderOptions{
		APIKey:  pcfg.APIKey,
		BaseURL: pcfg.BaseURL,
		Kind:    pcfg.Kind,
		Headers: config.CloneHeaders(pcfg.Headers),
	}

	envPrefix := providerEnvPrefix(name)
	if v := os.Getenv(envPrefix + "_API_KEY"); v != "" {
		opts.APIKey = v
	}
	if v := os.Getenv(envPrefix + "_AUTH_TOKEN"); v != "" {
		opts.APIKey = v
	}
	if v := os.Getenv(envPrefix + "_BASE_URL"); v != "" {
		opts.BaseURL = v
	}
	if v := os.Getenv(envPrefix + "_HEADERS_JSON"); v != "" {
		headers, err := config.ParseHeadersJSON(v)
		if err != nil {
			return llm.ProviderOptions{}, fmt.Errorf("parse %s_HEADERS_JSON: %w", envPrefix, err)
		}
		opts.Headers = config.MergeHeaders(opts.Headers, headers)
	}

	return opts, nil
}

func InitProviders(cfg *config.Config, overrides map[string]llm.LLMProvider) map[string]llm.LLMProvider {
	providers := make(map[string]llm.LLMProvider)

	needed := make(map[string]bool)
	for _, a := range cfg.Agents.List {
		provName, _ := llm.ParseProviderModel(a.Model)
		if provName != "" {
			needed[provName] = true
		}
		for _, fb := range a.Fallbacks {
			provName, _ = llm.ParseProviderModel(fb)
			if provName != "" {
				needed[provName] = true
			}
		}
	}

	for name := range needed {
		opts, err := ResolveProviderOpts(name, cfg)
		if err != nil {
			runtimelogging.Error("failed to resolve provider options", "provider", name, "error", err)
			continue
		}

		if !llm.CanInitializeProvider(name, opts) {
			runtimelogging.Warn("provider is missing required credentials or compatible endpoint, skipping", "provider", name)
			continue
		}

		if opts.BaseURL != "" {
			runtimelogging.Info("using custom base URL for provider", "provider", name, "base_url", opts.BaseURL)
		}

		provider, err := llm.NewProvider(name, opts)
		if err != nil {
			runtimelogging.Error("failed to create provider", "provider", name, "error", err)
			continue
		}
		providers[name] = provider
	}

	for name, provider := range overrides {
		if provider == nil {
			continue
		}
		providers[name] = provider
	}

	return providers
}

func providerEnvPrefix(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}

	var b strings.Builder
	prevUnderscore := false
	for _, r := range strings.ToUpper(name) {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			b.WriteRune(r)
			prevUnderscore = false
		case !prevUnderscore:
			b.WriteRune('_')
			prevUnderscore = true
		}
	}
	return strings.Trim(b.String(), "_")
}
