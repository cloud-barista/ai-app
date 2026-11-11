package model

import "regexp"

var deploymentIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func ValidDeploymentID(id string) bool {
	return deploymentIDPattern.MatchString(id)
}
