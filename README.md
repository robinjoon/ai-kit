# ctx

`ctx`는 같은 컴퓨터에서 Claude Code와 Codex가 현재 Git worktree와 branch에
맞는 작업 컨텍스트를 이어 가게 하는 로컬 도구다.

## MVP 기능

1. repository·worktree·branch 조합마다 현재 작업 컨텍스트 하나를 유지한다.
2. `ctx start`로 새 작업을 시작한다.
3. 에이전트가 의미 있는 진행 뒤 `ctx checkpoint`를 자동 호출한다.
4. `ctx resume`은 최신 체크포인트 전체와 현재 Git 차이를 반환한다.
5. `ctx status`는 현재 작업과 마지막 저장 시점을 보여 준다.

체크포인트에는 목표, 현재 요약, 결정, 다음 행동과 차단 요소만 저장한다. 각
체크포인트는 이전 기록을 수정하지 않는 독립 JSON 파일이며, 현재 작업은 가장
최근 체크포인트 하나만 가리킨다.

## 고려하지 않는 것

- 서로 다른 물리적 컴퓨터 사이의 동기화
- 여러 에이전트의 동시 작업
- 다중 head, `parent_ids`, merge
- 네트워크 또는 파일 원격 저장소
- handoff 포인터와 별도 handoff 레코드
- 런타임 snapshot과 비정상 종료 복구
- 여러 작업의 목록·선택·alias
- 저장소 ID 이전과 `repo link`
- JSON Schema, 콘텐츠 해시, 분산 잠금
- Git checkout, commit, merge 또는 파일 수정

동시에 쓰는 상황은 지원하지 않는다. Claude Code와 Codex는 한 번에 하나씩
사용하며, 전환 전에 보내는 에이전트가 최신 체크포인트를 남긴다.

같은 컴퓨터의 여러 Git worktree와 branch는 지원한다. 이들은 같은 컨텍스트를
동시에 수정하는 주체가 아니라 서로 분리된 로컬 작업 공간으로 취급한다.

## 체크포인트 생성 경로

현재 사용할 수 있는 경로는 공용 Agent Skill이 `ctx checkpoint`를 호출하는
에이전트 기반 체크포인트다. 다음 시점에 사용자의 별도 저장 요청 없이 실행한다.

- 코드나 문서의 의미 있는 변경과 검증이 끝난 뒤
- 목표, 결정 또는 다음 행동이 바뀐 뒤
- 응답을 끝내기 전 저장할 만한 진행이 있을 때
- 다른 에이전트로 전환하기 전

단순 질의응답이나 실제 진행이 없는 턴에는 체크포인트를 만들지 않는다.

로그 기반 자동 체크포인트를 위한 내부 파이프라인도 구현되어 있다. 이 경로는
Claude Code와 Codex의 로컬 JSONL 로그를 직접 읽으므로 에이전트 훅에 의존하지
않는다. 다만 로그 증거를 전체 체크포인트로 바꾸는 모델은 아직 블랙박스 경계만
있으며, 실제 모델과 공개 `checkpoint --auto` 명령은 연결하지 않았다.

두 경로는 동일한 체크포인트 스키마와 저장소를 사용한다. 에이전트 기반 경로는
에이전트가 알고 있는 의도와 결정을 더 자세히 담을 수 있고, 로그 기반 경로는 앱이
평문으로 남긴 관찰 가능한 정보만 사용한다. 자세한 구조와 현재 구현 범위는
[자동 체크포인트 설계](docs/automatic-checkpoints.md)에 정리되어 있다.

## 사용 흐름

```text
Claude Code                    로컬 ctx                     Codex
     |                            |                            |
     |  start / checkpoint       |                            |
     |--------------------------->|                            |
     |                            |       resume latest        |
     |                            |<----------------------------|
     |                            |---------------------------->|
     |                            |     checkpoint after work   |
     |                            |<----------------------------|
```

별도 handoff 명령은 없다. 보내는 쪽이 체크포인트를 만들고 받는 쪽이 최신
체크포인트를 재개하면 전환이 끝난다.

## CLI

```bash
ctx start --title "로그인 오류 수정"
ctx checkpoint --input checkpoint.json --reason progress
ctx resume
ctx status
```

체크포인트 입력은 작은 JSON 객체다.

```json
{
  "goal": "로그인 오류를 수정한다.",
  "summary": "세션 쿠키 만료 처리를 수정했고 회귀 테스트를 추가했다.",
  "decisions": ["만료 쿠키는 즉시 폐기한다."],
  "next_actions": ["전체 테스트를 실행한다."],
  "blockers": []
}
```

`goal`과 `summary`만 필수다. CLI는 알 수 없는 필드를 거부하며 JSON Schema나
외부 검증 라이브러리를 사용하지 않는다.

기본 저장 위치는 운영체제의 사용자 설정 디렉터리 아래 `ctx/`다. 현재 Git
위치를 관찰해 repository, worktree, branch bucket을 자동 선택한다.

```text
ctx/
  repos/<repository-name>-<key>/
    worktrees/<worktree-name>-<key>/
      branches/<branch-name>-<key>/
        scope.json
        active.json
        checkpoints/<checkpoint-id>.json
```

예를 들어 `feature/login` branch는 다음과 같이 안전한 이름과 짧은 해시를 함께
사용한다.

```text
repos/ai-kit-a0764528/
  worktrees/ai-kit-a0764528/
    branches/feature-login-7d3a41b2/
```

- repository key는 Git common directory에서 만든다. 연결된 worktree들이 같은
  repository 아래 모인다.
- worktree key는 worktree 루트 절대 경로에서 만든다.
- branch key는 branch 이름에서 만든다. detached HEAD는 commit별 bucket을 쓴다.
- `scope.json`에는 원본 common directory, worktree 경로와 branch 이름을 기록한다.
- `active.json`과 체크포인트는 해당 branch bucket 안에만 존재한다.

브랜치를 전환하면 그 브랜치의 최신 컨텍스트가 자동 선택되고, 새 worktree에서
실행하면 그 worktree 전용 컨텍스트가 선택된다. 체크포인트 ID에는 Git 정보를
넣지 않는다.

## 설치

Go 1.26 이상에서 다음을 실행한다.

```bash
./scripts/install.sh
```

기본 설치 위치는 `~/.local/bin/ctx`다. 네 Agent Skill의 공유 사본은
`~/.local/share/ctx/skills`에 설치되고 Claude Code와 Codex가 같은 사본을
가리킨다. 설치 후 CLI가 PATH에 없다면 다음 환경 변수를 설정하고 앱을 완전히
재시작한다.

```bash
export CTX_BIN="$HOME/.local/bin/ctx"
```

## 개발 검증

```bash
cd cli
go test ./...
go test -race ./...
go vet ./...
go build -trimpath -o bin/ctx ./cmd/ctx
```

```bash
./scripts/install_test.sh
```
