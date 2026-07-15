# localcode

Bedrock + OpenAI-compatible(로컬 LLM) 양쪽에 붙는 TUI 코딩 에이전트. Claude Code처럼 파일 읽기/쓰기, 셸 실행 같은 툴을 모델이 직접 호출할 수 있고, 클라우드 모델(Bedrock)과 로컬 모델(LM Studio 등)을 config 하나로 전환합니다.

- [설치 방법](INSTALL.md) — 소스 빌드, macOS/Windows 배포 패키지 만들기
- [사용 방법](USAGE.md) — config.json 작성법, 화면 조작, 로컬 LLM 연결
- [LICENSE](LICENSE) — MIT

## 빠른 시작

```bash
go build -o localcode ./cmd/localcode
mkdir -p ~/.localcode
cp config.example.json ~/.localcode/config.json
# ~/.localcode/config.json을 열어 Bedrock 리전·모델 ID, 로컬 LLM 주소로 수정

./localcode --agent general-purpose
```

## 테스트

```bash
go test ./...
```

## 아직 없는 것

- 코어 데몬/HTTP-SSE 분리 (현재는 TUI가 agent loop를 in-process로 직접 호출하는 단일 프로세스 MVP)
- Web UI, MCP, Skills, 백그라운드 Task Manager(동시 에이전트 실행/추적)
- macOS 코드 서명·공증, Windows msi 코드 서명 (둘 다 설치는 되지만 아직 미서명 상태)
- Windows arm64용 msi (현재 amd64만 msi 지원, arm64는 portable zip)

자세한 제약 사항은 [USAGE.md](USAGE.md#알려진-제약-mvp)를 참고하세요.
