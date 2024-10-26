package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"codeberg.org/mutker/bumpa/internal/config"
	"codeberg.org/mutker/bumpa/internal/errors"
	"codeberg.org/mutker/bumpa/internal/logger"
)

// Message represents a chat message
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest represents a chat completion request
type ChatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
}

// ChatResponse represents a chat completion response
type ChatResponse struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message,omitempty"` // Ollama format
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices,omitempty"` // OpenAI format
}

// Client interface defines the LLM client behavior
type Client interface {
	GenerateText(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}

// OpenAIClient implements the OpenAI-compatible API client
type OpenAIClient struct {
	url    string
	token  string
	model  string
	client *http.Client
}

// New creates a new OpenAIClient
func New(_ context.Context, cfg *config.LLMConfig) (Client, error) {
	if cfg.BaseURL == "" {
		return nil, errors.New(errors.CodeConfigError)
	}

	return &OpenAIClient{
		url:   cfg.BaseURL,
		token: cfg.APIKey,
		model: cfg.Model,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}, nil
}

// GenerateText generates text using the configured LLM
func (c *OpenAIClient) GenerateText(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	reqBody := ChatRequest{
		Model: c.model,
		Messages: []Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", errors.Wrap(errors.CodeLLMError, err)
	}

	// For Groq/OpenAI, use chat/completions endpoint directly
	endpoint := "chat/completions"
	if strings.Contains(c.url, "/openai/v1") {
		// URL already contains the API version
		endpoint = "chat/completions"
	} else {
		// Add v1 prefix for standard OpenAI URLs
		endpoint = "v1/chat/completions"
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		fmt.Sprintf("%s/%s", strings.TrimSuffix(c.url, "/"), endpoint),
		bytes.NewBuffer(jsonData),
	)
	if err != nil {
		return "", errors.Wrap(errors.CodeLLMError, err)
	}

	logger.Debug().
		Str("url", req.URL.String()).
		Str("method", req.Method).
		Msg("Making LLM request")

	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return "", errors.Wrap(errors.CodeLLMError, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", errors.Wrap(errors.CodeLLMError, err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", errors.WrapWithContext(
			errors.CodeLLMError,
			fmt.Errorf("status %d", resp.StatusCode),
			string(body),
		)
	}

	var result ChatResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return "", errors.Wrap(errors.CodeLLMError, err)
	}

	// Handle both Ollama and OpenAI response formats
	if result.Message.Content != "" {
		return result.Message.Content, nil
	}
	if len(result.Choices) > 0 {
		return result.Choices[0].Message.Content, nil
	}

	return "", errors.New(errors.CodeLLMError)
}

// CallTool is a helper function to call LLM tools
func CallTool(ctx context.Context, client Client, toolName string, input interface{}) (string, error) {
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return "", errors.Wrap(errors.CodeInputError, err)
	}

	systemPrompt := fmt.Sprintf("You are using the %s tool.", toolName)
	userPrompt := fmt.Sprintf("Use the %s tool with input:\n%s", toolName, string(inputJSON))

	logger.Debug().
		Str("tool", toolName).
		Str("systemPrompt", systemPrompt).
		Str("userPrompt", userPrompt).
		Msg("Calling LLM tool")

	response, err := client.GenerateText(ctx, systemPrompt, userPrompt)
	if err != nil {
		return "", errors.Wrap(errors.CodeLLMError, err)
	}

	return response, nil
}
