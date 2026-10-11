// Package homeeval adapts the existing plugin to Home's public Management API.
package homeeval

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"cpa-codex-candy-eval/internal/plugin"
)

const defaultCodexURL = "https://chatgpt.com/backend-api/codex/responses"

type Config struct {
	HomeURL       string
	ManagementKey string
	PanelKey      string
	DataDir       string
	CodexURL      string
}

type Client struct {
	baseURL       *url.URL
	managementKey string
	codexURL      string
	httpClient    *http.Client
	mu            sync.Mutex
	metadata      map[string]credentialMetadata
	streams       map[string]*modelStream
	ctx           context.Context
	cancel        context.CancelFunc
}

type credentialMetadata struct {
	PlanType  string
	AccountID string
	Expires   time.Time
}

type credential struct {
	ID             string    `json:"id"`
	AuthIndex      string    `json:"auth_index"`
	Name           string    `json:"name"`
	Provider       string    `json:"provider"`
	Type           string    `json:"type"`
	Source         string    `json:"source"`
	Email          string    `json:"email"`
	PlanType       string    `json:"plan_type,omitempty"`
	Priority       int       `json:"priority"`
	Disabled       bool      `json:"disabled"`
	Unavailable    bool      `json:"unavailable"`
	StatusMessage  string    `json:"status_message,omitempty"`
	NextRetryAfter time.Time `json:"next_retry_after,omitzero"`
}

type model struct {
	ID string `json:"id"`
}

type modelStream struct {
	cancel context.CancelFunc
	result chan streamResult
	read   sync.Mutex
	done   bool
}

type homeAPIError struct {
	path   string
	status int
}

func (e *homeAPIError) Error() string {
	return fmt.Sprintf("Home 接口 %s 返回 HTTP %d", e.path, e.status)
}

type streamResult struct {
	payload []byte
	err     error
}

func NewClient(cfg Config) (*Client, error) {
	base, err := url.Parse(strings.TrimSpace(cfg.HomeURL))
	if err != nil || base == nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, fmt.Errorf("CPA_HOME_URL must be an HTTP(S) Home address without credentials, query, or fragment")
	}
	base.Path = strings.TrimRight(base.Path, "/")
	for _, suffix := range []string{"/v8/management", "/v0/management"} {
		base.Path = strings.TrimSuffix(base.Path, suffix)
	}
	if strings.TrimSpace(cfg.ManagementKey) == "" {
		return nil, fmt.Errorf("CPA_HOME_MANAGEMENT_KEY is required")
	}
	if cfg.CodexURL == "" {
		cfg.CodexURL = defaultCodexURL
	}
	upstream, err := url.Parse(cfg.CodexURL)
	if err != nil || (upstream.Scheme != "http" && upstream.Scheme != "https") || upstream.Host == "" || upstream.User != nil || upstream.Fragment != "" {
		return nil, fmt.Errorf("invalid Codex Responses URL")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Client{
		baseURL: base, managementKey: cfg.ManagementKey, codexURL: cfg.CodexURL,
		httpClient: &http.Client{Timeout: 70 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		metadata:   make(map[string]credentialMetadata), streams: make(map[string]*modelStream),
		ctx: ctx, cancel: cancel,
	}, nil
}

func (c *Client) request(ctx context.Context, method, path string, query url.Values, body any) (json.RawMessage, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	u := *c.baseURL
	u.Path += "/v8/management" + path
	u.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.managementKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("无法连接 Home，请检查地址和网络")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Never expose error pages that could echo the management key or tokens.
		return nil, &homeAPIError{path: path, status: resp.StatusCode}
	}
	const maxResponse = 32 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return nil, fmt.Errorf("读取 Home 响应失败")
	}
	if len(data) > maxResponse || !json.Valid(data) {
		return nil, fmt.Errorf("Home 响应过大或不是有效 JSON")
	}
	return data, nil
}

