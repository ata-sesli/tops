package localruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"

	"tops/internal/config"
	"tops/internal/obs"
)

const (
	defaultLlamaRepoURL = "https://github.com/ggml-org/llama.cpp"
)

var yzmaLlamaRefPins = map[string]string{
	"v1.13.0": "b8920",
}

var requiredDylibFamilies = []string{
	"libllama",
	"libggml",
	"libggml-base",
	"libggml-cpu",
	"libggml-metal",
}

var optionalDylibFamilies = []string{
	"libggml-blas",
	"libggml-common",
	"libmtmd",
}

// BuildYZMALibsOptions controls `tps local build-yzma-libs`.
type BuildYZMALibsOptions struct {
	Backend    string
	InstallDir string
	LlamaRef   string
	Clean      bool
	Jobs       int
}

type BuildDependencyGap struct {
	Dependency string   `json:"dependency"`
	RequiredBy []string `json:"required_by"`
}

// BuildYZMALibsResult is the structured command output for local runtime build pipeline.
type BuildYZMALibsResult struct {
	Status string `json:"status"`
	Stage  string `json:"stage,omitempty"`
	Reason string `json:"reason,omitempty"`

	Platform string `json:"platform"`
	Arch     string `json:"arch"`
	Backend  string `json:"backend"`

	InstallDir  string `json:"install_dir"`
	BuildRoot   string `json:"build_root"`
	RepoDir     string `json:"repo_dir,omitempty"`
	Collected   int    `json:"collected_count,omitempty"`
	Installed   int    `json:"installed_count,omitempty"`
	DurationMs  int64  `json:"duration_ms,omitempty"`
	LlamaRef    string `json:"llama_ref,omitempty"`
	LlamaCommit string `json:"llama_commit,omitempty"`
	YZMAVersion string `json:"yzma_version,omitempty"`

	CollectedDylibs []string             `json:"collected_dylibs,omitempty"`
	InstalledDylibs []string             `json:"installed_dylibs,omitempty"`
	SkippedDylibs   []string             `json:"skipped_dylibs,omitempty"`
	MissingDeps     []BuildDependencyGap `json:"missing_dependencies,omitempty"`

	Probe   *ProbeResult `json:"probe,omitempty"`
	Message string       `json:"message,omitempty"`
}

type shellCommandRunner interface {
	Run(ctx context.Context, dir string, name string, args ...string) (string, error)
}

type execRunner struct {
	logger *obs.Logger
}

func (r execRunner) Run(ctx context.Context, dir string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	output := strings.TrimSpace(string(out))
	if r.logger != nil && r.logger.Enabled() {
		r.logger.Printf("localruntime cmd=%s args=%s dir=%s", name, strings.Join(args, " "), dir)
		if output != "" {
			r.logger.Printf("localruntime cmd_output=%s", output)
		}
	}
	if err != nil {
		if output == "" {
			return output, fmt.Errorf("%s %s failed: %w", name, strings.Join(args, " "), err)
		}
		return output, fmt.Errorf("%s %s failed: %w (%s)", name, strings.Join(args, " "), err, output)
	}
	return output, nil
}

func (s Service) BuildYZMALibs(ctx context.Context, cfg config.Config, opts BuildYZMALibsOptions) (BuildYZMALibsResult, error) {
	return s.buildYZMALibsWithRunner(ctx, cfg, opts, execRunner{logger: s.logger})
}

