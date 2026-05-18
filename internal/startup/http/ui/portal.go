package ui

import (
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"strings"

	"github.com/Isites/anyai/internal/config"
	"github.com/Isites/anyai/internal/gateway"
	"github.com/go-chi/chi/v5"
)

type Plane interface {
	ConfigSnapshot() *config.Config
	SaveConfig(raw []byte) error
	LogEntriesPayload(limit int) []map[string]any
	SubscribeLogs() (<-chan gateway.LogEntry, func())
}

type Portal struct {
	plane       Plane
	assets      fs.FS
	assetServer http.Handler
}

func NewPortal(plane Plane, assets fs.FS) *Portal {
	return &Portal{
		plane:       plane,
		assets:      assets,
		assetServer: http.FileServer(http.FS(assets)),
	}
}

func (p *Portal) RegisterRoutes(r chi.Router) {
	r.Handle("/ui/assets/*", http.StripPrefix("/ui/assets/", p.assetServer))

	r.Get("/", p.serveShell)
	r.Get("/ui", p.serveShell)
	r.Get("/ui/", p.serveShell)
	r.Get("/ui/{section}", p.serveShell)
	r.Get("/chat", p.serveShell)
	r.Get("/jobs", p.serveShell)
	r.Get("/logs", p.serveShell)
	r.Get("/settings", p.serveShell)

	r.Get("/settings/api/config", p.serveConfigJSON)
	r.Post("/settings/api/config", p.saveConfigJSON)
	r.Get("/logs/stream", p.streamLogs)
}

func (p *Portal) serveShell(w http.ResponseWriter, _ *http.Request) {
	data, err := fs.ReadFile(p.assets, "app.html")
	if err != nil {
		http.Error(w, "ui asset missing", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; connect-src 'self'; img-src 'self' data:; font-src 'self'")
	_, _ = w.Write(data)
}

func (p *Portal) serveConfigJSON(w http.ResponseWriter, _ *http.Request) {
	cfg := p.plane.ConfigSnapshot()
	if cfg == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"config not available"}`))
		return
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		http.Error(w, `{"error":"marshal config"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

func (p *Portal) saveConfigJSON(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, `{"error":"read body"}`, http.StatusBadRequest)
		return
	}
	if err := p.plane.SaveConfig(body); err != nil {
		http.Error(w, `{"error":"`+escapeJSONString(err.Error())+`"}`, http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func (p *Portal) streamLogs(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	prepareEventStream(w)
	for _, entry := range p.plane.LogEntriesPayload(0) {
		if err := writeSSEJSON(w, "log", entry); err != nil {
			return
		}
		flusher.Flush()
	}

	ch, cancel := p.plane.SubscribeLogs()
	if cancel == nil {
		http.NotFound(w, r)
		return
	}
	defer cancel()
	for {
		select {
		case <-r.Context().Done():
			return
		case entry, ok := <-ch:
			if !ok {
				return
			}
			payload := map[string]any{
				"time":    entry.Time.UTC().Format("2006-01-02T15:04:05.000000000Z07:00"),
				"level":   entry.Level,
				"message": entry.Message,
				"attrs":   entry.Attrs,
			}
			if err := writeSSEJSON(w, "log", payload); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func escapeJSONString(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`)
	return replacer.Replace(value)
}

func prepareEventStream(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
}

func writeSSEJSON(w http.ResponseWriter, eventName string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, "event: "+eventName+"\ndata: "+string(data)+"\n\n")
	return err
}
