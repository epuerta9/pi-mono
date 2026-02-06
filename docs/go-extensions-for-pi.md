# Go Extensions for Pi: Integration Points

## The Key Insight

Pi is **already designed for Go integration**:

1. **Pluggable Operations interfaces** - Every tool has a replaceable backend
2. **Tools Manager** - Downloads Go binaries (ripgrep, fd) from GitHub
3. **Extension system** - Can register new tools that call Go binaries
4. **RPC mode** - Headless protocol for external orchestration

## Integration Architecture

```
┌─────────────────────────────────────────────────────────────────────────┐
│                              Pi Agent                                    │
│                                                                         │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐   │
│  │ Bash Tool   │  │ Grep Tool   │  │ Read Tool   │  │ Custom Tool │   │
│  │             │  │             │  │             │  │             │   │
│  │ Operations  │  │ Operations  │  │ Operations  │  │ Operations  │   │
│  │ Interface   │  │ Interface   │  │ Interface   │  │ Interface   │   │
│  └──────┬──────┘  └──────┬──────┘  └──────┬──────┘  └──────┬──────┘   │
│         │                │                │                │           │
└─────────┼────────────────┼────────────────┼────────────────┼───────────┘
          │                │                │                │
          ▼                ▼                ▼                ▼
┌─────────────────────────────────────────────────────────────────────────┐
│                         Go Tools Layer                                   │
│                                                                         │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐   │
│  │ pi-bash     │  │ ripgrep     │  │ pi-read     │  │ pi-index    │   │
│  │ (SSH/K8s)   │  │ (already!)  │  │ (cached)    │  │ (semantic)  │   │
│  └─────────────┘  └─────────────┘  └─────────────┘  └─────────────┘   │
│                                                                         │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐   │
│  │ pi-git      │  │ pi-test     │  │ pi-build    │  │ pi-watch    │   │
│  │ (fast ops)  │  │ (parallel)  │  │ (smart)     │  │ (daemon)    │   │
│  └─────────────┘  └─────────────┘  └─────────────┘  └─────────────┘   │
└─────────────────────────────────────────────────────────────────────────┘
```

---

## High-Value Go Tools for Pi

### 1. **pi-exec** - Remote/Container Execution

**Problem:** Pi's bash tool only runs locally.

**Solution:** Go binary that implements BashOperations for remote execution.

```go
// pi-exec: Smart command router
package main

import (
    "encoding/json"
    "os"
    "os/exec"
)

type ExecRequest struct {
    Command string            `json:"command"`
    Cwd     string            `json:"cwd"`
    Env     map[string]string `json:"env"`
    Target  string            `json:"target"` // "local", "ssh:host", "k8s:pod", "docker:container"
}

type ExecResponse struct {
    ExitCode int    `json:"exit_code"`
    Stdout   string `json:"stdout"`
    Stderr   string `json:"stderr"`
}

func main() {
    var req ExecRequest
    json.NewDecoder(os.Stdin).Decode(&req)

    var result ExecResponse

    switch {
    case req.Target == "local":
        result = execLocal(req)
    case strings.HasPrefix(req.Target, "ssh:"):
        result = execSSH(req)
    case strings.HasPrefix(req.Target, "k8s:"):
        result = execK8s(req)
    case strings.HasPrefix(req.Target, "docker:"):
        result = execDocker(req)
    }

    json.NewEncoder(os.Stdout).Encode(result)
}

func execSSH(req ExecRequest) ExecResponse {
    host := strings.TrimPrefix(req.Target, "ssh:")
    cmd := exec.Command("ssh", host, req.Command)
    // ...
}

func execK8s(req ExecRequest) ExecResponse {
    pod := strings.TrimPrefix(req.Target, "k8s:")
    cmd := exec.Command("kubectl", "exec", pod, "--", "sh", "-c", req.Command)
    // ...
}
```

**Pi Extension:**
```typescript
// extensions/remote-exec.ts
import { ExtensionFactory } from "@mariozechner/pi-coding-agent";
import { spawn } from "child_process";

const extension: ExtensionFactory = (pi) => {
    // Replace bash operations with pi-exec
    pi.on("tool_call", async (event, ctx) => {
        if (event.tool === "bash" && process.env.PI_EXEC_TARGET) {
            const result = await callPiExec({
                command: event.params.command,
                cwd: ctx.cwd,
                target: process.env.PI_EXEC_TARGET
            });
            return {
                override: true,
                result: { content: [{ type: "text", text: result.stdout }] }
            };
        }
    });
};
```

