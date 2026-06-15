# Sandboxing Models

> Two schools of agent sandboxing, what pi and deepagents actually do, and which
> fits the Go single-binary model. Companion to [[harness-architecture]] §11–12.

---

## 1. Does pi have a sandbox subsystem? (and deepagents)

Neither has a *core* sandbox subsystem — both treat it as a **pluggable tool
backend**, i.e. **School A** (below). Verified by cloning both:

- **deepagents** (`backends/protocol.py`, `backends/sandbox.py`): a
  `SandboxBackendProtocol` with `execute / ls / read / write / edit / grep / glob /
  upload_files / download_files`. The file/exec tools dispatch to a `Backend` —
  `state`, `local_shell`, `store`, or `sandbox` (wrapping vendor providers via
  `integrations/sandbox_provider.py`). Notably their own comment says `BaseSandbox`
  "does not reduce or partition the trust boundary" — it's about *where ops run*.
- **pi** (`ssh.ts`): `createReadTool/createWriteTool/createBashTool` parametrized by
  `ReadOperations / WriteOperations / BashOperations`. Plus extension examples
  `sandbox/` (`@anthropic-ai/sandbox-runtime`) and `gondolin/` (route tools into a
  micro-VM).

**Both = harness on the host; tool execution routed through a swappable
operations/backend interface.** Neither runs the whole harness *inside* a sandbox.

---

## 2. The two schools

### School A — sandbox as *hands* (harness outside, tools reach in)

The harness runs on the host; only **tool side effects** (read/write/exec/bash) are
routed into a sandbox via an `Operations`/`Backend` interface (local, SSH, a microVM
exec channel, or a vendor: E2B / Daytona / Modal / Runloop).

- ✅ fine-grained (sandbox just `bash`, read files locally); keeps host-native TUI;
  works with managed vendors at zero infra; matches pi/deepagents.
- ❌ the boundary is **per-operation** — you must define & secure every op that
  crosses in; **partial isolation** (harness, LLM keys, your context still on the
  host); per-op marshaling latency; vendor API surface or you build the exec protocol.

### School B — harness *inside* the sandbox (whole binary in a microVM)

Boot a microVM/container with the **single Go binary + a full Linux env**. The whole
harness runs inside; the host only **orchestrates** (provision VM, inject prompt/
session, collect results/traces).

- ✅ **complete isolation** in one boundary (harness, tools, scoped keys, network
  policy); the harness code is *unaware of sandboxing* — it just runs on "a Linux
  box," native fs/exec/bash at full speed, **no per-op marshaling**; the static
  binary makes the VM image trivial (copy one file, no runtime deps); orchestration
  is a clean separate layer; maps 1:1 onto the mesh (one agent = one VM, talking
  over NATS).
- ❌ coarser-grained (whole-VM, which is usually what you *want* for security); VM
  boot latency (Firecracker ~125 ms — mitigate with pools/snapshots); the TUI is now
  remote → needs the client/server frontend split (which we already designed).

---

## 3. Which fits Go — and why it's nearly free here

> [!success] Recommendation: **School B as the primary secure story; School A as a
> lighter secondary seam.** And they compose.

Go's defining trait — a **single static binary, no runtime deps, cross-compiled** —
makes School B almost trivial: bake one file into a minimal rootfs and boot it. More
importantly, **School B's requirements are the architecture we already have:**

| School B needs | We already designed |
|---|---|
| drive a harness running elsewhere | headless core + client frontend (`Workspace`, §14) |
| prompts in / events + traces out | the event stream over `Transport` (§12) |
| resumable sessions | JSONL/store inside the VM (or a mounted volume) |
| many isolated agents | one binary per microVM, coordinated over NATS (the mesh) |

So "harness inside an ephemeral microVM, orchestrated and streamed from outside" is
**not extra work — it's the natural consequence** of the headless-core +
client-frontend + `Transport` design. The harness-in-the-box is the *server*; your
TUI/orchestrator outside is the *client*; sessions resume from the in-VM store;
prompts/results/traces flow over the same boundary.

This also delivers exactly the separation you want: **we only do harness
engineering; sandbox orchestration lives entirely outside the harness path.**

### Keep School A as a seam too

Still expose an `Operations` interface (read/write/exec/bash) à la pi/deepagents, so
the *lightweight* case works without a full VM (e.g. sandbox just `bash` on the host,
or route to a managed vendor). And the two **compose**: run the harness in a VM (B)
*and* let its bash tool use an `Operations` backend (A) for nested isolation.

```go
// School A seam (lightweight / composable)
type Operations interface {
    Exec(ctx, cmd, args) (Result, error)
    Read(path) ([]byte, error); Write(path, b) error; Edit(...)
}
// impls: Local | SSH | MicroVMExec | VendorSandbox(E2B/Daytona/Modal/Runloop)
```

---

## 4. MicroVM / isolation options (for School B orchestration)

| Tool | Lang | Notes |
|---|---|---|
| **Firecracker** | Rust | minimal microVM, ~125 ms boot; `firecracker-go-sdk`; great for ephemeral per-task VMs |
| **Cloud Hypervisor** | Rust | similar, modern devices |
| **Incus** | Go | system containers **and** VMs, nice API/CLI; "full Linux env" |
| **Kata Containers** | Go | run like OCI containers, isolated like VMs |
| **gVisor** | Go | userspace kernel; lighter than a VM, strong syscall isolation (no full Linux) |
| Managed (School A) | — | **E2B / Daytona / Modal / Runloop** — sandbox-as-a-service APIs |

> [!note] Default secure story
> Bake the static binary into a **Firecracker** (ephemeral, fast) or **Incus**
> (full-Linux, persistent-ish) image; the in-VM harness speaks to the orchestrator
> over **vsock / socket / NATS** — the same `Transport`. Orchestration (pooling,
> snapshots, lifecycle) is a layer *outside* the harness, never on its hot path.

---

## 5. The contract both schools share

Whichever school, the harness must support: **prompt(s) in**, **events + results +
traces out**, and **resumable sessions**. That's already the headless-mode +
`Workspace`/`Transport` contract. So sandboxing doesn't add a new core requirement —
it reuses the boundary we built for the UI and the mesh. One boundary, three uses:
remote UI, mesh peers, sandbox orchestration.
