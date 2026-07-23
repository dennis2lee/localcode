# Model setup guide

localcode supports three provider types:

| Provider type | What it is |
|---|---|
| `bedrock` | Amazon Bedrock, cloud hosted Claude |
| `anthropic` | The Anthropic API directly, using a console.anthropic.com key |
| `openai-compat` | Any OpenAI compatible endpoint, such as LM Studio or vLLM |

This document covers how to set each one up for real. Read [USAGE.md](USAGE.md#config-file-configjson) first for what each config field means.

Both cloud providers can be authenticated with `localcode login <bedrock|anthropic>`. See [USAGE.md, authenticating with /login](USAGE.md#authenticating-with-login).

Signing in with a claude.ai Pro or Max subscription is not supported. That flow needs a private OAuth client issued specifically for Claude Code. Those credentials are not public, and a third party tool reproducing them would risk violating the Anthropic terms of service.

## Amazon Bedrock

### 1. Prepare the AWS account

1. Create an AWS account if you do not have one.
2. Open **Bedrock, then Model access** at [console.aws.amazon.com/bedrock/home#/modelaccess](https://console.aws.amazon.com/bedrock/home#/modelaccess) and enable access for the Claude models you want. Skipping this makes every call fail with `AccessDeniedException`.
3. Anthropic model availability differs by region. Check the region column in the model table below.

### 2. Set up credentials

localcode uses the standard AWS credential chain with no extra configuration. `internal/provider/bedrock.go` calls `config.LoadDefaultConfig`. Any one of these is enough.

```bash
# Option 1: access keys
aws configure

# Option 2a: SSO through the AWS CLI
aws sso login --profile your-profile
export AWS_PROFILE=your-profile

# Option 2b: localcode's own SSO login, works without the AWS CLI installed
localcode login bedrock
# Prompts for start URL, region, account, and role, then tells you the
# providers.<name>.profile value to put in config.json.

# Option 3: environment variables
export AWS_ACCESS_KEY_ID=...
export AWS_SECRET_ACCESS_KEY=...
export AWS_SESSION_TOKEN=...   # only for temporary credentials

# Verify
aws sts get-caller-identity
```

On EC2, ECS, or any container with an instance or task role, the role is picked up automatically.

### 3. Regions and model IDs

Two different things both carry a region, which is the confusing part.

| Setting | Meaning |
|---|---|
| `providers.<name>.region` | The region handed to the SDK, the same as `AWS_REGION` |
| Prefix on the model ID | Part of the cross region inference profile ID, separate from the above |

From Sonnet 4.5 onward, Bedrock refuses calls to a base model ID and requires a cross region inference profile ID instead, meaning the base ID with `us.`, `eu.`, `global.`, or similar in front. Calling a base ID directly produces:

```
Invocation of model ID anthropic.claude-sonnet-4-5-20250929-v1:0 with on-demand
throughput isn't supported. Retry your request with the ID or ARN of an
inference profile that contains this model.
```

So `profiles.<name>.model` in config.json needs the full prefixed ID.

### 4. Usable model IDs, as of 2026-07

This table reflects the **Bedrock Converse and ConverseStream API**, which is what localcode actually calls through `bedrockruntime.ConverseStream` in [internal/provider/bedrock.go](internal/provider/bedrock.go). Names here may lag the newest Anthropic model names, because Bedrock manages its own rollout schedule and naming separately.

| Model | Base model ID | Region prefixes | Converse API |
|---|---|---|---|
| Claude Opus 4.6 | `anthropic.claude-opus-4-6-v1` | `global.` `us.` `eu.` `jp.` `apac.` | Yes |
| Claude Sonnet 4.6 | `anthropic.claude-sonnet-4-6` | `global.` `us.` `eu.` `jp.` | Yes |
| Claude Sonnet 4.5 | `anthropic.claude-sonnet-4-5-20250929-v1:0` | `global.` `us.` `eu.` `jp.` | Yes |
| Claude Opus 4.5 | `anthropic.claude-opus-4-5-20251101-v1:0` | `global.` `us.` `eu.` | Yes |
| Claude Haiku 4.5 | `anthropic.claude-haiku-4-5-20251001-v1:0` | `global.` `us.` `eu.` | Yes |

For Sonnet 4.5 on a US profile the model ID is `us.anthropic.claude-sonnet-4-5-20250929-v1:0`. For global routing use something like `global.anthropic.claude-opus-4-6-v1`.

| Prefix | When to use it |
|---|---|
| `global.` | Default choice. No price premium, highest availability. |
| `us.` `eu.` `jp.` `apac.` | Only when you have a data residency requirement. Roughly 10% price premium. |

> **Important limitation.** Claude Opus 4.7, Opus 4.8, Sonnet 5, and Fable 5 are absent from the table on purpose. Per Anthropic's documentation these models have no ARN style model ID and are served only through Bedrock's newer Messages API gateway (`bedrock-mantle`, `/anthropic/v1/messages`). localcode's Bedrock provider implements the Converse API only, so **these newest models cannot be used through Bedrock right now.** Either pick a model from the table, or use the [Anthropic API directly](#using-the-anthropic-api-directly), which has no such restriction.

Availability and regions change often. For an authoritative current list:

```bash
aws bedrock list-foundation-models --region=us-west-2 --by-provider anthropic \
  --query "modelSummaries[*].modelId"
```

### 5. config.json example

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
    "explore":         { "profile": "cheap" }
  },
  "default_profile": "balanced"
}
```

### 6. Verify

```bash
localcode --agent general-purpose
```

Send a message. If authentication or the model ID is wrong, work through these in order.

| Error | Cause and fix |
|---|---|
| `aws sts get-caller-identity` fails | A credentials problem, not a localcode problem. Fix that first. |
| `AccessDeniedException` | Model access is not enabled for that model in the console. |
| `... with on-demand throughput isn't supported` | The model ID is missing its region prefix, such as `us.`. |
| `ValidationException: model identifier is invalid` | A typo, or a model not offered in that region. |
| `no EC2 IMDS role found`, `failed to refresh cached credentials` | See below. |
| `ValidationException: ... Your account is not authorized to invoke this API operation` | See below. |
| `ValidationException: ... 'temperature' is deprecated for this model` | See below. |

**`no EC2 IMDS role found` or `failed to refresh cached credentials`**

The AWS credential chain found nothing and fell all the way through to EC2 instance metadata, which of course fails on a laptop.

If you already ran `aws sso login` or `localcode login bedrock`, and other tools such as the AWS CLI work fine, the session is not the problem. The usual cause is that localcode was never told to use that profile. Fix it either way:

* Set `providers.<name>.profile` in config.json to the AWS profile name. `localcode login bedrock` writes `localcode-bedrock` by default.
* Or export `AWS_PROFILE` in your shell.

Recent versions detect this error and append the same advice to the console output.

**`Your account is not authorized to invoke this API operation`**

Credentials resolved correctly, so unlike the IMDS case above this is not an auth chain problem. Either the model ID is not valid, or that account and role has no access to it. Common causes:

* **The model is not supported by the Bedrock Converse API**, for example `claude-opus-4-8`. Anything missing from the model table above usually falls here. Switch to a listed model, or use the [Anthropic API directly](#using-the-anthropic-api-directly).
* **You added a `[1m]` suffix and still get this.** See the 1M context section below for what is and is not verified.
* **Model access is off for that specific model.** Recheck Bedrock, then Model access in the console. Having only one model left disabled is common.

**`'temperature' is deprecated for this model`**

Some newer models, confirmed on Opus, reject the `temperature` field outright. Older localcode always sent `temperature`, even the 0.0 default, when a profile never configured one, and these models object to the field being present at all.

From v0.17.0 the field is sent only when a profile explicitly sets a non zero temperature, so no action is needed. If you still see this, update localcode.

### 1M context with the `[1m]` suffix

Adding `[1m]` to `profiles.<name>.model`, for example `"us.anthropic.claude-sonnet-4-6[1m]"`, makes localcode strip the suffix, request the real model ID, and pass Anthropic's 1M context beta flag (`anthropic_beta: context-1m-2025-08-07`) through `AdditionalModelRequestFields`. This mirrors the "Sonnet 4.6 (1M context)" shorthand shown in Claude Code's own model settings.

It was built for models that support that beta, such as Sonnet 4, 4.5, and 4.6.

> **Not verified.** The exact field name and behavior were never confirmed against a real Bedrock account with 1M context access. They are carried over from the Anthropic direct API convention. Bedrock may want a different name, or may not support this beta yet.

If it does not work, confirm 1M context model access is enabled in the console, then try again without `[1m]` to fall back to the default context.

## Using the Anthropic API directly

This provider connects to `api.anthropic.com` without going through Bedrock. It is the way to reach the newest models that the Bedrock Converse API does not carry yet, such as Opus 4.7 and 4.8, Sonnet 5, and Fable 5. See the important limitation note in the Bedrock section above.

Billing is per usage against a key issued at `console.anthropic.com`, entirely separate from a claude.ai Pro or Max subscription.

### 1. Create an API key

Go to [console.anthropic.com](https://console.anthropic.com), open **API Keys**, and create a key. It looks like `sk-ant-...`.

### 2. Log in

```bash
localcode login anthropic
```

The key is saved to `~/.localcode/credentials.json` with mode 0600. You do not need to put it in config.json. When `providers.<name>` omits `api_key`, the stored key is used automatically.

### 3. config.json example

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

Committing a project config.json to git is safe with this setup, because the key lives only in `~/.localcode/credentials.json`. You can put the key directly in `providers.<name>.api_key` instead, but then keep that file out of version control.

## Local LLMs over an OpenAI compatible endpoint

Use this to run entirely locally without Bedrock, or to point light and fast work such as the `explore` agent at a local model.

### LM Studio

1. Install [LM Studio](https://lmstudio.ai/) and download a model, for example Qwen3-30B-A3B.
2. Open the **Developer** tab on the left and start the local server. The default address is `http://localhost:1234/v1`.
3. Copy the exact model name LM Studio displays into `profiles.<name>.model`. A mismatched name makes the request fail.

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

### vLLM and other OpenAI compatible servers, including remote proxies

Any server that exposes `/chat/completions` works the same way once `base_url` points at it. For servers that require authentication, set `providers.<name>.api_key` and it is sent as an `Authorization: Bearer <key>` header. On a corporate network, check that the port is open through any reverse proxy or firewall in between.

If you already run a proxy such as an internal LiteLLM and configured it in opencode's `opencode.jsonc` through the `@ai-sdk/openai-compatible` provider with `baseURL` and `apiKey`, the same values move straight into config.json:

```json
{
  "providers": {
    "itg": {
      "type": "openai-compat",
      "base_url": "http://YOUR-PROXY-HOST:4000/v1",
      "api_key": "sk-REPLACE-WITH-YOUR-OWN-KEY"
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

Some opencode fields have no localcode equivalent:

| opencode field | Why there is no equivalent |
|---|---|
| `npm` | Selects which SDK package to load. localcode has one built in openai-compat client. |
| `name`, `models.<id>.name` | Display names for the opencode UI. |
| `tool_call` | Declares tool call support. localcode always sends `tools` on `/chat/completions` using the OpenAI function calling format, and simply attempts the request. |

## Mixing Bedrock and local models

Register both under `providers`, then point each entry in the `agents` map at a different `profile`. For example, route complex work to Bedrock Sonnet and simple file exploration to a local model.

See [USAGE.md, switching models](USAGE.md#switching-models) for details.
