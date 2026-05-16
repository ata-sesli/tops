package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/phoenix-tui/phoenix/tea"
	"tops/internal/ui/tui/render"

	"tops/internal/app"
	"tops/internal/storage/modelprofile"
)

type configMenuItemKind string

const (
	configMenuKindEditText configMenuItemKind = "edit_text"
	configMenuKindEditInt  configMenuItemKind = "edit_int"
	configMenuKindToggle   configMenuItemKind = "toggle"
	configMenuKindCycle    configMenuItemKind = "cycle"
)

type configMenuItem struct {
	Section  string
	Key      string
	Label    string
	Value    string
	RawValue string
	Kind     configMenuItemKind
	Options  []string
}

type configEditState struct {
	ItemKey string
	Label   string
	Value   string
}

type configMenuState struct {
	Items    []configMenuItem
	Selected int
	Edit     *configEditState
	LoadErr  string
}

func (m *sessionModel) refreshConfigMenu() {
	items := make([]configMenuItem, 0, 16)
	m.configMenu.LoadErr = ""
	if m.runtime == nil {
		m.configMenu.Items = items
		m.configMenu.Selected = 0
		return
	}
	rt := *m.runtime
	items = append(items, configMenuItem{
		Section:  "Model",
		Key:      "model.active",
		Label:    "Active Model",
		Value:    rt.Config.Provider.Model,
		RawValue: rt.Config.Provider.Model,
		Kind:     configMenuKindEditText,
	})

	profile := modelprofile.ModelProfile{
		Provider: rt.Config.Provider.Type,
		Model:    rt.Config.Provider.Model,
	}
	if isLocalProvider(rt.Config.Provider.Type) {
		profiles, err := modelprofile.Load("")
		if err != nil {
			m.configMenu.LoadErr = fmt.Sprintf("Model profile load error: %s", err)
		} else if loaded, ok := profiles.Get(rt.Config.Provider.Type, rt.Config.Provider.Model); ok {
			profile = loaded
		}
		contextValue := "unset"
		contextRaw := ""
		if profile.Context > 0 {
			contextValue = strconv.Itoa(profile.Context)
			contextRaw = contextValue
		}
		maxLengthValue := "unset"
		maxLengthRaw := ""
		if profile.MaxLength > 0 {
			maxLengthValue = strconv.Itoa(profile.MaxLength)
			maxLengthRaw = maxLengthValue
		}
		systemPromptValue := "unset"
		systemPromptRaw := strings.TrimSpace(profile.SystemPrompt)
		if systemPromptRaw != "" {
			systemPromptValue = systemPromptRaw
		}
		intelligenceRaw := strings.TrimSpace(profile.IntelligenceMode)
		if intelligenceRaw == "" {
			intelligenceRaw = "auto"
		}
		intelligenceValue := formatIntelligenceModeLabel(intelligenceRaw)
		thinkRaw := strings.TrimSpace(profile.Think)
		thinkValue := "unset"
		if thinkRaw != "" {
			thinkValue = strings.ToLower(thinkRaw)
		}
		temperatureRaw := ""
		temperatureValue := "unset"
		if profile.Temperature > 0 {
			temperatureRaw = formatFloat(profile.Temperature)
			temperatureValue = temperatureRaw
		}
		topKRaw := ""
		topKValue := "unset"
		if profile.TopK > 0 {
			topKRaw = strconv.Itoa(profile.TopK)
			topKValue = topKRaw
		}
		topPRaw := ""
		topPValue := "unset"
		if profile.TopP > 0 {
			topPRaw = formatFloat(profile.TopP)
			topPValue = topPRaw
		}
		minPRaw := ""
		minPValue := "unset"
		if profile.MinP > 0 {
			minPRaw = formatFloat(profile.MinP)
			minPValue = minPRaw
		}
		repeatPenaltyRaw := ""
		repeatPenaltyValue := "unset"
		if profile.RepeatPenalty > 0 {
			repeatPenaltyRaw = formatFloat(profile.RepeatPenalty)
			repeatPenaltyValue = repeatPenaltyRaw
		}
		thinkBudgetRaw := ""
		thinkBudgetValue := "unset"
		if profile.ThinkBudgetTokens > 0 {
			thinkBudgetRaw = strconv.Itoa(profile.ThinkBudgetTokens)
			thinkBudgetValue = thinkBudgetRaw
		}
		items = append(items,
			configMenuItem{Section: "Model Profile", Key: "profile.context", Label: "Context", Value: contextValue, RawValue: contextRaw, Kind: configMenuKindEditInt},
			configMenuItem{Section: "Model Profile", Key: "profile.max_length", Label: "Max Length", Value: maxLengthValue, RawValue: maxLengthRaw, Kind: configMenuKindEditInt},
			configMenuItem{Section: "Model Profile", Key: "profile.system_prompt", Label: "System Prompt", Value: systemPromptValue, RawValue: systemPromptRaw, Kind: configMenuKindEditText},
			configMenuItem{
				Section:  "Model Profile",
				Key:      "profile.intelligence_mode",
				Label:    "Intelligence Mode",
				Value:    intelligenceValue,
				RawValue: intelligenceRaw,
				Kind:     configMenuKindCycle,
				Options:  []string{"blitz", "grounded", "auto"},
			},
			configMenuItem{
				Section:  "Model Profile",
				Key:      "profile.think",
				Label:    "Think",
				Value:    thinkValue,
				RawValue: thinkRaw,
				Kind:     configMenuKindCycle,
				Options:  []string{"off", "on", "low", "medium", "high"},
			},
			configMenuItem{Section: "Model Profile", Key: "profile.temperature", Label: "Temperature", Value: temperatureValue, RawValue: temperatureRaw, Kind: configMenuKindEditText},
			configMenuItem{Section: "Model Profile", Key: "profile.top_k", Label: "Top K", Value: topKValue, RawValue: topKRaw, Kind: configMenuKindEditInt},
			configMenuItem{Section: "Model Profile", Key: "profile.top_p", Label: "Top P", Value: topPValue, RawValue: topPRaw, Kind: configMenuKindEditText},
			configMenuItem{Section: "Model Profile", Key: "profile.min_p", Label: "Min P", Value: minPValue, RawValue: minPRaw, Kind: configMenuKindEditText},
			configMenuItem{Section: "Model Profile", Key: "profile.repeat_penalty", Label: "Repeat Penalty", Value: repeatPenaltyValue, RawValue: repeatPenaltyRaw, Kind: configMenuKindEditText},
			configMenuItem{Section: "Model Profile", Key: "profile.think_budget_tokens", Label: "Think Budget Tokens", Value: thinkBudgetValue, RawValue: thinkBudgetRaw, Kind: configMenuKindEditInt},
		)

		askProfile := profile.EffectiveAskResponseProfile()
		items = append(items,
			configMenuItem{Section: "Ask Response", Key: "ask.observations", Label: "Observations", Value: onOff(askProfile.Observations), RawValue: onOff(askProfile.Observations), Kind: configMenuKindToggle, Options: []string{"off", "on"}},
			configMenuItem{Section: "Ask Response", Key: "ask.inferences", Label: "Inferences", Value: onOff(askProfile.Inferences), RawValue: onOff(askProfile.Inferences), Kind: configMenuKindToggle, Options: []string{"off", "on"}},
			configMenuItem{Section: "Ask Response", Key: "ask.uncertainties", Label: "Uncertainties", Value: onOff(askProfile.Uncertainties), RawValue: onOff(askProfile.Uncertainties), Kind: configMenuKindToggle, Options: []string{"off", "on"}},
			configMenuItem{Section: "Ask Response", Key: "ask.assumptions", Label: "Assumptions", Value: onOff(askProfile.Assumptions), RawValue: onOff(askProfile.Assumptions), Kind: configMenuKindToggle, Options: []string{"off", "on"}},
			configMenuItem{Section: "Ask Response", Key: "ask.notes", Label: "Notes", Value: onOff(askProfile.Notes), RawValue: onOff(askProfile.Notes), Kind: configMenuKindToggle, Options: []string{"off", "on"}},
		)
	}

	items = append(items,
		configMenuItem{
			Section:  "Execution",
			Key:      "execution.read_only",
			Label:    "Read-Only Policy",
			Value:    string(rt.Config.Execution.Permissions.ReadOnly),
			RawValue: string(rt.Config.Execution.Permissions.ReadOnly),
			Kind:     configMenuKindCycle,
			Options:  []string{"allow", "request", "disallow"},
		},
		configMenuItem{
			Section:  "Execution",
			Key:      "execution.write",
			Label:    "Write Policy",
			Value:    string(rt.Config.Execution.Permissions.Write),
			RawValue: string(rt.Config.Execution.Permissions.Write),
			Kind:     configMenuKindCycle,
			Options:  []string{"allow", "request", "disallow"},
		},
		configMenuItem{
			Section:  "Execution",
			Key:      "execution.trace_mode",
			Label:    "Trace Mode",
			Value:    string(rt.Config.Execution.TraceMode),
			RawValue: string(rt.Config.Execution.TraceMode),
			Kind:     configMenuKindCycle,
			Options:  []string{"release", "debug"},
		},
	)

	m.configMenu.Items = items
	if len(items) == 0 {
		m.configMenu.Selected = 0
		return
	}
	if m.configMenu.Selected < 0 {
		m.configMenu.Selected = 0
	}
	if m.configMenu.Selected >= len(items) {
		m.configMenu.Selected = len(items) - 1
	}
}

