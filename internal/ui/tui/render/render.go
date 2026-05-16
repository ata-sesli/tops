package render

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/phoenix-tui/phoenix/core"
	"tops/internal/ui/termutil/ansi"
)

// Color token used by the local renderer.
type Color string

// Position is used by join/place helpers.
type Position int

const (
	Left Position = iota
	Center
	Right
)

const (
	Top    Position = Left
	Bottom Position = Right
)

type Border struct {
	Top         string
	Bottom      string
	Left        string
	Right       string
	TopLeft     string
	TopRight    string
	BottomLeft  string
	BottomRight string
}

func RoundedBorder() Border {
	return Border{
		Top:         "─",
		Bottom:      "─",
		Left:        "│",
		Right:       "│",
		TopLeft:     "╭",
		TopRight:    "╮",
		BottomLeft:  "╰",
		BottomRight: "╯",
	}
}

func NormalBorder() Border {
	return Border{
		Top:         "─",
		Bottom:      "─",
		Left:        "│",
		Right:       "│",
		TopLeft:     "┌",
		TopRight:    "┐",
		BottomLeft:  "└",
		BottomRight: "┘",
	}
}

type Style struct {
	fg           Color
	bg           Color
	bold         bool
	width        int
	height       int
	paddingTop   int
	paddingRight int
	paddingBot   int
	paddingLeft  int
	border       Border
	hasBorder    bool
	borderSides  [4]bool // top,right,bottom,left
	borderFG     Color
}

func NewStyle() Style {
	return Style{}
}

func (s Style) Foreground(c Color) Style {
	s.fg = c
	return s
}

func (s Style) Background(c Color) Style {
	s.bg = c
	return s
}

func (s Style) Bold(v bool) Style {
	s.bold = v
	return s
}

func (s Style) Border(b Border, sides ...bool) Style {
	s.hasBorder = true
	s.border = b
	if len(sides) == 4 {
		s.borderSides = [4]bool{sides[0], sides[1], sides[2], sides[3]}
	} else {
		s.borderSides = [4]bool{true, true, true, true}
	}
	return s
}

func (s Style) BorderForeground(c Color) Style {
	s.borderFG = c
	return s
}

func (s Style) Padding(values ...int) Style {
	s.paddingTop, s.paddingRight, s.paddingBot, s.paddingLeft = resolveSpacing(values)
	return s
}

func (s Style) PaddingLeft(value int) Style {
	s.paddingLeft = max(0, value)
	return s
}

func (s Style) PaddingRight(value int) Style {
	s.paddingRight = max(0, value)
	return s
}

func (s Style) PaddingTop(value int) Style {
	s.paddingTop = max(0, value)
	return s
}

func (s Style) PaddingBottom(value int) Style {
	s.paddingBot = max(0, value)
	return s
}

func (s Style) Width(width int) Style {
	s.width = width
	return s
}

func (s Style) Height(height int) Style {
	s.height = height
	return s
}

func (s Style) Render(content string) string {
	lines := splitLines(content)
	if s.width > 0 {
		for i := range lines {
			lines[i] = fitLine(lines[i], s.width)
		}
	}
	lines = applyPadding(lines, s)
	if s.width > 0 {
		for i := range lines {
			lines[i] = fitLine(lines[i], s.width+s.paddingLeft+s.paddingRight)
		}
	}
	if s.height > 0 {
		pad := max(0, s.height-len(lines))
		lineWidth := maxLineWidth(lines)
		for i := 0; i < pad; i++ {
			lines = append(lines, strings.Repeat(" ", lineWidth))
		}
	}
	if s.hasBorder {
		lines = applyBorder(lines, s)
	}
	lines = applyColorStyle(lines, s)
	return strings.Join(lines, "\n")
}

func applyPadding(lines []string, s Style) []string {
	innerWidth := maxLineWidth(lines)
	for i := range lines {
		lines[i] = fitLine(lines[i], innerWidth)
		if s.paddingLeft > 0 {
			lines[i] = strings.Repeat(" ", s.paddingLeft) + lines[i]
		}
		if s.paddingRight > 0 {
			lines[i] += strings.Repeat(" ", s.paddingRight)
		}
	}
	lineWidth := maxLineWidth(lines)
	blank := strings.Repeat(" ", lineWidth)
	if s.paddingTop > 0 {
		top := make([]string, 0, s.paddingTop+len(lines))
		for i := 0; i < s.paddingTop; i++ {
			top = append(top, blank)
		}
		lines = append(top, lines...)
	}
	if s.paddingBot > 0 {
		for i := 0; i < s.paddingBot; i++ {
			lines = append(lines, blank)
		}
	}
	return lines
}

