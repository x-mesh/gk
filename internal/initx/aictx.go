package initx

// AIContextFile은 생성될 AI context 파일 하나를 나타낸다.
type AIContextFile struct {
	Path    string // 상대 경로 (예: "CLAUDE.md")
	Content string // 생성된 내용
}

// AIContextOptions는 AI context 생성 옵션이다.
type AIContextOptions struct {
	IncludeKiro bool // .kiro/steering/* 파일도 생성할지
}

// GenerateAIContext는 프로젝트 분석 결과를 template에 주입하여
// AI context 파일 내용을 생성한다.
// IncludeKiro=true 시 .kiro/steering/ 파일 3개를 생성한다.
// CLAUDE.md, AGENTS.md는 AI 코딩 어시스턴트가 자동 생성하므로 포함하지 않는다.
func GenerateAIContext(result *AnalysisResult, opts AIContextOptions) []AIContextFile {
	var files []AIContextFile

	if opts.IncludeKiro {
		files = append(files,
			AIContextFile{Path: ".kiro/steering/product.md", Content: kiroProductTemplate},
			AIContextFile{Path: ".kiro/steering/tech.md", Content: kiroTechTemplate},
			AIContextFile{Path: ".kiro/steering/structure.md", Content: kiroStructureTemplate},
		)
	}

	return files
}

// bt is a backtick character, used to embed backticks inside raw string literals.
const bt = "`"

// Template constants — internal/cli/templates/ai/ 파일 내용과 동일.
// Go embed는 같은 패키지 디렉토리에서만 동작하므로 상수로 정의한다.

var claudeMDTemplate = `# CLAUDE.md

> This file is read by Claude Code at the start of every session.
> Fill in each section — delete placeholder lines when done.
> Commit this file so the whole team benefits.

## Project

<!-- One-paragraph description of what this project does and why it exists. -->
TODO: describe the project

## Commands

<!-- The commands Claude should use to build, test, lint, and run this project. -->

` + bt + bt + bt + `sh
# Build
TODO: e.g.  go build ./...  |  npm run build  |  make build

# Test (run before every commit)
TODO: e.g.  go test ./...   |  npm test        |  pytest

# Lint / format
TODO: e.g.  golangci-lint run  |  npm run lint  |  ruff check .

# Run locally
TODO: e.g.  go run ./cmd/app  |  npm run dev
` + bt + bt + bt + `

## Architecture

<!-- High-level layout. What lives where, and why. -->

` + bt + bt + bt + `
TODO: e.g.
cmd/         CLI entry-points
internal/    private packages (not importable by outside modules)
pkg/         public/shared packages
` + bt + bt + bt + `

Key design decisions:
- TODO: decision 1
- TODO: decision 2

## Conventions

<!-- Rules Claude must follow when writing or modifying code in this repo. -->

- **Style**: TODO (e.g. gofmt + golangci-lint / eslint + prettier / black + ruff)
- **Naming**: TODO (e.g. snake_case for files, CamelCase for types)
- **Error handling**: TODO (e.g. always wrap with fmt.Errorf / never swallow errors)
- **Tests**: TODO (e.g. table-driven, one assertion per test, no external deps in unit tests)
- **Commits**: TODO (e.g. Conventional Commits — feat/fix/chore/refactor)
- **No magic numbers** — use named constants
- **No commented-out code** — delete it or track it in a ticket

## Key Files

<!-- Point Claude at the files that matter most so it reads them first. -->

| File / Directory | Purpose |
|------------------|---------|
| TODO path        | TODO purpose |
| TODO path        | TODO purpose |

## Environment

<!-- Variables required to run or test the project locally. -->

` + bt + bt + bt + `sh
# Copy .env.example and fill in values
# TODO: list required env vars, e.g.:
# DATABASE_URL=
# API_KEY=
` + bt + bt + bt + `

## Out of Scope

<!-- Topics Claude should NOT touch without explicit instruction. -->

- TODO: e.g. "Do not modify the generated protobuf files in pkg/proto/"
- TODO: e.g. "Do not change the public API surface of pkg/ without a design discussion"
`