func (s Service) buildYZMALibsWithRunner(ctx context.Context, cfg config.Config, opts BuildYZMALibsOptions, runner shellCommandRunner) (BuildYZMALibsResult, error) {
	started := time.Now()
	result := BuildYZMALibsResult{
		Status:   "failed",
		Stage:    "scope_guard",
		Platform: runtime.GOOS,
		Arch:     runtime.GOARCH,
		Backend:  normalizedBackend(opts.Backend),
	}
	if result.Backend == "" {
		result.Backend = "metal"
	}
	installDir := canonicalizePath(opts.InstallDir)
	if installDir == "" {
		installDir = canonicalizePath(config.DefaultYZMARuntimeLibDir())
	}
	result.InstallDir = installDir

	buildRoot := canonicalizePath(defaultYZMABuildRoot())
	result.BuildRoot = buildRoot
	repoDir := filepath.Join(buildRoot, "llama.cpp")
	result.RepoDir = repoDir

	defer func() {
		result.DurationMs = time.Since(started).Milliseconds()
	}()

	if err := validateBuildScope(runtime.GOOS, runtime.GOARCH, result.Backend); err != nil {
		result.Reason = strings.TrimSpace(err.Error())
		return result, nil
	}

	llamaRef, yzmaVersion, refErr := resolvePinnedLlamaRef(opts.LlamaRef)
	if refErr != nil {
		result.Stage = "pin_resolution"
		result.Reason = strings.TrimSpace(refErr.Error())
		result.YZMAVersion = yzmaVersion
		return result, nil
	}
	result.LlamaRef = llamaRef
	result.YZMAVersion = yzmaVersion

	if opts.Clean {
		if err := os.RemoveAll(buildRoot); err != nil {
			result.Stage = "source_prepare"
			result.Reason = fmt.Sprintf("clean build root: %v", err)
			return result, nil
		}
	}
	if err := os.MkdirAll(buildRoot, 0o755); err != nil {
		result.Stage = "source_prepare"
		result.Reason = fmt.Sprintf("create build root: %v", err)
		return result, nil
	}

	result.Stage = "source_prepare"
	if err := prepareLlamaSource(ctx, runner, buildRoot, repoDir, llamaRef); err != nil {
		result.Reason = strings.TrimSpace(err.Error())
		return result, nil
	}
	if commit, err := gitResolveCommit(ctx, runner, repoDir); err == nil {
		result.LlamaCommit = strings.TrimSpace(commit)
	}

	buildDir := filepath.Join(repoDir, "build")
	if opts.Clean {
		_ = os.RemoveAll(buildDir)
	}
	result.Stage = "build"
	jobs := opts.Jobs
	if jobs <= 0 {
		jobs = runtime.NumCPU()
		if jobs <= 0 {
			jobs = 1
		}
	}
	if err := runLlamaBuild(ctx, runner, repoDir, jobs); err != nil {
		result.Reason = strings.TrimSpace(err.Error())
		return result, nil
	}

	result.Stage = "inventory"
	inventory, invErr := collectDylibInventory(buildDir)
	if invErr != nil {
		result.Reason = strings.TrimSpace(invErr.Error())
		return result, nil
	}
	result.Collected = len(inventory)
	result.CollectedDylibs = baseNames(inventory)

	selected, skipped, selErr := selectAllowlistedDylibs(inventory)
	result.SkippedDylibs = baseNames(skipped)
	if selErr != nil {
		result.Reason = strings.TrimSpace(selErr.Error())
		return result, nil
	}

	stageDir := filepath.Join(buildRoot, "bundle")
	_ = os.RemoveAll(stageDir)
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		result.Stage = "bundle"
		result.Reason = fmt.Sprintf("create bundle stage dir: %v", err)
		return result, nil
	}
	installedNames, copyErr := stageSelectedDylibs(selected, stageDir)
	if copyErr != nil {
		result.Stage = "bundle"
		result.Reason = strings.TrimSpace(copyErr.Error())
		return result, nil
	}
	result.InstalledDylibs = append([]string(nil), installedNames...)
	result.Installed = len(installedNames)

	result.Stage = "linkage"
	if err := normalizeBundleRPaths(ctx, runner, stageDir, installedNames); err != nil {
		result.Reason = strings.TrimSpace(err.Error())
		return result, nil
	}
	if leakedRPaths, err := findExternalRPathLeaks(ctx, runner, stageDir, installedNames); err != nil {
		result.Reason = strings.TrimSpace(err.Error())
		return result, nil
	} else if len(leakedRPaths) > 0 {
		result.Reason = "bundle contains external LC_RPATH entries; runtime is not dependency-closed"
		return result, nil
	}

	result.Stage = "dependency_closure"
	depsByFile, depErr := readBundleDependencies(ctx, runner, stageDir, installedNames)
	if depErr != nil {
		result.Reason = strings.TrimSpace(depErr.Error())
		return result, nil
	}
	gaps := findMissingBundleDependencies(installedNames, depsByFile)
	if len(gaps) > 0 {
		result.Reason = "unknown or unbundled non-system dylib dependencies detected"
		result.MissingDeps = gaps
		return result, nil
	}

	result.Stage = "install"
	meta := runtimeBundleMetadata{
		Manager:     "tops",
		Runtime:     "llama.cpp",
		LlamaRef:    result.LlamaRef,
		LlamaCommit: result.LlamaCommit,
		YZMAVersion: result.YZMAVersion,
		Platform:    result.Platform,
		Arch:        result.Arch,
		Backend:     result.Backend,
		SharedLibs:  append([]string(nil), installedNames...),
	}
	if err := writeRuntimeMetadata(stageDir, meta); err != nil {
		result.Reason = strings.TrimSpace(err.Error())
		return result, nil
	}
	if err := installBundleAtomic(stageDir, installDir); err != nil {
		result.Reason = strings.TrimSpace(err.Error())
		return result, nil
	}

	result.Stage = "probe"
	probeCfg := normalizeConfig(cfg)
	probeCfg.Provider.Type = config.ProviderYZMA
	probeCfg.Provider.LibPath = installDir
	probe, probeErr := s.ProbeYZMAWithOptions(ctx, probeCfg, ProbeOptions{
		Source:             "build_post_install",
		DisableCPUFallback: true,
		Generate:           true,
	})
	if probeErr != nil {
		result.Reason = strings.TrimSpace(probeErr.Error())
		result.Message = "Built libraries, but YZMA validation failed.\nThis usually means llama.cpp/YZMA ABI mismatch or missing backend dependency."
		return result, nil
	}
	result.Probe = &probe
	if !strings.EqualFold(probe.Status, "ok") {
		result.Reason = strings.TrimSpace(probe.Reason)
		result.Message = "Built libraries, but YZMA validation failed.\nThis usually means llama.cpp/YZMA ABI mismatch or missing backend dependency."
		return result, nil
	}

	result.Status = "ok"
	result.Stage = "completed"
	result.Reason = "llama.cpp build, install, and YZMA probe succeeded"
	return result, nil
}

