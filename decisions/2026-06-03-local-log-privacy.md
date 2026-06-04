# Local transcript log privacy boundary

**Date:** 2026-06-03
**Status:** Adopted (plan `runtime-token-usage` v1.2, Phase A0)
**Scope:** The daemon-side transcript collector that feeds `ambient_usage`
(local Claude Code sessions never dispatched through `agent_task_queue`).

## Decision

The local-transcript collector reads **numbers and identifiers only**. It MUST
NEVER deserialize, read, transmit, persist, or log the natural-language content
of a transcript — no `message.content`, no prompt text, no tool input/output
bodies, no file contents.

The only fields the collector is permitted to extract from a transcript line are:

| Field         | Purpose                                  |
| ------------- | ---------------------------------------- |
| `session_id`  | dedup anchor + task-session exclusion    |
| `message.id`  | per-message dedup key (坑#1)             |
| `requestId`   | per-request dedup key (坑#1)             |
| `timestamp`   | UTC bucket time (`event_at`)             |
| `model`       | per-model rollup dimension               |
| `provider`    | adapter identity (e.g. `claude`)         |
| `usage.{input_tokens, output_tokens, cache_read_input_tokens, cache_creation_input_tokens}` | the four token counts |

Everything else on the line is ignored. The upload payload struct and the
`ambient_usage` row carry exactly these fields and no `content`/`text`/`prompt`
field exists on either type — this is enforced, not merely intended.

## Why

These transcripts live at `~/.claude/projects/**/*.jsonl` on the user's own
machine and contain the full text of every local Claude Code conversation —
source code, secrets pasted into prompts, private reasoning. Token *counts* are
billing/observability metadata the user already expects us to aggregate (we do
it for daemon-dispatched tasks today). Conversation *content* is not ours to
read, and shipping it to the server would turn an observability feature into a
data-exfiltration vector.

The boundary is drawn at "numbers leave the machine, words never do" because it
is simple to state, simple to test, and leaves no judgement call at the parse
site: an assistant line's `usage` block and a handful of ids are the whole
contract.

## How it is enforced

1. **Type-level:** the daemon `UsageEvent` upload struct and the
   `ambient_usage` table schema contain only the fields in the table above.
   There is no field that could hold message content, so a careless change
   cannot smuggle it through — adding one would be a visible, reviewable diff.
2. **Test-level:** a test asserts (via reflection / field enumeration over the
   upload struct) that no field name matches `content|prompt|text|body`, and
   that a fixture transcript line with a populated `message.content` produces an
   upload payload whose serialized form does not contain that content string.
3. **Fail-soft parsing:** the adapter extracts the numeric/id fields by key and
   never reflects the whole line into a rich object; a malformed or unexpected
   line is skipped, not partially deserialized (坑#3).

## Boundary, restated for future adapters

Any future collector adapter (Codex, OTEL, …) inherits this doctrine
unchanged. The `Collector` contract is "return `[]UsageEvent` of numbers and
ids"; an adapter that needs to read content to do its job is out of contract and
must be redesigned, not exempted.
