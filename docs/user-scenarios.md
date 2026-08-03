# ctx 사용자 시나리오

`ctx`는 Claude Code와 Codex 사이에서 개발 작업의 **의미 있는 상태**를 이어 주는 로컬 우선 시스템이다. 사용자는 각 앱 안에서 스킬을 호출하고, 공통 `ctx` CLI가 작업·체크포인트·Git 기준점·핸드오프를 관리한다.

> 이 문서는 v1 스키마와 MVP 범위의 **사용 흐름**을 설명한다. 공통 Go CLI는 구현되어 있으며, 흐름을 앱 안에서 호출하는 Claude Code와 Codex용 스킬은 아직 구현 대상이다.

## 한눈에 보는 역할 분리

```mermaid
flowchart LR
    U["사용자"] --> APP["이미 열려 있는<br/>Claude Code 또는 Codex"]
    APP --> SKILL["ctx 스킬"]
    SKILL --> CLI["ctx CLI"]
    CLI --> STORE["로컬 사이드카 저장소<br/>작업·체크포인트·핸드오프"]
    CLI -. "상태 관측·비교" .-> GIT["Git 작업 사본"]
    STORE <--> SYNC["선택적 ctx 동기화"]
    GIT <--> REMOTE["기존 Git 원격"]
```

두 저장 경로는 서로 다른 문제를 해결한다.

| 대상 | 정본 | ctx가 하는 일 |
|---|---|---|
| 소스 코드와 패치 | 기존 Git 저장소 | 브랜치, HEAD, 변경 상태를 관측하고 비교한다. |
| 목표, 결정, 진행 상황, 다음 행동 | 체크포인트 JSON | 에이전트가 독립적으로 재개할 수 있는 전체 상태를 저장한다. |
| 앱·장치 전환 지점 | 안정 체크포인트와 얇은 핸드오프 | 재개할 체크포인트를 명확히 가리킨다. |
| 순간적인 Git·세션 상태 | 런타임 스냅숏 | 예기치 않은 종료 뒤 상태 확인을 보조한다. |

`ctx`는 코드를 복사하거나 브랜치를 바꾸지 않는다. 장치 간 전환에서는 Git과 ctx 컨텍스트가 각각 동기화되어야 한다.

## 시나리오 1. 새 작업을 시작하고 중간 상태를 저장한다

사용자는 앱 안에서 새 작업을 시작한다. 이후 의미 있는 결정이나 진행 지점을 체크포인트로 남긴다.

```mermaid
sequenceDiagram
    actor User as 사용자
    participant App as Claude Code 또는 Codex
    participant Skill as ctx 스킬
    participant CLI as ctx CLI
    participant Git as Git 작업 사본
    participant Store as 사이드카 저장소

    User->>App: 새 작업을 시작해 줘
    App->>Skill: 시작 의도 전달
    Skill->>CLI: task create
    CLI->>Git: 저장소 식별 및 상태 관측
    CLI->>Store: 작업 ID 생성 및 앱에 바인딩
    CLI-->>App: 활성 작업 반환

    Note over User,App: 개발 작업 진행

    User->>App: 여기까지 체크포인트로 저장해 줘
    Skill->>CLI: resolve
    CLI->>Git: 현재 HEAD와 작업 트리 관측
    Skill->>CLI: checkpoint + 의미 컨텍스트
    CLI->>Store: 새 체크포인트 추가
    CLI-->>App: 체크포인트 ID와 저장 결과
```

- 작업 ID는 자동 발급되며 브랜치명이나 앱 세션 ID와 분리된다.
- 체크포인트는 이전 기록을 덮어쓰지 않는 추가 전용 레코드다.
- 캡처가 완전하면 안정 체크포인트, 일부가 누락되면 초안 체크포인트가 된다.
- 미커밋 코드는 체크포인트에 복제되지 않고 기존 작업 트리에 남는다.

## 시나리오 2. 같은 Mac에서 Claude Code에서 Codex로 전환한다

앱을 새로 실행하거나 프롬프트를 복사하지 않는다. 보내는 앱에서 핸드오프를 만들고, 받는 앱에서 같은 작업을 재개한다.

