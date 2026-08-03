# ctx CLI

Go implementation of the shared `ctx` command-line core.

## Development

Requires Go 1.26 or newer.

```bash
go test ./...
go run ./cmd/ctx --help
go build -trimpath -o bin/ctx ./cmd/ctx
```

Release builds can replace the default `dev` version through `-ldflags`.

```bash
go build -trimpath -ldflags "-X main.version=0.1.0" -o bin/ctx ./cmd/ctx
```
