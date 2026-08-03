# ctx Git 작업 트리 지문 v1

`ctx-git-worktree-v1`은 서로 다른 장치에서 체크포인트의 Git 기준점과 현재 작업 사본이 같은지를 비교하기 위한 결정론적 지문이다. 코드나 패치를 보존하는 형식은 아니며, 같은 Git 상태를 관측한 구현이 같은 SHA-256 값을 만들도록 입력과 직렬화를 고정한다.

## 수집 범위

v1 수집기는 다음 상태를 포함한다.

- 전체 HEAD 상태와 Git 객체 형식
- 진행 중인 merge, rebase, cherry-pick 등의 작업
- HEAD와 다른 index 엔트리 및 conflict stage
- index와 다른 작업 트리 파일
- 무시되지 않은 모든 untracked 파일
- submodule의 현재 HEAD와 tracked 및 untracked dirty 여부

ignored 파일, upstream, ahead 및 behind 수치, stash, reflog, 절대 경로, 장치 ID와 관측 시각은 포함하지 않는다. untracked 디렉터리는 파일 단위로 펼치며 Git이 추적하지 않는 빈 디렉터리는 포함하지 않는다.

ctx가 `clean` 또는 `dirty` 상태와 이 프로필의 지문을 기록하려면 변경 목록의 `complete`와 `untracked_included`의 값이 `true`이고 `ignored_included`의 값이 `false`여야 한다. 수집 중 저장소 상태가 바뀌거나 파일 종류를 처리할 수 없으면 불완전한 지문을 만들지 않고 작업 트리 상태를 `unknown`으로 기록한다.

## Git 관측 규칙

1. rename과 copy 감지는 끈 상태로 porcelain v2에 해당하는 상태를 수집한다. 따라서 이름 변경은 지문 입력에서 삭제와 추가 엔트리로 표현한다.
2. index 엔트리는 Git index의 stage, mode와 전체 object ID를 사용한다. 충돌하지 않은 엔트리는 stage `0`, 충돌 엔트리는 존재하는 stage `1`부터 `3`까지를 모두 사용한다.
3. 작업 트리의 regular file과 symbolic link는 해당 저장소의 객체 형식으로 계산한 Git blob object ID와 canonical Git mode를 사용한다. clean filter가 필요한 경로는 저장소 속성을 적용한 `git hash-object --path`와 같은 결과를 사용한다.
4. submodule은 mode `160000`, 현재 HEAD의 전체 object ID 또는 `null`, tracked dirty와 untracked dirty 여부를 사용한다.
5. 삭제된 경로는 `worktree.state: missing`, index와 같은 작업 트리 내용은 `worktree.state: matches-index`로 표현한다.
6. Git이 처리할 수 없는 특수 파일이나 읽는 동안 내용이 바뀐 파일을 만나면 수집에 실패한다.

## 경로 표현과 정렬

Git이 반환한 NUL 구분 원시 경로 바이트를 수정하거나 Unicode로 정규화하지 않는다. 지문 페이로드에서는 이 바이트를 패딩 없는 base64url 문자열인 `path_b64`로 표현한다. 엔트리는 디코딩한 원시 경로 바이트의 오름차순으로 정렬하고, 같은 경로의 index 엔트리는 stage 오름차순으로 정렬한다.

런타임 스냅숏의 사람이 읽는 `changes.entries[].path`는 정규형 POSIX 상대 경로다. 지문 계산은 이 JSON 문자열을 다시 인코딩하지 않고 Git에서 얻은 원시 경로 바이트를 사용한다.

## 정규 페이로드

해시 입력은 다음 구조의 JSON 객체다. 선택 필드를 생략하는 대신 아래 판별형 가운데 하나를 사용한다.

```json
{
  "profile": "ctx-git-worktree-v1",
  "object_format": "sha1",
  "head": {
    "state": "attached",
    "symbolic_ref": "refs/heads/main",
    "oid": "0123456789abcdef0123456789abcdef01234567"
  },
  "operation": {
    "kind": "none"
  },
  "entries": [
    {
      "path_b64": "UkVBRE1FLm1k",
      "index_status": "unmodified",
      "worktree_status": "modified",
      "conflict": false,
      "index_entries": [
        {
          "stage": 0,
          "mode": "100644",
          "oid": "89abcdef0123456789abcdef0123456789abcdef"
        }
      ],
      "worktree": {
        "state": "present",
        "mode": "100644",
        "object_kind": "blob",
        "oid": "fedcba9876543210fedcba9876543210fedcba98"
      }
    }
  ]
}
```

`head`는 `common.schema.json`의 같은 이름 정의를 사용한다. `operation`에는 `kind`만 넣으며, 사람이 읽는 `detail`은 구현마다 달라질 수 있으므로 지문에서 제외한다. 각 변경 엔트리는 다음 규칙을 따른다.

- `path_b64`, `index_status`, `worktree_status`, `conflict`, `index_entries`, `worktree`를 항상 포함한다.
- `index_entries`는 index에 경로가 없으면 빈 배열이다.
- `stage`는 `0`부터 `3`까지의 정수다. `mode`는 `100644`, `100755`, `120000`, `160000` 가운데 하나다.
- blob의 `oid` 길이는 `object_format`이 `sha1`이면 40자, `sha256`이면 64자다.
- `worktree`는 다음 네 형태 가운데 하나다.

```json
{"state":"matches-index"}
```

```json
{"state":"missing"}
```

```json
{"state":"present","mode":"100644","object_kind":"blob","oid":"<full-object-id>"}
```

```json
{"state":"present","mode":"160000","object_kind":"gitlink","oid":"<full-object-id-or-null>","tracked_dirty":false,"untracked_dirty":false}
```

## 해시 계산

1. 위 페이로드를 RFC 8785 JCS로 정규화한다.
2. 결과의 UTF-8 바이트에 대한 SHA-256을 계산한다.
3. 소문자 16진수 앞에 `sha256:`을 붙여 `worktree.fingerprint.digest`에 저장한다.

페이로드의 `entries`가 비어 있으면 작업 트리 상태는 `clean`이다. 하나 이상이면 `dirty`다. 진행 중인 Git 작업은 `operation`에 별도로 남으므로, 변경 엔트리가 없는 merge 상태도 작업 트리 자체는 `clean`일 수 있다.

## 일관된 관측

수집기는 최소한 HEAD와 index 식별값을 파일 내용 수집 전후에 비교한다. 둘 중 하나가 바뀌거나 수집한 파일의 stat 정보가 읽기 전후에 달라지면 결과를 폐기하고 제한된 횟수만큼 다시 시도한다. 반복해도 안정된 관측을 얻지 못하면 `state: unknown`, 불완전한 변경 목록과 진단 메시지를 기록한다.
