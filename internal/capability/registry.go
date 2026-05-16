package capability

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"tops/internal/model"
)

type Registry struct {
	caps             map[string]Capability
	order            []string
	commandAvailable func(string) bool
}

type Option func(*Registry)

func WithCommandAvailable(fn func(string) bool) Option {
	return func(r *Registry) {
		if fn != nil {
			r.commandAvailable = fn
		}
	}
}

func NewCoreRegistry(opts ...Option) Registry {
	r := Registry{
		caps:             map[string]Capability{},
		commandAvailable: defaultCommandAvailable,
	}
	for _, opt := range opts {
		opt(&r)
	}
	for _, cap := range coreCapabilities() {
		_ = r.add(cap)
	}
	sort.Strings(r.order)
	return r
}

func defaultCommandAvailable(name string) bool {
	_, err := exec.LookPath(strings.TrimSpace(name))
	return err == nil
}

func (r *Registry) add(cap Capability) error {
	id := strings.TrimSpace(cap.ID)
	if id == "" {
		return fmt.Errorf("capability id is required")
	}
	if _, exists := r.caps[id]; exists {
		return fmt.Errorf("duplicate capability id %q", id)
	}
	r.caps[id] = cap
	r.order = append(r.order, id)
	return nil
}

func (r Registry) Get(id string) (Capability, bool) {
	cap, ok := r.caps[strings.TrimSpace(id)]
	return cap, ok
}

func (r Registry) List() []Capability {
	out := make([]Capability, 0, len(r.order))
	for _, id := range r.order {
		if cap, ok := r.caps[id]; ok {
			out = append(out, cap)
		}
	}
	return out
}

func (r Registry) Retrieve(query string, limit int) []Capability {
	if limit <= 0 {
		limit = 8
	}
	terms := tokenize(query)
	type scored struct {
		cap   Capability
		score int
	}
	scoredCaps := make([]scored, 0, len(r.caps))
	for _, cap := range r.List() {
		text := strings.ToLower(cap.ID + " " + cap.Description + " " + strings.Join(cap.Examples, " "))
		score := 0
		for _, term := range terms {
			if strings.Contains(text, term) {
				score += 3
			}
		}
		for name := range cap.Arguments {
			for _, term := range terms {
				if strings.Contains(strings.ToLower(name), term) {
					score++
				}
			}
		}
		if score > 0 {
			scoredCaps = append(scoredCaps, scored{cap: cap, score: score})
		}
	}
	sort.SliceStable(scoredCaps, func(i, j int) bool {
		if scoredCaps[i].score == scoredCaps[j].score {
			return scoredCaps[i].cap.ID < scoredCaps[j].cap.ID
		}
		return scoredCaps[i].score > scoredCaps[j].score
	})
	if len(scoredCaps) > limit {
		scoredCaps = scoredCaps[:limit]
	}
	out := make([]Capability, 0, len(scoredCaps))
	for _, item := range scoredCaps {
		out = append(out, item.cap)
	}
	return out
}

func (r Registry) Compile(action CapabilityAction, platform model.PlatformContext) (CommandPlan, error) {
	if action.Action != ActionUseCapability {
		return CommandPlan{}, fmt.Errorf("cannot compile action %q", action.Action)
	}
	cap, ok := r.Get(action.CapabilityID)
	if !ok {
		return CommandPlan{}, fmt.Errorf("unknown capability %q", action.CapabilityID)
	}
	args, err := validateArgs(cap, action.Arguments)
	if err != nil {
		return CommandPlan{}, err
	}
	if cap.Compile == nil {
		return CommandPlan{}, fmt.Errorf("capability %q has no compiler", cap.ID)
	}
	return cap.Compile(CompileContext{
		Platform:         model.NormalizePlatformContext(platform),
		CommandAvailable: r.commandAvailable,
	}, args)
}

func validateArgs(cap Capability, raw map[string]any) (map[string]any, error) {
	if raw == nil {
		raw = map[string]any{}
	}
	out := map[string]any{}
	for key := range raw {
		if _, ok := cap.Arguments[key]; !ok {
			return nil, fmt.Errorf("capability %q unknown argument %q", cap.ID, key)
		}
	}
	for key, spec := range cap.Arguments {
		val, ok := raw[key]
		if !ok {
			if spec.Default != nil {
				out[key] = spec.Default
				continue
			}
			if spec.Required {
				return nil, fmt.Errorf("capability %q missing required argument %q", cap.ID, key)
			}
			continue
		}
		strVal, isString := val.(string)
		if len(spec.Enum) > 0 {
			if !isString {
				return nil, fmt.Errorf("capability %q argument %q must be a string enum", cap.ID, key)
			}
			if !contains(spec.Enum, strVal) {
				return nil, fmt.Errorf("capability %q argument %q invalid value %q", cap.ID, key, strVal)
			}
		}
		out[key] = val
	}
	return out, nil
}

func tokenize(input string) []string {
	replacer := strings.NewReplacer("?", " ", ".", " ", ",", " ", "_", " ", "-", " ", "/", " ")
	input = replacer.Replace(strings.ToLower(strings.TrimSpace(input)))
	seen := map[string]struct{}{}
	out := []string{}
	for _, term := range strings.Fields(input) {
		if len(term) < 2 {
			continue
		}
		if _, ok := seen[term]; ok {
			continue
		}
		seen[term] = struct{}{}
		out = append(out, term)
	}
	return out
}

func contains(items []string, target string) bool {
	target = strings.TrimSpace(target)
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}
