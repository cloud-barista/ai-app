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

// enginePrefix is where both the AI-App service and the automation engine
// mount the engine routes, so they are forwarded with their path unchanged.
const enginePrefix = BasePath + "/engine"

// partnerNLPrefix is where both the AI-App service and the partner natural
// language agent mount its routes.
const partnerNLPrefix = BasePath + "/partner/nl"

// newEngineProxy forwards engine routes to the automation engine with their
// path unchanged.
//
// Headers pass through unchanged, Authorization included, so the engine applies
// the same credentials the caller sent here.
func newEngineProxy(engineURL string) http.Handler {
	return newForwardProxy("Automation engine", engineURL, samePath)
}

// newPartnerNLProxy forwards the partner natural language agent routes, the
// same way newEngineProxy forwards engine routes.
func newPartnerNLProxy(agentURL string) http.Handler {
	return newForwardProxy("Partner natural language agent", agentURL, samePath)
}

func samePath(path string) string { return path }

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

func writeUnreachable(w http.ResponseWriter, name string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadGateway)
	_ = json.NewEncoder(w).Encode(model.SimpleMsg{Message: name + " is not reachable from the AI-App service"})
}