func normalizedBackend(raw string) string {
	backend := strings.ToLower(strings.TrimSpace(raw))
	if backend == "" {
		return "metal"
	}
	return backend
}

func validateBuildScope(goos, goarch, backend string) error {
	if goos != "darwin" || goarch != "arm64" {
		return fmt.Errorf("tps local build-yzma-libs is currently macOS arm64 Metal-only")
	}
	if strings.TrimSpace(backend) != "metal" {
		return fmt.Errorf("backend %q is not supported in this milestone; use --backend metal", backend)
	}
	return nil
}

func defaultYZMABuildRoot() string {
	return filepath.Join(".build", "tops-yzma")
}

func resolvePinnedLlamaRef(explicitRef string) (string, string, error) {
	yzmaVersion := resolveYZMAVersionFromBuildInfo()
	ref, err := resolvePinnedLlamaRefForVersion(explicitRef, yzmaVersion, yzmaLlamaRefPins)
	return ref, yzmaVersion, err
}

func resolvePinnedLlamaRefForVersion(explicitRef string, yzmaVersion string, pins map[string]string) (string, error) {
	ref := strings.TrimSpace(explicitRef)
	if ref != "" {
		return ref, nil
	}
	if strings.TrimSpace(yzmaVersion) == "" {
		return "", fmt.Errorf("could not resolve yzma module version from build metadata; pass --llama-ref explicitly")
	}
	key := strings.TrimSpace(yzmaVersion)
	if !strings.HasPrefix(key, "v") {
		key = "v" + key
	}
	if mapped, ok := pins[key]; ok && strings.TrimSpace(mapped) != "" {
		return strings.TrimSpace(mapped), nil
	}
	return "", fmt.Errorf("no default llama.cpp ref pin is configured for yzma version %q; pass --llama-ref explicitly", yzmaVersion)
}

