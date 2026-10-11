package plugin

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// SetStateFile configures standalone persistence before registration starts.
func SetStateFile(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("state file is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	mu.Lock()
	defer mu.Unlock()
	if loaded {
		return fmt.Errorf("plugin state is already loaded")
	}
	statePath = path
	return nil
}

// HTTPHandler serves the embedded plugin page and its declared API. The caller
// must authenticate API requests before forwarding them to this handler.
func HTTPHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body []byte
		if r.Body != nil {
			var err error
			body, err = io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
			if err != nil {
				http.Error(w, "request body is too large", http.StatusRequestEntityTooLarge)
				return
			}
		}
		resp := handleManagement(managementRequest{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Body: body})
		for name, values := range resp.Headers {
			w.Header()[name] = values
		}
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
		}
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(resp.Body)
	})
}

// HomePage reuses the evaluation UI with an independent panel password and a
// Home-specific catalog. No Home credentials are embedded in the page.
func HomePage() []byte {
	page := strings.NewReplacer(
		"<title>Codex 降智测试</title>", "<title>CPA Home · Codex 降智测试</title>",
		"<head>", "<head><script>window.CPA_HOME_EVAL = true;</script>",
		"<h2>连接 CPA</h2>", "<h2>连接 Home 测试面板</h2>",
		"请输入 CPA 管理密钥。登录 CPA 管理面板时勾选「记住密码」，本页会自动使用该密钥。", "请输入测试面板访问密钥。",
		"placeholder=\"管理密钥\" aria-label=\"管理密钥\"", "placeholder=\"面板访问密钥\" aria-label=\"面板访问密钥\"",
		"<div id=\"app\" hidden>", "<div id=\"app\" hidden><p class=\"panel-intro\">Home 中的 Codex 账号 · 每次请求最多 60 秒 · 测试消耗账号额度</p>",
	).Replace(string(uiHTML))
	return []byte(page)
}
