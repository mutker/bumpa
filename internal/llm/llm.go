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
	"time"

	"codeberg.org/mutker/bumpa/internal/config"
	"codeberg.org/mutker/bumpa/internal/errors"
	"codeberg.org/mutker/bumpa/internal/logger"
)

// ChatRequest represents a chat completion request
type ChatRequest struct {
	Model      string    `json:"model"`
	Messages   []Message `json:"messages"`
	Tools      []Tool    `json:"tools,omitempty"`
	ToolChoice string    `json:"tool_choice,omitempty"`
}

// Message represents a chat message
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatResponse represents a chat completion response
type ChatResponse struct {
	Message MessageResponse `json:"message,omitempty"` // Ollama format
	Choices []MessageChoice `json:"choices,omitempty"` // OpenAI format
}

type MessageResponse struct {
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

type MessageChoice struct {
	Message MessageResponse `json:"message"`
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
	Type        string   `json:"type" yaml:"type"`
	Description string   `json:"description" yaml:"description"`
	Enum        []string `json:"enum,omitempty" yaml:"enum,omitempty"`
}

type Tool struct {
	Type     string   `json:"type"`
	Function Function `json:"function"`
}

type ToolCallArguments struct {
	File                  string `json:"file"`
	Status                string `json:"status"`
	Diff                  string `json:"diff"`
	HasSignificantChanges bool   `json:"hasSignificantChanges"`
	Summary               string `json:"summary"`
}

type Function struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Parameters  Parameters `json:"parameters"`
}

// Client interface defines the LLM client behavior
type Client interface {
	GenerateText(ctx context.Context, systemPrompt, userPrompt string, tools []Tool) (string, error)
}

// OpenAIClient implements the OpenAI-compatible API client
type OpenAIClient struct {
	url    string
	token  string
	model  string
	client *http.Client
}

// New creates a new OpenAIClient
func New(ctx context.Context, cfg *config.LLMConfig) (Client, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, errors.WrapWithContext(errors.CodeConfigError, err, "invalid LLM configuration")
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

func (c *OpenAIClient) GenerateText(ctx context.Context, systemPrompt, userPrompt string, tools []Tool) (string, error) {
	messages := []Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}

	reqBody := ChatRequest{
		Model:      c.model,
		Messages:   messages,
		Tools:      tools,
		ToolChoice: "auto",
	}

	// Enhanced debug logging
	event := logger.DebugWithComponent("llm").
		Str("model", c.model).
		Str("url", c.url).
		Int("message_count", len(messages)).
		Int("tools_count", len(tools))

	// Marshal request body
	requestJSON, err := json.Marshal(reqBody)
	if err != nil {
		return "", errors.Wrap(errors.CodeLLMError, err)
	}

	if event.Enabled() {
		event.RawJSON("request", requestJSON)
	}
	event.Msg("Making LLM request")

	endpoint := "chat/completions"
	if !strings.Contains(c.url, "/openai/v1") {
		endpoint = "v1/chat/completions"
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		fmt.Sprintf("%s/%s", strings.TrimSuffix(c.url, "/"), endpoint),
		bytes.NewBuffer(requestJSON),
	)
	if err != nil {
		return "", errors.Wrap(errors.CodeLLMError, err)
	}

	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return "", errors.Wrap(errors.CodeLLMError, err)
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", errors.Wrap(errors.CodeLLMError, err)
	}

	if resp.StatusCode != http.StatusOK {
		logger.Error().
			Str("status", resp.Status).
			RawJSON("response", responseBody).
			Msg("LLM request failed")
		return "", errors.WrapWithContext(
			errors.CodeLLMError,
			fmt.Errorf("status %d", resp.StatusCode),
			string(responseBody),
		)
	}

	// Log successful response in debug
	logger.Debug().
		Str("status", resp.Status).
		RawJSON("response", responseBody).
		Msg("LLM response received")

	var result ChatResponse
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return "", errors.Wrap(errors.CodeLLMError, err)
	}

	// Handle both direct content and tool calls
	content := ""

	// Try to get content from tool calls first
	if toolCalls := getToolCalls(result); len(toolCalls) > 0 {
		// Use the first tool call's response
		if len(toolCalls) > 0 && toolCalls[0].Function.Arguments != "" {
			return toolCalls[0].Function.Arguments, nil
		}
	}

	// Fall back to direct content
	if result.Message.Content != "" {
		content = result.Message.Content
	} else if len(result.Choices) > 0 && result.Choices[0].Message.Content != "" {
		content = result.Choices[0].Message.Content
	}

	if content == "" {
		logger.Error().
			RawJSON("response", responseBody).
			Msg("Empty response from LLM")
		return "", errors.WrapWithContext(
			errors.CodeLLMError,
			errors.ErrInvalidInput,
			"empty response from LLM",
		)
	}

	return strings.TrimSpace(content), nil
}

