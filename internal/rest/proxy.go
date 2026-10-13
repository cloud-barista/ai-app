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

// enginePrefix is where both the AI-App service and the automation engine
// mount the engine routes, so they are forwarded with their path unchanged.
const enginePrefix = BasePath + "/engine"

// agentRenamed lists the agent routes that sit at the agent's root but are
// reached under agentPrefix here, because the AI-App service has a route of the
// same name: /aiapp/agent/archive is the agent's /aiapp/archive.
var agentRenamed = []string{"/archive", "/readyz"}

// newAgentProxy forwards intent and agent routes to the automation agent.
//
// Headers pass through unchanged, Authorization included, so the agent applies
// the same credentials the caller sent here.
func newAgentProxy(agentURL string) http.Handler {
	return newForwardProxy("Automation agent", agentURL, agentPath)
}

// newEngineProxy forwards engine routes to the automation engine with their
// path unchanged, the same way newAgentProxy forwards agent routes.
func newEngineProxy(engineURL string) http.Handler {
	return newForwardProxy("Automation engine", engineURL, func(path string) string { return path })
}

// newForwardProxy forwards to the service called name at serviceURL, mapping
// each path with mapPath.
func newForwardProxy(name, serviceURL string, mapPath func(string) string) http.Handler {
	target, err := url.Parse(serviceURL)
	if err != nil || target.Scheme == "" || target.Host == "" {
		log.Error().Str("url", serviceURL).Msg(name + " URL is malformed, its routes will report 502")
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeUnreachable(w, name)
		})
	}

	return &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.URL.Path = mapPath(r.In.URL.Path)
			r.Out.URL.RawPath = ""
			r.SetXForwarded()
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Warn().Err(err).Str("url", serviceURL).Str("path", r.URL.Path).
				Msg("Failed to reach the " + strings.ToLower(name))
			writeUnreachable(w, name)
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

func writeUnreachable(w http.ResponseWriter, name string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadGateway)
	_ = json.NewEncoder(w).Encode(model.SimpleMsg{Message: name + " is not reachable from the AI-App service"})
}
