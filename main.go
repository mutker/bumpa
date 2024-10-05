package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/plugins/ollama"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/sashabaranov/go-openai"
	"github.com/spf13/viper"
)

const (
	TimeFormatRFC3339 TimeFormatType = "RFC3339"
	TimeFormatUnix    TimeFormatType = "TimeFormatUnix"
)

type TimeFormatType string

type Config struct {
	Logging struct {
		Environment string
		TimeFormat  string
		Output      string
		Level       string
		Path        string `mapstructure:"file_path"`
	}

	Git struct {
		IncludeGitignore bool     `mapstructure:"include_gitignore"`
		Ignore           []string `mapstructure:"ignore"`
	}

	LLM struct {
		Provider string
		Model    string
		BaseURL  string `mapstructure:"base_url"`
		APIKey   string `mapstructure:"api_key"`
	}

	Prompts struct {
		DiffSummary struct {
			System, User string
		}
		CommitMessage struct {
			System, User string
		}
		FileSummary struct {
			System, User string
		}
	}
	Templates struct {
		CommitMessage string
	}
	Commit CommitConfig
}

type CommitConfig struct {
	IncludeGitignore bool
	Ignore           []string
	Templates        struct {
		CommitMessage string
	}
	Prompts struct {
		DiffSummary   PromptPair
		CommitMessage PromptPair
		FileSummary   PromptPair
	}
}

type PromptPair struct {
	System, User string
}

type CommitGenerator struct {
	cfg    *Config
	llm    LLMClient
	logger zerolog.Logger
	repo   *git.Repository
}

type LLMClient interface {
	GenerateText(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}

type OllamaClient struct {
	model ai.Model
}

type OpenAIClient struct {
	client *openai.Client
	model  string
}

func init() {
	if err := initLogger(); err != nil {
		fmt.Printf("Failed to initialize logger: %v\n", err)
		os.Exit(1)
	}
}

func main() {
	ctx := context.Background()

	cfg, err := loadConfig()
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load configuration")
	}

	llm, err := initLLM(ctx, cfg)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to initialize LLM")
	}

	// Open the existing repository
	repo, err := git.PlainOpen(".")
	if err != nil {
		log.Fatal().Err(err).Msg("failed to open git repository")
	}

	commitGen := NewCommitGenerator(cfg, llm, log.Logger, repo)

	message, err := commitGen.generate()
	if err != nil {
		if err == ErrNoChanges {
			log.Info().Msg("no changes to commit")
			return
		}
		log.Fatal().Err(err).Msg("failed to generate commit message")
	}

	filesToCommit, err := getFilesToCommit(repo)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to get files to commit")
	}

	if len(filesToCommit) == 0 {
		log.Info().Msg("no changes to commit")
		return
	}

	fileList := strings.Builder{}
	fileList.WriteString("Files:\n")
	for _, file := range filesToCommit {
		fileList.WriteString(fmt.Sprintf("  %s\n", file))
	}

	promptMessage := fmt.Sprintf("%s\nDo you want to commit the following files with the message: %q?", fileList.String(), message)

	if !promptYesNo(promptMessage) {
		log.Info().Msg("commit aborted")
		return
	}

	if err := makeCommit(repo, message, filesToCommit); err != nil {
		log.Fatal().Err(err).Msg("failed to commit")
	}

	log.Info().Msg("commit successfully created")
}

func loadConfig() (*Config, error) {
	viper.SetConfigName(".bumpa")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")
	viper.AutomaticEnv()

	viper.SetDefault("llm.provider", "ollama")
	viper.SetDefault("llm.model", "llama3.1:8b-instruct-q8_0")
	viper.SetDefault("llm.base_url", "http://localhost:11434")
	viper.SetDefault("llm.api_key", "")
	viper.SetDefault("logging.environment", "development")
	viper.SetDefault("logging.timeformat", time.RFC3339)
	viper.SetDefault("logging.output", "console")
	viper.SetDefault("logging.level", "info")

	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("error reading config file: %w", err)
		}
		log.Warn().Msg("No configuration file found, using defaults and environment variables")
	}

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unable to decode into config struct: %w", err)
	}

	return &cfg, nil
}

