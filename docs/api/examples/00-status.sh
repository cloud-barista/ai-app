#!/usr/bin/env bash
# Checks that the AI-App service answers and what deployment depends on:
# Semaphore, CB-Tumblebug, the stage folder, dry run and port opening.
#
#   ./00-status.sh
set -euo pipefail
. "$(dirname "$0")/common.sh"

aiapp GET /readyz
aiapp GET /status | jq '{
	dryRun, openPorts,
	semaphore: {configured: .semaphore.configured, reachable: .semaphore.reachable,
		authenticated: .semaphore.authenticated, version: .semaphore.version,
		uiUrl: .semaphore.uiUrl, message: .semaphore.message,
		templates: [.semaphore.templates[]? | "\(.name) exists=\(.exists)"]},
	tumblebug: {url: .tumblebug.url, reachable: .tumblebug.reachable, message: .tumblebug.message},
	stage: .stage}'
