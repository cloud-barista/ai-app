"""Train and replay a vLLM scheduler policy against AI-App observation exports."""

from __future__ import annotations

import argparse
from dataclasses import dataclass
from datetime import datetime
from hashlib import sha256
import json
from pathlib import Path
from statistics import mean, median
from sys import float_info

import joblib
import sklearn
from sklearn.ensemble import RandomForestRegressor
from sklearn.multioutput import MultiOutputRegressor

if __package__:
    from .publication import publish
else:
    from publication import publish


VERSION = "khu-vllm-max-num-seqs/v2"
LEGACY_VERSION = "khu-vllm-max-num-seqs/v1"
HOLDOUT_ACCOUNTING = "update-downtime/v1"
OUTPUTS = ("p95Seconds", "goodputRps", "peakVRAMMiB", "errorRate")
ZERO_MARGIN = dict.fromkeys(OUTPUTS, 0.0)


@dataclass(frozen=True)
class Sample:
    experiment: str
    observation: dict
    scope: dict
    start: datetime
    end: datetime
    offered_rps: float
    max_num_seqs: int
    outcomes: dict[str, float]

    @property
    def point(self) -> tuple[float, int]:
        return self.offered_rps, self.max_num_seqs


def number(value, minimum=0.0) -> float:
    if type(value) not in (float, int) or not minimum <= value <= float_info.max:
        raise ValueError("invalid numeric value")
    return float(value)


def count(value, minimum=0) -> int:
    if type(value) is not int or value < minimum:
        raise ValueError("invalid count")
    return value


def required_text(value) -> str:
    if not isinstance(value, str) or not value.strip():
        raise ValueError("missing scope or record identity")
    return value


def timestamp(value) -> datetime:
    at = datetime.fromisoformat(required_text(value))
    if at.tzinfo is None:
        raise ValueError("observation timestamps require a timezone")
    return at


def measurement(item: dict) -> float:
    if item["quality"] != "measured":
        raise ValueError("observation has missing or invalid metrics")
    return number(item["value"])


def read_sample(row: dict, guard: dict) -> Sample:
    obs, load = row["observation"], row["load"]
    metrics = obs["metrics"]
    gpus = metrics["gpus"]
    if obs["runtimeId"] != "vllm" or metrics["complete"] is not True or not isinstance(gpus, dict) or not gpus:
        raise ValueError("a complete vLLM GPU observation is required")
    required_text(obs["observationId"])
    required_text(obs["correlationId"])
    for field in ("inferenceRunId", "applicationRunId", "applicationDeploymentId"):
        required_text(obs[field])
    count(obs["revision"], 1)
    placement = obs["placement"]
    required_text(placement["deploymentId"])
    scope = {key: required_text(obs[key]) for key in ("modelId", "artifactId", "specId", "applicationName")}
    scope["applicationVersion"] = count(obs["applicationVersion"], 1)
    scope.update({key: required_text(placement[key]) for key in ("nsId", "infraId", "nodeId")})
    scope["gpuUUIDs"] = sorted(required_text(key) for key in gpus)
    scope["workloadId"] = required_text(load["workloadId"])
    scope["runtimeImage"] = required_text(load["runtimeImage"])
    scope["promptTokensP50"] = count(load["promptTokensP50"], 1)
    scope["maxOutputTokens"] = count(load["maxOutputTokens"], 1)
    start, end = (timestamp(obs["window"][key]) for key in ("start", "end"))
    load_start, load_end = (timestamp(load["window"][key]) for key in ("start", "end"))
    seconds = (end - start).total_seconds()
    if (not 120 <= seconds <= 1800 or timestamp(obs["appliedAt"]) > start
            or load["observationId"] != obs["observationId"] or (load_start, load_end) != (start, end)):
        raise ValueError("load test and applied observation must share a settled window")
    attempted = count(load["attempted"], 1)
    failed, within_slo = count(load["failed"]), count(load["withinSLO"])
    if failed + within_slo > attempted or number(load["sloSeconds"], 1e-6) != guard["p95LimitSeconds"]:
        raise ValueError("load outcomes must use the policy's latency SLO")
    return Sample(
        experiment=required_text(row["experimentId"]),
        observation=obs,
        scope=scope,
        start=start,
        end=end,
        offered_rps=number(load["offeredRps"], 1e-6),
        max_num_seqs=count(obs["maxNumSeqs"], 1),
        outcomes={
            "p95Seconds": measurement(metrics["serving"]["latencyP95"]),
            "goodputRps": within_slo / seconds,
            "peakVRAMMiB": max(measurement(gpu["peakUsedVRAM"]) for gpu in gpus.values()),
            "errorRate": failed / attempted,
        },
    )


