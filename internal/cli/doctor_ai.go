package cli

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/x-mesh/gk/internal/config"
)

// aiAPISpec describes one HTTP-API provider the doctor knows how to
// probe. endpoint is the *default* reachability URL; it is replaced at
// runtime when the user overrides it in config (ai.<name>.endpoint).
type aiAPISpec struct {
	name     string
	envKey   string
	endpoint string
}

// aiCLISpec describes one CLI-binary provider the doctor knows about.
type aiCLISpec struct {
	name string
}

// defaultAIAPISpecs and defaultAICLISpecs enumerate the providers gk
// can drive. Kept as package-level data so both the live check path and
// the tests share one source of truth.
var defaultAIAPISpecs = []aiAPISpec{
	{name: "anthropic", envKey: "ANTHROPIC_API_KEY", endpoint: "https://api.anthropic.com/v1/messages"},
	{name: "openai", envKey: "OPENAI_API_KEY", endpoint: "https://api.openai.com/v1/models"},
	{name: "nvidia", envKey: "NVIDIA_API_KEY", endpoint: "https://integrate.api.nvidia.com/v1/models"},
	{name: "groq", envKey: "GROQ_API_KEY", endpoint: "https://api.groq.com/openai/v1/models"},
}

var defaultAICLISpecs = []aiCLISpec{
	{name: "gemini"},
	{name: "qwen"},
	{name: "kiro-cli"},
}

// aiDoctorChecks builds the full set of AI provider rows, honouring the
// resolved config: per-provider endpoint overrides are probed instead of
// the built-in defaults, and the configured default provider
// (cfg.AI.Provider) is flagged so the user can see which one `gk commit`
// will actually reach for. cfg may be nil (config failed to load) — in
// that case the built-in defaults are used so the AI section still
// renders.
func aiDoctorChecks(cfg *config.Config) []doctorCheck {
	defaultProvider := ""
	if cfg != nil {
		defaultProvider = strings.TrimSpace(cfg.AI.Provider)
	}

	checks := make([]doctorCheck, 0, len(defaultAIAPISpecs)+len(defaultAICLISpecs))
	for _, spec := range defaultAIAPISpecs {
		endpoint := spec.endpoint
		overridden := false
		keyFromConfig := false
		if cfg != nil {
			if ep := configEndpointFor(cfg, spec.name); ep != "" {
				endpoint = ep
				overridden = true
			}
			keyFromConfig = configAPIKeyFor(cfg, spec.name) != ""
		}
		checks = append(checks, decorateDefaultProvider(
			checkAIAPIProvider(spec.name, spec.envKey, endpoint, overridden, keyFromConfig),
			spec.name, defaultProvider))
	}
	for _, spec := range defaultAICLISpecs {
		checks = append(checks, decorateDefaultProvider(
			checkAIProvider(spec.name), spec.name, defaultProvider))
	}
	return checks
}

// configEndpointFor returns the user-configured endpoint override for an
// HTTP-API provider, or "" when no override is set. Only HTTP-API
// providers carry an endpoint in config; CLI providers return "".
func configEndpointFor(cfg *config.Config, name string) string {
	switch name {
	case "anthropic":
		return strings.TrimSpace(cfg.AI.Anthropic.Endpoint)
	case "openai":
		return strings.TrimSpace(cfg.AI.OpenAI.Endpoint)
	case "nvidia":
		return strings.TrimSpace(cfg.AI.Nvidia.Endpoint)
	case "groq":
		return strings.TrimSpace(cfg.AI.Groq.Endpoint)
	default:
		if custom, ok := cfg.AI.CustomProvider(name); ok {
			return strings.TrimSpace(custom.Endpoint)
		}
		return ""
	}
}

