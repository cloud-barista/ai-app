package model

import "time"

// ServiceStatus is what the AI-App service depends on to deploy, as checked
// when it was asked. It holds no credentials.
type ServiceStatus struct {
	// DryRun is true when deployments stop after placing their files and
	// node control is blocked (AIAPP_TUMBLEBUG_DRY_RUN). Semaphore is not
	// called then.
	DryRun bool `json:"dryRun" example:"false"`
	// OpenPorts is true when a deployment opens the ports it serves in the
	// node's CB-Tumblebug security groups (AIAPP_DEPLOYMENT_OPEN_PORTS).
	OpenPorts bool `json:"openPorts" example:"true"`
	// Semaphore runs the deployment playbooks.
	Semaphore SemaphoreStatus `json:"semaphore"`
	// Tumblebug is the CB-Tumblebug the nodes are read from.
	Tumblebug DependencyStatus `json:"tumblebug"`
	// Stage is the folder deployment files are placed in for Semaphore.
	Stage StageStatus `json:"stage"`
	// CheckedAt is when the checks ran.
	CheckedAt time.Time `json:"checkedAt" example:"2026-11-03T12:00:00Z"`
}

// DependencyStatus is whether one service this one calls answered.
type DependencyStatus struct {
	// URL is where it is called, without credentials.
	URL string `json:"url,omitempty" example:"http://cb-tumblebug:1323/tumblebug"`
	// Reachable is whether it answered.
	Reachable bool `json:"reachable" example:"true"`
	// LatencyMs is how long the reachability call took, in milliseconds.
	// Absent when it did not answer.
	LatencyMs *int64 `json:"latencyMs,omitempty" example:"3"`
	// Version is the version it reports, when it reports one.
	Version string `json:"version,omitempty" example:"v2.12.17-5767d93-1741607759"`
	// Message says what was found, or why a check failed.
	Message string `json:"message" example:"CB-Tumblebug is ready"`
}

// SemaphoreStatus is DependencyStatus with what Semaphore holds for this
// service. Nothing is created to find it out: a project or template that does
// not exist yet is created by the first deployment that needs it.
type SemaphoreStatus struct {
	DependencyStatus
	// Configured is false when AIAPP_SEMAPHORE_URL is not set.
	Configured bool `json:"configured" example:"true"`
	// Authenticated is whether the configured token, or user name and
	// password, were accepted.
	Authenticated bool `json:"authenticated" example:"true"`
	// Project is the Semaphore project deployments are registered in
	// (AIAPP_SEMAPHORE_PROJECT).
	Project string `json:"project" example:"aiapp"`
	// ProjectID is the project's ID. Absent when it does not exist yet or
	// could not be read.
	ProjectID int `json:"projectId,omitempty" example:"1"`
	// Templates are the templates of the deployment playbooks, each with
	// whether it exists. Every one reads as missing when the project could
	// not be read.
	Templates []SemaphoreTemplateStatus `json:"templates"`
	// UIURL is the web UI the task links of deployment records point at
	// (AIAPP_SEMAPHORE_UI_URL). Absent when it is not set, and then the
	// records carry no task links.
	UIURL string `json:"uiUrl,omitempty" example:"http://203.0.113.5:3001"`
}

// SemaphoreTemplateStatus says whether the template of one playbook exists.
type SemaphoreTemplateStatus struct {
	Name     string `json:"name" example:"aiapp-compose-up"`
	Playbook string `json:"playbook" example:"compose-up.yml"`
	// Exists is false until the first deployment that runs the playbook.
	Exists bool `json:"exists" example:"true"`
	// ID is the template's ID. Absent when it does not exist.
	ID int `json:"id,omitempty" example:"3"`
}

// StageStatus is whether deployment files can be placed.
type StageStatus struct {
	// Path is AIAPP_STAGE_ROOT.
	Path string `json:"path" example:"/var/lib/aiapp/stage"`
	// Writable is whether a file could be created in it.
	Writable bool `json:"writable" example:"true"`
	// Message says what was found.
	Message string `json:"message" example:"writable"`
}
