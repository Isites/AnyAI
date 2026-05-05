package accesspolicy

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

type HTTPAccessPolicy struct {
	token string
}

func NewHTTPAccessPolicy(token string) HTTPAccessPolicy {
	return HTTPAccessPolicy{
		token: strings.TrimSpace(token),
	}
}

func (p HTTPAccessPolicy) BearerAuthMiddleware() func(http.Handler) http.Handler {
	if p.token == "" {
		return func(next http.Handler) http.Handler { return next }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/health" {
				next.ServeHTTP(w, r)
				return
			}

			auth := r.Header.Get("Authorization")
			if auth == "" {
				auth = "Bearer " + r.URL.Query().Get("token")
			}
			if !strings.HasPrefix(auth, "Bearer ") {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}

			provided := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
			if subtle.ConstantTimeCompare([]byte(provided), []byte(p.token)) != 1 {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
