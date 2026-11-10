#!/usr/bin/env bash
# Deploys an inference service (a runtime serving one model artifact) onto a
# node CB-Tumblebug already runs, and waits until it ends.
#
#   ./03-deploy-inference.sh nsId infraId nodeId [modelId] [artifactId] [runtimeId]
#
#   modelId     default qwen3-vl-8b
#   artifactId  default ollama-gguf
#   runtimeId   default: the first runtime the artifact lists
#   GPU=true|false   optional; omitted, the node spec in CB-Tumblebug decides
#   HF_TOKEN         optional; passed as inference.env.HF_TOKEN for gated
#                    Hugging Face models. It is stored in plain text in the
#                    deployment record and on the node.
#   SSH_USER         optional; targets[].sshUser, the SSH user to run the
#                    playbooks as instead of the CB-Tumblebug one, for
#                    example ec2-user on an Amazon Linux or RHEL node
#   API_KEY          optional, vllm only; inference.apiKey, 16 to 128
#                    characters of [A-Za-z0-9._~-]. Omitted, the node keeps
#                    the key of its previous vllm deployment or gets a new
#                    one. The summary shows only its last 4 characters.
set -euo pipefail
. "$(dirname "$0")/common.sh"

if [ $# -lt 3 ]; then
	sed -n '2,20p' "$0" >&2
	exit 2
fi
ns=$1 infra=$2 node=$3
model="${4:-qwen3-vl-8b}"
artifact="${5:-ollama-gguf}"
runtime="${6:-}"

body=$(jq -n --arg ns "$ns" --arg infra "$infra" --arg node "$node" \
	--arg model "$model" --arg artifact "$artifact" --arg runtime "$runtime" \
	--arg gpu "${GPU:-}" --arg hf "${HF_TOKEN:-}" --arg user "${SSH_USER:-}" --arg key "${API_KEY:-}" '{
	type: "inference",
	targets: [{kind: "node", nsId: $ns, infraId: $infra, nodeId: $node}
		+ (if $user != "" then {sshUser: $user} else {} end)],
	inference: ({modelId: $model, artifactId: $artifact}
		+ (if $runtime != "" then {runtimeId: $runtime} else {} end)
		+ (if $gpu != "" then {gpu: ($gpu == "true")} else {} end)
		+ (if $hf != "" then {env: {HF_TOKEN: $hf}} else {} end)
		+ (if $key != "" then {apiKey: $key} else {} end))}')

record=$(aiapp POST /deployments -H 'Content-Type: application/json' -d "$body")
id=$(jq -r '.id' <<<"$record")
echo "deployment id: $id" >&2
wait_deployment "$id" | summarize_deployment
