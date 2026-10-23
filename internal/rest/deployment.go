package rest

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/catalog"
	"github.com/ish-hcc/ai-agent-proto/internal/deploy"
	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

// malformedBody is the caller-facing text for an unreadable request body.
const malformedBody = "Malformed request body: check JSON syntax"

// allowedActions are the lifecycle actions this API forwards.
//
// Recovery actions (refine, reconcile, abort, withdraw) are excluded: they change
// resource bookkeeping in ways that need a deliberate operator decision.
var allowedActions = map[string]bool{
	"suspend":   true,
	"resume":    true,
	"reboot":    true,
	"terminate": true,
}

// RestPostDeploymentPlan godoc
// @ID PostDeploymentPlan
// @Summary Plan a deployment
// @Description Build the provisioning request for an AI application without contacting any cloud. Leave specId or imageId empty to take them from the partner recommender API (AIAPP_RECOMMENDER_URL); without one both must be pinned
// @Tags [Deploy] AI Application Deployment
// @Accept json
// @Produce json
// @Param nsId path string true "Namespace ID" default(default)
// @Param deployReq body model.DeployAppReq true "Deployment request"
// @Success 200 {object} model.DeploymentPlan "Deployment plan"
// @Failure 400 {object} model.SimpleMsg "Malformed or incomplete request"
// @Failure 404 {object} model.SimpleMsg "AI application not found"
// @Failure 409 {object} model.SimpleMsg "A model service the application requires is not deployed"
// @Failure 422 {object} model.SimpleMsg "No recommender configured: pin specId and imageId"
// @Failure 500 {object} model.SimpleMsg "Deployment planning failed"
// @Failure 502 {object} model.SimpleMsg "Remote placement service failed"
// @Security BearerAuth
// @Router /ns/{nsId}/deployments/plan [post]
func (h *Handler) RestPostDeploymentPlan(c echo.Context) error {
	ctx := c.Request().Context()
	nsID := c.Param("nsId")

	req, err := bindDeployReq(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, model.SimpleMsg{Message: messageFor(err, malformedBody)})
	}

	plan, err := h.deploy.Plan(ctx, nsID, req)
	if err != nil {
		log.Error().Err(err).Str("nsId", nsID).Str("appId", req.AppID).Msg("Failed to plan deployment")
		return c.JSON(statusCodeFor(err), model.SimpleMsg{Message: messageFor(err, "Deployment planning failed")})
	}
	return c.JSON(http.StatusOK, plan)
}

// RestPostDeployment godoc
// @ID PostDeployment
// @Summary Deploy an AI application
// @Description Provision nodes and install the AI application on them. The request is reviewed first, refused if the name is taken, and only sent to the cloud when the dry-run guard is off. After provisioning the serving port is checked on every node once; with waitHealthy=true the call keeps checking until every node answers or AIAPP_HEALTH_TIMEOUT passes. Either way a node that does not answer is reported under health and does not fail the deployment
// @Tags [Deploy] AI Application Deployment
// @Accept json
// @Produce json
// @Param nsId path string true "Namespace ID" default(default)
// @Param waitHealthy query bool false "Wait until the application answers on every node, up to AIAPP_HEALTH_TIMEOUT"
// @Param deployReq body model.DeployAppReq true "Deployment request"
// @Success 200 {object} model.DeploymentResult "Deployment planned or provisioned"
// @Failure 400 {object} model.SimpleMsg "Malformed request or request not viable"
// @Failure 404 {object} model.SimpleMsg "AI application not found"
// @Failure 409 {object} model.SimpleMsg "Deployment name already in use, or a model service the application requires is not deployed"
// @Failure 422 {object} model.SimpleMsg "No recommender configured: pin specId and imageId"
// @Failure 500 {object} model.SimpleMsg "Deployment failed"
// @Failure 502 {object} model.SimpleMsg "Remote placement service failed"
// @Security BearerAuth
// @Router /ns/{nsId}/deployments [post]
func (h *Handler) RestPostDeployment(c echo.Context) error {
	ctx := c.Request().Context()
	nsID := c.Param("nsId")

	req, err := bindDeployReq(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, model.SimpleMsg{Message: messageFor(err, malformedBody)})
	}

	deployFn := h.deploy.Deploy
	if c.QueryParam("waitHealthy") == "true" {
		deployFn = h.deploy.DeployAndWait
	}

	started := time.Now()
	result, err := deployFn(ctx, nsID, req)
	h.archiveDirect(ctx, nsID, kindDeployment, "deploy_ai_app", started, req, result, h.deploy.DryRun(), err)
	if err != nil {
		log.Error().Err(err).Str("nsId", nsID).Str("appId", req.AppID).
			Str("infraName", req.InfraName).Msg("Failed to deploy AI application")
		return c.JSON(statusCodeFor(err), model.SimpleMsg{Message: messageFor(err, "Deployment failed")})
	}
	return c.JSON(http.StatusOK, result)
}

