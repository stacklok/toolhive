# **🧱 Technical Design Proposal: `.thvignore`\-Driven Bind Mount Filtering in ToolHive**

---

## **🎯 Goals**

| Objective | Solution |
| ----- | ----- |
| Exclude secrets (e.g., `.ssh`, `.env`) from containers while using bind mounts | Use `.thvignore` to drive tmpfs overlays |
| Maintain real-time access to files like SQLite DBs | Bind mount full directory |
| Support both global ignore patterns (e.g., user-wide) and per-project rules | Combine global and local `.thvignore` |
| Provide a consistent, secure experience across all runtimes | Abstract runtime-specific mount behavior in ToolHive's execution layer |

---

## **🗂 Config File Design**

### **🧭 Per-folder config: `.thvignore`**

Lives **next to the files being mounted**.

```shell
my-folder/
├── database.db
├── .ssh/
└── .thvignore
```

`.thvignore`:

```
.ssh/
*.bak
.env
```

### **🌍 Global config: `~/.config/toolhive/thvignore`**

Example:

```
node_modules/
.DS_Store
.idea/
```

These patterns apply to **all** mounts unless explicitly disabled.

---

## **🧠 Behavior Overview**

### **✅ At runtime:**

1. User runs:

```shell
thv run --volume ./my-folder:/app server-name
```

2.
   ToolHive does the following:

   * Load global ignore file from `~/.config/toolhive/thvignore`

   * Load `./my-folder/.thvignore` (if present)

   * Combine and normalize both sets of patterns

   * For each pattern:

     * Determine full container path (e.g. `/app/.ssh`)

     * Add a `tmpfs` mount over it to the runtime configuration

---

## **🧱 Component Design**

### **🔹 `IgnoreProcessor` (new module in `pkg/ignore/`)**

```go
package ignore

type IgnoreProcessor struct {
    GlobalPatterns []string
    LocalPatterns  []string
}

func NewIgnoreProcessor() *IgnoreProcessor
func (ip *IgnoreProcessor) LoadGlobal() error
func (ip *IgnoreProcessor) LoadLocal(sourceDir string) error
func (ip *IgnoreProcessor) GetOverlayPaths(bindMount, containerPath string) []string
func (ip *IgnoreProcessor) ShouldIgnore(path string) bool
```

*
  Reads `.gitignore`\-style files using existing Go libraries

* Integrates with ToolHive's existing mount processing pipeline

* Converts ignore patterns into **container absolute paths** (e.g. `/app/.ssh`)

---

### **🔹 Enhanced `runtime.Mount` (in `pkg/container/runtime/types.go`)**

Extend the existing Mount struct to support tmpfs:

```go
type Mount struct {
    Source   string
    Target   string
    ReadOnly bool
    Type     string // NEW: "bind" or "tmpfs"
}
```

Integration with existing mount processing in `pkg/runner/config_builder.go`:

```go
func (b *RunConfigBuilder) processVolumeMounts() error {
    // Existing mount processing...
    
    // NEW: Process ignore patterns
    ignoreProcessor := ignore.NewIgnoreProcessor()
    ignoreProcessor.LoadGlobal()
    ignoreProcessor.LoadLocal(sourceDir)
    
    overlayPaths := ignoreProcessor.GetOverlayPaths(source, target)
    for _, overlayPath := range overlayPaths {
        b.addTmpfsOverlay(overlayPath)
    }
}
```

---

## **🧪 Runtime Support**

Enhanced `convertMounts` function in `pkg/container/docker/client.go`:

```go
func convertMounts(mounts []runtime.Mount) []mount.Mount {
    result := make([]mount.Mount, 0, len(mounts))
    for _, m := range mounts {
        if m.Type == "tmpfs" {
            result = append(result, mount.Mount{
                Type:   mount.TypeTmpfs,
                Target: m.Target,
                TmpfsOptions: &mount.TmpfsOptions{
                    SizeBytes: 1024 * 1024, // 1MB tmpfs for security overlays
                },
            })
        } else {
            result = append(result, mount.Mount{
                Type:     mount.TypeBind,
                Source:   m.Source,
                Target:   m.Target,
                ReadOnly: m.ReadOnly,
            })
        }
    }
    return result
}
```

