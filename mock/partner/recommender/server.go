package recommender

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/rs/zerolog/log"
)

// ReasonPrefix marks every reason this mock returns, so a plan built on it never
// reads as if a real recommendation had been made.
const ReasonPrefix = "[partner mock] "

// maxRequestBody bounds a request body. An application spec is a few kilobytes.
const maxRequestBody = 1 << 20

// specsReq is the body of POST /recommendations/specs.
type specsReq struct {
	App   *App `json:"app"`
	Limit int  `json:"limit"`
}

// specsResp is the answer of POST /recommendations/specs.
type specsResp struct {
	Candidates []SpecCandidate `json:"candidates"`
	Reason     string          `json:"reason"`
}

// imageReq is the body of POST /recommendations/image.
type imageReq struct {
	App    *App   `json:"app"`
	SpecID string `json:"specId"`
}

// imageResp is the answer of POST /recommendations/image.
type imageResp struct {
	ImageID string `json:"imageId"`
	Source  string `json:"source"`
	Reason  string `json:"reason"`
}

// message is the error body, in the shape the AI-App service answers with.
type message struct {
	Message string `json:"message"`
}

// NewHandler serves the proposed recommendation contract over r.
//
// A non-empty token is required as a bearer token on every route.
func NewHandler(r *Recommender, token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /recommendations/specs", r.serveSpecs)
	mux.HandleFunc("POST /recommendations/image", r.serveImage)
	return requireToken(token, mux)
}

func (r *Recommender) serveSpecs(w http.ResponseWriter, req *http.Request) {
	var body specsReq
	if err := decodeBody(w, req, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, message{Message: err.Error()})
		return
	}
	if err := checkApp(body.App); err != nil {
		writeJSON(w, http.StatusBadRequest, message{Message: err.Error()})
		return
	}

	limit := body.Limit
	if limit <= 0 {
		limit = defaultSpecCandidates
	}
	candidates, err := r.RecommendSpecs(req.Context(), body.App, limit)
	if err != nil {
		log.Error().Err(err).Str("appId", body.App.ID).Msg("Spec recommendation failed")
		writeJSON(w, http.StatusBadGateway, message{Message: "CB-Tumblebug spec recommendation failed"})
		return
	}

	writeJSON(w, http.StatusOK, specsResp{
		Candidates: candidates,
		Reason: ReasonPrefix + fmt.Sprintf("%d CB-Tumblebug specs matching %s, cheapest first",
			len(candidates), describeFilter(body.App, limit)),
	})
}

func (r *Recommender) serveImage(w http.ResponseWriter, req *http.Request) {
	var body imageReq
	if err := decodeBody(w, req, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, message{Message: err.Error()})
		return
	}
	if err := checkApp(body.App); err != nil {
		writeJSON(w, http.StatusBadRequest, message{Message: err.Error()})
		return
	}
	if body.SpecID == "" {
		writeJSON(w, http.StatusBadRequest, message{Message: "specId is required"})
		return
	}

	choice, err := r.ResolveImage(req.Context(), body.App, body.SpecID)
	if err != nil {
		log.Error().Err(err).Str("appId", body.App.ID).Str("specId", body.SpecID).Msg("Image resolution failed")
		writeJSON(w, http.StatusBadGateway, message{Message: "CB-Tumblebug image search failed"})
		return
	}

	writeJSON(w, http.StatusOK, imageResp{
		ImageID: choice.ID,
		Source:  "cb-tumblebug:" + r.imageNamespace,
		Reason:  ReasonPrefix + choice.Reason,
	})
}

// describeFilter renders the spec filter an application turns into.
func describeFilter(app *App, limit int) string {
	policy := buildSpecRequest(app, limit).Filter.Policy
	if len(policy) == 0 {
		return "no filter"
	}
	parts := make([]string, 0, len(policy))
	for _, cond := range policy {
		for _, op := range cond.Condition {
			parts = append(parts, cond.Metric+op.Operator+op.Operand)
		}
	}
	return strings.Join(parts, ", ")
}

func checkApp(app *App) error {
	if app == nil || app.ID == "" {
		return errors.New("app with an id is required")
	}
	return nil
}

func decodeBody(w http.ResponseWriter, req *http.Request, out any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxRequestBody))
	if err := decoder.Decode(out); err != nil {
		return errors.New("request body is not valid JSON")
	}
	return nil
}

func requireToken(token string, next http.Handler) http.Handler {
	if token == "" {
		return next
	}
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		got := []byte(req.Header.Get("Authorization"))
		if subtle.ConstantTimeCompare(got, want) != 1 {
			writeJSON(w, http.StatusUnauthorized, message{Message: "missing or invalid bearer token"})
			return
		}
		next.ServeHTTP(w, req)
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Warn().Err(err).Msg("Failed to write response")
	}
}
