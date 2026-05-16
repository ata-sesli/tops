package capability

import (
	"embed"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed core/*.json
var coreManifestFS embed.FS

func coreCapabilities() []Capability {
	caps, err := loadCoreCapabilities()
	if err != nil {
		panic(err)
	}
	return caps
}

func loadCoreCapabilities() ([]Capability, error) {
	entries, err := coreManifestFS.ReadDir("core")
	if err != nil {
		return nil, fmt.Errorf("read capability manifests: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	compilers := coreCompilers()
	var out []Capability
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		raw, err := coreManifestFS.ReadFile("core/" + entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read capability manifest %s: %w", entry.Name(), err)
		}
		var caps []Capability
		if err := json.Unmarshal(raw, &caps); err != nil {
			return nil, fmt.Errorf("parse capability manifest %s: %w", entry.Name(), err)
		}
		for i := range caps {
			caps[i].ID = strings.TrimSpace(caps[i].ID)
			caps[i].CompilerID = strings.TrimSpace(caps[i].CompilerID)
			compiler, ok := compilers[caps[i].CompilerID]
			if !ok {
				return nil, fmt.Errorf("capability %q references unknown compiler_id %q", caps[i].ID, caps[i].CompilerID)
			}
			caps[i].Compile = compiler
			out = append(out, caps[i])
		}
	}
	return out, nil
}

func coreCompilers() map[string]func(CompileContext, map[string]any) (CommandPlan, error) {
	return map[string]func(CompileContext, map[string]any) (CommandPlan, error){
		"system_info":         compileSystemInfo,
		"filesystem_location": compileFilesystemLocation,
		"filesystem_count":    compileFilesystemCount,
		"disk_usage":          compileDiskUsage,
		"network_open_ports":  compileOpenPorts,
	}
}
