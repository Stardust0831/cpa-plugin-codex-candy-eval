package homeeval

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"

	"cpa-codex-candy-eval/internal/plugin"
)

const pluginBase = "/v0/management/plugins/cpa-codex-candy-eval"

// Server is a separate panel process. It only exposes the plugin's evaluation
// routes and safe model catalogs, never an unrestricted Home management proxy.
// The underlying plugin uses process-wide state; create one Server per process.
type Server struct {
	client        *Client
	panelKeyHash  [32]byte
	pluginHandler http.Handler
	page          []byte
	closeOnce     sync.Once
}

func NewServer(cfg Config) (*Server, error) {
	if strings.TrimSpace(cfg.PanelKey) == "" {
		return nil, fmt.Errorf("CPA_HOME_EVAL_KEY is required")
	}
	if cfg.PanelKey == cfg.ManagementKey {
		return nil, fmt.Errorf("CPA_HOME_EVAL_KEY must differ from the Home management key")
	}
	client, err := NewClient(cfg)
	if err != nil {
		return nil, err
	}
	if cfg.DataDir == "" {
		cfg.DataDir = "./data"
	}
	if err := plugin.SetStateFile(filepath.Join(cfg.DataDir, "cpa-home-codex-eval-state.json")); err != nil {
		client.Close()
		return nil, err
	}
	plugin.SetHostCall(client.Call)
	var registration plugin.Envelope
	if err := json.Unmarshal(plugin.HandleMethod("plugin.register", nil), &registration); err != nil || !registration.OK {
		client.Close()
		return nil, fmt.Errorf("could not register evaluation plugin")
	}
	return &Server{client: client, panelKeyHash: sha256.Sum256([]byte(cfg.PanelKey)), pluginHandler: plugin.HTTPHandler(), page: plugin.HomePage()}, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.Method == http.MethodGet && (r.URL.Path == "/" || r.URL.Path == "/index.html") {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; img-src data:; font-src data:; base-uri 'none'; form-action 'none'; frame-ancestors 'self'")
		_, _ = w.Write(s.page)
		return
	}
	key, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	keyHash := sha256.Sum256([]byte(key))
	if !ok || subtle.ConstantTimeCompare(keyHash[:], s.panelKeyHash[:]) != 1 {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "面板访问密钥无效"})
		return
	}
	path := strings.TrimRight(r.URL.Path, "/")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/home/catalog":
		models, err := s.client.catalog(r.Context())
		if err != nil {
			writeHomeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"models": models})
	case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files/models":
		id := r.URL.Query().Get("name")
		files, err := s.client.credentials(r.Context())
		if err != nil {
			writeHomeError(w, err)
			return
		}
		found := false
		for _, file := range files {
			if file.ID == id {
				found = true
				break
			}
		}
		if !found {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "Codex 账号不存在"})
			return
		}
		models, err := s.client.credentialModels(r.Context(), id)
		if err != nil {
			writeHomeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"models": models})
	case strings.HasPrefix(path, pluginBase+"/") && path != pluginBase+"/credentials/sync":
		s.pluginHandler.ServeHTTP(w, r)
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "Route not found"})
	}
}

func writeHomeError(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// Close cancels in-flight requests and lets the plugin finish saving results.
func (s *Server) Close() {
	s.closeOnce.Do(func() {
		s.client.Close()
		plugin.Quiesce()
	})
}
