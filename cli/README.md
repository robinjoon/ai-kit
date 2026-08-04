# ctx CLI

Go implementation of the shared `ctx` command-line core.

## Development

Requires Go 1.26 or newer.

```bash
go test ./...
go test -race ./...
go vet ./...
go run ./cmd/ctx --help
go build -trimpath -o bin/ctx ./cmd/ctx
```

The CLI implements `task create/list/switch`, `repo link --from <local-repo-id>`,
`resolve`, `checkpoint`, `handoff`, `resume`, `status`, `snapshot`, and filesystem `sync`. Use
`ctx <command> --help` for command-specific input and selection rules.

Release builds can replace the default `dev` version through `-ldflags`.

```bash
go build -trimpath -ldflags "-X main.version=0.1.0" -o bin/ctx ./cmd/ctx
```

## Install from the checkout

From the repository root, install the CLI and the five shared Agent Skills:

```bash
./scripts/install.sh
```

The default CLI target is `~/.local/bin/ctx`. Canonical skill copies are installed
under `~/.local/share/ctx/skills`, with user-level links in `~/.claude/skills` and
`~/.agents/skills` so Claude Code and Codex use the same instructions. The
installer uses an exact stable SemVer Git tag only from a clean checkout and
otherwise builds `ctx dev`. Both forms are accepted by the Agent Skills. Override
the destination or version when needed:

```bash
./scripts/install.sh --bin-dir /usr/local/bin --version 0.1.0
```

Use `./scripts/install.sh --help` for the three skill-directory overrides. The
script does not invoke `sudo`, edit shell profiles, replace an unrelated file named
`ctx`, or overwrite same-named skills it does not manage. When the destination is
not on `PATH`, it prints the corresponding `PATH` and `CTX_BIN` settings. Apply
that setting to the launch environment and fully restart already-running macOS GUI
apps.
