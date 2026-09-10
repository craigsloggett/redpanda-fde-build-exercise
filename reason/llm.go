package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type Chatter interface {
	Chat(ctx context.Context, msgs []Message, jsonMode bool) (Reply, error)
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Reply struct {
	Content          string
	FinishReason     string
	PromptTokens     int
	CompletionTokens int
}

type ChatClient struct {
	BaseURL string
	Model   string
	APIKey  string
	HTTP    *http.Client
}

type chatRequest struct {
	Model          string          `json:"model"`
	Messages       []Message       `json:"messages"`
	Temperature    float64         `json:"temperature"`
	MaxTokens      int             `json:"max_tokens"`
	Stream         bool            `json:"stream"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content   string `json:"content"`
			Reasoning string `json:"reasoning"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`

	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`

	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Bound the number of tokens the model uses which is large enough for a thinking model to think and still answer.
const maxCompletionTokens = 1200

func (c *ChatClient) Chat(ctx context.Context, msgs []Message, jsonMode bool) (Reply, error) {
	req := chatRequest{Model: c.Model, Messages: msgs, MaxTokens: maxCompletionTokens}
	if jsonMode {
		req.ResponseFormat = &responseFormat{Type: "json_object"}
	}

	body, err := json.Marshal(req)
	if err != nil {
		return Reply{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Reply{}, err
	}

	httpReq.Header.Set("Content-Type", "application/json")

	if c.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	res, err := c.HTTP.Do(httpReq)
	if err != nil {
		return Reply{}, fmt.Errorf("llm request: %w", err)
	}

	defer func() { _ = res.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return Reply{}, fmt.Errorf("llm response: %w", err)
	}

	if res.StatusCode/100 != 2 {
		return Reply{}, fmt.Errorf("llm status %d: %s", res.StatusCode, truncate(string(raw), 300))
	}

	var parsed chatResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return Reply{}, fmt.Errorf("llm response is not JSON: %w", err)
	}

	if parsed.Error != nil {
		return Reply{}, fmt.Errorf("llm error: %s", parsed.Error.Message)
	}

	if len(parsed.Choices) == 0 {
		return Reply{}, errors.New("llm response has no choices")
	}

	choice := parsed.Choices[0]
	content := choice.Message.Content

	if strings.TrimSpace(content) == "" {
		content = choice.Message.Reasoning
	}

	return Reply{
		Content:          content,
		FinishReason:     choice.FinishReason,
		PromptTokens:     parsed.Usage.PromptTokens,
		CompletionTokens: parsed.Usage.CompletionTokens,
	}, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}

	return s[:n] + "..."
}
