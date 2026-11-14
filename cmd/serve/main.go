// Command serve runs the API and the dashboard locally:
//
//	go run ./cmd/serve            # http://127.0.0.1:8080
//
// It mounts exactly the handler that the Vercel function uses, plus the static
// dashboard from ./public, so what you see here is what gets deployed.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"mime"
	"net/http"
	"os"
	"os/signal"
	"time"

	"jobscraper/internal/api"
)

// securityHeaders applies the same policy vercel.json sets in production, so a
// script or style that would be blocked there is blocked here too.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "address to listen on (localhost only by default)")
	static := flag.String("static", "public", "directory with the dashboard files")
	flag.Parse()

	// Go's built-in table has no entry for web fonts (Vercel serves them correctly).
	if err := mime.AddExtensionType(".woff2", "font/woff2"); err != nil {
		log.Fatal(err)
	}

	if _, err := os.Stat(*static); err != nil {
		log.Fatalf("static directory %q not found: %v (run from the project root)", *static, err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	srv, closeDB, err := api.NewFromEnv(ctx)
	if err != nil {
		log.Fatalf("cannot start: %v", err)
	}
	defer closeDB()

	mux := http.NewServeMux()
	mux.Handle("/api/", srv)
	mux.Handle("/", http.FileServer(http.Dir(*static)))

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           securityHeaders(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}()

	fmt.Printf("dashboard and API on http://%s  (Ctrl-C to stop)\n", *addr)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