```mermaid
sequenceDiagram
    actor User as 사용자
    participant Claude as Claude Code
    participant Ctx as ctx CLI와 저장소
    participant Git as 같은 Git 작업 사본
    participant Codex as Codex

    User->>Claude: Codex에서 이어갈 수 있게 핸드오프해 줘
    Claude->>Ctx: resolve 후 handoff
    Ctx->>Git: 현재 Git 기준점 관측
    Ctx->>Ctx: 안정 체크포인트와 핸드오프 생성
    Ctx-->>Claude: 저장 완료

    User->>Codex: 최근 작업을 이어서 해 줘
    Codex->>Ctx: resume
    Ctx->>Git: 체크포인트 기준점과 현재 상태 비교
    Ctx-->>Codex: 목표·결정·진행·다음 행동·Git 차이
    Codex-->>User: 이어서 할 작업 제시 또는 실행
```

핸드오프 문서는 작업 내용을 다시 복사한 문서가 아니다. 안정 체크포인트의 ID와 해시를 가리키는 얇은 포인터이며, 실제 재개 컨텍스트는 체크포인트에서 만든다.

## 시나리오 3. 다른 Mac에서 같은 작업을 이어 간다

장치 전환에는 **코드 경로**와 **컨텍스트 경로**가 모두 준비되어야 한다.

```mermaid
flowchart LR
    subgraph A["Mac A"]
        A_APP["Claude Code"]
        A_GIT["Git 작업 사본"]
        A_CTX["ctx 사이드카"]
        A_APP -->|"코드 작업"| A_GIT
        A_APP -->|"handoff --sync"| A_CTX
    end

    GIT_REMOTE["Git 원격"]
    CTX_REMOTE["ctx 동기화 저장소"]

    subgraph B["Mac B"]
        B_GIT["Git 작업 사본"]
        B_CTX["ctx 사이드카"]
        B_APP["Codex"]
        B_GIT -->|"현재 코드 상태"| B_APP
        B_CTX -->|"resume 컨텍스트"| B_APP
    end

    A_GIT -->|"commit·push 등 기존 Git 흐름"| GIT_REMOTE
    GIT_REMOTE -->|"clone·fetch·pull 등"| B_GIT
    A_CTX -->|"체크포인트 동기화"| CTX_REMOTE
    CTX_REMOTE -->|"resume 전에 가져오기"| B_CTX
```

1. Mac A에서 필요한 코드 상태를 기존 Git 흐름으로 원격에 보낸다.
2. `handoff`로 안정 체크포인트를 만들고 ctx 동기화를 수행한다.
3. Mac B에서 Git 작업 사본을 준비한 뒤 `resume`을 요청한다.
4. ctx는 컨텍스트를 불러오고 현재 Git 상태와 핸드오프 기준점의 차이를 보여 준다.

동기화가 실패하면 ctx는 오래된 로컬 상태를 최신 상태처럼 취급하지 않고, 로컬 자료를 사용 중임을 알려야 한다.

## 시나리오 4. 재개하려는 코드 상태가 체크포인트와 다르다

리베이스, 브랜치 변경, 다른 장치의 미반영 커밋처럼 Git 기준점이 달라질 수 있다. ctx는 이 차이를 숨기거나 자동 수정하지 않는다.

```mermaid
flowchart TD
    START["resume 요청"] --> SELECT["작업과 안정 체크포인트 선택"]
    SELECT --> COMPARE["체크포인트의 Git 기준점과<br/>현재 작업 사본 비교"]
    COMPARE --> MATCH{"브랜치·HEAD·작업 트리가<br/>기대 상태와 일치하는가?"}
    MATCH -->|"예"| LOAD["재개 컨텍스트를 앱에 주입"]
    LOAD --> CONTINUE["개발 계속"]
    MATCH -->|"아니요"| REPORT["차이와 위험을 사용자에게 표시"]
    REPORT --> DECIDE{"사용자가 진행 방식을 결정"}
    DECIDE -->|"Git 상태를 먼저 정리"| COMPARE
    DECIDE -->|"차이를 인지하고 진행"| LOAD
```

- ctx는 자동으로 브랜치를 전환하거나 패치를 적용하거나 코드를 병합하지 않는다.
- 사용자는 Git 상태를 맞춘 뒤 다시 재개하거나, 차이를 인지한 상태에서 현재 코드 기준으로 계속할 수 있다.
- 작업 트리 지문은 상태 비교용이며 미커밋 코드의 백업이 아니다.

## 시나리오 5. 두 장치나 에이전트에서 동시에 진행한다

