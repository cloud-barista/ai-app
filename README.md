<p align="left"><b>English</b> | <a href="README.ko.md">한국어</a></p>

# AI-App

**Deploy AI models and AI applications onto multi-cloud VMs that CB-Tumblebug already runs, and find out where they serve.**

AI-App is a Go service from the AI-MCMP project (Cloud-Barista). It keeps a catalog of AI models and AI applications,
installs them on CB-Tumblebug nodes over SSH through Semaphore, opens their serving ports, and tells you their URLs.
An automation engine on top runs fixed deploy, heal, control and status workflows through the same API, without an LLM.

> Status: **prototype.** The API is shared with partner institutions and has been run on real GPU VMs on AWS, Azure and GCP,
> but it is not production hardened (see [Limitations](#limitations)).

- [What it does](#what-it-does)
- [Architecture](#architecture)
- [Quick start](#quick-start)
- [Using it with CB-Tumblebug](#using-it-with-cb-tumblebug)
- [How partner institutions use it](#how-partner-institutions-use-it)
- [API](#api)
- [Documentation](#documentation)
- [What works today](#what-works-today)
- [Test results](#test-results)
- [Limitations](#limitations)
- [Roadmap](#roadmap)
- [Configuration](#configuration)
- [Development](#development)
- [License](#license)

## What it does

| You want to | AI-App gives you |
|---|---|
| Know what can be deployed | A **model catalog** (models, their artifacts per source and format, compatible runtimes), synced daily from Hugging Face and Ollama (Docker Model Runner, NGC and Kaggle can be turned on), plus models you register yourself |
| Serve a model | `POST /aiapp/deployments` with `type: inference`: a runtime (`vllm` or `ollama`) serving one model artifact on a node, exposing an OpenAI compatible API |
| Run an AI application | Upload files and a `deploy.sh` once (`POST /aiapp/apps`, versioned), then deploy it with `type: app`. Examples for Open WebUI, Langflow and Dify are included |
| Know where things serve | `GET /aiapp/endpoints` returns the URL of every inference service and application running now |
| Follow a deployment | `GET /aiapp/deployments/{id}/progress` (poll until `done`), per node logs, and links to the Semaphore task screens |
| Take it down | `DELETE /aiapp/deployments/{id}` runs `docker compose down` on the nodes (model weights in named volumes stay) |
| Control the VMs | suspend, resume, reboot, terminate the nodes of a deployment through CB-Tumblebug |
| Automate | The automation engine runs `deploy`, `heal` (redeploy once on failure), `control` and `status` workflows, over REST or as MCP tools |
| Audit | Every deployment, removal, node control and engine run is kept as a JSON Lines record |

What AI-App does **not** do:

- **It does not create VMs.** Create the infra with CB-Tumblebug (directly or through an infra recommender) and hand AI-App the `nsId` / `infraId` / `nodeId`.
- It does not recommend models or infrastructure. Recommendation routes exist as a placeholder for a partner implementation and answer `501` today.
- It does not proxy model traffic. Clients call the node directly (`http://<node>:8000/v1`); AI-App only tells them the address and, for vLLM, the API key.

## Architecture

```
  Clients: AI-Web portal, scripts, operator console, MCP clients, partner NL agent
                │ REST /aiapp/*            │ MCP /aiapp/engine/mcp
                ▼                          ▼
  ┌───────────────────────────┐   ┌──────────────────────────────┐
  │ ai-app-server      :8090  │◀──│ ai-automation-engine  :8091  │
  │ catalog, apps, deploy,    │MCP│ deploy / heal / control /    │
  │ endpoints, dry-run gate,  │   │ status workflows (no LLM)    │
  │ console, Swagger, archive │   └──────────────────────────────┘
  └───────────┬───────────────┘
              │ node info, SSH key, spec,           │ register key + inventory,
              │ security group rules, node control  │ run playbook, read logs
              ▼                                     ▼
       CB-Tumblebug REST                    Semaphore UI ──SSH──▶ node
                                                     install-docker
                                                     compose-up    (inference: runtime + model)
                                                     copy-and-run  (app: files + deploy.sh)
                                                     compose-down  (remove)
```

| Process | Port | Role |
|---|---|---|
| `ai-app-server` (`cmd/ai-app-server`) | `8090` | The API, the MCP server of 13 tools (`/aiapp/mcp`), the operator console (`/aiapp/ui`), Swagger (`/aiapp/api/index.html`) |
| `ai-automation-engine` (`cmd/ai-automation-engine`) | `8091` | Fixed workflows that call AI-App tools over MCP. Its own MCP server (`/aiapp/engine/mcp`) exposes 5 tools. AI-App forwards `/aiapp/engine/*` to it, so clients need only port `8090` |
| Semaphore UI `v2.12.17` (external, optional) | `3001` | Runs the Ansible playbooks in [`deploy/ansible`](deploy/ansible) on the nodes. Needed only for real deployments |
| `mock-partner-recommender`, `mock-partner-nl-agent` (`mock/partner/`) | `8092`, `8093` | **Mock implementations** of partner owned parts (infra recommendation, natural language agent), kept to test the integration contracts. Not part of AI-App |

**Safety by default:** `AIAPP_TUMBLEBUG_DRY_RUN=true` is the default. A deployment then reads the node and stages its files but
sends nothing to Semaphore, and node control is blocked. The gate is a service setting, not a request parameter, so no caller
(engine, MCP client, LLM agent) can bypass it.

## Quick start

### 1. Run it locally with no configuration (no cloud needed)

Requires Go 1.26 or later.

```sh
git clone https://github.com/cloud-barista/ai-app.git
cd ai-app
make run-app        # AI-App, :8090
make run-engine     # automation engine, :8091 (second terminal, optional)
```

Every setting has a default, so no env file is needed. Everything that does not touch a cloud works:

```sh
curl localhost:8090/aiapp/readyz                    # {"message":"Service is ready"}
curl localhost:8090/aiapp/runtimes                  # ollama, vllm
curl localhost:8090/aiapp/models                    # 3 built-in models + synced ones
curl localhost:8090/aiapp/models/qwen3-vl-8b/artifacts/ollama-gguf/compose   # the compose file a node would get
curl -F name=open-webui -F script=@examples/apps/open-webui/deploy.sh \
     -F files=@examples/apps/open-webui/docker-compose.yml -F ports=3000 \
     localhost:8090/aiapp/apps                      # upload an application (again = version 2)
curl localhost:8090/aiapp/engine/card               # the engine, forwarded through :8090
```

- Operator console: <http://localhost:8090/aiapp/ui>
- Swagger UI: <http://localhost:8090/aiapp/api/index.html>

Without a reachable CB-Tumblebug a deployment request answers `502` (`CB-Tumblebug could not be read`); that is expected.

### 2. Run it with Docker

```sh
make docker-up      # docker compose up -d --build: AI-App, engine and the two mocks
make docker-logs
make docker-down
```

Catalog, uploaded apps, deployment records and archives live in named volumes and survive container rebuilds.
CB-Tumblebug is **not** included in the compose file; the containers look for it at `http://host.docker.internal:1323/tumblebug`.

### 3. Deploy to real nodes

1. Have CB-Tumblebug running with an infra whose nodes meet the [node requirements](#node-requirements).
2. Configure and start AI-App with Semaphore:

   ```sh
   cp .env.example .env
   # in .env set at least:
   #   AIAPP_TUMBLEBUG_BASE_URL=http://<tumblebug-host>:1323/tumblebug
   #   AIAPP_TUMBLEBUG_USERNAME=... / AIAPP_TUMBLEBUG_PASSWORD=...
   #   AIAPP_SEMAPHORE_PASSWORD=<any password for the bundled Semaphore admin>
   #   AIAPP_TUMBLEBUG_DRY_RUN=false        # this makes deployments change real nodes
   #   AIAPP_AUTH_TOKEN=<token>             # recommended once the port is reachable by others
   docker compose --profile deploy up -d
   curl localhost:8090/aiapp/status         # Semaphore, CB-Tumblebug, stage folder, dry run
   ```

3. Deploy with the example scripts in [`docs/api/examples`](docs/api/examples) (they need `curl` and `jq`):

   ```sh
   export AIAPP_URL=http://localhost:8090/aiapp AIAPP_AUTH_TOKEN=<token>
   cd docs/api/examples
   ./03-deploy-inference.sh <nsId> <infraId> <nodeId> qwen3-vl-8b ollama-gguf     # Ollama + Qwen3-VL-8B, waits until done
   ./02-upload-app.sh ../../../examples/apps/open-webui                          # upload Open WebUI
   ./04-deploy-app.sh <nsId> <infraId> <nodeId> open-webui OLLAMA_BASE_URL=http://<node-ip>:11434
   ./05-endpoints.sh                                                             # URLs of what is serving now
   ./07-remove.sh <deploymentId>                                                 # take it down
   ```

   Or call the API directly:

   ```sh
   curl -X POST localhost:8090/aiapp/deployments -H "Authorization: Bearer $AIAPP_AUTH_TOKEN" \
        -H 'Content-Type: application/json' -d '{
          "type": "inference",
          "targets": [{"kind": "node", "nsId": "<nsId>", "infraId": "<infraId>", "nodeId": "<nodeId>"}],
          "inference": {"modelId": "qwen3-vl-8b", "artifactId": "ollama-gguf"}
        }'
   # 202 + {"id": "...", "status": "pending", ...}; then poll GET /aiapp/deployments/{id}/progress
   ```

Running without Docker is also possible (`cp conf/template-setup.env conf/setup.env`, then `make run-app`), but then Semaphore
must see `deploy/ansible` and the same stage directory (`AIAPP_STAGE_ROOT`) as AI-App. The `deploy` compose profile sets this up for you.

## Using it with CB-Tumblebug

AI-App is a consumer of CB-Tumblebug. It was tested with **CB-Tumblebug 0.13.8** (cb-spider 0.13.20).

```
 you / infra recommender ──▶ CB-Tumblebug: create infra (GPU VMs)        ← not AI-App
                                    │ nsId / infraId / nodeId
                                    ▼
 you / AI-Web / engine ────▶ AI-App: POST /aiapp/deployments
                                    1. read the node, its SSH key and spec from CB-Tumblebug
                                    2. stage the compose file or app files
                                    3. Semaphore runs install-docker, then compose-up or copy-and-run over SSH
                                    4. open the serving port on the node's security groups in CB-Tumblebug
                                    ▼
 clients ──────────────────▶ http://<node-ip>:<port>   (from GET /aiapp/endpoints)
```

What AI-App calls in CB-Tumblebug:

| Purpose | When |
|---|---|
| Read a node (IP, `securityGroupIds`, `specId`, image OS, SSH user) and its private key | Every deployment and removal |
| Read the node spec in namespace `system` (`AIAPP_TUMBLEBUG_SPEC_NAMESPACE`) for `acceleratorType` | To decide GPU, unless the request sets `inference.gpu` |
| Add inbound TCP rules (`0.0.0.0/0`) to the node's security groups | After a successful deployment, only for ports not already open |
| Node control (suspend, resume, reboot, terminate) | `POST /aiapp/deployments/{id}/control` |

Settings: `AIAPP_TUMBLEBUG_BASE_URL`, `AIAPP_TUMBLEBUG_USERNAME` / `_PASSWORD`, optional `AIAPP_TUMBLEBUG_CREDENTIAL_HOLDER`.
The startup log prints the base URL and whether it came from the environment or the default.

### Node requirements

| Requirement | Detail |
|---|---|
| Created by CB-Tumblebug | AI-App addresses a node by `nsId` / `infraId` / `nodeId` and uses the SSH key CB-Tumblebug holds |
| Linux distribution | Ubuntu, Debian, RHEL, Rocky, AlmaLinux, CentOS Stream, Oracle Linux, Fedora, Amazon Linux 2023. Docker and the compose plugin are installed if missing |
| GPU | NVIDIA only. **The driver must be in the image and be 550 or newer** (Ollama 0.40.13 needs it). AI-App installs the NVIDIA Container Toolkit. A GPU deployment whose runtime ends up on CPU fails |
| Security groups | The node info must list `securityGroupIds`, otherwise ports are not opened (the deployment still succeeds) |
| Outbound internet | The node pulls images and models directly from Docker Hub, ghcr.io, Hugging Face, the Ollama registry and GitHub |
| SSH user | Nothing to set. If the node refuses CB-Tumblebug's user (`cb-user` is missing on some AWS RHEL family images), AI-App tries the image's default user (for example `ec2-user`). Override with `targets[].sshUser` |

Known CB-Tumblebug notes from real runs (details in [docs/api/etri.md](docs/api/etri.md)): GCP GPU stock differs per zone; when retrying
in another zone, use a new node group name, because CB-Tumblebug reuses the first zone's security group and SSH gets blocked.

## How partner institutions use it

| Who | What they do with AI-App | Guide |
|---|---|---|
| Infra provider (ETRI) | Creates GPU VMs with CB-Tumblebug, then deploys runtimes, models and apps with `POST /aiapp/deployments`; the Travel Planner portal reads `GET /aiapp/endpoints` | [docs/api/etri.md](docs/api/etri.md) |
| AI-Web portal (BESP) | Reads the catalog, requests deployments, shows progress and endpoints | [docs/api/besp.md](docs/api/besp.md) |
| Model and app recommendation (Kyung Hee University) | Implements recommenders behind `internal/recommend`; AI-App exposes the catalog metadata unchanged as input | [docs/api/khu.md](docs/api/khu.md) |
| Natural language agent (Kyung Hee University, AI-Ops) | Uses the existing `/aiapp/partner/nl/*` proxy and engine MCP. AI-Ops manages OpsLLM routing, Registry, permissions and approvals; the original mock remains in `mock/partner/nlagent` | [KHU implementation](docs/api/khu-implementation.md) |
| Infra recommender (Kyung Hee University, AI-App) | Adds `cmd/khu-infra-recommender` behind the existing `/aiapp/partner/rec/*` proxy for CB-Tumblebug spec and image recommendations. Select it with `AIAPP_PARTNER_REC_URL`; the original mock remains in place | [Infra recommendation](docs/api/infra-recommendation.md) |
| Approved deployment and updates (Kyung Hee University, AI-App) | Connects approved revisions to the existing engine and deployment tools through a separate authenticated MCP endpoint | [Approved deployment](docs/api/approved-deployment.md) |
| Inference observation and optimization (Kyung Hee University, AI-App) | Joins approved deployment and engine records to vLLM/DCGM metrics, then predicts configuration changes. AI-Ops reviews and approves published proposals | [Observation](docs/api/inference-observation.md), [Optimization](docs/api/inference-optimization.md) |

Two API layers, two kinds of callers:

| Layer | Address | Auth |
|---|---|---|
| Management API | `http://<ai-app-host>:8090/aiapp/...` | `Authorization: Bearer <AIAPP_AUTH_TOKEN>` (readiness, console and Swagger are open) |
| Inference service | vLLM `http://<node>:8000/v1`, Ollama `http://<node>:11434` (OpenAI compatible under `/v1`) | vLLM: `Authorization: Bearer <apiKey>` on `/v1/*` (401 without it; `/health` is open). The key is per deployment, given as `inference.apiKey` or generated, kept on redeploy, and read from `GET /aiapp/endpoints`. Ollama: none, it has no API key support |
| Application | `http://<node>:<port>` (Dify `80`, Open WebUI `3000`, Langflow `7860`) | the application's own login |

## API

All paths are under `/aiapp`. Field level documentation is in Swagger (`/aiapp/api/index.html`, file [`api/docs/swagger.yaml`](api/docs/swagger.yaml)).

| Area | Routes |
|---|---|
| Status | `GET /readyz`, `GET /status` |
| Catalog | `GET /runtimes`, `GET /runtimes/{runtimeId}`, `GET/POST /models`, `GET/DELETE /models/{modelId}`, `GET /models/{modelId}/artifacts/{artifactId}/compose`, `GET/POST /catalog/sync` |
| Applications | `GET/POST /apps`, `GET/DELETE /apps/{name}`, `GET /apps/{name}/versions/{version}` |
| Deployments | `GET/POST /deployments`, `GET/DELETE /deployments/{id}`, `GET /deployments/{id}/progress`, `GET /deployments/{id}/logs`, `POST /deployments/{id}/control` |
| Endpoints | `GET /endpoints` (filters `nsId`, `infraId`, `nodeId`, `type`, `available`). Reads each node from CB-Tumblebug on every call: URLs use the node's current IP and are given only while it is `Running`. vLLM entries carry `apiKey` |
| Recommendation (placeholder) | `POST /recommendations/models`, `POST /recommendations/apps` (`501` until an implementation is registered) |
| Tools and audit | `GET /tools`, `/mcp` (MCP, Streamable HTTP), `GET /archive`, `GET /archive/{runId}` |
| Automation engine (forwarded) | `POST /engine/workflows/{deploy\|heal\|control\|status}`, `GET /engine/runs`, `GET /engine/runs/{runId}`, `GET /engine/card`, `/engine/mcp` |

Every list route (`GET /models`, `/apps`, `/deployments`, `/endpoints`, `/archive`, `/engine/runs`) and the MCP list tools take `limit` (default 100, 1 to 1000) and `offset` (default 0),
and answer with `total`, the count after the filters. A limit out of range or a negative offset is `400`. See the [API guide](docs/api/README.md) section 9-0.

Deployment states: `pending`, `deploying`, `running`, `failed`, `partial`, `dry-run`, `interrupted`, `removing`, `removed`, `remove-failed`.
A node becomes `running` only when the runtime's health path answers (and, on GPU nodes, the runtime is actually on the GPU),
or for an app when its ports answer HTTP and its containers stay up without restarts.

## Documentation

The documents under `docs/` are written in Korean.

| Document | For | Contents |
|---|---|---|
| [docs/api/README.md](docs/api/README.md) | Developers calling the API | Call order, auth, errors, async deployment and progress, ports, full API list, request shapes, example apps with screenshots |
| [docs/api/khu-implementation.md](docs/api/khu-implementation.md) | Maintainers and partner developers | KHU additions, existing Innogrid features reused, AI-App/AI-Ops responsibilities, integration paths and verification scope |
| [docs/api/gpu-installation.md](docs/api/gpu-installation.md) | Operators | Host setup, GPU deployment, AI-Ops integration, test scripts and cleanup |
| [docs/api/inference-optimization-v3.md](docs/api/inference-optimization-v3.md) | Optimization developers | Candidate predictions using operational context, grouped chronological evaluation and the existing MCP approval path |
| [docs/api/inference-optimization-v4.md](docs/api/inference-optimization-v4.md) | Optimization developers | Validation policy outcomes for RF/GP and input selection, qualification and live comparison |
| [docs/api/inference-optimization-v5.md](docs/api/inference-optimization-v5.md) | Optimization developers | Measured-point KNN as a validation candidate, preserved guards and fresh verification scope |
| [docs/api/etri.md](docs/api/etri.md) | Infra provider | VM requirements, tested images per CSP, deploying the three node travel scenario |
| [docs/api/besp.md](docs/api/besp.md) | Portal developers | Screens and calls for catalog, deployment, progress and endpoints |
| [docs/api/khu.md](docs/api/khu.md) | Recommendation implementers | The `internal/recommend` interface and how to register an implementation |
| [docs/api/live-test-2026-11-03.md](docs/api/live-test-2026-11-03.md) | Everyone | Results of the run on real GPU VMs |
| [docs/api/examples/](docs/api/examples) | Everyone | Runnable curl scripts `00` to `07` |
| [docs/design/model-catalog-and-node-deploy.md](docs/design/model-catalog-and-node-deploy.md) | Maintainers | Data model and deployment design, open problems |
| [examples/apps/](examples/apps) | App authors | Open WebUI, Langflow, Dify bundles to upload |
| Swagger (`/aiapp/api/index.html`) | Developers | Every route and field |

## What works today

| Area | State |
|---|---|
| Runtimes | `vllm` (`vllm/vllm-openai:v0.32.0`, Hugging Face safetensors) and `ollama` (`ollama/ollama:0.40.13`, GGUF) |
| Built-in models | `qwen3-embedding-0.6b` (embedding), `qwen3-vl-8b` (vision chat), `ministral-3-8b` (chat), each with the runtime arguments it needs |
| Catalog sync | Hugging Face and Ollama by default. Docker Model Runner, NGC and Kaggle are off by default because no runtime serves their artifacts yet; turned on, their artifacts are listed with `deployable: false` and deploying them answers `422`. Turning a source off again deletes its models at the end of the next run |
| Example apps | Open WebUI `v0.12.4`, Langflow `1.13.0`, Dify `1.18.0` |
| Clouds | Any CSP CB-Tumblebug supports, since AI-App only talks to CB-Tumblebug and SSH. Run on real VMs on **AWS** (T4, L4), **Azure** (T4) and **GCP** (L4), including RHEL 9, Amazon Linux 2023 and AlmaLinux 9 nodes |
| Integrations | CB-Tumblebug 0.13.8, Semaphore UI v2.12.17, MCP (Streamable HTTP) for the 13 AI-App tools and 5 engine tools, an agent registry (`POST {registry}/agents`) for the engine card |

On 2026-11-03 the three node travel scenario (vLLM embedding + Dify, Ollama Qwen3-VL + Open WebUI, vLLM Ministral + Langflow)
was deployed with the API alone and every service answered from outside. See [the test report](docs/api/live-test-2026-11-03.md).

## Test results

### 2026-11-11 GPU deployment and AI-Ops integration

The test used an AWS `g4dn.xlarge` in Seoul with a Tesla T4 GPU and Ubuntu 22.04. Ollama served `qwen3-vl:8b`, which also acted as the self-hosted OpsLLM for AI-Ops. Deployment used the existing AI-App API and Semaphore.

| Test | Observed result |
|---|---|
| Deployment through Semaphore | After selecting the built-in Ansible callback in Compose, installation and Compose tasks completed and the inference deployment reached `running`. |
| GPU execution | The runtime checks passed and logs reported Tesla T4 and CUDA. |
| Model response | `POST /api/generate` returned HTTP `200`, answer `4` for `2 + 2`, and `done: true`. |
| AI-Ops integration | Natural-language catalog and deployment-status queries succeeded through port `8090`. OpsLLM and engine run IDs were linked; MCP/REST record parity and restart recovery passed. |
| Automated checks | `go test ./...`, `go vet ./...` and nine mocked HTTP regression tests for `scripts/gpu-test.sh` passed. The script's `status` and `test` commands also passed against the real node. |
| Cleanup | AWS reported the GPU VM as `terminated`; the root EBS volume was deleted and the test Infra disappeared from CB-Tumblebug. |

See the [installation and test guide](docs/api/gpu-installation.md) and [KHU implementation record](docs/api/khu-implementation.md) for reproduction steps and scope. Environment-specific run IDs and raw results remain in local records excluded from Git.

This run did not verify live approved-revision deployment or updates, vLLM/DCGM optimization gains, external subscription or open-weights providers, a clean-host installation, or the new script's live `deploy` command. The test VM is terminated; another run requires a prepared node and updated LLM endpoint configuration.

### 2026-11-11 approved deployment and inference optimization

A second GPU VM ran Qwen3-1.7B in FP16 with vLLM `0.32.0` on a Tesla T4. AI-Ops submitted approved revisions through the registered AI-App MCP server; AI-App deployed the runtime, model and a proxy application through its existing deployment service and Semaphore. Runtime setting changes redeployed both services. Repeating the initial request after an engine timeout preserved its deployment ID and completed successfully.

The study used four fixed, self-authored prompts, `temperature=0`, up to 64 output tokens, two offered request rates and `--max-num-seqs` values of 1 and 8. Five repetitions per combination produced 20 measurement windows, each with 120 seconds of offered load followed by request draining. The observation MCP joined actual inference/application deployments, engine runs and revisions to vLLM and DCGM metrics.

| Offered requests/s | `max-num-seqs` | Mean server p95 (s) | Mean client SLO goodput (requests/s) | Mean client failure rate |
|---|---|---|---|---|
| 0.3 | 1 | 1.975 | 0.298 | 0% |
| 0.3 | 8 | 1.975 | 0.296 | 0% |
| 0.7 | 1 | 14.438 | 0.022 | 61.19% |
| 0.7 | 8 | 1.990 | 0.678 | 0% |

Server p95 comes from the vLLM histogram. Client SLO goodput counts responses completed through the application within 2.91 seconds, divided by the measurement duration including draining. Peak GPU memory was 11,932 MiB for all four combinations. These results describe this small synthetic workload.

The Random Forest used repetitions 1–3 for training, 4 for validation and 5 for the final test. It did not pass the holdout gate: both final baseline windows fell outside the training bounds, and no update qualified. The low-load rejection included a floating-point boundary difference. The artifact remains in shadow mode; a qualified model proposal's live application and post-application comparison remain unverified. The measured setting change above was an explicitly approved experimental change.

A fresh baseline observation produced a published `ABSTAIN` proposal. AI-Ops returned `eligible: false`, rejected its approval with HTTP `409` and created no next revision. This verified the live rejection path.

Both test deployments reached `removed`. AWS instance termination, root EBS deletion and CB-Tumblebug Infra removal were verified. The test Prometheus container and SSH tunnel were stopped.

The [optimization contract](docs/api/inference-optimization.md) describes training, publication and approval. The experiment driver, prompts, measurement datasets, trained artifacts and plots remain in local directories excluded from Git.

### 2026-11-11 inference optimization v3 measurements

The expanded study used the same T4, Qwen3-1.7B FP16 and vLLM 0.32.0. It measured 18 conditions over five chronological repetitions, for 90 windows. Each window offered requests for 120 seconds and then drained outstanding requests. Setting order alternated between repetitions. Inputs came from eight author-written topics.

| Input and load | Input tokens | Output limit | Mean requests/s | `max-num-seqs` |
|---|---|---|---|---|
| Short, steady | 28–30 | 64 | 0.4, 1.2 | 2, 4, 8 |
| Medium, steady | 235–237 | 48 | 0.8, 1.2 | 2, 8 |
| Long, steady | 439–441 | 64 | 0.4, 1.2 | 2, 8 |
| Medium, bursty | 235–237 | 48 | 0.8, 1.2 | 2, 8 |

Bursty load sent requests at twice the mean rate for ten seconds, followed by ten idle seconds. All windows passed deployment, engine Run, Revision, node, GPU UUID and metric checks, including measured KV cache usage. Repetitions 1–3 supplied 54 training windows and 30 paired candidate rows; repetition 4 supplied 18 validation windows. Validation selected the input set containing token lengths, queue, GPU utilization, free VRAM and KV cache usage. Repetition 5 supplied 18 final test windows. Model source and operational limits were frozen before collection. [v3 implementation and commands](docs/api/inference-optimization-v3.md)

The following is an offline policy evaluation on measured data. Each of eight workload/rate contexts starts from setting 2 over a one-hour decision horizon, with a 300-second downtime budget deducted for each update. This also charges the fixed-setting policy separately in every context.

| Policy | Mean downtime-adjusted goodput, requests/s | UPDATE | ABSTAIN | Operational-limit violations |
|---|---|---|---|---|
| Keep baseline | 0.49099 | 0 | 0 | 4 |
| Measured lookup | 0.84951 | 3 | 1 | 1 |
| Random Forest v3 | 0.84951 | 3 | 2 | 1 |
| Fixed setting 8, selected on training data | 0.81313 | 8 | 0 | 0 |

The RF passed the holdout gate, with zero unsafe or weak updates among its three updates. Adjusted goodput was 73.02% above keeping the baseline and equal to measured lookup; the study did not establish an advantage over lookup. Limits were a 2.91-second server latency estimate, 2% errors and 14,592 MiB VRAM. One violation remained under the RF policy, while fixed setting 8 had none. Both throughput and limit compliance matter when interpreting the comparison.

The remaining violation occurred in an unchanged baseline. Abstention keeps that setting, so abstention and operational-limit compliance need separate checks.

The published UPDATE received `eligible: true` and `nextRevision` through the real AI-Ops principal and registered MCP path. The private test driver read this field as `next`, failed before posting approval and entered error cleanup. **Live approval, application and post-application comparison of the model proposal remain unverified.** The driver mismatch was reproduced against the actual response and corrected. After target removal, approval returned HTTP `409`; Revision 13 was not created.

Once the same proposal's observation was over 30 minutes old, review returned `observation_expired` and approval returned HTTP `409`.

Independent AWS, CB-Tumblebug, Docker and process queries confirmed VM termination, root EBS deletion, Infra removal and shutdown of the owned Prometheus and SSH tunnel. Raw load tools, data, model artifacts and plots remain in ignored local storage. Validation is limited to the measured node, GPU, model, runtime, workloads and request rates; a new GPU node requires its own measured validation.

### 2026-11-12 inference optimization v3 repeat trial

A fresh T4 VM ran the same 18 conditions over five repetitions, producing 90 new windows. Policy source, operational limits and downtime budget stayed frozen; data from the previous GPU were kept separate. The 54 training, 18 validation and 18 final-test windows passed condition, ordering, metric-quality, deployment, Run and GPU-binding checks. Read-only container inspection confirmed the pinned model revision, FP16, 2,048-token context limit and runtime image digest.

Validation selected the 13-feature input set, but **the RF failed this trial's holdout gate**. The table reports the eight final contexts under the same accounting as the preceding trial: each starts at setting 2 with a one-hour horizon, charging a 300-second budget for every change.

| Policy | Mean adjusted goodput (requests/s) | Updates | Abstentions | Operational-limit breaches |
| --- | ---: | ---: | ---: | ---: |
| Keep setting 2 | 0.49273 | 0 | 0 | 4 |
| Training-selected fixed setting 8 | 0.81313 | 8 | 0 | 0 |
| Measured lookup | 0.85126 | 3 | 1 | 1 |
| Random Forest v3 | 0.62516 | 1 | 3 | 3 |

RF adjusted goodput was 26.88% above keeping the baseline and 26.56% below measured lookup. Its one offline update met the operational and minimum-gain limits. Small p95 support-range overruns caused RF to abstain on long and bursty workloads at 1.2 requests/s, where lookup selected updates. KV cache usage exceeded support at bursty 0.8 requests/s; both policies abstained there. Support bounds were not widened using the final test.

The published proposal was `ABSTAIN` with `holdout_gate_failed`. Actual AI-Ops MCP review returned `eligible: false`, approval returned HTTP `409`, and Revision 13 returned `404`. **Live approval, application and post-application comparison of a learned proposal remain incomplete.** The earlier passing result did not reproduce, so this model lacks sufficient evidence for operational use. Raw load tools, measurements, models, plots and private drivers remain ignored. [Implementation and validation scope](docs/api/inference-optimization-v3.md)

Independent queries at 03:23 UTC on 2026-11-12 confirmed instance termination, root EBS deletion, Infra removal, and shutdown of the owned Prometheus, SSH tunnel, workers and watchdog. Cleanup completed within the approved additional five-hour VM limit.

### 2026-11-12 v4 policy selection

The added `optimization/selection_policy.py` compares RF and GP using validation policy outcomes: unsafe or insufficient-gain updates, downtime-adjusted goodput, operational breaches and abstentions, then input count and prediction error. Support calibration uses training and validation only. When candidate error bands overlap, the policy prefers the smaller concurrency setting that still passes every existing guard. Original policies and the measured-lookup reference remain reproducible.

The fresh 90-window v4 trial selected RF with six inputs but failed qualification: adjusted goodput was 0.72311 requests/s, versus 0.49151 for keep and 0.84910 for the unchanged lookup. A higher final baseline goodput exceeded a zero-variation support interval. Actual MCP review returned ineligible, approval returned409, and an authorized read confirmed that Revision13 was absent. VM, root EBS, Infra, owned collectors and tunnels were removed within the approved limit. **The v4 trial did not apply or compare a qualified learned proposal live.** [Conditions, calculation assumptions and failure](docs/api/inference-optimization-v4.md)

### 2026-11-12 v5 measured-point KNN candidate

The added `optimization/neighbor_policy.py` compares workload-specific KNN with the unchanged RF/GP validation winner. Python tests (25) and the four Go packages pass. A fresh T4 with Qwen3-1.7B FP16 and vLLM 0.32.0 completed 18 conditions across five repetitions, 90 windows total. The 54/18/18 chronological split selected KNN with two inputs and passed qualification. Final downtime-adjusted goodput was 0.85004 requests/s versus 0.49151 for keep, a 72.94% increase that matched the lookup reference. Unsafe and insufficient-gain updates were zero; one operational breach remained in an unchanged setting.

Actual MCP review returned `eligible: true`, approval returned 202, and Revision 13 applied the 2→8 setting change. In two subsequent medium-input bursty windows at 1.2 RPS, server histogram p95 fell from 9.458 s to 1.606 s and 1.475 s; client SLO goodput rose from 0.298 to 1.174 and 1.190 requests/s. Both comparisons returned `KEEP` after accounting for the 173.982 s application response gap, with zero failed requests. Independent checks covered model, dataset and proposal hashes, actual runtime arguments, GPU, deployments, runs and arithmetic. [Conditions, policy comparison, actual application and limits](docs/api/inference-optimization-v5.md)

Fresh AWS, CB-Tumblebug, Docker and process queries confirmed VM termination, root EBS deletion, Infra removal, and shutdown of the owned collector, tunnel, workers and watchdog within the approved five-hour limit.

Our Prometheus collects development and experiment metrics; deployment environments use Innogrid monitoring. A separate POC ExtraTrees development replay matched v5 and lookup goodput and was not added to this GPU trial. Multi-node scaling is KHU's responsibility and remains unimplemented. Raw loads, data, models and plots stay excluded from Git. [POC reuse](docs/api/poc-optimization-reuse.md) · [Partner questions and preparation](docs/api/khu-collaboration-khu-response.md)

## Limitations

| Limitation | Impact |
|---|---|
| Serving ports open to `0.0.0.0/0` and stay open after removal; inference services have no auth | Fine for test environments; restrict at the network level for anything else |
| Deployment `env` values (for example `HF_TOKEN`) are stored in plain text in deployment records, archives and the node's `.env` | Do not pass long lived secrets |
| One bearer token for the whole API | No users or roles |
| Health is checked once at deployment time | A container that dies later does not change the record |
| One model per runtime per node | Deploying another model with the same runtime on the same node replaces the first |
| Large first time model downloads can hit the 30 minute task limit (`AIAPP_SEMAPHORE_TIMEOUT`); the engine waits only 15 minutes (`AIAPP_HEALTH_TIMEOUT`) | Raise the limits for large models |
| `kind: k8s` targets | Rejected with `422` |
| Windows and other Linux distributions (Amazon Linux 2, SUSE, Alpine) | Not supported by `install-docker` |
| Only NVIDIA GPUs | No AMD, NPU or TPU support |

## Roadmap

| Item | Owner |
|---|---|
| Model and app recommendation behind `/aiapp/recommendations/*` | Kyung Hee University (`internal/recommend`) |
| Real infra recommender and natural language agent replacing the mocks (point `AIAPP_PARTNER_REC_URL` / `AIAPP_MOCK_NL_URL` at them) | Partner institutions |
| Kubernetes deployment targets (`kind: k8s`) | Planned for year 3 |
| Restricting serving port sources, closing ports, secret handling for `env` | Open |
| Monitoring services after deployment | Open |
| More runtimes (formats from NGC, Docker Model Runner, Kaggle; image generation) | Open |
| Users and roles instead of a single token | Waits for the AI-Ops framework decision |

## Configuration

**No variable is required**; every one has a default. Processes read `conf/setup.env` when it exists (start from
[`conf/template-setup.env`](conf/template-setup.env)); environment variables win over the file. Docker compose reads `.env`
(start from [`.env.example`](.env.example)).

| Variable | Default | Meaning |
|---|---|---|
| `AIAPP_SERVER_PORT` | `8090` | AI-App port |
| `AIAPP_AUTH_TOKEN` | (empty: auth off) | Bearer token for every route except readiness, console and Swagger. Give AI-App, the engine and the NL agent the same value |
| `AIAPP_TUMBLEBUG_BASE_URL` | `http://localhost:1323/tumblebug` | CB-Tumblebug |
| `AIAPP_TUMBLEBUG_USERNAME` / `_PASSWORD` | | CB-Tumblebug basic auth |
| **`AIAPP_TUMBLEBUG_DRY_RUN`** | **`true`** | Write gate. `false` lets deployments and node control change real nodes |
| `AIAPP_SEMAPHORE_URL` | (compose: `http://semaphore:3000`) | Semaphore API |
| `AIAPP_SEMAPHORE_USERNAME` / `_PASSWORD` or `AIAPP_SEMAPHORE_TOKEN` | | Semaphore login |
| `AIAPP_SEMAPHORE_UI_URL` | | Browser address of Semaphore; adds `taskUrl` links to records |
| `AIAPP_STAGE_ROOT` | `/var/lib/aiapp/stage` | Where deployment files are staged. Semaphore must see the same absolute path |
| `AIAPP_DEPLOYMENT_OPEN_PORTS` | `true` | Open serving ports on the node's security groups |
| `AIAPP_MODEL_STORE_FILE` | (empty: memory) | JSON file that keeps the model catalog |
| `AIAPP_DEPLOYMENT_STORE_FILE` | (empty: memory) | JSON file that keeps deployment records |
| `AIAPP_APP_STORE_DIR` | `./data/apps` | Uploaded applications |
| `AIAPP_CATALOG_SYNC_ENABLED` | `true` | Sync the model catalog from public sources at startup and every `AIAPP_CATALOG_SYNC_INTERVAL` (`24h`) |

<details>
<summary>All variables</summary>

| Variable | Default | Read by | Meaning |
|---|---|---|---|
| `AIAPP_SERVER_PORT` | `8090` | App | AI-App port |
| `AIAPP_ENGINE_PORT` | `8091` | Engine | Engine port |
| `AIAPP_ENGINE_URL` | `http://127.0.0.1:8091` | App | Where `/aiapp/engine/*` is forwarded |
| `AIAPP_MOCK_NL_URL` | `http://127.0.0.1:8093` | App | Where `/aiapp/partner/nl/*` is forwarded (natural language agent, mock today) |
| `AIAPP_PARTNER_REC_URL` | `http://127.0.0.1:8092` | App | Where `/aiapp/partner/rec/*` is forwarded (infra recommender, mock today) |
| `AIAPP_APP_MCP_URL` | `http://127.0.0.1:8090/aiapp/mcp` | Engine | AI-App MCP server the engine uses |
| `AIAPP_AUTH_TOKEN` | | App, Engine, NL | Bearer token for every route except readiness, console and Swagger. Empty turns auth off |
| `AIAPP_APP_TOKEN` | | Engine, NL | Token the engine sends to the AI-App MCP server |
| `AIAPP_TUMBLEBUG_BASE_URL` | `http://localhost:1323/tumblebug` | App, Rec | CB-Tumblebug |
| `AIAPP_TUMBLEBUG_USERNAME` / `_PASSWORD` | | App, Rec | Basic auth |
| `AIAPP_TUMBLEBUG_CREDENTIAL_HOLDER` | | App, Rec | `x-credential-holder` header |
| `AIAPP_TUMBLEBUG_TIMEOUT` | `20m` | App, Rec | Limit per CB-Tumblebug call |
| `AIAPP_TUMBLEBUG_IMAGE_NAMESPACE` | `system` | Rec | Image catalog namespace |
| `AIAPP_TUMBLEBUG_SPEC_NAMESPACE` | `system` | App, Rec | Spec catalog namespace; used to decide GPU |
| `AIAPP_TUMBLEBUG_DEFAULT_OS_TYPE` | `ubuntu 22.04` | Rec | OS when a request names none |
| `AIAPP_TUMBLEBUG_DRY_RUN` | `true` | App | Write gate |
| `AIAPP_HEALTH_TIMEOUT` | `15m` | Engine | How long an engine workflow waits for a deployment to end |
| `AIAPP_HEALTH_INTERVAL` | `20s` | Engine | How often it asks |
| `AIAPP_ARCHIVE_DIR` | `./data/archive` | App | Deployment and control archive |
| `AIAPP_MODEL_STORE_FILE` | | App | Model catalog file. Seeds 3 models only when the file does not exist yet |
| `AIAPP_CATALOG_SYNC_ENABLED` | `true` | App | Catalog sync on or off |
| `AIAPP_CATALOG_SYNC_INTERVAL` | `24h` | App | Sync interval. With no source reachable it retries after 5m, 15m, 1h |
| `AIAPP_CATALOG_SYNC_SOURCES` | `huggingface,ollama` | App | Comma separated: `huggingface`, `ollama`, `docker-model-runner`, `ngc`, `kaggle`. The last three are off by default because no runtime serves them yet. Models of a source left out are deleted at the end of each run (never `seed` or `manual` ones) |
| `AIAPP_CATALOG_SYNC_LIMIT` | `100` | App | Models per source (per task for Hugging Face) |
| `AIAPP_APP_STORE_DIR` | `./data/apps` | App | Uploaded applications, `<name>/<version>/` |
| `AIAPP_APP_UPLOAD_MAX_BYTES` | `268435456` | App | Upload size limit (256 MiB) |
| `AIAPP_SEMAPHORE_URL` | | App | Semaphore address without `/api`. Empty fails real deployments when they reach Semaphore |
| `AIAPP_SEMAPHORE_TOKEN` | | App | Semaphore API token, used instead of user and password |
| `AIAPP_SEMAPHORE_USERNAME` / `_PASSWORD` | | App | Semaphore login |
| `AIAPP_SEMAPHORE_PROJECT` | `aiapp` | App | Semaphore project, created if missing |
| `AIAPP_SEMAPHORE_PLAYBOOK_DIR` | `/ansible` | App | Playbook folder inside the Semaphore container |
| `AIAPP_SEMAPHORE_POLL_INTERVAL` | `5s` | App | Task polling interval |
| `AIAPP_SEMAPHORE_TIMEOUT` | `30m` | App | Limit per playbook task |
| `AIAPP_SEMAPHORE_UI_URL` | | App | Browser address of Semaphore for `taskUrl` links |
| `AIAPP_STAGE_ROOT` | `/var/lib/aiapp/stage` | App | Stage directory, absolute path shared with Semaphore |
| `AIAPP_DEPLOYMENT_OPEN_PORTS` | `true` | App | Open serving ports after a successful deployment |
| `AIAPP_RECOMMENDER_MODEL` / `_APP` | | App | Name of a registered recommender in `internal/recommend`. Empty answers `501`; an unknown name stops startup |
| `AIAPP_DEPLOYMENT_STORE_FILE` | | App | Deployment record file. Records in progress at the previous stop become `interrupted` |
| `AIAPP_ENGINE_ARCHIVE_DIR` | `./data/engine-archive` | Engine | Engine run records |
| `AIAPP_ENGINE_REGISTRY_URL` / `_TOKEN` | | Engine | Agent registry to register the engine card with at startup |
| `AIAPP_MOCK_RECOMMENDER_PORT` / `_TOKEN` | `8092` | Rec | Mock recommender port and bearer token |
| `AIAPP_MOCK_NL_PORT` | `8093` | NL | Mock NL agent port |
| `AIAPP_MOCK_NL_ENGINE_MCP_URL` | `http://127.0.0.1:8091/aiapp/engine/mcp` | NL | Engine MCP server the mock agent uses |
| `AIAPP_MOCK_NL_ENGINE_TOKEN` | | NL | Token for the engine MCP server; falls back to `AIAPP_APP_TOKEN` |
| `AIAPP_MOCK_NL_ARCHIVE_DIR` | `./data/partner-nl-archive` | NL | Mock agent run records |
| `AIAPP_MOCK_NL_MAX_STEPS` | `12` | NL | Tool call limit per instruction |
| `AIAPP_MOCK_NL_REGISTRY_URL` / `_TOKEN` | | NL | Agent registry for the mock agent card |
| `AIAPP_LLM_BASE_URL` | `https://api.anthropic.com` | NL | Any endpoint that accepts Anthropic Messages (`/v1/messages`), Ollama 0.15+ included |
| `AIAPP_LLM_API_KEY` | | NL | Empty makes only the instruction route answer `503` |
| `AIAPP_LLM_MODEL` | `claude-opus-5` | NL | Planning model |
| `AIAPP_LLM_MAX_TOKENS` / `AIAPP_LLM_TIMEOUT` | `16000` / `5m` | NL | Limits per model call |

`AIAPP_SEMAPHORE_PORT` (default `3001`) is used only by `docker-compose.yml` to publish Semaphore on the host.
App = `ai-app-server`, Engine = `ai-automation-engine`, Rec = `mock-partner-recommender`, NL = `mock-partner-nl-agent`.

</details>

### Upgrading

From a build before 2026-11-03:

| Left over | Symptom | Fix |
|---|---|---|
| Model catalog file written before runtimes were named (`AIAPP_MODEL_STORE_FILE`, compose volume `catalog`, `/app/data/catalog/models.json`) | Startup fails: `model catalog ... is in the format before runtimes` | Delete the file; the 3 built-in models come back. Register your own models again with `runtimes[].runtimeId` |
| Deployment records with `type: model-service` | Listed, but missing from `?type=inference` and `GET /aiapp/endpoints` | Deploy again with `type: inference` to the same node and runtime; it replaces the old containers |
| Deployment record file without `version` (`running` meant in progress, `succeeded` meant done) | Converted on read: `running` to `deploying` (then `interrupted`), `succeeded` to `running` | Filter with `?status=running` instead of `?status=succeeded` |

## Development

```sh
make build      # bin/: ai-app-server, ai-automation-engine, mock-partner-recommender, mock-partner-nl-agent
make test       # go test ./...
make verify     # gofmt + build + test + golangci-lint
make swag       # regenerate Swagger in api/docs
```

| Path | Contents |
|---|---|
| `cmd/` | Entry points of AI-App and the engine |
| `internal/modelcat`, `internal/catalogsync`, `internal/compose` | Model catalog and runtime profiles, catalog sync from public sources, compose generation |
| `internal/appstore` | Application upload and versions |
| `internal/nodedeploy` | Node deployment, removal, ports, endpoints, progress |
| `internal/tumblebug`, `internal/semaphore` | Clients for CB-Tumblebug and Semaphore |
| `internal/tools`, `internal/mcpserver`, `internal/engine` | Tool layer with the dry-run gate, MCP servers, automation workflows |
| `internal/recommend` | Interface for recommendation implementations |
| `deploy/ansible` | Playbooks Semaphore runs on nodes |
| `web/` | Operator console (embedded in the binary) |
| `mock/partner/` | Mocks of partner owned services; AI-App code does not import them (checked by `internal/boundary`) |

## License

Licensed under the [Apache License 2.0](LICENSE).
Container images that AI-App deploys onto nodes (runtimes, models, example applications) keep their own licenses.
