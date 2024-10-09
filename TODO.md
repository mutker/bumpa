# TODO

## Setup
- [x] Implement configuration loading
  - [x] Use Viper for YAML config parsing
  - [x] Define configuration struct
- [x] Implement logging facilities
  - [x] Implement LLM integration
  - [x] Set up Ollama client
  - [x] Set up OpenAI client
- [ ] Setup proper project structure according to [golang-standards/project-layout](https://github.com/golang-standards/project-layout)

## Features
- [x] Implement `commit` command
  - [x] Analyze git diff
  - [x] Generate commit message using LLM
  - [x] Generate a summary per file to minimize token use
- [ ] Implement `pr` command
- [ ] Implement `changelog` command
- [ ] Implement `bump` command
  - [ ] Implement version bumping logic
  - [ ] Parse and update VERSION file
  - [ ] Implement semantic versioning rules
- [ ] Implement `release-notes` command

## Configuration
- [x] Add support for custom LLM model configuration
- [ ] Implement configuration validation

## Git
- [ ] Create prepare-commit-msg hook

## Documentation
- [ ] Write godoc comments for all exported functions and types
- [ ] Create installation instructions
- [ ] Create usage examples for each command
- [ ] Create git hook usage examples
- [ ] Create CONTRIBUTING.md guide

## Packaging and Distribution
- [ ] Create PKGBUILD and Dockerfile
- [ ] Set up Goreleaser configuration
- [ ] Create golangci-lint workflow
- [ ] Create release workflow

## Testing
- [ ] Set up testing framework and mocks
- [ ] Write unit tests for core functionality
- [ ] Write integration tests for CLI commands
- [ ] Set up mock LLM responses for testing

## Future
- [ ] Implement support for additional LLM providers
