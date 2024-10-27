package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"text/template"

	"codeberg.org/mutker/bumpa/internal/config"
	"codeberg.org/mutker/bumpa/internal/errors"
	"codeberg.org/mutker/bumpa/internal/logger"
)

const (
	ProviderOpenAICompatible = "openai-compatible"
)

// ChatRequest represents a chat completion request
type ChatRequest struct {
	Model      string    `json:"model"`
	Messages   []Message `json:"messages"`
	Tools      []Tool    `json:"tools,omitempty"`
	ToolChoice string    `json:"tool_choice,omitempty"` //nolint:tagliatelle // Matching API spec
}

// Message represents a chat message
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatResponse represents a chat completion response
type ChatResponse struct {
	// Common fields between OpenAI and Ollama
	Choices []MessageChoice `json:"choices"`
}

// MessageResponse represents the assistant's response
type MessageResponse struct {
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"` //nolint:tagliatelle // Matching API spec
}

// MessageChoice represents a single choice in the response
type MessageChoice struct {
	Message MessageResponse `json:"message"`
	Index   int             `json:"index"`
}

// Tool-related structures
type ToolCall struct {
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type Parameters struct {
	Type       string              `json:"type"`
	Properties map[string]Property `json:"properties"`
	Required   []string            `json:"required"`
}

type Property struct {
	Type        string   `json:"type"           yaml:"type"`
	Description string   `json:"description"    yaml:"description"`
	Enum        []string `json:"enum,omitempty" yaml:"enum,omitempty"`
}

// Tool represents a function that can be called by the model
//
//nolint:tagliatelle // Maintaining consistent naming convention
type Tool struct {
	Type         string   `json:"type"` // Always "function"
	Function     Function `json:"function"`
	SystemPrompt string   `mapstructure:"system_prompt" yaml:"system_prompt"`
	UserPrompt   string   `mapstructure:"user_prompt"   yaml:"user_prompt"`
}

type ToolCallArguments struct {
	File                  string `json:"file"`
	Status                string `json:"status"`
	Diff                  string `json:"diff"`
	HasSignificantChanges bool   `json:"hasSignificantChanges"`
	Summary               string `json:"summary"`
}

// Function represents the function definition for a tool
type Function struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Parameters  Parameters `json:"parameters"`
}

// Client interface defines the LLM client behavior
type Client interface {
	GenerateText(ctx context.Context, systemPrompt, userPrompt string, tools []Tool) (string, error)
}

// OpenAIClient implements OpenAI-compatible API client (works for both OpenAI and Ollama)
type OpenAIClient struct {
	url    string
	token  string // Optional for Ollama
	model  string
	client *http.Client
}

// New creates a new OpenAIClient
//
//nolint:ireturn // Interface return needed for provider flexibility and testing
func New(cfg *config.LLMConfig) (Client, error) {
	logger.Debug().
		Str("provider", cfg.Provider).
		Str("base_url", cfg.BaseURL).
		Str("model", cfg.Model).
		Msg("Initializing LLM client")

	if err := validateConfig(cfg); err != nil {
		return nil, errors.WrapWithContext(errors.CodeConfigError, err, "invalid LLM configuration")
	}

	if cfg.Provider != ProviderOpenAICompatible {
		return nil, errors.WrapWithContext(
			errors.CodeConfigError,
			errors.ErrInvalidConfig,
			fmt.Sprintf("provider must be openai-compatible (got: %s)", cfg.Provider),
		)
	}

	return &OpenAIClient{
		url:   cfg.BaseURL,
		token: cfg.APIKey,
		model: cfg.Model,
		client: &http.Client{
			Timeout: cfg.RequestTimeout,
		},
	}, nil
}

func (c *OpenAIClient) GenerateText(ctx context.Context, systemPrompt, userPrompt string, tools []Tool) (string, error) {
	select {
	case <-ctx.Done():
		return "", errors.Wrap(errors.CodeTimeoutError, ctx.Err())
	default:
		messages := []Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		}

		request := ChatRequest{
			Model:      c.model,
			Messages:   messages,
			Tools:      tools,
			ToolChoice: "auto",
		}

		requestJSON, err := json.Marshal(request) //nolint:musttag // ChatRequest is properly tagged with json
		if err != nil {
			return "", errors.WrapWithContext(
				errors.CodeLLMError,
				err,
				"failed to marshal request",
			)
		}

		resp, err := c.makeRequest(ctx, requestJSON)
		if err != nil {
			return "", err // Already wrapped
		}

		content, err := extractContent(resp)
		if err != nil {
			return "", errors.WrapWithContext(
				errors.CodeLLMError,
				err,
				"failed to extract content from response",
			)
		}

		return content, nil
	}
}

