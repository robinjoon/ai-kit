# ctx v1 스키마

이 디렉터리는 에이전트 제품과 실행 환경에 독립적인 ctx v1 저장 규격과 입력 계약을 정의한다. JSON 체크포인트가 재개의 정본이며 Markdown 핸드오프는 체크포인트를 선택하는 얇은 포인터다.

스키마는 [JSON Schema Draft 2020-12](https://json-schema.org/draft/2020-12)를 사용한다. `$defs`와 `$ref`로 공통 정의를 재사용하며, `format` 검증을 활성화한 검증기를 사용해야 한다. 콘텐츠 해시는 [RFC 8785 JSON Canonicalization Scheme](https://www.rfc-editor.org/rfc/rfc8785.html)을 사용한다.

## 파일

| 파일 | 역할 |
|---|---|
| `common.schema.json` | ID, 생성 주체, 경로, 세션 로그, Git 상태, 의미 컨텍스트의 공통 정의 |
| `capture-input.schema.json` | 스킬이 `checkpoint`와 `handoff` 명령에 전달하는 임시 의미 입력 |
| `runtime-snapshot.schema.json` | 자동으로 수집한 단일 작업 사본의 기계적 상태 |
| `checkpoint.schema.json` | 에이전트가 독립적으로 재개할 수 있는 자체 완결형 의미 상태 |
| `handoff.schema.json` | 안정 체크포인트를 가리키는 얇은 핸드오프 머리말 |
| `handoff-rendering.md` | 얇은 포인터를 `handoff.md`로 직렬화하는 규칙 |
| `worktree-fingerprint.md` | Git 작업 트리 지문의 입력과 해시 계산 규칙 |
| `examples/` | 각 스키마의 유효 예제 |

의존 방향은 다음과 같이 단방향이다.

```text
capture-input ─────┐
runtime-snapshot ──┤
checkpoint ────────┼─> common
handoff ───────────┘
```

핸드오프의 `checkpoint_id`는 JSON Schema의 `$ref`가 아니라 레코드 사이의 데이터 참조다.

## 레코드 경계

### 런타임 스냅숏

런타임 스냅숏은 Git과 세션의 기계적 관측값이다. 개별 레코드는 생성 후 수정하지 않지만 장기 보존이나 동기화를 전제로 하지 않는다.

- 하나의 장치와 작업 사본을 나타낸다.
- 연결된 HEAD, 분리된 HEAD, 아직 커밋이 없는 HEAD를 구분한다.
- staged, unstaged, untracked, 충돌 상태를 정규화한 변경 목록으로 기록한다.
- `session_refs`로 제품별 세션과 로그 위치를 선택적으로 참조한다.
- 의미 요약, 결정, 다음 행동은 포함하지 않는다.

### 체크포인트

체크포인트는 재개의 정본이다. 부모, 런타임 스냅숏, 원본 로그를 읽지 않아도 현재 상태를 이해할 수 있는 자체 완결된 전체 상태를 담는다.

- `parent_ids`는 이력과 분기 추적에만 사용한다.
- `purpose`는 생성 이유, `stability`는 재개 대상으로 사용할 수 있는지, `work_status`는 작업의 진행 상태를 나타낸다.
- 목표, 성공 기준, 범위, 제약, 가정, 확인된 사실, 결정과 근거, 진행 상황, 다음 행동, 차단 요소, 질문, 검증, 관련 자료를 구조화한다.
- `context_digest`는 의미 컨텍스트만 비교하거나 중복을 판정할 수 있도록 `context` 객체를 JCS로 정규화한 해시다.
- 각 저장소의 HEAD, 진행 중인 Git 작업, dirty 상태와 지문을 자체적으로 보관한다.
- 상세 변경 목록은 런타임 스냅숏에 두고, 체크포인트에는 의미 있는 파일만 `relevant_resources`로 남긴다.

`capture.completeness`가 `complete`일 때 빈 배열은 해당 항목을 검토했지만 내용이 없다는 뜻이다. 확인하지 못한 영역이 있으면 `partial`과 `warnings`, `omitted_sections`를 사용한다.

### 핸드오프

핸드오프는 의미 정보를 새로 저장하지 않는다. 작업과 체크포인트를 식별하는 ID 및 해시, 생성 정보, 선택적 대상, 렌더링 프로필과 렌더링 결과 해시만 보관한다. `handoff.md`의 본문은 두 ID를 넣은 고정 안내문이며 본문에만 존재하는 정보가 없다. `resume`은 본문이 아니라 참조된 체크포인트를 읽어 에이전트용 컨텍스트를 만든다.

## 저장 레코드와 에이전트 입력

이 디렉터리의 세 레코드 스키마는 디스크와 교환 번들에 보존하는 결과 형식이다. `capture-input.schema.json`은 저장 레코드가 아니라 스킬과 CLI 사이에서만 사용하는 임시 요청 계약이다.

캡처 입력의 최상위 필드는 다음 네 개뿐이다.

| 필드 | 책임 |
|---|---|
| `input_version` | 저장 레코드와 별도로 관리하는 입력 계약 버전 |
| `work_status` | 에이전트가 대화에서 판단한 작업 진행 상태 |
| `capture` | 의미 컨텍스트의 검토 완전성과 생략 영역 |
| `context` | 저장 체크포인트와 동일한 자체 완결형 의미 컨텍스트 |

입력에는 `task_id`, `checkpoint_id`, `parent_ids`, `created_at`, `purpose`, `stability`, `producer`, `session_refs`, `workspace`, 해시와 대상 정보를 넣지 않는다. `purpose`와 대상은 명령 옵션이 정하고, CLI가 활성 작업, 현재 세션, Git 관측값을 이용해 나머지 필드를 채운다. `handoff`는 최종 캡처가 완전할 때만 `purpose: handoff`, `stability: stable` 체크포인트와 얇은 포인터를 한 트랜잭션에서 만든다.

| 최종 체크포인트 영역 | 출처 |
|---|---|
| `work_status`, `capture`, `context` | 검증된 캡처 입력 |
| `purpose`, 핸드오프 대상 | 명령 종류와 명령 옵션 |
| `stability` | 최종 캡처가 `complete`이면 `stable`, `partial`이면 `draft` |
| 작업 및 체크포인트 ID, 부모, 생성 시각 | 활성 작업과 CLI 생성기 |
| `producer`, `session_refs` | `--client`와 로컬 세션 바인딩 |
| `workspace` | Git 어댑터의 현재 관측값 |
| `context_digest`, `content_digest` | CLI의 JCS 및 SHA-256 계산 |

입력의 `capture.omitted_sections`에는 `context` 아래의 허용된 섹션만 기록한다. Git이나 세션처럼 CLI가 수집하는 영역의 누락은 입력에 넣지 않는다. 입력이 `complete`여도 기계적 관측에 실패하면 일반 체크포인트의 최종 캡처는 `partial`이 될 수 있으며, 이때 `stability: draft`로 저장한다. 핸드오프는 생성하지 않고 오류를 반환한다. v1에는 이 안정성 결정을 덮어쓰는 옵션이 없다. 유효한 전체 예제는 [`capture-input.example.json`](examples/capture-input.example.json)에 있다.

`decision_id`, `action_id`, `blocker_id`, `validation_id`는 전역 시스템 ID가 아니라 체크포인트 안의 의미 항목을 가리키는 참조다. 스킬은 `decision-use-go`, `action-run-tests`처럼 사람이 읽을 수 있는 `referenceId`를 만들고, 기존 항목을 다음 체크포인트에 옮길 때 같은 값을 유지한다. 작업, 체크포인트, 스냅숏과 핸드오프 레코드 자체의 ID에만 ULID를 사용한다.

스킬은 캡처 전에 `ctx resolve --cwd ... --client ... --json`을 호출한다. `validations[].cwd.repo_id`와 `relevant_resources[].locator.repo_id`는 이 결과의 저장소 ID를 그대로 사용하며, 에이전트가 경로나 원격 URL에서 ID를 만들지 않는다. CLI는 입력의 저장소 ID가 `resolve` 결과에 없으면 추측하지 않고 거부한다.

## 에이전트와 로그 참조

`producer.system`과 `session_refs[].system`은 닫힌 열거형이 아닌 개방형 식별자다. 예시는 `com.anthropic.claude-code`와 `com.openai.codex`지만 코어 규격은 이 값들을 특별하게 해석하지 않는다.

로그 위치는 `session_refs[].logs[].locator`에 기록한다.

| locator 종류 | 용도 |
|---|---|
| `repo_path` | Git 저장소 안의 상대 경로 |
| `store_path` | ctx 저장소 루트 기준 상대 경로 |
| `home_path` | 특정 장치의 사용자 홈 기준 상대 경로 |
| `uri` | 파일 URI, 원격 URI 또는 어댑터가 이해하는 URI |

장치 로컬 로그에는 `home_path` 또는 `scope: device`인 URI와 `device_id`를 사용한다. `file:` URI는 항상 장치 범위이며, `scope: portable`인 URI에는 `device_id`를 넣지 않는다. 각 로그에는 체크포인트 안에서 유일한 `log_ref_id`가 있다. `adapter`와 `adapter_version`은 로그 형식을 해석할 구현 및 규격 버전을 지정하고, `selector`는 메시지 ID, 행, 바이트, 시간 구간 또는 제품별 불투명 선택자를 나타낸다.

로그와 세션 참조는 근거 확인과 복구를 위한 선택 자료다. 체크포인트의 목표, 결정, 진행 상태나 Git 기준점을 로그 참조로 대체해서는 안 된다.

## 해시

`context_digest`는 `context` 객체 자체를 RFC 8785 JCS로 정규화한 UTF-8 바이트의 SHA-256이다.

JCS는 객체 키 순서를 정규화하지만 배열 순서는 보존한다. 따라서 결정, 진행 항목, 다음 행동과 관련 자료의 배열 순서는 의미 있는 표시 및 실행 순서로 취급하며, 순서가 바뀌면 `context_digest`도 달라진다. `producer`, `session_refs`, `capture`와 `workspace`는 `context_digest`에 포함하지 않는다.

`content_digest`는 다음 순서로 계산한다.

1. 레코드에서 `content_digest` 필드를 제거한다.
2. 남은 JSON을 RFC 8785 JCS로 정규화한다.
3. UTF-8 바이트의 SHA-256을 계산한다.
4. 소문자 16진수 앞에 `sha256:`을 붙인다.

`checkpoint_digest`는 참조한 체크포인트의 `content_digest`와 같아야 한다.

`ctx-git-worktree-v1`은 HEAD, 진행 중인 Git 작업, index와 작업 트리 내용을 정규 페이로드로 만든 뒤 JCS와 SHA-256을 적용한다. 원시 Git 경로 바이트, 파일 모드, conflict stage, symbolic link와 submodule 처리까지 [작업 트리 지문 규격](worktree-fingerprint.md)에 정의한다.

`rendered_body_digest`는 [handoff Markdown v1](handoff-rendering.md)이 만든 본문 바이트를 변환하지 않고 그대로 계산한 SHA-256이다. 렌더러가 LF와 마지막 LF 하나를 보장한다.

## 중복 체크포인트 판정

CLI는 다음 값으로 임시 중복 판정 객체를 만들고 JCS로 정규화한다.

- `schema_version`, `task_id`, `purpose`, `stability`, `work_status`, `capture`
- 바이트 오름차순으로 정렬한 `parent_ids`
- 저장된 `context_digest`
- `repo_id`로 정렬한 각 저장소의 `repo_id`, `object_format`, `head`, `operation`, `worktree`

`checkpoint_id`, `created_at`, `producer`, `session_refs`, `working_copy_id`, `observed_at`, `runtime_snapshot_id`, `content_digest`는 제외한다. 같은 작업의 잠금을 보유한 상태에서 이 객체의 SHA-256이 같은 체크포인트를 발견하면 기존 체크포인트를 반환한다. 이 해시는 저장 레코드 필드가 아니라 원자적 생성의 멱등성 키다.

## JSON Schema 밖의 불변식

다음 규칙은 `ctx` 의미 검증기가 확인한다.

- 모든 부모 체크포인트는 같은 `task_id`에 속하며 그래프는 순환하지 않는다.
- 일반 체크포인트는 부모가 최대 하나이고 `purpose: merge`만 부모를 두 개 이상 가진다.
- 여러 헤드 중 하나를 시각이나 ULID만으로 자동 선택하지 않는다.
- `workspace.primary_repo_id`는 `workspace.repositories`에 존재한다.
- 모든 `evidence_refs`와 `resource_refs`는 같은 체크포인트 안의 참조 ID로 해석된다.
- 캡처 입력의 의미 참조 ID는 체크포인트에 그대로 보존하며, 동일한 기존 항목에는 이전 체크포인트와 같은 ID를 사용한다.
- `session_ref_id`, `log_ref_id`와 `resource_id`는 체크포인트 전체에서 하나의 증거 참조 네임스페이스를 공유하며 서로 중복되지 않는다. `evidence_refs[].ref_id`는 이 네임스페이스에서 해석된다. 세션 전체를 참조할 때는 selector를 쓰지 않고, 특정 로그나 범위가 필요하면 `log_ref_id`와 선택적 selector를 사용한다.
- 자료의 `selection` 또는 로그의 `selector`와 `evidence_refs[].selector`는 모두 원본 locator를 기준으로 한 절대 범위다. 둘을 함께 쓰면 종류가 같아야 하고 증거의 `selector`가 상위 범위의 부분집합이어야 한다. 행, 바이트와 시간 범위는 양 끝이 상위 범위 안에 있어야 하며, 메시지 ID는 상위 집합의 부분집합이어야 한다. `opaque` selector는 두 값이 완전히 같을 때만 함께 쓸 수 있다. 증거의 `selector`를 생략하면 상위 범위 전체를 참조한다.
- `resource_refs`와 `output_resource_ref`는 `relevant_resources[].resource_id`로, `dependencies`는 `next_actions[].action_id`로, `supersedes`는 `decisions[].decision_id`로 해석된다. 각 ID는 해당 배열에서 유일하다.
- `blocker_id`와 `validation_id`도 각 배열에서 유일하며, `workspace.repositories[].repo_id`는 중복되지 않는다.
- 다음 행동의 의존 그래프, 결정의 대체 그래프와 체크포인트 부모 그래프는 순환하지 않는다.
- 검증 항목의 작업 디렉터리와 모든 저장소 경로 locator의 `repo_id`는 `workspace.repositories`에 존재한다.
- `work_status: blocked`이면 활성 상태인 차단 요소가 하나 이상이고, `work_status: completed`이면 현재 진행 항목과 활성 상태인 차단 요소가 없다.
- 완전한 변경 목록의 `total_entries`는 `entries`의 길이와 같고 각 `path`는 유일하다. `conflict: true`와 `unmerged` 상태는 서로 일치한다.
- `clean`은 ignored 파일을 제외한 tracked 및 untracked 변경이 없다는 뜻이다. v1 지문이 있는 `clean`과 `dirty` 상태는 untracked 파일을 모두 포함한다.
- `previous_snapshot_id`는 같은 장치와 작업 사본에서 먼저 생성한 스냅숏을 가리킨다. `active_checkpoint_id`는 `task_id`와 같은 작업에 속한다.
- 런타임 스냅숏의 `producer.device_id`가 있으면 최상위 `device_id`와 같다.
- 핸드오프가 가리키는 체크포인트는 존재하며 같은 작업에 속한다.
- 핸드오프 체크포인트는 `purpose: handoff`, `stability: stable`, `capture.completeness: complete`다.
- 모든 콘텐츠 해시와 작업 트리 지문이 실제 데이터와 일치한다.
- 범위 선택자의 끝은 시작보다 작지 않다.
- 로컬 로그나 런타임 스냅숏이 없어도 체크포인트만으로 작업을 재개할 수 있다.

## 확장 규칙

코어 객체는 `additionalProperties: false`로 닫는다. 공급자별 추가 데이터는 역도메인 형식의 키를 사용하는 `extensions`에만 둔다.

```json
{
  "extensions": {
    "com.openai.codex": {
      "provider_specific_value": "example"
    }
  }
}
```

코어가 이해하지 못하는 확장 데이터는 보존할 수 있지만 재개 판단의 필수 정보로 사용해서는 안 된다.

## 검증

다음 명령은 스키마 메타 검증, 유효 예제 검증, 대표적인 무효 사례의 거부 여부를 확인한다.

```bash
uv run --with jsonschema --with rfc8785 python scripts/validate-schemas.py
```

검증기는 외부 네트워크에서 `$id`를 조회하지 않고 다섯 개의 로컬 스키마를 URN 기준으로 레지스트리에 등록한다. 유효·무효 구조뿐 아니라 예제 레코드의 JCS 콘텐츠 해시와 핸드오프가 참조한 체크포인트 해시도 확인한다. 이 Python 명령은 스키마 원본과 예제를 Go 구현과 독립적으로 교차 검증하는 개발 도구이며 `ctx` 실행 환경의 의존성이 아니다.

## 버전 관리

`schema_version`은 호환되지 않는 저장 레코드 변경의 주 버전이며 v1에서는 정수 `1`로 고정한다. 캡처 입력은 `input_version`으로 별도 버전을 관리한다. 각 계약의 필수 필드나 의미를 바꾸는 변경은 해당 버전을 올린다. 제품별 필드를 저장 레코드의 코어에 추가하는 대신 `extensions`를 사용한다.