func initLogger() error {
	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	level, err := zerolog.ParseLevel(cfg.Logging.Level)
	if err != nil {
		level = zerolog.InfoLevel
	}

	timeFormat := getTimeFormat(cfg.Logging.TimeFormat)

	var output io.Writer
	switch cfg.Logging.Output {
	case "file":
		if cfg.Logging.Path == "" {
			return fmt.Errorf("log file path is not set")
		}
		file, err := os.OpenFile(cfg.Logging.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o666)
		if err != nil {
			return fmt.Errorf("failed to open log file: %w", err)
		}
		output = file
	case "journald":
		// ... journald setup (if you're keeping this option)
	default:
		output = os.Stdout
	}

	logger := zerolog.New(output).Level(level).With().Timestamp().Logger()

	if cfg.Logging.Environment != "production" {
		logger = logger.Output(zerolog.ConsoleWriter{
			Out:        output,
			TimeFormat: timeFormat,
			NoColor:    false,
		})
	}

	log.Logger = logger

	return nil
}

func initLLM(ctx context.Context, cfg *Config) (LLMClient, error) {
	switch cfg.LLM.Provider {
	case "ollama":
		return initOllamaClient(ctx, cfg)
	case "openai":
		return initOpenAIClient(cfg)
	default:
		return nil, fmt.Errorf("unsupported LLM provider: %s", cfg.LLM.Provider)
	}
}

func initOllamaClient(ctx context.Context, cfg *Config) (*OllamaClient, error) {
	if cfg.LLM.BaseURL == "" {
		return nil, fmt.Errorf("ollama base URL is not set")
	}
	if err := ollama.Init(ctx, &ollama.Config{ServerAddress: cfg.LLM.BaseURL}); err != nil {
		return nil, fmt.Errorf("failed to initialize Ollama: %w", err)
	}
	modelName := cfg.LLM.Model
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

func initOpenAIClient(cfg *Config) (*OpenAIClient, error) {
	if cfg.LLM.APIKey == "" {
		return nil, fmt.Errorf("OpenAI API key is not set")
	}
	config := openai.DefaultConfig(cfg.LLM.APIKey)
	if cfg.LLM.BaseURL != "" {
		config.BaseURL = cfg.LLM.BaseURL
	}
	return &OpenAIClient{
		client: openai.NewClientWithConfig(config),
		model:  cfg.LLM.Model,
	}, nil
}

func (c *OllamaClient) GenerateText(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	if c.model == nil {
		return "", fmt.Errorf("failed to initialize Ollama model")
	}
	log.Debug().Msg("Sending request to Ollama")
	result, err := ai.GenerateText(ctx, c.model,
		ai.WithSystemPrompt(systemPrompt),
		ai.WithTextPrompt(userPrompt))
	if err != nil {
		return "", fmt.Errorf("failed request to Ollama: %w", err)
	}
	log.Debug().Str("result", result).Msg("Received response from Ollama")
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
		return "", fmt.Errorf("failed to generate text: %w", err)
	}
	return resp.Choices[0].Message.Content, nil
}

func NewCommitGenerator(cfg *Config, llm LLMClient, logger zerolog.Logger, repo *git.Repository) *CommitGenerator {
	return &CommitGenerator{cfg: cfg, llm: llm, logger: logger, repo: repo}
}

var ErrNoChanges = fmt.Errorf("no changes to commit")

func (cg *CommitGenerator) generate() (string, error) {
	fileSummaries, err := cg.getFileSummaries()
	if err != nil {
		return "", fmt.Errorf("failed to get file summaries: %w", err)
	}

	if len(fileSummaries) == 0 {
		return "", ErrNoChanges
	}

	diffSummary := cg.generateDiffSummary(fileSummaries)
	log.Debug().Str("diffSummary", diffSummary).Msg("Generated diff summary")

	commitMessage, err := cg.generateCommitMessageFromSummary(diffSummary)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to generate valid commit message")
		return "", err // This line is technically unnecessary due to log.Fatal, but included for clarity
	}

	log.Info().Str("message", commitMessage).Msg("Generated commit message")
	return commitMessage, nil
}

func (cg *CommitGenerator) shouldIgnoreFile(path string) bool {
	// Check against the ignore list in the config
	for _, pattern := range cg.cfg.Git.Ignore {
		matched, err := filepath.Match(pattern, path)
		if err == nil && matched {
			return true
		}
	}

	// FIXME: We're not checking against .gitignore in this in-memory approach
	return false
}

func (cg *CommitGenerator) generateDiffSummary(fileSummaries map[string]string) string {
	var summaryBuilder strings.Builder
	summaryBuilder.WriteString("Changes in multiple files:\n\n")
	for file, summary := range fileSummaries {
		summaryBuilder.WriteString(fmt.Sprintf("- %s: %s\n", file, summary))
	}
	return summaryBuilder.String()
}

