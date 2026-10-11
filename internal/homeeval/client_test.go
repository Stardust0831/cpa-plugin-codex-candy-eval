package homeeval

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cpa-codex-candy-eval/internal/plugin"
)

const testManagementKey = "synthetic-home-management-secret"

func mockHome(t *testing.T, call http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testManagementKey {
			t.Errorf("missing server-side Home authentication")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v8/management/credentials":
			io.WriteString(w, `{"files":[{"id":"uuid-a","auth_index":"wrong-index","name":"a.json","provider":"codex","email":"a@example.test","proxy_url":"synthetic-private-proxy","access_token":"synthetic-oauth-secret"},{"id":"uuid-disabled","provider":"codex","disabled":true},{"id":"uuid-other","provider":"claude"}]}`)
		case "/v8/management/credentials/download":
			if id := r.URL.Query().Get("id"); id != "uuid-a" && id != "uuid-disabled" {
				t.Error("wrong credential download ID")
			}
			claims := base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_plan_type":"plus","chatgpt_account_id":"account-a"}}`))
			json.NewEncoder(w).Encode(map[string]string{"id_token": "header." + claims + ".signature", "access_token": "synthetic-oauth-secret", "refresh_token": "synthetic-refresh-secret"})
		case "/v8/management/credentials/models":
			io.WriteString(w, `{"models":[{"id":"friendly"}]}`)
		case "/v8/management/config/oauth/model-alias/codex":
			io.WriteString(w, `[{"name":"gpt-test","alias":"friendly"}]`)
		case "/v8/management/requests/api-call":
			call(w, r)
		default:
			t.Errorf("unexpected Home route: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func newTestClient(t *testing.T, home string) *Client {
	t.Helper()
	c, err := NewClient(Config{HomeURL: home, ManagementKey: testManagementKey})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func TestPinnedCodexRequest(t *testing.T) {
	for _, format := range []string{"sse", "json"} {
		t.Run(format, func(t *testing.T) {
			called := false
			home := mockHome(t, func(w http.ResponseWriter, r *http.Request) {
				called = true
				var req struct {
					AuthIndex string            `json:"auth_index"`
					Method    string            `json:"method"`
					URL       string            `json:"url"`
					Header    map[string]string `json:"header"`
					Data      string            `json:"data"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				if req.AuthIndex != "uuid-a" || req.Method != "POST" || req.URL != defaultCodexURL {
					t.Errorf("call was not pinned to expected account/upstream: %+v", req)
				}
				if req.Header["Authorization"] != "Bearer $TOKEN$" || req.Header["Chatgpt-Account-Id"] != "account-a" {
					t.Errorf("wrong auth headers: %+v", req.Header)
				}
				if req.Header["Session_id"] != "session-a" {
					t.Errorf("original session header lost: %+v", req.Header)
				}
				var body map[string]any
				json.Unmarshal([]byte(req.Data), &body)
				if body["model"] != "gpt-test" || body["stream"] != true || body["store"] != false || body["temperature"] != nil {
					t.Errorf("request normalization failed: %+v", body)
				}
				if _, ok := body["input"].([]any); !ok {
					t.Errorf("string input not normalized")
				}
				if body["reasoning"].(map[string]any)["effort"] != "high" {
					t.Error("model effort suffix lost")
				}
				response := `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"21"}]}]}`
				if format == "sse" {
					response = "data: {\"type\":\"response.completed\",\"response\":" + response + "}\n\n"
				}
				json.NewEncoder(w).Encode(map[string]any{"status_code": 200, "body": response})
			})
			c := newTestClient(t, home.URL+"/v8/management")
			result, err := c.execute(context.Background(), "uuid-a", "prefix/friendly(high)", []byte(`{"input":"test","temperature":1}`), http.Header{"Authorization": {"must-not-forward"}, "Session_id": {"session-a"}})
			if err != nil {
				t.Fatal(err)
			}
			if !called || !strings.Contains(string(result), "response.completed") {
				t.Errorf("missing completed response: %s", result)
			}
			metadata, err := c.Call("host.auth.get", map[string]string{"auth_index": "uuid-a"})
			if err != nil || string(metadata) != `{"json":{"plan_type":"plus"}}` {
				t.Errorf("safe metadata: %s, %v", metadata, err)
			}
			files, err := c.Call("host.auth.list", nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"wrong-index", "uuid-other", "synthetic-oauth-secret", "synthetic-private-proxy", testManagementKey} {
				if strings.Contains(string(files), secret) {
					t.Errorf("credential list leaked %s", secret)
				}
			}
		})
	}
}

func TestDisabledOrMissingAccountDoesNotExecute(t *testing.T) {
	home := mockHome(t, func(http.ResponseWriter, *http.Request) { t.Error("unavailable account executed") })
	c := newTestClient(t, home.URL)
	for _, id := range []string{"uuid-disabled", "uuid-other", "uuid-missing"} {
		if _, err := c.execute(context.Background(), id, "gpt-test", []byte(`{}`), nil); err == nil {
			t.Errorf("accepted account %s", id)
		}
	}
}

func TestUpstreamErrorDoesNotExposeBody(t *testing.T) {
	home := mockHome(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"status_code": 429, "body": "synthetic-oauth-secret"})
	})
	c := newTestClient(t, home.URL)
	_, err := c.execute(context.Background(), "uuid-a", "friendly", []byte(`{}`), nil)
	var envelope *plugin.EnvelopeError
	if !errors.As(err, &envelope) || envelope.HTTPStatus != 429 || strings.Contains(err.Error(), "synthetic-oauth-secret") {
		t.Fatalf("unsafe/wrong upstream error: %v", err)
	}
}

func TestStreamCloseCancelsPendingHomeRequest(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	home := mockHome(t, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
		close(cancelled)
	})
	c := newTestClient(t, home.URL)
	raw, err := c.Call("host.model.execute_stream", map[string]any{"auth_id": "uuid-a", "forced_provider": "codex", "model": "friendly", "body": []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	var stream map[string]string
	json.Unmarshal(raw, &stream)
	read := make(chan error, 1)
	go func() { _, err := c.Call("host.model.stream_read", stream); read <- err }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("request not started")
	}
	c.Call("host.model.stream_close", stream)
	select {
	case err := <-read:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("read error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("read not cancelled")
	}
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("Home HTTP request not cancelled")
	}
}

func TestManagementRedirectNeverForwardsKey(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1); io.WriteString(w, `{}`) }))
	defer target.Close()
	home := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer home.Close()
	c := newTestClient(t, home.URL)
	_, err := c.credentials(context.Background())
	if err == nil || redirected.Load() != 0 {
		t.Fatalf("management redirect followed: %v", err)
	}
}

func TestHomeErrorBodyIsNotExposed(t *testing.T) {
	home := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401); io.WriteString(w, testManagementKey) }))
	defer home.Close()
	c := newTestClient(t, home.URL)
	_, err := c.credentials(context.Background())
	if err == nil || strings.Contains(err.Error(), testManagementKey) {
		t.Fatalf("unsafe management error: %v", err)
	}
}
