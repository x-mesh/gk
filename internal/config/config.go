package config

// Config holds the full resolved configuration for gk.
type Config struct {
	BaseBranch string       `mapstructure:"base_branch"  yaml:"base_branch"`
	Remote     string       `mapstructure:"remote"       yaml:"remote"`
	Log        LogConfig    `mapstructure:"log"          yaml:"log"`
	UI         UIConfig     `mapstructure:"ui"           yaml:"ui"`
	Branch     BranchConfig `mapstructure:"branch"       yaml:"branch"`
}

// LogConfig controls git log output format.
type LogConfig struct {
	Format string `mapstructure:"format" yaml:"format"`
	Graph  bool   `mapstructure:"graph"  yaml:"graph"`
	Limit  int    `mapstructure:"limit"  yaml:"limit"`
}

// UIConfig controls terminal UI behaviour.
type UIConfig struct {
	Color  string `mapstructure:"color"  yaml:"color"`
	Prefer string `mapstructure:"prefer" yaml:"prefer"`
}

// BranchConfig controls branch management settings.
type BranchConfig struct {
	StaleDays int      `mapstructure:"stale_days"  yaml:"stale_days"`
	Protected []string `mapstructure:"protected"   yaml:"protected"`
}

// Defaults returns a Config populated with default values.
func Defaults() Config {
	return Config{
		BaseBranch: "",
		Remote:     "origin",
		Log: LogConfig{
			Format: "%h %s %cr <%an>",
			Graph:  false,
			Limit:  20,
		},
		UI: UIConfig{
			Color:  "auto",
			Prefer: "",
		},
		Branch: BranchConfig{
			StaleDays: 30,
			Protected: []string{"main", "master", "develop"},
		},
	}
}
