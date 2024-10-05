# bumpa

🚧 **HIGHLY EXPERIMENTAL WORK IN PROGRESS** 🚧

bumpa leverages LLMs to assist in generating changelogs, bumping application versions, creating release notes, and generating commit messages based on conventional commits.

## Features

- Generate commit messages adhering to conventional commits format
- Flexible LLM integration: Use locally via Ollama or any OpenAI API-compatible vendor
- Customizable configuration

## Planned features

- Create pull request descriptions
- Generate changelogs automatically
- Bump application versions following semantic versioning principles
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
- `bump`: Bump the application version
- `release-notes`: Generate release notes

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

🚧 This has yet not been tested, and might not work as expected (or at all).

Integrate bumpa into your CI/CD pipeline by adding it to your workflow configuration. For example, in a GitHub Actions workflow:

```yaml
- name: Generate Commit Message
  run: bumpa commit

- name: Generate Changelog
  run: bumpa changelog

- name: Bump Version
  run: bumpa bump

- name: Generate Release Notes
  run: bumpa release-notes
```

## Configuration

Rename the `bumpa.example.yaml` file to `.bumpa.yaml` and put it in your project root. Adjust accordingly:

```yaml
llm:
  provider: "ollama"  # or "openai" for any OpenAI API-comptaible provider
  model: "llama3.1:8b-instruct-q8_0"  # or e.g. "gpt-4o" for OpenAI
  api_key: "your-api-key-if-needed"

  logging:
    environment: "development"
    timeformat: "RFC3339"
    output: "console"
    level: "info"

  git:
    include_gitignore: false
    ignore:
      - ".bumpa.yaml"
      - "*.log"
      - "*.tmp"

  prompts:
    diff_summary:
      system: "Your system prompt for diff summary"
      user: "Your user prompt for diff summary"
    commit_message:
      system: "Your system prompt for commit message"
      user: "Your user prompt for commit message"
    file_summary:
      system: "Your system prompt for file summary"
      user: "Your user prompt for file summary"
```

Prompts are included in `bumpa.example.yaml`, but feel free to modify. Please create an issue or make a PR if you find a particular effective prompt and/or model!

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

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.
```
