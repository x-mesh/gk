package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/x-mesh/gk/internal/config"
)

func init() {
	configCmd := &cobra.Command{
		Use:   "config",
		Short: "Read or set gk configuration",
	}
	configCmd.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "Print resolved configuration as YAML",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(nil)
			if err != nil {
				return err
			}
			out, err := yaml.Marshal(cfg)
			if err != nil {
				return err
			}
			fmt.Print(string(out))
			return nil
		},
	})
	configCmd.AddCommand(&cobra.Command{
		Use:   "get <key>",
		Short: "Print a single dot-notation config value",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(nil)
			if err != nil {
				return err
			}
			raw, err := yaml.Marshal(cfg)
			if err != nil {
				return err
			}
			var m map[string]any
			if err := yaml.Unmarshal(raw, &m); err != nil {
				return err
			}
			val, ok := lookupDot(m, args[0])
			if !ok {
				return fmt.Errorf("key %q not found", args[0])
			}
			fmt.Println(val)
			return nil
		},
	})
	configCmd.AddCommand(&cobra.Command{
		Use:   "set <key> <value>",
		Short: "Not yet implemented",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintln(os.Stderr, "gk config set: not implemented in v0.1.0 initial drop")
			os.Exit(2)
			return nil
		},
	})
	rootCmd.AddCommand(configCmd)
}

func lookupDot(m map[string]any, key string) (any, bool) {
	cur := any(m)
	start := 0
	for i := 0; i <= len(key); i++ {
		if i == len(key) || key[i] == '.' {
			seg := key[start:i]
			sub, ok := cur.(map[string]any)
			if !ok {
				return nil, false
			}
			cur, ok = sub[seg]
			if !ok {
				return nil, false
			}
			start = i + 1
		}
	}
	return cur, true
}
