package server

import (
	"net/http"

	channelpolicy "github.com/Isites/anyai/internal/startup/http/accesspolicy"
)

// BearerAuthMiddleware validates bearer auth for all endpoints except health.
func BearerAuthMiddleware(token string) func(http.Handler) http.Handler {
	return channelpolicy.NewHTTPAccessPolicy(token).BearerAuthMiddleware()
}