---

### 2. **pi-index** - Semantic Code Indexer

**Problem:** Every grep/search re-scans files. Large repos are slow.

**Solution:** Go daemon that maintains a semantic index.

```go
// pi-index: Background code indexer
package main

import (
    "github.com/blevesearch/bleve/v2"
    "github.com/fsnotify/fsnotify"
)

type CodeIndex struct {
    index   bleve.Index
    watcher *fsnotify.Watcher
}

func (ci *CodeIndex) Start(rootDir string) {
    // Initial index
    ci.indexDirectory(rootDir)

    // Watch for changes
    ci.watcher.Add(rootDir)
    go ci.watchLoop()
}

func (ci *CodeIndex) Search(query SearchQuery) []SearchResult {
    // Semantic search with ranking
    q := bleve.NewQueryStringQuery(query.Pattern)
    req := bleve.NewSearchRequest(q)
    req.Size = query.Limit
    req.Fields = []string{"path", "line", "content"}

    results, _ := ci.index.Search(req)
    // ...
}

// HTTP API for Pi to call
func main() {
    index := NewCodeIndex()
    index.Start(os.Getenv("PI_WORKSPACE"))

    http.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
        var query SearchQuery
        json.NewDecoder(r.Body).Decode(&query)
        results := index.Search(query)
        json.NewEncoder(w).Encode(results)
    })

    http.HandleFunc("/symbols", func(w http.ResponseWriter, r *http.Request) {
        // Return indexed symbols (functions, classes, etc.)
    })

    http.ListenAndServe(":9123", nil)
}
```

**Pi Extension:**
```typescript
// extensions/semantic-search.ts
pi.registerTool({
    name: "semantic_search",
    description: "Search code semantically using pre-built index. Faster than grep for large repos.",
    parameters: Type.Object({
        query: Type.String({ description: "Natural language or pattern query" }),
        type: Type.Optional(Type.String({ description: "Symbol type: function, class, variable" })),
    }),
    async execute(id, params) {
        const response = await fetch("http://localhost:9123/search", {
            method: "POST",
            body: JSON.stringify(params)
        });
        const results = await response.json();
        return { content: [{ type: "text", text: formatResults(results) }] };
    }
});
```

---

### 3. **pi-git** - Fast Git Operations

**Problem:** Git operations via bash are slow and verbose.

**Solution:** Go binary with native git operations.

```go
// pi-git: Fast git operations for agents
package main

import (
    "github.com/go-git/go-git/v5"
    "github.com/go-git/go-git/v5/plumbing/object"
)

type GitOp struct {
    repo *git.Repository
}

// Get status in structured format (not parsing git status output)
func (g *GitOp) Status() StatusResult {
    wt, _ := g.repo.Worktree()
    status, _ := wt.Status()

    result := StatusResult{}
    for file, s := range status {
        switch s.Staging {
        case git.Added:
            result.Staged = append(result.Staged, file)
        case git.Modified:
            result.Modified = append(result.Modified, file)
        // ...
        }
    }
    return result
}

// Diff with semantic understanding
func (g *GitOp) SmartDiff(path string) DiffResult {
    // Returns structured diff, not raw text
    // Includes: added lines, removed lines, context
    // Can filter by file type, function, etc.
}

// Blame with caching
func (g *GitOp) Blame(path string) BlameResult {
    // Cached blame info
    // Returns author, date, commit for each line
}

// Find when a line was added/changed
func (g *GitOp) LineHistory(path string, line int) []Commit {
    // Git log -L but faster
}

// Main command dispatcher
func main() {
    var cmd Command
    json.NewDecoder(os.Stdin).Decode(&cmd)

    g := &GitOp{repo: openRepo(cmd.RepoPath)}

    var result interface{}
    switch cmd.Operation {
    case "status":
        result = g.Status()
    case "diff":
        result = g.SmartDiff(cmd.Path)
    case "blame":
        result = g.Blame(cmd.Path)
    case "history":
        result = g.LineHistory(cmd.Path, cmd.Line)
    }

    json.NewEncoder(os.Stdout).Encode(result)
}
```

