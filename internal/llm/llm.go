package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"text/template"
	"time"

	"codeberg.org/mutker/bumpa/internal/config"
	"codeberg.org/mutker/bumpa/internal/errors"
	"codeberg.org/mutker/bumpa/internal/logger"
)

// Core constants
const (
	ProviderOpenAICompatible = "openai-compatible"
)

// Core Interfaces
type Client interface {
	GenerateText(ctx context.Context, systemPrompt, userPrompt string, tools []Tool) (string, error)
}

// Primary Client Structure
type OpenAIClient struct {
	url         string
	token       string
	model       string
	client      *http.Client
	rateLimiter *RateLimiter
}

// Request/Response structures
type ChatRequest struct {
	Model      string    `json:"model"`
	Messages   []Message `json:"messages"`
	Tools      []Tool    `json:"tools,omitempty"`
	ToolChoice string    `json:"tool_choice,omitempty"` //nolint:tagliatelle // Following OpenAI API spec
}

type ChatResponse struct {
	Choices []MessageChoice `json:"choices"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type MessageResponse struct {
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"` //nolint:tagliatelle // Matching API spec
}

type MessageChoice struct {
	Message MessageResponse `json:"message"`
	Index   int             `json:"index"`
}

// Tool-related structures
type Tool struct {
	Type     string   `json:"type"`
	Function Function `json:"function"`
	//nolint:tagliatelle // Maintaining API spec naming
	SystemPrompt string `mapstructure:"system_prompt" yaml:"system_prompt"`
	//nolint:tagliatelle // Maintaining API spec naming
	UserPrompt string `mapstructure:"user_prompt" yaml:"user_prompt"`
}

type Function struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Parameters  Parameters `json:"parameters"`
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

type ToolCall struct {
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type ToolCallArguments struct {
	File                  string `json:"file"`
	Status                string `json:"status"`
	Diff                  string `json:"diff"`
	HasSignificantChanges bool   `json:"hasSignificantChanges"`
	Summary               string `json:"summary"`
}

//nolint:ireturn,nolintlint // Interface return needed for provider flexibility and testing
func New(cfg *config.LLMConfig) (Client, error) {
	logger.Debug().
		Str("provider", cfg.Provider).
		Str("base_url", cfg.BaseURL).
		Str("model", cfg.Model).
		Msg("Initializing LLM client")

	if err := validateConfig(cfg); err != nil {
		return nil, err
	}

	if cfg.Provider != ProviderOpenAICompatible {
		return nil, errors.WrapWithContext(
			errors.CodeConfigError,
			errors.ErrInvalidConfig,
			errors.FormatContext("provider must be openai-compatible (got: %s)", cfg.Provider),
		)
	}

	return &OpenAIClient{
		url:         cfg.BaseURL,
		token:       cfg.APIKey,
		model:       cfg.Model,
		client:      &http.Client{Timeout: cfg.RequestTimeout},
		rateLimiter: NewRateLimiter(),
	}, nil
}

func (c *OpenAIClient) GenerateText(ctx context.Context, systemPrompt, userPrompt string, tools []Tool) (string, error) {
	select {
	case <-ctx.Done():
		return "", errors.WrapWithContext(
			errors.CodeTimeoutError,
			ctx.Err(),
			errors.ContextLLMTimeout,
		)
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

		logger.Debug().
			Int("message_count", len(messages)).
			Int("tool_count", len(tools)).
			Str("model", c.model).
			Msg("Preparing LLM request")

		requestJSON, err := json.Marshal(&request) //nolint:musttag // ChatRequest is properly tagged with json
		if err != nil {
			return "", errors.WrapWithContext(
				errors.CodeLLMError,
				err,
				"failed to marshal request",
			)
		}

		resp, err := c.makeRequest(ctx, requestJSON)
		if err != nil {
			return "", err
		}

		content, err := extractContent(resp)
		if err != nil {
			return "", err
		}

		return content, nil
	}
}

func (c *OpenAIClient) makeRequest(ctx context.Context, requestJSON []byte) (*ChatResponse, error) {
	estimatedTokens := EstimateTokens(requestJSON)
	logger.Info().Msgf("Estimated token usage for request: %d", estimatedTokens)

	c.rateLimiter.WaitForCapacity()

	endpoint := strings.TrimSuffix(c.url, "/") + "/chat/completions"
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewBuffer(requestJSON))
		if err != nil {
			return nil, errors.WrapWithContext(
				errors.CodeLLMError,
				err,
				errors.ContextLLMRequest,
			)
		}

		req.Header.Set("Content-Type", "application/json")
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}

		resp, err := c.client.Do(req)
		if err != nil {
			return nil, errors.WrapWithContext(
				errors.CodeLLMError,
				err,
				errors.ContextLLMRequest,
			)
		}

		rateLimitInfo, err := parseRateLimitHeaders(resp.Header)
		if err != nil {
			resp.Body.Close()
			logger.Warn().Err(err).Msg("Failed to parse rate limit headers")
		} else {
			c.rateLimiter.UpdateLimits(rateLimitInfo)
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()

			// Log current status and wait time
			waitTime := defaultRetryDuration
			if rateLimitInfo.RetryAfter > 0 {
				waitTime = rateLimitInfo.RetryAfter
			}

			logger.Debug().
				Int("estimated_tokens", estimatedTokens).
				Int("remaining_tokens", rateLimitInfo.RemainingTokens).
				Float64("wait_time_seconds", waitTime.Seconds()).
				Time("reset_at", time.Now().Add(waitTime)).
				Msg("Rate limit reached, waiting before retry")

			time.Sleep(waitTime)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return nil, errors.WrapWithContext(
				errors.CodeLLMError,
				errors.ErrLLMStatus,
				fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(body)),
			)
		}

		var result ChatResponse
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			resp.Body.Close()
			return nil, errors.WrapWithContext(
				errors.CodeLLMError,
				err,
				errors.ContextLLMResponse,
			)
		}
		resp.Body.Close()
		return &result, nil
	}
}

