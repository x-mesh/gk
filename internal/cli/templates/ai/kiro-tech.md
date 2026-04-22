---
inclusion: always
---
# Tech Stack & Architecture

## Language & runtime
<!-- TODO: Primary language and version, e.g. "Go 1.24", "Node 22 (ESM)", "Python 3.12" -->

## Key dependencies
<!-- TODO: List major libraries/frameworks with a one-line rationale for each -->
| Package | Purpose |
|---------|---------|
| <!-- dep --> | <!-- why --> |

## Architecture style
<!-- TODO: e.g. "Layered CLI (cmd → internal → pkg)", "Hexagonal", "Monolith", "Microservices" -->

## Directory conventions
<!-- TODO: Describe which code lives where and why -->
- `cmd/` — entry points only; no business logic
- `internal/` — private application code
- `pkg/` — reusable packages safe to import externally

## Build & tooling
<!-- TODO: How to build, test, lint -->
- Build: `<!-- e.g. go build ./... -->`
- Test:  `<!-- e.g. go test ./... -->`
- Lint:  `<!-- e.g. golangci-lint run -->`

## Key architectural decisions
<!-- TODO: Document non-obvious choices the AI must respect -->
- **Decision 1**: <!-- What was decided and why; what was rejected -->
- **Decision 2**: ...

## Coding standards
<!-- TODO: Style rules, naming conventions, error-handling patterns, etc. -->
- Error handling: ...
- Naming: ...
- Testing: ...

## Environment & configuration
<!-- TODO: How is the app configured? Env vars, config files, flags? -->
- Config file: ...
- Required env vars: ...
