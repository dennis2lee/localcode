# 사용 방법

## 실행

```bash
localcode --agent general-purpose
```

| 플래그 | 기본값 | 설명 |
|---|---|---|
| `--config <path>` | (없음) | 이 경로 하나만 config로 사용. 지정하지 않으면 `~/.localcode/config.json` + `./.localcode/config.json`(프로젝트 override)을 병합 |
| `--agent <name>` | `general-purpose` | config의 `agents` 맵에서 어떤 모델 프로필을 쓸지 선택 |

`localcode version` — 빌드된 버전 문자열만 출력하고 종료합니다.

## 설정 파일 (config.json)

`~/.localcode/config.json` (전역) 또는 `<프로젝트>/.localcode/config.json` (프로젝트 override, 있으면 전역보다 우선).

```json
{
  "providers": {
    "bedrock": { "type": "bedrock", "region": "us-west-2" },
    "local":   { "type": "openai-compat", "base_url": "http://localhost:1234/v1" }
  },
  "profiles": {
    "strong":   { "provider": "bedrock", "model": "us.anthropic.claude-opus-4-5-20251101-v1:0", "max_tokens": 8192 },
    "balanced": { "provider": "bedrock", "model": "us.anthropic.claude-sonnet-4-5-20250929-v1:0", "max_tokens": 8192 },
    "cheap":    { "provider": "local", "model": "qwen3-30b-a3b", "max_tokens": 4096 }
  },
  "agents": {
    "general-purpose": { "profile": "balanced" },
    "explore":          { "profile": "cheap" }
  },
  "default_profile": "balanced",
  "max_concurrent_tasks": 5
}
```

### 필드 설명

- **providers**: 모델 백엔드 연결 정보. `type`은 `bedrock` 또는 `openai-compat`.
  - `bedrock.region`: AWS 리전 (예: `us-west-2`). 인증은 별도 설정 없이 AWS 기본 자격 증명 체인을 사용합니다.
  - `openai-compat.base_url`: `/chat/completions` 앞부분 URL. LM Studio, vLLM 등 OpenAI 호환 서버 주소.
  - `openai-compat.api_key`: 필요하면 지정 (로컬 서버는 보통 불필요).
- **profiles**: 실제로 쓸 provider+model 조합에 이름을 붙인 것. `max_tokens`, `temperature` 선택적으로 지정.
- **agents**: 에이전트/작업 종류 이름 → 프로필 매핑. `--agent` 플래그로 선택한 이름이 여기서 풀립니다. 없는 이름이면 `default_profile`로 대체됩니다.
- **max_concurrent_tasks**: 향후 백그라운드 에이전트 동시 실행 제한용 (현재 MVP에는 아직 미적용).

설정이 틀리면(예: 존재하지 않는 provider를 가리키는 profile) 실행 시작 시점에 바로 에러를 내고 종료합니다.

## 화면 조작

- 하단 입력창에 메시지를 입력하고 **Enter**로 전송
- 모델이 파일 쓰기(`write_file`)/수정(`edit`)/셸 실행(`bash`)을 요청하면 화면에 **권한 확인 모달**이 뜹니다 — `y`로 승인, `n`으로 거부
- **Ctrl+C**로 종료

## 사용 가능한 툴 (모델이 호출)

| 툴 | 권한 필요 | 설명 |
|---|---|---|
| `read_file` | 아니오 | 파일 내용을 줄 번호와 함께 읽기 |
| `glob` | 아니오 | 패턴(`**` 포함)으로 파일 목록 검색 |
| `grep` | 아니오 | 정규식으로 파일 내용 검색 |
| `write_file` | 예 | 파일 생성/덮어쓰기 |
| `edit` | 예 | 파일 내 특정 문자열을 다른 문자열로 치환 |
| `bash` | 예 | 셸 명령 실행 (기본 타임아웃 2분) |

## 다른 모델로 전환하기

같은 대화 안에서 모델을 바꾸는 기능은 아직 없습니다. 대신 config의 `agents` 맵에 새 이름을 추가하고, `--agent <이름>`으로 재시작하면 됩니다.

```json
"agents": {
  "quick-search": { "profile": "cheap" }
}
```

```bash
localcode --agent quick-search
```

## 로컬 LLM (LM Studio 등) 붙이기

1. LM Studio에서 모델을 로드하고 로컬 서버를 켭니다 (기본 `http://localhost:1234/v1`).
2. config의 `providers.local.base_url`을 그 주소로 맞춥니다.
3. `profiles`에 `model`을 LM Studio에 로드된 모델 이름과 정확히 일치시킵니다.
4. `agents`에서 그 프로필을 가리키도록 설정하고 `--agent`로 실행합니다.

## 세션 로그

세션 이벤트는 `~/.localcode/sessions/<session-id>.jsonl`에 append-only로 저장됩니다. 디버깅이나 재생(replay)에 사용할 수 있습니다.

## 알려진 제약 (MVP)

- 단일 프로세스: TUI가 에이전트 루프를 in-process로 직접 호출합니다. 코어 데몬/HTTP-SSE 분리, Web UI는 아직 없습니다.
- MCP, Skills, 백그라운드 다중 에이전트 실행/관리 기능은 아직 없습니다.
- 대화 히스토리는 프로세스 메모리에만 있고, 세션 로그 파일로부터 자동 복원되지는 않습니다 (재생 로직은 `internal/session.LoadFromDisk`에 있지만 `main.go`에서 아직 연결하지 않았습니다).
