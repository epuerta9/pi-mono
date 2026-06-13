# Pi Harness in Go — Design Doc

> Porting pi's agent loop + extensibility to a statically compiled Go binary

**Author:** Claude / epuerta9
**Date:** 2026-06-13
**Status:** Draft for discussion

---

## 1. Goal & Scope

Reproduce pi's agent harness (the TypeScript `pi-mono`) as a single statically
compiled Go binary, keeping pi's conceptual model and ~all of its extension
power, while accepting the one constraint Go imposes: **you cannot import
arbitrary user `.go` code at runtime.**

The design keeps:

- The agent loop and its 5 core mechanisms (below).
- The full extension *API surface* (events, tools, commands, providers, UI).
- Most of the extension *power*, by routing each surface to the right Go
  mechanism instead of forcing everything through one.

The one thing that does not survive static compilation: extensions rendering
arbitrary host UI objects (e.g. pi's `diff.ts` subclassing `CustomEditor` /
handing back a live TUI `Component`). That is replaced with a **declarative
widget protocol.**

---

## 2. Layering (mirror pi's split — do not conflate)

pi deliberately separates the loop *mechanism* from the *product*. Keep this.

| Package | Role |
|---|---|
| `agentcore/` | Provider-agnostic loop. Knows nothing about coding, tools-on-disk, UI, or extensions. ≈ the `pi-agent` package. |
| `codingagent/` | The product: real tools (bash/read/edit), TUI, sessions, compaction, **and** the extension host. Consumes `agentcore`. |
| `llm/` | Provider clients (Anthropic, OpenAI, Google). ≈ `pi-ai`. |
| `ext/` | Extension runtime: Starlark host + registry + dispatch. |

**Rule:** `agentcore` has zero imports from `codingagent` or `ext`. Extensibility
is layered **on top of** the loop, exactly as pi does it — the loop has no
concept of "extension."

---

## 3. The core loop — 5 mechanisms → Go

The loop skeleton is intentionally boring:

```
build context → stream assistant response → got tool calls?
   ├─ no  → check follow-ups → stop or continue
   └─ yes → execute them → append results → loop
```

All sophistication is in *how it's parameterized.* Five mechanisms:

### 3.1 The `AgentMessage` ↔ LLM boundary (highest-leverage abstraction)

The agent operates on `AgentMessage` = LLM messages ∪ custom types. The LLM only
ever sees `user`/`assistant`/`toolResult`. Translation happens at exactly **one**
place, right before the API call:

```
AgentMessage[] → transformContext() → convertToLLM() → []llm.Message → LLM
```

This single chokepoint buys: UI-only messages, custom message types, context
pruning/compaction, injected synthetic context — all invisible to the loop.

```go
type AgentMessage interface{ isAgentMessage() }   // sealed-ish union
type ConvertToLLM func([]AgentMessage) []llm.Message
type TransformCtx func(ctx context.Context, in []AgentMessage) ([]AgentMessage, error)
```

### 3.2 Events as a stream, UI as a pure subscriber

The loop pushes typed events; the renderer subscribes. In Go this is cleaner
than JS: a channel.

```go
type AgentEvent interface{ isAgentEvent() }   // AgentStart, MessageUpdate,
                                              // ToolExecStart/End, TurnEnd, ...
// loop runs in a goroutine, sends on chan AgentEvent; UI ranges over it.
```

### 3.3 Tools are data; errors are values

A tool is `{name, description, schema, execute}`. The loop validates args against
the schema, calls `execute`, and turns a returned error into an `isError` tool
result (never a process crash) — feedback to the model.

```go
type Tool struct {
    Name, Label, Description string
    Schema  json.RawMessage           // JSON Schema
    Execute func(ctx context.Context, id string, args json.RawMessage,
                 onUpdate func(ToolResult)) (ToolResult, error)
}
// loop: res, err := tool.Execute(...); if err != nil { res = errResult(err) }
```

### 3.4 Steering + follow-up = the concurrency model

Two injection points:

- **Steering:** checked *after each tool call.* If the user typed, remaining tool
  calls are skipped and the message is injected before the next turn → **interrupt
  semantics.**
- **Follow-up:** checked when the agent *would stop*; lets it continue → **queue
  semantics.**

In pi these are awaited callbacks over a queue. In Go they are channel reads.

```go
type LoopConfig struct {
    Steering <-chan []AgentMessage   // select after each tool
    FollowUp <-chan []AgentMessage   // drain at the stop boundary
    // ...
}
// after each tool: select { case m := <-cfg.Steering: skip rest, inject m
//                           default: }
```

