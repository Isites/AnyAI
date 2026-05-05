package llm

import "net/http"

func cloneHeaderMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}

	dst := make(map[string]string, len(src))
	for key, value := range src {
		if key == "" {
			continue
		}
		dst[key] = value
	}
	if len(dst) == 0 {
		return nil
	}
	return dst
}

func newHeaderHTTPClient(headers map[string]string) *http.Client {
	headers = cloneHeaderMap(headers)
	if len(headers) == 0 {
		return nil
	}
	return &http.Client{
		Transport: headerRoundTripper{
			base:    http.DefaultTransport,
			headers: headers,
		},
	}
}

func newOpenAIHTTPClient(headers map[string]string) *http.Client {
	headers = cloneHeaderMap(headers)
	return &http.Client{
		Transport: openAICompatibleStreamDiagnosticRoundTripper{
			base: headerRoundTripper{
				base:    http.DefaultTransport,
				headers: headers,
			},
		},
	}
}

func toHTTPHeader(headers map[string]string) http.Header {
	if len(headers) == 0 {
		return nil
	}

	httpHeaders := make(http.Header, len(headers))
	for key, value := range headers {
		httpHeaders.Set(key, value)
	}
	if len(httpHeaders) == 0 {
		return nil
	}
	return httpHeaders
}

type headerRoundTripper struct {
	base    http.RoundTripper
	headers map[string]string
}

func (rt headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	base := rt.base
	if base == nil {
		base = http.DefaultTransport
	}

	clone := req.Clone(req.Context())
	clone.Header = req.Header.Clone()
	for key, value := range rt.headers {
		clone.Header.Set(key, value)
	}
	return base.RoundTrip(clone)
}
