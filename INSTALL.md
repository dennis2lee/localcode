# 설치 방법

## 1. 소스에서 빌드

### 요구 사항

- Go 1.23 이상 (`brew install go`)
- macOS에서 `.app` 번들을 만들려면 Xcode Command Line Tools의 `lipo` (기본 내장)
- Windows용 zip을 만들려면 `zip` (macOS/Linux 기본 내장)

### 빌드

```bash
git clone https://github.com/dennis2lee/localcode.git
cd localcode
go build -o localcode ./cmd/localcode
```

바로 실행하려면:

```bash
./localcode --agent general-purpose
```

## 2. 배포 패키지 빌드 (macOS / Windows)

```bash
make dist            # dist/mac, dist/windows 둘 다 생성
make dist-mac         # macOS만
make dist-windows      # Windows만
```

결과물:

| 플랫폼 | 경로 | 형태 |
|---|---|---|
| macOS | `dist/mac/localcode-<version>-darwin-universal.tar.gz` | 순수 바이너리 (Intel+Apple Silicon universal) |
| macOS | `dist/mac/LocalCode-<version>-darwin-universal-app.tar.gz` | `.app` 번들 (더블클릭 실행, 터미널 자동 실행) |
| Windows | `dist/windows/localcode-<version>-windows-amd64.zip` | 64비트 인텔/AMD |
| Windows | `dist/windows/localcode-<version>-windows-arm64.zip` | ARM64 (Surface 등) |

### macOS 설치

```bash
tar xzf dist/mac/LocalCode-<version>-darwin-universal-app.tar.gz -C /Applications
```

`.app`은 아직 Apple Developer ID로 서명·공증되지 않았습니다. 처음 실행 시 Gatekeeper가 막으면:

1. Finder에서 `LocalCode.app`을 우클릭 → "열기" 선택
2. 경고 창에서 다시 "열기" 클릭

(배포용으로 서명하려면 `codesign --sign "Developer ID Application: ..." LocalCode.app` 후 `xcrun notarytool submit`으로 공증해야 합니다. Apple Developer 계정이 필요합니다.)

### Windows 설치

압축을 풀고 `localcode.exe`를 원하는 위치에 두고 실행하면 됩니다. 별도 설치 과정은 없습니다 (현재는 portable 배포만 지원, `.msi` 설치 패키지는 아직 없습니다).

## 3. 설정 파일 준비

실행 전에 config.json이 필요합니다. 자세한 항목은 [USAGE.md](USAGE.md)를 참고하세요.

```bash
mkdir -p ~/.localcode
cp config.example.json ~/.localcode/config.json
# ~/.localcode/config.json을 열어 실제 Bedrock 리전/모델 ID, 로컬 LLM 주소로 수정
```

## 4. AWS 자격 증명 (Bedrock 프로필을 쓰는 경우)

기본 AWS 자격 증명 체인을 그대로 사용합니다. 아래 중 하나로 설정되어 있어야 합니다.

- `aws configure` (액세스 키)
- `aws sso login` (SSO)
- 환경 변수 `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` / `AWS_SESSION_TOKEN`
- EC2/컨테이너 인스턴스 역할

Bedrock 콘솔에서 해당 리전에 사용할 Claude 모델 접근 권한(model access)이 활성화되어 있어야 합니다.
