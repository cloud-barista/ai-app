package infrarec

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/rs/zerolog/log"
)

// maxRequestBody bounds a request body. An application spec is a few kilobytes.
const maxRequestBody = 1 << 20

type appInput struct {
	App     *App                `json:"app"`
	Profile *applicationProfile `json:"profile"`
}

// specsReq is the body of POST /recommendations/specs.
type specsReq struct {
	appInput
	Limit int `json:"limit"`
}

// specsResp is the answer of POST /recommendations/specs.
type specsResp struct {
	Candidates []SpecCandidate `json:"candidates"`
	Reason     string          `json:"reason"`
}

// imageReq is the body of POST /recommendations/image.
type imageReq struct {
	appInput
	SpecID string `json:"specId"`
}

// imageResp is the answer of POST /recommendations/image.
type imageResp struct {
	ImageID         string `json:"imageId"`
	Source          string `json:"source"`
	Reason          string `json:"reason"`
	DriverStatus    string `json:"driverStatus"`
	GPUVendorStatus string `json:"gpuRuntimeVendorStatus,omitempty"`
}

// message is the error body, in the shape the AI-App service answers with.
type message struct {
	Message string `json:"message"`
}

// NewHandler serves the recommendation contract and checks the forwarded bearer.
func NewHandler(r *Recommender, token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /recommendations/specs", r.serveSpecs)
	mux.HandleFunc("POST /recommendations/image", r.serveImage)
	return requireToken(token, mux)
}

func (h *Recommender) serveSpecs(w http.ResponseWriter, req *http.Request) {
	var body specsReq
	if err := decodeBody(w, req, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, message{Message: err.Error()})
		return
	}
	app, err := normalizeApp(body.App, body.Profile)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, message{Message: err.Error()})
		return
	}

	candidates, err := h.RecommendSpecs(req.Context(), app, body.Limit)
	if err != nil {
		log.Error().Err(err).Str("appId", app.ID).Msg("Spec recommendation failed")
		writeJSON(w, http.StatusBadGateway, message{Message: "CB-Tumblebug spec recommendation failed"})
		return
	}

	reason := fmt.Sprintf("%d CB-Tumblebug specs matching %s, ordered by availability then price",
		len(candidates), describeFilter(app))
	vendors := make(map[string]int)
	for _, candidate := range candidates {
		if candidate.GPUVendorStatus != "" {
			vendors[candidate.GPUVendorStatus]++
		}
	}
	if len(vendors) > 0 {
		reason += fmt.Sprintf(". AI-App GPU inference requires NVIDIA (%d compatible, %d unsupported, %d unverified)",
			vendors[gpuVendorCompatible], vendors[gpuVendorUnsupported], vendors[unverified])
	}
	if app.Resources.MinStorageGiB > 0 {
		reason += fmt.Sprintf(". Verify the %d GiB storage floor in the provisioning review", app.Resources.MinStorageGiB)
	}
	if app.SLO.LatencyP95MSMax > 0 || app.SLO.ThroughputRPSMin > 0 {
		reason += fmt.Sprintf(". Serving SLO is unverified (p95 cap %g ms, throughput floor %g req/s; zero means unspecified)",
			app.SLO.LatencyP95MSMax, app.SLO.ThroughputRPSMin)
	}
	writeJSON(w, http.StatusOK, specsResp{Candidates: candidates, Reason: reason})
}

func (h *Recommender) serveImage(w http.ResponseWriter, req *http.Request) {
	var body imageReq
	if err := decodeBody(w, req, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, message{Message: err.Error()})
		return
	}
	app, err := normalizeApp(body.App, body.Profile)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, message{Message: err.Error()})
		return
	}
	specID := strings.TrimSpace(body.SpecID)
	if specID == "" || specID == "." || specID == ".." || strings.ContainsAny(specID, "/\\") {
		writeJSON(w, http.StatusBadRequest, message{Message: "a valid specId is required"})
		return
	}

	choice, err := h.ResolveImage(req.Context(), app, specID)
	switch {
	case errors.Is(err, errSpecNotFound), errors.Is(err, errNoCompatibleImage):
		writeJSON(w, http.StatusNotFound, message{Message: err.Error()})
		return
	case errors.Is(err, errSpecMismatch):
		writeJSON(w, http.StatusConflict, message{Message: err.Error()})
		return
	case err != nil:
		log.Error().Err(err).Str("appId", app.ID).Str("specId", specID).Msg("Image resolution failed")
		writeJSON(w, http.StatusBadGateway, message{Message: "CB-Tumblebug image search failed"})
		return
	}

	reason := choice.Reason
	vendorStatus := gpuRuntimeVendorStatus(app.Accelerator.Type, choice.Vendor)
	if vendorStatus == gpuVendorUnsupported || vendorStatus == unverified {
		reason += ". AI-App GPU inference requires NVIDIA (GPU vendor status " + vendorStatus + ")"
	}
	writeJSON(w, http.StatusOK, imageResp{
		ImageID:         choice.ID,
		Source:          "cb-tumblebug:" + h.imageNamespace,
		Reason:          reason,
		DriverStatus:    choice.DriverStatus,
		GPUVendorStatus: vendorStatus,
	})
}

// describeFilter renders the spec filter an application turns into.
func describeFilter(app *App) string {
	policy := buildSpecRequest(app, 0).Filter.Policy
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

func decodeBody(w http.ResponseWriter, req *http.Request, out any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxRequestBody))
	if err := decoder.Decode(out); err != nil || decoder.Decode(new(any)) != io.EOF {
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
