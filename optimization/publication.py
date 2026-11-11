"""Publish a Shadow result to the engine's operator-owned proposal directory."""

from hashlib import sha256
import json
import os
from pathlib import Path
import re
from tempfile import NamedTemporaryFile


def publish(row: dict, preview: dict, manifest: dict, directory: Path, transition: dict | None = None) -> str:
    observation = row["observation"]
    if (not re.fullmatch(r"[0-9a-f]{64}", preview["observationId"] or "") or not preview["correlationId"]
            or not preview["deploymentId"] or not preview["revision"]):
        raise ValueError("publication requires an observation bound to a deployment revision")
    guard = manifest["guard"]
    current = preview.get("currentMaxNumSeqs", 0)
    candidate = preview.get("candidateMaxNumSeqs", 0)
    forecasts = preview.get("predictions", {})
    payload = {
        "schemaVersion": "khu-inference-optimization/v1",
        "observation": observation,
        "load": {key: row["load"][key] for key in (
            "observationId", "workloadId", "runtimeImage", "window", "promptTokensP50",
            "maxOutputTokens", "offeredRps", "sloSeconds", "attempted", "failed", "withinSLO",
        ) if key in row["load"]},
        "guard": {key: guard[key] for key in (
            "p95LimitSeconds", "peakVRAMLimitMiB", "errorRateLimit", "minimumGainFraction",
            "evaluationHorizonSeconds", "maxUpdateDowntimeSeconds",
        ) if key in guard},
        "decision": {
            **{key: preview[key] for key in ("mode", "action", "reason", "dataKind", "modelVersion", "artifactSha256", "datasetSha256")},
            "holdoutPassed": manifest["holdoutPassed"],
            "currentMaxNumSeqs": current,
            "candidateMaxNumSeqs": candidate,
            "forecast": forecasts.get(candidate),
            "currentForecast": forecasts.get(current),
            "validationMargin": preview.get("validationMargin"),
        },
    }
    if transition is not None:
        payload["transitionEvidence"] = transition
    encoded = json.dumps(payload, sort_keys=True, separators=(",", ":"), allow_nan=False).encode()
    proposal_id = sha256(encoded).hexdigest()
    directory.mkdir(mode=0o750, parents=True, exist_ok=True)
    path = directory / (proposal_id + ".json")
    staged = NamedTemporaryFile(dir=directory, prefix=".proposal-", delete=False)
    temp = Path(staged.name)
    try:
        with staged:
            staged.write(encoded)
            staged.flush()
            os.fsync(staged.fileno())
        try:
            os.link(temp, path)
        except FileExistsError:
            if path.read_bytes() != encoded:
                raise ValueError("published proposal content does not match its identifier")
    finally:
        temp.unlink()
    return proposal_id