func (c *OpenAIClient) makeRequest(ctx context.Context, requestJSON []byte) (*ChatResponse, error) {
	endpoint := strings.TrimSuffix(c.url, "/") + "/chat/completions"

	logger.Debug().
		Str("url", endpoint).
		RawJSON("request", requestJSON).
		Msg("Making LLM request")

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		endpoint,
		bytes.NewBuffer(requestJSON),
	)
	if err != nil {
		return nil, errors.WrapWithContext(
			errors.CodeLLMError,
			err,
			"failed to create request",
		)
	}

	// Add required headers
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" { // Only set Authorization if token is provided
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, errors.WrapWithContext(
			errors.CodeLLMError,
			err,
			"failed to make request",
		)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		logger.Debug().
			Int("status", resp.StatusCode).
			Str("body", string(body)).
			Msg("LLM request failed")

		return nil, errors.WrapWithContext(
			errors.CodeLLMError,
			errors.ErrLLMStatus,
			fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(body)),
		)
	}

	var result ChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, errors.WrapWithContext(
			errors.CodeLLMError,
			err,
			"failed to decode response",
		)
	}

	return &result, nil
}

// CallTool generates text using the LLM client with tool-specific formatting
//
//nolint:cyclop // Complex function handling tool calls and responses
func CallTool(ctx context.Context, client Client, tool *config.Tool, input interface{}) (string, error) {
	if err := validateToolConfig(tool); err != nil {
		return "", err
	}

	// Convert config.Property to llm.Property
	properties := make(map[string]Property)
	for k, v := range tool.Function.Parameters.Properties {
		properties[k] = Property{
			Type:        v.Type,
			Description: v.Description,
			Enum:        v.Enum,
		}
	}

	// Create tool definition
	toolDef := Tool{
		Type: "function",
		Function: Function{
			Name:        tool.Function.Name,
			Description: tool.Function.Description,
			Parameters: Parameters{
				Type:       tool.Function.Parameters.Type,
				Properties: properties,
				Required:   tool.Function.Parameters.Required,
			},
		},
	}

	if err := validateTool(&toolDef); err != nil {
		return "", errors.WrapWithContext(
			errors.CodeConfigError,
			err,
			"invalid tool configuration",
		)
	}

	tools := []Tool{toolDef}

	// Execute template with input data
	userPrompt, err := executeTemplate("user_prompt", tool.UserPrompt, input)
	if err != nil {
		return "", errors.Wrap(errors.CodeTemplateError, err)
	}

	// Add debug logging for request
	logger.Debug().
		Str("tool_name", tool.Name).
		Str("system_prompt", tool.SystemPrompt).
		Str("user_prompt", userPrompt).
		Interface("input", input).
		Msg("Executing tool with prompts")

	// Make the LLM call
	response, err := client.GenerateText(ctx, tool.SystemPrompt, userPrompt, tools)
	if err != nil {
		return "", err
	}

	// Try to parse as tool call response first
	if strings.HasPrefix(response, "{") && strings.HasSuffix(response, "}") {
		var toolResponse struct {
			Summary string `json:"summary"`
			Message string `json:"message"`
			Content string `json:"content"`
		}

		if err := json.Unmarshal([]byte(response), &toolResponse); err == nil {
			// Try summary first, then message, then content
			if toolResponse.Summary != "" {
				return toolResponse.Summary, nil
			}
			if toolResponse.Message != "" {
				return toolResponse.Message, nil
			}
			if toolResponse.Content != "" {
				return toolResponse.Content, nil
			}
		}

		// If we can't parse it as our expected format, log and return as-is
		logger.Debug().
			Str("tool_name", tool.Name).
			Str("response", response).
			Msg("Received JSON response but couldn't extract expected fields")
	}

	// Clean and validate the response
	response = cleanResponse(response)
	if response == "" {
		return "", errors.WrapWithContext(
			errors.CodeLLMError,
			errors.ErrInvalidInput,
			"empty response from tool",
		)
	}

	return response, nil
}