func (cg *CommitGenerator) getFileStatus(status *git.FileStatus) string {
	switch {
	case status.Staging == git.Added:
		return "Added"
	case status.Staging == git.Modified:
		return "Modified"
	case status.Staging == git.Deleted:
		return "Deleted"
	case status.Worktree == git.Untracked:
		return "Untracked"
	default:
		return "Changed"
	}
}

func (cg *CommitGenerator) getFileDiff(path string) (string, error) {
	w, err := cg.repo.Worktree()
	if err != nil {
		return "", err
	}

	status, err := w.Status()
	if err != nil {
		return "", err
	}

	fileStatus := status.File(path)
	if fileStatus.Staging == git.Untracked {
		return "[New File]", nil
	}

	if fileStatus.Staging == git.Deleted {
		return "[Deleted File]", nil
	}

	currentContent, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	head, err := cg.repo.Head()
	if err != nil {
		return "", err
	}

	commit, err := cg.repo.CommitObject(head.Hash())
	if err != nil {
		return "", err
	}

	file, err := commit.File(path)
	if err != nil {
		if err == object.ErrFileNotFound {
			return "[New File]", nil
		}
		return "", err
	}

	oldContent, err := file.Contents()
	if err != nil {
		return "", err
	}

	diff := cg.generateSimpleDiff(oldContent, string(currentContent))
	return cg.summarizeDiff(diff), nil
}

func (cg *CommitGenerator) generateSimpleDiff(old, new string) string {
	oldLines := strings.Split(old, "\n")
	newLines := strings.Split(new, "\n")

	var diff strings.Builder
	for i := 0; i < len(oldLines) || i < len(newLines); i++ {
		if i < len(oldLines) && i < len(newLines) && oldLines[i] == newLines[i] {
			continue
		}
		if i < len(oldLines) {
			diff.WriteString("- " + oldLines[i] + "\n")
		}
		if i < len(newLines) {
			diff.WriteString("+ " + newLines[i] + "\n")
		}
	}
	return diff.String()
}

func (cg *CommitGenerator) summarizeDiff(diff string) string {
	lines := strings.Split(diff, "\n")
	if len(lines) > 10 {
		return strings.Join(lines[:10], "\n") + "\n..."
	}
	return diff
}

func (cg *CommitGenerator) generateFileSummary(path string, status *git.FileStatus) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	diff, err := cg.getFileDiff(path)
	if err != nil {
		return "", fmt.Errorf("failed to get diff for %s: %w", path, err)
	}

	prompt := cg.cfg.Commit.Prompts.FileSummary
	fileSummaryPrompt := fmt.Sprintf(prompt.User, path, cg.getFileStatus(status), diff)

	summary, err := cg.llm.GenerateText(ctx, prompt.System, fileSummaryPrompt)
	if err != nil {
		return "", fmt.Errorf("failed to generate file summary: %w", err)
	}

	log.Debug().Msg(summary)

	return fmt.Sprintf("%s: %s", path, summary), nil
}

func (cg *CommitGenerator) getFileSummaries() (map[string]string, error) {
	w, err := cg.repo.Worktree()
	if err != nil {
		return nil, err
	}

	status, err := w.Status()
	if err != nil {
		return nil, err
	}

	fileSummaries := make(map[string]string)
	for path, fileStatus := range status {
		if cg.shouldIgnoreFile(path) {
			log.Debug().Str("path", path).Msg("Ignoring file")
			continue
		}

		summary, err := cg.generateFileSummary(path, fileStatus)
		if err != nil {
			return nil, fmt.Errorf("failed to generate summary for file %s: %w", path, err)
		}
		fileSummaries[path] = summary
	}

	log.Debug().Int("count", len(fileSummaries)).Msg("Generated file summaries")
	return fileSummaries, nil
}

func (cg *CommitGenerator) generateCommitMessageFromSummary(summary string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	systemPrompt := cg.cfg.Prompts.CommitMessage.System
	userPrompt := fmt.Sprintf(cg.cfg.Prompts.CommitMessage.User, summary)

	for attempts := 0; attempts < 3; attempts++ {
		log.Debug().Str("systemPrompt", systemPrompt).Str("userPrompt", userPrompt).Msg("Sending prompts to LLM")

		message, err := cg.llm.GenerateText(ctx, systemPrompt, userPrompt)
		if err != nil {
			return "", fmt.Errorf("failed to generate commit message: %w", err)
		}

		log.Debug().Str("generatedMessage", message).Msg("Received message from LLM")

		if isValidCommitMessage(message) {
			return message, nil
		}

		// If invalid, try to extract a valid message
		extractedMessage := extractConventionalCommit(message)
		if extractedMessage != "" {
			return extractedMessage, nil
		}

		// If extraction fails, update the prompt and try again
		userPrompt = fmt.Sprintf("The previous response was invalid. Please generate a commit message in the Conventional Commits format. It should start with a type (feat, fix, etc.) followed by a colon and a short description. Here's the summary again:\n\n%s", summary)
	}

	return "", fmt.Errorf("failed to generate a valid commit message after multiple attempts")
}

