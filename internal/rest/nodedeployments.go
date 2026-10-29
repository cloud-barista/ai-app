package rest

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
)

// RestPostNodeDeployment godoc
// @ID PostNodeDeployment
// @Summary Deploy onto existing nodes
// @Description Deploy a model service or an uploaded AI application onto nodes CB-Tumblebug already runs. This service does not create VMs.
// @Description
// @Description type model-service needs modelService: modelId and artifactId from GET /models, and optionally engine (an engine profile the artifact lists; defaults to its first) and gpu. A docker-compose.yml is rendered from the artifact and the engine profile (preview it with GET /models/{modelId}/artifacts/{artifactId}/compose). type app needs app: name and version (0 takes the latest) from GET /apps. Both take env, written to a .env file next to the deployed files; names match [A-Z_][A-Z0-9_]*, values hold no line break and no single quote.
// @Description
// @Description Each target is {kind: node, nsId, infraId, nodeId}. The cloud, region, zone, address and SSH key are read from the node in CB-Tumblebug (public IP, or private IP when there is none). kind k8s is reserved and answered with 422. When gpu is omitted it is taken from the node spec (acceleratorType gpu); a spec that cannot be read means no GPU, and nodes[].notes says why.
// @Description
// @Description For every node, in parallel up to a bound: the files are placed in <AIAPP_STAGE_ROOT>/<id>/<csp>-<nodeId>/, then Semaphore runs install-docker (with the NVIDIA Container Toolkit on a GPU node) and then compose-up (model service, which waits for the engine health path from the node itself) or copy-and-run (app, which runs the app script from the folder the files are copied to). On the node the files go to /opt/aiapp/<work key> and run as compose project aiapp-<work key>, where the work key is ms-<engine> for a model service and app-<name> for an app; nodes[].workKey and nodes[].nodeDir record them. Deploying the same work key to a node again replaces the previous deployment there. The SSH key is registered in Semaphore for the run only and deleted afterwards; it is never part of a record, a log or a response.
// @Description
// @Description The request is checked against the catalogs first: a malformed request is 400, an unknown model, artifact, engine or app is 404, and an artifact the engine cannot run is 422. An accepted request answers 202 with the record in status pending; follow it with GET /deployments/{id}. With dry run on (AIAPP_TUMBLEBUG_DRY_RUN), only the node look-up and the file placement are done, nothing is registered in Semaphore, and the finished record (status dry-run, with the rendered compose and the node addresses) is returned with 200.
// @Tags [Deployment] Node Deployment
// @Accept json
// @Produce json
// @Param request body model.NodeDeploymentReq true "What to deploy and where"
// @Success 200 {object} model.NodeDeployment "Dry run: the files were placed and nothing was run"
// @Success 202 {object} model.NodeDeployment "Accepted; the deployment goes on in the background"
// @Failure 400 {object} model.SimpleMsg "Malformed request"
// @Failure 404 {object} model.SimpleMsg "Model, artifact, engine profile or application not found"
// @Failure 422 {object} model.SimpleMsg "A k8s target, or an artifact the engine cannot run"
// @Failure 500 {object} model.SimpleMsg "The record could not be written"
// @Security BearerAuth
// @Router /deployments [post]
func (h *Handler) RestPostNodeDeployment(c echo.Context) error {
	ctx := c.Request().Context()

	var req model.NodeDeploymentReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, model.SimpleMsg{Message: malformedBody})
	}

	d, err := h.nodeDeploy.Create(ctx, req)
	if err != nil {
		log.Warn().Err(err).Str("type", req.Type).Msg("Refused node deployment")
		return c.JSON(statusCodeFor(err), model.SimpleMsg{Message: messageFor(err, "Deployment failed to start")})
	}
	if d.DryRun {
		return c.JSON(http.StatusOK, d)
	}
	return c.JSON(http.StatusAccepted, d)
}

// RestGetAllNodeDeployments godoc
// @ID GetAllNodeDeployments
// @Summary List deployments onto nodes
// @Description List every deployment made through POST /deployments, newest first, with the status of every node. Task output is left out; read it with GET /deployments/{id}/logs. Records are kept in AIAPP_DEPLOYMENT_STORE_FILE when it is set, and in memory otherwise.
// @Tags [Deployment] Node Deployment
// @Produce json
// @Success 200 {object} model.NodeDeploymentListResp "Deployments, newest first"
// @Security BearerAuth
// @Router /deployments [get]
func (h *Handler) RestGetAllNodeDeployments(c echo.Context) error {
	ctx := c.Request().Context()
	return c.JSON(http.StatusOK, model.NodeDeploymentListResp{Deployments: h.nodeDeploy.List(ctx)})
}

