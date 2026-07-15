# 사용 방법

## 실행 모드

```bash
localcode --agent general-purpose
```

| 플래그 | 기본값 | 설명 |
|---|---|---|
| `--config <path>` | (없음) | 이 경로 하나만 config로 사용. 지정하지 않으면 `~/.localcode/config.json` + `./.localcode/config.json`(프로젝트 override)을 병합 |
| `--agent <name>` | `general-purpose` | config의 `agents` 맵에서 어떤 모델 프로필을 쓸지 선택 |
| `--listen <host:port>` | `127.0.0.1:4096` | 데몬이 붙는 주소. Web UI도 이 주소에서 서빙됩니다 |
| `--server <url>` | (없음) | 이 값을 주면 로컬 데몬을 띄우지 않고, 이미 떠 있는(원격일 수도 있는) 데몬에 TUI만 클라이언트로 붙습니다 |
| `--headless` | `false` | TUI 없이 데몬만 실행 (HTTP API + Web UI). 원격 서버에서 이 모드로 띄워두고 SSH 터널로 붙는 용도 |

`localcode version` — 빌드된 버전 문자열만 출력하고 종료합니다.

세 가지 조합:

1. **`localcode`** (기본) — 로컬에 데몬을 띄우고 TUI를 그 클라이언트로 붙임. 동시에 브라우저로 `http://127.0.0.1:4096`을 열면 Web UI로도 같은 세션에 접근 가능 (세션은 각자 새로 생성됨 — 아직 세션 선택 UI는 없습니다).
2. **`localcode --headless --listen 0.0.0.0:4096`** — 데몬만 실행. 원격 서버 배포용.
3. **`localcode --server http://호스트:4096`** — 원격/이미 떠 있는 데몬에 TUI만 연결.

### 원격 데몬 + SSH 터널

```bash
# 사내 리눅스 서버
localcode --headless --listen 127.0.0.1:4096

# 맥북
ssh -L 4096:127.0.0.1:4096 linux-box
localcode --server http://localhost:4096   # 터미널
# 또는 브라우저에서 http://localhost:4096
```

`0.0.0.0` 바인딩은 임의 코드 실행(bash 툴) API를 외부에 노출하는 것과 같으므로, 반드시 loopback + SSH 터널로 접근하세요. 인증 토큰은 아직 없습니다 — 신뢰할 수 없는 네트워크에 직접 바인딩하지 마세요.

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
  "max_concurrent_tasks": 5,
  "mcp_servers": {
    "github": { "command": "npx", "args": ["-y", "@modelcontextprotocol/server-github"], "env": { "GITHUB_TOKEN": "..." } }
  }
}
```

### 필드 설명

- **providers**: 모델 백엔드 연결 정보. `type`은 `bedrock` 또는 `openai-compat`.
  - `bedrock.region`: AWS 리전 (예: `us-west-2`). 인증은 별도 설정 없이 AWS 기본 자격 증명 체인을 사용합니다.
  - `openai-compat.base_url`: `/chat/completions` 앞부분 URL. LM Studio, vLLM 등 OpenAI 호환 서버 주소.
  - `openai-compat.api_key`: 필요하면 지정 (로컬 서버는 보통 불필요).
- **profiles**: 실제로 쓸 provider+model 조합에 이름을 붙인 것. `max_tokens`, `temperature` 선택적으로 지정.
- **agents**: 에이전트/작업 종류 이름 → 프로필 매핑. `--agent` 플래그로 선택한 이름이 여기서 풀립니다. 없는 이름이면 `default_profile`로 대체됩니다.
- **max_concurrent_tasks**: 백그라운드 태스크(아래 참고) 동시 실행 개수 제한.
- **mcp_servers**: Claude Code의 `.mcp.json`과 같은 모양(`command`/`args`/`env`)이라, 기존 항목을 그대로 옮겨 쓸 수 있습니다. 각 서버는 stdio로 붙고, 그 서버의 툴은 `mcp__<서버이름>__<툴이름>`으로 노출됩니다. **MCP 툴은 항상 권한 확인을 거칩니다** — 서버가 자기 툴을 "읽기 전용"이라고 알려와도(annotations) 신뢰하지 않습니다. 서버 연결에 실패하면 데몬이 아예 뜨지 않습니다(현재는 재시도/우아한 실패 처리가 없습니다).

설정이 틀리면(예: 존재하지 않는 provider를 가리키는 profile) 실행 시작 시점에 바로 에러를 내고 종료합니다.

## Skills

`~/.localcode/skills/<이름>/SKILL.md` (전역) 또는 `<프로젝트>/.localcode/skills/<이름>/SKILL.md` (프로젝트, 같은 이름이면 전역보다 우선)에 다음 형식으로 둡니다.

```markdown
---
name: pdf-tools
description: PDF 파일 병합/분할/워터마크 작업
---
# PDF Tools