func resolveYZMAVersionFromBuildInfo() string {
	info, ok := debug.ReadBuildInfo()
	if !ok || info == nil {
		return ""
	}
	if strings.TrimSpace(info.Main.Path) == "github.com/hybridgroup/yzma" {
		return strings.TrimSpace(info.Main.Version)
	}
	for _, dep := range info.Deps {
		if dep == nil {
			continue
		}
		if strings.TrimSpace(dep.Path) == "github.com/hybridgroup/yzma" {
			return strings.TrimSpace(dep.Version)
		}
	}
	return ""
}

func prepareLlamaSource(ctx context.Context, runner shellCommandRunner, buildRoot, repoDir, ref string) error {
	if stat, err := os.Stat(repoDir); err == nil && stat.IsDir() {
		if _, err := runner.Run(ctx, buildRoot, "git", "-C", repoDir, "fetch", "--tags", "--prune", "origin"); err != nil {
			return fmt.Errorf("update llama.cpp source: %w", err)
		}
	} else {
		if _, err := runner.Run(ctx, buildRoot, "git", "clone", defaultLlamaRepoURL, repoDir); err != nil {
			return fmt.Errorf("clone llama.cpp source: %w", err)
		}
	}
	if _, err := runner.Run(ctx, buildRoot, "git", "-C", repoDir, "checkout", ref); err != nil {
		return fmt.Errorf("checkout llama.cpp ref %q: %w", ref, err)
	}
	return nil
}

func gitResolveCommit(ctx context.Context, runner shellCommandRunner, repoDir string) (string, error) {
	out, err := runner.Run(ctx, repoDir, "git", "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("resolve llama.cpp commit: %w", err)
	}
	return strings.TrimSpace(out), nil
}

func runLlamaBuild(ctx context.Context, runner shellCommandRunner, repoDir string, jobs int) error {
	configureArgs := []string{
		"-B", "build",
		"-DCMAKE_BUILD_TYPE=Release",
		"-DBUILD_SHARED_LIBS=ON",
		"-DLLAMA_ALL_WARNINGS=OFF",
		"-DLLAMA_OPENSSL=ON",
		"-DLLAMA_USE_SYSTEM_GGML=OFF",
		"-DGGML_METAL=ON",
		"-DGGML_METAL_EMBED_LIBRARY=ON",
		"-DLLAMA_BUILD_TESTS=OFF",
		"-DLLAMA_BUILD_EXAMPLES=OFF",
		"-DLLAMA_BUILD_SERVER=OFF",
	}
	if _, err := runner.Run(ctx, repoDir, "cmake", configureArgs...); err != nil {
		return fmt.Errorf("cmake configure failed: %w", err)
	}
	buildArgs := []string{"--build", "build", "-j", strconv.Itoa(jobs)}
	if _, err := runner.Run(ctx, repoDir, "cmake", buildArgs...); err != nil {
		return fmt.Errorf("cmake build failed: %w", err)
	}
	return nil
}

func collectDylibInventory(buildDir string) ([]string, error) {
	entries := make([]string, 0, 32)
	err := filepath.WalkDir(buildDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".dylib") {
			return nil
		}
		absPath := path
		if normalized, err := filepath.Abs(path); err == nil {
			absPath = normalized
		}
		entries = append(entries, absPath)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("collect dylib inventory: %w", err)
	}
	sort.Strings(entries)
	return entries, nil
}