func (c *Client) credentials(ctx context.Context) ([]credential, error) {
	raw, err := c.request(ctx, http.MethodGet, "/credentials", nil, nil)
	if err != nil {
		return nil, err
	}
	var result struct {
		Files []credential `json:"files"`
	}
	if err := json.Unmarshal(raw, &result); err != nil || result.Files == nil {
		return nil, fmt.Errorf("Home 凭证列表格式无效")
	}
	files := make([]credential, 0, len(result.Files))
	for _, file := range result.Files {
		if file.Provider == "" {
			file.Provider = file.Type
		}
		if !strings.EqualFold(file.Provider, "codex") || file.ID == "" {
			continue
		}
		file.Provider, file.Type, file.AuthIndex, file.Source = "codex", "codex", file.ID, "db"
		if file.Unavailable && !file.NextRetryAfter.IsZero() && !file.NextRetryAfter.After(time.Now()) {
			file.Unavailable = false
		}
		if file.Name == "" {
			file.Name = file.ID
		}
		files = append(files, file)
	}
	return files, nil
}

func (c *Client) credentialModels(ctx context.Context, id string) ([]model, error) {
	raw, err := c.request(ctx, http.MethodGet, "/credentials/models", url.Values{"name": {id}}, nil)
	if err != nil {
		return nil, err
	}
	var result struct {
		Models []model `json:"models"`
	}
	if err := json.Unmarshal(raw, &result); err != nil || result.Models == nil {
		return nil, fmt.Errorf("Home 模型列表格式无效")
	}
	return result.Models, nil
}

func (c *Client) catalog(ctx context.Context) ([]model, error) {
	files, err := c.credentials(ctx)
	if err != nil {
		return nil, err
	}
	ids := make(map[string]bool)
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)
	for _, file := range files {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			models, err := c.credentialModels(ctx, id)
			if err != nil {
				return
			}
			mu.Lock()
			for _, m := range models {
				if m.ID != "" {
					ids[m.ID] = true
				}
			}
			mu.Unlock()
		}(file.ID)
	}
	wg.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	// The static catalog remains available when there are no active Codex accounts.
	if len(ids) == 0 {
		raw, err := c.request(ctx, http.MethodGet, "/models", url.Values{"scope": {"static"}, "channel": {"codex"}}, nil)
		if err != nil {
			return nil, err
		}
		var result struct {
			Models []model `json:"models"`
		}
		if err := json.Unmarshal(raw, &result); err != nil || result.Models == nil {
			return nil, fmt.Errorf("Home Codex 模型列表格式无效")
		}
		for _, m := range result.Models {
			if m.ID != "" {
				ids[m.ID] = true
			}
		}
	}
	models := make([]model, 0, len(ids))
	for id := range ids {
		models = append(models, model{ID: id})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}