func (m *sessionModel) moveConfigMenu(delta int) {
	if len(m.configMenu.Items) == 0 {
		return
	}
	m.configMenu.Selected += delta
	if m.configMenu.Selected < 0 {
		m.configMenu.Selected = 0
	}
	if m.configMenu.Selected >= len(m.configMenu.Items) {
		m.configMenu.Selected = len(m.configMenu.Items) - 1
	}
}

func (m *sessionModel) currentConfigMenuItem() (configMenuItem, bool) {
	if len(m.configMenu.Items) == 0 || m.configMenu.Selected < 0 || m.configMenu.Selected >= len(m.configMenu.Items) {
		return configMenuItem{}, false
	}
	return m.configMenu.Items[m.configMenu.Selected], true
}

func (m *sessionModel) beginConfigMenuEdit(item configMenuItem) {
	m.configMenu.Edit = &configEditState{
		ItemKey: item.Key,
		Label:   item.Label,
		Value:   item.RawValue,
	}
	m.syncInputForActiveSurface()
}

func (m *sessionModel) cancelConfigMenuEdit() {
	m.configMenu.Edit = nil
	m.syncInputForActiveSurface()
}

func (m *sessionModel) applyConfigMenuCurrent(cycleOnly bool) (*sessionModel, tea.Cmd) {
	item, ok := m.currentConfigMenuItem()
	if !ok || m.runtime == nil {
		return m, nil
	}
	switch item.Kind {
	case configMenuKindEditInt, configMenuKindEditText:
		if cycleOnly {
			return m, nil
		}
		m.beginConfigMenuEdit(item)
		return m, nil
	case configMenuKindToggle, configMenuKindCycle:
		value := nextCycleOption(item.Options, item.RawValue)
		return m.applyConfigMenuValue(item, value)
	default:
		return m, nil
	}
}

