# Architecture

## Core Principles

### Package Responsibilities

#### cmd/bumpa
- User interaction and prompts
- Command-line interface
- Workflow coordination
- Error presentation
- Domain package integration

#### internal/version
- Version parsing and validation
- Version bump calculations
- Workflow orchestration
- File update operations

#### internal/git
- Repository operations
- Commit and tag management
- Status and diff operations

#### internal/commit
- Commit message generation
- Message validation and formatting

#### internal/config
- Configuration management
- Environment handling

#### internal/errors
- Error definitions and wrapping
- Error context management

#### internal/llm
- LLM client operations
- Prompt management

### Design Principles
- Each package owns its domain workflow
- Clear boundaries and responsibilities
- Dependencies flow through interfaces
- Lower-level packages provide pure operations

### Workflow
1. User input (cmd/bumpa)
2. Domain logic (internal/*)
3. Results presentation (cmd/bumpa)### Log Levels

### Log Levels

#### Info Level
- Purpose: User-facing progress updates and actions
- Content: Brief, informal messages about completed steps and state changes
- Examples:
  - Actions taken ("Created commit")
  - Step completion ("Generated commit message")
  - State changes ("Version bumped to 1.2.0")
  - User-relevant outcomes ("No changes to commit")
- Style: Conversational, concise, focuses on what happened

#### Warn Level
- Purpose: Non-critical issues and recoverable problems
- Content: Issue description, impact, and possible resolution
- Examples:
  - Recoverable errors
  - Fallback to alternatives
  - Configuration issues
  - Rate limiting
  - Performance degradation
  - Invalid but skippable items
- Style: Clear, actionable, explains impact and next steps

#### Debug Level
- Purpose: Complete technical audit trail
- Content: Full technical context and data payloads
- Examples:
  - API requests/responses
  - Function entry/exit
  - State transitions
  - Git operations
  - Configuration details
  - Performance metrics
  - Decision points
- Style: Detailed, structured, comprehensive technical context

### Error Handling
- Domain packages return rich errors
- User-facing messages flow through main
- Technical details logged in domain

### Error Handling
- Domain packages return rich errors
- User messages flow through top level
- Technical details logged in domain

#### Error Layers
- Layer 1: Error Codes (e.g., CodeGitError, CodeLLMError)
- Layer 2: Error Messages (user-friendly descriptions)
- Layer 3: Base Errors (core error types)
- Layer 4: Error Contexts (detailed situation descriptions)

### Configuration
- Domain packages consume configuration
- Configuration flows through dependency injection

### Testing
- Domain logic tested in isolation
- Integration tests from top level
- Mock user interaction in main tests
