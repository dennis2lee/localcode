# Model setup guide

Choose a provider, configure a model profile, and assign that profile to an agent. localcode supports three provider types:

| Provider type | Service |
|---|---|
| `bedrock` | Amazon Bedrock, cloud hosted Claude |
| `anthropic` | The Anthropic API directly, using a console.anthropic.com key |
| `openai-compat` | Any OpenAI-compatible endpoint, such as LM Studio or vLLM |

See [USAGE.md](USAGE.md#config-file-configjson) for configuration field definitions.

Use `localcode login <bedrock|anthropic>` for cloud authentication. See [USAGE.md, authenticating with /login](USAGE.md#authenticating-with-login).

claude.ai Pro and Max subscriptions are not supported. That sign in flow requires a private OAuth client issued for Claude Code. localcode does not reproduce those credentials because of the Anthropic terms of service risk.

## Amazon Bedrock

### 1. Prepare the AWS account

1. Create an AWS account if you do not have one.
2. Open **Bedrock, then Model access** at [console.aws.amazon.com/bedrock/home#/modelaccess](https://console.aws.amazon.com/bedrock/home#/modelaccess) and enable access for the Claude models you want. Skipping this makes every call fail with `AccessDeniedException`.
3. Anthropic model availability differs by region. Check the region column in the model table below.

### 2. Set up credentials

AWS credentials are required only when a request uses Bedrock. Startup does not read AWS configuration. Local models require neither AWS files nor a Claude installation, including when merged global and project settings retain an unused Bedrock entry.

`internal/provider/bedrock.go` calls `config.LoadDefaultConfig` on the first Bedrock request. Configure one source in the standard AWS credential chain:

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

EC2 instance roles and ECS or container task roles are detected automatically.

### 3. Regions and model IDs

The provider region and model ID prefix are separate settings:

| Setting | Meaning |
|---|---|
| `providers.<name>.region` | SDK request region, equivalent to `AWS_REGION` |
| Prefix on the model ID | Routing scope of the cross region inference profile |

Set `profiles.<name>.model` to the full inference profile ID. For Sonnet 4.5 and later models in the table below, add a supported prefix such as `us.`, `eu.`, or `global.` to the base ID. A direct base ID request produces:

```
Invocation of model ID anthropic.claude-sonnet-4-5-20250929-v1:0 with on-demand
throughput isn't supported. Retry your request with the ID or ARN of an
inference profile that contains this model.
```

### 4. Usable model IDs, checked 2026-08-19

The table covers **Bedrock Converse and ConverseStream**, used by `bedrockruntime.ConverseStream` in [internal/provider/bedrock.go](../internal/provider/bedrock.go). Bedrock model names and availability follow a separate release schedule from the Anthropic API.

| Model | Base model ID | Region prefixes | Converse API |
|---|---|---|---|
| Claude Opus 4.6 | `anthropic.claude-opus-4-6-v1` | `global.` `us.` `eu.` `jp.` `apac.` | Yes |
| Claude Sonnet 4.6 | `anthropic.claude-sonnet-4-6` | `global.` `us.` `eu.` `jp.` | Yes |
| Claude Sonnet 4.5 | `anthropic.claude-sonnet-4-5-20250929-v1:0` | `global.` `us.` `eu.` `jp.` | Yes |
| Claude Opus 4.5 | `anthropic.claude-opus-4-5-20251101-v1:0` | `global.` `us.` `eu.` | Yes |
| Claude Haiku 4.5 | `anthropic.claude-haiku-4-5-20251001-v1:0` | `global.` `us.` `eu.` | Yes |

Examples: `us.anthropic.claude-sonnet-4-5-20250929-v1:0` for US Sonnet 4.5 routing, or `global.anthropic.claude-opus-4-6-v1` for global Opus 4.6 routing.

| Prefix | When to use it |
|---|---|
| `global.` | Default choice. No price premium, highest availability. |
| `us.` `eu.` `jp.` `apac.` | Only when you have a data residency requirement. Roughly 10% price premium. |

> **Unverified with localcode:** Claude Opus 5, Opus 4.8, Opus 4.7, Sonnet 5, and Fable 5 on Bedrock. localcode uses the older of the two Claude integrations:
>
> | | Models | How it is called |
> |---|---|---|
> | Legacy, ARN-versioned | Opus 4.6 and earlier (the table above) | `InvokeModel` and `Converse` on `bedrock-runtime`, including localcode's `bedrockruntime.ConverseStream` |
> | Claude in Amazon Bedrock | Opus 4.7 and later | The Messages API at `https://bedrock-mantle.{region}.api.aws/anthropic/v1/messages`, with plain IDs like `anthropic.claude-opus-5` |
>
> The newer models have no ARN-versioned ID and are absent from Anthropic's legacy model table. Anthropic documentation describes access through `InvokeModel` on `bedrock-runtime`, using the same infrastructure as the Messages endpoint. The AWS Opus 4.8 launch post also describes Converse access with `us.anthropic.claude-opus-4-8`. These descriptions differ. This project has not verified them with a real account.
>
> An unsupported request can return `ValidationException: ... Your account is not authorized to invoke this API operation`. Use a model from the table above or the [Anthropic API directly](#using-the-anthropic-api-directly).

Check current model IDs and regional availability with:

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

Send a message. Check failures in this order:

| Error | Cause and fix |
|---|---|
| `aws sts get-caller-identity` fails | Resolve AWS credentials before testing localcode. |
| `AccessDeniedException` | Model access is not enabled for that model in the console. |
| `... with on-demand throughput isn't supported` | The model ID is missing its region prefix, such as `us.`. |
| `ValidationException: model identifier is invalid` | A typo, or a model not offered in that region. |
| `no EC2 IMDS role found`, `failed to refresh cached credentials` | See below. |
| `ValidationException: ... Your account is not authorized to invoke this API operation` | See below. |
| `ValidationException: ... 'temperature' is deprecated for this model` | See below. |

**`no EC2 IMDS role found` or `failed to refresh cached credentials`**

The AWS credential chain found no usable credentials and attempted EC2 instance metadata. A local workstation normally has no instance role.

If `aws sso login` or `localcode login bedrock` succeeded, confirm that localcode uses the same AWS profile:

* Set `providers.<name>.profile` in config.json to the AWS profile name. `localcode login bedrock` writes `localcode-bedrock` by default.
* Or export `AWS_PROFILE` in your shell.

Recent versions include this profile advice in the error output.

**`Your account is not authorized to invoke this API operation`**

This error occurs after credential resolution. Check the model ID, API support, and account access:

* Unsupported or unverified Converse model, such as `claude-opus-4-8`. See the limitation below the model table. Use a listed model or the [Anthropic API directly](#using-the-anthropic-api-directly).
* A `[1m]` suffix on the model ID. See the 1M context section for verification limits.
* Disabled access for the requested model. Check Bedrock, then Model access in the console.

**`'temperature' is deprecated for this model`**

Update localcode if an unconfigured temperature causes this error. Some newer models, including Opus, reject the field. Older versions sent it even when the profile had no temperature setting.

Since v0.17.0, localcode sends the field only for an explicitly configured nonzero temperature.

### 1M context with the `[1m]` suffix

The `[1m]` suffix requests the Anthropic 1M context beta. Example: `"us.anthropic.claude-sonnet-4-5-20250929-v1:0[1m]"` in `profiles.<name>.model`. localcode strips the suffix from the request model ID. It sends `anthropic_beta: context-1m-2025-08-07` through `AdditionalModelRequestFields`. The suffix follows the shorthand in Claude Code model settings.

**Opus 4.6 and Sonnet 4.6 do not need the suffix.** Anthropic's Bedrock documentation, checked 2026-08-19, lists a 1M token context for those models and for newer models unverified with localcode. Sonnet 4.5 and older models retain a 200k default context.

`internal/modelinfo` uses 1M for Opus 4.6, Sonnet 4.6, and 5-series families. It uses 200k for other families. A profile `context_window` overrides these values.

> **Not verified:** the beta field and behavior have not been tested with a real Bedrock account that has 1M context access. The implementation uses the Anthropic direct API convention. Bedrock may require a different field or may not support the beta.

If the request fails, check 1M context access in the console. Retry without `[1m]` to use the default context.

Bedrock limits request bodies to 20 MB regardless of context size. Attachments can exceed that limit while remaining within the token limit.

## Using the Anthropic API directly

Use this provider for direct access to `api.anthropic.com`. It avoids the Bedrock Converse compatibility limitation for Opus 5, Opus 4.8, Opus 4.7, Sonnet 5, and Fable 5. See the limitation in the Bedrock section above.

Usage is billed to an API key issued at `console.anthropic.com`. Billing is separate from claude.ai Pro and Max subscriptions.

### 1. Create an API key

Go to [console.anthropic.com](https://console.anthropic.com), open **API Keys**, and create a key. It looks like `sk-ant-...`.

### 2. Log in

```bash
localcode login anthropic
```

The key is stored in `~/.localcode/credentials.json` with mode 0600. The provider uses that key when `providers.<name>.api_key` is omitted.

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

This setup keeps the API key out of the project config.json. Check for other secrets before committing that file. If you set `providers.<name>.api_key` directly, keep the file out of version control.

## Local LLMs over an OpenAI-compatible endpoint

Use a local endpoint to run without Bedrock. Assign a local profile to an agent such as `explore` to mix local and hosted models.

### LM Studio

1. Install [LM Studio](https://lmstudio.ai/) and download a model, for example Qwen3-30B-A3B.
2. Open the **Developer** tab on the left and start the local server. The default address is `http://localhost:1234/v1`.
3. Copy the exact model name LM Studio displays into `profiles.<name>.model`. A mismatched name makes the request fail.

Since v0.38.0, localcode reads the context limit from the server. The configured server limit takes precedence over the model's capacity. For example, a server configured for 8k requires requests to fit within 8k even if the model supports 128k.

| Profile field | Use |
|---|---|
| `context_window` | Set only when the server does not report a context limit |
| `max_tokens` | Maximum response length; default 4096 |

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

### A local model that stops mid-task

Some local models end a turn after a tool result while describing unfinished work. For known affected families, localcode asks the model whether the task is complete — with the tools on the request but not callable — and sends a continuation only when the answer says work remains, up to three times. For other models, set `keep_going` on the profile:

```json
{
  "profiles": {
    "local-fast": { "provider": "local", "model": "qwen3-30b-a3b", "keep_going": 3 }
  }
}
```

Set `-1` to disable a model's default continuation behavior. See [USAGE.md](USAGE.md#a-model-that-stops-mid-task) for excluded cases, including a model question that requires user input.

### vLLM and other OpenAI-compatible servers, including remote proxies

Set `base_url` to the OpenAI-compatible server that provides `/chat/completions`. For authentication, set `providers.<name>.api_key`. localcode sends it as `Authorization: Bearer <key>`. Confirm that any reverse proxy and firewall permit the connection.

For an existing LiteLLM or similar proxy in opencode's `opencode.jsonc`, copy the `@ai-sdk/openai-compatible` provider's `baseURL` and `apiKey` values to `base_url` and `api_key`:

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
| `npm` | SDK package selection. localcode uses one built-in `openai-compat` client. |
| `name`, `models.<id>.name` | Display names for the opencode UI. |
| `tool_call` | Tool support declaration. localcode sends `tools` on `/chat/completions` in OpenAI function calling format without a separate capability setting. |

## Mixing Bedrock and local models

Register both under `providers`, then point each entry in the `agents` map at a different `profile`. For example, route complex work to Bedrock Sonnet and simple file exploration to a local model.

See [USAGE.md, switching models](USAGE.md#switching-models) for details.
