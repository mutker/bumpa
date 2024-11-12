# bumpa

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Go Report Card](https://goreportcard.com/badge/codeberg.org/mutker/bumpa)](https://goreportcard.com/report/codeberg.org/mutker/bumpa)

🚧 **HIGHLY EXPERIMENTAL WORK IN PROGRESS** 🚧

bumpa leverages LLMs to assist in generating changelogs, bumping application versions, creating release notes, and generating commit messages based on conventional commits.

## Features

- Generate commit messages adhering to conventional commits format (with support for GPG commit signing)
- Flexible LLM integration: Use locally via Ollama or any OpenAI API-compatible vendor
- Advanced git configuration handling (with includeIf directives support)
- Bump application versions following semantic versioning principles

## Planned features

- Create pull request descriptions
- Generate changelogs automatically
- Create release notes (configurable for different version bump types)
- Multiple usage modes: Standalone CLI, Git commit hook, or in CI/CD workflows
- Templating support for prompts, changelogs, release notes, commit messages, and pull request descriptions

## Installation

```bash
go install codeberg.org/mutker/bumpa@latest
```

## Usage

### As a CLI tool

Please note: Currently only `commit` is implemented. Additional commands will be implemented in the future.

```bash
bumpa [command] [flags]
```

Available commands:
  - `commit`: Generate a commit message
  - `pr`: Generate a pull request description
  - `changelog`: Generate a changelog
  - `version`: Bump the semantic version
  - `release`: Generate release notes

### As a Git commit hook

🚧 This has yet not been tested, and might not work as expected (or at all).

1. Create a file named `prepare-commit-msg` in your `.git/hooks/` directory
2. Add the following content:

```bash
#!/bin/sh
bumpa commit
```

3. Make the hook executable:

```bash
chmod +x .git/hooks/prepare-commit-msg
```

### In CI/CD workflows

🚧 This has not yet been implemented!

Integrate bumpa into your CI/CD pipeline by adding it to your workflow configuration. For example, in a GitHub Actions workflow:

```yaml
- name: Generate Commit Message
  run: bumpa commit

- name: Generate Changelog
  run: bumpa changelog

- name: Bump Version
  run: bumpa version

- name: Generate Release Notes
  run: bumpa release-notes
```

## Configuration

Rename the `bumpa.example.yaml` file to `.bumpa.yaml` and put it in your project root.

```yaml
logging:
  environment: development
  timeformat: "RFC3339"
  output: console
  level: debug

llm:
  provider: openai-compatible
  model: llama3-70b-tool-use
  base_url: http://localhost:11434/v1
  api_key: ""  # Optional, remove if not needed
  max_retries: 3
  request_timeout: 30s
  commit_msg_timeout: 30s

git:
  include_gitignore: true
  ignore:
    - "go.mod"
    - "go.sum"
    - "*.log"
    - "TODO.md"
  max_diff_lines: 10
  preferred_line_length: 72 # Standard git commit message length

# Function calls/tools for commit message generation
tools:
  - name: "generate_file_summary"
    system_prompt: ...
    user_prompt: ...

  - name: "generate_commit_message"
    system_prompt: ...
    user_prompt: ...

  - name: "retry_commit_message"
    system_prompt: ...
    user_prompt: ...
```

Prompts for function calls (tool use) are included in `bumpa.example.yaml`, but feel free to modify. Please create an issue or make a PR if you find a particular effective prompt and/or model!

## Templates

🚧 This has not yet been implemented!

bumpa supports customizable templates for various outputs. Create your templates using Go's text/template syntax and specify their paths in the configuration file.

Example commit message template:

```
{{.Type}}({{.Scope}}): {{.Subject}}

{{.Body}}

{{.Footer}}
```

## Contributing

Contributions are welcome! Please feel free to submit a issue or pull request.

## License

This project is licensed under the MIT License. See the [LICENSE](LICENSE) file for details.
```