func (c *Client) credentialMetadata(ctx context.Context, id string) (credentialMetadata, error) {
	c.mu.Lock()
	meta, ok := c.metadata[id]
	c.mu.Unlock()
	if ok && time.Now().Before(meta.Expires) {
		return meta, nil
	}
	raw, err := c.request(ctx, http.MethodGet, "/credentials/download", url.Values{"id": {id}}, nil)
	if err != nil {
		return credentialMetadata{}, err
	}
	var file struct {
		PlanType  string `json:"plan_type"`
		AccountID string `json:"account_id"`
		IDToken   string `json:"id_token"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return credentialMetadata{}, fmt.Errorf("Home 凭证格式无效")
	}
	meta = credentialMetadata{PlanType: file.PlanType, AccountID: file.AccountID, Expires: time.Now().Add(5 * time.Minute)}
	parts := strings.Split(file.IDToken, ".")
	if len(parts) == 3 {
		payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
		if err == nil {
			var claims struct {
				Auth struct {
					PlanType  string `json:"chatgpt_plan_type"`
					AccountID string `json:"chatgpt_account_id"`
				} `json:"https://api.openai.com/auth"`
			}
			if json.Unmarshal(payload, &claims) == nil {
				if meta.PlanType == "" {
					meta.PlanType = claims.Auth.PlanType
				}
				if meta.AccountID == "" {
					meta.AccountID = claims.Auth.AccountID
				}
			}
		}
	}
	// Cache only display/routing metadata, never OAuth tokens.
	c.mu.Lock()
	for key, cached := range c.metadata {
		if time.Now().After(cached.Expires) {
			delete(c.metadata, key)
		}
	}
	c.metadata[id] = meta
	c.mu.Unlock()
	return meta, nil
}

// Call implements the small host callback surface used by the evaluation plugin.
func (c *Client) Call(method string, payload any) (json.RawMessage, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	switch method {
	case "host.auth.list":
		files, err := c.credentials(c.ctx)
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"files": files})
	case "host.auth.get":
		var req struct {
			AuthIndex string `json:"auth_index"`
		}
		if json.Unmarshal(data, &req) != nil || req.AuthIndex == "" {
			return nil, fmt.Errorf("auth_index is required")
		}
		meta, err := c.credentialMetadata(c.ctx, req.AuthIndex)
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"json": map[string]string{"plan_type": meta.PlanType}})
	case "host.model.execute_stream":
		return c.openStream(data)
	case "host.model.stream_read", "host.model.stream_close":
		var req struct {
			ID string `json:"stream_id"`
		}
		if json.Unmarshal(data, &req) != nil || req.ID == "" {
			return nil, fmt.Errorf("stream_id is required")
		}
		c.mu.Lock()
		stream := c.streams[req.ID]
		if method == "host.model.stream_close" {
			delete(c.streams, req.ID)
		}
		c.mu.Unlock()
		if method == "host.model.stream_close" {
			if stream != nil {
				stream.cancel()
			}
			return json.RawMessage(`{}`), nil
		}
		if stream == nil {
			return nil, fmt.Errorf("model stream is closed")
		}
		stream.read.Lock()
		defer stream.read.Unlock()
		if stream.done {
			return json.RawMessage(`{"done":true}`), nil
		}
		result := <-stream.result
		stream.done = true
		if result.err != nil {
			return nil, result.err
		}
		return json.Marshal(map[string]any{"payload": result.payload, "done": true})
	default:
		return nil, fmt.Errorf("unsupported Home host callback: %s", method)
	}
}

func (c *Client) openStream(data []byte) (json.RawMessage, error) {
	var req struct {
		AuthID   string      `json:"auth_id"`
		Provider string      `json:"forced_provider"`
		Model    string      `json:"model"`
		Body     []byte      `json:"body"`
		Headers  http.Header `json:"headers"`
	}
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, err
	}
	if req.AuthID == "" || req.Provider != "codex" {
		return nil, fmt.Errorf("Home 模式仅支持指定 Codex OAuth 账号")
	}
	ctx, cancel := context.WithCancel(c.ctx)
	stream := &modelStream{cancel: cancel, result: make(chan streamResult, 1)}
	var idBytes [16]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		cancel()
		return nil, err
	}
	id := hex.EncodeToString(idBytes[:])
	c.mu.Lock()
	c.streams[id] = stream
	c.mu.Unlock()
	go func() {
		defer cancel()
		body, err := c.execute(ctx, req.AuthID, req.Model, req.Body, req.Headers)
		stream.result <- streamResult{payload: body, err: err}
	}()
	return json.Marshal(map[string]string{"stream_id": id})
}

func (c *Client) execute(ctx context.Context, id, selectedModel string, body []byte, headers http.Header) ([]byte, error) {
	files, err := c.credentials(ctx)
	if err != nil {
		return nil, err
	}
	var auth *credential
	for i := range files {
		if files[i].ID == id {
			auth = &files[i]
			break
		}
	}
	if auth == nil || auth.Disabled || auth.Unavailable {
		return nil, fmt.Errorf("所选 Home 账号不存在、已停用或暂不可用")
	}
	meta, err := c.credentialMetadata(ctx, id)
	if err != nil {
		return nil, err
	}
	var request map[string]any
	if json.Unmarshal(body, &request) != nil || request == nil {
		return nil, fmt.Errorf("invalid Responses request")
	}
	// Home's api-call is raw HTTP, so perform the Codex request normalization here.
	modelName := selectedModel
	if i := strings.LastIndex(modelName, "/"); i >= 0 {
		modelName = modelName[i+1:]
	}
	if strings.HasSuffix(modelName, ")") {
		if i := strings.LastIndex(modelName, "("); i >= 0 {
			reasoning, _ := request["reasoning"].(map[string]any)
			if reasoning == nil {
				reasoning = make(map[string]any)
			}
			reasoning["effort"] = modelName[i+1 : len(modelName)-1]
			request["reasoning"], modelName = reasoning, modelName[:i]
		}
	}
	// Resolve Home's OAuth aliases instead of silently testing a different model.
	rawAliases, aliasErr := c.request(ctx, http.MethodGet, "/config/oauth/model-alias/codex", nil, nil)
	if aliasErr != nil {
		var apiErr *homeAPIError
		if !errors.As(aliasErr, &apiErr) || apiErr.status != http.StatusNotFound {
			return nil, aliasErr
		}
	} else {
		var aliases []struct {
			Name  string `json:"name"`
			Alias string `json:"alias"`
		}
		if string(rawAliases) != "null" && json.Unmarshal(rawAliases, &aliases) != nil {
			return nil, fmt.Errorf("Home 模型别名格式无效")
		}
		for _, alias := range aliases {
			if alias.Alias == modelName {
				modelName = alias.Name
				break
			}
		}
	}
	request["model"], request["stream"], request["store"] = modelName, true, false
	delete(request, "temperature")
	if input, ok := request["input"].(string); ok {
		request["input"] = []any{map[string]any{"role": "user", "content": []any{map[string]string{"type": "input_text", "text": input}}}}
	}
	if _, ok := request["instructions"]; !ok {
		request["instructions"] = ""
	}
	requestBody, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	requestHeaders := map[string]string{
		"Authorization": "Bearer $TOKEN$", "Content-Type": "application/json", "Accept": "text/event-stream",
		"Originator": "codex-tui", "User-Agent": "codex-tui/0.160.0 (Ubuntu 26.4.0; aarch64) ghostty/1.3.1 (codex-tui; 0.160.0)",
	}
	for name, values := range headers {
		if len(values) > 0 && !strings.EqualFold(name, "Authorization") && !strings.EqualFold(name, "Host") {
			requestHeaders[name] = values[0]
		}
	}
	if meta.AccountID != "" {
		requestHeaders["Chatgpt-Account-Id"] = meta.AccountID
	}
	raw, err := c.request(ctx, http.MethodPost, "/requests/api-call", nil, map[string]any{
		"auth_index": id, "method": http.MethodPost, "url": c.codexURL, "header": requestHeaders, "data": string(requestBody),
	})
	if err != nil {
		return nil, err
	}
	var result struct {
		Status int    `json:"status_code"`
		Body   string `json:"body"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Status == 0 {
		return nil, fmt.Errorf("Home api-call 响应格式无效")
	}
	if result.Status < 200 || result.Status >= 300 {
		// Upstream response bodies may contain credential material; only expose status.
		return nil, &plugin.EnvelopeError{Code: "home_upstream_error", Message: fmt.Sprintf("Codex 上游返回 HTTP %d", result.Status), HTTPStatus: result.Status}
	}
	if json.Valid([]byte(result.Body)) {
		return []byte("data: {\"type\":\"response.completed\",\"response\":" + result.Body + "}\n\n"), nil
	}
	return []byte(result.Body), nil
}

// Close cancels pending HTTP requests before the plugin is quiesced.
func (c *Client) Close() {
	c.cancel()
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, stream := range c.streams {
		stream.cancel()
		delete(c.streams, id)
	}
}
