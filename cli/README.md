# ctx CLI

같은 컴퓨터의 Claude Code와 Codex가 현재 Git repository·worktree·branch의
최신 작업 컨텍스트를 공유하기 위한 작은 Go CLI다. 외부 Go 의존성, JSON
Schema, 동기화, 다중 head를 사용하지 않는다.

```bash
go test ./...
go run ./cmd/ctx --help
```

명령은 `start`, `checkpoint`, `resume`, `status` 네 개다. 모든 명령에
`--cwd`, `--store`, `--client`, `--json` 전역 옵션을 사용할 수 있다.
