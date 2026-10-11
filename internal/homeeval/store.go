package homeeval

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"cpa-codex-candy-eval/internal/plugin"
)

const StoreID = "cpa-home-codex-eval"

// StorePlugin gives Home's standard dynamic library loader ownership of the
// same panel used by the standalone command. Home distributes libraries to CPA
// workers too; without local panel secrets they only register inert metadata.
// Secrets deliberately never come from the cluster-wide plugin configuration.
type StorePlugin struct {
	mu       sync.Mutex
	panel    *Server
	server   *http.Server
	listener net.Listener
	done     chan struct{}
	cancel   context.CancelFunc
}

func (p *StorePlugin) HandleMethod(method string, _ []byte) []byte {
	switch method {
	case "plugin.register", "plugin.reconfigure":
		if err := p.start(); err != nil {
			return storeError("home_panel_start_failed", err.Error(), http.StatusInternalServerError)
		}
		return storeResult(map[string]any{
			"schema_version": 6,
			"metadata": map[string]any{
				"Name": StoreID, "Version": plugin.Version,
				"Author":           "haowang02; Home adaptation: Stardust0831",
				"GitHubRepository": "https://github.com/Stardust0831/cpa-plugin-codex-candy-eval",
				"ConfigFields": []map[string]string{{
					"Name": "load-in-home", "Type": "boolean",
					"Description": "在 Home 控制面加载；仅配置 CPA_HOME_MANAGEMENT_KEY 和 CPA_HOME_EVAL_KEY 的进程启动独立测试面板。",
				}},
			},
			"capabilities": map[string]bool{"management_api": true},
		})
	case "management.register":
		// Home has no plugin resource routes, and workers must expose no panel.
		return storeResult(map[string]any{"routes": []any{}, "resources": []any{}})
	case "plugin.quiesce":
		p.Close()
		return storeResult(map[string]any{})
	default:
		return storeError("unknown_method", "Unsupported Home panel method", http.StatusNotFound)
	}
}

func (p *StorePlugin) start() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.server != nil {
		return nil
	}
	managementKey, panelKey := os.Getenv("CPA_HOME_MANAGEMENT_KEY"), os.Getenv("CPA_HOME_EVAL_KEY")
	if managementKey == "" && panelKey == "" {
		return nil
	}
	value := func(name, fallback string) string {
		if v := os.Getenv(name); v != "" {
			return v
		}
		return fallback
	}
	// Reserve the port before initializing shared evaluation state.
	listener, err := net.Listen("tcp", value("CPA_HOME_EVAL_LISTEN", "127.0.0.1:8320"))
	if err != nil {
		return err
	}
	panel, err := NewServer(Config{
		HomeURL: value("CPA_HOME_URL", "http://127.0.0.1:8317"), ManagementKey: managementKey,
		PanelKey: panelKey, DataDir: value("CPA_HOME_EVAL_DATA_DIR", "./data"),
	})
	if err != nil {
		_ = listener.Close()
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	server := &http.Server{Handler: panel, BaseContext: func(net.Listener) context.Context { return ctx }, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 90 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	p.panel, p.server, p.listener, p.done = panel, server, listener, make(chan struct{})
	p.cancel = cancel
	done := p.done
	go func() {
		defer close(done)
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("CPA Home evaluation panel stopped: %v", err)
		}
	}()
	log.Printf("CPA Home evaluation plugin listening on %s", listener.Addr())
	return nil
}

// Close is idempotent across plugin.quiesce and the C ABI shutdown callback.
func (p *StorePlugin) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.server == nil {
		return
	}
	// Cancel active HTTP handlers/model requests before the host can unload us.
	p.cancel()
	p.panel.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.server.Shutdown(ctx); err != nil {
		_ = p.server.Close()
	}
	<-p.done
	p.panel, p.server, p.listener, p.done = nil, nil, nil, nil
	p.cancel = nil
}

func storeResult(value any) []byte {
	raw, _ := json.Marshal(value)
	data, _ := json.Marshal(plugin.Envelope{OK: true, Result: raw})
	return data
}

func storeError(code, message string, status int) []byte {
	data, _ := json.Marshal(plugin.Envelope{Error: &plugin.EnvelopeError{Code: code, Message: message, HTTPStatus: status}})
	return data
}
