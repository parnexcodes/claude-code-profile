package shim

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// UpstreamConfig holds one responses upstream.
type UpstreamConfig struct {
	Name    string `json:"name" yaml:"name"`
	BaseURL string `json:"base_url" yaml:"base-url"`
	APIKey  string `json:"api_key" yaml:"api-key"`
	Model   string `json:"model" yaml:"model"`
	Alias   string `json:"alias" yaml:"alias"`
}

// ShimConfig is the file the daemon reads.
type ShimConfig struct {
	Host      string           `json:"host" yaml:"host"`
	Port      int              `json:"port" yaml:"port"`
	Upstreams []UpstreamConfig `json:"upstreams" yaml:"upstreams"`
}

func (c *ShimConfig) addr() string {
	host := c.Host
	if host == "" {
		host = "127.0.0.1"
	}
	port := c.Port
	if port == 0 {
		port = 8318
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// Handler implements the Anthropic -> Responses translation.
type Handler struct {
	cfg *ShimConfig
	// model -> upstream lookup, built from cfg.Upstreams
	m map[string]UpstreamConfig
}

func NewHandler(cfg *ShimConfig) *Handler {
	m := make(map[string]UpstreamConfig)
	for _, u := range cfg.Upstreams {
		alias := u.Alias
		if alias == "" {
			alias = u.Model
		}
		if alias != "" {
			m[alias] = u
			m[strings.ToLower(alias)] = u
		}
		if u.Model != "" {
			m[u.Model] = u
			m[strings.ToLower(u.Model)] = u
		}
		// also allow lookup by name
		if u.Name != "" {
			m[u.Name] = u
		}
	}
	return &Handler{cfg: cfg, m: m}
}

func (h *Handler) lookup(model string) *UpstreamConfig {
	if model == "" {
		// fallback to first upstream
		if len(h.cfg.Upstreams) > 0 {
			u := h.cfg.Upstreams[0]
			return &u
		}
		return nil
	}
	if u, ok := h.m[model]; ok {
		return &u
	}
	if u, ok := h.m[strings.ToLower(model)]; ok {
		return &u
	}
	// fallback to first
	if len(h.cfg.Upstreams) > 0 {
		u := h.cfg.Upstreams[0]
		return &u
	}
	return nil
}

func extractText(content interface{}) string {
	if content == nil {
		return ""
	}
	switch v := content.(type) {
	case string:
		return v
	case []interface{}:
		var parts []string
		for _, blk := range v {
			switch b := blk.(type) {
			case map[string]interface{}:
				if t, ok := b["type"].(string); ok && t == "text" {
					if txt, ok := b["text"].(string); ok {
						parts = append(parts, txt)
					}
				} else if txt, ok := b["text"].(string); ok {
					parts = append(parts, txt)
				} else if c, ok := b["content"].(string); ok {
					parts = append(parts, c)
				} else {
					// try to marshal?
					if data, err := json.Marshal(b); err == nil {
						parts = append(parts, string(data))
					}
				}
			case string:
				parts = append(parts, b)
			default:
				parts = append(parts, fmt.Sprint(b))
			}
		}
		return strings.Join(parts, "\n")
	case map[string]interface{}:
		if txt, ok := v["text"].(string); ok {
			return txt
		}
		return fmt.Sprint(v)
	default:
		return fmt.Sprint(v)
	}
}

func anthropicToResponsesInput(body map[string]interface{}) string {
	var parts []string
	// system
	if sys, ok := body["system"]; ok {
		var sysText string
		switch v := sys.(type) {
		case string:
			sysText = v
		case []interface{}:
			var sp []string
			for _, c := range v {
				sp = append(sp, extractText(c))
			}
			sysText = strings.Join(sp, "\n")
		default:
			sysText = fmt.Sprint(v)
		}
		if strings.TrimSpace(sysText) != "" {
			parts = append(parts, fmt.Sprintf("System: %s", sysText))
		}
	}
	// messages
	if msgs, ok := body["messages"].([]interface{}); ok {
		for _, mm := range msgs {
			if m, ok := mm.(map[string]interface{}); ok {
				role, _ := m["role"].(string)
				if role == "" {
					role = "user"
				}
				text := extractText(m["content"])
				if strings.TrimSpace(text) != "" {
					roleTitle := role
					if len(roleTitle) > 0 {
						roleTitle = strings.ToUpper(roleTitle[:1]) + roleTitle[1:]
					}
					parts = append(parts, fmt.Sprintf("%s: %s", roleTitle, text))
				}
			}
		}
	}
	input := strings.Join(parts, "\n\n")
	if strings.TrimSpace(input) == "" {
		input = "hi"
	}
	return input
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// CORS headers for Claude Code
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "*")
	w.Header().Set("Access-Control-Expose-Headers", "X-Request-Id, Retry-After")
	if r.Method == http.MethodOptions {
		w.WriteHeader(200)
		return
	}
	path := r.URL.Path
	// handle /v1/models
	if r.Method == http.MethodGet && (path == "/v1/models" || path == "/models") {
		h.handleModels(w, r)
		return
	}
	if r.Method == http.MethodGet && (path == "/" || path == "/health") {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
		return
	}
	if r.Method == http.MethodPost && strings.HasPrefix(path, "/v1/messages") {
		h.handleMessages(w, r)
		return
	}
	// also handle /v1/messages?beta=true etc - path prefix already handled
	http.NotFound(w, r)
}

func (h *Handler) handleModels(w http.ResponseWriter, r *http.Request) {
	// return aliases
	var data []map[string]interface{}
	seen := map[string]bool{}
	for _, u := range h.cfg.Upstreams {
		alias := u.Alias
		if alias == "" {
			alias = u.Model
		}
		if alias == "" {
			continue
		}
		if seen[alias] {
			continue
		}
		seen[alias] = true
		data = append(data, map[string]interface{}{
			"id":       alias,
			"object":   "model",
			"created":  1787673600,
			"owned_by": u.Name,
		})
	}
	if data == nil {
		data = []map[string]interface{}{}
	}
	resp := map[string]interface{}{
		"object": "list",
		"data":   data,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (h *Handler) handleMessages(w http.ResponseWriter, r *http.Request) {
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, `{"type":"error","error":{"type":"api_error","message":"read body failed"}}`, 400)
		return
	}
	var body map[string]interface{}
	if len(bodyBytes) > 0 {
		if err := json.Unmarshal(bodyBytes, &body); err != nil {
			body = map[string]interface{}{}
		}
	} else {
		body = map[string]interface{}{}
	}
	model, _ := body["model"].(string)
	if model == "" {
		// fallback to first upstream alias/model if available
		if len(h.cfg.Upstreams) > 0 {
			model = h.cfg.Upstreams[0].Alias
			if model == "" {
				model = h.cfg.Upstreams[0].Model
			}
		}
		if model == "" {
			model = "unknown"
		}
	}
	var maxTokens int
	if v, ok := body["max_tokens"]; ok {
		switch n := v.(type) {
		case float64:
			maxTokens = int(n)
		case int:
			maxTokens = n
		case int64:
			maxTokens = int(n)
		}
	}
	if maxTokens == 0 {
		if v, ok := body["max_output_tokens"]; ok {
			switch n := v.(type) {
			case float64:
				maxTokens = int(n)
			case int:
				maxTokens = n
			}
		}
	}
	if maxTokens == 0 {
		maxTokens = 1024
	}
	// Some Responses API models require at least 1024 tokens due to reasoning
	if maxTokens < 1024 {
		maxTokens = 1024
	}
	if maxTokens > 4000 {
		maxTokens = 4000
	}
	input := anthropicToResponsesInput(body)
	up := h.lookup(model)
	if up == nil {
		http.Error(w, `{"type":"error","error":{"type":"invalid_request_error","message":"unknown provider for model `+model+`"}}`, 400)
		return
	}
	// Call upstream responses
	upstreamModel := up.Model
	if upstreamModel == "" {
		upstreamModel = model
	}
	base := strings.TrimRight(up.BaseURL, "/")
	// ensure base ends not with /responses? we expect base like https://opencode.ai/zen/go/v1
	// So endpoint is base + "/responses"
	endpoint := base + "/responses"
	if strings.HasSuffix(base, "/responses") {
		endpoint = base
	} else if strings.HasSuffix(base, "/chat/completions") {
		// shouldn't happen for responses, but handle
		endpoint = strings.TrimSuffix(base, "/chat/completions") + "/responses"
	}
	payload := map[string]interface{}{
		"model":             upstreamModel,
		"input":             input,
		"max_output_tokens": maxTokens,
	}
	payloadBytes, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", endpoint, bytes.NewReader(payloadBytes))
	if err != nil {
		http.Error(w, `{"type":"error","error":{"type":"api_error","message":"upstream request failed"}}`, 500)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+up.APIKey)
	req.Header.Set("User-Agent", "ccp-responses-shim/1.0")
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(500)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"type": "error",
			"error": map[string]interface{}{
				"type":    "api_error",
				"message": fmt.Sprintf("upstream error: %v", err),
			},
		})
		return
	}
	defer func() { _ = resp.Body.Close() }()
	respBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		w.Header().Set("Content-Type", "application/json")
		// Try to extract upstream message
		var uj map[string]interface{}
		msg := string(respBytes)
		if err := json.Unmarshal(respBytes, &uj); err == nil {
			if e, ok := uj["error"].(map[string]interface{}); ok {
				if m, ok := e["message"].(string); ok {
					msg = m
				}
			}
		}
		w.WriteHeader(resp.StatusCode)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"type": "error",
			"error": map[string]interface{}{
				"type":    "api_error",
				"message": msg,
			},
		})
		return
	}
	var upstream map[string]interface{}
	if err := json.Unmarshal(respBytes, &upstream); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(500)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"type": "error",
			"error": map[string]interface{}{
				"type":    "api_error",
				"message": "invalid upstream response",
			},
		})
		return
	}
	// parse output
	var outputText string
	var usageIn, usageOut int
	if usage, ok := upstream["usage"].(map[string]interface{}); ok {
		if v, ok := usage["input_tokens"].(float64); ok {
			usageIn = int(v)
		}
		if v, ok := usage["output_tokens"].(float64); ok {
			usageOut = int(v)
		}
	}
	if out, ok := upstream["output"].([]interface{}); ok {
		for _, item := range out {
			if m, ok := item.(map[string]interface{}); ok {
				if m["type"] == "message" && m["role"] == "assistant" {
					if content, ok := m["content"].([]interface{}); ok {
						for _, c := range content {
							if cc, ok := c.(map[string]interface{}); ok {
								if cc["type"] == "output_text" {
									if txt, ok := cc["text"].(string); ok {
										outputText += txt
									}
								}
							}
						}
					}
				}
			}
		}
	}
	if strings.TrimSpace(outputText) == "" {
		// check for incomplete
		if status, ok := upstream["status"].(string); ok && status == "incomplete" {
			// try to still return what we have, but if empty, return placeholder
			outputText = ""
		}
		if outputText == "" {
			// try fallback: check if upstream has error
			if errInfo, ok := upstream["error"]; ok && errInfo != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(500)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"type": "error",
					"error": map[string]interface{}{
						"type":    "api_error",
						"message": fmt.Sprint(errInfo),
					},
				})
				return
			}
			outputText = "(no output)"
		}
	}
	// determine if client expects stream
	accept := r.Header.Get("Accept")
	isStream := strings.Contains(accept, "text/event-stream")
	if v, ok := body["stream"]; ok {
		if b, ok := v.(bool); ok && b {
			isStream = true
		}
	}
	// Also check if query wants stream? Claude Code uses Accept: application/json normally, but we also had Python check for stream in body
	if isStream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(200)
		// SSE events
		id, _ := upstream["id"].(string)
		if id == "" {
			id = "msg_" + model
		}
		// message_start
		start := map[string]interface{}{
			"type": "message_start",
			"message": map[string]interface{}{
				"id":            id,
				"type":          "message",
				"role":          "assistant",
				"model":         model,
				"content":       []interface{}{},
				"stop_reason":   nil,
				"stop_sequence": nil,
				"usage":         map[string]interface{}{"input_tokens": usageIn, "output_tokens": 0},
			},
		}
		writeSSE(w, "message_start", start)
		writeSSE(w, "content_block_start", map[string]interface{}{"type": "content_block_start", "index": 0, "content_block": map[string]interface{}{"type": "text", "text": ""}})
		writeSSE(w, "content_block_delta", map[string]interface{}{"type": "content_block_delta", "index": 0, "delta": map[string]interface{}{"type": "text_delta", "text": outputText}})
		writeSSE(w, "content_block_stop", map[string]interface{}{"type": "content_block_stop", "index": 0})
		writeSSE(w, "message_delta", map[string]interface{}{"type": "message_delta", "delta": map[string]interface{}{"stop_reason": "end_turn", "stop_sequence": nil}, "usage": map[string]interface{}{"output_tokens": usageOut}})
		writeSSE(w, "message_stop", map[string]interface{}{"type": "message_stop"})
		return
	}
	// non-stream
	respObj := map[string]interface{}{
		"id":    upstream["id"],
		"type":  "message",
		"role":  "assistant",
		"model": model,
		"content": []interface{}{
			map[string]interface{}{"type": "text", "text": outputText},
		},
		"stop_reason":   "end_turn",
		"stop_sequence": nil,
		"usage": map[string]interface{}{
			"input_tokens":  usageIn,
			"output_tokens": usageOut,
		},
	}
	// ensure id
	if respObj["id"] == nil || respObj["id"] == "" {
		respObj["id"] = "msg_" + model
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(respObj)
}

func writeSSE(w http.ResponseWriter, event string, data interface{}) {
	b, _ := json.Marshal(data)
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, string(b))
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func RunFromConfig(path string) error {
	cfg := &ShimConfig{}
	if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
		_ = yaml.Unmarshal(data, cfg)
	}
	if cfg.Host == "" {
		cfg.Host = defaultShimHost
	}
	if cfg.Port == 0 {
		cfg.Port = defaultShimPort
	}
	h := NewHandler(cfg)
	addr := cfg.addr()
	srv := &http.Server{
		Addr:    addr,
		Handler: h,
	}
	// log to stderr for daemon log file
	fmt.Fprintf(os.Stderr, "shim listening on http://%s\n", addr)
	return srv.ListenAndServe()
}