// RestGetAllDeployments godoc
// @ID GetAllDeployments
// @Summary List deployments
// @Description List every deployment of the namespace with its node status
// @Tags [Deploy] AI Application Deployment
// @Produce json
// @Param nsId path string true "Namespace ID" default(default)
// @Success 200 {array} tumblebug.InfraInfo "Deployment list"
// @Failure 500 {object} model.SimpleMsg "Deployment lookup failed"
// @Security BearerAuth
// @Router /ns/{nsId}/deployments [get]
func (h *Handler) RestGetAllDeployments(c echo.Context) error {
	ctx := c.Request().Context()
	nsID := c.Param("nsId")

	list, err := h.deploy.List(ctx, nsID)
	if err != nil {
		log.Error().Err(err).Str("nsId", nsID).Msg("Failed to list deployments")
		return c.JSON(statusCodeFor(err), model.SimpleMsg{Message: "Deployment lookup failed"})
	}
	return c.JSON(http.StatusOK, list)
}

// RestGetDeploymentStatus godoc
// @ID GetDeploymentStatus
// @Summary Get deployment status
// @Description Report the node status summary, suitable for polling while a deployment comes up
// @Tags [Deploy] AI Application Deployment
// @Produce json
// @Param nsId path string true "Namespace ID" default(default)
// @Param infraId path string true "Deployment ID"
// @Success 200 {object} tumblebug.InfraStatusView "Deployment status"
// @Failure 404 {object} model.SimpleMsg "Deployment not found"
// @Failure 500 {object} model.SimpleMsg "Deployment status lookup failed"
// @Security BearerAuth
// @Router /ns/{nsId}/deployments/{infraId}/status [get]
func (h *Handler) RestGetDeploymentStatus(c echo.Context) error {
	ctx := c.Request().Context()
	nsID := c.Param("nsId")
	infraID := c.Param("infraId")

	status, err := h.deploy.Status(ctx, nsID, infraID)
	if err != nil {
		log.Error().Err(err).Str("nsId", nsID).Str("infraId", infraID).Msg("Failed to get deployment status")
		return c.JSON(statusCodeFor(err), model.SimpleMsg{Message: "Deployment status lookup failed"})
	}
	return c.JSON(http.StatusOK, status)
}

// RestGetDeploymentAccelerator godoc
// @ID GetDeploymentAccelerator
// @Summary Read the accelerator on a deployment
// @Description Ask the deployed nodes what accelerator they carry and compare it against what the application asked for. Values the driver cannot report are left unknown rather than zero, and MIG state is reported because it decides what is observable at all
// @Tags [Deploy] AI Application Deployment
// @Produce json
// @Param nsId path string true "Namespace ID" default(default)
// @Param infraId path string true "Deployment ID"
// @Param appId query string false "Registered application to compare against"
// @Success 200 {object} model.AcceleratorReport "Accelerator report"
// @Failure 404 {object} model.SimpleMsg "Deployment or application not found"
// @Failure 500 {object} model.SimpleMsg "Accelerator probe failed"
// @Security BearerAuth
// @Router /ns/{nsId}/deployments/{infraId}/accelerator [get]
func (h *Handler) RestGetDeploymentAccelerator(c echo.Context) error {
	ctx := c.Request().Context()
	nsID := c.Param("nsId")
	infraID := c.Param("infraId")

	var app *model.AppSpec
	if appID := c.QueryParam("appId"); appID != "" {
		found, err := h.catalog.Get(ctx, appID)
		if err != nil {
			log.Warn().Err(err).Str("appId", appID).Msg("Failed to get AI application")
			return c.JSON(statusCodeFor(err), model.SimpleMsg{Message: "AI application not found"})
		}
		app = found
	}

	report, err := h.deploy.ProbeAccelerator(ctx, nsID, infraID, app)
	if err != nil {
		log.Error().Err(err).Str("nsId", nsID).Str("infraId", infraID).Msg("Failed to probe accelerator")
		return c.JSON(statusCodeFor(err), model.SimpleMsg{Message: "Accelerator probe failed"})
	}
	return c.JSON(http.StatusOK, report)
}

