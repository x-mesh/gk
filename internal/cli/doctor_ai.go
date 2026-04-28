package cli

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"time"
)

// checkAIProvider emits one doctorCheck row per AI CLI that `gk
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

// checkAIAPIProvider reports on HTTP-API providers (no binary on disk —
// just an API key in the environment). Beyond the env-var check it also
// pings the provider's endpoint with a short-timeout GET so the user
// sees whether the API is actually reachable from this machine — the
// most common failure (proxy, DNS, captive portal) wouldn't surface
// until the first real `gk commit`.
//
// Probe interpretation:
//
//	200 / 401 / 403 / 404 / 405 → endpoint reachable (any response is enough)
//	5xx                         → endpoint is up but degraded
//	dial / timeout / TLS errors → network blocked
func checkAIAPIProvider(name, envKey, endpoint string) doctorCheck {
	if os.Getenv(envKey) == "" {
		return doctorCheck{
			Name:   "ai api: " + name,
			Status: statusWarn,
			Detail: envKey + " not set",
			Fix:    "export " + envKey + "=...  # then `gk commit --provider " + name + "`",
		}
	}
	if endpoint == "" {
		// No probe configured for this provider — the env-var presence
		// is the best we can do without a network round-trip.
		return doctorCheck{
			Name:   "ai api: " + name,
			Status: statusPass,
			Detail: envKey + " set",
		}
	}

	reachable, status, err := probeAIAPI(endpoint)
	switch {
	case reachable:
		return doctorCheck{
			Name:   "ai api: " + name,
			Status: statusPass,
			Detail: fmt.Sprintf("%s set · endpoint %d", envKey, status),
		}
	case err != nil:
		return doctorCheck{
			Name:   "ai api: " + name,
			Status: statusWarn,
			Detail: fmt.Sprintf("%s set · probe failed: %v", envKey, err),
			Fix:    "check network/proxy reachability to " + endpoint,
		}
	default:
		return doctorCheck{
			Name:   "ai api: " + name,
			Status: statusWarn,
			Detail: fmt.Sprintf("%s set · endpoint returned %d", envKey, status),
			Fix:    "the provider returned a server error — try again later or check the provider's status page",
		}
	}
}

// probeAIAPI sends a short-timeout, unauthenticated GET to endpoint and
// returns (reachable, status, error). reachable is true for any 4xx
// other than network failure — those mean the request reached a real
// server and was rejected by it. 5xx is *not* reachable for our
// purposes since the endpoint is misbehaving.
func probeAIAPI(endpoint string) (reachable bool, status int, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, rErr := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if rErr != nil {
		return false, 0, rErr
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, dErr := client.Do(req)
	if dErr != nil {
		return false, 0, dErr
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 500 {
		return true, resp.StatusCode, nil
	}
	return false, resp.StatusCode, nil
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
