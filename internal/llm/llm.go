package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
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
	splitPartsExpected       = 2
)

// Core interfaces
type Client interface {
	GenerateText(ctx context.Context, systemPrompt, userPrompt string, functions []APIFunction) (string, error)
}

// Primary client structure
type OpenAIClient struct {
	url         string
	token       string
	model       string
	client      *http.Client
	rateLimiter *RateLimiter
}

// Request/Response structures
type ChatRequest struct {
	Model     string     `json:"model"`
	Messages  []Message  `json:"messages"`
	Functions []Function `json:"tools,omitempty"`
}

type ChatResponse struct {
	Choices []MessageChoice `json:"choices"`
	Error   *APIError       `json:"error,omitempty"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type MessageResponse struct {
	Content       string         `json:"content"`
	FunctionCalls []FunctionCall `json:"tool_calls,omitempty"` //nolint:tagliatelle // Following OpenAI API spec
}

type MessageChoice struct {
	Message MessageResponse `json:"message"`
	Index   int             `json:"index"`
	Role    string          `json:"role,omitempty"`
	Content string          `json:"content,omitempty"`
}

// API-related structures
type APIFunction struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Parameters  Parameters `json:"parameters"`
}

type APIError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    int    `json:"code,omitempty"`
}

type Parameters struct {
	Type       string              `json:"type"`
	Properties map[string]Property `json:"properties"`
	Required   []string            `json:"required"`
}

type Property struct {
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Enum        []string `json:"enum,omitempty"`
}

type Function struct {
	Type     string      `json:"type"`
	Function FunctionDef `json:"function"`
}

type FunctionDef struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Parameters  Parameters `json:"parameters"`
}

type FunctionCall struct {
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type FunctionChoice struct {
	Type     string `json:"type,omitempty"`
	Function *struct {
		Name string `json:"name,omitempty"`
	} `json:"function,omitempty"`
}

type FunctionCallArguments struct {
	File                  string `json:"file"`
	Status                string `json:"status"`
	Diff                  string `json:"diff"`
	HasSignificantChanges bool   `json:"hasSignificantChanges"`
	Summary               string `json:"summary"`
}

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

func (c *OpenAIClient) GenerateText(ctx context.Context, systemPrompt, userPrompt string, apiFunctions []APIFunction) (string, error) {
	if ctx == nil {
		return "", errors.WrapWithContext(
			errors.CodeConfigError,
			errors.ErrInvalidInput,
			"context cannot be nil",
		)
	}

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

		functions := make([]Function, len(apiFunctions))
		for i, fn := range apiFunctions {
			functions[i] = Function{
				Type:     "function",
				Function: apiFunctionToFunctionDef(&fn),
			}
		}

		request := ChatRequest{
			Model:     c.model,
			Messages:  messages,
			Functions: functions,
		}

		logger.Debug().
			Int("message_count", len(messages)).
			Int("function_count", len(functions)).
			Str("model", c.model).
			Msg("Preparing LLM request")

		requestJSON, err := json.Marshal(&request)
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

// Function-related functions
func CallFunction(ctx context.Context, client Client, fn *config.LLMFunction, input map[string]interface{}) (string, error) {
	startTime := time.Now()

	// Get the model being used
	var model string
	if openAIClient, ok := client.(*OpenAIClient); ok {
		model = openAIClient.model
	}

	logEvent := logger.Info().
		Str("model", model)

	if file, exists := input["file"]; exists {
		if filename, ok := file.(string); ok {
			logEvent = logEvent.Str("file", filename)
		}
	}
	logEvent.Msg("Calling LLM function: " + fn.Name)

	if err := validateFunctionConfig(fn); err != nil {
		return "", err
	}

	functionDef := createFunctionDefinition(fn)

	if err := validateFunction(&functionDef); err != nil {
		return "", err
	}

	// Debug log the input data
	logger.Debug().
		Str("function", fn.Name).
		Interface("template_vars", input).
		Msg("Template variables available")

	// Execute templates for both prompts
	systemPrompt, err := executeTemplate("system_prompt", fn.SystemPrompt, input)
	if err != nil {
		return "", errors.WrapWithContext(
			errors.CodeTemplateError,
			err,
			"failed to execute system prompt template",
		)
	}

	userPrompt, err := executeTemplate("user_prompt", fn.UserPrompt, input)
	if err != nil {
		return "", errors.WrapWithContext(
			errors.CodeTemplateError,
			err,
			"failed to execute user prompt template",
		)
	}

	// Debug log the processed prompts
	logger.Debug().
		Str("function", fn.Name).
		Str("processed_system_prompt", systemPrompt).
		Str("processed_user_prompt", userPrompt).
		Msg("Processed prompts")

	response, err := client.GenerateText(ctx, systemPrompt, userPrompt, []APIFunction{functionDef})
	if err != nil {
		logger.Warn().
			Err(err).
			Str("function", fn.Name).
			Msg("LLM call failed")
		return "", err
	}

	response = processFunctionResponse(response, fn.Name)
	if response == "" {
		return "", errors.WrapWithContext(
			errors.CodeLLMError,
			errors.ErrInvalidInput,
			errors.ContextLLMEmptyResponse,
		)
	}

	logger.Debug().
		Str("function", fn.Name).
		Int("response_length", len(response)).
		Dur("duration", time.Since(startTime)).
		Msg("LLM function execution completed")

	return response, nil
}

func createFunctionDefinition(fn *config.LLMFunction) APIFunction {
	return APIFunction{
		Name:        fn.Name,
		Description: fn.Description,
		Parameters: Parameters{
			Type:       fn.Parameters.Type,
			Properties: convertProperties(fn.Parameters.Properties),
			Required:   fn.Parameters.Required,
		},
	}
}

// APIFunction to FunctionDef conversion
func apiFunctionToFunctionDef(fn *APIFunction) FunctionDef {
	if fn == nil {
		return FunctionDef{}
	}
	return FunctionDef(*fn)
}

func processFunctionResponse(response, functionName string) string {
	if strings.HasPrefix(response, "{") && strings.HasSuffix(response, "}") {
		var functionResponse struct {
			Summary string `json:"summary"`
			Message string `json:"message"`
			Content string `json:"content"`
			File    string `json:"file"`
			Status  string `json:"status"`
			Diff    string `json:"diff"`
		}

		if err := json.Unmarshal([]byte(response), &functionResponse); err == nil {
			// Check fields in priority order
			if functionResponse.Summary != "" {
				return functionResponse.Summary
			}
			if functionResponse.Message != "" {
				return functionResponse.Message
			}
			if functionResponse.Content != "" {
				return functionResponse.Content
			}

			// If we have file info but no summary, construct a basic one
			if functionResponse.File != "" {
				return "update %s" + filepath.Base(functionResponse.File)
			}
		}

		logger.Debug().
			Str("function_name", functionName).
			Str("response", response).
			Msg("Received JSON response but couldn't extract expected fields")
	}

	return cleanResponse(response)
}

func validateFunction(fn *APIFunction) error {
	if fn == nil {
		return errors.WrapWithContext(
			errors.CodeConfigError,
			errors.ErrInvalidInput,
			"function definition is required",
		)
	}

	if fn.Name == "" {
		return errors.WrapWithContext(
			errors.CodeConfigError,
			errors.ErrInvalidInput,
			"function name is required",
		)
	}

	if fn.Parameters.Type == "" {
		return errors.WrapWithContext(
			errors.CodeConfigError,
			errors.ErrInvalidInput,
			"parameters type is required",
		)
	}

	if len(fn.Parameters.Properties) == 0 {
		return errors.WrapWithContext(
			errors.CodeConfigError,
			errors.ErrInvalidInput,
			"parameters properties are required",
		)
	}

	return nil
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

func validateFunctionConfig(fn *config.LLMFunction) error {
	if fn == nil {
		return errors.WrapWithContext(
			errors.CodeConfigError,
			errors.ErrInvalidInput,
			"function configuration is required",
		)
	}

	if strings.TrimSpace(fn.SystemPrompt) == "" {
		return errors.WrapWithContext(
			errors.CodeConfigError,
			errors.ErrInvalidInput,
			"system prompt is required",
		)
	}

	if strings.TrimSpace(fn.UserPrompt) == "" {
		return errors.WrapWithContext(
			errors.CodeConfigError,
			errors.ErrInvalidInput,
			"user prompt is required",
		)
	}

	if fn.Name == "" {
		return errors.WrapWithContext(
			errors.CodeConfigError,
			errors.ErrInvalidInput,
			"function name is required",
		)
	}

	if fn.Parameters.Type == "" {
		return errors.WrapWithContext(
			errors.CodeConfigError,
			errors.ErrInvalidInput,
			"parameters type is required",
		)
	}

	if len(fn.Parameters.Properties) == 0 {
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
	properties := make(map[string]Property, len(configProps))
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

	if resp.Error != nil {
		return "", errors.WrapWithContext(
			errors.CodeLLMError,
			errors.ErrLLMStatus,
			resp.Error.Message,
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
		Bool("has_function_calls", len(choice.Message.FunctionCalls) > 0).
		Bool("has_content", choice.Message.Content != "").
		Msg("Processing LLM response")

	// Check for function calls
	if len(choice.Message.FunctionCalls) > 0 {
		return choice.Message.FunctionCalls[0].Function.Arguments, nil
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
	// Add debug logging for template execution
	logger.Debug().
		Str("template_name", name).
		Str("template_text", text).
		Interface("template_data", data).
		Msg("Executing template")

	tmpl, err := template.New(name).Option("missingkey=error").Parse(text)
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
			fmt.Sprintf("failed to execute template: %v (data: %+v)", err, data),
		)
	}

	result := buf.String()
	logger.Debug().
		Str("template_name", name).
		Str("result", result).
		Msg("Template execution result")

	return result, nil
}
