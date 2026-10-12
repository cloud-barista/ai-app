// Package rest wires the Echo HTTP server and its routes.
package rest

import (
	"context"
	"fmt"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	echoSwagger "github.com/swaggo/echo-swagger"

	_ "github.com/ish-hcc/ai-agent-proto/api/docs"

	"github.com/ish-hcc/ai-agent-proto/internal/agent"
	"github.com/ish-hcc/ai-agent-proto/internal/archive"
	"github.com/ish-hcc/ai-agent-proto/internal/catalog"
	"github.com/ish-hcc/ai-agent-proto/internal/config"
	"github.com/ish-hcc/ai-agent-proto/internal/deploy"
	"github.com/ish-hcc/ai-agent-proto/internal/mcpserver"
	"github.com/ish-hcc/ai-agent-proto/internal/tools"
)

// BasePath is the mount point for every route of this service.
const BasePath = "/aiapp"

// Server owns the Echo instance and its lifecycle.
type Server struct {
	echo *echo.Echo
	port int
}

// AppDependencies are the service layer objects the AI-App routes are built on.
type AppDependencies struct {
	Catalog *catalog.Store
	Deploy  *deploy.Service
	Archive *archive.Store
	Tools   *tools.Registry
}

// AgentDependencies are the service layer objects the agent routes are built on.
type AgentDependencies struct {
	Agent   *agent.Service
	Archive *archive.Store
}

// NewAppServer builds the AI-App service: catalog, deployment, tools, the MCP
// server, the console and a proxy to the automation agent.
func NewAppServer(cfg *config.Config, deps AppDependencies) *Server {
	handler := &Handler{
		catalog: deps.Catalog,
		deploy:  deps.Deploy,
		archive: deps.Archive,
		tools:   deps.Tools,
	}

	e := newEcho()
	g := e.Group(BasePath)
	g.GET("/readyz", RestGetReadyz)
	g.GET("/api/*", echoSwagger.WrapHandler)

	// The operator console, served from this origin so that it can call the
	// routes below without a CORS policy on an API that has no authentication.
	g.GET("/ui", RestGetConsole)

	// AI application catalog: the metadata specification, as a live surface.
	g.GET("/apps", handler.RestGetAllApps)
	g.POST("/apps", handler.RestPostApp)
	g.GET("/apps/search", handler.RestGetAppMatches)
	g.GET("/apps/:appId", handler.RestGetApp)
	g.DELETE("/apps/:appId", handler.RestDeleteApp)
	g.GET("/apps/:appId/specs", handler.RestGetAppSpecs)

	// Deployment and lifecycle, callable without going through the agent.
	g.POST("/ns/:nsId/deployments/plan", handler.RestPostDeploymentPlan)
	g.POST("/ns/:nsId/deployments", handler.RestPostDeployment)
	g.GET("/ns/:nsId/deployments", handler.RestGetAllDeployments)
	g.GET("/ns/:nsId/deployments/:infraId/status", handler.RestGetDeploymentStatus)
	g.GET("/ns/:nsId/deployments/:infraId/accelerator", handler.RestGetDeploymentAccelerator)
	g.POST("/ns/:nsId/deployments/:infraId/control", handler.RestPostDeploymentControl)
	g.DELETE("/ns/:nsId/deployments/:infraId", handler.RestDeleteDeployment)

	// The tool surface the agent works through.
	g.GET("/tools", handler.RestGetTools)

	// The same tools as an MCP server over Streamable HTTP.
	g.Any("/mcp", echo.WrapHandler(mcpserver.NewHandler(deps.Tools)))

	// The automation agent runs in its own process. The console and operators
	// keep one address, so its routes are forwarded from here.
	proxy := echo.WrapHandler(newAgentProxy(cfg.Agent.URL))
	g.POST("/ns/:nsId/intents", proxy)
	g.Any("/agent/*", proxy)

	// Deploy and control archive.
	g.GET("/archive", handler.RestGetArchive)
	g.GET("/archive/:runId", handler.RestGetArchiveRun)

	return &Server{echo: e, port: cfg.Server.Port}
}

// NewAgentServer builds the automation agent service: intents and the archive
// of intent runs.
func NewAgentServer(cfg *config.Config, deps AgentDependencies) *Server {
	handler := &Handler{
		agent:   deps.Agent,
		archive: deps.Archive,
	}

	e := newEcho()
	g := e.Group(BasePath)
	g.GET("/readyz", RestGetReadyz)

	g.POST("/ns/:nsId/intents", handler.RestPostIntent)

	// Intent run archive.
	g.GET("/archive", handler.RestGetArchive)
	g.GET("/archive/:runId", handler.RestGetArchiveRun)

	return &Server{echo: e, port: cfg.Agent.Port}
}

func newEcho() *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.Use(middleware.Recover())
	e.Use(middleware.RequestID())
	return e
}

// Start blocks serving HTTP until the server is shut down.
func (s *Server) Start() error {
	if err := s.echo.Start(fmt.Sprintf(":%d", s.port)); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("http server stopped: %w", err)
	}
	return nil
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	if err := s.echo.Shutdown(ctx); err != nil {
		return fmt.Errorf("failed to shut down http server: %w", err)
	}
	return nil
}

// Port reports the port the server listens on.
func (s *Server) Port() int {
	return s.port
}
