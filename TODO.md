# TODO

## Setup
- [x] Implement configuration loading
  - [x] Use Viper for YAML config parsing
  - [x] Define configuration struct
- [x] Implement logging facilities
  - [x] Implement LLM integration
  - [x] Set up OpenAI-compatible client
- [x] Setup proper project structure according to [golang-standards/project-layout](https://github.com/golang-standards/project-layout)
- [x] Implement logger and error management
- [x] Fix all linting errors
- [x] Implement rate limiting and retry logic

## Features
- [ ] Implement `changelog` command
- [x] Implement `commit` command
  - [x] Analyze git diff
  - [x] Generate commit message using LLM
  - [x] Generate a summary per file to minimize token use
  - [x] Implement conventional commits validation
  - [x] Add retry logic for failed generations
- [ ] Implement `pr` command
- [ ] Implement `release-notes` command
- [ ] Implement `version` command

## Configuration
- [x] Add support for custom LLM model configuration
- [x] Implement basic configuration validation
- [ ] Add support for custom commit message templates
- [ ] Add support for custom prompts

## Git
- [ ] Create prepare-commit-msg hook
- [x] Implement git diff analysis
- [x] Add support for gitignore patterns

## Documentation
- [ ] Write godoc comments for all exported functions and types
- [x] Create basic installation instructions
- [ ] Create usage examples for each command
- [ ] Create git hook usage examples
- [ ] Create CONTRIBUTING.md guide

## Testing
- [ ] Set up testing framework and mocks
- [x] Add basic logger tests
- [ ] Write unit tests for core functionality
- [ ] Write integration tests for CLI commands
- [ ] Set up mock LLM responses for testing

## Future
- [ ] Support additional LLM providers
- [ ] Add interactive mode for commit message editing
- [ ] Implement support for multiple languages
- [ ] Add performance optimizations for large diffs
