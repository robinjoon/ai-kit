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