func selectAllowlistedDylibs(inventory []string) ([]string, []string, error) {
	selected := make([]string, 0, len(inventory))
	skipped := make([]string, 0, len(inventory))
	foundFamilies := map[string]bool{}

	for _, libPath := range inventory {
		base := strings.ToLower(filepath.Base(strings.TrimSpace(libPath)))
		family := dylibFamily(base)
		if family == "" {
			skipped = append(skipped, libPath)
			continue
		}
		foundFamilies[family] = true
		selected = append(selected, libPath)
	}
	missingFamilies := make([]string, 0, len(requiredDylibFamilies))
	for _, family := range requiredDylibFamilies {
		if foundFamilies[family] {
			continue
		}
		missingFamilies = append(missingFamilies, family)
	}
	if len(missingFamilies) > 0 {
		return selected, skipped, fmt.Errorf("required dylib families are missing from build output: %s", strings.Join(missingFamilies, ", "))
	}
	return selected, skipped, nil
}

func dylibFamily(base string) string {
	base = strings.ToLower(strings.TrimSpace(base))
	if base == "" {
		return ""
	}
	for _, family := range append(append([]string{}, requiredDylibFamilies...), optionalDylibFamilies...) {
		if matchesDylibFamily(base, family) {
			return family
		}
	}
	return ""
}

func matchesDylibFamily(base string, family string) bool {
	base = strings.ToLower(strings.TrimSpace(base))
	family = strings.ToLower(strings.TrimSpace(family))
	if base == "" || family == "" {
		return false
	}
	if family == "libggml" {
		return strings.HasPrefix(base, "libggml.") || base == "libggml.dylib"
	}
	return strings.HasPrefix(base, family+".") || base == family+".dylib"
}

func stageSelectedDylibs(selected []string, stageDir string) ([]string, error) {
	nameToPath := map[string]string{}
	for _, source := range selected {
		base := filepath.Base(strings.TrimSpace(source))
		if strings.TrimSpace(base) == "" {
			continue
		}
		if _, exists := nameToPath[base]; exists {
			continue
		}
		nameToPath[base] = source
	}
	installed := make([]string, 0, len(nameToPath))
	for _, name := range sortedMapKeys(nameToPath) {
		source := nameToPath[name]
		target := filepath.Join(stageDir, name)
		if err := copyFileFollowLinks(source, target); err != nil {
			return nil, fmt.Errorf("stage dylib %q: %w", name, err)
		}
		installed = append(installed, name)
	}
	sort.Strings(installed)
	return installed, nil
}

func readBundleDependencies(ctx context.Context, runner shellCommandRunner, stageDir string, installed []string) (map[string][]string, error) {
	depsByFile := map[string][]string{}
	for _, name := range installed {
		path := filepath.Join(stageDir, name)
		out, err := runner.Run(ctx, stageDir, "otool", "-L", path)
		if err != nil {
			return nil, fmt.Errorf("otool -L %s: %w", name, err)
		}
		deps, parseErr := parseOtoolDependencies(out)
		if parseErr != nil {
			return nil, fmt.Errorf("parse otool output for %s: %w", name, parseErr)
		}
		depsByFile[name] = deps
	}
	return depsByFile, nil
}

func normalizeBundleRPaths(ctx context.Context, runner shellCommandRunner, stageDir string, installed []string) error {
	for _, name := range installed {
		path := filepath.Join(stageDir, name)
		rpaths, err := readBundleRPaths(ctx, runner, stageDir, path)
		if err != nil {
			return fmt.Errorf("read LC_RPATH for %s: %w", name, err)
		}
		hasLoaderPath := false
		for _, rpath := range rpaths {
			if strings.EqualFold(strings.TrimSpace(rpath), "@loader_path") {
				hasLoaderPath = true
				continue
			}
			if _, err := runner.Run(ctx, stageDir, "install_name_tool", "-delete_rpath", rpath, path); err != nil {
				return fmt.Errorf("remove rpath %q from %s: %w", rpath, name, err)
			}
		}
		if !hasLoaderPath {
			if _, err := runner.Run(ctx, stageDir, "install_name_tool", "-add_rpath", "@loader_path", path); err != nil {
				return fmt.Errorf("add @loader_path rpath to %s: %w", name, err)
			}
		}
	}
	return nil
}