### 3.5 The loop is a pure function of its config

All policy is injected; the loop is stateless mechanism.

```go
type LoopConfig struct {
    Model        llm.Model
    ConvertToLLM ConvertToLLM
    TransformCtx TransformCtx                          // optional
    GetAPIKey    func(provider string) (string, error) // expiring tokens
    Steering     <-chan []AgentMessage
    FollowUp     <-chan []AgentMessage
    Stream       llm.StreamFn                          // injectable for tests
}

func Run(ctx context.Context, prompts []AgentMessage,
         cfg LoopConfig, events chan<- AgentEvent) ([]AgentMessage, error)
```

**Net:** the loop maps onto goroutines/channels more naturally than onto JS
promises. This is the easy, high-confidence part of the port.

---

## 4. Extensibility — the actual hard part

pi's extension power comes from two **separable** properties:

- **(a)** the breadth of the API surface (events + tools + commands + providers + UI), and
- **(b)** extensions being full TypeScript with the host's exact types, hot-imported
  via a TS runtime (jiti).

**Key realization:** (a) ports to Go *entirely* — the API surface is just a set of
host functions you expose. You only make a decision about (b). And you keep most
of (b) by matching each surface to the right mechanism instead of forcing one.

### 4.1 The routing table

| Extension surface | Go mechanism | Why |
|---|---|---|
| Tools, `on(event)` handlers, commands, context/tool interception | **Starlark** | Pure logic. Sandboxed, deterministic, hot-reload. ~80% of real extensions. |
| Rich/custom TUI, custom editors | **Compiled-in Go iface** *or* **declarative widgets** | Starlark can't hold Go `Component` pointers. pi's `setWidget` already accepts plain `[]string` — expose *that*. |
| Custom model providers / APIs | **Compiled-in Go** | `registerProvider` needs real HTTP/stream code. |
| Heavy / cross-language extensions | **MCP (subprocess JSON-RPC)** | Crash-isolated, any language, ecosystem standard, free. |

### 4.2 Why Starlark for the logic-plane

- `go.starlark.net`: mature, embeddable, Python-shaped, **deterministic** and
  **sandboxed by default** (no fs/network unless you hand it a builtin).
- You expose pi's `ExtensionAPI` surface as Go-implemented Starlark builtins:
  `pi.register_tool`, `pi.on`, `pi.register_command`, `pi.exec`, `ctx.ui.select`, …
- JSON Schema replaces TypeBox for tool params.
- Hot-reloadable, safe to run untrusted, no compile step.

**What you give up vs TS extensions** (confined to the UI-heavy minority):

- No shared host *types*: the Starlark/Go boundary is values, not pointers, so an
  extension can't subclass `CustomEditor` or return a live component.
- Starlark is synchronous, no goroutines; `pi.exec` blocks the calling Starlark
  thread (fine — run each extension call on a Go goroutine; the *host* stays
  concurrent).
- Python-*shaped*, not Python: no pip / ecosystem.

**Rejected alternatives:**

- **Go `plugin` (`.so`):** OS-limited, exact-toolchain match, fragile → unusable.
- **WASM (wazero/extism):** more powerful & language-agnostic, but the
  serialization boundary makes UI callbacks just as awkward *and* adds real
  complexity. Use only for untrusted heavy compute in arbitrary languages — and
  in that case MCP usually wins on simplicity.

### 4.3 Registration vs runtime (keep pi's two-phase wiring)

pi's subtle but critical pattern: at load time, action methods
(`sendMessage`/`setModel`/…) are **throwing stubs.** The runner's `bindCore()`
later overwrites them with live implementations. Registration is declarative and
side-effect-free; runtime actions get injected once the host is ready.

Go/Starlark mapping:

- **Phase 1 (load):** exec the `.star` file. `pi.register_tool`/`on`/… just mutate
  a Go `*Extension` record (handlers map, tools map, commands map). Builtins that
  perform **actions** (`sendMessage`, `setModel`) check a `*Runtime` stored in the
  Starlark thread-local; it is nil here → they error. Mirrors the stubs.
- **Phase 2 (bind):** host sets the live `*Runtime`. Now action builtins work.

### 4.4 Dispatch — the real power is the per-event combination policy

An `ExtensionRunner` is the event bus. `emit(event)` walks every extension's
handlers for that type. The interesting part is how results combine **per event:**

