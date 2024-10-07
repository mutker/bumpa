# TODO

## Core Functionality
- [x] Implement configuration loading
  - [x] Use Viper for YAML config parsing
  - [x] Define configuration struct
- [x] Implement error handling
- [x] Add detailed logging for debugging and monitoring
- [ ] Implement version bumping logic
  - [ ] Parse and update VERSION file
  - [ ] Implement semantic versioning rules

## LLM Integration
- [x] Implement LLM integration
  - [x] Set up Ollama client
  - [x] Set up OpenAI client
- [x] Implement tool use
- [ ] Implement caching mechanism for LLM responses

## CLI Commands
- [x] Implement `commit` command
  - [x] Analyze git diff
  - [x] Implement retry mechanism
  - [x] Generate a summary per file to minimize token use
- [ ] Implement `bump` (version bump) command
- [ ] Implement `changelog` command
- [ ] Implement `rel` (release notes) command
- [ ] Implement `pr` (pull request) command
  - [ ] Support for codeberg.org
  - [ ] Support for GitLab
  - [ ] Support for GitHub

## Testing
- [ ] Set up testing framework and mocks
- [ ] Write unit tests for core functionality
- [ ] Write integration tests for CLI commands
- [ ] Set up mock LLM responses for testing

## Git Integration
- [ ] Implement .gitignore handling for file ignoring
- [ ] Create prepare-commit-msg hook script
- [ ] Implement Git hook installation command
- [ ] Implement commit signing support
  - [ ] Add GPG signing option
  - [ ] Add SSH signing option

## Documentation
- [ ] Write godoc comments for all exported functions and types
- [ ] Create usage examples for each command
- [ ] Update README.md with detailed installation and usage instructions
- [ ] Create CONTRIBUTING.md guide

## Packaging and Distribution
- [ ] Set up Goreleaser configuration
- [ ] Create release workflow for Codeberg CI

## Configuration
- [x] Add support for custom LLM model configuration
- [x] Implement configuration validation
- [ ] Add support for custom templates for PR descriptions and release notes

## Final Steps
- [ ] Perform code review and refactoring
- [ ] Conduct security audit
- [ ] Set up issue templates on Codeberg
- [ ] Perform end-to-end testing
- [ ] Update documentation based on final implementation
