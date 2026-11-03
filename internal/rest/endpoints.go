package rest

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/ish-hcc/ai-agent-proto/internal/model"
	"github.com/ish-hcc/ai-agent-proto/internal/nodedeploy"
)

// RestGetAllEndpoints godoc
// @ID GetAllEndpoints
// @Summary List the inference services and applications serving now
// @Description List the addresses of what serves on the nodes now, derived from the deployment records of POST /deployments: one endpoint per node and work key (ms-<runtimeId> for an inference service, app-<name> for an app). Each filter is optional and matched exactly; all given filters must match. Endpoints are ordered by nsId, infraId, nodeId and workKey.
// @Description
// @Description On a node, a work key is decided by the latest deployment of it there (by creation time) that is pending or deploying, that started its service (compose-up or copy-and-run ran), or that was taken down after starting it. It has an endpoint only when that deployment is running on the node. So a redeploy replaces the endpoint with the new deploymentId; DELETE /deployments/{id} removes it, and an older deployment of the same work key does not come back, since compose-down stopped the project they share; a later deployment that failed after it started its service leaves no endpoint, since the shared project was recreated and the older containers are gone; a later deployment that failed before that (for example in install-docker) leaves the older endpoint as it was; one still pending or deploying leaves none until it ends. Dry-run deployments never make an endpoint.
// @Description
// @Description An inference endpoint carries runtimeId, modelId, artifactId, url (the OpenAI compatible API, http://<host>:<port><apiBasePath>) and healthUrl. An app endpoint carries name, version and urls, one http://<host>:<port> per port it serves. host is the public IP of the node, or its private IP when it has none. portsOpened is true when every served port was found or added in a CB-Tumblebug security group of the node; otherwise the URLs may not be reachable from outside, and portsError or notes say why.
// @Tags [Deployment] Endpoints
// @Produce json
// @Param nsId query string false "CB-Tumblebug namespace of the node"
// @Param type query string false "Endpoint type" Enums(inference, app)
// @Param infraId query string false "CB-Tumblebug Infra of the node"
// @Param nodeId query string false "Node within the Infra"
// @Success 200 {object} model.EndpointListResp "Endpoints serving now"
// @Security BearerAuth
// @Router /endpoints [get]
func (h *Handler) RestGetAllEndpoints(c echo.Context) error {
	ctx := c.Request().Context()
	filter := nodedeploy.EndpointFilter{
		NsID:    c.QueryParam("nsId"),
		Type:    c.QueryParam("type"),
		InfraID: c.QueryParam("infraId"),
		NodeID:  c.QueryParam("nodeId"),
	}
	return c.JSON(http.StatusOK, model.EndpointListResp{Endpoints: h.nodeDeploy.Endpoints(ctx, filter)})
}
