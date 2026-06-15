# Caching & the Harness Control Surface

> What's provider-owned vs. harness-owned, and where the real optimization
> leverage actually is. Companion to [[harness-spec-and-roadmap]] Part II.

> [!abstract] The thesis in one line
> Prompt caching is mostly the **provider's** job and largely automatic; the
> harness's real leverage is **never generating the tokens in the first place**
> (context engineering) plus a **content-addressed tool-result cache** the provider
> can't do for you. *The cheapest token is the one you never send.*

---

## 1. How prompt caching actually works (grounded in pi)

Caching is **prefix caching**: a cache hit covers everything from the *start* of
the request up to the matching point. It is cumulative and order-sensitive — change
anything early and the whole downstream prefix is invalidated. The request prefix is
ordered **`tools → system → messages[]`**.

pi (Anthropic provider, `packages/ai/src/providers/anthropic.ts`) sets two
breakpoints (`cache_control: {type:"ephemeral"}`):

1. On the **system block** → caches `tools + system` (the stable preamble).
2. On the **last message's last block** → caches the **entire conversation so far**.
   Each turn this breakpoint moves to the new tail, so next turn the whole prior
   conversation is a cache hit (~10% cost) and only the new turn is full price.

Economics: read ≈ **0.1×**, write ≈ **1.25×**, ≤ **4 breakpoints**, ephemeral TTL
(~5 min, or `1h` opt-in).

### How LLM messages work (refresher)

The API is **stateless** — the client resends the full history every turn:

```
system        ← top-level instructions
tools         ← function schemas (separate array)
messages[]    ← role-alternating: user (text/image/tool_result),
                assistant (text/thinking/tool_use), ...
```

Tool loop: assistant emits `tool_use` → harness executes → appends a `user` message
with `tool_result` → resend everything → repeat. Cost compounds because the whole
array is resent every turn — which is *why* caching and compaction exist.

---

## 2. Is caching Anthropic-only? No — but the mechanics differ

| Provider | Breakpoints | Write premium | Read discount | Notes |
|---|---|---|---|---|
| **Anthropic** | **manual**, ≤4 (`cache_control`) | ~1.25× | ~0.1× | ephemeral ~5 min / `1h` opt-in |
| **OpenAI** | **automatic**, none | none | ~0.5× | auto for prompts ≥1024 tokens; optional 24h retention |
| **Google Gemini** | **automatic** (+ optional explicit `CachedContent`) | implicit none | ~0.25× | reads `cachedContentTokenCount` |

pi's code reflects this: it only writes `cache_control` in the Anthropic provider;
the OpenAI/Google providers merely **read back** `cached_tokens` /
`cachedContentTokenCount`. So OpenAI and Gemini cache **for free, server-side** —
you just send a stable prefix and they reward you.

**Split the concern accordingly:**
- **Universal:** keep the prefix stable + append-only (helps all three). Mostly
  automatic — see §3.
- **Provider-specific adapter:** Anthropic-only breakpoint placement (cost model:
  set a breakpoint only where `reuse > 1.25× write`). For OpenAI/Gemini it's a no-op
  that just records hit stats.

```
ApplyCacheHints(layers, model) → request
  anthropic: emit ≤4 cache_control breakpoints by cost model
  openai/gemini: no-op (auto-cache); record cachedTokens from usage
```

---

## 3. Ordering is a given — guarding the prefix is not

Naive append-only conversation is *naturally* prefix-stable, so "ordering" is free.
The real (narrow) work is preventing the operations that **secretly mutate the
prefix** mid-session:

| Cache-buster | Why it hurts |
|---|---|
| **Compaction / summarization** | rewrites the *middle* → invalidates everything after. The big, unavoidable one. |
| **Tool-set changes** (`setActiveTools`, ext adds tool) | `tools` is the *first* layer → cold-starts everything |
| **Dynamic system prompt** (`before_agent_start`) | per-turn system mutation → full price *every* turn |
| **Context/RAG injection into early messages** | moves the prefix |

> [!tip] So "cache discipline" ≈ **compaction strategy + don't-mutate-the-prefix
> hygiene**, not "ordering." Compact rarely, at a layer boundary, rewriting only the
> tail; treat tools + system as immutable within a session.

---

## 4. The harness control surface (where the real leverage is)

Provider caching discounts a token ~90%; **not sending it is 100% off *and*
improves quality.** Ranked by leverage, all of this is ours:

### 4.1 Context engineering — what enters context at all (biggest)
- **Tool-result shaping/truncation** — file reads, `bash`, `grep` dumps drive token
  growth. Cap bytes/lines, head+tail, structured summaries; `edit` returns a diff.
- **Compaction quality** — what to keep vs. summarize (token ↔ fidelity).
- **Sub-agent isolation** — noisy work in a child loop; return a 1–2k-token summary.
- **Pruning / dedup** — drop stale tool results; dedupe repeated file reads.
- **Lazy retrieval** — fetch when needed; don't pre-dump the repo.

### 4.2 The tool layer
- Tool **granularity & schemas** (fewer/sharper = smaller array, better selection).
- **Gating** (expose only relevant tools), **parallel execution** (latency).
- **Result memoization** → §4.4.

### 4.3 Loop / control flow
- **Loop detection** (cf. Crush `loop_detection.go`) — catch repetition, break out.
- **Per-phase model routing** — cheap model to plan/summarize, strong to code.
- **Thinking-level control** — reasoning burns *output* tokens; tune per task.
- Turn budgeting, retry/overflow recovery, steering (don't waste a turn).

### 4.4 The one cache that's genuinely ours (and *is* Docker-shaped)
Not the prompt cache (provider's) — the **tool-result / file-read cache**:
content-addressed, invalidated on write (cf. Crush `internal/filetracker`). The agent
reads the same file repeatedly → serve from a local content-addressed store; bust the
entry when `edit`/`write` touches that path. Also: search indices, embeddings,
sub-agent results. This is a real subsystem we own end-to-end — the legitimate home
for the layered/content-addressed cache idea, because the provider can't know your
`read config.go` is the same bytes as last turn.

### 4.5 Observability-driven tuning (the `Broker`)
Measure per-turn tokens, tool latency, success, retries → feed back into
routing/compaction/truncation policy. The self-optimizing-harness bet.

> [!success] Honest hierarchy
> **context engineering (4.1) ≫ tool layer (4.2) ≈ loop control (4.3) ≫ prompt-cache
> discipline.** Caching is "don't leave money on the table" hygiene that's largely
> automatic. Differentiation lives in *not generating the tokens* and in the
> harness-side result cache.

---

## 5. Where each plugs into the architecture

| Lever | Seam ([[harness-architecture]]) |
|---|---|
| Tool-result shaping, dedup | tool wrapper |
| Compaction, pruning, retrieval, sub-agent summaries | `transformContext` |
| Prefix discipline + breakpoints | `convertToLLM` + per-provider cache adapter |
| Model routing, thinking level, loop detection | the loop / `LoopConfig` |
| Tool-result/file content cache | tool layer (content-addressed store + filetracker) |
| Telemetry feedback | `Broker` taps |
