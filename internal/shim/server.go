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
	if msgs, ok := body["messages"].([]interface{}); ok {
		for _, mm := range msgs {
			if m, ok := mm.(map[string]interface{}); ok {
				role, _ := m["role"].(string)
				if role == "" {
					role = "user"
				}
				content := m["content"]
				if arr, ok := content.([]interface{}); ok {
					var textParts []string
					var toolUses []string
					var toolResults []string
					for _, blk := range arr {
						if b, ok := blk.(map[string]interface{}); ok {
							t, _ := b["type"].(string)
							switch t {
							case "text":
								if txt, ok := b["text"].(string); ok {
									textParts = append(textParts, txt)
								}
							case "tool_use":
								name, _ := b["name"].(string)
								id, _ := b["id"].(string)
								inp := b["input"]
								inpStr := ""
								if inp != nil {
									if data, err := json.Marshal(inp); err == nil {
										inpStr = string(data)
									} else {
										inpStr = fmt.Sprint(inp)
									}
								}
								toolUses = append(toolUses, fmt.Sprintf("ToolUse %s (%s): %s", name, id, inpStr))
							case "tool_result":
								toolUseID, _ := b["tool_use_id"].(string)
								c := b["content"]
								txt := extractText(c)
								if txt == "" {
									if s, ok := c.(string); ok {
										txt = s
									} else if c != nil {
										txt = fmt.Sprint(c)
									}
								}
								toolResults = append(toolResults, fmt.Sprintf("ToolResult %s: %s", toolUseID, txt))
							case "image":
								textParts = append(textParts, "[image]")
							default:
								if txt, ok := b["text"].(string); ok {
									textParts = append(textParts, txt)
								} else if c, ok := b["content"].(string); ok {
									textParts = append(textParts, c)
								} else {
									textParts = append(textParts, fmt.Sprint(b))
								}
							}
						} else if s, ok := blk.(string); ok {
							textParts = append(textParts, s)
						}
					}
					var combined []string
					if len(textParts) > 0 {
						combined = append(combined, strings.Join(textParts, "\n"))
					}
					if len(toolUses) > 0 {
						combined = append(combined, strings.Join(toolUses, "\n"))
					}
					if len(toolResults) > 0 {
						combined = append(combined, strings.Join(toolResults, "\n"))
					}
					text := strings.Join(combined, "\n")
					if strings.TrimSpace(text) != "" {
						roleTitle := strings.ToUpper(role[:1]) + role[1:]
						parts = append(parts, fmt.Sprintf("%s: %s", roleTitle, text))
					}
				} else {
					text := extractText(content)
					if strings.TrimSpace(text) != "" {
						roleTitle := strings.ToUpper(role[:1]) + role[1:]
						parts = append(parts, fmt.Sprintf("%s: %s", roleTitle, text))
					}
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
		maxTokens = 16384
	}
	if maxTokens < 1024 {
		maxTokens = 1024
	}
	if maxTokens > 16384 {
		maxTokens = 16384
	}
	input := anthropicToResponsesInput(body)
	up := h.lookup(model)
	if up == nil {
		http.Error(w, `{"type":"error","error":{"type":"invalid_request_error","message":"unknown provider for model `+model+`"}}`, 400)
		return
	}
	upstreamModel := up.Model
	if upstreamModel == "" {
		upstreamModel = model
	}
	base := strings.TrimRight(up.BaseURL, "/")
	endpoint := base + "/responses"
	if strings.HasSuffix(base, "/responses") {
		endpoint = base
	} else if strings.HasSuffix(base, "/chat/completions") {
		endpoint = strings.TrimSuffix(base, "/chat/completions") + "/responses"
	}
	var upstreamTools []map[string]interface{}
	if tools, ok := body["tools"].([]interface{}); ok {
		for _, t := range tools {
			if tm, ok := t.(map[string]interface{}); ok {
				name, _ := tm["name"].(string)
				desc, _ := tm["description"].(string)
				schema := tm["input_schema"]
				if schema == nil {
					schema = tm["parameters"]
				}
				if schema == nil {
					schema = map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
				}
				if sm, ok := schema.(map[string]interface{}); ok {
					if _, hasType := sm["type"]; !hasType {
						sm["type"] = "object"
					}
				}
				upstreamTools = append(upstreamTools, map[string]interface{}{
					"type":        "function",
					"name":        name,
					"description": desc,
					"parameters":  schema,
				})
			}
		}
	}
	payload := map[string]interface{}{
		"model":             upstreamModel,
		"input":             input,
		"max_output_tokens": maxTokens,
	}
	if len(upstreamTools) > 0 {
		payload["tools"] = upstreamTools
		payload["parallel_tool_calls"] = true
		if tc, ok := body["tool_choice"].(map[string]interface{}); ok {
			if t, ok := tc["type"].(string); ok {
				switch t {
				case "auto":
					payload["tool_choice"] = "auto"
				case "any", "required":
					payload["tool_choice"] = "required"
				case "tool":
					if name, ok := tc["name"].(string); ok {
						payload["tool_choice"] = map[string]interface{}{"type": "function", "function": map[string]interface{}{"name": name}}
					} else {
						payload["tool_choice"] = "auto"
					}
				default:
					payload["tool_choice"] = "auto"
				}
			}
		} else if tc, ok := body["tool_choice"].(string); ok {
			payload["tool_choice"] = tc
		}
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
	client := &http.Client{Timeout: 300 * time.Second}
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
	// parse output - handle both output_text and function_call
	var outputText string
	var toolUses []map[string]interface{}
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
				switch m["type"] {
				case "message":
					if m["role"] == "assistant" {
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
				case "function_call":
					name, _ := m["name"].(string)
					callID, _ := m["call_id"].(string)
					if callID == "" {
						callID, _ = m["id"].(string)
					}
					if callID == "" {
						callID = "call_" + name
					}
					argsStr, _ := m["arguments"].(string)
					var inputObj interface{}
					if argsStr != "" {
						if err := json.Unmarshal([]byte(argsStr), &inputObj); err != nil {
							inputObj = map[string]interface{}{"_raw": argsStr}
						}
					} else if args, ok := m["arguments"].(map[string]interface{}); ok {
						inputObj = args
					}
					if inputObj == nil {
						inputObj = map[string]interface{}{}
					}
					toolUses = append(toolUses, map[string]interface{}{
						"type":  "tool_use",
						"id":    callID,
						"name":  name,
						"input": inputObj,
					})
				}
			}
		}
	}
	if strings.TrimSpace(outputText) == "" && len(toolUses) == 0 {
		if status, ok := upstream["status"].(string); ok && status == "incomplete" {
			outputText = ""
		}
		if outputText == "" && len(toolUses) == 0 {
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
	stopReason := "end_turn"
	if len(toolUses) > 0 {
		stopReason = "tool_use"
	}
	accept := r.Header.Get("Accept")
	isStream := strings.Contains(accept, "text/event-stream")
	if v, ok := body["stream"]; ok {
		if b, ok := v.(bool); ok && b {
			isStream = true
		}
	}
	if isStream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(200)
		id, _ := upstream["id"].(string)
		if id == "" {
			id = "msg_" + model
		}
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
		idx := 0
		if outputText != "" {
			writeSSE(w, "content_block_start", map[string]interface{}{"type": "content_block_start", "index": idx, "content_block": map[string]interface{}{"type": "text", "text": ""}})
			writeSSE(w, "content_block_delta", map[string]interface{}{"type": "content_block_delta", "index": idx, "delta": map[string]interface{}{"type": "text_delta", "text": outputText}})
			writeSSE(w, "content_block_stop", map[string]interface{}{"type": "content_block_stop", "index": idx})
			idx++
		}
		for _, tu := range toolUses {
			writeSSE(w, "content_block_start", map[string]interface{}{"type": "content_block_start", "index": idx, "content_block": map[string]interface{}{"type": "tool_use", "id": tu["id"], "name": tu["name"], "input": map[string]interface{}{}}})
			inputBytes, _ := json.Marshal(tu["input"])
			writeSSE(w, "content_block_delta", map[string]interface{}{"type": "content_block_delta", "index": idx, "delta": map[string]interface{}{"type": "input_json_delta", "partial_json": string(inputBytes)}})
			writeSSE(w, "content_block_stop", map[string]interface{}{"type": "content_block_stop", "index": idx})
			idx++
		}
		writeSSE(w, "message_delta", map[string]interface{}{"type": "message_delta", "delta": map[string]interface{}{"stop_reason": stopReason, "stop_sequence": nil}, "usage": map[string]interface{}{"output_tokens": usageOut}})
		writeSSE(w, "message_stop", map[string]interface{}{"type": "message_stop"})
		return
	}
	var content []map[string]interface{}
	if outputText != "" {
		content = append(content, map[string]interface{}{"type": "text", "text": outputText})
	}
	content = append(content, toolUses...)
	if len(content) == 0 {
		content = append(content, map[string]interface{}{"type": "text", "text": "(no output)"})
	}
	respObj := map[string]interface{}{
		"id":            upstream["id"],
		"type":          "message",
		"role":          "assistant",
		"model":         model,
		"content":       content,
		"stop_reason":   stopReason,
		"stop_sequence": nil,
		"usage": map[string]interface{}{
			"input_tokens":  usageIn,
			"output_tokens": usageOut,
		},
	}
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
