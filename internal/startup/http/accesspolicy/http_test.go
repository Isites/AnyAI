package accesspolicy

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHTTPAccessPolicyBearerAuthMiddleware(t *testing.T) {
	policy := NewHTTPAccessPolicy("secret")
	handler := policy.BearerAuthMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	unauthorizedReq := httptest.NewRequest(http.MethodGet, "/api/runs", nil)
	unauthorizedRec := httptest.NewRecorder()
	handler.ServeHTTP(unauthorizedRec, unauthorizedReq)
	assert.Equal(t, http.StatusUnauthorized, unauthorizedRec.Code)

	authorizedReq := httptest.NewRequest(http.MethodGet, "/api/runs", nil)
	authorizedReq.Header.Set("Authorization", "Bearer secret")
	authorizedRec := httptest.NewRecorder()
	handler.ServeHTTP(authorizedRec, authorizedReq)
	assert.Equal(t, http.StatusOK, authorizedRec.Code)
}
