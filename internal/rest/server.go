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

	"github.com/ish-hcc/ai-agent-proto/internal/archive"
	"github.com/ish-hcc/ai-agent-proto/internal/catalog"
	"github.com/ish-hcc/ai-agent-proto/internal/config"
	"github.com/ish-hcc/ai-agent-proto/internal/deploy"
	"github.com/ish-hcc/ai-agent-proto/internal/engine"
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

// NewAppServer builds the AI-App service: catalog, deployment, tools, the MCP
// server, the console, and proxies to the automation engine and to the partner
// natural language agent.
func NewAppServer(cfg *config.Config, deps AppDependencies) *Server {
	handler := &Handler{
		catalog: deps.Catalog,
		deploy:  deps.Deploy,
		archive: deps.Archive,
		tools:   deps.Tools,
	}

	// The engine and partner agent readiness are open here as their own are
	// there: the console overview asks for them before anyone has entered a
	// token.
	e := newEcho(cfg.Auth.Token, partnerNLPrefix+"/readyz", enginePrefix+"/readyz")
	g := e.Group(BasePath)
	g.GET("/readyz", RestGetReadyz)
	g.GET("/api/*", echoSwagger.WrapHandler)

	// The operator console, served from this origin so that it can call the
	// routes below without a CORS policy. The page itself is open and sends the
	// bearer token the operator enters on every call it makes.
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
	g.GET("/ns/:nsId/deployments/:infraId/health", handler.RestGetDeploymentHealth)
	g.POST("/ns/:nsId/deployments/:infraId/update", handler.RestPostDeploymentUpdate)
	g.POST("/ns/:nsId/deployments/:infraId/control", handler.RestPostDeploymentControl)
	g.DELETE("/ns/:nsId/deployments/:infraId", handler.RestDeleteDeployment)

	// The tool surface the agent works through.
	g.GET("/tools", handler.RestGetTools)

	// The same tools as an MCP server over Streamable HTTP.
	g.Any("/mcp", echo.WrapHandler(mcpserver.NewHandler(deps.Tools)))

	// The automation engine runs in its own process. The console and operators
	// keep one address, so its routes are forwarded from here with their path,
	// MCP endpoint included.
	g.Any("/engine/*", echo.WrapHandler(newEngineProxy(cfg.Engine.URL)))

	// Natural language instructions are the partner institution's area. Its
	// agent, here a mock, is reached the same way, path unchanged.
	g.Any("/partner/nl/*", echo.WrapHandler(newPartnerNLProxy(cfg.PartnerNL.URL)))

	// Deploy and control archive.
	g.GET("/archive", handler.RestGetArchive)
	g.GET("/archive/:runId", handler.RestGetArchiveRun)

	return &Server{echo: e, port: cfg.Server.Port}
}

// EngineDependencies are the service layer objects the engine routes are built on.
type EngineDependencies struct {
	Engine *engine.Engine
	Card   *engine.CardBuilder
	// Tools are the workflows as MCP tools.
	Tools *tools.Registry
}

// NewEngineServer builds the automation engine service: workflows over REST and
// MCP, the engine run archive and the engine card.
func NewEngineServer(cfg *config.Config, deps EngineDependencies) *Server {
	handler := &Handler{
		engine:     deps.Engine,
		engineCard: deps.Card,
	}

	// The engine readiness is also served under the engine prefix, the path the
	// AI-App service forwards unchanged.
	e := newEcho(cfg.Auth.Token, enginePrefix+"/readyz")
	g := e.Group(BasePath)
	g.GET("/readyz", RestGetReadyz)
	g.GET("/engine/readyz", RestGetReadyz)

	g.POST("/engine/workflows/:workflow", handler.RestPostEngineWorkflow)
	g.GET("/engine/runs", handler.RestGetEngineRuns)
	g.GET("/engine/runs/:runId", handler.RestGetEngineRun)
	g.GET("/engine/card", handler.RestGetEngineCard)

	// The workflows as MCP tools over Streamable HTTP. The namespace comes from
	// the namespace header, as on the AI-App MCP server.
	g.Any("/engine/mcp", echo.WrapHandler(mcpserver.NewNamedHandler(engine.MCPServerName, deps.Tools)))

	return &Server{echo: e, port: cfg.Engine.Port}
}

// newEcho builds an Echo instance that requires token on every route but the
// open ones; see RequireBearer.
func newEcho(token string, extraOpen ...string) *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.Use(middleware.Recover())
	e.Use(middleware.RequestID())
	e.Use(RequireBearer(token, extraOpen...))
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