**Pi Extension:**
```typescript
pi.registerTool({
    name: "git",
    description: "Fast git operations with structured output",
    parameters: Type.Object({
        operation: Type.Union([
            Type.Literal("status"),
            Type.Literal("diff"),
            Type.Literal("blame"),
            Type.Literal("history"),
        ]),
        path: Type.Optional(Type.String()),
        line: Type.Optional(Type.Number()),
    }),
    async execute(id, params) {
        const result = await execPiGit(params);
        return { content: [{ type: "text", text: formatGitResult(result) }] };
    }
});
```

---

### 4. **pi-test** - Parallel Test Runner

**Problem:** Running tests via `bash("npm test")` is slow and gives unstructured output.

**Solution:** Go test orchestrator that runs tests in parallel.

```go
// pi-test: Smart test runner
package main

type TestRunner struct {
    workers int
    cache   *TestCache
}

type TestResult struct {
    Name     string        `json:"name"`
    Status   string        `json:"status"`  // passed, failed, skipped
    Duration time.Duration `json:"duration"`
    Output   string        `json:"output"`
    Error    string        `json:"error,omitempty"`
}

func (tr *TestRunner) RunTests(patterns []string, changedFiles []string) []TestResult {
    // 1. Find all test files matching patterns
    testFiles := tr.findTestFiles(patterns)

    // 2. Filter to tests affected by changed files (smart selection)
    if len(changedFiles) > 0 {
        testFiles = tr.filterAffectedTests(testFiles, changedFiles)
    }

    // 3. Run in parallel with worker pool
    results := make(chan TestResult, len(testFiles))
    var wg sync.WaitGroup

    sem := make(chan struct{}, tr.workers)
    for _, tf := range testFiles {
        wg.Add(1)
        go func(file string) {
            defer wg.Done()
            sem <- struct{}{}
            defer func() { <-sem }()

            result := tr.runSingleTest(file)
            results <- result
        }(tf)
    }

    wg.Wait()
    close(results)

    // 4. Collect and return
    var all []TestResult
    for r := range results {
        all = append(all, r)
    }
    return all
}

// Detect test framework and run appropriately
func (tr *TestRunner) runSingleTest(file string) TestResult {
    switch detectFramework(file) {
    case "jest":
        return tr.runJest(file)
    case "pytest":
        return tr.runPytest(file)
    case "go":
        return tr.runGoTest(file)
    // ...
    }
}
```

**Pi Extension:**
```typescript
pi.registerTool({
    name: "test",
    description: "Run tests in parallel. Optionally run only tests affected by recent changes.",
    parameters: Type.Object({
        pattern: Type.Optional(Type.String({ description: "Test file pattern" })),
        affected: Type.Optional(Type.Boolean({ description: "Only run tests affected by uncommitted changes" })),
        parallel: Type.Optional(Type.Number({ description: "Number of parallel workers (default: CPU count)" })),
    }),
    async execute(id, params) {
        const changedFiles = params.affected ? await getChangedFiles() : [];
        const result = await execPiTest({
            patterns: [params.pattern || "**/*.test.*"],
            changedFiles,
            workers: params.parallel,
        });
        return { content: [{ type: "text", text: formatTestResults(result) }] };
    }
});
```

---

### 5. **pi-watch** - File Watcher Daemon

**Problem:** Pi doesn't know when files change externally.

**Solution:** Go daemon that watches files and notifies Pi.

```go
// pi-watch: File system watcher
package main

import (
    "github.com/fsnotify/fsnotify"
    "github.com/nats-io/nats.go"
)

type FileWatcher struct {
    watcher *fsnotify.Watcher
    nc      *nats.Conn
}

func (fw *FileWatcher) Start(rootDir string) {
    fw.addRecursive(rootDir)

    for {
        select {
        case event := <-fw.watcher.Events:
            // Debounce and notify
            fw.notify(FileEvent{
                Path:      event.Name,
                Operation: mapOp(event.Op),
                Time:      time.Now(),
            })
        }
    }
}

func (fw *FileWatcher) notify(event FileEvent) {
    // Publish to NATS (or local IPC)
    data, _ := json.Marshal(event)
    fw.nc.Publish("pi.files.changed", data)

    // Also write to a socket Pi can read
    writeToSocket(event)
}
```

