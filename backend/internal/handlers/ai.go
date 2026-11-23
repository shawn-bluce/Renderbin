package handlers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shawn-bluce/renderbin/backend/internal/db/sqlcgen"
)

// "Ask AI": the model a signed-in reader can chat with about a document. The
// model is one global setting, written by the super admin alone, and spoken to
// over the OpenAI-compatible chat completions API -- the one nearly every
// provider and local runner (OpenAI, DeepSeek, Qwen, Ollama, vLLM, OpenRouter,
// ...) offers, so three strings configure any of them.
//
// The three values are configs rows like every other setting. The API key is
// stored in plaintext, as users' MCP keys are; it is never sent to a browser
// except as a "set" flag and its last four characters, and only the super
// admin's session can read even that (GET /api/settings/ai).
const (
	ConfigAIBaseURL = "ai_base_url"
	ConfigAIAPIKey  = "ai_api_key"
	ConfigAIModel   = "ai_model"
)

type aiConfig struct {
	BaseURL string
	APIKey  string
	Model   string
}

// ready reports whether the model is configured. The API key is not required:
// a local runner such as Ollama accepts any key, or none.
func (c aiConfig) ready() bool {
	return c.BaseURL != "" && c.Model != ""
}

// loadAIConfig reads the model settings; a missing row reads as "".
func loadAIConfig(ctx context.Context, queries *sqlcgen.Queries) aiConfig {
	get := func(key string) string {
		v, err := queries.GetConfig(ctx, key)
		if err != nil {
			return ""
		}
		return v
	}
	return aiConfig{BaseURL: get(ConfigAIBaseURL), APIKey: get(ConfigAIAPIKey), Model: get(ConfigAIModel)}
}

// aiEnabled is what the rest of the app asks: whether to offer "Ask AI".
func aiEnabled(ctx context.Context, queries *sqlcgen.Queries) bool {
	return loadAIConfig(ctx, queries).ready()
}

// validateAIBaseURL accepts an http(s) URL with a host, or "" (unset).
func validateAIBaseURL(raw string) (string, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", "base_url must be an http(s) URL, e.g. https://api.openai.com/v1"
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", "base_url must not carry a query string or fragment"
	}
	return strings.TrimRight(raw, "/"), ""
}

// chatCompletionsURL turns the configured base URL into the endpoint. Providers
// document the base as ".../v1"; someone pasting the full endpoint gets what
// they meant too.
func chatCompletionsURL(base string) string {
	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(base, "/chat/completions") {
		return base
	}
	return base + "/chat/completions"
}

// newLLMClient is the HTTP client for the model. No overall timeout: an answer
// streams for as long as the model writes. The wait for the response to start
// is bounded instead, so a provider that accepts the connection and then says
// nothing does not hold the request forever.
func newLLMClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = 3 * time.Minute
	return &http.Client{Transport: t}
}

type llmMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// upstreamError is the model provider refusing the request (a bad key, an
// unknown model, a context too long), reported to the user as the provider
// put it -- that message is the only useful thing to show.
type upstreamError struct {
	Status int
	Msg    string
}

func (e *upstreamError) Error() string {
	if e.Msg == "" {
		return fmt.Sprintf("the AI service answered HTTP %d", e.Status)
	}
	return fmt.Sprintf("the AI service answered HTTP %d: %s", e.Status, e.Msg)
}

// llmStream reads one streamed completion.
type llmStream struct {
	body io.ReadCloser
	sc   *bufio.Scanner
	// whole is set when the provider ignored stream:true and answered with one
	// JSON completion; it is handed out as a single delta.
	whole *string
}

// openLLMStream starts a streamed completion. A non-2xx answer is an
// *upstreamError; nothing has been sent to the reader yet when it returns, so
// the caller can still answer with an ordinary HTTP error.
func openLLMStream(ctx context.Context, client *http.Client, cfg aiConfig, messages []llmMessage) (*llmStream, error) {
	body, err := json.Marshal(map[string]any{
		"model":    cfg.Model,
		"messages": messages,
		"stream":   true,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, chatCompletionsURL(cfg.BaseURL), bytes.NewReader(body))
	if err != nil {
		return nil, &upstreamError{Msg: "invalid base URL"}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		return nil, &upstreamError{Status: resp.StatusCode, Msg: providerMessage(raw)}
	}

	s := &llmStream{body: resp.Body}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		defer resp.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		if err != nil {
			return nil, err
		}
		var c struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(raw, &c); err != nil || len(c.Choices) == 0 {
			return nil, &upstreamError{Status: resp.StatusCode, Msg: providerMessage(raw)}
		}
		s.whole = &c.Choices[0].Message.Content
		return s, nil
	}
	s.sc = bufio.NewScanner(resp.Body)
	// A single SSE line holds one delta, a few bytes; the cap is for providers
	// that pad events with usage or logprobs.
	s.sc.Buffer(make([]byte, 64<<10), 4<<20)
	return s, nil
}

