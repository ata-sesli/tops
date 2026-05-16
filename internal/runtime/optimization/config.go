package optimization

type TokenBudgets struct {
	HelpMaxTokens         int
	GenPlanningMaxTokens  int
	GenSynthesisMaxTokens int
	AskPlanningMaxTokens  int
	AskSynthesisBase      int
	AskSynthesisPerField  int
	AskSynthesisCap       int
}

type Config struct {
	StrictFast          bool
	RepairMaxRetries    int
	AskEvidenceMaxChars int
	TokenBudgets        TokenBudgets
}

func Default() Config {
	return Config{
		StrictFast:          true,
		RepairMaxRetries:    1,
		AskEvidenceMaxChars: 2400,
		TokenBudgets: TokenBudgets{
			HelpMaxTokens:         520,
			GenPlanningMaxTokens:  360,
			GenSynthesisMaxTokens: 480,
			AskPlanningMaxTokens:  180,
			AskSynthesisBase:      90,
			AskSynthesisPerField:  45,
			AskSynthesisCap:       320,
		},
	}
}
