package homeeval

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPanelEvaluationAndIsolation(t *testing.T) {
	home := mockHome(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"status_code": 200, "body": `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"答案是 21"}]}],"usage":{"input_tokens":7,"output_tokens":3}}`})
	})
	dataDir := t.TempDir()
	panel, err := NewServer(Config{HomeURL: home.URL, ManagementKey: testManagementKey, PanelKey: "synthetic-panel-key", DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	defer panel.Close()
	request := func(method, path, key, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if key != "" {
			r.Header.Set("Authorization", "Bearer "+key)
		}
		w := httptest.NewRecorder()
		panel.ServeHTTP(w, r)
		for _, secret := range []string{testManagementKey, "synthetic-oauth-secret", "synthetic-refresh-secret", "synthetic-private-proxy"} {
			if strings.Contains(w.Body.String(), secret) {
				t.Errorf("panel leaked secret %s", secret)
			}
		}
		return w
	}
	page := request("GET", "/", "", "")
	if page.Code != 200 || !strings.Contains(page.Body.String(), "window.CPA_HOME_EVAL = true") || !strings.Contains(page.Body.String(), "连接 Home 测试面板") {
		t.Fatal("missing Home UI")
	}
	if page.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("missing page CSP")
	}
	for _, key := range []string{"", "wrong-key", testManagementKey} {
		if got := request("GET", pluginBase+"/state", key, ""); got.Code != 401 {
			t.Fatalf("accepted invalid panel key: %d", got.Code)
		}
	}
	for _, path := range []string{"/v8/management/config", "/v8/management/credentials/download?id=uuid-a", "/v0/management/auth-files/status", pluginBase + "/credentials/sync"} {
		if got := request("PATCH", path, "synthetic-panel-key", `{}`); got.Code != 404 {
			t.Errorf("exposed management route %s: %d", path, got.Code)
		}
	}
	for _, path := range []string{pluginBase + "/credentials/sync", pluginBase + "/credentials/sync/"} {
		if got := request("POST", path, "synthetic-panel-key", `{"credentials":[]}`); got.Code != 404 {
			t.Errorf("exposed credential sync %s: %d", path, got.Code)
		}
	}
	if got := request("GET", "/api/home/catalog", "synthetic-panel-key", ""); got.Code != 200 || !strings.Contains(got.Body.String(), "friendly") {
		t.Fatalf("catalog: %s", got.Body)
	}
	if got := request("GET", "/v0/management/auth-files/models?name=uuid-other", "synthetic-panel-key", ""); got.Code != 404 {
		t.Fatal("exposed non-Codex account models")
	}
	if got := request("POST", pluginBase+"/run", "synthetic-panel-key", `{"auth_ids":["uuid-a"],"model":"friendly","effort":"low","runs":1}`); got.Code != 200 {
		t.Fatalf("start: %s", got.Body)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		state := request("GET", pluginBase+"/state", "synthetic-panel-key", "")
		var result struct {
			Auths []struct {
				ID      string          `json:"id"`
				Running json.RawMessage `json:"running"`
				Results []struct {
					OK     bool   `json:"ok"`
					Answer string `json:"answer"`
				} `json:"results"`
			} `json:"auths"`
		}
		if state.Code != 200 {
			t.Fatalf("state: %s", state.Body)
		}
		if err := json.Unmarshal(state.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		completed := false
		for _, auth := range result.Auths {
			if auth.ID == "uuid-a" && len(auth.Results) == 1 && len(auth.Running) == 0 {
				if !auth.Results[0].OK || auth.Results[0].Answer != "答案是 21" {
					t.Fatalf("bad result: %+v", auth)
				}
				completed = true
			}
		}
		if completed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("simulated evaluation did not complete")
		}
		time.Sleep(10 * time.Millisecond)
	}
	panel.Close()
	file, err := os.Open(filepath.Join(dataDir, "cpa-home-codex-eval-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	saved, _ := io.ReadAll(file)
	if !json.Valid(saved) || !strings.Contains(string(saved), "uuid-a") || !strings.Contains(string(saved), `"ok":true`) {
		t.Fatalf("missing persisted history: %s", saved)
	}
}

func TestRejectMissingOrSharedPanelKey(t *testing.T) {
	for _, key := range []string{"", testManagementKey} {
		if _, err := NewServer(Config{HomeURL: "http://127.0.0.1:8317", ManagementKey: testManagementKey, PanelKey: key}); err == nil {
			t.Errorf("accepted panel key %q", key)
		}
	}
}