// Tool-related functions
func CallTool(ctx context.Context, client Client, tool *config.Tool, input interface{}) (string, error) {
	startTime := time.Now()

	// Get the actual model being used
	var model string
	if openAIClient, ok := client.(*OpenAIClient); ok {
		model = openAIClient.model
	}

	logEvent := logger.Info().
		Str("model", model)

	if inputMap, ok := input.(map[string]interface{}); ok {
		if file, exists := inputMap["file"]; exists {
			if filename, ok := file.(string); ok {
				logEvent = logEvent.Str("file", filename)
			}
		}
	}
	logEvent.Msg("Calling LLM tool: " + tool.Name)

	if err := validateToolConfig(tool); err != nil {
		return "", err
	}

	properties := convertProperties(tool.Function.Parameters.Properties)
	toolDef := createToolDefinition(tool, properties)

	if err := validateTool(&toolDef); err != nil {
		return "", err
	}

	userPrompt, err := executeTemplate("user_prompt", tool.UserPrompt, input)
	if err != nil {
		return "", errors.WrapWithContext(
			errors.CodeTemplateError,
			err,
			"failed to execute user prompt template",
		)
	}

	response, err := client.GenerateText(ctx, tool.SystemPrompt, userPrompt, []Tool{toolDef})
	if err != nil {
		logger.Warn().
			Err(err).
			Str("tool", tool.Name).
			Msg("LLM call failed")
		return "", err
	}

	response = processToolResponse(response, tool.Name)
	if response == "" {
		return "", errors.WrapWithContext(
			errors.CodeLLMError,
			errors.ErrInvalidInput,
			errors.ContextLLMEmptyResponse,
		)
	}

	logger.Debug().
		Str("tool", tool.Name).
		Int("response_length", len(response)).
		Dur("duration", time.Since(startTime)).
		Msg("LLM tool execution completed")

	return response, nil
}

func processToolResponse(response, toolName string) string {
	if strings.HasPrefix(response, "{") && strings.HasSuffix(response, "}") {
		var toolResponse struct {
			Summary string `json:"summary"`
			Message string `json:"message"`
			Content string `json:"content"`
		}

		if err := json.Unmarshal([]byte(response), &toolResponse); err == nil {
			if toolResponse.Summary != "" {
				return toolResponse.Summary
			}
			if toolResponse.Message != "" {
				return toolResponse.Message
			}
			if toolResponse.Content != "" {
				return toolResponse.Content
			}
		}

		logger.Debug().
			Str("tool_name", toolName).
			Str("response", response).
			Msg("Received JSON response but couldn't extract expected fields")
	}

	return cleanResponse(response)
}