func nextCycleOption(options []string, current string) string {
	if len(options) == 0 {
		return current
	}
	current = strings.ToLower(strings.TrimSpace(current))
	for i, option := range options {
		if strings.EqualFold(option, current) {
			return options[(i+1)%len(options)]
		}
	}
	return options[0]
}

func formatIntelligenceModeLabel(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "blitz":
		return "Blitz"
	case "grounded":
		return "Grounded"
	default:
		return "Auto"
	}
}

func (m *sessionModel) applyConfigMenuEdit() (*sessionModel, tea.Cmd) {
	if m.configMenu.Edit == nil {
		return m, nil
	}
	item, ok := m.currentConfigMenuItem()
	if !ok {
		m.cancelConfigMenuEdit()
		return m, nil
	}
	value := strings.TrimSpace(m.input.Value())
	m.cancelConfigMenuEdit()
	return m.applyConfigMenuValue(item, value)
}

func (m *sessionModel) applyConfigMenuValue(item configMenuItem, value string) (*sessionModel, tea.Cmd) {
	if m.runtime == nil {
		return m, nil
	}
	rt := *m.runtime
	appendError := func(err error) (*sessionModel, tea.Cmd) {
		if err != nil {
			m.appendOutputBlock("Config menu error: " + err.Error())
		}
		m.refreshConfigMenu()
		return m, nil
	}
	appendSuccess := func(output string, updated *app.Runtime) (*sessionModel, tea.Cmd) {
		if strings.TrimSpace(output) != "" {
			m.appendOutputBlock(output)
		}
		if updated != nil {
			m.runtime = updated
		}
		m.refreshConfigMenu()
		m.syncInputForActiveSurface()
		return m, nil
	}

	switch item.Key {
	case "model.active":
		parsed := ParseResult{Kind: KindModelUse, Payload: strings.TrimSpace(value)}
		m.startPending("provider")
		return m, tea.Batch(
			pendingTickCmd(),
			switchModelCmd(m.ctx, rt, m.session.configPath, m.session.runtimeLoader, parsed),
		)
	case "profile.context":
		output, _, updated, err := setModelConfig(rt, m.session.configPath, m.session.runtimeLoader, "context", value)
		if err != nil {
			return appendError(err)
		}
		return appendSuccess(output, updated)
	case "profile.max_length":
		output, _, updated, err := setModelConfig(rt, m.session.configPath, m.session.runtimeLoader, "max_length", value)
		if err != nil {
			return appendError(err)
		}
		return appendSuccess(output, updated)
	case "profile.system_prompt":
		output, _, updated, err := setModelConfig(rt, m.session.configPath, m.session.runtimeLoader, "system_prompt", value)
		if err != nil {
			return appendError(err)
		}
		return appendSuccess(output, updated)
	case "profile.intelligence_mode":
		output, _, updated, err := setModelConfig(rt, m.session.configPath, m.session.runtimeLoader, "intelligence_mode", value)
		if err != nil {
			return appendError(err)
		}
		return appendSuccess(output, updated)
	case "profile.think":
		output, _, updated, err := setModelConfig(rt, m.session.configPath, m.session.runtimeLoader, "think", value)
		if err != nil {
			return appendError(err)
		}
		return appendSuccess(output, updated)
	case "profile.temperature":
		output, _, updated, err := setModelConfig(rt, m.session.configPath, m.session.runtimeLoader, "temperature", value)
		if err != nil {
			return appendError(err)
		}
		return appendSuccess(output, updated)
	case "profile.top_k":
		output, _, updated, err := setModelConfig(rt, m.session.configPath, m.session.runtimeLoader, "top_k", value)
		if err != nil {
			return appendError(err)
		}
		return appendSuccess(output, updated)
	case "profile.top_p":
		output, _, updated, err := setModelConfig(rt, m.session.configPath, m.session.runtimeLoader, "top_p", value)
		if err != nil {
			return appendError(err)
		}
		return appendSuccess(output, updated)
	case "profile.min_p":
		output, _, updated, err := setModelConfig(rt, m.session.configPath, m.session.runtimeLoader, "min_p", value)
		if err != nil {
			return appendError(err)
		}
		return appendSuccess(output, updated)
	case "profile.repeat_penalty":
		output, _, updated, err := setModelConfig(rt, m.session.configPath, m.session.runtimeLoader, "repeat_penalty", value)
		if err != nil {
			return appendError(err)
		}
		return appendSuccess(output, updated)
	case "profile.think_budget_tokens":
		output, _, updated, err := setModelConfig(rt, m.session.configPath, m.session.runtimeLoader, "think_budget_tokens", value)
		if err != nil {
			return appendError(err)
		}
		return appendSuccess(output, updated)
	case "ask.observations":
		output, _, updated, err := setModelResponseProfile(rt, m.session.configPath, m.session.runtimeLoader, "observations", value)
		if err != nil {
			return appendError(err)
		}
		return appendSuccess(output, updated)
	case "ask.inferences":
		output, _, updated, err := setModelResponseProfile(rt, m.session.configPath, m.session.runtimeLoader, "inferences", value)
		if err != nil {
			return appendError(err)
		}
		return appendSuccess(output, updated)
	case "ask.uncertainties":
		output, _, updated, err := setModelResponseProfile(rt, m.session.configPath, m.session.runtimeLoader, "uncertainties", value)
		if err != nil {
			return appendError(err)
		}
		return appendSuccess(output, updated)
	case "ask.assumptions":
		output, _, updated, err := setModelResponseProfile(rt, m.session.configPath, m.session.runtimeLoader, "assumptions", value)
		if err != nil {
			return appendError(err)
		}
		return appendSuccess(output, updated)
	case "ask.notes":
		output, _, updated, err := setModelResponseProfile(rt, m.session.configPath, m.session.runtimeLoader, "notes", value)
		if err != nil {
			return appendError(err)
		}
		return appendSuccess(output, updated)
	case "execution.read_only":
		output, _, updated, err := setExecutionPolicy(rt, m.session.configPath, m.session.runtimeLoader, "read-only", value)
		if err != nil {
			return appendError(err)
		}
		return appendSuccess(output, updated)
	case "execution.write":
		output, _, updated, err := setExecutionPolicy(rt, m.session.configPath, m.session.runtimeLoader, "write", value)
		if err != nil {
			return appendError(err)
		}
		return appendSuccess(output, updated)
	case "execution.trace_mode":
		output, _, updated, err := setExecutionTrace(rt, m.session.configPath, m.session.runtimeLoader, value)
		if err != nil {
			return appendError(err)
		}
		return appendSuccess(output, updated)
	default:
		return appendError(fmt.Errorf("unsupported menu item %q", item.Key))
	}
}

