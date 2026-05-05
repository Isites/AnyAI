package llm

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func newLoopbackTestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		if isLoopbackListenPermissionError(err) {
			t.Skipf("loopback listen unavailable in this environment: %v", err)
		}
		require.NoError(t, err)
	}

	server := httptest.NewUnstartedServer(handler)
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	return server
}

func isLoopbackListenPermissionError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "operation not permitted") || strings.Contains(msg, "permission denied")
}
