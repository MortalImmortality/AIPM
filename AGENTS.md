# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

Go 1.26+ is required. The executable is `aipm`.

```sh
go mod download
make build              # bin/aipm
./bin/aipm              # interactive terminal required
./bin/aipm list         # read-only, redacted registry listing
./bin/aipm status       # infer actual user configuration
make test               # go test ./...
make race               # go test -race ./...
make vet                # go vet ./...
make fmt-check
make smoke              # isolated real-PTY shell smoke test; requires Python 3
```

Single test: `go test ./internal/config -run TestCodexRoundTrip -v`.

## Architecture

`cmd/aipm` handles launch, read-only commands, and numbered shell-menu startup. `internal/shell` owns line-oriented menus, sequential forms, redacted confirmations, and hidden terminal input. It does not construct configuration patches. Ordinary input must not prefetch bytes intended for a later secret read; terminal state must be restored on cancellation or signals. No alternate screen, cursor repainting, or screen clearing.

`internal/app` prepares CRUD and switch actions, validates active-provider lifecycle rules, and coordinates adapters with the registry. Prepared actions capture the expected state; cancellation must not write anything.

`internal/config` infers the active provider from actual client configuration and creates targeted patches. Codex uses `go-toml/v2/unstable/edit` to preserve unrelated TOML bytes and comments. Claude preserves unrelated JSON data but may normalize formatting. Official restoration edits routing/model fields, never replaces the file with a stale backup or modifies saved logins. Official preference snapshots are bound to tool and resolved target path.

`internal/store` owns the private registry, backups, locks, optimistic content checks, atomic writes, and recoverable config/registry transactions. Registry activity metadata must not replace inspection of actual client configuration.

`internal/provider` owns provider IDs, validation, authentication modes, and model mapping keys. Codex uses the exact literal `anim_<Name>` configuration key, independent of the random registry ID. Pass TOML key components to the editor; display names never become file paths or interpolated TOML syntax. Inspect accepts legacy `aipm_<ID>` keys; explicit apply migrates them. Remove tables only when registered definitions or path-bound snapshot hashes prove ownership, never by prefix alone.

## Configuration boundary

Default targets are `$CODEX_HOME/config.toml` (fallback `~/.codex/config.toml`) and `$CLAUDE_CONFIG_DIR/settings.json` (fallback `~/.claude/settings.json`). Registry defaults to `os.UserConfigDir()/aipm`; `AIPM_HOME` overrides it. Overrides must be absolute paths.

This tool manages Codex Responses-compatible providers and Claude Anthropic-compatible gateways only. No API calls, login management, cloud auth integration, shell-profile edits, managed-policy edits, or client launches. Existing Claude credential helpers/cloud-provider selections are blockers, not settings to silently remove.

Direct credentials and backups are private plaintext (0700 directories, 0600 files), not encrypted. All tests and manual smoke runs must use isolated target directories and dummy credentials; do not inspect or modify personal Codex/Claude settings during development.

## Dependency compatibility

The shell UI uses standard-library I/O and `github.com/charmbracelet/x/term` for terminal mode handling, with no full-screen UI framework. The TOML editor is an unstable API pinned in `go.mod`; upgrades require preservation fixtures to pass. See README for verified official provider documentation and configuration-precedence limits.
