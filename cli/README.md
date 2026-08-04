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

From the repository root, build and install to `~/.local/bin/ctx`:

```bash
./scripts/install.sh
```

The installer uses an exact stable SemVer Git tag only from a clean checkout and
otherwise builds `ctx dev`. Both forms are accepted by the project Agent Skills.
Override the destination or version when needed:

```bash
./scripts/install.sh --bin-dir /usr/local/bin --version 0.1.0
```

The script does not invoke `sudo`, edit shell profiles, or replace a symlink,
directory, or unrelated file named `ctx`. When the destination is not on `PATH`, it
prints the corresponding `PATH` and `CTX_BIN` settings. Apply that setting to the
launch environment and fully restart already-running macOS GUI apps.
