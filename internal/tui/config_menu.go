package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"tops/internal/app"
	"tops/internal/modelprofile"
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
	if isOllamaProvider(rt.Config.Provider.Type) {
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
			systemPromptValue = truncateMenuValue(systemPromptRaw, 36)
		}
		thinkValue := "unset"
		thinkRaw := strings.TrimSpace(profile.Think)
		if thinkRaw != "" {
			thinkValue = thinkRaw
		}
		items = append(items,
			configMenuItem{Section: "Model Profile", Key: "profile.context", Label: "Context", Value: contextValue, RawValue: contextRaw, Kind: configMenuKindEditInt},
			configMenuItem{Section: "Model Profile", Key: "profile.max_length", Label: "Max Length", Value: maxLengthValue, RawValue: maxLengthRaw, Kind: configMenuKindEditInt},
			configMenuItem{Section: "Model Profile", Key: "profile.system_prompt", Label: "System Prompt", Value: systemPromptValue, RawValue: systemPromptRaw, Kind: configMenuKindEditText},
			configMenuItem{
				Section:  "Model Profile",
				Key:      "profile.think",
				Label:    "Think",
				Value:    thinkValue,
				RawValue: thinkRaw,
				Kind:     configMenuKindCycle,
				Options:  []string{"off", "low", "medium", "high", "on"},
			},
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
	m.rebuildConfigViewportContent()
}

func truncateMenuValue(value string, width int) string {
	value = strings.TrimSpace(value)
	if width <= 3 || lipgloss.Width(value) <= width {
		return value
	}
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	return string(runes[:max(1, width-1)]) + "…"
}

func (m *sessionModel) renderConfigMenuText() (string, int) {
	maxLineWidth := max(20, m.configViewport.Width-1)
	if m.runtime == nil {
		return wrapTextBlock("Config Menu\n\nRuntime is not loaded.\nRun /setup to initialize configuration.", maxLineWidth), -1
	}
	if len(m.configMenu.Items) == 0 {
		return wrapTextBlock("Config Menu\n\nNo configurable items available.", maxLineWidth), -1
	}
	var b strings.Builder
	selectedLine := -1
	lineNo := 0
	b.WriteString("Config Menu\n")
	lineNo++
	currentSection := ""
	for i, item := range m.configMenu.Items {
		if item.Section != currentSection {
			if currentSection != "" {
				b.WriteString("\n")
				lineNo++
			}
			currentSection = item.Section
			b.WriteString(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("252")).Render(currentSection))
			b.WriteString("\n")
			lineNo++
		}
		prefix := "  "
		style := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
		if i == m.configMenu.Selected {
			prefix = "▶ "
			style = style.Bold(true).Foreground(lipgloss.Color("230")).Background(lipgloss.Color("63"))
			selectedLine = lineNo
		}
		line := fmt.Sprintf("%s%-18s %s", prefix, item.Label, item.Value)
		if m.configMenu.Edit != nil && m.configMenu.Edit.ItemKey == item.Key {
			line += "  (editing)"
		}
		line = truncateMenuValue(line, maxLineWidth)
		b.WriteString(style.Render(line))
		b.WriteString("\n")
		lineNo++
	}
	if strings.TrimSpace(m.configMenu.LoadErr) != "" {
		b.WriteString("\n")
		lineNo++
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Render(m.configMenu.LoadErr))
		b.WriteString("\n")
		lineNo++
	}
	return strings.TrimRight(b.String(), "\n"), selectedLine
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
	m.rebuildConfigViewportContent()
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

func (m *sessionModel) applyConfigMenuCurrent(cycleOnly bool) (tea.Model, tea.Cmd) {
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

func (m *sessionModel) applyConfigMenuEdit() (tea.Model, tea.Cmd) {
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

func (m *sessionModel) applyConfigMenuValue(item configMenuItem, value string) (tea.Model, tea.Cmd) {
	if m.runtime == nil {
		return m, nil
	}
	rt := *m.runtime
	appendError := func(err error) (tea.Model, tea.Cmd) {
		if err != nil {
			m.appendOutputBlock("Config menu error: " + err.Error())
		}
		m.refreshConfigMenu()
		return m, nil
	}
	appendSuccess := func(output string, updated *app.Runtime) (tea.Model, tea.Cmd) {
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
			switchModelCmd(m.ctx, m.ollama, rt, m.session.configPath, m.session.runtimeLoader, parsed),
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
	case "profile.think":
		output, _, updated, err := setModelConfig(rt, m.session.configPath, m.session.runtimeLoader, "think", value)
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

func (m *sessionModel) rebuildConfigViewportContent() {
	if m.configViewport.Width <= 0 || m.configViewport.Height <= 0 {
		return
	}
	menu, selectedLine := m.renderConfigMenuText()
	sections := []string{
		menu,
		"",
		lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("252")).Render("Manager Output"),
	}
	output := strings.TrimSpace(m.outputContent)
	if output == "" {
		sections = append(sections, lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render("No output yet."))
	} else {
		sections = append(sections, wrapTextBlock(output, m.configViewport.Width))
	}
	m.configViewport.SetContent(strings.Join(sections, "\n"))
	m.ensureConfigSelectionVisible(selectedLine)
}

func (m *sessionModel) ensureConfigSelectionVisible(selectedLine int) {
	if selectedLine < 0 || m.configViewport.Height <= 0 {
		return
	}
	top := m.configViewport.YOffset
	bottom := top + m.configViewport.Height - 1
	padding := 2
	if selectedLine < top+padding {
		m.configViewport.YOffset = max(0, selectedLine-padding)
		return
	}
	if selectedLine > bottom-padding {
		m.configViewport.YOffset = max(0, selectedLine-(m.configViewport.Height-padding-1))
	}
}
