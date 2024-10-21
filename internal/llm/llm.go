package llm

import (
	"context"
	"encoding/json"
	"fmt"

	"codeberg.org/mutker/bumpa/internal/config"
	"codeberg.org/mutker/bumpa/internal/errors"
	"codeberg.org/mutker/bumpa/internal/logger"
	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/plugins/ollama"
	"github.com/sashabaranov/go-openai"
)

type Client interface {
	GenerateText(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}

type OllamaClient struct {
	model ai.Model
}

type OpenAIClient struct {
	client *openai.Client
	model  string
}

//nolint:ireturn // Interface return needed for LLM provider abstraction and testing
func New(ctx context.Context, cfg *config.LLMConfig) (Client, error) {
	logger.Debug().Str("provider", cfg.Provider).Msg("Initializing LLM client")
	switch cfg.Provider {
	case "ollama":
		return NewOllamaClient(ctx, cfg)
	case "openai":
		return NewOpenAIClient(cfg)
	default:
		logger.Error().Str("provider", cfg.Provider).Msg("Unsupported LLM provider")
		return nil, errors.New(errors.CodeInputError)
	}
}

func NewOllamaClient(ctx context.Context, cfg *config.LLMConfig) (*OllamaClient, error) {
	if cfg.BaseURL == "" {
		return nil, errors.New(errors.CodeConfigError)
	}
	if err := ollama.Init(ctx, &ollama.Config{ServerAddress: cfg.BaseURL}); err != nil {
		return nil, errors.Wrap(errors.CodeLLMError, err)
	}
	modelName := cfg.Model
	ollama.DefineModel(ollama.ModelDefinition{
		Name: modelName,
		Type: "chat",
	}, &ai.ModelCapabilities{
		Multiturn:  true,
		SystemRole: true,
	})
	model := ollama.Model(modelName)

	return &OllamaClient{model: model}, nil
}

func NewOpenAIClient(cfg *config.LLMConfig) (*OpenAIClient, error) {
	if cfg.APIKey == "" {
		return nil, errors.New(errors.CodeConfigError)
	}
	openaiConfig := openai.DefaultConfig(cfg.APIKey)
	if cfg.BaseURL != "" {
		openaiConfig.BaseURL = cfg.BaseURL
	}

	return &OpenAIClient{
		client: openai.NewClientWithConfig(openaiConfig),
		model:  cfg.Model,
	}, nil
}

func (c *OllamaClient) GenerateText(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	result, err := ai.GenerateText(ctx, c.model,
		ai.WithSystemPrompt(systemPrompt),
		ai.WithTextPrompt(userPrompt))
	if err != nil {
		return "", errors.Wrap(errors.CodeLLMError, err)
	}

	return result, nil
}

func (c *OpenAIClient) GenerateText(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	resp, err := c.client.CreateChatCompletion(
		ctx,
		openai.ChatCompletionRequest{
			Model: c.model,
			Messages: []openai.ChatCompletionMessage{
				{
					Role:    openai.ChatMessageRoleSystem,
					Content: systemPrompt,
				},
				{
					Role:    openai.ChatMessageRoleUser,
					Content: userPrompt,
				},
			},
		},
	)
	if err != nil {
		return "", errors.Wrap(errors.CodeLLMError, err)
	}

	return resp.Choices[0].Message.Content, nil
}

func CallTool(ctx context.Context, client Client, toolName string, input interface{}) (string, error) {
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return "", errors.Wrap(errors.CodeInputError, err)
	}

	systemPrompt := fmt.Sprintf("You are an AI assistant that uses the %s tool.", toolName)
	userPrompt := fmt.Sprintf("Use the %s tool with the following input:\n%s", toolName, string(inputJSON))

	text, err := client.GenerateText(ctx, systemPrompt, userPrompt)
	if err != nil {
		return "", errors.Wrap(errors.CodeLLMError, err)
	}

	return text, nil
}
