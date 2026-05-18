package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// Module contributes routes to the unified HTTP runtime.
type Module interface {
	RegisterRoutes(chi.Router)
}

// ModuleFunc adapts a function to Module.
type ModuleFunc func(chi.Router)

func (f ModuleFunc) RegisterRoutes(r chi.Router) { f(r) }

func Handle(pattern string, handler http.Handler) Module {
	return ModuleFunc(func(r chi.Router) { r.Handle(pattern, handler) })
}

func Mount(pattern string, handler http.Handler) Module {
	return ModuleFunc(func(r chi.Router) { r.Mount(pattern, handler) })
}

func Get(pattern string, handler http.HandlerFunc) Module {
	return ModuleFunc(func(r chi.Router) { r.Get(pattern, handler) })
}

func Post(pattern string, handler http.HandlerFunc) Module {
	return ModuleFunc(func(r chi.Router) { r.Post(pattern, handler) })
}

// ServerOptions configures the HTTP server.
type ServerOptions struct {
	AuthToken string
	Metrics   *Metrics
	Modules   []Module
}

// Server hosts the unified HTTP surface.
type Server struct {
	httpServer *http.Server
	router     chi.Router
	host       string
	port       int
	opts       ServerOptions
}

// NewServer creates a server with shared middleware.
func NewServer(host string, port int, opts ...ServerOptions) *Server {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)

	var o ServerOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	if o.Metrics != nil {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				o.Metrics.IncRequests()
				next.ServeHTTP(w, req)
			})
		})
	}
	if o.AuthToken != "" {
		r.Use(BearerAuthMiddleware(o.AuthToken))
	}

	s := &Server{
		router: r,
		host:   host,
		port:   port,
		opts:   o,
	}
	s.routes()
	return s
}

func (s *Server) routes() {
	s.router.Get("/health", s.handleHealth)
	for _, module := range s.opts.Modules {
		if module == nil {
			continue
		}
		module.RegisterRoutes(s.router)
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"status":"ok","timestamp":"%s"}`, time.Now().UTC().Format(time.RFC3339))
}

// Start begins serving traffic.
func (s *Server) Start() error {
	addr := net.JoinHostPort(s.host, fmt.Sprintf("%d", s.port))
	s.httpServer = &http.Server{
		Addr:              addr,
		Handler:           s.router,
		ReadTimeout:       15 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       60 * time.Second,
	}
	slog.Info("gateway listening", "addr", addr)
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s == nil || s.httpServer == nil {
		return nil
	}
	return s.httpServer.Shutdown(ctx)
}

func (s *Server) Router() chi.Router {
	if s == nil {
		return nil
	}
	return s.router
}