func (m sessionModel) renderConfigMenuColumns(width int) string {
	if width <= 0 {
		return ""
	}
	if m.runtime == nil {
		return wrapTextBlock("Runtime is not loaded. Run /setup to initialize configuration.", width)
	}
	if len(m.configMenu.Items) == 0 {
		return wrapTextBlock("No configurable items available.", width)
	}

	sectionMap := map[string][]int{
		"Model Profile": {},
		"Ask Response":  {},
		"Execution":     {},
	}
	for i, item := range m.configMenu.Items {
		section := item.Section
		if section == "Model" {
			section = "Model Profile"
		}
		if _, ok := sectionMap[section]; ok {
			sectionMap[section] = append(sectionMap[section], i)
		}
	}

	columnWidth := max(24, (width-2)/3)
	renderSection := func(title string, indices []int) string {
		var lines []string
		lines = append(lines, render.NewStyle().Bold(true).Foreground(render.Color("252")).Render(title))
		lines = append(lines, render.NewStyle().Foreground(render.Color("240")).Render(strings.Repeat("─", max(8, columnWidth-2))))
		if len(indices) == 0 {
			lines = append(lines, render.NewStyle().Foreground(render.Color("241")).Render("  (n/a)"))
		}
		for _, idx := range indices {
			item := m.configMenu.Items[idx]
			prefix := "  "
			style := render.NewStyle().Foreground(render.Color("245"))
			if idx == m.configMenu.Selected {
				prefix = "▶ "
				style = style.Bold(true).Foreground(render.Color("230")).Background(render.Color("63"))
			}
			line := fmt.Sprintf("%s%s: %s", prefix, item.Label, item.Value)
			if m.configMenu.Edit != nil && m.configMenu.Edit.ItemKey == item.Key {
				line += " *"
			}
			wrapped := wrapTextBlock(line, max(12, columnWidth-1))
			for _, wline := range strings.Split(wrapped, "\n") {
				lines = append(lines, style.Render(wline))
			}
		}
		block := strings.Join(lines, "\n")
		return render.NewStyle().Width(columnWidth).Render(block)
	}

	row := render.JoinHorizontal(
		render.Top,
		renderSection("Model Profile", sectionMap["Model Profile"]),
		renderSection("Ask Response", sectionMap["Ask Response"]),
		renderSection("Execution", sectionMap["Execution"]),
	)
	if strings.TrimSpace(m.configMenu.LoadErr) != "" {
		row += "\n" + render.NewStyle().Foreground(render.Color("203")).Render(m.configMenu.LoadErr)
	}
	return row
}