여기에 실제 지침을 자세히 적습니다. 모델이 `Skill` 툴로
이 이름을 호출하면 이 본문 전체가 그대로 반환됩니다.
```

시작 시 모든 스킬의 `name`/`description`만 시스템 프롬프트에 목록으로 들어가고(스킬당 몇 십 토큰), 본문은 모델이 실제로 `Skill(name)`을 호출할 때만 로드됩니다 — 안 쓰는 스킬은 거의 공짜입니다. 본문 안에서 다른 파일(`scripts/*.py` 등)을 참조하고 싶으면, 모델이 `read_file`/`bash`로 알아서 읽도록 상대 경로로 적으면 됩니다.

## 화면 조작 (TUI / Web UI 공통)

- 메시지를 입력하고 **Enter**로 전송 (Web UI는 전송 버튼도 있음)
- 모델이 파일 쓰기(`write_file`)/수정(`edit`)/셸 실행(`bash`)/MCP 툴 호출을 요청하면 **권한 확인 모달**이 뜹니다 — TUI는 `y`/`n`, Web UI는 승인/거부 버튼
- 권한 요청은 세션에 붙은 아무 클라이언트에서나 응답 가능하고, 응답하면 다른 클라이언트의 모달은 자동으로 닫힙니다
- TUI 종료는 **Ctrl+C**

## 사용 가능한 툴 (모델이 호출)

| 툴 | 권한 필요 | 설명 |
|---|---|---|
| `read_file` | 아니오 | 파일 내용을 줄 번호와 함께 읽기 |
| `glob` | 아니오 | 패턴(`**` 포함)으로 파일 목록 검색 |
| `grep` | 아니오 | 정규식으로 파일 내용 검색 |
| `write_file` | 예 | 파일 생성/덮어쓰기 |
| `edit` | 예 | 파일 내 특정 문자열을 다른 문자열로 치환 |
| `bash` | 예 | 셸 명령 실행 (기본 타임아웃 2분) |
| `Skill` | 아니오 | 이름으로 스킬 본문 전체를 로드 (설정된 스킬이 있을 때만 등록됨) |
| `mcp__<server>__<tool>` | 예 (항상) | 설정된 각 MCP 서버가 제공하는 툴 |

## 백그라운드 태스크

세션 하나(부모)에서 다른 에이전트를 백그라운드로 띄우고 진행 상황을 추적할 수 있습니다. 지금은 API로만 가능합니다 (TUI/Web UI에 "백그라운드로 실행" 버튼은 아직 없고, 결과 상태를 보여주는 사이드바만 있습니다):

```bash
curl -X POST http://127.0.0.1:4096/api/sessions/<parent-id>/tasks \
  -d '{"agent":"explore","prompt":"src/ 아래에서 TODO 다 찾아줘"}'
```

부모 세션의 이벤트 스트림에 `task.spawned`, `task.status`(running/completed/failed/cancelled) 이벤트가 흘러들어오고, Web UI 사이드바와 TUI 트랜스크립트에 실시간으로 표시됩니다. 동시 실행 개수는 config의 `max_concurrent_tasks`로 제한됩니다.

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

## 알려진 제약

- 세션 선택 UI가 없습니다: TUI/Web UI를 새로 열 때마다 새 세션이 생성됩니다. (데몬과 이벤트 로그 자체는 다중 세션·재접속을 완전히 지원하지만, 클라이언트 쪽에 "기존 세션에 다시 붙기" 화면이 아직 없습니다.)
- 대화 히스토리는 데몬 프로세스 메모리에만 있고, 세션 로그 파일로부터 자동 복원되지는 않습니다 (재생 로직은 `internal/session.LoadFromDisk`에 있지만 아직 데몬 시작 시 연결하지 않았습니다). 데몬을 재시작하면 진행 중이던 대화 컨텍스트는 사라집니다.
- MCP 서버가 하나라도 연결에 실패하면 데몬 시작 자체가 실패합니다 (재시도나 "이 서버만 비활성화하고 계속" 같은 처리가 없습니다).
- 인증 토큰이 없습니다: `--listen`에 바인딩된 주소에 도달할 수 있는 사람은 누구나 API 전체(셸 실행 포함)를 쓸 수 있습니다. 반드시 loopback + SSH 터널로만 노출하세요.
