# localcode

Bedrock + OpenAI-compatible(로컬 LLM) 양쪽에 붙는 코딩 에이전트. Claude Code처럼 파일 읽기/쓰기, 셸 실행, MCP, Skills를 모델이 직접 호출할 수 있고, 클라우드 모델(Bedrock)과 로컬 모델(LM Studio 등)을 config 하나로 전환합니다. 역할별로 다른 모델·프롬프트·툴 범위를 가진 여러 에이전트를 정의하고 `Task` 툴로 서로 위임하게 할 수도 있습니다(oh-my-opencode 스타일). `AGENTS.md`(opencode/Claude Code와 같은 관례) 프로젝트 규칙 자동 인식, `/init`으로 초안 생성, `.localcode/commands/*.md` 사용자 정의 슬래시 명령도 지원합니다. 코어는 헤드리스 데몬이고, TUI와 브라우저(Web UI)는 둘 다 그 위의 대등한 클라이언트입니다.

- [설치 방법](INSTALL.md) — 소스 빌드, macOS/Windows 배포 패키지 만들기
- [사용 방법](USAGE.md) — config.json 작성법(Provider/MCP/Skills), 화면 조작, 백그라운드 태스크
- [모델 설정 가이드](MODELS.md) — Amazon Bedrock/Claude, 로컬 LLM(LM Studio 등) 실제 설정 방법과 검증된 모델 ID
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
- 데몬 재시작 시 세션 히스토리 자동 복원 (재생 로직은 있지만 아직 시작 시 연결 안 됨 — 프로세스가 살아있는 동안은 세션 목록/재접속이 완전히 동작함)

자세한 제약 사항은 [USAGE.md](USAGE.md#알려진-제약)를 참고하세요.
