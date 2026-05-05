package memory

import "strings"

const DefaultSearchLimit = 5

func NormalizeScope(scope SearchScope) SearchScope {
	return SearchScope{
		AgentID:   strings.TrimSpace(scope.AgentID),
		SessionID: strings.TrimSpace(scope.SessionID),
	}
}

func NormalizeID(id string) string {
	return strings.TrimSpace(id)
}

func NormalizeMaxResults(maxResults, fallback int) int {
	if fallback <= 0 {
		fallback = DefaultSearchLimit
	}
	if maxResults <= 0 {
		return fallback
	}
	return maxResults
}

func NormalizeLayers(layers []Layer, defaults ...Layer) []Layer {
	source := layers
	if len(source) == 0 {
		source = defaults
	}
	if len(source) == 0 {
		return nil
	}
	seen := make(map[Layer]struct{}, len(source))
	out := make([]Layer, 0, len(source))
	for _, layer := range source {
		layer = canonicalLayer(layer)
		if layer == "" {
			continue
		}
		if _, ok := seen[layer]; ok {
			continue
		}
		seen[layer] = struct{}{}
		out = append(out, layer)
	}
	return out
}

func canonicalLayer(layer Layer) Layer {
	switch strings.ToLower(strings.TrimSpace(string(layer))) {
	case "", "all":
		return ""
	case "episodic":
		return LayerEpisodic
	case "candidate", "candidates":
		return LayerCandidates
	case "long_term", "long-term", "longterm":
		return LayerLongTerm
	default:
		return Layer(strings.TrimSpace(string(layer)))
	}
}