func extractContent(resp *ChatResponse) (string, error) {
	if resp == nil {
		return "", errors.Wrap(errors.CodeLLMError, errors.ErrInvalidInput)
	}

	// Check for valid response
	if len(resp.Choices) == 0 {
		return "", errors.WrapWithContext(
			errors.CodeLLMError,
			errors.ErrInvalidInput,
			"no choices in response",
		)
	}

	// Get the first choice
	choice := resp.Choices[0]

	// Check for tool calls first
	if len(choice.Message.ToolCalls) > 0 {
		toolCall := choice.Message.ToolCalls[0]
		if toolCall.Function.Arguments != "" {
			return toolCall.Function.Arguments, nil
		}
	}

	// Fall back to content
	if choice.Message.Content != "" {
		return choice.Message.Content, nil
	}

	return "", errors.WrapWithContext(
		errors.CodeLLMError,
		errors.ErrInvalidInput,
		"no content found in response",
	)
}

// Helper function to clean tool responses
func cleanResponse(response string) string {
	// Remove any markdown formatting
	response = strings.ReplaceAll(response, "`", "")
	response = strings.TrimSpace(response)

	// Remove common LLM prefixes
	prefixes := []string{
		"Here's a summary:",
		"Summary:",
		"Response:",
		"Result:",
	}
	for _, prefix := range prefixes {
		response = strings.TrimPrefix(response, prefix)
	}

	return strings.TrimSpace(response)
}

func validateConfig(cfg *config.LLMConfig) error {
	if cfg == nil {
		return errors.New("LLM configuration is required")
	}
	if cfg.Provider != ProviderOpenAICompatible {
		return errors.New("Provider must be openai-compatible")
	}
	if cfg.BaseURL == "" {
		return errors.New("BaseURL is required")
	}
	if cfg.Model == "" {
		return errors.New("Model is required")
	}
	return nil
}

// Helper function to validate tool configuration
func validateTool(tool *Tool) error {
	if tool.Type != "function" {
		return errors.New(errors.CodeConfigError)
	}
	if tool.Function.Name == "" {
		return errors.WrapWithContext(
			errors.CodeConfigError,
			errors.ErrInvalidInput,
			"missing function name",
		)
	}
	if tool.Function.Parameters.Type == "" {
		return errors.WrapWithContext(
			errors.CodeConfigError,
			errors.ErrInvalidInput,
			"missing parameters type",
		)
	}
	if len(tool.Function.Parameters.Properties) == 0 {
		return errors.WrapWithContext(
			errors.CodeConfigError,
			errors.ErrInvalidInput,
			"missing parameters properties",
		)
	}
	return nil
}

func validateToolConfig(tool *config.Tool) error {
	if tool == nil {
		return errors.WrapWithContext(
			errors.CodeConfigError,
			errors.ErrInvalidInput,
			"tool configuration is required",
		)
	}

	if strings.TrimSpace(tool.SystemPrompt) == "" {
		return errors.WrapWithContext(
			errors.CodeConfigError,
			errors.ErrInvalidInput,
			"system prompt is required",
		)
	}

	if strings.TrimSpace(tool.UserPrompt) == "" {
		return errors.WrapWithContext(
			errors.CodeConfigError,
			errors.ErrInvalidInput,
			"user prompt is required",
		)
	}

	if tool.Function.Name == "" {
		return errors.WrapWithContext(
			errors.CodeConfigError,
			errors.ErrInvalidInput,
			"function name is required",
		)
	}

	if tool.Function.Parameters.Type == "" {
		return errors.WrapWithContext(
			errors.CodeConfigError,
			errors.ErrInvalidInput,
			"parameters type is required",
		)
	}

	if len(tool.Function.Parameters.Properties) == 0 {
		return errors.WrapWithContext(
			errors.CodeConfigError,
			errors.ErrInvalidInput,
			"parameters properties are required",
		)
	}

	return nil
}

func executeTemplate(name, text string, data interface{}) (string, error) {
	tmpl, err := template.New(name).Parse(text)
	if err != nil {
		return "", err
	}

	var buf strings.Builder
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}

	return buf.String(), nil
}