- `context` handlers → **chain** (each transforms the message list)
- `tool_call` handlers → can **block** and short-circuit
- `before_agent_start` → **chain** the system prompt
- `tool_result` handlers → can **rewrite** the result

Preserve these exact semantics; they are what make interception powerful.

```go
type Runner struct { exts []*Extension /* ordered */ }

func (r *Runner) Emit(ev Event) Result                              // generic walk
func (r *Runner) EmitContext(msgs []AgentMessage) []AgentMessage    // chains
func (r *Runner) EmitToolCall(ev ToolCallEvent) (block bool, reason string)
// a handler error is captured + reported, never crashes the host
// (mirrors pi's emitError listeners).
```

---

## 5. Concrete Go interface sketch

```go
// --- agentcore ---
type AgentMessage interface{ isAgentMessage() }
type AgentEvent   interface{ isAgentEvent() }

type Tool struct {
    Name, Label, Description string
    Schema  json.RawMessage
    Execute func(ctx context.Context, id string, args json.RawMessage,
                 onUpdate func(ToolResult)) (ToolResult, error)
}
type ToolResult struct {
    Content []ContentBlock   // text | image
    Details any
    IsError bool
}

type LoopConfig struct {
    Model        llm.Model
    Tools        []Tool
    ConvertToLLM func([]AgentMessage) []llm.Message
    TransformCtx func(context.Context, []AgentMessage) ([]AgentMessage, error)
    GetAPIKey    func(provider string) (string, error)
    Steering     <-chan []AgentMessage
    FollowUp     <-chan []AgentMessage
    Stream       llm.StreamFn
}
func Run(ctx context.Context, prompts []AgentMessage, cfg LoopConfig,
         events chan<- AgentEvent) ([]AgentMessage, error)

// --- ext (host-side API the Starlark builtins call into) ---
type ExtensionAPI interface {
    On(event string, handler Handler)
    RegisterTool(t Tool)
    RegisterCommand(name string, c Command)
    RegisterProvider(name string, p ProviderConfig)   // Go-implemented
    // actions (live only after bind):
    SendMessage(m CustomMessage, opts SendOpts)
    SetModel(m llm.Model) (bool, error)
    Exec(cmd string, args []string, opts ExecOpts) (ExecResult, error)
}

// Starlark builtins are thin shims: parse Starlark args → call ExtensionAPI.
// A Go closure registered as a Starlark handler is stored in the Extension's
// handlers map; Emit() invokes it via starlark.Call on a fresh thread.
```

---

## 6. Example: the `diff` extension, two ways

pi's `.pi/extensions/diff.ts` does two things: (1) shells out to `git status`
(pure logic), and (2) renders a live `SelectList` TUI component (host objects).

- **(1) ports cleanly to Starlark:** `pi.exec("git", ["status","--porcelain"])`,
  parse lines, `pi.register_command("diff", fn)`.
- **(2) does NOT:** instead of returning a `Component`, the Starlark command calls
  a declarative selector builtin:

  ```python
  choice = ctx.ui.select("Select file to diff", labels)
  ```

  The **host** owns the `SelectList` rendering; Starlark only supplies data and
  receives the chosen value. This is the declarative-widget protocol — and note
  pi already supports the `string[]` form of `setWidget`, so this is not a
  downgrade of the *model*, just of who owns the pixels.

---

## 7. Recommended build order

1. `agentcore` loop + event channel + a hardcoded tool. No extensions. Prove the
   steering/follow-up channels and the `convertToLLM` boundary.
2. One real provider in `llm/` (Anthropic Messages).
3. `ext/`: Starlark host with `pi.register_tool` + `pi.on("tool_call")` + the
   two-phase stub→bind wiring. Load one `.star` file end to end.
4. Runner dispatch with the per-event combination policies (chain / block).
5. Declarative `ctx.ui.select` + `setWidget(string[])` for the UI seam.
6. MCP client for cross-language extensions.
7. Compiled-in provider/UI interfaces for the power-user 20%.

The seam between the loop and Starlark (steps 1 + 3) is where the design
decisions bite — build those first and the rest follows.

---

## 8. Summary

A Go core loop (goroutines/channels — a great fit) + Starlark for the logic-plane
extension API + compiled-in Go for providers/rich-UI + MCP for cross-language. You
keep pi's entire conceptual model and almost all of its power. The only casualty
of static compilation is extensions rendering arbitrary host UI objects, replaced
by a declarative widget protocol.
