package obs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// ReadyFunc reports whether the service can do its job (for example, the
// database answers). A nil error means ready.
type ReadyFunc func(ctx context.Context) error

// Mux serves /metrics, /healthz (the process is alive) and /readyz (the
// dependencies are reachable).
func Mux(reg *Registry, ready ReadyFunc) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/metrics", reg.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := ready(ctx); err != nil {
			http.Error(w, "not ready: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintln(w, "ready")
	})
	return mux
}

// Serve runs the observability HTTP server until ctx is done and then shuts it
// down gracefully. It returns once the server has stopped.
func Serve(ctx context.Context, addr string, h http.Handler, log *slog.Logger) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	log.Info("metrics server listening", "addr", ln.Addr().String())

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Probe asks a running instance whether it is ready; it backs the container
// HEALTHCHECK, because the distroless image has no curl. addr is the metrics
// listen address (":9090" probes localhost).
func Probe(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("bad listen address %q: %w", addr, err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + net.JoinHostPort(host, port) + "/readyz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("readyz answered %s", resp.Status)
	}
	return nil
}