func (s *llmStream) Close() error { return s.body.Close() }

// next returns the next piece of the answer. done is true at the end of the
// stream, with err nil when it ended properly.
func (s *llmStream) next() (delta string, done bool, err error) {
	if s.whole != nil {
		if *s.whole == "" {
			return "", true, nil
		}
		d := *s.whole
		*s.whole = ""
		return d, false, nil
	}
	for s.sc.Scan() {
		line := s.sc.Text()
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue // blank separators, ": keep-alive" comments, event: lines
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			return "", true, nil
		}
		var ev struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue // a provider-specific event this reader does not know
		}
		if ev.Error != nil {
			return "", true, &upstreamError{Status: http.StatusOK, Msg: ev.Error.Message}
		}
		if len(ev.Choices) > 0 && ev.Choices[0].Delta.Content != "" {
			return ev.Choices[0].Delta.Content, false, nil
		}
	}
	if err := s.sc.Err(); err != nil {
		return "", true, err
	}
	// The body ended without [DONE]. Some providers do that; what arrived is
	// the answer.
	return "", true, nil
}

// providerMessage extracts the human-readable part of an error body: the
// OpenAI shape {"error":{"message":...}}, a bare {"message":...}, or the text
// itself, shortened.
func providerMessage(raw []byte) string {
	var body struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	if json.Unmarshal(raw, &body) == nil {
		var nested struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(body.Error, &nested) == nil && nested.Message != "" {
			return shorten(nested.Message, 300)
		}
		var flat string
		if json.Unmarshal(body.Error, &flat) == nil && flat != "" {
			return shorten(flat, 300)
		}
		if body.Message != "" {
			return shorten(body.Message, 300)
		}
	}
	return shorten(strings.TrimSpace(string(raw)), 300)
}

// shorten cuts s to at most n runes, marking the cut.
func shorten(s string, n int) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "")
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// isUpstream reports whether err is the provider's refusal rather than ours.
func isUpstream(err error) (*upstreamError, bool) {
	var ue *upstreamError
	ok := errors.As(err, &ue)
	return ue, ok
}

type aiSettingsResponse struct {
	BaseURL string `json:"base_url"`
	Model   string `json:"model"`
	// The key itself never leaves the server: the form shows whether one is
	// set and how it ends, enough to tell which key it is.
	APIKeySet  bool   `json:"api_key_set"`
	APIKeyHint string `json:"api_key_hint"`
}

func toAISettings(cfg aiConfig) aiSettingsResponse {
	resp := aiSettingsResponse{BaseURL: cfg.BaseURL, Model: cfg.Model, APIKeySet: cfg.APIKey != ""}
	if k := cfg.APIKey; len(k) >= 12 {
		resp.APIKeyHint = k[len(k)-4:]
	}
	return resp
}

// GetAI answers GET /api/settings/ai with the model settings; super admin only.
func (h *SettingsHandler) GetAI(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireSuperAdmin(w, r); !ok {
		return
	}
	writeJSON(w, toAISettings(loadAIConfig(r.Context(), h.queries)))
}

type updateAISettingsRequest struct {
	BaseURL *string `json:"base_url"`
	Model   *string `json:"model"`
	// APIKey absent keeps the stored key, so saving the form does not require
	// re-entering it; "" clears it.
	APIKey *string `json:"api_key"`
}

// maxAISettingBytes bounds each of the three values.
const maxAISettingBytes = 2048

// UpdateAI answers PUT /api/settings/ai; super admin only. Each field is
// optional. Clearing the base URL or the model turns Ask AI off.
func (h *SettingsHandler) UpdateAI(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireSuperAdmin(w, r); !ok {
		return
	}
	var req updateAISettingsRequest
	if !decodeSmallJSON(w, r, &req) {
		return
	}
	updates := map[string]string{}
	if req.BaseURL != nil {
		base, msg := validateAIBaseURL(*req.BaseURL)
		if msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		updates[ConfigAIBaseURL] = base
	}
	if req.Model != nil {
		updates[ConfigAIModel] = strings.TrimSpace(*req.Model)
	}
	if req.APIKey != nil {
		updates[ConfigAIAPIKey] = strings.TrimSpace(*req.APIKey)
	}
	for key, value := range updates {
		if len(value) > maxAISettingBytes || strings.ContainsAny(value, "\r\n") {
			http.Error(w, key+" is too long or contains a line break", http.StatusBadRequest)
			return
		}
	}
	for key, value := range updates {
		if err := h.queries.SetConfig(r.Context(), sqlcgen.SetConfigParams{Key: key, Value: value}); err != nil {
			serverError(w, h.logger, "set AI config", err)
			return
		}
	}
	writeJSON(w, toAISettings(loadAIConfig(r.Context(), h.queries)))
}
