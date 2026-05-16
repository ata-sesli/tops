package capability

import (
	"path/filepath"
	"strings"
	"testing"

	"tops/internal/model"
	"tops/internal/runtime/workflow"
)

func TestDefaultRegistryRetrievesRelevantCapabilities(t *testing.T) {
	reg := NewCoreRegistry()

	found := reg.Retrieve("how many files are in this folder including hidden ones?", 3)

	if len(found) == 0 {
		t.Fatal("expected at least one retrieved capability")
	}
	if found[0].ID != "filesystem.count" {
		t.Fatalf("expected filesystem.count first, got %q", found[0].ID)
	}
	if len(found) > 3 {
		t.Fatalf("expected retrieval limit to be honored, got %d", len(found))
	}
}

func TestCoreRegistryLoadsCapabilitiesFromJSONManifests(t *testing.T) {
	reg := NewCoreRegistry()

	for _, id := range []string{
		"system.info",
		"filesystem.location",
		"filesystem.count",
		"disk.usage",
		"network.open_ports",
	} {
		cap, ok := reg.Get(id)
		if !ok {
			t.Fatalf("expected capability %q from JSON manifests", id)
		}
		if cap.CompilerID == "" {
			t.Fatalf("expected capability %q to define compiler_id", id)
		}
		if len(cap.Examples) == 0 {
			t.Fatalf("expected capability %q to define examples", id)
		}
	}
}

func TestParseCapabilityActionValidatesActions(t *testing.T) {
	action, err := ParseAction(`{
		"action": "use_capability",
		"capability_id": "filesystem.count",
		"arguments": {"entity": "directory"}
	}`)
	if err != nil {
		t.Fatalf("parse action failed: %v", err)
	}
	if action.Action != ActionUseCapability || action.CapabilityID != "filesystem.count" {
		t.Fatalf("unexpected action: %+v", action)
	}

	for _, raw := range []string{
		`{"action":"final_answer","final_answer":"Done."}`,
		`{"action":"clarify","clarification":"Which path?"}`,
		`{"action":"fail","reason":"unsupported"}`,
	} {
		if _, err := ParseAction(raw); err != nil {
			t.Fatalf("expected action to parse: %s: %v", raw, err)
		}
	}

	if _, err := ParseAction(`{"action":"run_shell","arguments":{}}`); err == nil {
		t.Fatal("expected unknown action to fail")
	}
}

func TestCompileFilesystemCountUsesBroadFindAndPostprocessesVisibility(t *testing.T) {
	reg := NewCoreRegistry()
	if _, err := reg.Compile(CapabilityAction{
		Action:       ActionUseCapability,
		CapabilityID: "filesystem.count",
		Arguments: map[string]any{
			"entity": "directory",
			"scope":  "current_directory",
			"bogus":  "nope",
		},
	}, model.PlatformContext{OSFamily: "macos"}); err == nil {
		t.Fatal("expected unknown argument to fail validation")
	}

	plan, err := reg.Compile(CapabilityAction{
		Action:       ActionUseCapability,
		CapabilityID: "filesystem.count",
		Arguments: map[string]any{
			"entity":     "directory",
			"scope":      "current_directory",
			"visibility": "visible_only",
			"recursion":  "none",
		},
	}, model.PlatformContext{OSFamily: "macos"})
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}
	if plan.Step.CommandName != "find" {
		t.Fatalf("expected find, got %q", plan.Step.CommandName)
	}
	if plan.Step.OutputLineLimit != 0 {
		t.Fatalf("expected untruncated count command output for exact postprocessing, got limit %d", plan.Step.OutputLineLimit)
	}
	gotArgs := strings.Join(plan.Step.Args, " ")
	if strings.Contains(gotArgs, "-name") {
		t.Fatalf("expected no shell-level visibility filter, got args %v", plan.Step.Args)
	}

	result := workflow.StepResult{
		Command:  "find",
		Args:     plan.Step.Args,
		Stdout:   "./visible\n./.hidden\n./nested.name\n",
		ExitCode: 0,
	}
	evidence := plan.Postprocess([]workflow.StepResult{result})

	if !strings.Contains(evidence.Stdout, "count=2") {
		t.Fatalf("expected two visible directories, got evidence %q", evidence.Stdout)
	}
	if strings.Contains(evidence.Stdout, ".hidden") {
		t.Fatalf("expected hidden entry to be filtered, got evidence %q", evidence.Stdout)
	}
}