// CallTool generates text using the LLM client with tool-specific formatting
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

// Helper function to extract tool calls from response
func getToolCalls(response ChatResponse) []ToolCall {
	if len(response.Choices) > 0 && len(response.Choices[0].Message.ToolCalls) > 0 {
		return response.Choices[0].Message.ToolCalls
	}
	if len(response.Message.ToolCalls) > 0 {
		return response.Message.ToolCalls
	}
	return nil
}

// Helper function to parse tool call arguments
func parseToolCallArguments(arguments string) (*ToolCallArguments, error) {
	var args ToolCallArguments
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return nil, errors.Wrap(errors.CodeLLMError, err)
	}
	return &args, nil
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

func validateConfig(cfg *config.LLMConfig) error {
	if cfg == nil {
		return errors.New("LLM configuration is required")
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

func validateAnalyzeFileChangesResponse(response string) error {
	if response == "" {
		return errors.WrapWithContext(
			errors.CodeLLMError,
			errors.ErrInvalidInput,
			"empty file analysis response",
		)
	}

	// Check if it's a JSON response
	if strings.HasPrefix(response, "{") && strings.HasSuffix(response, "}") {
		var result struct {
			Summary string `json:"summary"`
		}
		if err := json.Unmarshal([]byte(response), &result); err != nil {
			return errors.WrapWithContext(
				errors.CodeLLMError,
				err,
				"invalid JSON response format",
			)
		}
		if result.Summary == "" {
			return errors.WrapWithContext(
				errors.CodeLLMError,
				errors.ErrInvalidInput,
				"missing summary in JSON response",
			)
		}
		return nil
	}

	// For non-JSON responses, ensure it's a reasonable length
	if len(response) < 10 || len(response) > 500 {
		return errors.WrapWithContext(
			errors.CodeLLMError,
			errors.ErrInvalidInput,
			fmt.Sprintf("unexpected response length: %d", len(response)),
		)
	}

	return nil
}

// Add helper for consistent property name casing
func normalizePropertyNames(properties map[string]Property) map[string]Property {
	normalized := make(map[string]Property)
	for key, value := range properties {
		// Maintain original casing for JSON
		normalized[key] = value
	}
	return normalized
}

// Helper function for logging with context awareness
func logLLMRequest(ctx context.Context, req *ChatRequest) {
	event := logger.DebugWithComponent("llm").
		Str("model", req.Model).
		Int("message_count", len(req.Messages)).
		Int("tools_count", len(req.Tools)).
		Str("tool_choice", req.ToolChoice)

	// Add trace context if available
	if traceID := ctx.Value("trace_id"); traceID != nil {
		event.Interface("trace_id", traceID)
	}
	if requestID := ctx.Value("request_id"); requestID != nil {
		event.Interface("request_id", requestID)
	}

	// Add operation timing context if available
	if deadline, ok := ctx.Deadline(); ok {
		event.Time("deadline", deadline)
	}

	// Add request body only if debug is enabled
	if event.Enabled() {
		if reqJSON, err := json.Marshal(req); err == nil {
			event.RawJSON("request", reqJSON)
		}
	}

	event.Msg("LLM request")
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