func findExternalRPathLeaks(ctx context.Context, runner shellCommandRunner, stageDir string, installed []string) (map[string][]string, error) {
	leaks := map[string][]string{}
	for _, name := range installed {
		path := filepath.Join(stageDir, name)
		rpaths, err := readBundleRPaths(ctx, runner, stageDir, path)
		if err != nil {
			return nil, fmt.Errorf("read LC_RPATH for %s: %w", name, err)
		}
		for _, rpath := range rpaths {
			rpath = strings.TrimSpace(rpath)
			if rpath == "" || strings.EqualFold(rpath, "@loader_path") {
				continue
			}
			leaks[name] = append(leaks[name], rpath)
		}
		sort.Strings(leaks[name])
	}
	return leaks, nil
}

func readBundleRPaths(ctx context.Context, runner shellCommandRunner, stageDir, dylibPath string) ([]string, error) {
	out, err := runner.Run(ctx, stageDir, "otool", "-l", dylibPath)
	if err != nil {
		return nil, err
	}
	return parseOtoolRPaths(out), nil
}

func parseOtoolRPaths(output string) []string {
	lines := strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
	seen := map[string]struct{}{}
	rpaths := make([]string, 0, 4)
	for idx := 0; idx < len(lines); idx++ {
		line := strings.TrimSpace(lines[idx])
		if line != "cmd LC_RPATH" {
			continue
		}
		for j := idx + 1; j < len(lines); j++ {
			next := strings.TrimSpace(lines[j])
			if strings.HasPrefix(next, "cmd ") {
				break
			}
			if !strings.HasPrefix(next, "path ") {
				continue
			}
			value := strings.TrimSpace(strings.TrimPrefix(next, "path "))
			if value == "" {
				break
			}
			if cut := strings.Index(value, " ("); cut > 0 {
				value = strings.TrimSpace(value[:cut])
			}
			if value == "" {
				break
			}
			if _, exists := seen[value]; !exists {
				seen[value] = struct{}{}
				rpaths = append(rpaths, value)
			}
			break
		}
	}
	sort.Strings(rpaths)
	return rpaths
}

func parseOtoolDependencies(output string) ([]string, error) {
	lines := strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
	if len(lines) == 0 {
		return nil, fmt.Errorf("empty otool output")
	}
	deps := make([]string, 0, len(lines))
	for idx, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if idx == 0 && strings.HasSuffix(line, ":") {
			continue
		}
		parts := strings.SplitN(line, " (", 2)
		dep := strings.TrimSpace(parts[0])
		if dep == "" || strings.HasSuffix(dep, ":") {
			continue
		}
		deps = append(deps, dep)
	}
	return deps, nil
}

func findMissingBundleDependencies(installed []string, depsByFile map[string][]string) []BuildDependencyGap {
	bundled := map[string]struct{}{}
	for _, name := range installed {
		bundled[strings.ToLower(strings.TrimSpace(name))] = struct{}{}
	}

	gaps := map[string]map[string]struct{}{}
	for consumer, deps := range depsByFile {
		for _, dep := range deps {
			if isSystemDependency(dep) {
				continue
			}
			base := strings.ToLower(filepath.Base(strings.TrimSpace(dep)))
			if base == "" {
				continue
			}
			if _, ok := bundled[base]; ok {
				continue
			}
			if _, exists := gaps[base]; !exists {
				gaps[base] = map[string]struct{}{}
			}
			gaps[base][consumer] = struct{}{}
		}
	}

	out := make([]BuildDependencyGap, 0, len(gaps))
	for dep, consumers := range gaps {
		item := BuildDependencyGap{
			Dependency: dep,
			RequiredBy: sortedMapKeys(consumers),
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Dependency < out[j].Dependency
	})
	return out
}