| Runtime | Bind Mount | Tmpfs Overlay |
| ----- | ----- | ----- |
| Docker | ✅ `mount.TypeBind` | ✅ `mount.TypeTmpfs` |
| Podman | ✅ `--mount type=bind` | ✅ `--mount type=tmpfs` |
| Colima | ✅ `mount.TypeBind` | ✅ `mount.TypeTmpfs` |

---

## **🧰 CLI Integration**

Extend existing `thv run` command flags:

```go
// In cmd/thv/app/run.go
var (
    runIgnoreGlobally     bool
    runPrintOverlays      bool
    runIgnoreFile         string
)

func init() {
    runCmd.Flags().BoolVar(&runIgnoreGlobally, "ignore-globally", true,
        "Load global ignore patterns from ~/.config/toolhive/thvignore")
    runCmd.Flags().BoolVar(&runPrintOverlays, "print-resolved-overlays", false,
        "Debug: show resolved container paths for tmpfs overlays")
    runCmd.Flags().StringVar(&runIgnoreFile, "ignore-file", ".thvignore",
        "Name of the ignore file to look for in source directories")
}
```

---

## **🔐 Security Considerations**

* Warn users if sensitive-looking files (`.ssh`, `.env`) are present but not excluded

* Validate ignore patterns to prevent overly broad exclusions

* Integrate with existing permission profile system for defense-in-depth

* Log overlay mount creation for audit purposes

---

## **🎯 Use Cases**

### **🔑 Cloud Provider Credentials**

**Scenario**: Developer working on a project with AWS/GCP credentials that should never be accessible to MCP servers.

```shell
my-project/
├── src/
├── .aws/credentials
├── .gcp/service-account.json
├── .env.production
└── .thvignore
```

**`.thvignore`**:
```
.aws/
.gcp/
*.pem
.env.production
```

**Result**: MCP server analyzes code in `src/` but cloud credentials are hidden via tmpfs overlays.

---

### **🏢 SSH Keys and Development Secrets**

**Scenario**: Developer's home directory mounted for MCP server to access project files while protecting personal credentials.

```shell
~/dev-project/
├── code/
├── .ssh/id_rsa
├── .gnupg/
├── .docker/config.json
└── .thvignore
```

**`.thvignore`**:
```
.ssh/
.gnupg/
.docker/config.json
.kube/config
```

**Result**: MCP server can access project code but personal authentication credentials remain protected.

---

### **🤖 AI/ML Model Protection**

**Scenario**: Data scientist using MCP servers for code analysis while protecting sensitive datasets and production models.

```shell
ml-project/
├── notebooks/
├── src/
├── data/customer-data.csv
├── models/production-model.pkl
└── .thvignore
```

**`.thvignore`**:
```
data/*.csv
models/production-*
*.pkl
.kaggle/
```

**Result**: MCP server can analyze notebooks and source code but cannot access sensitive data or production models.

---

## **📄 Example: Final Runtime Command**

If user runs:

```shell
thv run --volume ./my-folder:/app server-name
```

And:

```shell
# ~/.config/toolhive/thvignore
node_modules/

# ./my-folder/.thvignore
.ssh/
.env
```

ToolHive generates runtime configuration with:

```go
// Main bind mount
runtime.Mount{
    Source: "/absolute/path/my-folder",
    Target: "/app",
    Type: "bind",
    ReadOnly: false,
}

// Tmpfs overlays
runtime.Mount{Target: "/app/.ssh", Type: "tmpfs"}
runtime.Mount{Target: "/app/.env", Type: "tmpfs"}
runtime.Mount{Target: "/app/node_modules", Type: "tmpfs"}
```

Which converts to Docker commands:

```shell
docker run \
  -v /absolute/path/my-folder:/app \
  --tmpfs /app/.ssh:rw,nosuid,nodev,noexec \
  --tmpfs /app/.env:rw,nosuid,nodev,noexec \
  --tmpfs /app/node_modules:rw,nosuid,nodev,noexec \
  my-image
```

---

## **✅ Summary**

| Feature | Outcome |
| ----- | ----- |
| Real-time file access | ✅ via full bind mount |
| Hidden files (e.g. `.ssh`, `.env`) | ✅ overlaid with tmpfs |
| Config flexibility | ✅ per-folder \+ global `.thvignore` |
| Runtime compatibility | ✅ Docker, Podman, Colima |
| Integration | ✅ Works with existing permission profiles |

