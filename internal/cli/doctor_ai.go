package cli

import (
	"fmt"
	"os"
	"os/exec"
)

// checkAIProvider emits one doctorCheck row per AI CLI that `gk ai
// commit` can drive. Missing binaries are WARN (optional dependency),
// present-but-unauthenticated is WARN too, and only an outright probe
// failure surfaces as FAIL.
//
// The `kiro-cli` row explicitly differentiates the headless CLI from
// the `kiro` IDE launcher — the two have identical names but different
// binaries; this is the top cause of user confusion per pitfalls.
func checkAIProvider(name string) doctorCheck {
	path, err := exec.LookPath(name)
	if err != nil {
		fix := fmt.Sprintf("brew install %s  # or the project's installer", name)
		if name == "kiro-cli" {
			if _, idePathErr := exec.LookPath("kiro"); idePathErr == nil {
				return doctorCheck{
					Name:   "ai provider: kiro-cli",
					Status: statusWarn,
					Detail: "IDE launcher `kiro` present, but headless `kiro-cli` missing",
					Fix:    "install kiro-cli from https://kiro.dev/docs/cli/installation",
				}
			}
			fix = "install kiro-cli (headless) from https://kiro.dev/docs/cli/installation"
		}
		return doctorCheck{
			Name:   "ai provider: " + name,
			Status: statusWarn,
			Detail: "not found on PATH",
			Fix:    fix,
		}
	}

	authOK, hint := providerAuthHint(name)
	detail := path
	if authOK {
		return doctorCheck{Name: "ai provider: " + name, Status: statusPass, Detail: detail}
	}
	return doctorCheck{
		Name:   "ai provider: " + name,
		Status: statusWarn,
		Detail: detail + " (auth unconfigured)",
		Fix:    hint,
	}
}

// providerAuthHint returns (ok, remediation). Heuristic only — we do
// not spawn subprocesses here so false-negatives turn into runtime
// errors with a clearer message later on.
func providerAuthHint(name string) (bool, string) {
	switch name {
	case "gemini":
		for _, k := range []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"} {
			if os.Getenv(k) != "" {
				return true, ""
			}
		}
		return false, "export GEMINI_API_KEY=... or run `gemini` once to set up OAuth"
	case "qwen":
		for _, k := range []string{"DASHSCOPE_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GEMINI_API_KEY", "BAILIAN_CODING_PLAN_API_KEY"} {
			if os.Getenv(k) != "" {
				return true, ""
			}
		}
		return false, "export DASHSCOPE_API_KEY=... or run `qwen auth qwen-oauth`"
	case "kiro-cli":
		if os.Getenv("KIRO_API_KEY") != "" {
			return true, ""
		}
		return false, "export KIRO_API_KEY=... (Kiro Pro/Power) or sign in via IDE"
	default:
		return true, ""
	}
}
