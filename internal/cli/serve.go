package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/InfiniteRoomLabs/yasshm/internal/audit"
	"github.com/InfiniteRoomLabs/yasshm/internal/auth"
	"github.com/InfiniteRoomLabs/yasshm/internal/config"
	"github.com/InfiniteRoomLabs/yasshm/internal/jobs"
	"github.com/InfiniteRoomLabs/yasshm/internal/mcpserver"
	"github.com/InfiniteRoomLabs/yasshm/internal/policy"
	"github.com/InfiniteRoomLabs/yasshm/internal/store"
	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
)

const serveUsage = `usage: yasshm serve --config <file>

Serves the MCP endpoint on the config's loopback listen address until
SIGTERM or SIGINT, then shuts down gracefully (a second signal forces the
exit). Logs are JSON lines on stderr.
`

// readyPoll is how often serve checks the authenticator's readiness to log
// a change.
const readyPoll = time.Second

// httpCloseWait bounds the wait for in-flight requests after the engine has
// shut down: their commands are gone, so they answer at once.
const httpCloseWait = 5 * time.Second

// runServe is the serve subcommand with the process's signals: the first
// SIGTERM or SIGINT starts a graceful shutdown, a second one exits at once.
func runServe(args []string, stdout, stderr io.Writer) int {
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(ch)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := make(chan struct{})
	defer close(stop)
	go watchSignals(ch, stop, cancel, func() {
		_, _ = fmt.Fprintln(stderr, `{"level":"ERROR","msg":"second signal: exiting without a graceful shutdown"}`)
		os.Exit(exitError)
	})
	return Serve(ctx, args, stdout, stderr)
}

// watchSignals cancels on the first signal and calls force on the second,
// until stop is closed.
func watchSignals(ch <-chan os.Signal, stop <-chan struct{}, cancel, force func()) {
	n := 0
	for {
		select {
		case <-ch:
			n++
			if n == 1 {
				cancel()
			} else {
				force()
				return
			}
		case <-stop:
			return
		}
	}
}

// Serve runs the gateway until ctx ends (spec 5.9): it loads the config,
// opens the audit log, the store and the job engine (one gateway per data
// directory), starts the authenticator, binds the listen address, writes
// gateway.started, and serves. When ctx ends it stops accepting, shuts the
// engine down within shutdown_grace_seconds, closes the guard (writing a
// pending summary of suppressed rejections) and the authenticator, writes
// gateway.stopped and returns 0. Logs are JSON lines on stderr; the line
// "listening" carries the address actually bound (port 0 asks the kernel).
// No log line holds a token, key material, command text, output, a host
// or a key path.
func Serve(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("config", "", "path of the config file")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			if _, werr := fmt.Fprint(stdout, serveUsage); werr != nil {
				return writeFailed(stderr, werr)
			}
			return exitOK
		}
		_, _ = fmt.Fprintf(stderr, "yasshm: %s\n\n%s", boundedText(err.Error()), serveUsage)
		return exitUsage
	}
	if fs.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "yasshm: unexpected argument %s\n\n%s", quoteBounded(fs.Arg(0)), serveUsage)
		return exitUsage
	}
	if *path == "" {
		_, _ = fmt.Fprintf(stderr, "yasshm: serve needs --config <file>\n\n%s", serveUsage)
		return exitUsage
	}
	log := slog.New(slog.NewJSONHandler(stderr, nil)).With("version", Version)
	g := &gateway{log: log}
	defer g.close(ctx)
	if err := g.start(ctx, *path); err != nil {
		log.Error("start-up failed", "reason", err.Error())
		return exitError
	}
	return g.serve(ctx)
}

// gateway is serve's state: what is open and must be closed.
type gateway struct {
	log     *slog.Logger
	cfg     *config.Config
	audit   *audit.Logger
	engine  *jobs.Engine
	auth    *auth.CloudflareAccess
	guard   *auth.Guard
	srv     *http.Server
	ln      net.Listener
	stopped bool
}

// startError is a start-up failure with a fixed, operator-facing reason.
type startError string

func (e startError) Error() string { return string(e) }

