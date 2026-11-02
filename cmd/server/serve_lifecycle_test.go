package main

import (
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A stop request is one shutdown regardless of how many reasons arrive: a
// signal racing a parent death must not close the runtime twice.
func TestServeLifecycleStopIsIdempotent(t *testing.T) {
	lifecycle := newServeLifecycle(&http.Server{}, nil)

	lifecycle.requestStop("first")
	lifecycle.requestStop("second")

	select {
	case <-lifecycle.stopRequest:
	default:
		t.Fatal("stop request must be observable")
	}
}

// A request that is in flight when shutdown starts must still be answered, and
// the process must exit afterwards. This is the "restart works" case: the port
// is released because Serve returned, not because the process was killed.
func TestServeLifecycleShutsDownAndExits(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("ok"))
		}),
	}
	lifecycle := newServeLifecycle(server, nil)
	exitCode := make(chan int, 1)
	lifecycle.exit = func(code int) { exitCode <- code }

	go lifecycle.runWithListener(listener, false)

	response, err := http.Get("http://" + listener.Addr().String())
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())

	lifecycle.requestStop("test")

	select {
	case code := <-exitCode:
		require.Equal(t, 0, code, "a stop request is a clean exit")
	case <-time.After(5 * time.Second):
		t.Fatal("the server did not exit; the port would stay bound")
	}

	// The listener is released, so a restart can bind the same address.
	rebound, err := net.Listen("tcp", listener.Addr().String())
	require.NoError(t, err, "the port must be free after shutdown")
	require.NoError(t, rebound.Close())
}
