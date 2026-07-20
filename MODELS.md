# 모델 설정 가이드

localcode는 세 종류의 provider를 지원합니다 — **Amazon Bedrock**(클라우드, Claude), **Anthropic API 직접 사용**(console.anthropic.com API 키), **OpenAI-compatible**(로컬 LLM, LM Studio/vLLM 등). 이 문서는 각각을 실제로 어떻게 설정하는지 다룹니다. 기본 항목 설명은 [USAGE.md](USAGE.md#설정-파일-configjson)를 먼저 참고하세요.

두 클라우드 provider(Bedrock, Anthropic API) 모두 `localcode login <bedrock|anthropic>` 명령으로 인증을 끝낼 수 있습니다 — 자세한 사용법은 [USAGE.md의 "/login으로 인증하기"](USAGE.md#login으로-인증하기)를 참고하세요. claude.ai Pro/Max 구독 자체를 재사용하는 로그인은 지원하지 않습니다 — Claude Code 전용으로 발급된 비공개 OAuth 클라이언트가 필요한데, 그 자격 증명은 공개되어 있지 않고 제3자 도구가 재현하면 Anthropic 이용약관 위반 소지가 있기 때문입니다.

## Amazon Bedrock (Claude)

### 1. AWS 계정 준비

1. AWS 계정이 없다면 만듭니다.
2. AWS 콘솔 → **Bedrock → Model access**([console.aws.amazon.com/bedrock/home#/modelaccess](https://console.aws.amazon.com/bedrock/home#/modelaccess))에서 쓰려는 Claude 모델의 접근 권한을 요청/활성화합니다. 이 단계를 빼먹으면 API 호출이 `AccessDeniedException`으로 실패합니다.
3. Anthropic 모델은 리전마다 사용 가능 여부가 다릅니다. 아래 모델 ID 표의 리전 컬럼을 참고하세요.

### 2. 자격 증명 설정

localcode는 별도 설정 없이 **AWS 기본 자격 증명 체인**을 그대로 사용합니다(`internal/provider/bedrock.go`가 `config.LoadDefaultConfig`를 호출). 아래 중 하나만 준비되어 있으면 됩니다.

```bash
# 방법 1: 액세스 키
aws configure

# 방법 2a: AWS CLI로 SSO
aws sso login --profile your-profile
export AWS_PROFILE=your-profile

# 방법 2b: localcode 자체 SSO 로그인 (AWS CLI 설치 없이도 동작)
localcode login bedrock
# 시작 URL/리전/계정/역할을 대화식으로 물어보고, config.json에 넣을
# providers.<name>.profile 값까지 안내해줍니다.

# 방법 3: 환경 변수
export AWS_ACCESS_KEY_ID=...
export AWS_SECRET_ACCESS_KEY=...
export AWS_SESSION_TOKEN=...   # 임시 자격 증명인 경우

# 확인
aws sts get-caller-identity
```

EC2/ECS/컨테이너에서 돌린다면 인스턴스/태스크 역할로도 자동 인식됩니다.

### 3. 리전과 모델 ID

`config.json`의 `providers.<name>.region`이 `AWS_REGION`처럼 SDK에 전달되는 리전입니다. **모델 ID 자체에도 별도 리전 프리픽스가 붙습니다** — 이게 헷갈리는 부분입니다.

Sonnet 4.5 이후 모델부터 Bedrock은 base 모델 ID로 직접 호출하는 걸 막고, 반드시 **cross-region inference profile ID**(base ID 앞에 `us.`/`eu.`/`global.` 등을 붙인 것)를 쓰도록 강제합니다. base ID로 그냥 호출하면 이런 에러가 납니다:

```
Invocation of model ID anthropic.claude-sonnet-4-5-20250929-v1:0 with on-demand
throughput isn't supported. Retry your request with the ID or ARN of an
inference profile that contains this model.
```

즉 `config.json`의 `profiles.<name>.model`에는 **프리픽스가 붙은 전체 ID**를 넣어야 합니다.

### 4. 실제 사용 가능한 모델 ID (2026-07 기준)

이 표는 localcode가 실제로 호출하는 **Bedrock Converse/ConverseStream API** 기준입니다 (`bedrockruntime.ConverseStream` — [internal/provider/bedrock.go](internal/provider/bedrock.go)). 최신 모델 이름과 이 표의 이름이 다를 수 있는데, Bedrock에 올라오는 시점과 이름 체계가 Anthropic 자체 API와 별도로 관리되기 때문입니다.

| 모델 | Base 모델 ID | 리전 프리픽스 | Converse API |
|---|---|---|---|
| Claude Opus 4.6 | `anthropic.claude-opus-4-6-v1` | `global.` `us.` `eu.` `jp.` `apac.` | ✅ |
| Claude Sonnet 4.6 | `anthropic.claude-sonnet-4-6` | `global.` `us.` `eu.` `jp.` | ✅ |
| Claude Sonnet 4.5 | `anthropic.claude-sonnet-4-5-20250929-v1:0` | `global.` `us.` `eu.` `jp.` | ✅ |
| Claude Opus 4.5 | `anthropic.claude-opus-4-5-20251101-v1:0` | `global.` `us.` `eu.` | ✅ |
| Claude Haiku 4.5 | `anthropic.claude-haiku-4-5-20251001-v1:0` | `global.` `us.` `eu.` | ✅ |

예를 들어 US 리전 프로필로 Sonnet 4.5를 쓰려면 모델 ID는 `us.anthropic.claude-sonnet-4-5-20250929-v1:0`, 지연 없는 전역 라우팅을 원하면 `global.anthropic.claude-opus-4-6-v1`처럼 씁니다. `global.`은 가격 프리미엄이 없고 가용성이 가장 높으며, `us.`/`eu.`/`jp.`/`apac.` 같은 리전 고정 프리픽스는 데이터 상주(data residency) 요구사항이 있을 때만 쓰면 됩니다(약 10% 가격 프리미엄).

> **⚠️ 중요한 제약**: Claude Opus 4.7, Opus 4.8, Sonnet 5, Fable 5는 **이 표에 없습니다** — Anthropic 공식 문서 기준으로 이 모델들은 ARN 버전 모델 ID가 없고, Bedrock의 새 Messages API 게이트웨이(`bedrock-mantle`, `/anthropic/v1/messages`)를 통해서만 제공됩니다. localcode의 Bedrock provider는 기존 Converse API만 구현되어 있어서, **현재는 이 최신 모델들을 Bedrock으로 쓸 수 없습니다.** Bedrock에서 최신 모델을 쓰고 싶다면 위 표의 모델(Opus 4.6/Sonnet 4.6 등)을 쓰거나, localcode의 향후 업데이트를 기다리세요. 최신 모델이 꼭 필요하다면 아래 [Anthropic API 직접 사용](#anthropic-api-직접-사용) provider로 우회할 수 있습니다 — Bedrock을 거치지 않으므로 이 제약이 없습니다.

최신 사용 가능 여부/리전은 언제든 바뀔 수 있으므로, 확실한 최신 목록이 필요하면:

```bash
aws bedrock list-foundation-models --region=us-west-2 --by-provider anthropic \
  --query "modelSummaries[*].modelId"
```

### 5. config.json 예시

```json
{
  "providers": {
    "bedrock": { "type": "bedrock", "region": "us-west-2" }
  },
  "profiles": {
    "strong":   { "provider": "bedrock", "model": "us.anthropic.claude-opus-4-6-v1", "max_tokens": 8192 },
    "balanced": { "provider": "bedrock", "model": "us.anthropic.claude-sonnet-4-5-20250929-v1:0", "max_tokens": 8192 },
    "cheap":    { "provider": "bedrock", "model": "us.anthropic.claude-haiku-4-5-20251001-v1:0", "max_tokens": 4096 }
  },
  "agents": {
    "general-purpose": { "profile": "balanced" },
    "explore":          { "profile": "cheap" }
  },
  "default_profile": "balanced"
}
```

### 6. 확인

```bash
localcode --agent general-purpose
```

바로 메시지를 보내보고, 인증/모델 ID 문제가 있으면 다음을 순서대로 의심하세요:

1. `aws sts get-caller-identity`가 실패한다 → 자격 증명 문제
2. `AccessDeniedException` → 콘솔에서 해당 모델 access 활성화 안 함
3. `... with on-demand throughput isn't supported` → 모델 ID에 리전 프리픽스(`us.` 등)가 빠짐
4. `ValidationException: model identifier is invalid` → 오타 또는 해당 리전에서 미지원 모델
5. `no EC2 IMDS role found` / `failed to refresh cached credentials` → **AWS 기본 자격 증명 체인이 아무 것도 못 찾고 EC2 인스턴스 메타데이터까지 떨어진 것**입니다 (당연히 노트북/PC에선 실패). `aws sso login`이나 `localcode login bedrock`을 이미 실행해서 다른 도구(예: AWS CLI, 실제 Claude Code)에서는 되는데 localcode에서만 이 에러가 난다면, 그 세션 자체는 문제가 아니라 **localcode가 그 프로필을 쓰도록 지정을 안 한 것**이 원인일 가능성이 큽니다 — `config.json`의 `providers.<name>.profile`에 그 AWS 프로필 이름을 명시하거나(`localcode login bedrock`은 기본으로 `localcode-bedrock`을 씁니다), 셸에서 `AWS_PROFILE` 환경 변수를 지정하세요. localcode 최신 버전은 이 에러를 감지하면 콘솔에 바로 이 해결법을 안내하는 힌트를 덧붙입니다.
6. `ValidationException: ... StatusCode: 400 ... Your account is not authorized to invoke this API operation` → 자격 증명은 정상적으로 찾았는데(그래서 위 5번과 다르게 IMDS 에러가 아님) **모델 ID 자체가 유효하지 않거나, 그 계정/역할에 그 모델 access가 없는 것**입니다. 흔한 원인:
   - **Bedrock Converse API가 아직 지원하지 않는 모델**을 넣은 경우 (예: `claude-opus-4-8`) — 위 4번째 표(`실제 사용 가능한 모델 ID`)에 없는 모델은 대부분 이 경우입니다. 표에 있는 모델로 바꾸거나, 꼭 그 모델이 필요하면 [Anthropic API 직접 사용](#anthropic-api-직접-사용) provider로 우회하세요.
   - **`[1m]` 접미사를 붙였는데도 이 에러가 난다면** — v0.16.0부터 localcode가 `[1m]`을 인식해서 실제 모델 ID로 자르고 1M-context beta 플래그(`anthropic_beta: context-1m-2025-08-07`)를 `AdditionalModelRequestFields`로 함께 보내지만, **이 beta 플래그 이름/동작은 AWS 문서에서 직접 재확인한 게 아니라 Anthropic 다이렉트 API 관례를 그대로 옮긴 추정치입니다** — Bedrock 쪽에서 다른 이름을 요구하거나 아직 이 beta 자체를 지원하지 않을 수 있습니다. 이 경우 계정에 1M context 모델 access가 켜져 있는지 콘솔에서 확인하고, 여전히 안 되면 `[1m]` 없이 기본 컨텍스트로 우선 시도해보세요.
   - 콘솔의 **Bedrock → Model access**에서 그 모델의 access가 실제로 활성화되어 있는지도 다시 확인하세요 (특정 모델만 안 켜져 있는 경우가 흔합니다).
7. `ValidationException: The model returned the following errors: 'temperature' is deprecated for this model.` → 일부 최신 모델(Opus 계열에서 확인됨)은 `temperature` 필드 자체를 아예 거부합니다. `config.json`의 `profiles.<name>.temperature`를 지정하지 않았어도 예전 localcode는 항상 0.0(기본값)을 Bedrock에 같이 보냈는데, 이 모델들은 그 필드가 존재하는 것 자체를 문제 삼습니다. v0.17.0부터는 `temperature`를 실제로 설정한 프로필에서만 그 필드를 보내므로 별도 조치 없이 해결됩니다 — 이 에러가 계속 나면 localcode 버전을 업데이트하세요.

### 1M context (`[1m]`)

`profiles.<name>.model`에 `[1m]` 접미사를 붙이면(예: `"us.anthropic.claude-sonnet-4-6[1m]"`) localcode가 그 접미사를 떼어 실제 모델 ID로 요청하고, Anthropic의 1M-context beta 플래그(`anthropic_beta: context-1m-2025-08-07`)를 `AdditionalModelRequestFields`로 함께 보냅니다 — Claude Code 설정 화면에 나오는 "Sonnet 4.6 (1M context)" 표기와 같은 관례를 그대로 흉내낸 것입니다. Sonnet 4/4.5/4.6처럼 이 beta를 지원하는 모델을 염두에 두고 만들었지만, **정확한 필드 이름과 동작을 실제 Bedrock 계정으로 검증하지는 못했습니다** — Anthropic 다이렉트 API의 관례를 그대로 옮긴 추정치입니다. 지원하지 않는 모델에 붙이면 Bedrock이 그 필드를 무시할 수도, 에러를 낼 수도 있습니다. 안 되면 위 6번 항목을 참고하고, `[1m]` 없이 기본 컨텍스트로 먼저 시도해보세요.

## Anthropic API 직접 사용

Bedrock을 거치지 않고 `api.anthropic.com`에 바로 붙는 provider입니다. Bedrock Converse API가 아직 지원하지 않는 최신 모델(Opus 4.7/4.8, Sonnet 5, Fable 5 등 — 위 Bedrock 절의 "중요한 제약" 참고)을 쓰려면 이 provider가 필요합니다. `console.anthropic.com`에서 발급한 API 키로 사용량만큼 과금되며, claude.ai Pro/Max 구독과는 별개입니다.

### 1. API 키 발급

[console.anthropic.com](https://console.anthropic.com) → **API Keys**에서 새 키를 만듭니다 (`sk-ant-...` 형식).

### 2. 로그인

```bash
localcode login anthropic
```

키를 입력하면 `~/.localcode/credentials.json`(권한 0600)에 저장됩니다. `config.json`에는 키를 직접 넣지 않아도 됩니다 — `providers.<name>`에 `api_key`를 생략하면 저장된 키를 자동으로 씁니다.

### 3. config.json 예시

```json
{
  "providers": {
    "anthropic": { "type": "anthropic" }
  },
  "profiles": {
    "strong": { "provider": "anthropic", "model": "claude-opus-4-8", "max_tokens": 8192 }
  },
  "agents": {
    "general-purpose": { "profile": "strong" }
  },
  "default_profile": "strong"
}
```

프로젝트 config.json을 git에 커밋해도 안전합니다 — 키 자체는 `~/.localcode/credentials.json`에만 있고 config.json에는 없기 때문입니다. 키를 config.json에 직접 넣고 싶다면 `providers.<name>.api_key`에 지정하면 되지만, 그 경우 파일을 커밋하지 않도록 주의하세요.

## 로컬 LLM (OpenAI-compatible)

Bedrock 없이 완전히 로컬에서 돌리고 싶을 때, 또는 가볍고 빠른 작업(예: `explore` 에이전트)에 로컬 모델을 쓰고 싶을 때 사용합니다.

### LM Studio

1. [LM Studio](https://lmstudio.ai/) 설치 후 원하는 모델 다운로드 (예: Qwen3-30B-A3B).
2. 좌측 **Developer** 탭 → 로컬 서버 시작. 기본 주소는 `http://localhost:1234/v1`.
3. LM Studio에 표시되는 정확한 모델 이름을 `config.json`의 `profiles.<name>.model`에 그대로 씁니다 (이름이 다르면 요청이 실패합니다).

```json
{
  "providers": {
    "local": { "type": "openai-compat", "base_url": "http://localhost:1234/v1" }
  },
  "profiles": {
    "local-fast": { "provider": "local", "model": "qwen3-30b-a3b", "max_tokens": 4096 }
  },
  "agents": {
    "general-purpose": { "profile": "local-fast" }
  },
  "default_profile": "local-fast"
}
```

### vLLM / 그 외 OpenAI 호환 서버 (API 키가 필요한 원격 프록시 포함)

`/chat/completions`를 제공하는 서버라면 무엇이든 `base_url`만 맞추면 동일하게 동작합니다. 인증이 필요하면 `providers.<name>.api_key`를 지정하세요 (`Authorization: Bearer <key>` 헤더로 전달됩니다). 사내망을 거친다면 리버스 프록시/방화벽에서 해당 포트가 열려 있는지 확인하세요.

opencode의 `opencode.jsonc`에서 `@ai-sdk/openai-compatible` provider로 `baseURL`/`apiKey`를 지정해 쓰던 사내 LiteLLM 같은 프록시가 있다면, localcode에서는 같은 정보를 `config.json`에 그대로 옮기면 됩니다:

```json
{
  "providers": {
    "itg": {
      "type": "openai-compat",
      "base_url": "http://105.140.238.68:4000/v1",
      "api_key": "sk-PtnU_sKmNlLLjLjsERblibA"
    }
  },
  "profiles": {
    "nemo": { "provider": "itg", "model": "nvidia/nemotron-3-super", "max_tokens": 4096 }
  },
  "agents": {
    "general-purpose": { "profile": "nemo" }
  },
  "default_profile": "nemo"
}
```

opencode 설정의 `npm`/`name`/`models.<id>.name`/`tool_call` 필드는 opencode의 provider 어댑터 등록 방식(어떤 SDK 패키지를 쓸지, UI에 표시할 이름, 도구 호출 지원 여부 선언)이라 localcode에는 대응 필드가 없습니다 — localcode는 모든 openai-compat provider가 `/chat/completions`에 `tools`를 함께 보내는 방식(OpenAI 함수 호출 규격)으로 동작하고, 서버가 그 요청 형식을 실제로 지원하는지는 별도 설정 없이 그대로 시도합니다.

## Bedrock + 로컬 모델 같이 쓰기

`providers`에 둘 다 등록하고, `agents` 맵에서 작업 종류별로 다른 `profile`을 가리키면 됩니다 — 예를 들어 복잡한 작업은 Bedrock Sonnet, 단순 파일 탐색은 로컬 모델로 라우팅. 자세한 건 [USAGE.md의 "다른 모델로 전환하기"](USAGE.md#다른-모델로-전환하기)를 참고하세요.