var agentsMDTemplate = `# AGENTS.md

> This file is read by AI coding agents (Jules, GitHub Copilot Workspace,
> Codex, Gemini CLI, etc.) before they start work.
> Keep it short, factual, and always up-to-date.

## Project Overview

<!-- One paragraph: what this project is, what language/runtime it uses,
     and what problem it solves. -->
TODO: describe the project

## Repository Layout

` + bt + bt + bt + `
TODO: e.g.
cmd/          entry-points / CLI binaries
internal/     private application packages
pkg/          exported/shared packages
docs/         design documents and ADRs
testdata/     fixtures used by tests
` + bt + bt + bt + `

## Build & Test

<!-- Exact commands an agent must run to verify its changes compile and pass tests. -->

` + bt + bt + bt + `sh
# Verify everything builds
TODO: e.g.  go build ./...  |  npm ci && npm run build

# Run the full test suite
TODO: e.g.  go test ./...   |  npm test  |  pytest -q

# Lint (must be clean before opening a PR)
TODO: e.g.  golangci-lint run  |  npm run lint  |  ruff check . && mypy .
` + bt + bt + bt + `

**An agent MUST run build + test + lint and confirm they are clean before
submitting any change.**

## Coding Conventions

- Language/runtime: TODO (e.g. Go 1.24 / Node 22 / Python 3.12)
- Formatter: TODO (e.g. gofmt / prettier / black) — run before every commit
- Error strategy: TODO (e.g. wrap all errors with context; never panic in library code)
- Test style: TODO (e.g. table-driven unit tests; no mocks for pure functions)
- Commit format: TODO (e.g. Conventional Commits — ` + bt + `feat:` + bt + `, ` + bt + `fix:` + bt + `, ` + bt + `chore:` + bt + `)

## Key Interfaces & Entry Points

<!-- The files an agent should read first to understand the codebase. -->

| Path | Role |
|------|------|
| TODO | TODO |
| TODO | TODO |

## Do Not Touch

<!-- Files or directories agents must never modify without explicit human approval. -->

- TODO: e.g. ` + bt + `pkg/proto/` + bt + ` — auto-generated; re-run ` + bt + `make proto` + bt + ` instead
- TODO: e.g. ` + bt + `CHANGELOG.md` + bt + ` — updated by the release script only
- TODO: e.g. ` + bt + `.github/workflows/` + bt + ` — CI config requires team review

## Pull Request Checklist

Before marking a PR ready for review an agent must confirm:

- [ ] ` + bt + `TODO build command` + bt + ` exits 0
- [ ] ` + bt + `TODO test command` + bt + ` exits 0 with no failures
- [ ] ` + bt + `TODO lint command` + bt + ` exits 0
- [ ] No new TODO/FIXME left in changed files (or each is tracked in an issue)
- [ ] Commit messages follow the project convention
`

var kiroProductTemplate = `---
inclusion: always
---
# Product Overview

## What this product is
<!-- TODO: One sentence description of the product and its core value -->
<!-- Example: "A CLI tool that streamlines Git workflows for developer teams." -->

## Target users
<!-- TODO: Who uses this product and what problem does it solve for them? -->
<!-- Example: "Developers who want safer, faster Git operations without memorizing flags." -->

## Core goals
- <!-- TODO: Primary goal — the single most important outcome this product must achieve -->
- <!-- TODO: Secondary goal -->
- <!-- TODO: Non-goal: what this product deliberately does NOT do -->

## Key features
<!-- TODO: List the major capabilities. One line each. -->
- Feature 1: ...
- Feature 2: ...
- Feature 3: ...

## Success criteria
<!-- TODO: How do you know the product is working? Metrics, user signals, etc. -->
- ...

## Out of scope
<!-- TODO: Explicitly list what this product will never do, to prevent scope creep -->
- ...
`

var kiroTechTemplate = `---
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
- ` + bt + `cmd/` + bt + ` — entry points only; no business logic
- ` + bt + `internal/` + bt + ` — private application code
- ` + bt + `pkg/` + bt + ` — reusable packages safe to import externally

## Build & tooling
<!-- TODO: How to build, test, lint -->
- Build: ` + bt + `<!-- e.g. go build ./... -->` + bt + `
- Test:  ` + bt + `<!-- e.g. go test ./... -->` + bt + `
- Lint:  ` + bt + `<!-- e.g. golangci-lint run -->` + bt + `

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
`

var kiroStructureTemplate = `---
inclusion: always
---
# Repository Structure

## Top-level layout
` + bt + bt + bt + `
.
├── cmd/            # Executable entry points
├── internal/       # Private application packages
├── pkg/            # Public reusable packages
├── docs/           # Documentation
├── testdata/       # Fixtures and golden files
└── <!-- TODO: add/remove directories to match your repo -->
` + bt + bt + bt + `

## Key files
<!-- TODO: List files the AI must know about to navigate this codebase effectively -->
| File / Dir | Role |
|-----------|------|
| ` + bt + `<!-- path -->` + bt + ` | <!-- what it does --> |

## Where to put new code
<!-- TODO: Decision rules for where new logic belongs -->
- New CLI command → ` + bt + `<!-- e.g. internal/cli/<command>.go -->` + bt + `
- New business logic → ` + bt + `<!-- e.g. internal/<package>/ -->` + bt + `
- New shared utility → ` + bt + `<!-- e.g. pkg/<package>/ -->` + bt + `
- Tests → alongside the file they test, ` + bt + `_test.go` + bt + ` suffix

## Generated files
<!-- TODO: List any files that are auto-generated and must NOT be edited by hand -->
- ` + bt + `<!-- file -->` + bt + ` — generated by ` + bt + `<!-- tool -->` + bt + `; edit ` + bt + `<!-- source -->` + bt + ` instead

## Naming conventions
<!-- TODO: File, package, and symbol naming rules -->
- Files: ` + bt + `snake_case.go` + bt + `
- Packages: short, lowercase, no underscores
- Exported types: ` + bt + `PascalCase` + bt + `; unexported: ` + bt + `camelCase` + bt + `

## Module / package boundaries
<!-- TODO: Describe import rules, forbidden dependencies, layering constraints -->
- ` + bt + `internal/cli` + bt + ` may import ` + bt + `internal/*` + bt + ` and ` + bt + `pkg/*` + bt + `
- ` + bt + `pkg/*` + bt + ` must NOT import ` + bt + `internal/*` + bt + `
- <!-- TODO: other rules -->
`