def split_samples(samples: list[Sample], baseline: int):
    ordered = sorted(samples, key=lambda row: row.start)
    groups = {}
    ids = set()
    for row in ordered:
        obs_id = row.observation["observationId"]
        if obs_id in ids or row.scope != ordered[0].scope:
            raise ValueError("duplicate observations or mixed node, model, runtime or workload scopes")
        ids.add(obs_id)
        groups.setdefault(row.experiment, []).append(row)
    if any(a.end > b.start for a, b in zip(ordered, ordered[1:])):
        raise ValueError("load tests on the same node must not overlap")
    experiments = list(groups.values())
    if len(experiments) < 5 or any(a[-1].end > b[0].start for a, b in zip(experiments, experiments[1:])):
        raise ValueError("supply at least five chronological, non-interleaved experiment groups")
    rates = sorted({row.offered_rps for row in ordered})
    candidates = sorted({row.max_num_seqs for row in ordered})
    grid = {(rps, setting) for rps in rates for setting in candidates}
    if len(rates) < 2 or len(candidates) < 2 or baseline not in candidates:
        raise ValueError("repeat at least two load levels and two settings, including the baseline")
    if any(len(group) != len(grid) or {row.point for row in group} != grid for group in experiments):
        raise ValueError("each experiment must cover the same load-by-setting grid once")
    n = len(experiments)
    return rates, candidates, (experiments[: n * 3 // 5], experiments[n * 3 // 5 : n * 4 // 5], experiments[n * 4 // 5 :])


def limits_ok(values: dict, margin: dict, guard: dict) -> bool:
    limits = {
        "p95Seconds": guard["p95LimitSeconds"],
        "peakVRAMMiB": guard["peakVRAMLimitMiB"],
        "errorRate": guard["errorRateLimit"],
    }
    return all(values[key] + margin[key] <= limit for key, limit in limits.items())


def update_serving_fraction(guard: dict) -> float:
    horizon = guard.get("evaluationHorizonSeconds")
    downtime = guard.get("maxUpdateDowntimeSeconds")
    # shortcut: older fixtures omit the budget, require it for measured policies.
    if guard["dataKind"] == "fixture" and horizon is None and downtime is None:
        return 1.0
    horizon = number(horizon)
    downtime = number(downtime)
    if not 0 < downtime < horizon:
        raise ValueError("require a positive update downtime shorter than the evaluation horizon")
    return 1 - downtime / horizon


def choose(current: Sample, predictions: dict, margin: dict, guard: dict) -> tuple[str, int, str]:
    keep = current.max_num_seqs
    if keep not in predictions:
        return "ABSTAIN", keep, "current_setting_outside_measured_grid"
    safe = [setting for setting, values in predictions.items() if limits_ok(values, margin, guard)]
    gain = guard["minimumGainFraction"]
    error = margin["goodputRps"]
    current_upper = max(current.outcomes["goodputRps"], predictions[keep]["goodputRps"] + error)
    if safe:
        best = max(safe, key=lambda setting: (predictions[setting]["goodputRps"], setting == keep))
        effective = (predictions[best]["goodputRps"] - error) * update_serving_fraction(guard)
        if best != keep and effective > current_upper * (1 + gain):
            return "UPDATE", best, "estimated_gain_passes_validation_margin_and_downtime"
    if limits_ok(current.outcomes, ZERO_MARGIN, guard):
        return "KEEP", keep, "no_supported_improvement"
    return "ABSTAIN", keep, "current_slo_breach_without_supported_change"


def predict(model, points: list[tuple[float, int]]) -> dict:
    return {point: dict(zip(OUTPUTS, values)) for point, values in zip(points, model.predict(points).tolist())}


def windows_by_load(groups: list[list[Sample]]):
    for rows in groups:
        by_load = {}
        for row in rows:
            by_load.setdefault(row.offered_rps, {})[row.max_num_seqs] = row
        yield from by_load.values()


def candidate_input(current: Sample, setting: int) -> tuple:
    return current.offered_rps, setting, *(current.outcomes[key] for key in OUTPUTS)


def paired_forecast(model, current: Sample, settings: list[int]) -> dict:
    candidates = [setting for setting in settings if setting != current.max_num_seqs]
    outcomes = model.predict([candidate_input(current, setting) for setting in candidates]).tolist()
    forecasts = {setting: dict(zip(OUTPUTS, values)) for setting, values in zip(candidates, outcomes)}
    forecasts[current.max_num_seqs] = current.outcomes
    return forecasts


def within_baseline_bounds(current: Sample, bounds: dict) -> bool:
    limits = bounds[str(current.offered_rps)]
    return all(limits[key][0] <= value <= limits[key][1] for key, value in current.outcomes.items())


def validation_margin(groups: list[list[Sample]], forecast, baseline: int) -> dict:
    margin = ZERO_MARGIN.copy()
    for windows in windows_by_load(groups):
        predictions = forecast(windows[baseline])
        for setting, window in windows.items():
            for key in OUTPUTS:
                margin[key] = max(margin[key], abs(window.outcomes[key] - predictions[setting][key]))
    return margin


def evaluate(groups: list[list[Sample]], forecast, margin: dict, guard: dict) -> dict:
    updates = unsafe = weak_gain = breaches = 0
    goodput = []
    effective_goodput = []
    serving_fraction = update_serving_fraction(guard)
    for windows in windows_by_load(groups):
        current = windows[guard["baselineMaxNumSeqs"]]
        action, selected, _ = choose(current, forecast(current), margin, guard)
        outcome = windows[selected].outcomes
        ok = limits_ok(outcome, ZERO_MARGIN, guard)
        effective = outcome["goodputRps"]
        if action == "UPDATE":
            updates += 1
            unsafe += int(not ok)
            effective *= serving_fraction
            weak_gain += int(effective <= current.outcomes["goodputRps"] * (1 + guard["minimumGainFraction"]))
        breaches += int(not ok)
        goodput.append(outcome["goodputRps"])
        effective_goodput.append(effective)
    return {
        "windows": len(goodput), "updates": updates, "unsafeUpdates": unsafe,
        "weakUpdates": weak_gain, "sloBreaches": breaches, "meanGoodputRps": mean(goodput),
        "meanEffectiveGoodputRps": mean(effective_goodput),
    }


def train(dataset: Path, guard: dict, destination: Path) -> dict:
    baseline = count(guard["baselineMaxNumSeqs"], 1)
    number(guard["p95LimitSeconds"], 1e-6)
    number(guard["peakVRAMLimitMiB"], 1.0)
    number(guard["minimumGainFraction"], 1e-6)
    if number(guard["errorRateLimit"]) > 1 or guard["dataKind"] not in {"measurement", "fixture"}:
        raise ValueError("invalid error limit or dataset kind")
    update_serving_fraction(guard)
    raw = dataset.read_bytes()
    samples = [read_sample(json.loads(line), guard) for line in raw.splitlines() if line.strip()]
    rates, settings, (training, validation, test) = split_samples(samples, baseline)
    fit = [row for group in training for row in group]
    inputs, outcomes = [], []
    for windows in windows_by_load(training):
        current = windows[baseline]
        for setting, candidate in windows.items():
            if setting != baseline:
                inputs.append(candidate_input(current, setting))
                outcomes.append([candidate.outcomes[key] for key in OUTPUTS])
    model = MultiOutputRegressor(RandomForestRegressor(
        n_estimators=128, min_samples_leaf=2, random_state=42, n_jobs=1,
    ))
    model.fit(inputs, outcomes)

    baseline_bounds = {}
    for rps in rates:
        measured = [row.outcomes for row in fit if row.point == (rps, baseline)]
        baseline_bounds[str(rps)] = {
            key: [min(row[key] for row in measured), max(row[key] for row in measured)] for key in OUTPUTS
        }
    grid = [(rps, setting) for rps in rates for setting in settings]
    lookup = {
        point: {key: median(row.outcomes[key] for row in fit if row.point == point) for key in OUTPUTS}
        for point in grid
    }

    def forecast(current):
        return paired_forecast(model, current, settings)

    def supported_forecast(current):
        return forecast(current) if within_baseline_bounds(current, baseline_bounds) else {}

    def measured_forecast(current):
        return {setting: lookup[current.offered_rps, setting] for setting in settings}

    margin = validation_margin(validation, forecast, baseline)
    report = {
        "randomForest": evaluate(test, supported_forecast, margin, guard),
        "measuredLookup": evaluate(test, measured_forecast, validation_margin(validation, measured_forecast, baseline), guard),
        "keep": evaluate(test, lambda _: {}, ZERO_MARGIN, guard),
    }
    rf = report["randomForest"]
    passed = (rf["updates"] > 0 and rf["unsafeUpdates"] == 0 and rf["weakUpdates"] == 0
              and rf["meanEffectiveGoodputRps"] > report["keep"]["meanEffectiveGoodputRps"]
              and rf["meanEffectiveGoodputRps"] >= report["measuredLookup"]["meanEffectiveGoodputRps"])
    destination.mkdir(parents=True, exist_ok=False)
    artifact = destination / "model.joblib"
    joblib.dump(model, artifact)
    manifest = {
        "version": VERSION, "scikitLearnVersion": sklearn.__version__,
        "datasetSha256": sha256(raw).hexdigest(), "artifactSha256": sha256(artifact.read_bytes()).hexdigest(),
        "scope": fit[0].scope, "guard": guard, "offeredRps": rates, "maxNumSeqs": settings,
        "baselineBounds": baseline_bounds,
        "validationMargin": margin, "holdout": report, "holdoutPassed": passed, "holdoutAccounting": HOLDOUT_ACCOUNTING,
        "split": {
            name: [group[0].experiment for group in split]
            for name, split in (("train", training), ("validation", validation), ("test", test))
        },
    }
    (destination / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    return manifest


def preview(row: dict, directory: Path) -> dict:
    manifest = json.loads((directory / "manifest.json").read_text(encoding="utf-8"))
    return preview_artifact(row, directory, manifest)


def preview_artifact(row: dict, directory: Path, manifest: dict) -> dict:
    path = directory / "model.joblib"
    version = manifest["version"]
    if version not in (VERSION, LEGACY_VERSION) or manifest["scikitLearnVersion"] != sklearn.__version__ or (
        sha256(path.read_bytes()).hexdigest() != manifest["artifactSha256"]
    ):
        raise ValueError("policy artifact version or digest mismatch")
    guard = manifest["guard"]
    obs = row.get("observation") if isinstance(row, dict) else None
    obs = obs if isinstance(obs, dict) else {}
    place = obs.get("placement")
    out = {
        "mode": "shadow", "action": "ABSTAIN", "modelVersion": version,
        "artifactSha256": manifest["artifactSha256"], "datasetSha256": manifest["datasetSha256"],
        "dataKind": guard["dataKind"], "observationId": obs.get("observationId"),
        "correlationId": obs.get("correlationId"), "revision": obs.get("revision"),
        "deploymentId": place.get("deploymentId") if isinstance(place, dict) else None,
    }
    try:
        current = read_sample(row, guard)
    except (KeyError, TypeError, ValueError):
        return {**out, "reason": "incomplete_observation_or_load_test"}
    out["currentMaxNumSeqs"] = current.max_num_seqs
    out["baseline"] = {"action": "KEEP", "measured": current.outcomes}
    if current.scope != manifest["scope"] or current.offered_rps not in manifest["offeredRps"]:
        return {**out, "reason": "outside_measured_scope"}
    if current.max_num_seqs != guard["baselineMaxNumSeqs"]:
        return {**out, "reason": "current_setting_not_in_validation"}
    if guard["dataKind"] == "measurement" and manifest.get("holdoutAccounting") != HOLDOUT_ACCOUNTING:
        return {**out, "reason": "holdout_requires_downtime_evaluation"}
    if manifest["holdoutPassed"] is not True:
        return {**out, "reason": "holdout_gate_failed"}
    if version == VERSION and not within_baseline_bounds(current, manifest["baselineBounds"]):
        return {**out, "reason": "current_metrics_outside_measured_scope"}
    # Policy directories are written by the operator's training job.
    model = joblib.load(path)
    settings = manifest["maxNumSeqs"]
    if version == LEGACY_VERSION:
        points = [(current.offered_rps, setting) for setting in settings]
        predictions = {setting: values for (_, setting), values in predict(model, points).items()}
    else:
        predictions = paired_forecast(model, current, settings)
    action, candidate, reason = choose(current, predictions, manifest["validationMargin"], guard)
    result = {
        **out, "action": action, "reason": reason,
        "candidateMaxNumSeqs": candidate,
        "predictions": predictions, "validationMargin": manifest["validationMargin"],
    }
    if action == "UPDATE":
        result["inferenceArgs"] = ["--max-num-seqs", str(candidate)]
    return result


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    fit = commands.add_parser("train")
    fit.add_argument("dataset", type=Path)
    fit.add_argument("guard", type=Path)
    fit.add_argument("destination", type=Path)
    shadow = commands.add_parser("preview")
    shadow.add_argument("sample", type=Path)
    shadow.add_argument("artifact", type=Path)
    shadow.add_argument("--publish-dir", type=Path)
    shadow.add_argument("--transition-evidence", type=Path, help="Application response measurement to attach when publishing")
    args = parser.parse_args()
    try:
        if args.command == "train":
            result = train(args.dataset, json.loads(args.guard.read_text(encoding="utf-8")), args.destination)
        else:
            if args.transition_evidence and not args.publish_dir:
                raise ValueError("--transition-evidence requires --publish-dir")
            row = json.loads(args.sample.read_text(encoding="utf-8"))
            manifest = json.loads((args.artifact / "manifest.json").read_text(encoding="utf-8"))
            result = preview_artifact(row, args.artifact, manifest)
            if args.publish_dir:
                transition = None
                if args.transition_evidence:
                    transition = json.loads(args.transition_evidence.read_text(encoding="utf-8"))
                    if not isinstance(transition, dict):
                        raise ValueError("transition evidence must be a JSON object")
                result["proposalId"] = publish(row, result, manifest, args.publish_dir, transition)
    except (KeyError, OSError, TypeError, ValueError) as error:
        parser.exit(1, f"{error}\n")
    print(json.dumps(result, indent=2, allow_nan=False))


if __name__ == "__main__":
    main()
