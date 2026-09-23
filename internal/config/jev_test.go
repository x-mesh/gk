package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestJevDefaults(t *testing.T) {
	d := Defaults().AI.Jev
	if d.Endpoint != "https://api.typesafe.ai/v1/systemone" || d.Model != "jev-latest" {
		t.Fatalf("defaults = %+v", d)
	}
	if d.APIKey != "" || d.Suggest || d.FindRerank {
		t.Fatalf("features must default off and have no key: %+v", d)
	}
}

func TestLoadJevGlobalAndEnvironmentOverrideLocal(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	for _, name := range []string{"GK_AI_JEV_ENDPOINT", "GK_AI_JEV_API_KEY", "GK_AI_JEV_MODEL", "GK_AI_JEV_SUGGEST", "GK_AI_JEV_FIND_RERANK"} {
		old, had := os.LookupEnv(name)
		_ = os.Unsetenv(name)
		t.Cleanup(func() {
			if had {
				_ = os.Setenv(name, old)
			} else {
				_ = os.Unsetenv(name)
			}
		})
	}
	global := filepath.Join(configHome, "gk", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(global), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(global, []byte("ai:\n  jev:\n    endpoint: https://global.example/systemone\n    api_key: global-secret\n    model: global-model\n    suggest: true\n    find_rerank: false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	command := exec.Command("git", "init")
	command.Dir = repo
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(repo, ".gk.yaml"), []byte("ai:\n  jev:\n    endpoint: https://repo.example/systemone\n    api_key: repo-secret\n    model: repo-model\n    suggest: false\n    find_rerank: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	orig, _ := os.Getwd()
	defer func() { _ = os.Chdir(orig) }()
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AI.Jev.Endpoint != "https://global.example/systemone" || cfg.AI.Jev.APIKey != "global-secret" || cfg.AI.Jev.Model != "global-model" || !cfg.AI.Jev.Suggest || cfg.AI.Jev.FindRerank {
		t.Fatalf("local config must not override Jev: %+v", cfg.AI.Jev)
	}
	t.Setenv("GK_AI_JEV_ENDPOINT", "https://env.example/systemone")
	t.Setenv("GK_AI_JEV_API_KEY", "env-secret")
	t.Setenv("GK_AI_JEV_MODEL", "env-model")
	t.Setenv("GK_AI_JEV_SUGGEST", "false")
	t.Setenv("GK_AI_JEV_FIND_RERANK", "true")
	cfg, err = Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AI.Jev.Endpoint != "https://env.example/systemone" || cfg.AI.Jev.APIKey != "env-secret" || cfg.AI.Jev.Model != "env-model" || cfg.AI.Jev.Suggest || !cfg.AI.Jev.FindRerank {
		t.Fatalf("environment must override global Jev: %+v", cfg.AI.Jev)
	}
}

func TestLoadJevRejectsInvalidEnvironmentBool(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GK_AI_JEV_SUGGEST", "sometimes")
	if _, err := Load(nil); err == nil {
		t.Fatal("invalid Jev boolean must fail")
	}
}
