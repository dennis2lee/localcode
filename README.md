# localcode

Bedrock + Anthropic API 직접 + OpenAI-compatible(로컬 LLM) 세 가지에 다 붙는 코딩 에이전트. Claude Code처럼 파일 읽기/쓰기, 셸 실행, MCP, Skills를 모델이 직접 호출하고, 코어는 헤드리스 데몬으로 띄운 뒤 TUI와 브라우저(Web UI)가 둘 다 그 위의 대등한 클라이언트로 붙는 구조입니다.

## 핵심 기능

- **Provider 3종**: Bedrock, Anthropic API 직접, OpenAI-compatible(LM Studio/vLLM 등 로컬 LLM) — config 하나로 전환. `localcode login bedrock`(AWS SSO 디바이스 플로우, AWS CLI 불필요)/`localcode login anthropic`(API 키 저장)으로 CLI에서 바로 인증.
- **안전장치**: `permission` config로 opencode 스타일 세밀한 허용/거부/확인 규칙(`git *`는 자동 허용, `rm *`는 자동 차단 등), Claude Code 스타일 `hooks`(pre/post_tool_use, user_prompt_submit, stop, session_start 시점에 셸 명령 실행/차단).
- **멀티 에이전트**: 역할별로 다른 모델·프롬프트·툴 범위를 가진 에이전트를 정의하고 `Task` 툴로 서로 위임(oh-my-opencode 스타일). Tab 키(또는 `/agent`)로 세션 맥락을 유지한 채 에이전트 전환 — opencode의 Plan/Build 모드와 같은 흐름.
- **프로젝트 컨텍스트**: `AGENTS.md` 자동 인식(`@경로` 임포트 포함, `CLAUDE.md` 폴백), `/init`으로 초안 생성, `.localcode/commands/*.md` 사용자 정의 슬래시 명령, Claude Code 스타일 auto memory(모델이 세션 간 스스로 기록하는 메모).
- **대화 관리**: `/compact [지침]`으로 즉시 압축, context 80% 초과 시 자동 압축, `/usage`로 모델별 누적 토큰 사용량(토큰 수만, 달러 아님). **데몬을 재시작해도** 세션 목록·대화 컨텍스트·`/usage` 누적치가 디스크에서 그대로 복원됩니다.
- **Web UI**: 파일 드래그 앤 드롭 첨부, 프롬프트 하단 실시간 상태 표시줄(에이전트/모델/context 사용률/TPS/통신 표시등), 세션 이름 변경/삭제가 가능한 오른쪽 패널(세션 목록 + 연결된 MCP 서버 목록).
- **MCP 서버 관리**: `localcode mcp add/list/get/remove` CLI 서브커맨드로 (Claude Code의 `claude mcp`처럼) config.json을 직접 건드리지 않고 등록·조회·삭제.

## 문서

- [설치 방법](INSTALL.md) — 소스 빌드, macOS/Windows 배포 패키지 만들기
- [사용 방법](USAGE.md) — config.json 작성법, 명령어, 화면 조작, 세션/에이전트 관리 (전체 목차는 문서 상단 참고)
- [모델 설정 가이드](MODELS.md) — Amazon Bedrock/Claude, 로컬 LLM(LM Studio 등) 실제 설정 방법과 검증된 모델 ID
- [개선 목록](IMPROVEMENTS.md) — 알려진 완성도 갭, UI 개선 아이디어
- [CHANGELOG](CHANGELOG.md) — 버전별 변경 이력
- [LICENSE](LICENSE) — MIT

## 아키텍처

```
[core daemon]  ← 세션/에이전트 루프/툴/MCP/Skills/Provider/다중 에이전트 Task Manager
   ├ HTTP API   (세션 생성, 메시지 전송, 권한 응답, 백그라운드 태스크 스폰)
   └ SSE        (토큰 스트림, 툴 시작/종료, 권한 요청, 태스크 상태)
        ↑              ↑
     [TUI]         [Web UI]   ← 둘 다 동일한 API를 쓰는 1급 클라이언트
```

세션은 메시지 배열이 아니라 append-only 이벤트 로그라서, TUI를 껐다 켜거나 브라우저 탭을 새로 열어도 `since` seq 하나로 그 자리에서 이어붙습니다.

## 빠른 시작

```bash
go build -o localcode ./cmd/localcode
mkdir -p ~/.localcode
cp config.example.json ~/.localcode/config.json
# ~/.localcode/config.json을 열어 Bedrock 리전·모델 ID, 로컬 LLM 주소로 수정

./localcode --agent general-purpose
```

기본 실행은 로컬 데몬을 띄우고 TUI를 그 클라이언트로 붙입니다. 같은 주소(`http://127.0.0.1:4096`)를 브라우저로 열면 Web UI로도 동시에 접속할 수 있습니다. 원격 서버에서 데몬만 돌리고(`--headless`) 맥북에서 `--server`로 붙는 구성은 [USAGE.md](USAGE.md#원격-데몬--ssh-터널)를 참고하세요.

## 테스트

```bash
go test ./...
```

## 아직 없는 것

- macOS 코드 서명·공증, Windows msi 코드 서명 (둘 다 설치는 되지만 아직 미서명 상태)
- Windows arm64용 msi (현재 amd64만 msi 지원, arm64는 portable zip)

자세한 제약 사항은 [USAGE.md](USAGE.md#알려진-제약)를 참고하세요.
