# ctx handoff Markdown v1

`ctx-handoff-markdown-v1`은 안정 체크포인트를 선택하기 위한 얇은 `handoff.md` 형식이다. 의미 컨텍스트는 체크포인트 JSON에만 저장하고, 본문에는 작업 ID와 체크포인트 ID를 이용한 고정 안내문만 둔다.

## 입력 조건

렌더러는 다음 조건을 모두 만족하는 핸드오프와 체크포인트만 입력으로 받는다.

- 체크포인트의 `purpose`가 `handoff`다.
- 체크포인트의 `stability`가 `stable`이다.
- 체크포인트의 `capture.completeness`가 `complete`다.
- 두 레코드의 `task_id`가 같다.
- 핸드오프의 `checkpoint_digest`가 체크포인트의 `content_digest`와 같다.

## 파일 직렬화

파일은 다음 바이트 구조를 따른다.

```text
---\n
<YAML 머리말>
---\n
\n
# ctx handoff\n
\n
Load checkpoint `<checkpoint_id>` for task `<task_id>` through `ctx resume`.\n
```

YAML 머리말은 `handoff.schema.json` 레코드와 같은 값을 담으며 다음 규칙으로 직렬화한다.

1. YAML 1.2의 JSON 호환 부분집합만 사용하고 태그, 앵커, 별칭과 주석은 사용하지 않는다.
2. 키는 `handoff.schema.json`의 `properties` 순서로 출력하고 선택 필드가 없으면 해당 키를 생략한다.
3. 중첩된 `producer`와 `target`의 키도 각 공통 스키마의 `properties` 순서를 따른다.
4. 모든 문자열은 JSON 방식으로 이스케이프한 큰따옴표 문자열로 출력한다.
5. 정수와 빈 객체는 각각 `1`과 `{}`처럼 JSON 표기로 출력한다. 비어 있지 않은 `extensions` 값은 RFC 8785 JCS로 정규화한 JSON을 YAML 값으로 사용한다.
6. 들여쓰기는 공백 두 칸이며 탭과 줄 끝 공백을 사용하지 않는다.
7. 모든 줄바꿈은 LF이고 파일은 마지막 LF 하나로 끝난다.

예시는 다음과 같다.

```markdown
---
schema_version: 1
record_type: "ctx.handoff"
handoff_id: "01ARZ3NDEKTSV4RRFFQ69G5FAY"
task_id: "01ARZ3NDEKTSV4RRFFQ69G5FAV"
checkpoint_id: "01ARZ3NDEKTSV4RRFFQ69G5FAW"
checkpoint_digest: "sha256:8699fb26c336cfe660051b38c50902ca4668b6d1f450a9b4860740b92d59b37b"
generated_at: "2026-08-03T06:06:00Z"
producer:
  actor_type: "cli"
  system: "ctx.cli"
  version: "0.1.0"
  device_id: "personal-macbook"
  extensions: {}
target:
  system: "com.openai.codex"
  interface: "desktop"
render_profile: "ctx-handoff-markdown-v1"
rendered_body_digest: "sha256:eb53a863c21b0edc4392a2090bf16a2990c22d0ada44155813f1de484fdfd528"
extensions: {}
---

# ctx handoff

Load checkpoint `01ARZ3NDEKTSV4RRFFQ69G5FAW` for task `01ARZ3NDEKTSV4RRFFQ69G5FAV` through `ctx resume`.
```

## 본문 해시와 검증

본문은 두 번째 `---` 뒤의 빈 구분 줄을 제외하고 `# ctx handoff`의 `#`부터 파일의 마지막 LF까지다. `rendered_body_digest`는 이 본문의 UTF-8 바이트에 대한 SHA-256이다.

`ctx`는 다음 순서로 핸드오프를 검증한다.

1. 머리말을 JSON 값으로 변환해 `handoff.schema.json`으로 검증한다.
2. `checkpoint_id`로 체크포인트를 찾고 작업 ID와 `checkpoint_digest`를 비교한다.
3. 두 ID로 고정 본문을 다시 만들고 `rendered_body_digest`와 비교한다.

어느 단계든 일치하지 않으면 본문을 재개의 입력으로 사용하지 않는다. 유효한 체크포인트가 있으면 핸드오프를 다시 생성하고, 실제 재개 컨텍스트는 체크포인트에서 만든다.