func (cg *CommitGenerator) processCommitMessage(message string) string {
	message = strings.TrimSpace(message)
	lines := strings.Split(message, "\n")

	// Extract the first line that matches the Conventional Commit format
	var header string
	for _, line := range lines {
		if isValidCommitMessage(line) {
			header = line
			break
		}
	}

	if header == "" {
		log.Error().Str("invalidMessage", message).Msg("Generated commit message is not in the expected format")
		return ""
	}

	if len(header) > 100 {
		log.Warn().Str("header", header).Msg("Commit message header exceeds 100 characters")
		header = header[:100]
	}

	// Construct the body from the remaining lines
	var body strings.Builder
	for _, line := range lines[1:] {
		if len(line) > 100 {
			line = line[:100]
		}
		body.WriteString(line)
		body.WriteString("\n")
	}

	if body.Len() > 0 {
		return fmt.Sprintf("%s\n\n%s", header, strings.TrimSpace(body.String()))
	}

	return header
}

func isValidCommitType(message string) bool {
	validTypes := []string{"feat", "fix", "docs", "style", "refactor", "perf", "test", "chore", "ci", "build", "revert"}
	for _, validType := range validTypes {
		if strings.HasPrefix(strings.ToLower(message), validType+":") || strings.HasPrefix(strings.ToLower(message), validType+"(") {
			return true
		}
	}
	return false
}

func isValidCommitMessage(message string) bool {
	// Implement a stricter check for Conventional Commits format
	pattern := `^(feat|fix|docs|style|refactor|perf|test|chore|ci|build|revert)(\([a-z]+\))?: [a-z].+`
	matched, _ := regexp.MatchString(pattern, strings.Split(message, "\n")[0])
	return matched
}

func extractConventionalCommit(message string) string {
	lines := strings.Split(message, "\n")
	for _, line := range lines {
		if isValidCommitMessage(line) {
			return line
		}
	}
	return ""
}

func getFilesToCommit(repo *git.Repository) ([]string, error) {
	w, err := repo.Worktree()
	if err != nil {
		return nil, err
	}

	status, err := w.Status()
	if err != nil {
		return nil, err
	}

	var files []string
	for file, fileStatus := range status {
		if fileStatus.Staging != git.Unmodified || fileStatus.Worktree != git.Unmodified {
			files = append(files, file)
		}
	}

	sort.Strings(files)
	return files, nil
}

func makeCommit(repo *git.Repository, message string, filesToAdd []string) error {
	w, err := repo.Worktree()
	if err != nil {
		return err
	}

	for _, file := range filesToAdd {
		_, err := w.Add(file)
		if err != nil {
			return fmt.Errorf("failed to add file %s: %w", file, err)
		}
	}

	cfg, err := repo.Config()
	if err != nil {
		return fmt.Errorf("failed to get git config: %w", err)
	}

	name := cfg.User.Name
	email := cfg.User.Email

	if name == "" || email == "" {
		return fmt.Errorf("git user name or email not set in .git/config")
	}

	commitOptions := &git.CommitOptions{
		Author: &object.Signature{
			Name:  name,
			Email: email,
			When:  time.Now(),
		},
		All: true,
	}

	_, err = w.Commit(message, commitOptions)
	return err
}

func promptYesNo(question string) bool {
	log.Debug().Str("question", question).Msg("Prompting user for confirmation")
	fmt.Printf("%s (y/N): ", question)

	reader := bufio.NewReader(os.Stdin)
	response, err := reader.ReadString('\n')
	if err != nil {
		log.Error().Err(err).Msg("Failed to read user input")
		return false
	}

	response = strings.TrimSpace(strings.ToLower(response))
	result := response == "y" || response == "yes"
	log.Debug().Str("response", response).Bool("result", result).Msg("User response")
	return result
}

func getTimeFormat(formatString string) string {
	switch formatString {
	case "RFC3339":
		return time.RFC3339
	case "TimeFormatUnix":
		return "UNIXTIME"
	default:
		return time.RFC3339 // Default to RFC3339 if not specified or unknown
	}
}
