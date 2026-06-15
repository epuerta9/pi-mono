# Sandboxing Models

> Two schools of agent sandboxing, which fits Go, and the concrete wedge: a full
> isolated Linux VM running the binary. Companion to [[harness-architecture]] §11–12.

---

## 1. The real question: where does the boundary cut?

Both pi and deepagents do **School A** (verified by cloning): a pluggable tool
**backend / operations** interface — deepagents' `SandboxBackendProtocol`
(`execute/ls/read/write/edit/grep/glob/upload/download`), pi's `ssh.ts`
(`Bash/Read/WriteOperations`). Neither runs the harness *inside* a sandbox.

But "A vs B" is the wrong axis. Both can use the *same* isolation primitive (e.g.
Firecracker). The real difference is **where the marshaling boundary cuts:**

| | **School A — sandbox as hands** | **School B — harness in sandbox** |
|---|---|---|
| Harness runs | on host | inside the VM |
| Boundary crossed | **per tool op** (read/write/exec/grep) | **once** (prompt in, events out) |
| In the trust boundary | file/shell side effects only | everything: harness, keys, egress, context |
| Tools | RPC across the line each call | native local calls |
| Protocol you maintain | one method per capability | just prompt/events |
| TUI | host-native (snappy) | remote (over Transport) |

> [!note] Firecracker needs a guest binary *either way*
> Firecracker isn't ssh-into-a-box; you ship a guest rootfs with *something* that
> services requests over vsock. So both schools put a Go binary in the VM — the only
> question is **how much harness lives there**: a thin ops-shim (A, fat protocol) or
> the whole harness (B, thin protocol). "Single static binary" helps both equally —
> it is *not* the discriminator.

### Why a *coding* agent leans B

Per-op (A) fights the things a coding agent needs, which are trivial when
hands+brain+files are co-located (B): a coherent working tree (`cwd`, relative paths,
symlinks), **background processes** (dev server, `watch`, test runner), **localhost
tool-to-tool networking**, **LSPs/debuggers/REPLs** (long-lived, stateful, not
request/response). In A each needs protocol surface; in B they "just work."

### Where A genuinely wins

Host-native TUI; fine-grained fencing (sandbox only `bash`, read host paths
directly); managed vendors (E2B/Daytona/Modal/Runloop) at zero infra; partial
isolation that's often *sufficient* ("don't trash my machine").

### Verdict — and the unifying design

Workload-dependent, **not** "Go ⇒ B." For a coding harness with strong isolation and
the mesh, **lean B**. But build the **`Operations` seam regardless** — School B is
just `Operations = local`:

```go
type Operations interface {
    Exec(ctx, cmd, args) (Result, error)
    Read(p) ([]byte,error); Write(p,b) error; Edit(...); Grep(...); Glob(...)
}
// Local (= School B, in-VM) | SSH | MicroVMExec | Vendor(E2B/Daytona/Modal/Runloop)
```

The same harness then runs both ways; you choose at deploy time, not in code.

---

## 2. The sandbox wedge: B with a full isolated Linux VM

Keep it simple: **first sandbox wedge = the single binary running inside a full Linux
VM**, driven over the `Workspace`/`Transport` boundary we already have (prompt in /
events + traces out / resumable session = §14).

> [!warning] Hardware reality on a Mac (M-series): Firecracker & Incus don't run natively
> **Firecracker requires Linux + KVM** — macOS support is an experimental PoC only;
> on Apple Silicon you'd nest it inside a Linux VM (don't, for dev). **Incus** also
> needs a Linux kernel (on Mac it runs only *inside* a Lima/Colima Linux VM). So on
> your M5, the host hypervisor is **Apple's Virtualization.framework**, and the
> Firecracker-vs-Incus question is really a *Linux-deploy-target* question.

**Pick per environment:**

| Environment | Use | Why |
|---|---|---|
| **Mac M5 (dev)** | **Tart** (or Lima) on Virtualization.framework | native Apple-Silicon **full Linux VM**; Tart adds OCI image push/pull + snapshot/clone; near-native speed |
| **Linux host (prod, ephemeral)** | **Firecracker** | ms snapshot/restore, warm pools, throwaway-per-task microVMs |
| **Linux host (managed fleet)** | **Incus** | full VMs *and* containers, cloud-init, nice API/CLI |

**Wedge recommendation:** build on **Tart** on your M5 (full Linux VM, snapshots,
versioned VM images), behind a `Sandbox` interface. Add a **Firecracker** driver for
the Linux production path later. Same interface, swap the driver dev→prod.

> Want full-VM (real-kernel) isolation specifically? That rules out plain
> containers/Docker (shared kernel). Tart/Lima/Incus-VM/Firecracker all give a real
> kernel boundary; LXC/Docker do not.

---

## 3. The three operations you asked about

### 1. Snapshots
- **Firecracker** — best-in-class: pause + snapshot full **memory + device state** to
  files, restore in ~ms; boot-from-snapshot enables **warm pools** (clone a
  pre-initialized agent per task). The gold standard.
- **Tart** — `tart clone` (copy-on-write, fast) + reuse base images; "snapshot and
  reuse states." Clone-from-image rather than live-memory, which is what you usually
  want for per-task VMs anyway.
- **Incus** — stateful/stateless snapshots + instance copy/clone. Solid, heavier.
- *Easy everywhere; Firecracker is the most powerful, Tart the pragmatic Mac path.*

### 2. Injecting data (repos, secrets)
- **Repo:** mount a host folder (Tart `--dir`, Lima mounts, Incus shares) → live;
  or `git clone` at boot; or bake into the **OCI VM image** (Tart) for reproducibility.
- **Secrets:** never bake into the image. Inject **at boot over the control channel**
  (vsock/ssh/Transport), scoped to the VM's lifetime. Firecracker has **MMDS**
  (metadata service) for small secrets/config; Incus has **cloud-init**; Tart via
  ssh/env/mounted file.
- *Pattern: repo via mount-or-clone; secrets via post-boot control channel, never on disk.*

### 3. Getting data out (outputs, files)
- **Bulk files/outputs:** a **shared bidirectional mount** (host sees writes
  instantly) or a **result volume** you detach and read. Tart/Lima/Incus fold this in;
  `incus file pull`. (Firecracker has no virtiofs by default → use a shared **block
  device** or stream over vsock — slightly more work.)
- **Structured stream (events/traces/results):** over **vsock / ssh / the Transport**
  — i.e. the harness's event stream (§12). This is also how traces leave the VM into
  the observability store ([[observability-gameplan]]).
- *Pattern: two channels — bulk files via mount/volume, structured events/traces via the Transport.*

---

## 4. The contract (same boundary, three uses)

B's requirements — **prompt(s) in, events + results + traces out, resumable
sessions** — are exactly the headless-mode + `Workspace`/`Transport` contract built
for the UI and the mesh. Sandboxing adds no new core requirement; it reuses that one
boundary. **One boundary, three uses: remote UI, mesh peers, sandbox orchestration** —
and orchestration (pooling, snapshots, lifecycle) stays a layer *outside* the harness,
never on its hot path.