같은 체크포인트에서 각각 작업하면 체크포인트 이력이 분기된다. ctx는 한쪽을 마지막 쓰기로 덮어쓰지 않는다.

```mermaid
flowchart LR
    BASE["공통 체크포인트 C0"]
    BASE --> A1["Mac A 또는 Agent A<br/>체크포인트 C1"]
    BASE --> B1["Mac B 또는 Agent B<br/>체크포인트 C2"]
    A1 --> SYNC["동기화 후 여러 헤드 감지"]
    B1 --> SYNC
    SYNC --> CHOOSE{"사용자 결정"}
    CHOOSE -->|"한쪽에서 계속"| SELECTED["선택한 헤드 재개"]
    CHOOSE -->|"두 흐름 통합"| MERGE["C1과 C2를 부모로 갖는<br/>merge 체크포인트 생성"]
    MERGE --> NEXT["통합된 다음 작업"]
```

여기서 merge 체크포인트는 **컨텍스트 계보의 통합**이다. 실제 코드 브랜치의 병합은 사용자가 기존 Git 도구로 별도로 수행한다.

## 시나리오 6. 앱이 예기치 않게 종료된다

제품 스킬이나 지원되는 앱 훅이 `ctx snapshot`을 호출해 저장한 런타임 스냅숏은 마지막으로 관측한 기계 상태를 알려 준다. 그러나 의미 체크포인트를 대신하지는 않는다.

```mermaid
sequenceDiagram
    participant App as 에이전트 앱
    participant Ctx as ctx
    participant Git as Git 작업 사본
    participant Store as 사이드카 저장소

    App->>Ctx: 제품 스킬 또는 수명 주기 훅이 snapshot 호출
    Ctx->>Git: HEAD·작업 트리·진행 중 작업 관측
    Ctx->>Store: 런타임 스냅숏 추가
    Note over App: 예기치 않은 종료
    App->>Ctx: 다음 실행에서 resume
    Ctx->>Store: 마지막 안정 체크포인트와 보조 스냅숏 조회
    Ctx->>Git: 현재 상태 재관측 및 비교
    Ctx-->>App: 의미 컨텍스트와 복구에 필요한 상태 차이
```

- 마지막 안정 체크포인트 이후의 대화 의미는 자동 스냅숏만으로 완전히 복원되지 않을 수 있다.
- 런타임 스냅숏과 선택적 세션 로그는 근거 확인과 복구를 돕는 자료다.
- 독립적인 재개의 정본은 항상 자체 완결형 체크포인트다.

## 사용자 의도별 진입점

아래 이름은 MVP에서 목표로 하는 공통 사용자 의도다. 실제 스킬 이름과 설치 방식은 플랫폼 구현에서 확정한다.

| 사용자의 말 | 의도 | 목표 CLI 동작 |
|---|---|---|
| “새 작업을 시작해 줘.” | 새 작업 생성 | `ctx task create` |
| “여기까지 저장해 둬.” | 의미 체크포인트 생성 | `ctx checkpoint` |
| “Codex에서 이어가게 넘겨 줘.” | 안정 핸드오프 생성 | `ctx handoff` |
| “최근 작업을 이어서 해 줘.” | 작업 재개 | `ctx resume` |
| “현재 ctx 상태를 보여 줘.” | 작업·Git·동기화 상태 확인 | `ctx status` |

## 사용자가 기대해도 되는 것과 아닌 것

| 기대해도 되는 것 | 기대하면 안 되는 것 |
|---|---|
| 목표, 결정, 진행 상황, 다음 행동의 구조화된 전달 | 원본 대화 전체의 재현 |
| 체크포인트 시점과 현재 Git 상태의 차이 확인 | 미커밋 코드의 자동 백업과 장치 간 전송 |
| 여러 체크포인트 헤드의 보존과 명시적 선택 | 충돌 시 임의의 최신 기록 자동 선택 |
| 앱 내부에서 시작·저장·핸드오프·재개 | 앱 실행, 자동 브랜치 전환, 패치 적용, 코드 병합 |

## 관련 규격

- [프로젝트 개요와 MVP 범위](../README.md)
- [ctx v1 데이터 계약](../schemas/v1/README.md)
- [handoff Markdown v1](../schemas/v1/handoff-rendering.md)
- [Git 작업 트리 지문 v1](../schemas/v1/worktree-fingerprint.md)
