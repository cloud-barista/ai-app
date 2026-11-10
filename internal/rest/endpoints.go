package rest

import (
	"net/http"
	"strconv"

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
// @Description Every node with an endpoint is read from CB-Tumblebug on each call, once per node. nodeStatus is the node status CB-Tumblebug gives (Running, Suspended, Terminated, Failed, ...), unknown when CB-Tumblebug could not be read, or not-found when it no longer has the node; the endpoint is listed in every case. available is true only when the node is Running; only then are host and the URLs set, built from the current public IP of the node, or its private IP when it has none. A node that is not Running has no address, since its IP may change when it is resumed.
// @Description
// @Description An inference endpoint carries runtimeId, modelId, artifactId, url (the OpenAI compatible API, http://<host>:<port><apiBasePath>) and healthUrl. An app endpoint carries name, version and urls, one http://<host>:<port> per port it serves. portsOpened is true when every served port was found or added in a CB-Tumblebug security group of the node; otherwise the URLs may not be reachable from outside, and portsError or notes say why.
// @Tags [Deployment] Endpoints
// @Produce json
// @Param nsId query string false "CB-Tumblebug namespace of the node"
// @Param type query string false "Endpoint type" Enums(inference, app)
// @Param infraId query string false "CB-Tumblebug Infra of the node"
// @Param nodeId query string false "Node within the Infra"
// @Param available query bool false "true: only the endpoints whose node is Running, with addresses; false: only the others"
// @Param limit query int false "Most endpoints to return, from 1 to 1000" default(100)
// @Param offset query int false "Endpoints to skip, after the filters and in the order of nsId, infraId, nodeId and workKey, 0 or more. Past the end gives an empty list" default(0)
// @Success 200 {object} model.EndpointListResp "Endpoints serving now, one page of them with total, limit and offset"
// @Failure 400 {object} model.SimpleMsg "available is not a boolean, limit is not from 1 to 1000, or offset is negative"
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
	if raw := c.QueryParam("available"); raw != "" {
		available, err := strconv.ParseBool(raw)
		if err != nil {
			return c.JSON(http.StatusBadRequest, model.SimpleMsg{Message: "available must be true or false"})
		}
		filter.Available = &available
	}
	page, err := pageQuery(c)
	if err != nil {
		return c.JSON(http.StatusBadRequest, model.SimpleMsg{Message: model.ValidationMessage(err)})
	}
	resp := model.EndpointListResp{}
	resp.Endpoints, resp.Page = model.Paginate(h.nodeDeploy.Endpoints(ctx, filter), page)
	return c.JSON(http.StatusOK, resp)
}