// start opens everything in order and binds the listener; on an error the
// caller's close releases what was opened.
func (g *gateway) start(ctx context.Context, path string) error {
	cfg, err := config.Load(path)
	if err != nil {
		var ce *config.Error
		if errors.As(err, &ce) {
			for _, p := range ce.Problems {
				g.log.Error("config problem", "problem", p.String())
			}
		}
		return startError("the config is not valid")
	}
	g.cfg = cfg
	g.log.Info("config loaded", "tenants", len(cfg.Tenants), "servers", serverCount(cfg.Tenants))

	if g.audit, err = audit.Open(cfg.Gateway.AuditLog); err != nil {
		return startError("the audit log cannot be opened (it must be a regular file owned by this user, mode 0600)")
	}
	g.engine, err = jobs.New(ctx, jobs.Config{
		DataDir: cfg.Gateway.DataDir,
		Tenants: cfg.Tenants,
		Audit:   g.audit,
		OnError: func(err error) { g.log.Error("job engine error", "code", string(jobs.CodeOf(err))) },
	})
	switch {
	case errors.Is(err, store.ErrLocked):
		return startError("the data directory is in use by another gateway")
	case errors.Is(err, store.ErrUnsafe):
		return startError("the data directory or database is not private to this user (0700 directory, 0600 files)")
	case errors.Is(err, store.ErrTooNew):
		return startError("the database was written by a newer yasshm")
	case err != nil:
		return startError("the job engine cannot start (is the data directory there?)")
	}
	pe, err := policy.NewEngine(cfg.Tenants)
	if err != nil {
		return startError("the policy cannot be compiled")
	}
	g.auth, err = auth.NewCloudflareAccess(cfg.Auth.CloudflareAccess, cfg.Tenants, auth.WithLogger(g.log))
	if err != nil {
		return startError("the authenticator cannot be built")
	}
	g.auth.Start(ctx)
	g.guard = auth.NewGuard(g.auth, g.audit, auth.WithMiddlewareLogger(g.log))
	gw, err := mcpserver.New(mcpserver.Config{
		Version: Version, Tenants: cfg.Tenants, Policy: pe, Jobs: g.engine, Audit: g.audit,
		Guard: g.guard, Health: g.auth, Logger: g.log,
	})
	if err != nil {
		return startError("the MCP endpoint cannot be built")
	}
	g.srv = gw.HTTPServer()
	var lc net.ListenConfig
	if g.ln, err = lc.Listen(ctx, "tcp", cfg.Gateway.Listen); err != nil {
		return startError("the listen address cannot be bound")
	}
	if err := g.audit.Write(audit.GatewayStarted(Version, g.ln.Addr().String())); err != nil {
		return startError("gateway.started cannot be written to the audit log")
	}
	return nil
}

// serve accepts requests until ctx ends or the server fails, then shuts
// down and returns the exit code.
func (g *gateway) serve(ctx context.Context) int {
	g.log.Info("listening", "listen", g.ln.Addr().String(), "write_timeout_seconds", int(g.srv.WriteTimeout/time.Second))
	failed := make(chan error, 1)
	go func() { failed <- g.srv.Serve(g.ln) }()
	// Shutdown runs after ctx has ended: what it waits for is bounded by its
	// own timeouts, not by ctx.
	rest := context.WithoutCancel(ctx)
	watchCtx, stopWatch := context.WithCancel(rest)
	watched := make(chan struct{})
	go func() {
		defer close(watched)
		g.watchReady(watchCtx)
	}()
	reason := audit.StopSignal
	select {
	case <-ctx.Done():
		g.log.Info("shutting down", "reason", "signal")
	case err := <-failed:
		g.log.Error("the server failed", "reason", errorKind(err))
		reason = audit.StopError
	}
	stopWatch()
	<-watched
	g.shutdown(rest, reason)
	if reason == audit.StopError {
		return exitError
	}
	return exitOK
}

func errorKind(err error) string {
	if errors.Is(err, http.ErrServerClosed) {
		return "closed"
	}
	return "accept"
}

// watchReady logs every change of the authenticator's readiness.
func (g *gateway) watchReady(ctx context.Context) {
	ready := g.auth.Ready()
	g.log.Info("readiness", "ready", ready, "status", g.auth.Status())
	t := time.NewTicker(readyPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if r := g.auth.Ready(); r != ready {
				ready = r
				g.log.Info("readiness", "ready", ready, "status", g.auth.Status())
			}
		}
	}
}

// grace is the largest shutdown_grace_seconds of the tenants.
func (g *gateway) grace() time.Duration {
	var d int64
	for i := range g.cfg.Tenants {
		d = max(d, g.cfg.Tenants[i].Limits.ShutdownGraceSeconds)
	}
	return time.Duration(d) * time.Second
}

// shutdown stops accepting, shuts the engine down within the grace (the
// commands are ended and marked killed, so requests waiting on them answer),
// waits briefly for in-flight requests, closes the guard and the
// authenticator and writes gateway.stopped.
func (g *gateway) shutdown(ctx context.Context, reason audit.StopReason) {
	ctx, cancel := context.WithTimeout(ctx, g.grace()+httpCloseWait)
	defer cancel()
	httpDone := make(chan struct{})
	go func() {
		defer close(httpDone)
		if err := g.srv.Shutdown(ctx); err != nil {
			_ = g.srv.Close()
		}
	}()
	if err := g.engine.Shutdown(ctx); err != nil {
		g.log.Error("job engine shutdown incomplete", "code", string(jobs.CodeOf(err)))
	}
	g.engine = nil
	<-httpDone
	g.guard.Close()
	g.auth.Close()
	g.auth = nil
	if err := g.audit.Write(audit.GatewayStopped(reason)); err != nil {
		g.log.Error("gateway.stopped cannot be written to the audit log")
	}
	g.stopped = true
	g.log.Info("stopped", "reason", reason.String())
}

// close releases whatever start opened, also after a failed start.
func (g *gateway) close(ctx context.Context) {
	if g.ln != nil && !g.stopped {
		_ = g.ln.Close()
	}
	if g.engine != nil {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		_ = g.engine.Shutdown(sctx)
		cancel()
	}
	if g.auth != nil {
		g.auth.Close()
	}
	if g.audit != nil {
		_ = g.audit.Close()
	}
}

// serverCount counts the servers of ts.
func serverCount(ts []tenant.Tenant) int {
	n := 0
	for i := range ts {
		n += len(ts[i].Servers)
	}
	return n
}
