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
| `-version`, `--version` | `false` | 빌드된 버전 문자열만 출력하고 종료 |

`localcode version` 서브커맨드도 동일하게 동작합니다 (`-version`과 결과 같음).

세 가지 조합:

1. **`localcode`** (기본) — 로컬에 데몬을 띄우고 TUI를 그 클라이언트로 붙임. 동시에 브라우저로 `http://127.0.0.1:4096`을 열면 Web UI로도 접근 가능하고, 세션 전환 화면에서 서로 같은 세션에 이어붙을 수 있습니다 (아래 "세션 전환" 참고).
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

- **providers**: 모델 백엔드 연결 정보. `type`은 `bedrock`, `anthropic`, `openai-compat` 중 하나.
  - `bedrock.region`: AWS 리전 (예: `us-west-2`). `bedrock.profile`: 사용할 AWS named profile (`localcode login bedrock`이 만들어 둔 프로필 등) — 생략하면 AWS 기본 자격 증명 체인을 그대로 사용합니다. 모델 access 활성화, 실제 모델 ID(리전 프리픽스 포함) 등 자세한 설정은 [MODELS.md](MODELS.md#amazon-bedrock-claude)를 참고하세요.
  - `anthropic.api_key`: 생략하면 `localcode login anthropic`으로 저장한 키(`~/.localcode/credentials.json`)를 자동으로 씁니다. `anthropic.base_url`: 기본값은 `api.anthropic.com`이고, 사내 프록시를 거친다면 override 가능. 자세한 건 [MODELS.md](MODELS.md#anthropic-api-직접-사용) 참고.
  - `openai-compat.base_url`: `/chat/completions` 앞부분 URL. LM Studio, vLLM 등 OpenAI 호환 서버 주소.
  - `openai-compat.api_key`: 필요하면 지정 (로컬 서버는 보통 불필요).
- **profiles**: 실제로 쓸 provider+model 조합에 이름을 붙인 것. `max_tokens`, `temperature` 선택적으로 지정.
- **agents**: 에이전트/작업 종류 이름 → 프로필 매핑. `--agent` 플래그로 선택한 이름이 여기서 풀립니다. 없는 이름이면 `default_profile`로 대체됩니다. `profile` 외에 `description`(다른 에이전트가 Task 툴로 위임할 때 보는 설명), `prompt`(이 에이전트 전용 시스템 프롬프트 추가분), `tools`(이 에이전트가 쓸 수 있는 툴을 이 목록으로 제한 — 비워두면 전체 툴 사용 가능)를 지정할 수 있습니다. 자세한 건 아래 "여러 에이전트 조합하기" 참고.
- **max_concurrent_tasks**: 백그라운드 태스크(아래 참고) 동시 실행 개수 제한.
- **mcp_servers**: Claude Code의 `.mcp.json`과 같은 모양(`command`/`args`/`env`)이라, 기존 항목을 그대로 옮겨 쓸 수 있습니다. 각 서버는 stdio로 붙고, 그 서버의 툴은 `mcp__<서버이름>__<툴이름>`으로 노출됩니다. **MCP 툴은 항상 권한 확인을 거칩니다** — 서버가 자기 툴을 "읽기 전용"이라고 알려와도(annotations) 신뢰하지 않습니다. 서버 하나가 연결에 실패해도(잘못된 command, 프로세스 크래시 등) 그 서버만 건너뛰고 나머지는 정상 등록됩니다 — 데몬 로그에 경고만 남고 시작은 막히지 않습니다. 연결된 서버의 세션이 죽으면 다음 호출 시 자동으로 한 번 재연결을 시도합니다.

설정이 틀리면(예: 존재하지 않는 provider를 가리키는 profile) 실행 시작 시점에 바로 에러를 내고 종료합니다.

## `/login`으로 인증하기

`localcode login <bedrock|anthropic>`는 클라우드 provider 인증을 대화식으로 끝내주는 CLI 서브커맨드입니다 (데몬/TUI를 띄우기 전에 터미널에서 직접 실행). config.json에 `api_key`를 직접 적어 넣거나 AWS CLI를 미리 설치해야 하는 수고를 없애줍니다.

> claude.ai **Pro/Max 구독 자체를 재사용하는 로그인은 지원하지 않습니다.** Claude Code가 그렇게 동작하는 건 Anthropic이 Claude Code 전용으로 발급한 비공개 OAuth 클라이언트를 쓰기 때문인데, 그 자격 증명과 스코프는 공개되어 있지 않습니다. 제3자 도구가 이를 흉내 내려고 하면 Anthropic 이용약관 위반 소지가 있어 구현하지 않았습니다. 아래 두 방법은 각각 AWS/Anthropic이 공개한 정식 인증 절차만 사용합니다.

### `localcode login bedrock`

AWS IAM Identity Center(SSO)의 **디바이스 인가 플로우**를 직접 구현했습니다 — AWS CLI 설치 없이도 동작합니다.

```bash
localcode login bedrock
```

- SSO 시작 URL, SSO 리전을 물어봅니다 (플래그로 미리 줄 수도 있음: `--start-url`, `--sso-region`, `--region`, `--profile`, `--account`, `--role`).
- 인증용 URL을 출력하고 (가능하면 자동으로 브라우저도 엽니다) 승인을 기다립니다. **이 URL은 디바이스 코드 방식이라 어떤 기기에서 열어도 상관없습니다** — 이 명령을 실행 중인 컴퓨터일 필요가 없습니다.
- 로그인 후 접근 가능한 AWS 계정/역할이 여러 개면 번호로 선택하게 하고, 하나뿐이면 자동 선택합니다.
- 결과를 **AWS CLI와 동일한 위치**에 저장합니다: `~/.aws/sso/cache/<start-url의 sha1>.json` (토큰 캐시), `~/.aws/config`의 `[profile <이름>]` (기본값 `localcode-bedrock`). 이미 같은 이름의 프로필이 있으면 건드리지 않습니다.
- 이렇게 저장된 자격 증명은 AWS 기본 자격 증명 체인이 그대로 인식하므로, config.json에는 `"providers": {"bedrock": {"type":"bedrock","region":"...","profile":"localcode-bedrock"}}`만 추가하면 됩니다 — 명령이 끝나면 정확한 값을 출력해줍니다.

### `localcode login anthropic`

```bash
localcode login anthropic
```

`console.anthropic.com`에서 발급한 API 키를 입력받아(터미널이면 화면에 표시되지 않음) `~/.localcode/credentials.json`(권한 0600)에 저장합니다. config.json에는 `"providers": {"anthropic": {"type":"anthropic"}}`만 추가하면 되고, `api_key`는 생략해도 저장된 키를 자동으로 씁니다. 자세한 사용처는 [MODELS.md의 Anthropic API 직접 사용](MODELS.md#anthropic-api-직접-사용) 참고.

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

## AGENTS.md 프로젝트 규칙

opencode/Claude Code와 같은 관례입니다: 프로젝트 루트(또는 그 상위 디렉터리, git 저장소 루트까지)에 `AGENTS.md`를 두면 시작 시 자동으로 시스템 프롬프트에 붙습니다. `CLAUDE.md`도 같은 자리에서 폴백으로 인식하므로, 이미 Claude Code용으로 작성해둔 `CLAUDE.md`가 있으면 그대로 재사용됩니다. `~/.localcode/AGENTS.md`(전역, 없으면 `~/.claude/CLAUDE.md` 폴백)를 두면 모든 프로젝트에 공통으로 적용되는 개인 규칙도 함께 붙습니다 — 프로젝트 규칙과 전역 규칙은 서로 덮어쓰지 않고 둘 다 합쳐집니다.

```markdown
# AGENTS.md
빌드: `make build`
테스트: `go test ./...`
아키텍처: 코어 데몬(HTTP+SSE) + TUI/Web UI 클라이언트, internal/agent가 턴 진행 로직.
컨벤션: 주석은 WHY만, 에러 처리는 실제 발생 가능한 경우에만.
```

내용을 직접 쓰기 번거로우면 `/init` 명령으로 모델이 저장소를 스캔해서 초안을 만들어 줍니다 (아래 참고).

`AGENTS.md`/`CLAUDE.md` 본문 안에서 `@경로`로 다른 파일을 그 자리에 그대로 삽입할 수 있습니다(Claude Code의 import 문법과 동일). 상대 경로는 그 파일(임포트하는 쪽)이 있는 디렉터리 기준으로 풀리고, `@~/경로`는 홈 디렉터리 기준입니다. 임포트한 파일이 또 다른 파일을 임포트하는 재귀도 가능하며 최대 4단계까지만 따라갑니다. 코드 블록(` ``` `) 안이나 인라인 코드(`` `@경로` ``)로 감싼 건 임포트로 처리하지 않고 그대로 둡니다.

```markdown
# AGENTS.md
@README.md 를 참고해 프로젝트 개요를 파악하세요.
개인 워크플로: @~/.localcode/my-workflow.md
```

## Auto Memory (모델이 스스로 기록하는 메모리)

Claude Code의 auto memory와 같은 개념입니다. `AGENTS.md`가 **사람이 직접 쓰는** 규칙이라면, auto memory는 **모델이 세션을 진행하며 스스로 기록**하는 메모입니다 — 빌드 명령, 디버깅 중 알아낸 사실, 사용자가 말한 선호("pnpm 써줘" 등) 같은 걸 다음 세션에도 이어서 기억하게 합니다.

- 프로젝트별로 `~/.localcode/projects/<slug>/memory/` 디렉터리가 자동으로 배정됩니다. `<slug>`는 프로젝트의 git 저장소 루트 경로에서 만들어지므로, 같은 저장소의 여러 워크트리/서브디렉터리에서 실행해도 메모리 디렉터리 하나를 공유합니다 (git 저장소가 아니면 실행 디렉터리 자체를 기준으로).
- 그 디렉터리의 `MEMORY.md`가 인덱스 파일로, 매 세션 시작 시 시스템 프롬프트에 자동으로 로드됩니다(최대 200줄 또는 25KB, Claude Code와 동일한 상한 — 그 이상은 안 실립니다). 세부 내용은 `debugging.md` 같은 별도 토픽 파일로 분리하도록 시스템 프롬프트가 모델에게 안내합니다 — 토픽 파일은 모델이 필요할 때 `read_file`로 직접 읽습니다.
- 별도의 전용 툴은 없습니다 — 모델이 이미 가진 `read_file`/`write_file`/`edit` 툴로 그 디렉터리에 자유롭게 쓰고 읽습니다. 시스템 프롬프트에 디렉터리 경로와 현재 인덱스 내용이 매 세션 안내됩니다.
- `/memory` 명령으로 현재 메모리 디렉터리 경로와 인덱스 내용을 바로 확인할 수 있습니다 (모델 호출 없음, 즉답).
- 끄고 싶으면 config에 `"auto_memory_enabled": false`를 추가하세요. 기본값은 켜짐입니다.

```json
{
  "auto_memory_enabled": false
}
```

## 화면 조작 (TUI / Web UI 공통)

- 메시지를 입력하고 **Enter**로 전송 (Web UI는 전송 버튼도 있음)
- 입력창은 여러 줄을 입력하면 자동으로 늘어납니다 (최대 약 10줄, 그 이상은 내부 스크롤). 줄바꿈만 넣고 싶으면 TUI는 **Ctrl+J**, Web UI는 **Shift+Enter**
- 모델이 파일 쓰기(`write_file`)/수정(`edit`)/셸 실행(`bash`)/MCP 툴 호출을 요청하면 **권한 확인 모달**이 뜹니다 — TUI는 `y`/`n`, Web UI는 승인/거부 버튼
- 권한 요청은 세션에 붙은 아무 클라이언트에서나 응답 가능하고, 응답하면 다른 클라이언트의 모달은 자동으로 닫힙니다
- TUI 종료는 **Ctrl+C**

### `/skill` 명령

메시지 입력창에 직접 스킬 명령을 칠 수 있습니다 — 모델이 알아서 Skill 툴을 호출하길 기다리지 않고 사용자가 바로 지정합니다.

- `/skill` — 등록된 스킬 이름/설명 목록을 즉시 보여줍니다 (모델 호출 없음, 즉답)
- `/skill <이름>` — 그 스킬의 전체 본문을 모델에게 지침으로 붙여서 바로 그 턴을 진행합니다. 화면에는 짧게 `/skill <이름>`만 남고, 실제 스킬 본문은 모델에게만 전달됩니다
- 없는 이름을 입력하면 등록된 이름 목록과 함께 에러가 표시됩니다 (역시 모델 호출 없음)

### `/init` 명령

opencode의 `/init`과 같습니다. 저장소를 스캔(`Glob`/`Grep`/`Read`)해서 빌드/린트/테스트 명령, 아키텍처 개요, 코드 컨벤션을 담은 `AGENTS.md`를 프로젝트 루트에 생성하거나(이미 있으면) 개선합니다. 화면에는 짧게 `/init`만 남고, 실제로는 모델에게 스캔·작성 지침이 전달됩니다 — 파일 쓰기가 필요하므로 처음 실행 시 `write_file`/`edit` 권한 확인이 뜹니다.

### 사용자 정의 명령 (`/<이름>`)

`.localcode/commands/<이름>.md`(프로젝트) 또는 `~/.localcode/commands/<이름>.md`(전역, 같은 이름이면 프로젝트가 우선)에 마크다운 파일을 두면 `/<이름>`으로 바로 호출할 수 있습니다 — opencode의 커스텀 커맨드와 같은 형식입니다.

```markdown
---
description: 지정한 패턴에 매칭되는 테스트만 실행
agent: build
model: my-strong-model-id
---
`$ARGUMENTS` 패턴에 매칭되는 테스트를 찾아서 실행 결과를 분석해줘.
관련 소스: @internal/agent/loop.go
현재 실패 목록: !`go test ./... 2>&1 | grep FAIL`
```

- **`description`**: `/commands`로 목록을 볼 때 표시되는 한 줄 설명 (선택).
- **`agent`**: 이 명령을 실행할 때만 쓸 에이전트를 지정 (선택). 세션의 기본 에이전트는 바뀌지 않고, 이 한 턴에만 적용됩니다 — 그 에이전트의 프로필(모델)·시스템 프롬프트·툴 제한이 그대로 적용됩니다.
- **`model`**: 프로필과 상관없이 이 한 턴만 다른 모델 ID로 강제 (선택).
- **본문**: 실제로 모델에게 전달되는 프롬프트 템플릿. `$ARGUMENTS`(전체 인자 문자열), `$1`~`$9`(공백으로 나눈 위치 인자), `` !`셸 명령` ``(그 셸 명령의 표준출력을 그 자리에 삽입), `@경로`(그 파일 내용을 그 자리에 삽입, 명령 파일이 아니라 현재 작업 디렉터리 기준 상대경로)를 지원합니다.

예: `/hello World` → 본문의 `$1`과 `$ARGUMENTS`가 각각 `World`로 치환된 텍스트가 모델에게 전달되고, 화면에는 `/hello World`만 남습니다. `/commands`로 등록된 명령 목록을 볼 수 있습니다.

### `/memory` 명령

현재 프로젝트의 auto memory 디렉터리 경로와 `MEMORY.md` 인덱스 내용을 즉시 보여줍니다 (모델 호출 없음). 자세한 설명은 위의 [Auto Memory](#auto-memory-모델이-스스로-기록하는-메모리) 절 참고.

### 그 외 로컬 명령 (`/help`, `/version`, `exit`, `:q`)

`/skill`처럼 메시지 입력창에 치는 명령이지만, 세션의 이벤트 로그에는 남지 않습니다 (서버에 아무것도 기록되지 않으므로 세션을 재생해도 다시 나타나지 않습니다).

| 명령 | 동작 |
|---|---|
| `/help` | 사용 가능한 명령 목록을 즉시 표시 (모델 호출 없음) |
| `/version` | 현재 붙어있는 **데몬**의 버전을 표시 (`GET /api/version`). `--server`로 원격 데몬에 붙은 경우 그 데몬의 버전이 나옵니다 — 로컬 바이너리 버전과 다를 수 있습니다 |
| `exit`, `:q` | TUI는 즉시 종료 (Ctrl+C와 동일). Web UI는 브라우저가 프로그램을 종료할 수 없으므로 안내 메시지만 표시 — 탭을 직접 닫으세요 |

## 세션 전환

세션은 데몬이 살아있는 동안 계속 유지되는 append-only 이벤트 로그라서, TUI를 다시 켜거나 브라우저 탭을 새로 열어도 이전 대화를 그대로 이어받을 수 있습니다.

- **TUI**: 시작할 때 기존 세션이 하나라도 있으면 터미널에 목록(세션 ID/agent/생성 시각)이 뜨고, 번호를 입력해 이어하거나 `n`(또는 빈 입력)으로 새 세션을 시작합니다.
- **Web UI**: 페이지를 열면 기존 세션이 있을 때 세션 선택 모달이 뜹니다. 우측 상단 **"세션 전환"** 버튼으로 언제든 다시 열 수 있습니다. 다른 세션으로 전환하면 화면이 지워지고, 그 세션의 이벤트 로그 전체(사용자 메시지·모델 응답·툴 실행 기록)가 처음부터 재생됩니다.

세션 목록은 `GET /api/sessions`로도 직접 조회할 수 있습니다 (백그라운드 태스크는 `visible:false`라 이 목록에는 안 뜨고, `GET /api/sessions/{id}/tasks`로 따로 봅니다).

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
| `Task` | 아니오 | 이름 붙은 다른 에이전트에게 작업을 위임하고 결과를 기다림 (`agents`가 2개 이상 설정됐을 때만 등록됨) |

## 여러 에이전트 조합하기

config의 `agents` 맵에 `profile` 하나만 지정했을 때는 지금까지처럼 "이 이름으로 실행하면 이 모델을 쓴다"는 라우팅일 뿐입니다. 여기에 `description`/`prompt`/`tools`를 추가하면 진짜 **역할이 다른 에이전트**가 되고, 모델이 `Task` 툴로 서로를 호출해서 위임할 수 있습니다 — opencode의 서브에이전트/모델 매칭 방식(예: `oh-my-opencode`가 orchestrator·explore·review처럼 역할별로 다른 모델을 붙이는 것)에서 착안했습니다.

```json
"agents": {
  "build": {
    "profile": "strong",
    "description": "Implements features and fixes bugs.",
    "prompt": "You are the build agent. Delegate research to the explore agent via the Task tool instead of doing it yourself."
  },
  "explore": {
    "profile": "cheap",
    "description": "Fast, read-only codebase search.",
    "prompt": "You are the explore agent. Locate relevant files and summarize quickly.",
    "tools": ["read_file", "glob", "grep"]
  }
}
```

- **`profile`**: 이 에이전트가 어떤 provider+model을 쓸지 (필수, 기존과 동일).
- **`description`**: 다른 에이전트가 `Task` 툴로 위임 대상을 고를 때 보는 한 줄 설명.
- **`prompt`**: 이 에이전트로 실행될 때 기본 시스템 프롬프트 뒤에 덧붙는 지침. 역할을 좁히는 용도 (`"파일을 수정하지 마라"`, `"빠르고 간결하게"` 등).
- **`tools`**: 이 에이전트가 쓸 수 있는 툴 이름 목록. 비워두면 제한 없음(전체 툴 + 등록되어 있으면 `Task`까지). 지정하면 모델에게 그 툴들만 보이고, 혹시 모델이 목록 밖 툴을 호출해도 실행 전에 거부됩니다(이중 방어).

**`Task` 도구**: `agents`에 항목이 2개 이상이면 자동으로 등록됩니다. 모델이 `Task({"agent":"explore","prompt":"..."})`를 호출하면:

1. `explore` 세션이 새로 만들어지고(부모 세션에 `task.spawned` 이벤트) `max_concurrent_tasks` 세마포어를 기다립니다.
2. `explore`의 `profile`/`prompt`/`tools`로 **동기적으로** 한 턴을 실행합니다 — 아래 "백그라운드 태스크"와 달리 위임한 에이전트의 턴이 이 결과를 기다립니다.
3. `explore`의 최종 답변 텍스트가 `Task` 호출의 결과로 돌아가고, 위임한 에이전트는 그걸 이어서 씁니다.

무한 위임(에이전트가 자기 자신 또는 서로를 계속 호출)을 막기 위해 위임 깊이가 3단계를 넘으면 `Task` 호출이 자동으로 거부됩니다.

## Plan 모드 (같은 대화 안에서 에이전트 전환)

opencode의 Plan/Build 모드(Tab 키로 전환)와 같은 개념입니다. `config.example.json`의 `plan` 에이전트는 `tools: ["read_file","glob","grep"]`만 허용해서 파일 쓰기·`edit`·`bash`가 아예 노출/실행되지 않습니다 — opencode의 Plan 모드는 이걸 "권한 ask"로 구현하는데, 실제로 [`bash가 plan 모드에서 실행돼버리는 버그`](https://github.com/anomalyco/opencode/issues/20938)나 [`서브에이전트가 read-only 제약을 우회하는 버그`](https://github.com/anomalyco/opencode/issues/26514)가 보고된 적이 있습니다. localcode는 애초에 그 툴을 모델에게 보여주지도 않고, 혹시 호출해도 실행 직전에 거부하는 이중 차단이라 같은 종류의 우회가 구조적으로 불가능합니다.

**세션 하나의 대화 맥락은 그대로 두고, 다음 메시지부터 쓸 에이전트만 바꿉니다** — 세션을 새로 만드는 게 아닙니다.

- **TUI**: **Tab** 키로 config에 등록된 에이전트를 순서대로 순환 전환. 상단에 현재 에이전트가 항상 표시됩니다.
- **Web UI**: 헤더의 드롭다운으로 전환.
- 둘 다 `/agent`(목록) / `/agent <이름>`(전환) 명령 지원.
- 전환은 `POST /api/sessions/{id}/agent`로 이뤄지고, 성공하면 세션에 `agent.switched` 이벤트가 남아 그 세션에 붙은 모든 클라이언트(TUI+Web UI 동시 접속 포함)가 동시에 갱신됩니다.

일반적인 흐름: `plan`으로 분석·설계를 시킨 뒤(파일 변경 불가) 계획이 맘에 들면 Tab으로 `build`로 전환해서 그대로 이어서 실행시킵니다 — 대화 맥락(무엇을 분석했는지)은 유지된 채로요.

## 백그라운드 태스크

세션 하나(부모)에서 다른 에이전트를 백그라운드로 띄우고 진행 상황을 추적할 수 있습니다. 위의 `Task` 툴과 이벤트 종류(`task.spawned`/`task.status`)는 같지만, 이건 **비동기**입니다 — 호출한 쪽이 결과를 기다리지 않고 바로 다음으로 넘어갑니다. 지금은 API로만 가능합니다 (TUI/Web UI에 "백그라운드로 실행" 버튼은 아직 없고, 결과 상태를 보여주는 사이드바만 있습니다):

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

- 대화 히스토리는 데몬 프로세스 메모리에만 있고, 세션 로그 파일로부터 자동 복원되지는 않습니다 (재생 로직은 `internal/session.LoadFromDisk`에 있지만 아직 데몬 시작 시 연결하지 않았습니다). **데몬을 재시작하면** 세션 목록·대화 컨텍스트가 전부 사라집니다 — 위 "세션 전환"은 같은 데몬 프로세스가 살아있는 동안에만 적용됩니다.
- MCP 서버 하나가 죽었다가 재연결에도 실패하면(예: 실행 파일 자체가 사라짐) 그 서버의 툴은 이후 호출마다 계속 에러를 반환합니다 — 데몬을 재시작해야 다시 붙습니다.
- 인증 토큰이 없습니다: `--listen`에 바인딩된 주소에 도달할 수 있는 사람은 누구나 API 전체(셸 실행 포함)를 쓸 수 있습니다. 반드시 loopback + SSH 터널로만 노출하세요.
