package rest

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/rs/zerolog/log"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/nodedeploy"
)

// RestPostNodeDeployment godoc
// @ID PostNodeDeployment
// @Summary Deploy onto existing nodes
// @Description Deploy an inference service or an uploaded AI application onto nodes CB-Tumblebug already runs. This service does not create VMs.
// @Description
// @Description type inference needs inference: modelId and artifactId from GET /models, and optionally runtimeId (a runtime profile from GET /runtimes the artifact lists; defaults to its first), gpu and args (extra runtime arguments appended after the ones the service sets, such as ["--max-model-len","32768"] for vllm; at most 32, each 1 to 256 characters without control characters, never --model, --port or --host; ollama refuses them). A docker-compose.yml is rendered from the artifact and the runtime profile (preview it with GET /models/{modelId}/artifacts/{artifactId}/compose). type app needs app: name and version (0 takes the latest) from GET /apps. Both take env, written to a .env file next to the deployed files; names match [A-Z_][A-Z0-9_]*, values hold no line break and no single quote.
// @Description
// @Description Each target is {kind: node, nsId, infraId, nodeId}. The cloud, region, zone, address and SSH key are read from the node in CB-Tumblebug (public IP, or private IP when there is none). kind k8s is reserved and answered with 422. When gpu is omitted it is taken from the node spec (acceleratorType gpu); a spec that cannot be read means no GPU, and nodes[].notes says why.
// @Description
// @Description For every node, in parallel up to a bound: the files are placed in <AIAPP_STAGE_ROOT>/<id>/<csp>-<nodeId>/, then Semaphore runs install-docker (with the NVIDIA Container Toolkit on a GPU node) and then compose-up (inference, which waits for the runtime health path from the node itself) or copy-and-run (app, which runs the app script from the folder the files are copied to). On the node the files go to /opt/aiapp/<work key> and run as compose project aiapp-<work key>, where the work key is ms-<runtimeId> for an inference service and app-<name> for an app; nodes[].workKey and nodes[].nodeDir record them. Deploying the same work key to a node again replaces the previous deployment there. The SSH key is registered in Semaphore for the run only and deleted afterwards; it is never part of a record, a log or a response.
// @Description
// @Description Once the playbooks of a node succeed, the ports the deployment serves (nodes[].ports: the runtime port of an inference service; for an app, app.ports or else the ports the app version was uploaded with) are opened over TCP inbound from 0.0.0.0/0 in every CB-Tumblebug security group the node lists (securityGroupIds). Ports a group already lets in are not sent again. nodes[].openedPorts lists, per group and port, whether the rule was added or already there. A port that cannot be opened does not fail the node: nodes[].portsError says why and a note points to it. AIAPP_DEPLOYMENT_OPEN_PORTS=false (true by default) turns port opening off; the ports are then only recorded, with a note. A dry run opens nothing; nodes[].ports still lists the ports it would open.
// @Description
// @Description The request is checked against the catalogs first: a malformed request is 400, an unknown model, artifact, runtime or app is 404, and an artifact the runtime cannot run is 422. An accepted request answers 202 with the record in status pending; follow it with GET /deployments/{id}. With dry run on (AIAPP_TUMBLEBUG_DRY_RUN), only the node look-up and the file placement are done, nothing is registered in Semaphore, and the finished record (status dry-run, with the rendered compose and the node addresses) is returned with 200.
// @Tags [Deployment] Node Deployment
// @Accept json
// @Produce json
// @Param request body model.NodeDeploymentReq true "What to deploy and where"
// @Success 200 {object} model.NodeDeployment "Dry run: the files were placed and nothing was run"
// @Success 202 {object} model.NodeDeployment "Accepted; the deployment goes on in the background"
// @Failure 400 {object} model.SimpleMsg "Malformed request"
// @Failure 404 {object} model.SimpleMsg "Model, artifact, runtime profile or application not found"
// @Failure 422 {object} model.SimpleMsg "A k8s target, or an artifact the runtime cannot run"
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
// @Description List the deployments made through POST /deployments, newest first, with the status of every node. Each filter is optional and matched exactly; all given filters must match. nsId matches a deployment when any of its targets is in that namespace. Task output is left out; read it with GET /deployments/{id}/logs. Records are kept in AIAPP_DEPLOYMENT_STORE_FILE when it is set, and in memory otherwise.
// @Tags [Deployment] Node Deployment
// @Produce json
// @Param type query string false "Deployment type" Enums(inference, app)
// @Param nsId query string false "CB-Tumblebug namespace of any target"
// @Param status query string false "Deployment status" Enums(pending, running, succeeded, failed, partial, dry-run, interrupted, removing, removed, remove-failed)
// @Success 200 {object} model.NodeDeploymentListResp "Deployments, newest first"
// @Security BearerAuth
// @Router /deployments [get]
func (h *Handler) RestGetAllNodeDeployments(c echo.Context) error {
	ctx := c.Request().Context()
	filter := nodedeploy.ListFilter{
		Type:   c.QueryParam("type"),
		NsID:   c.QueryParam("nsId"),
		Status: c.QueryParam("status"),
	}
	return c.JSON(http.StatusOK, model.NodeDeploymentListResp{Deployments: h.nodeDeploy.List(ctx, filter)})
}