// configAPIKeyFor returns the user-configured api_key for an HTTP-API
// provider, or "" when none is set. Only the presence matters to the
// caller — the value is never logged or returned to the report.
func configAPIKeyFor(cfg *config.Config, name string) string {
	switch name {
	case "anthropic":
		return strings.TrimSpace(cfg.AI.Anthropic.APIKey)
	case "openai":
		return strings.TrimSpace(cfg.AI.OpenAI.APIKey)
	case "nvidia":
		return strings.TrimSpace(cfg.AI.Nvidia.APIKey)
	case "groq":
		return strings.TrimSpace(cfg.AI.Groq.APIKey)
	default:
		if custom, ok := cfg.AI.CustomProvider(name); ok {
			return strings.TrimSpace(custom.APIKey)
		}
		return ""
	}
}

// decorateDefaultProvider annotates the check that matches the
// configured default provider (ai.provider) so the report makes clear
// which provider `gk commit` will use by default. Matching is on the
// provider's bare name (the suffix after "ai api: " / "ai provider: ").
func decorateDefaultProvider(c doctorCheck, providerName, defaultProvider string) doctorCheck {
	if defaultProvider == "" || providerName != defaultProvider {
		return c
	}
	c.Name = c.Name + " (default)"
	return c
}

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
	if authOK {
		return doctorCheck{
			Name:   "ai provider: " + name,
			Status: statusPass,
			Detail: path + " · auth configured (validity not verified)",
		}
	}
	return doctorCheck{
		Name:   "ai provider: " + name,
		Status: statusWarn,
		Detail: path + " · auth not configured",
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
// The probe is an *unauthenticated* GET, so it can only tell us two
// things: (a) the key is present in the environment ("set") and (b) the
// endpoint is reachable from this host. It deliberately does NOT verify
// that the key is valid — the wording reflects that so a "set" row is
// never mistaken for "authenticated". The key value itself is never
// printed; only set/unset is reported.
//
// Probe interpretation:
//
//	200 / 401 / 403 / 404 / 405 → endpoint reachable (any response is enough)
//	5xx                         → endpoint is up but degraded
//	dial / timeout / TLS errors → network blocked
func checkAIAPIProvider(name, envKey, endpoint string, endpointOverridden, keyFromConfig bool) doctorCheck {
	endpointNote := "endpoint"
	if endpointOverridden {
		endpointNote = "custom endpoint"
	}

	// The key may come from the env var or from ai.<name>.api_key in
	// config; either satisfies auth. keyNote names whichever source is
	// active so the report points at the right place. The value itself
	// is never printed.
	keyNote := envKey + " set"
	switch {
	case os.Getenv(envKey) != "":
		// env var wins the note even if config also has one
	case keyFromConfig:
		keyNote = "ai." + name + ".api_key set"
	default:
		return doctorCheck{
			Name:   "ai api: " + name,
			Status: statusWarn,
			Detail: envKey + " not set",
			Fix:    "export " + envKey + "=...  (or set ai." + name + ".api_key in config) # then `gk commit --provider " + name + "`",
		}
	}
	if endpoint == "" {
		// No probe configured for this provider — key presence is the
		// best we can do without a network round-trip.
		return doctorCheck{
			Name:   "ai api: " + name,
			Status: statusPass,
			Detail: keyNote + " (validity not verified)",
		}
	}

	reachable, status, err := probeAIAPI(endpoint)
	switch {
	case reachable:
		return doctorCheck{
			Name:   "ai api: " + name,
			Status: statusPass,
			Detail: fmt.Sprintf("%s (validity not verified) · %s reachable [HTTP %d]", keyNote, endpointNote, status),
		}
	case err != nil:
		return doctorCheck{
			Name:   "ai api: " + name,
			Status: statusWarn,
			Detail: fmt.Sprintf("%s (validity not verified) · %s probe failed: %v", keyNote, endpointNote, err),
			Fix:    "check network/proxy reachability to " + endpoint,
		}
	default:
		return doctorCheck{
			Name:   "ai api: " + name,
			Status: statusWarn,
			Detail: fmt.Sprintf("%s (validity not verified) · %s returned HTTP %d", keyNote, endpointNote, status),
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
// errors with a clearer message later on. "ok" means an auth source
// (API key env var or, for OAuth CLIs, the expectation that the CLI was
// signed in) is *present*, not that it is valid.
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
