package llm

import (
	"bytes"
	runtimelogging "github.com/Isites/anyai/internal/runtime/logging"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

type openAICompatibleRawStreamDiagnostic struct {
	URL         string
	StatusCode  int
	ContentType string
	StageHint   string
	TotalBytes  int
	DataFrames  int
	SawDone     bool
	Error       string
}

var emitOpenAICompatibleRawStreamDiagnostic = func(diag openAICompatibleRawStreamDiagnostic) {
	runtimelogging.Warn("openai-compatible raw stream diagnostic",
		"url", diag.URL,
		"status_code", diag.StatusCode,
		"content_type", diag.ContentType,
		"stage_hint", diag.StageHint,
		"total_bytes", diag.TotalBytes,
		"data_frames", diag.DataFrames,
		"saw_done", diag.SawDone,
		"error", diag.Error,
	)
}

type openAICompatibleStreamDiagnosticRoundTripper struct {
	base http.RoundTripper
}

func (rt openAICompatibleStreamDiagnosticRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	base := rt.base
	if base == nil {
		base = http.DefaultTransport
	}

	resp, err := base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.Body == nil || !shouldDiagnoseOpenAICompatibleResponse(req, resp) {
		return resp, nil
	}

	resp.Body = &openAICompatibleDiagnosticBody{
		body:        resp.Body,
		url:         sanitizeOpenAICompatibleURL(req.URL),
		statusCode:  resp.StatusCode,
		contentType: strings.TrimSpace(resp.Header.Get("Content-Type")),
	}
	return resp, nil
}

type openAICompatibleDiagnosticBody struct {
	body        io.ReadCloser
	url         string
	contentType string
	statusCode  int

	mu         sync.Mutex
	totalBytes int
	dataFrames int
	sawDone    bool
	lastErr    error
	emitted    bool
}

func (b *openAICompatibleDiagnosticBody) Read(p []byte) (int, error) {
	n, err := b.body.Read(p)
	if n > 0 {
		b.observe(p[:n])
	}
	if err != nil {
		b.mu.Lock()
		b.lastErr = err
		b.mu.Unlock()
		if err != io.EOF {
			b.emitLocked("read_error", err.Error())
		}
	}
	return n, err
}

func (b *openAICompatibleDiagnosticBody) Close() error {
	b.emitLocked("", "")
	return b.body.Close()
}

func (b *openAICompatibleDiagnosticBody) observe(chunk []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.totalBytes += len(chunk)
	b.dataFrames += bytes.Count(chunk, []byte("data:"))
	if bytes.Contains(chunk, []byte("[DONE]")) {
		b.sawDone = true
	}
}

func (b *openAICompatibleDiagnosticBody) emitLocked(stageHint, errMsg string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.emitted {
		return
	}

	if stageHint == "" {
		if b.sawDone || b.totalBytes == 0 {
			return
		}
		stageHint = inferOpenAICompatibleRawStage(b.totalBytes, b.dataFrames)
		if b.lastErr == nil || b.lastErr == io.EOF {
			errMsg = "stream closed without [DONE]"
		} else {
			errMsg = b.lastErr.Error()
		}
	}

	b.emitted = true
	emitOpenAICompatibleRawStreamDiagnostic(openAICompatibleRawStreamDiagnostic{
		URL:         b.url,
		StatusCode:  b.statusCode,
		ContentType: b.contentType,
		StageHint:   stageHint,
		TotalBytes:  b.totalBytes,
		DataFrames:  b.dataFrames,
		SawDone:     b.sawDone,
		Error:       errMsg,
	})
}

func shouldDiagnoseOpenAICompatibleResponse(req *http.Request, resp *http.Response) bool {
	if req == nil || resp == nil {
		return false
	}
	return strings.Contains(strings.ToLower(req.Header.Get("Accept")), "text/event-stream")
}

func inferOpenAICompatibleRawStage(totalBytes, dataFrames int) string {
	switch {
	case totalBytes == 0:
		return "empty_body"
	case dataFrames == 0:
		return "before_first_data_frame"
	case dataFrames == 1:
		return "first_data_frame"
	default:
		return "mid_stream"
	}
}

func sanitizeOpenAICompatibleURL(url *url.URL) string {
	if url == nil {
		return ""
	}
	clone := *url
	clone.RawQuery = ""
	clone.User = nil
	return clone.String()
}
