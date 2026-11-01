### Added
- Route `anthropic-compatible` custom provider nodes to the Claude Messages endpoint instead of forcing every node through `/chat/completions`. `getProviderConfig` now recognizes the node type (and its composed `<prefix>/<id>` form) and builds a Messages config: `x-api-key` raw auth, `anthropic-version: 2023-06-01`, `Format: providers.FormatClaude` and `RequireClaudeToolType`. This fixes the pre-existing gap for *all* Messages-speaking custom nodes, not only the zcode bridge.
- Dashboard quota tracking for `anthropic-compatible-zcode` connections (`internal/handlers/dashboard/usage_zcode.go`): `GET <baseUrl>/quota` on the local zcode-bridge is polled with the optional Bearer token, and the `{plan,quotas}` rows become Quota Tracker entries (`"<model> (<period>)"` keys, used/total/remaining/remainingPercentage/resetAt, plan name from the bridge). No frontend change needed; `parseQuotaData` already consumes this shape.
- `ProviderAliases["anthropic-compatible-zcode"] = "zai"` so model context windows resolve through the models.dev zai entry like the glm provider.

### Changed
- A Messages-speaking custom node with no registered executor is dispatched through `executor.Default()` instead of `forwardRequest`, so the Claude reply is translated back for Chat Completions clients (`UpstreamClaude` path) rather than passed through raw.
- The loopback SSRF exemption (`WithAllowLoopback`) now also honors the per-config `LocalByDesign` flag, which `claudeCompatibleConfig` sets when the node's configured baseUrl host is loopback (`isLoopbackBase`). Registry-only `IsLocalByDesign` could not know about custom nodes.

### Tests
- `internal/handlers/chat/connections_claude_node_test.go`: `claudeMessagesURL`, `isAnthropicCompatibleProvider`, `isLoopbackBase`.
- `internal/handlers/dashboard/usage_zcode_test.go`: quota parsing + dispatch, against an httptest bridge stub.
- Verified end to end against the live zcode-bridge: non-stream and stream on `/v1/chat/completions` (`zcode/*`), `/v1/messages` passthrough (non-stream and SSE), a tool-call roundtrip, and the dashboard quota panel showing the real ZCode Start Plan windows.