func applyBorder(lines []string, s Style) []string {
	if len(lines) == 0 {
		lines = []string{""}
	}
	innerWidth := maxLineWidth(lines)
	for i := range lines {
		lines[i] = fitLine(lines[i], innerWidth)
	}

	left := ""
	right := ""
	if s.borderSides[3] {
		left = s.border.Left
	}
	if s.borderSides[1] {
		right = s.border.Right
	}
	withSides := make([]string, 0, len(lines)+2)
	for _, line := range lines {
		withSides = append(withSides, left+line+right)
	}

	fullWidth := maxLineWidth(withSides)
	if s.borderSides[0] {
		topFill := strings.Repeat(s.border.Top, max(0, fullWidth-Width(s.border.TopLeft)-Width(s.border.TopRight)))
		withSides = append([]string{s.border.TopLeft + topFill + s.border.TopRight}, withSides...)
	}
	if s.borderSides[2] {
		botFill := strings.Repeat(s.border.Bottom, max(0, fullWidth-Width(s.border.BottomLeft)-Width(s.border.BottomRight)))
		withSides = append(withSides, s.border.BottomLeft+botFill+s.border.BottomRight)
	}
	for i := range withSides {
		withSides[i] = fitLine(withSides[i], fullWidth)
	}
	return withSides
}

func resolveSpacing(values []int) (top, right, bottom, left int) {
	switch len(values) {
	case 0:
		return 0, 0, 0, 0
	case 1:
		return values[0], values[0], values[0], values[0]
	case 2:
		return values[0], values[1], values[0], values[1]
	case 3:
		return values[0], values[1], values[2], values[1]
	default:
		return values[0], values[1], values[2], values[3]
	}
}

func splitLines(content string) []string {
	if content == "" {
		return []string{""}
	}
	return strings.Split(content, "\n")
}

func fitLine(line string, width int) string {
	if width <= 0 {
		return ""
	}
	if Width(line) == width {
		return line
	}
	if Width(line) < width {
		return line + strings.Repeat(" ", width-Width(line))
	}
	var out strings.Builder
	for _, r := range line {
		next := out.String() + string(r)
		if Width(next) > width {
			break
		}
		out.WriteRune(r)
	}
	return out.String()
}

func maxLineWidth(lines []string) int {
	maxWidth := 0
	for _, line := range lines {
		if w := Width(line); w > maxWidth {
			maxWidth = w
		}
	}
	return maxWidth
}

func JoinVertical(_ Position, blocks ...string) string {
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		parts = append(parts, block)
	}
	return strings.Join(parts, "\n")
}

func JoinHorizontal(_ Position, blocks ...string) string {
	if len(blocks) == 0 {
		return ""
	}
	split := make([][]string, len(blocks))
	maxLines := 0
	for i, block := range blocks {
		split[i] = splitLines(block)
		if len(split[i]) > maxLines {
			maxLines = len(split[i])
		}
	}
	widths := make([]int, len(blocks))
	for i := range blocks {
		widths[i] = maxLineWidth(split[i])
	}
	rows := make([]string, 0, maxLines)
	for row := 0; row < maxLines; row++ {
		var b strings.Builder
		for i := range blocks {
			line := ""
			if row < len(split[i]) {
				line = split[i][row]
			}
			b.WriteString(fitLine(line, widths[i]))
		}
		rows = append(rows, b.String())
	}
	return strings.Join(rows, "\n")
}

type placeOptions struct {
	whitespaceChars string
	whitespaceFG    Color
}

type PlaceOption func(*placeOptions)

func WithWhitespaceChars(chars string) PlaceOption {
	return func(opts *placeOptions) {
		opts.whitespaceChars = chars
	}
}

func WithWhitespaceForeground(c Color) PlaceOption {
	return func(opts *placeOptions) {
		opts.whitespaceFG = c
	}
}