**Pi Extension:**
```typescript
// extensions/file-watcher.ts
const extension: ExtensionFactory = async (pi) => {
    // Connect to pi-watch daemon
    const socket = net.connect("/tmp/pi-watch.sock");

    socket.on("data", (data) => {
        const event = JSON.parse(data.toString());

        // Notify agent of external changes
        if (event.operation === "modify" && isRelevantFile(event.path)) {
            pi.sendUserMessage(`File changed externally: ${event.path}`, { silent: true });
        }
    });

    // Register tool to query recent changes
    pi.registerTool({
        name: "recent_changes",
        description: "Get files that changed recently (external to this session)",
        parameters: Type.Object({
            since: Type.Optional(Type.String({ description: "Time window (e.g., '5m', '1h')" })),
        }),
        async execute(id, params) {
            const changes = await queryPiWatch(params.since);
            return { content: [{ type: "text", text: formatChanges(changes) }] };
        }
    });
};
```

---

### 6. **pi-cache** - Smart Response Cache

**Problem:** Repeated queries (e.g., "list all functions") re-run expensive operations.

**Solution:** Go service that caches tool results with smart invalidation.

```go
// pi-cache: Intelligent caching layer
package main

type Cache struct {
    store      map[string]*CacheEntry
    fileHashes map[string]string  // Track file changes for invalidation
}

type CacheEntry struct {
    Key       string
    Value     interface{}
    Deps      []string  // Files this result depends on
    CreatedAt time.Time
    TTL       time.Duration
}

func (c *Cache) Get(key string, deps []string) (interface{}, bool) {
    entry, exists := c.store[key]
    if !exists {
        return nil, false
    }

    // Check if any dependency changed
    for _, dep := range entry.Deps {
        currentHash := c.hashFile(dep)
        if currentHash != c.fileHashes[dep] {
            // File changed, invalidate cache
            delete(c.store, key)
            return nil, false
        }
    }

    // Check TTL
    if time.Since(entry.CreatedAt) > entry.TTL {
        delete(c.store, key)
        return nil, false
    }

    return entry.Value, true
}

// Example: Cache grep results
func (c *Cache) CachedGrep(pattern, path string) []GrepResult {
    key := fmt.Sprintf("grep:%s:%s", pattern, path)
    deps := listFilesIn(path)

    if cached, ok := c.Get(key, deps); ok {
        return cached.([]GrepResult)
    }

    // Run actual grep
    results := runRipgrep(pattern, path)

    c.Set(key, results, deps, 5*time.Minute)
    return results
}
```

---

## How Pi Loads Go Tools (Already Works!)

From `tools-manager.ts`, Pi already downloads Go binaries:

```typescript
const TOOLS: Record<string, ToolConfig> = {
    fd: {
        name: "fd",
        repo: "sharkdp/fd",  // Go binary from GitHub releases
        binaryName: "fd",
        // ...
    },
    rg: {
        name: "ripgrep",
        repo: "BurntSushi/ripgrep",  // Rust binary, same pattern
        binaryName: "rg",
        // ...
    },
    // Add your Go tools here!
    "pi-git": {
        name: "pi-git",
        repo: "yourorg/pi-git",
        binaryName: "pi-git",
        tagPrefix: "v",
        getAssetName: (version, plat, arch) => {
            // Return platform-specific binary name
        }
    }
};
```

---

## Summary: Where Go Adds Value TO Pi

| Go Tool | What It Does | Value |
|---------|--------------|-------|
| **pi-exec** | Remote/container execution | Run anywhere, not just local |
| **pi-index** | Semantic code indexer | Instant search on huge repos |
| **pi-git** | Fast git operations | Structured output, 10x faster |
| **pi-test** | Parallel test runner | Smart test selection, parallel |
| **pi-watch** | File watcher daemon | Know when files change externally |
| **pi-cache** | Result caching | Don't repeat expensive operations |
| **pi-build** | Smart build tool | Incremental, cached builds |
| **pi-lint** | Fast linter | Parallel, incremental |

The pattern:
1. **Go binary** implements a focused operation
2. **Pi extension** wraps it as a tool
3. **Tools manager** handles distribution
4. **Agent** calls it like any other tool

This is how Pi is *designed* to work. The architecture already exists.
