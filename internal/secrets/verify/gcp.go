package verify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// The Discovery API is free and checks a key that is sent with the request
// (observed). That it needs no per-project enablement is assumed; if it
// does, Google answers SERVICE_DISABLED, which still reads as a key that
// exists.
const defaultGCPEndpoint = "https://www.googleapis.com/discovery/v1/apis?name=discovery"

// gcpKeyExists are the ErrorInfo reasons that Google returns only after it
// has resolved the key to a project: the key's restrictions blocked this
// call, or the API is disabled in that project. SERVICE_DISABLED in this set
// is inferred from that order, not documented as a key check.
var gcpKeyExists = map[string]bool{
	"API_KEY_SERVICE_BLOCKED":       true,
	"API_KEY_HTTP_REFERRER_BLOCKED": true,
	"API_KEY_IP_ADDRESS_BLOCKED":    true,
	"API_KEY_ANDROID_APP_BLOCKED":   true,
	"API_KEY_IOS_APP_BLOCKED":       true,
	"SERVICE_DISABLED":              true,
}

type gcpDirectoryList struct {
	Kind string `json:"kind"`
}

type gcpError struct {
	Error struct {
		Details []struct {
			Type     string            `json:"@type"`
			Reason   string            `json:"reason"`
			Metadata map[string]string `json:"metadata"`
		} `json:"details"`
	} `json:"error"`
}

func (v *Verifier) checkGCP(ctx context.Context, c *mergedCred) Result {
	endpoint := v.GCPEndpoint
	if endpoint == "" {
		endpoint = defaultGCPEndpoint
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Result{Status: StatusUnknown, Code: CodeRequestFailed}
	}
	// A header, not the ?key= parameter, keeps the key out of URLs that
	// errors and proxies log.
	req.Header.Set("X-Goog-Api-Key", c.value)
	status, body, err := v.do(req)
	if err != nil {
		return Result{Status: StatusUnknown, Code: CodeRequestFailed}
	}
	if status == http.StatusOK {
		// A proxy or captive portal can also answer 200, so only the
		// Discovery response itself counts.
		var ok gcpDirectoryList
		if err := json.Unmarshal(body, &ok); err != nil || ok.Kind != "discovery#directoryList" {
			return Result{Status: StatusUnknown, Code: CodeRequestFailed}
		}
		return Result{Status: StatusValid}
	}
	var e gcpError
	_ = json.Unmarshal(body, &e)
	for _, d := range e.Error.Details {
		if d.Type != "type.googleapis.com/google.rpc.ErrorInfo" || d.Reason == "" {
			continue
		}
		switch {
		case d.Reason == "API_KEY_INVALID":
			return Result{Status: StatusRejected, Code: d.Reason}
		case gcpKeyExists[d.Reason]:
			return Result{Status: StatusValid, Account: d.Metadata["consumer"], Code: d.Reason}
		default:
			return Result{Status: StatusUnknown, Code: d.Reason}
		}
	}
	return Result{Status: StatusUnknown, Code: fmt.Sprintf("http_%d", status)}
}
