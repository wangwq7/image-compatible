# image-compatible

`image-compatible` is a CLIProxyAPI request-normalizer plugin for the
OpenAI Chat Completions to Codex Responses conversion path.

It performs one operation: when CPA has converted a Chat Completions tool
message into a Codex `function_call_output`, the plugin detects image content
that is still serialized inside the string-valued `output` field and restores
it to native Codex content parts.

For example:

```json
{
  "type": "function_call_output",
  "output": "[{\"type\":\"input_image\",\"image_url\":\"data:image/png;base64,...\"}]"
}
```

becomes:

```json
{
  "type": "function_call_output",
  "output": [
    {
      "type": "input_image",
      "image_url": "data:image/png;base64,..."
    }
  ]
}
```

Only the protocol structure changes. Image bytes, metadata, and part order are
preserved. When a repaired tool output contains an unrecognized structured part,
the plugin leaves that part unchanged; it never coerces it into text merely because
an image appears alongside it.

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
```

CPA owns the `enabled` field. Its management page and
`PATCH /v0/management/plugins/image-compatible/enabled` endpoint can disable
or re-enable the plugin at runtime without changing the global plugin switch.

The default scope is deliberately narrow:

- Chat Completions input (`openai`);
- Codex upstream conversion (`codex`);
- `gpt-5.6-sol` and `p/gpt-5.6-sol`.

## Build

Native build:

```bash
make build
make smoke
make strict
```

Linux AMD64 build in Docker:

```bash
make linux-amd64
```

Artifacts are written to `dist/`.
