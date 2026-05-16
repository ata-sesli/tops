package platformresolver

import (
	"fmt"
	"strings"

	"tops/internal/model"
)

type TemplateKey string

const (
	TemplateOSInfo                    TemplateKey = "os_info"
	TemplateKernelInfo                TemplateKey = "kernel_info"
	TemplatePWD                       TemplateKey = "pwd"
	TemplateCurrentUser               TemplateKey = "current_user"
	TemplateHostname                  TemplateKey = "hostname"
	TemplateDiskUsage                 TemplateKey = "disk_usage"
	TemplateDirCountCurrentDirectory  TemplateKey = "dir_count_current_directory"
	TemplateFileCountCurrentDirectory TemplateKey = "file_count_current_directory"
	TemplateToolVersion               TemplateKey = "tool_version"
)

type ResolveParams struct {
	Visibility string
	Recursion  string
	ToolName   string
}

type ResolvedCommand struct {
	TemplateID       string
	CommandName      string
	Args             []string
	OutputLineLimit  int
	ExpectedEvidence string
}

type Resolver interface {
	ResolveTemplate(key TemplateKey, platform model.PlatformContext, params ResolveParams) (ResolvedCommand, error)
}

type defaultResolver struct{}

func New() Resolver {
	return defaultResolver{}
}

func (defaultResolver) ResolveTemplate(key TemplateKey, platform model.PlatformContext, params ResolveParams) (ResolvedCommand, error) {
	platform = model.NormalizePlatformContext(platform)
	switch key {
	case TemplateOSInfo:
		switch platform.OSFamily {
		case "macos":
			return ResolvedCommand{
				TemplateID:       "os_info",
				CommandName:      "sw_vers",
				Args:             []string{},
				OutputLineLimit:  80,
				ExpectedEvidence: "OS product and version",
			}, nil
		case "linux":
			return ResolvedCommand{
				TemplateID:       "os_info",
				CommandName:      "cat",
				Args:             []string{"/etc/os-release"},
				OutputLineLimit:  120,
				ExpectedEvidence: "Linux distro release metadata",
			}, nil
		case "windows":
			return ResolvedCommand{
				TemplateID:       "os_info",
				CommandName:      "cmd",
				Args:             []string{"/c", "ver"},
				OutputLineLimit:  40,
				ExpectedEvidence: "Windows version string",
			}, nil
		default:
			return ResolvedCommand{}, fmt.Errorf("platform %q does not support template %q", platform.OSFamily, key)
		}
	case TemplateKernelInfo:
		switch platform.OSFamily {
		case "windows":
			return ResolvedCommand{
				TemplateID:       "kernel_info",
				CommandName:      "cmd",
				Args:             []string{"/c", "ver"},
				OutputLineLimit:  40,
				ExpectedEvidence: "Windows kernel/version string",
			}, nil
		default:
			return ResolvedCommand{
				TemplateID:       "kernel_info",
				CommandName:      "uname",
				Args:             []string{"-srm"},
				OutputLineLimit:  40,
				ExpectedEvidence: "Kernel and architecture information",
			}, nil
		}
	case TemplatePWD:
		if platform.OSFamily == "windows" {
			return ResolvedCommand{
				TemplateID:       "current_directory",
				CommandName:      "cmd",
				Args:             []string{"/c", "cd"},
				OutputLineLimit:  20,
				ExpectedEvidence: "Current working directory",
			}, nil
		}
		return ResolvedCommand{
			TemplateID:       "current_directory",
			CommandName:      "pwd",
			Args:             []string{},
			OutputLineLimit:  20,
			ExpectedEvidence: "Current working directory",
		}, nil
	case TemplateCurrentUser:
		return ResolvedCommand{
			TemplateID:       "current_user",
			CommandName:      "whoami",
			Args:             []string{},
			OutputLineLimit:  20,
			ExpectedEvidence: "Current user",
		}, nil
	case TemplateHostname:
		return ResolvedCommand{
			TemplateID:       "hostname",
			CommandName:      "hostname",
			Args:             []string{},
			OutputLineLimit:  20,
			ExpectedEvidence: "Host name",
		}, nil
	case TemplateDiskUsage:
		if platform.OSFamily == "windows" {
			return ResolvedCommand{
				TemplateID:       "disk_usage",
				CommandName:      "cmd",
				Args:             []string{"/c", "wmic logicaldisk get size,freespace,caption"},
				OutputLineLimit:  120,
				ExpectedEvidence: "Disk usage summary",
			}, nil
		}
		return ResolvedCommand{
			TemplateID:       "disk_usage",
			CommandName:      "df",
			Args:             []string{"-h"},
			OutputLineLimit:  120,
			ExpectedEvidence: "Disk usage summary",
		}, nil
	case TemplateDirCountCurrentDirectory:
		if platform.OSFamily == "windows" {
			return ResolvedCommand{
				TemplateID:       "directory_count",
				CommandName:      "cmd",
				Args:             []string{"/c", "dir /b /ad"},
				OutputLineLimit:  500,
				ExpectedEvidence: "Directory listing to count",
			}, nil
		}
		args := []string{"."}
		if strings.TrimSpace(params.Recursion) != "recursive" {
			args = append(args, "-mindepth", "1", "-maxdepth", "1")
		}
		args = append(args, "-type", "d")
		if strings.TrimSpace(params.Visibility) == "" || strings.TrimSpace(params.Visibility) == "visible_only" {
			args = append(args, "-name", "[!.]*")
		}
		return ResolvedCommand{
			TemplateID:       "directory_count",
			CommandName:      "find",
			Args:             args,
			OutputLineLimit:  500,
			ExpectedEvidence: "Directory listing to count",
		}, nil
	case TemplateFileCountCurrentDirectory:
		if platform.OSFamily == "windows" {
			return ResolvedCommand{
				TemplateID:       "file_count",
				CommandName:      "cmd",
				Args:             []string{"/c", "dir /b /a-d"},
				OutputLineLimit:  500,
				ExpectedEvidence: "File listing to count",
			}, nil
		}
		args := []string{"."}
		if strings.TrimSpace(params.Recursion) != "recursive" {
			args = append(args, "-mindepth", "1", "-maxdepth", "1")
		}
		args = append(args, "-type", "f")
		if strings.TrimSpace(params.Visibility) == "" || strings.TrimSpace(params.Visibility) == "visible_only" {
			args = append(args, "-name", "[!.]*")
		}
		return ResolvedCommand{
			TemplateID:       "file_count",
			CommandName:      "find",
			Args:             args,
			OutputLineLimit:  500,
			ExpectedEvidence: "File listing to count",
		}, nil
	case TemplateToolVersion:
		tool := strings.TrimSpace(params.ToolName)
		if tool == "" {
			return ResolvedCommand{}, fmt.Errorf("template %q requires tool_name", key)
		}
		args := []string{"--version"}
		if tool == "go" {
			args = []string{"version"}
		}
		return ResolvedCommand{
			TemplateID:       "tool_version",
			CommandName:      tool,
			Args:             args,
			OutputLineLimit:  40,
			ExpectedEvidence: fmt.Sprintf("%s version output", tool),
		}, nil
	default:
		return ResolvedCommand{}, fmt.Errorf("unsupported template key %q", key)
	}
}