func createToolDefinition(tool *config.Tool, properties map[string]Property) Tool {
	return Tool{
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
}

// Validation functions
func validateConfig(cfg *config.LLMConfig) error {
	if cfg == nil {
		return errors.WrapWithContext(
			errors.CodeConfigError,
			errors.ErrInvalidInput,
			"LLM configuration is required",
		)
	}
	if cfg.Provider != ProviderOpenAICompatible {
		return errors.WrapWithContext(
			errors.CodeConfigError,
			errors.ErrInvalidConfig,
			"provider must be openai-compatible",
		)
	}
	if cfg.BaseURL == "" {
		return errors.WrapWithContext(
			errors.CodeConfigError,
			errors.ErrInvalidInput,
			"BaseURL is required",
		)
	}
	if cfg.Model == "" {
		return errors.WrapWithContext(
			errors.CodeConfigError,
			errors.ErrInvalidInput,
			"Model is required",
		)
	}
	return nil
}

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

// Helper functions
func convertProperties(configProps map[string]config.Property) map[string]Property {
	properties := make(map[string]Property)
	for k, v := range configProps {
		properties[k] = Property{
			Type:        v.Type,
			Description: v.Description,
			Enum:        v.Enum,
		}
	}
	return properties
}

func cleanResponse(response string) string {
	originalLength := len(response)

	response = strings.ReplaceAll(response, "`", "")
	response = strings.TrimSpace(response)

	prefixes := []string{
		"Here's a summary:",
		"Summary:",
		"Response:",
		"Result:",
	}
	for _, prefix := range prefixes {
		response = strings.TrimPrefix(response, prefix)
	}

	if len(response) != originalLength {
		logger.Debug().
			Int("original_length", originalLength).
			Int("cleaned_length", len(response)).
			Msg("Cleaned LLM response")
	}

	return strings.TrimSpace(response)
}

func extractContent(resp *ChatResponse) (string, error) {
	if resp == nil {
		return "", errors.WrapWithContext(
			errors.CodeLLMError,
			errors.ErrInvalidInput,
			errors.ContextLLMInvalidResponse,
		)
	}

	if len(resp.Choices) == 0 {
		return "", errors.WrapWithContext(
			errors.CodeLLMError,
			errors.ErrInvalidInput,
			errors.ContextLLMNoChoices,
		)
	}

	choice := resp.Choices[0]

	logger.Debug().
		Int("choice_index", choice.Index).
		Int("tool_calls_count", len(choice.Message.ToolCalls)).
		Bool("has_content", choice.Message.Content != "").
		Msg("Processing LLM response")

	if len(choice.Message.ToolCalls) > 0 {
		toolCall := choice.Message.ToolCalls[0]
		if toolCall.Function.Arguments != "" {
			return toolCall.Function.Arguments, nil
		}
	}

	if choice.Message.Content != "" {
		return choice.Message.Content, nil
	}

	return "", errors.WrapWithContext(
		errors.CodeLLMError,
		errors.ErrInvalidInput,
		errors.ContextLLMEmptyResponse,
	)
}

func EstimateTokens(requestJSON []byte) int {
	return len(requestJSON) / tokenSizeMultiplier
}

func parseRateLimitHeaders(headers http.Header) (RateLimitInfo, error) {
	info := RateLimitInfo{}
	var parseErr error

	// Helper function to parse integers from headers
	parseIntHeader := func(header string) (int, error) {
		val := headers.Get(header)
		if val == "" {
			return 0, nil
		}
		return strconv.Atoi(val)
	}

	// Helper function to parse durations from headers
	parseDurationHeader := func(header string) (time.Duration, error) {
		val := headers.Get(header)
		if val == "" {
			return 0, nil
		}
		return time.ParseDuration(val)
	}

	// Parse remaining tokens and requests
	if info.RemainingTokens, parseErr = parseIntHeader(headerRemainingTokens); parseErr != nil {
		return info, errors.WrapWithContext(
			errors.CodeLLMError,
			parseErr,
			"invalid remaining tokens header",
		)
	}

	if info.RemainingRequests, parseErr = parseIntHeader(headerRemainingRequests); parseErr != nil {
		return info, errors.WrapWithContext(
			errors.CodeLLMError,
			parseErr,
			"invalid remaining requests header",
		)
	}

	// Parse reset durations
	if info.TokensResetIn, parseErr = parseDurationHeader(headerResetTokens); parseErr != nil {
		return info, errors.WrapWithContext(
			errors.CodeLLMError,
			parseErr,
			"invalid tokens reset header",
		)
	}

	if info.RequestsResetIn, parseErr = parseDurationHeader(headerResetRequests); parseErr != nil {
		return info, errors.WrapWithContext(
			errors.CodeLLMError,
			parseErr,
			"invalid requests reset header",
		)
	}

	// Parse retry-after
	//nolint:canonicalheader // Using lowercase as per API spec
	if retryAfter := headers.Get(headerRetryAfter); retryAfter != "" {
		seconds, err := strconv.Atoi(retryAfter)
		if err != nil {
			return info, errors.WrapWithContext(
				errors.CodeLLMError,
				err,
				"invalid retry-after header",
			)
		}
		info.RetryAfter = time.Duration(seconds) * time.Second
	}

	return info, nil
}

func executeTemplate(name, text string, data interface{}) (string, error) {
	tmpl, err := template.New(name).Parse(text)
	if err != nil {
		return "", errors.WrapWithContext(
			errors.CodeTemplateError,
			err,
			"failed to parse template",
		)
	}

	var buf strings.Builder
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", errors.WrapWithContext(
			errors.CodeTemplateError,
			err,
			"failed to execute template",
		)
	}

	return buf.String(), nil
}
