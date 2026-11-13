package clients

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// LiteLLM is the AI client: OpenAI-style chat completions through the
// install's LiteLLM proxy (LITELLM_BASE_URL), and only through it; no model
// is ever called at a provider directly (app/clients/litellm.py).
type LiteLLM struct {
	http      *http.Client
	base      string
	apiKey    string
	model     string
	maxTokens int
	log       *slog.Logger
}

// AITimeout bounds one model call (reasoning models think for a while).
const AITimeout = 180 * time.Second

// ErrAIOff: no model is set up.
var ErrAIOff = errors.New("no AI model is set up on this install")

// AIError is the model endpoint failing or answering unusably.
type AIError struct{ Msg string }

func (e *AIError) Error() string { return e.Msg }

// AIOutOfRoom: the model stopped at max_tokens before its answer was whole
// (a reasoning model can think the whole allowance away).
type AIOutOfRoom struct{ AIError }

// NewLiteLLM makes a client for one model (LITELLM_MODEL for rows and
// digests, SEARCH_AI_MODEL for search's best guess).
func NewLiteLLM(c *http.Client, base, apiKey, model string, maxTokens int, log *slog.Logger) *LiteLLM {
	return &LiteLLM{http: c, base: strings.TrimRight(base, "/"), apiKey: apiKey, model: model, maxTokens: maxTokens, log: log}
}

// Enabled reports whether this client has a key and a model.
func (l *LiteLLM) Enabled() bool { return l.apiKey != "" && l.model != "" }

var fenceRe = regexp.MustCompile("(?s)```(?:json)?\\s*(.*?)```")

// ParseJSON reads a reply as JSON; it tolerates a ```json fence or text
// around one object.
func ParseJSON(content string) (any, error) {
	text := strings.TrimSpace(content)
	if text == "" {
		return nil, errors.New("empty reply")
	}
	if m := fenceRe.FindStringSubmatch(text); m != nil {
		text = strings.TrimSpace(m[1])
	}
	var out any
	err := json.Unmarshal([]byte(text), &out)
	if err == nil {
		return out, nil
	}
	start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if start >= 0 && end > start {
		if err2 := json.Unmarshal([]byte(text[start:end+1]), &out); err2 == nil {
			return out, nil
		}
	}
	return nil, err
}

// ChatJSON is one chat completion parsed as JSON. A reply that is not
// usable JSON is retried (attempts in all), because reasoning models now and
// then return broken JSON or spend the budget thinking.
func (l *LiteLLM) ChatJSON(ctx context.Context, system, user string, attempts int) (any, error) {
	if !l.Enabled() {
		return nil, ErrAIOff
	}
	if attempts < 1 {
		attempts = 1
	}
	var last error
	outOfRoom := false
	for attempt := 1; attempt <= attempts; attempt++ {
		body, _ := json.Marshal(JSON{
			"model":           l.model,
			"messages":        []JSON{{"role": "system", "content": system}, {"role": "user", "content": user}},
			"response_format": JSON{"type": "json_object"},
			"max_tokens":      l.maxTokens,
			// LiteLLM's response cache would hand back yesterday's rows.
			"cache": JSON{"no-cache": true},
		})
		req, _ := http.NewRequest(http.MethodPost, l.base+"/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+l.apiKey)
		resp, err := do(ctx, l.http, req, AITimeout, maxBody)
		if err != nil {
			return nil, &AIError{Msg: "the model endpoint didn't answer (" + err.Error() + ")"}
		}
		if resp.Status >= 400 {
			return nil, &AIError{Msg: fmt.Sprintf("model endpoint answered %d", resp.Status)}
		}
		var reply struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(resp.Body, &reply); err != nil {
			return nil, &AIError{Msg: "the model endpoint's answer wasn't JSON"}
		}
		content, finish := "", ""
		if len(reply.Choices) > 0 {
			content, finish = reply.Choices[0].Message.Content, reply.Choices[0].FinishReason
		}
		out, err := ParseJSON(content)
		if err == nil {
			return out, nil
		}
		last, outOfRoom = err, finish == "length"
		l.log.WarnContext(ctx, "model reply unusable", "attempt", attempt, "finish", finish, "err", err)
	}
	if outOfRoom {
		return nil, &AIOutOfRoom{AIError{Msg: fmt.Sprintf("the model ran out of room (%d output tokens) before it answered", l.maxTokens)}}
	}
	return nil, &AIError{Msg: fmt.Sprintf("model returned no usable JSON (%v)", last)}
}
