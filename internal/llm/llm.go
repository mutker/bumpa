package llm

import (
	"context"
	"encoding/json"
	"fmt"

	"codeberg.org/mutker/bumpa/internal/config"
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

func New(ctx context.Context, cfg config.LLMConfig) (Client, error) {
	switch cfg.Provider {
	case "ollama":
		return NewOllamaClient(ctx, cfg)
	case "openai":
		return NewOpenAIClient(cfg)
	default:
		return nil, fmt.Errorf("unsupported LLM provider: %s", cfg.Provider)
	}
}

func NewOllamaClient(ctx context.Context, cfg config.LLMConfig) (*OllamaClient, error) {
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("ollama base URL is not set")
	}
	if err := ollama.Init(ctx, &ollama.Config{ServerAddress: cfg.BaseURL}); err != nil {
		return nil, fmt.Errorf("failed to initialize Ollama: %w", err)
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

func NewOpenAIClient(cfg config.LLMConfig) (*OpenAIClient, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("OpenAI API key is not set")
	}
	config := openai.DefaultConfig(cfg.APIKey)
	if cfg.BaseURL != "" {
		config.BaseURL = cfg.BaseURL
	}
	return &OpenAIClient{
		client: openai.NewClientWithConfig(config),
		model:  cfg.Model,
	}, nil
}

func (c *OllamaClient) GenerateText(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	result, err := ai.GenerateText(ctx, c.model,
		ai.WithSystemPrompt(systemPrompt),
		ai.WithTextPrompt(userPrompt))
	if err != nil {
		return "", fmt.Errorf("failed request to Ollama: %w", err)
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
		return "", fmt.Errorf("failed to call OpenAI API: %w", err)
	}
	return resp.Choices[0].Message.Content, nil
}

func CallTool(ctx context.Context, client Client, toolName string, input interface{}) (string, error) {
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("failed to marshal tool input: %w", err)
	}

	systemPrompt := fmt.Sprintf("You are an AI assistant that uses the %s tool.", toolName)
	userPrompt := fmt.Sprintf("Use the %s tool with the following input:\n%s", toolName, string(inputJSON))

	return client.GenerateText(ctx, systemPrompt, userPrompt)
}