// RestGetDeploymentHealth godoc
// @ID GetDeploymentHealth
// @Summary Check whether a deployment serves
// @Description Run a read-only curl against 127.0.0.1 on the serving port and health path on every node of the deployment, through the CB-Tumblebug remote command channel. A node is ready on a 2xx or 3xx answer, and the deployment is ready when every node is. Without appId the application is read from the ai-mcmp.app-id label of the deployment
// @Tags [Deploy] AI Application Deployment
// @Produce json
// @Param nsId path string true "Namespace ID" default(default)
// @Param infraId path string true "Deployment ID"
// @Param appId query string false "Registered application whose serving port is checked. Defaults to the deployment label"
// @Success 200 {object} model.ServingHealth "Serving health"
// @Failure 400 {object} model.SimpleMsg "No appId given and the deployment carries no application label"
// @Failure 404 {object} model.SimpleMsg "Deployment or application not found"
// @Failure 500 {object} model.SimpleMsg "Serving health check failed"
// @Security BearerAuth
// @Router /ns/{nsId}/deployments/{infraId}/health [get]
func (h *Handler) RestGetDeploymentHealth(c echo.Context) error {
	ctx := c.Request().Context()
	nsID := c.Param("nsId")
	infraID := c.Param("infraId")

	var (
		app *model.AppSpec
		err error
	)
	if appID := c.QueryParam("appId"); appID != "" {
		app, err = h.catalog.Get(ctx, appID)
	} else {
		app, err = h.deploy.DeployedApp(ctx, nsID, infraID)
	}
	if err != nil {
		log.Warn().Err(err).Str("nsId", nsID).Str("infraId", infraID).Msg("Failed to find the deployed application")
		return c.JSON(statusCodeFor(err), model.SimpleMsg{Message: deployedAppMessage(err)})
	}

	health, err := h.deploy.CheckServingHealth(ctx, nsID, infraID, app)
	if err != nil {
		log.Error().Err(err).Str("nsId", nsID).Str("infraId", infraID).Msg("Failed to check serving health")
		return c.JSON(statusCodeFor(err), model.SimpleMsg{Message: "Serving health check failed"})
	}
	return c.JSON(http.StatusOK, health)
}

// RestPostDeploymentUpdate godoc
// @ID PostDeploymentUpdate
// @Summary Update a deployment
// @Description Apply the current version of a registered AI application to a running deployment by running its install commands on every node again. The nodes are kept, and nothing is sent while the dry-run guard is on
// @Tags [Deploy] AI Application Deployment
// @Accept json
// @Produce json
// @Param nsId path string true "Namespace ID" default(default)
// @Param infraId path string true "Deployment ID"
// @Param updateReq body model.UpdateDeploymentReq true "Application to apply"
// @Success 200 {object} model.ProvisionResult "Update applied or planned"
// @Failure 400 {object} model.SimpleMsg "Malformed or incomplete request"
// @Failure 404 {object} model.SimpleMsg "Deployment or application not found"
// @Failure 409 {object} model.SimpleMsg "Deployment was not deployed with this application, or a model service it requires is not deployed"
// @Failure 500 {object} model.SimpleMsg "Deployment update failed"
// @Failure 502 {object} model.SimpleMsg "Remote placement service failed"
// @Security BearerAuth
// @Router /ns/{nsId}/deployments/{infraId}/update [post]
func (h *Handler) RestPostDeploymentUpdate(c echo.Context) error {
	ctx := c.Request().Context()
	nsID := c.Param("nsId")
	infraID := c.Param("infraId")

	var req model.UpdateDeploymentReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, model.SimpleMsg{Message: malformedBody})
	}
	if err := req.Validate(); err != nil {
		return c.JSON(http.StatusBadRequest, model.SimpleMsg{Message: messageFor(err, malformedBody)})
	}

	started := time.Now()
	result, err := h.deploy.Update(ctx, nsID, infraID, req.AppID)
	h.archiveDirect(ctx, nsID, kindDeployment, "update_deployment", started,
		map[string]any{"infraId": infraID, "appId": req.AppID}, result, h.deploy.DryRun(), err)
	if err != nil {
		log.Error().Err(err).Str("nsId", nsID).Str("infraId", infraID).Str("appId", req.AppID).
			Msg("Failed to update deployment")
		return c.JSON(statusCodeFor(err), model.SimpleMsg{Message: messageFor(err, "Deployment update failed")})
	}
	return c.JSON(http.StatusOK, result)
}

