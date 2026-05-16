package systemcontext

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"tops/internal/model"
)

type commandRunner interface {
	Run(ctx context.Context, name string, args ...string) (string, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func Detect() model.PlatformContext {
	return detectWithRunner(context.Background(), execRunner{}, runtime.GOOS, runtime.GOARCH)
}

func detectWithRunner(parent context.Context, runner commandRunner, goos string, arch string) model.PlatformContext {
	ctx, cancel := context.WithTimeout(parent, 500*time.Millisecond)
	defer cancel()

	family := model.NormalizeOSFamily(goos)
	platform := model.PlatformContext{
		OSFamily: family,
		Arch:     strings.TrimSpace(arch),
	}

	switch family {
	case "macos":
		platform.OSName = "macOS"
		platform.KernelName = "Darwin"
		populateMacOSMetadata(ctx, runner, &platform)
	case "linux":
		platform.OSName = "Linux"
		platform.KernelName = "Linux"
		populateLinuxMetadata(ctx, runner, &platform)
	case "windows":
		platform.OSName = "Windows"
		platform.KernelName = "Windows NT"
		populateWindowsMetadata(ctx, runner, &platform)
	default:
		platform.OSName = strings.TrimSpace(runtime.GOOS)
	}
	if strings.TrimSpace(platform.KernelVersion) == "" {
		if kernelVersion, err := runner.Run(ctx, "uname", "-r"); err == nil {
			platform.KernelVersion = strings.TrimSpace(kernelVersion)
		}
	}
	return model.NormalizePlatformContext(platform)
}

func populateMacOSMetadata(ctx context.Context, runner commandRunner, platform *model.PlatformContext) {
	if platform == nil {
		return
	}
	if productVersion, err := runner.Run(ctx, "sw_vers", "-productVersion"); err == nil {
		platform.OSVersion = strings.TrimSpace(productVersion)
	}
	if kernelVersion, err := runner.Run(ctx, "uname", "-r"); err == nil {
		platform.KernelVersion = strings.TrimSpace(kernelVersion)
	}
}

func populateLinuxMetadata(ctx context.Context, runner commandRunner, platform *model.PlatformContext) {
	if platform == nil {
		return
	}
	if name, version := parseLinuxOSRelease("/etc/os-release"); strings.TrimSpace(name) != "" || strings.TrimSpace(version) != "" {
		if strings.TrimSpace(name) != "" {
			platform.OSName = strings.TrimSpace(name)
		}
		platform.OSVersion = strings.TrimSpace(version)
	}
	if kernelVersion, err := runner.Run(ctx, "uname", "-r"); err == nil {
		platform.KernelVersion = strings.TrimSpace(kernelVersion)
	}
}

func populateWindowsMetadata(ctx context.Context, runner commandRunner, platform *model.PlatformContext) {
	if platform == nil {
		return
	}
	if raw, err := runner.Run(ctx, "cmd", "/c", "ver"); err == nil {
		version := parseWindowsVersion(raw)
		if strings.TrimSpace(version) != "" {
			platform.OSVersion = version
		}
		if strings.TrimSpace(platform.KernelVersion) == "" {
			platform.KernelVersion = version
		}
	}
}

func parseLinuxOSRelease(path string) (string, string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	lines := strings.Split(string(data), "\n")
	values := map[string]string{}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		val = strings.Trim(val, "\"")
		values[key] = val
	}
	name := strings.TrimSpace(values["NAME"])
	version := strings.TrimSpace(values["VERSION_ID"])
	if version == "" {
		version = strings.TrimSpace(values["VERSION"])
	}
	return name, version
}

func parseWindowsVersion(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	open := strings.Index(raw, "[Version")
	close := strings.Index(raw, "]")
	if open != -1 && close != -1 && close > open {
		segment := strings.TrimSpace(raw[open+len("[Version") : close])
		return strings.TrimSpace(segment)
	}
	return raw
}