func TestFilesystemCountIncludeHiddenUsesExactLineMetadata(t *testing.T) {
	reg := NewCoreRegistry()
	plan, err := reg.Compile(CapabilityAction{
		Action:       ActionUseCapability,
		CapabilityID: "filesystem.count",
		Arguments: map[string]any{
			"entity":     "file",
			"visibility": "include_hidden",
		},
	}, model.PlatformContext{OSFamily: "macos"})
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}

	result := workflow.StepResult{
		Command:              "find",
		Args:                 plan.Step.Args,
		Stdout:               "./a\n./b\n... (998 lines omitted)",
		StdoutNonemptyCount:  1000,
		StdoutLineCountExact: true,
		StdoutLineCountTotal: 1000,
		StdoutPreviewCount:   3,
		StdoutTruncated:      false,
		ExitCode:             0,
	}
	evidence := plan.Postprocess([]workflow.StepResult{result})

	if !strings.Contains(evidence.Stdout, `"count":1000`) {
		t.Fatalf("expected exact metadata count, got evidence %q", evidence.Stdout)
	}
	if strings.Contains(evidence.Stdout, "omitted") {
		t.Fatalf("expected truncation marker excluded from sample, got evidence %q", evidence.Stdout)
	}
}

func TestFilesystemCountHomeScopeCompilesResolvedPath(t *testing.T) {
	reg := NewCoreRegistry()
	plan, err := reg.Compile(CapabilityAction{
		Action:       ActionUseCapability,
		CapabilityID: "filesystem.count",
		Arguments: map[string]any{
			"entity": "file",
			"scope":  "home",
		},
	}, model.PlatformContext{OSFamily: "macos"})
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}
	if len(plan.Step.Args) == 0 {
		t.Fatalf("expected find root arg, got %+v", plan.Step.Args)
	}
	if plan.Step.Args[0] == "~" {
		t.Fatalf("expected home scope to be resolved before catalog validation, got %q", plan.Step.Args[0])
	}
	if !filepath.IsAbs(plan.Step.Args[0]) {
		t.Fatalf("expected absolute home path, got %q", plan.Step.Args[0])
	}
}

func TestOpenPortsUnavailableReturnsStructuredEvidence(t *testing.T) {
	reg := NewCoreRegistry(WithCommandAvailable(func(string) bool { return false }))

	plan, err := reg.Compile(CapabilityAction{
		Action:       ActionUseCapability,
		CapabilityID: "network.open_ports",
		Arguments: map[string]any{
			"protocol":        "tcp",
			"state":           "listening",
			"include_process": true,
			"limit":           50,
		},
	}, model.PlatformContext{OSFamily: "linux"})
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}
	if len(plan.Steps()) != 0 {
		t.Fatalf("expected no command steps when tools are unavailable, got %+v", plan.Steps())
	}

	evidence := plan.Postprocess(nil)
	if !strings.Contains(evidence.Stdout, "capability_unavailable") {
		t.Fatalf("expected unavailable evidence, got %q", evidence.Stdout)
	}
	if !evidence.Succeeded {
		t.Fatalf("expected unavailable evidence to be usable for LLM finalization")
	}
}

func TestOpenPortsPostprocessKeepsOnlyListeningRows(t *testing.T) {
	rows := parsePortRows(strings.Join([]string{
		"tcp4       0      0  *.22                   *.*                    LISTEN",
		"tcp4       0      0  127.0.0.1.5432         *.*                    LISTEN",
		"tcp4       0      0  192.168.1.2.61822      93.184.216.34.443      ESTABLISHED",
		"node 123 user 20u IPv4 0x0 0t0 TCP *:3000 (LISTEN)",
	}, "\n"), 10)

	if len(rows) != 3 {
		t.Fatalf("expected only listening rows, got %+v", rows)
	}
	for _, row := range rows {
		if strings.EqualFold(row["state"], "ESTABLISHED") || row["port"] == "443" {
			t.Fatalf("expected established connection filtered out, got %+v", rows)
		}
	}
}

func TestDiskUsageRootVolumePostprocessKeepsOnlyRootMount(t *testing.T) {
	reg := NewCoreRegistry()
	plan, err := reg.Compile(CapabilityAction{
		Action:       ActionUseCapability,
		CapabilityID: "disk.usage",
		Arguments: map[string]any{
			"target": "root_volume",
		},
	}, model.PlatformContext{OSFamily: "macos"})
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}
	if plan.Step.CommandName != "df" {
		t.Fatalf("expected df, got %q", plan.Step.CommandName)
	}

	result := workflow.StepResult{
		Command: "df",
		Args:    plan.Step.Args,
		Stdout: strings.Join([]string{
			"Filesystem      Size    Used   Avail Capacity Mounted on",
			"/dev/disk3s1s1  460Gi   12Gi   100Gi    11%   /",
			"devfs           215Ki  215Ki     0Bi   100%   /dev",
			"/dev/disk3s5    460Gi  230Gi   100Gi    70%   /System/Volumes/Data",
		}, "\n"),
		ExitCode: 0,
	}
	evidence := plan.Postprocess([]workflow.StepResult{result})

	if !strings.Contains(evidence.Stdout, `"mounted_on":"/"`) {
		t.Fatalf("expected root mount row in evidence, got %q", evidence.Stdout)
	}
	if strings.Contains(evidence.Stdout, `"/System/Volumes/Data"`) || strings.Contains(evidence.Stdout, `"mounted_on":"/dev"`) {
		t.Fatalf("expected only root mount row, got %q", evidence.Stdout)
	}
}