// RestGetNodeDeployment godoc
// @ID GetNodeDeployment
// @Summary Get a deployment onto nodes
// @Description Read one deployment: its status (pending, running, succeeded, failed, partial, dry-run, interrupted; removing, removed or remove-failed once taken down with DELETE /deployments/{id}), and for every node its status, how it was reached (csp, region, zone, host, port, user, specId; never the key), whether it got the GPU and why, the stage folder, the rendered compose and service URL of an inference service, and every playbook run with its Semaphore task ID and status. Task output is left out; read it with GET /deployments/{id}/logs.
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

// RestDeleteNodeDeployment godoc
// @ID DeleteNodeDeployment
// @Summary Take a deployment down from its nodes
// @Description Stop and remove the containers of a deployment on every node: Semaphore runs compose-down, which is docker compose -p aiapp-<work key> down --remove-orphans. Named volumes (for example model weights a runtime downloaded) and the node folder /opt/aiapp/<work key> are kept, so that the next deployment of the work key finds them. A node without the project has nothing to take down and succeeds. The ports the deployment opened (nodes[].openedPorts) stay open in the CB-Tumblebug security groups, since other workloads may share them; close them in CB-Tumblebug when needed.
// @Description
// @Description Only the latest deployment of a work key on a node is live there. A node where a later deployment of the same work key replaced this one is not touched, since stopping the project would stop the later deployment; it is marked removed and nodes[].notes names the later deployment. A node of a dry-run deployment, a node where the deployment never started its service (compose-up or copy-and-run never ran), and a node CB-Tumblebug no longer has are marked removed without running anything, each with a note.
// @Description
// @Description When some node needs compose-down, the answer is 202 with the record in status removing and nodes[].status removing; follow it with GET /deployments/{id} until it is removed (every node) or remove-failed (compose-down failed on some node; nodes[].error says why). DELETE again retries the nodes not removed yet. When no node needs compose-down, the finished record (status removed) is returned with 200. A deployment already removed is returned as it is with 200. With dry run on (AIAPP_TUMBLEBUG_DRY_RUN), nothing is sent to Semaphore: nodes that would be taken down keep their status and get a note, and the record is returned with 200. Every removal is archived (kind node-removal).
// @Tags [Deployment] Node Deployment
// @Produce json
// @Param id path string true "Deployment ID"
// @Success 200 {object} model.NodeDeployment "Nothing to run on any node, already removed, or dry run"
// @Success 202 {object} model.NodeDeployment "Accepted; the nodes are taken down in the background"
// @Failure 404 {object} model.SimpleMsg "Deployment not found"
// @Failure 409 {object} model.SimpleMsg "The deployment is still pending or running, or is being taken down"
// @Security BearerAuth
// @Router /deployments/{id} [delete]
func (h *Handler) RestDeleteNodeDeployment(c echo.Context) error {
	ctx := c.Request().Context()
	id := c.Param("id")

	d, err := h.nodeDeploy.Remove(ctx, id)
	if err != nil {
		log.Warn().Err(err).Str("deploymentId", id).Msg("Refused deployment removal")
		return c.JSON(statusCodeFor(err), model.SimpleMsg{Message: messageFor(err, "Deployment removal failed")})
	}
	if d.Status == model.NodeDeployRemoving {
		return c.JSON(http.StatusAccepted, d)
	}
	return c.JSON(http.StatusOK, d)
}
