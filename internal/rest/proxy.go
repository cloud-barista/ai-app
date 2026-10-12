package rest

import (
	"encoding/json"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

// agentPrefix is where the AI-App service mounts the agent's own routes.
const agentPrefix = BasePath + "/agent"

// agentRenamed lists the agent routes that sit at the agent's root but are
// reached under agentPrefix here, because the AI-App service has a route of the
// same name: /aiapp/agent/archive is the agent's /aiapp/archive.
var agentRenamed = []string{"/archive", "/readyz"}

// newAgentProxy forwards intent and agent routes to the automation agent.
//
// Headers pass through unchanged, Authorization included, so the agent applies
// the same credentials the caller sent here.
func newAgentProxy(agentURL string) http.Handler {
	target, err := url.Parse(agentURL)
	if err != nil || target.Scheme == "" || target.Host == "" {
		log.Error().Str("url", agentURL).Msg("Automation agent URL is malformed, agent routes will report 502")
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeAgentUnreachable(w)
		})
	}

	return &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.URL.Path = agentPath(r.In.URL.Path)
			r.Out.URL.RawPath = ""
			r.SetXForwarded()
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Warn().Err(err).Str("url", agentURL).Str("path", r.URL.Path).
				Msg("Failed to reach the automation agent")
			writeAgentUnreachable(w)
		},
	}
}

// agentPath maps a path received here to the path on the agent.
func agentPath(path string) string {
	rest, ok := strings.CutPrefix(path, agentPrefix)
	if !ok {
		return path
	}
	for _, name := range agentRenamed {
		if rest == name || strings.HasPrefix(rest, name+"/") {
			return BasePath + rest
		}
	}
	return path
}

func writeAgentUnreachable(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadGateway)
	_ = json.NewEncoder(w).Encode(model.SimpleMsg{Message: "Automation agent is not reachable from the AI-App service"})
}
