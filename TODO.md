# TODO

## Core Functionality
- [x] Implement configuration loading
  - [x] Use Viper for YAML config parsing
  - [x] Define configuration struct
- [x] Implement LLM integration
  - [x] Set up Ollama client
  - [x] Set up OpenAI client
- [ ] Implement version bumping logic
  - [ ] Parse and update VERSION file
  - [ ] Implement semantic versioning rules

## CLI Commands
- [x] Set up CLI framework
- [x] Implement `commit` command
  - [x] Analyze git diff
  - [x] Generate commit message using LLM
  - [ ] Generate a summary per file to minimize token use
- [ ] Implement `pr` command
- [ ] Implement `changelog` command
- [ ] Implement `bump` command
- [ ] Implement `release-notes` command

## Testing
- [ ] Set up testing framework and mocks
- [ ] Write unit tests for core functionality
- [ ] Write integration tests for CLI commands
- [ ] Set up mock LLM responses for testing

## Git Hook Integration
- [ ] Implement Git hook installation command
- [ ] Create prepare-commit-msg hook script

## Documentation
- [ ] Write godoc comments for all exported functions and types
- [ ] Create usage examples for each command
- [x] Update README.md with detailed installation and usage instructions
- [ ] Create CONTRIBUTING.md guide

## Packaging and Distribution
- [ ] Set up Goreleaser configuration
- [ ] Create release workflow in GitHub Actions

## Performance Optimization
- [ ] Implement caching mechanism for LLM responses
- [x] Optimize git diff analysis for large repositories

## Error Handling and Logging
- [x] Implement comprehensive error handling
- [x] Add detailed logging for debugging and monitoring

## Configuration
- [x] Add support for custom LLM model configuration
- [ ] Implement configuration validation

## New Tasks
- [ ] Implement support for additional LLM providers
- [ ] Add support for custom templates for PR descriptions and release notes
- [ ] Implement a plugin system for extensibility
- [ ] Add support for internationalization (i18n)
- [ ] Implement a dry-run mode for all commands
- [ ] Add support for different version control systems (e.g., Mercurial, SVN)
- [ ] Implement a web interface for easier configuration and usage
- [ ] Implement commit signing support
  - [ ] Add GPG signing option
  - [ ] Add SSH signing option
  - [ ] Ensure compatibility with different Git configurations

## Final Steps
- [ ] Perform code review and refactoring
- [ ] Conduct security audit
- [ ] Set up issue templates on GitHub
- [ ] Perform end-to-end testing
- [ ] Update documentation based on final implementation
