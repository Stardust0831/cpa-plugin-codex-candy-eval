// Command cpa-home-codex-eval runs the plugin panel alongside CPA Home.
package main

import (
	"context"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"cpa-codex-candy-eval/internal/homeeval"
)

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func main() {
	listen := flag.String("listen", env("CPA_HOME_EVAL_LISTEN", "127.0.0.1:8320"), "panel listen address")
	homeURL := flag.String("home-url", env("CPA_HOME_URL", "http://127.0.0.1:8317"), "CPA Home address")
	dataDir := flag.String("data-dir", env("CPA_HOME_EVAL_DATA_DIR", "./data"), "plugin test history directory")
	flag.Parse()
	panel, err := homeeval.NewServer(homeeval.Config{HomeURL: *homeURL, ManagementKey: os.Getenv("CPA_HOME_MANAGEMENT_KEY"), PanelKey: os.Getenv("CPA_HOME_EVAL_KEY"), DataDir: *dataDir})
	if err != nil {
		log.Fatal(err)
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		panel.Close()
		log.Fatal(err)
	}
	server := &http.Server{Handler: panel, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 90 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result := make(chan error, 1)
	go func() { result <- server.Serve(listener) }()
	log.Printf("CPA Home evaluation panel listening on %s", listener.Addr())
	select {
	case <-ctx.Done():
	case err = <-result:
		if err != http.ErrServerClosed {
			log.Printf("panel stopped: %v", err)
		}
	}
	// Cancel model calls first so graceful shutdown does not wait for Home's timeout.
	panel.Close()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		_ = server.Close()
	}
}
