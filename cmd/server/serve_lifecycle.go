package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/heron-ai/heron-engine/internal/app"
	"github.com/heron-ai/heron-engine/internal/logging"
)

// serveShutdownTimeout bounds graceful shutdown. It is a deadline, not a wait:
// an open SSE stream or an in-flight turn must not keep the port bound forever,
// because a supervisor that restarts this process would then fail to bind.
//
// The value is generous enough for one turn to finish and short enough that a
// restart loop still converges.
var serveShutdownTimeout = 10 * time.Second

// parentWatchInterval is how often the server checks whether the process that
// started it is still alive. A parent that dies without cleaning up (SIGKILL,
// OOM, panicked supervisor) leaves this process orphaned and holding the port;
// without this check the next start would fail to bind.
var parentWatchInterval = 2 * time.Second

// serveLifecycle owns the shutdown path of the HTTP server.
//
// Three things can stop it: a termination signal, the parent process going
// away, or the listener failing. All three converge on one shutdown so the
// runtime is closed exactly once and the process always exits — a server that
// hangs on shutdown is indistinguishable from a server that never started,
// except that the port stays taken.
type serveLifecycle struct {
	server *http.Server
	bundle *app.RuntimeBundle

	stopOnce     sync.Once
	stopRequest  chan struct{}
	shutdownDone chan struct{}
	// exit is os.Exit in production. A test substitutes it so the shutdown
	// path can be driven without killing the test process.
	exit func(code int)
}

func newServeLifecycle(server *http.Server, bundle *app.RuntimeBundle) *serveLifecycle {
	return &serveLifecycle{
		server:       server,
		bundle:       bundle,
		stopRequest:  make(chan struct{}),
		shutdownDone: make(chan struct{}),
		exit:         os.Exit,
	}
}

// exitProcess closes the runtime and terminates. It is the one exit path: a
// listener error, a signal and a parent death all end here, so the runtime is
// never closed twice and the process never lingers with the port bound.
func (l *serveLifecycle) exitProcess(code int) {
	if l.bundle != nil {
		if err := l.bundle.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: error shutting down runtime: %v\n", err)
		}
	}
	l.exit(code)
}

// requestStop records why the server is going away. Only the first reason is
// kept: a signal arriving while a parent-death shutdown is already running is
// the same shutdown.
func (l *serveLifecycle) requestStop(reason string) {
	l.stopOnce.Do(func() {
		logging.Info("serve: shutting down", map[string]any{"reason": reason})
		close(l.stopRequest)
	})
}

// run binds the configured address and blocks until the process exits.
func (l *serveLifecycle) run(exitWithParent bool) {
	listener, err := net.Listen("tcp", l.server.Addr)
	if err != nil {
		reportListenError(err)
		l.exitProcess(1)
	}
	l.runWithListener(listener, exitWithParent)
}

// runWithListener is the half of run that does not bind, so a test can drive
// the shutdown path on a port it owns.
func (l *serveLifecycle) runWithListener(listener net.Listener, exitWithParent bool) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(signals)
	go func() {
		select {
		case received := <-signals:
			l.requestStop(received.String())
		case <-l.stopRequest:
		}
	}()

	if exitWithParent {
		go l.watchParentProcess()
	}

	go func() {
		<-l.stopRequest
		ctx, cancel := context.WithTimeout(context.Background(), serveShutdownTimeout)
		defer cancel()
		if err := l.server.Shutdown(ctx); err != nil {
			logging.Warn("serve: graceful shutdown timed out", map[string]any{
				"timeout": serveShutdownTimeout.String(),
				"error":   err.Error(),
			})
		}
		close(l.shutdownDone)
	}()

	err := l.server.Serve(listener)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		l.exitProcess(1)
	}

	// ListenAndServe returns once Shutdown begins; wait for it to finish so the
	// runtime closes after the last request drains, not before.
	select {
	case <-l.shutdownDone:
	case <-time.After(serveShutdownTimeout + time.Second):
		logging.Warn("serve: exiting without a clean shutdown", map[string]any{
			"timeout": (serveShutdownTimeout + time.Second).String(),
		})
	}
	l.exitProcess(0)
}

// watchParentProcess exits when the process that started this one disappears.
//
// It polls the parent pid rather than asking the OS for a death signal: the
// portable signal (PR_SET_PDEATHSIG) is Linux-only and must be armed before the
// exec, which a child process cannot do for itself. A pid change is the
// observable consequence either way, and a two-second delay costs nothing on a
// path that only runs when something has already gone wrong.
func (l *serveLifecycle) watchParentProcess() {
	original := os.Getppid()
	if original <= 1 {
		// Started by init/systemd: there is no supervisor pid to outlive, and
		// reparenting to init is the normal state rather than an exit.
		return
	}
	ticker := time.NewTicker(parentWatchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-l.stopRequest:
			return
		case <-ticker.C:
			if os.Getppid() != original {
				l.requestStop("parent process exited")
				return
			}
		}
	}
}

// reportListenError makes the common restart failure legible. "address already
// in use" is what an operator sees when the previous instance has not finished
// exiting, and the bare listen error does not say what to do about it.
func reportListenError(err error) {
	if errors.Is(err, syscall.EADDRINUSE) {
		fmt.Fprintf(os.Stderr, "Server error: %v\nAnother heron-ai server is already listening there. Stop it, or pass a different --port.\n", err)
		return
	}
	fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
}
