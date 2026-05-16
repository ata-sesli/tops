package localruntime

import (
	"path/filepath"
	"strings"
	"testing"

	"tops/internal/config"
)

func TestValidateBuildScope(t *testing.T) {
	if err := validateBuildScope("darwin", "arm64", "metal"); err != nil {
		t.Fatalf("expected darwin/arm64 metal to pass, got %v", err)
	}
	if err := validateBuildScope("linux", "amd64", "metal"); err == nil {
		t.Fatal("expected non-darwin/arm64 scope to fail")
	}
	if err := validateBuildScope("darwin", "arm64", "cpu"); err == nil {
		t.Fatal("expected non-metal backend to fail")
	}
}

func TestSelectAllowlistedDylibs(t *testing.T) {
	inventory := []string{
		"/tmp/libllama.dylib",
		"/tmp/libggml.dylib",
		"/tmp/libggml-base.dylib",
		"/tmp/libggml-cpu.dylib",
		"/tmp/libggml-metal.dylib",
		"/tmp/libggml-blas.dylib",
		"/tmp/libunknown.dylib",
	}
	selected, skipped, err := selectAllowlistedDylibs(inventory)
	if err != nil {
		t.Fatalf("select allowlisted dylibs: %v", err)
	}
	if len(selected) != 6 {
		t.Fatalf("expected 6 selected dylibs, got %d", len(selected))
	}
	if len(skipped) != 1 || !strings.Contains(skipped[0], "libunknown.dylib") {
		t.Fatalf("expected skipped unknown dylib, got %+v", skipped)
	}
}

func TestSelectAllowlistedDylibsFailsWhenRequiredMissing(t *testing.T) {
	inventory := []string{
		"/tmp/libllama.dylib",
		"/tmp/libggml.dylib",
		"/tmp/libggml-base.dylib",
		"/tmp/libggml-cpu.dylib",
	}
	_, _, err := selectAllowlistedDylibs(inventory)
	if err == nil {
		t.Fatal("expected missing required family error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "missing") {
		t.Fatalf("expected missing-family error text, got %v", err)
	}
}

func TestParseOtoolDependencies(t *testing.T) {
	raw := `
/tmp/libllama.dylib:
	@rpath/libggml.dylib (compatibility version 0.0.0, current version 0.0.0)
	/usr/lib/libSystem.B.dylib (compatibility version 1.0.0, current version 1351.0.0)
`
	deps, err := parseOtoolDependencies(raw)
	if err != nil {
		t.Fatalf("parse otool dependencies: %v", err)
	}
	if len(deps) != 2 {
		t.Fatalf("expected 2 dependencies, got %d (%+v)", len(deps), deps)
	}
	if deps[0] != "@rpath/libggml.dylib" {
		t.Fatalf("unexpected first dep: %q", deps[0])
	}
}

func TestParseOtoolRPaths(t *testing.T) {
	raw := `
Load command 12
          cmd LC_RPATH
      cmdsize 80
         path /Users/me/work/llama.cpp/build/bin (offset 12)
Load command 13
          cmd LC_RPATH
      cmdsize 32
         path @loader_path (offset 12)
Load command 14
          cmd LC_RPATH
      cmdsize 32
         path @loader_path (offset 12)
`
	rpaths := parseOtoolRPaths(raw)
	if len(rpaths) != 2 {
		t.Fatalf("expected 2 unique rpaths, got %d (%+v)", len(rpaths), rpaths)
	}
	if rpaths[0] != "/Users/me/work/llama.cpp/build/bin" {
		t.Fatalf("unexpected first rpath: %q", rpaths[0])
	}
	if rpaths[1] != "@loader_path" {
		t.Fatalf("unexpected second rpath: %q", rpaths[1])
	}
}

func TestFindMissingBundleDependencies(t *testing.T) {
	installed := []string{"libllama.dylib", "libggml.dylib"}
	depsByFile := map[string][]string{
		"libllama.dylib": {
			"/usr/lib/libSystem.B.dylib",
			"@rpath/libggml.dylib",
			"@rpath/libmissing.dylib",
			"/opt/custom/libexternal.dylib",
		},
	}
	gaps := findMissingBundleDependencies(installed, depsByFile)
	if len(gaps) != 2 {
		t.Fatalf("expected 2 missing deps, got %d (%+v)", len(gaps), gaps)
	}
}

func TestResolvePinnedLlamaRefForVersion(t *testing.T) {
	pins := map[string]string{"v1.13.0": "b8920"}
	ref, err := resolvePinnedLlamaRefForVersion("", "v1.13.0", pins)
	if err != nil {
		t.Fatalf("resolve pinned ref: %v", err)
	}
	if ref != "b8920" {
		t.Fatalf("unexpected ref: %q", ref)
	}

	override, err := resolvePinnedLlamaRefForVersion("custom-ref", "v1.13.0", pins)
	if err != nil {
		t.Fatalf("resolve overridden ref: %v", err)
	}
	if override != "custom-ref" {
		t.Fatalf("expected explicit override, got %q", override)
	}

	if _, err := resolvePinnedLlamaRefForVersion("", "v9.9.9", pins); err == nil {
		t.Fatal("expected missing pin mapping error")
	}
}

func TestResolvedLibPathUsesTOPSDefaultWhenUnset(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("YZMA_LIB", "")
	cfg := config.Config{
		Provider: config.ProviderConfig{
			Type:  config.ProviderYZMA,
			Model: "gemma4:e4b",
		},
	}
	got := resolvedLibPath(cfg)
	want := filepath.Clean(config.DefaultYZMARuntimeLibDir())
	if got != want {
		t.Fatalf("expected default runtime lib dir %q, got %q", want, got)
	}
}

func TestResolvedLibPathPriority(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	envLib := filepath.Join(t.TempDir(), "env-libs")
	t.Setenv("YZMA_LIB", envLib)
	cfg := config.Config{
		Provider: config.ProviderConfig{
			Type:  config.ProviderYZMA,
			Model: "gemma4:e4b",
		},
	}
	if got := resolvedLibPath(cfg); got != filepath.Clean(envLib) {
		t.Fatalf("expected env lib path %q, got %q", filepath.Clean(envLib), got)
	}

	providerLib := filepath.Join(t.TempDir(), "provider-libs")
	cfg.Provider.LibPath = providerLib
	if got := resolvedLibPath(cfg); got != filepath.Clean(providerLib) {
		t.Fatalf("expected provider lib path %q, got %q", filepath.Clean(providerLib), got)
	}
}
