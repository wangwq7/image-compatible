# image-compatible

`image-compatible` is a CLIProxyAPI request-normalizer plugin for the
OpenAI Chat Completions to Codex Responses conversion path.

It performs two operations:

1. Repairs tool outputs where a structured image array was serialized into a
   JSON string. The repaired `function_call_output.output` is an array of
   native Codex `input_image` and `input_text` parts. This reproduces the
   behavior of the CPA source patch `fix(codex): unwrap stringified tool image
   outputs`.
2. Optionally replaces the oldest tool-generated screenshots with text when
   the configured base64 or conservative token estimate is exceeded. Images in
   ordinary user messages are not eligible for trimming.

The context estimate is intentionally conservative, but it is still an
estimate. Exact image token accounting is provider-specific, so no proxy-side
plugin can mathematically guarantee the upstream model's token count without a
provider count-tokens API.

## CPA configuration

Place the Linux artifact at the configured plugin directory as
`image-compatible.so`. The basename must match the config key.

```yaml
plugins:
  enabled: true
  dir: /path/to/plugins
  configs:
    image-compatible:
      enabled: true
      priority: 10
      models:
        - gpt-5.6-sol
        - p/gpt-5.6-sol
      source_formats:
        - openai
      target_formats:
        - codex
      tool_name_patterns:
        - screenshot
        - view_image
      repair_stringified_images: true
      trim_old_tool_images: true
      context_window_tokens: 272000
      reserve_tokens: 32000
      max_base64_bytes: 25165824
      estimated_tokens_per_image: 8192
      bytes_per_text_token: 2.0
      keep_recent_tool_images: 2
      placeholder: "[Earlier tool screenshot omitted by image-compatible]"
```

The default scope is deliberately narrow:

- only Chat Completions input (`openai`);
- only Codex upstream conversion (`codex`);
- only `gpt-5.6-sol` / `p/gpt-5.6-sol`;
- only images inside matching screenshot-like `function_call_output` items can
  be trimmed; other tool-generated images are preserved.

`keep_recent_tool_images` is a preferred minimum. If retaining that many
images would still exceed the configured limit, the plugin continues trimming
from oldest to newest until the conservative estimate is below the limit.

## Build

Native build:

```bash
make build
make smoke
```

Linux AMD64 build in Docker:

```bash
make linux-amd64
```

Artifacts are written to `dist/`.
