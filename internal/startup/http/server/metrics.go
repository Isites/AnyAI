package server

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Metrics collects unified HTTP runtime metrics.
type Metrics struct {
	requestsTotal   atomic.Int64
	toolCallsTotal  atomic.Int64
	llmCallsTotal   atomic.Int64
	errorsTotal     atomic.Int64
	startTime       time.Time

	toolCounts map[string]*atomic.Int64
	mu         sync.RWMutex
}

// NewMetrics creates a metrics collector.
func NewMetrics() *Metrics {
	return &Metrics{
		startTime:  time.Now(),
		toolCounts: make(map[string]*atomic.Int64),
	}
}

func (m *Metrics) IncRequests() { m.requestsTotal.Add(1) }
func (m *Metrics) IncLLMCalls() { m.llmCallsTotal.Add(1) }
func (m *Metrics) IncErrors()   { m.errorsTotal.Add(1) }

func (m *Metrics) IncToolCalls(toolName string) {
	m.toolCallsTotal.Add(1)

	m.mu.RLock()
	counter, ok := m.toolCounts[toolName]
	m.mu.RUnlock()
	if ok {
		counter.Add(1)
		return
	}

	m.mu.Lock()
	counter, ok = m.toolCounts[toolName]
	if !ok {
		counter = &atomic.Int64{}
		m.toolCounts[toolName] = counter
	}
	m.mu.Unlock()
	counter.Add(1)
}

// RenderPrometheus exposes metrics in Prometheus exposition format.
func (m *Metrics) RenderPrometheus() string {
	var b strings.Builder

	b.WriteString("# HELP anyai_uptime_seconds Time since gateway started.\n")
	b.WriteString("# TYPE anyai_uptime_seconds gauge\n")
	fmt.Fprintf(&b, "anyai_uptime_seconds %.1f\n\n", time.Since(m.startTime).Seconds())

	b.WriteString("# HELP anyai_http_requests_total Total HTTP requests.\n")
	b.WriteString("# TYPE anyai_http_requests_total counter\n")
	fmt.Fprintf(&b, "anyai_http_requests_total %d\n\n", m.requestsTotal.Load())

	b.WriteString("# HELP anyai_tool_calls_total Total tool calls.\n")
	b.WriteString("# TYPE anyai_tool_calls_total counter\n")
	fmt.Fprintf(&b, "anyai_tool_calls_total %d\n\n", m.toolCallsTotal.Load())

	b.WriteString("# HELP anyai_llm_calls_total Total LLM API calls.\n")
	b.WriteString("# TYPE anyai_llm_calls_total counter\n")
	fmt.Fprintf(&b, "anyai_llm_calls_total %d\n\n", m.llmCallsTotal.Load())

	b.WriteString("# HELP anyai_errors_total Total runtime errors.\n")
	b.WriteString("# TYPE anyai_errors_total counter\n")
	fmt.Fprintf(&b, "anyai_errors_total %d\n\n", m.errorsTotal.Load())

	m.mu.RLock()
	if len(m.toolCounts) > 0 {
		names := make([]string, 0, len(m.toolCounts))
		for name := range m.toolCounts {
			names = append(names, name)
		}
		sort.Strings(names)

		b.WriteString("# HELP anyai_tool_calls_by_tool Tool calls grouped by tool.\n")
		b.WriteString("# TYPE anyai_tool_calls_by_tool counter\n")
		for _, name := range names {
			fmt.Fprintf(&b, "anyai_tool_calls_by_tool{tool=%q} %d\n", name, m.toolCounts[name].Load())
		}
	}
	m.mu.RUnlock()

	return b.String()
}

// NewMetricsHandler serves Prometheus metrics.
func NewMetricsHandler(metrics *Metrics) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write([]byte(metrics.RenderPrometheus()))
	}
}