// RestGetNodeDeployment godoc
// @ID GetNodeDeployment
// @Summary Get a deployment onto nodes
// @Description Read one deployment: its status (pending, running, succeeded, failed, partial, dry-run, interrupted), and for every node its status, how it was reached (csp, region, zone, host, port, user, specId; never the key), whether it got the GPU and why, the stage folder, the rendered compose and service URL of a model service, and every playbook run with its Semaphore task ID and status. Task output is left out; read it with GET /deployments/{id}/logs.
// @Tags [Deployment] Node Deployment
// @Produce json
// @Param id path string true "Deployment ID"
// @Success 200 {object} model.NodeDeployment "Deployment record"
// @Failure 404 {object} model.SimpleMsg "Deployment not found"
// @Security BearerAuth
// @Router /deployments/{id} [get]
func (h *Handler) RestGetNodeDeployment(c echo.Context) error {
	ctx := c.Request().Context()
	id := c.Param("id")

	d, err := h.nodeDeploy.Get(ctx, id)
	if err != nil {
		return c.JSON(statusCodeFor(err), model.SimpleMsg{Message: messageFor(err, "Deployment lookup failed")})
	}
	return c.JSON(http.StatusOK, d)
}

// RestGetNodeDeploymentLogs godoc
// @ID GetNodeDeploymentLogs
// @Summary Get the task output of a deployment
// @Description Read the Semaphore task log of every playbook run on every node of a deployment, one line per entry with colour codes removed. A step's output appears when its task ends. The tasks and their logs are also kept in Semaphore under the template of each playbook.
// @Tags [Deployment] Node Deployment
// @Produce json
// @Param id path string true "Deployment ID"
// @Success 200 {object} model.NodeDeploymentLogs "Task output per node and step"
// @Failure 404 {object} model.SimpleMsg "Deployment not found"
// @Security BearerAuth
// @Router /deployments/{id}/logs [get]
func (h *Handler) RestGetNodeDeploymentLogs(c echo.Context) error {
	ctx := c.Request().Context()
	id := c.Param("id")

	logs, err := h.nodeDeploy.Logs(ctx, id)
	if err != nil {
		return c.JSON(statusCodeFor(err), model.SimpleMsg{Message: messageFor(err, "Deployment lookup failed")})
	}
	return c.JSON(http.StatusOK, logs)
}

// RestPostNodeDeploymentControl godoc
// @ID PostNodeDeploymentControl
// @Summary Control the nodes of a deployment
// @Description Apply a CB-Tumblebug lifecycle action (suspend, resume, reboot or terminate) to every node of a deployment, one call per node. A node CB-Tumblebug refuses is reported failed and the others still get the action. Nothing is sent, and every node is reported blocked, when dry run is on or the deployment was a dry run. Every call is archived (kind node-control).
// @Tags [Deployment] Node Deployment
// @Accept json
// @Produce json
// @Param id path string true "Deployment ID"
// @Param request body model.NodeControlReq true "Lifecycle action"
// @Success 200 {object} model.NodeControlResp "Per node result"
// @Failure 400 {object} model.SimpleMsg "Unknown action"
// @Failure 404 {object} model.SimpleMsg "Deployment not found"
// @Security BearerAuth
// @Router /deployments/{id}/control [post]
func (h *Handler) RestPostNodeDeploymentControl(c echo.Context) error {
	ctx := c.Request().Context()
	id := c.Param("id")

	var req model.NodeControlReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, model.SimpleMsg{Message: malformedBody})
	}
	resp, err := h.nodeDeploy.Control(ctx, id, req.Action)
	if err != nil {
		log.Warn().Err(err).Str("deploymentId", id).Str("action", req.Action).Msg("Failed to control deployment nodes")
		return c.JSON(statusCodeFor(err), model.SimpleMsg{Message: messageFor(err, "Node control failed")})
	}
	return c.JSON(http.StatusOK, resp)
}
