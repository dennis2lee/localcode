# 설치 방법

## 1. 소스에서 빌드

### 요구 사항

- Go 1.23 이상 (`brew install go`)
- macOS에서 `.app` 번들을 만들려면 Xcode Command Line Tools의 `lipo` (기본 내장)
- Windows용 zip을 만들려면 `zip` (macOS/Linux 기본 내장)
- Windows용 `.msi`를 만들려면 `msitools` (`brew install msitools`) — Windows 없이 macOS/Linux에서 바로 `.msi`를 빌드합니다

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
make dist            # dist/mac, dist/windows 전부 생성 (msi 포함)
make dist-mac         # macOS만
make dist-windows      # Windows zip만
make dist-msi          # Windows msi만
```

결과물:

| 플랫폼 | 경로 | 형태 |
|---|---|---|
| macOS | `dist/mac/localcode-<version>-darwin-universal.tar.gz` | 순수 바이너리 (Intel+Apple Silicon universal) |
| macOS | `dist/mac/LocalCode-<version>-darwin-universal-app.tar.gz` | `.app` 번들 (더블클릭 실행, 터미널 자동 실행) |
| Windows | `dist/windows/localcode-<version>-windows-amd64.msi` | 설치형 패키지 (64비트 인텔/AMD, 시작 메뉴 바로가기 + PATH 등록) |
| Windows | `dist/windows/localcode-<version>-windows-amd64.zip` | portable zip (64비트 인텔/AMD) |
| Windows | `dist/windows/localcode-<version>-windows-arm64.zip` | portable zip (ARM64, Surface 등 — msi는 아직 arm64 미지원) |

### macOS 설치

```bash
tar xzf dist/mac/LocalCode-<version>-darwin-universal-app.tar.gz -C /Applications
```

`.app`은 아직 Apple Developer ID로 서명·공증되지 않았습니다. 처음 실행 시 Gatekeeper가 막으면:

1. Finder에서 `LocalCode.app`을 우클릭 → "열기" 선택
2. 경고 창에서 다시 "열기" 클릭

(배포용으로 서명하려면 `codesign --sign "Developer ID Application: ..." LocalCode.app` 후 `xcrun notarytool submit`으로 공증해야 합니다. Apple Developer 계정이 필요합니다.)

### Windows 설치

**msi (권장, amd64):** `localcode-<version>-windows-amd64.msi`를 더블클릭해서 설치 마법사를 따라가면 `C:\Program Files\LocalCode\`에 설치되고, 시작 메뉴 바로가기가 생기고, PATH에 자동 등록되어 어디서든 `localcode` 명령을 바로 쓸 수 있습니다. 재설치하면 이전 버전을 자동으로 업그레이드합니다 (MSI `UpgradeCode` 고정).

msi가 아직 서명되지 않아 SmartScreen이 "Windows에서 PC를 보호했습니다" 경고를 띄울 수 있습니다 — "추가 정보" → "실행"으로 진행하거나, 배포 전에 code-signing 인증서로 서명하세요 (Windows에서 `signtool sign`, 또는 크로스플랫폼 `osslsigncode`).

**zip (portable, amd64/arm64):** 압축을 풀고 `localcode.exe`를 원하는 위치에 두고 바로 실행하면 됩니다. 설치 과정이나 PATH 등록이 없습니다. arm64는 아직 msi가 지원되지 않아(빌드에 사용한 `wixl` 0.106이 `-a arm64`를 거부합니다) zip만 제공됩니다.

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