func Place(width, height int, hPos Position, vPos Position, content string, options ...PlaceOption) string {
	opts := placeOptions{whitespaceChars: " "}
	for _, opt := range options {
		if opt != nil {
			opt(&opts)
		}
	}
	fillRune := " "
	if opts.whitespaceChars != "" {
		fillRune = string([]rune(opts.whitespaceChars)[0])
	}
	if width <= 0 || height <= 0 {
		return content
	}
	contentLines := splitLines(ansi.Strip(content))
	contentWidth := maxLineWidth(contentLines)
	contentHeight := len(contentLines)

	x := 0
	y := 0
	if hPos == Center {
		x = max(0, (width-contentWidth)/2)
	} else if hPos == Right {
		x = max(0, width-contentWidth)
	}
	if vPos == Center {
		y = max(0, (height-contentHeight)/2)
	} else if vPos == Bottom {
		y = max(0, height-contentHeight)
	}

	canvas := make([][]rune, height)
	for i := 0; i < height; i++ {
		line := strings.Repeat(fillRune, width)
		canvas[i] = []rune(line)
	}
	for row := 0; row < contentHeight; row++ {
		targetY := y + row
		if targetY < 0 || targetY >= height {
			continue
		}
		line := fitLine(contentLines[row], min(width, contentWidth))
		for col, r := range []rune(line) {
			targetX := x + col
			if targetX < 0 || targetX >= width {
				continue
			}
			canvas[targetY][targetX] = r
		}
	}
	out := make([]string, 0, height)
	for _, row := range canvas {
		out = append(out, string(row))
	}
	return strings.Join(out, "\n")
}

// Width returns terminal display width with Unicode-aware grapheme handling.
func Width(text string) int {
	return core.StringWidth(ansi.Strip(text))
}

func applyColorStyle(lines []string, s Style) []string {
	if len(lines) == 0 {
		return lines
	}
	out := make([]string, len(lines))
	copy(out, lines)

	if s.hasBorder && strings.TrimSpace(string(s.borderFG)) != "" {
		for i := range out {
			if (i == 0 && s.borderSides[0]) || (i == len(out)-1 && s.borderSides[2]) {
				out[i] = applyANSI(out[i], s.borderFG, "", false)
				continue
			}
			if i > 0 && i < len(out)-1 {
				line := out[i]
				if s.borderSides[3] {
					left, rest := splitFirstRune(line)
					line = applyANSI(left, s.borderFG, "", false) + rest
				}
				if s.borderSides[1] {
					prefix, right := splitLastRune(line)
					line = prefix + applyANSI(right, s.borderFG, "", false)
				}
				out[i] = line
			}
		}
	}

	if strings.TrimSpace(string(s.fg)) == "" && strings.TrimSpace(string(s.bg)) == "" && !s.bold {
		return out
	}
	for i := range out {
		out[i] = applyANSI(out[i], s.fg, s.bg, s.bold)
	}
	return out
}

func applyANSI(text string, fg Color, bg Color, bold bool) string {
	codes := make([]string, 0, 3)
	if bold {
		codes = append(codes, "1")
	}
	if fgCode := colorCode(fg, false); fgCode != "" {
		codes = append(codes, fgCode)
	}
	if bgCode := colorCode(bg, true); bgCode != "" {
		codes = append(codes, bgCode)
	}
	if len(codes) == 0 || text == "" {
		return text
	}
	return "\x1b[" + strings.Join(codes, ";") + "m" + text + "\x1b[0m"
}

func colorCode(c Color, background bool) string {
	raw := strings.TrimSpace(string(c))
	if raw == "" {
		return ""
	}
	base := "38"
	if background {
		base = "48"
	}
	if strings.HasPrefix(raw, "#") && len(raw) == 7 {
		r, rErr := strconv.ParseInt(raw[1:3], 16, 64)
		g, gErr := strconv.ParseInt(raw[3:5], 16, 64)
		b, bErr := strconv.ParseInt(raw[5:7], 16, 64)
		if rErr == nil && gErr == nil && bErr == nil {
			return fmt.Sprintf("%s;2;%d;%d;%d", base, r, g, b)
		}
	}
	if idx, err := strconv.Atoi(raw); err == nil && idx >= 0 && idx <= 255 {
		return fmt.Sprintf("%s;5;%d", base, idx)
	}
	return ""
}

func splitFirstRune(text string) (string, string) {
	if text == "" {
		return "", ""
	}
	runes := []rune(text)
	if len(runes) == 1 {
		return text, ""
	}
	return string(runes[0]), string(runes[1:])
}

func splitLastRune(text string) (string, string) {
	if text == "" {
		return "", ""
	}
	runes := []rune(text)
	if len(runes) == 1 {
		return "", text
	}
	return string(runes[:len(runes)-1]), string(runes[len(runes)-1])
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