func isSystemDependency(dep string) bool {
	value := strings.TrimSpace(dep)
	if value == "" {
		return true
	}
	systemPrefixes := []string{
		"/usr/lib/",
		"/System/Library/",
		"/System/iOSSupport/System/Library/",
		"/System/iOSSupport/usr/lib/",
	}
	for _, prefix := range systemPrefixes {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

type runtimeBundleMetadata struct {
	Manager     string   `json:"manager"`
	Runtime     string   `json:"runtime"`
	LlamaRef    string   `json:"llama_ref"`
	LlamaCommit string   `json:"llama_commit"`
	YZMAVersion string   `json:"yzma_version"`
	Platform    string   `json:"platform"`
	Arch        string   `json:"arch"`
	Backend     string   `json:"backend"`
	BuiltAt     string   `json:"built_at"`
	SharedLibs  []string `json:"shared_libs"`
}

func writeRuntimeMetadata(stageDir string, meta runtimeBundleMetadata) error {
	meta.BuiltAt = time.Now().UTC().Format(time.RFC3339)
	sort.Strings(meta.SharedLibs)
	blob, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal version metadata: %w", err)
	}
	path := filepath.Join(stageDir, "version.json")
	if err := os.WriteFile(path, append(blob, '\n'), 0o644); err != nil {
		return fmt.Errorf("write version metadata: %w", err)
	}
	return nil
}

func installBundleAtomic(stageDir, installDir string) error {
	parent := filepath.Dir(installDir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create install parent directory: %w", err)
	}

	tempDir := filepath.Join(parent, fmt.Sprintf(".tops-yzma-lib-%d", time.Now().UnixNano()))
	if err := copyDirectory(stageDir, tempDir); err != nil {
		return fmt.Errorf("copy staged bundle to temp install dir: %w", err)
	}
	defer os.RemoveAll(tempDir)

	backupDir := ""
	if _, err := os.Stat(installDir); err == nil {
		backupDir = installDir + ".backup-" + strconv.FormatInt(time.Now().UnixNano(), 10)
		if err := os.Rename(installDir, backupDir); err != nil {
			return fmt.Errorf("backup existing install dir: %w", err)
		}
	}
	if err := os.Rename(tempDir, installDir); err != nil {
		if backupDir != "" {
			_ = os.Rename(backupDir, installDir)
		}
		return fmt.Errorf("activate installed bundle: %w", err)
	}
	if backupDir != "" {
		_ = os.RemoveAll(backupDir)
	}
	return nil
}

func copyDirectory(srcDir, dstDir string) error {
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}
	return filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		targetPath := filepath.Join(dstDir, rel)
		if d.IsDir() {
			return os.MkdirAll(targetPath, 0o755)
		}
		return copyFileFollowLinks(path, targetPath)
	})
}

func copyFileFollowLinks(source, target string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	readPath := source
	if info.Mode()&os.ModeSymlink != 0 {
		resolved, err := filepath.EvalSymlinks(source)
		if err != nil {
			return err
		}
		readPath = resolved
	}
	in, err := os.Open(readPath)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	out, err := os.Create(target)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Chmod(0o644); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func baseNames(paths []string) []string {
	names := make([]string, 0, len(paths))
	for _, path := range paths {
		base := filepath.Base(strings.TrimSpace(path))
		if strings.TrimSpace(base) == "" {
			continue
		}
		names = append(names, base)
	}
	sort.Strings(names)
	return names
}

func sortedMapKeys[T any](items map[string]T) []string {
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