// RestPostDeploymentControl godoc
// @ID PostDeploymentControl
// @Summary Control a deployment
// @Description Apply suspend, resume, reboot or terminate to every node. terminate stops the nodes but keeps the records, so delete afterwards to stop billing
// @Tags [Deploy] AI Application Deployment
// @Accept json
// @Produce json
// @Param nsId path string true "Namespace ID" default(default)
// @Param infraId path string true "Deployment ID"
// @Param controlReq body model.ControlAppReq true "Lifecycle action"
// @Success 200 {object} model.ControlResult "Action applied or planned"
// @Failure 400 {object} model.SimpleMsg "Unsupported action"
// @Failure 404 {object} model.SimpleMsg "Deployment not found"
// @Failure 500 {object} model.SimpleMsg "Deployment control failed"
// @Security BearerAuth
// @Router /ns/{nsId}/deployments/{infraId}/control [post]
func (h *Handler) RestPostDeploymentControl(c echo.Context) error {
	ctx := c.Request().Context()
	nsID := c.Param("nsId")
	infraID := c.Param("infraId")

	var req model.ControlAppReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, model.SimpleMsg{Message: malformedBody})
	}
	if !allowedActions[req.Action] {
		return c.JSON(http.StatusBadRequest, model.SimpleMsg{Message: "Action must be suspend, resume, reboot or terminate"})
	}

	started := time.Now()
	result, err := h.deploy.Control(ctx, nsID, infraID, req.Action)
	h.archiveDirect(ctx, nsID, kindControl, "control_deployment", started,
		map[string]any{"infraId": infraID, "action": req.Action}, result, h.deploy.DryRun(), err)
	if err != nil {
		log.Error().Err(err).Str("nsId", nsID).Str("infraId", infraID).Str("action", req.Action).
			Msg("Failed to control deployment")
		return c.JSON(statusCodeFor(err), model.SimpleMsg{Message: "Deployment control failed"})
	}
	return c.JSON(http.StatusOK, result)
}

// RestDeleteDeployment godoc
// @ID DeleteDeployment
// @Summary Delete a deployment
// @Description Terminate every node of the deployment and remove its records. Shared resources of the namespace are kept
// @Tags [Deploy] AI Application Deployment
// @Produce json
// @Param nsId path string true "Namespace ID" default(default)
// @Param infraId path string true "Deployment ID"
// @Success 200 {object} model.ControlResult "Deployment deleted or planned"
// @Failure 404 {object} model.SimpleMsg "Deployment not found"
// @Failure 500 {object} model.SimpleMsg "Deployment deletion failed"
// @Security BearerAuth
// @Router /ns/{nsId}/deployments/{infraId} [delete]
func (h *Handler) RestDeleteDeployment(c echo.Context) error {
	ctx := c.Request().Context()
	nsID := c.Param("nsId")
	infraID := c.Param("infraId")

	started := time.Now()
	result, err := h.deploy.Delete(ctx, nsID, infraID)
	h.archiveDirect(ctx, nsID, kindControl, "delete_deployment", started,
		map[string]any{"infraId": infraID}, result, h.deploy.DryRun(), err)
	if err != nil {
		log.Error().Err(err).Str("nsId", nsID).Str("infraId", infraID).Msg("Failed to delete deployment")
		return c.JSON(statusCodeFor(err), model.SimpleMsg{Message: "Deployment deletion failed"})
	}
	return c.JSON(http.StatusOK, result)
}

// deployedAppMessage says why the application of a deployment is not known.
func deployedAppMessage(err error) string {
	switch {
	case errors.Is(err, deploy.ErrNoAppLabel):
		return "Deployment carries no ai-mcmp.app-id label; pass appId"
	case errors.Is(err, catalog.ErrNotFound):
		return "AI application not found"
	default:
		return "Deployment lookup failed"
	}
}

// bindDeployReq reads and validates a deployment request.
func bindDeployReq(c echo.Context) (*model.DeployAppReq, error) {
	var req model.DeployAppReq
	if err := c.Bind(&req); err != nil {
		return nil, fmt.Errorf("failed to bind deployment request: %w", err)
	}
	if err := req.Validate(); err != nil {
		return nil, err
	}
	return &req, nil
}
