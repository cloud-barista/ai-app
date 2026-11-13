"""Compare measured-point KNN with the unchanged RF/GP validation policy."""

import argparse
from collections import defaultdict
from hashlib import sha256
import json
from pathlib import Path
from statistics import median

import joblib
import sklearn
from sklearn.neighbors import KNeighborsRegressor

if __package__:
    from . import selection_policy as regression
    from .publication import publish
else:
    import selection_policy as regression
    from publication import publish

context = regression.context
legacy = regression.legacy
VERSION = "khu-vllm-max-num-seqs/v5"
ALGORITHM = "nearest_neighbors_by_workload"
FEATURES = ("offeredRps", "candidateMaxNumSeqs")
DEPENDENCIES = ("selection_policy.py", "context_policy.py", "policy.py", "publication.py")


def dependency_hashes():
    return {name: sha256(Path(__file__).with_name(name).read_bytes()).hexdigest() for name in DEPENDENCIES}


def train(dataset, guard, destination):
    destination.mkdir(parents=True, exist_ok=False)
    regressors = destination / "regression-candidate"
    previous = regression.train(dataset, guard, regressors)
    raw = dataset.read_bytes()
    if sha256(raw).hexdigest() != previous["datasetSha256"]:
        raise ValueError("dataset changed during training")
    samples = [context.read_sample(json.loads(line), guard) for line in raw.splitlines() if line.strip()]
    profiles, (training, validation, test) = context.split_samples(samples, guard["baselineMaxNumSeqs"])
    baseline = guard["baselineMaxNumSeqs"]
    settings = previous["contextSettings"]
    points = defaultdict(list)
    for windows in context.windows_by_context(training):
        current = windows[baseline]
        for setting, candidate in windows.items():
            if setting != baseline:
                points[current.scope["workloadId"], current.offered_rps, setting].append(candidate.outcomes)
    models = {}
    for workload in profiles:
        inputs, targets = [], []
        for (key, rate, setting), values in sorted(points.items()):
            if key == workload:
                inputs.append([rate, setting])
                targets.append([median(value[name] for value in values) for name in legacy.OUTPUTS])
        models[workload] = KNeighborsRegressor(n_neighbors=1).fit(inputs, targets)
    support = regression.support_envelope(training + validation, FEATURES, baseline)

    def forecast(current):
        return context.forecast(models[current.scope["workloadId"]], current,
                                settings[context.support_key(current)], FEATURES)

    margin = context.validation_margin(validation, forecast, baseline)
    report = context.evaluate(validation, forecast, margin, guard)
    error = sum(margin[key] / scale for key, scale in zip(legacy.OUTPUTS, (
        guard["p95LimitSeconds"], max(row.offered_rps for group in training for row in group),
        guard["peakVRAMLimitMiB"], 1)))
    candidate = {"algorithm": ALGORITHM, "features": list(FEATURES), "validation": report,
                 "normalizedValidationError": error, "validationMargin": margin, "fitWarnings": []}
    prior = next(item for item in previous["validationCandidates"]
                 if item["algorithm"] == previous["algorithm"] and item["features"] == previous["features"])
    selected = min([prior, candidate], key=regression.selection_key)
    neighbors = selected is candidate
    holdout = {**previous["holdout"]}
    if neighbors:
        holdout["selectedPolicy"] = context.evaluate(test, forecast, margin, guard)
    else:
        models = joblib.load(regressors / "model.joblib")
        support = previous["support"]
        margin = previous["validationMargin"]
    validated, final = selected["validation"], holdout["selectedPolicy"]
    validation_keep = context.fixed_reference(validation, baseline, guard)
    validation_passed = (validated["updates"] > 0 and validated["unsafeUpdates"] == 0 and validated["weakUpdates"] == 0
                         and validated["meanEffectiveGoodputRps"] > validation_keep["meanEffectiveGoodputRps"])
    passed = (validation_passed and final["updates"] > 0 and final["unsafeUpdates"] == 0 and final["weakUpdates"] == 0
              and final["meanEffectiveGoodputRps"] > holdout["keep"]["meanEffectiveGoodputRps"]
              and final["meanEffectiveGoodputRps"] >= holdout["measuredLookup"]["meanEffectiveGoodputRps"])
    path = destination / "model.joblib"
    joblib.dump(models, path)
    manifest = {**previous, "version": VERSION, "algorithm": selected["algorithm"],
                "predictorKind": "neighbors_by_workload" if neighbors else "regressor",
                "policySourceSha256": sha256(Path(__file__).read_bytes()).hexdigest(),
                "policyDependenciesSha256": dependency_hashes(),
                "artifactSha256": sha256(path.read_bytes()).hexdigest(), "features": selected["features"],
                "support": support, "validationMargin": margin, "validationPassed": validation_passed,
                "validationCandidates": [*previous["validationCandidates"], candidate],
                "candidateRanking": "measured-median-goodput/v1" if neighbors else previous["candidateRanking"],
                "neighborFit": {"nNeighbors": 1, "aggregation": "training-median-per-measured-point",
                                "prototypes": len(points), "trainingRows": previous["trainingRows"],
                                "unmeasuredPointsAllowed": False},
                "holdout": holdout, "holdoutPassed": passed}
    (destination / "manifest.json").write_text(json.dumps(manifest, indent=2, allow_nan=False) + "\n")
    return manifest


def preview(row, directory):
    if not isinstance(row, dict):
        raise ValueError("a policy sample must be a JSON object")
    manifest = json.loads((directory / "manifest.json").read_text())
    path = directory / "model.joblib"
    if (manifest["version"] != VERSION or manifest["scikitLearnVersion"] != sklearn.__version__
            or manifest["policySourceSha256"] != sha256(Path(__file__).read_bytes()).hexdigest()
            or manifest["policyDependenciesSha256"] != dependency_hashes()
            or sha256(path.read_bytes()).hexdigest() != manifest["artifactSha256"]):
        raise ValueError("policy artifact version or digest mismatch")
    obs = row.get("observation") if isinstance(row.get("observation"), dict) else {}
    placement = obs.get("placement") if isinstance(obs.get("placement"), dict) else {}
    out = {"mode": "shadow", "action": "ABSTAIN", "modelVersion": VERSION,
           "artifactSha256": manifest["artifactSha256"], "datasetSha256": manifest["datasetSha256"],
           "dataKind": manifest["guard"]["dataKind"], "observationId": obs.get("observationId"),
           "correlationId": obs.get("correlationId"), "revision": obs.get("revision"),
           "deploymentId": placement.get("deploymentId")}
    try:
        current = context.read_sample(row, manifest["guard"])
        context.features(current, current.max_num_seqs, manifest["features"])
    except (KeyError, TypeError, ValueError):
        return {**out, "reason": "incomplete_observation_or_load_test"}
    out.update({"currentMaxNumSeqs": current.max_num_seqs, "baseline": {"action": "KEEP", "measured": current.outcomes}})
    profile = context.workload_profile(current)
    if (context.scope(current) != manifest["scope"] or manifest["profiles"].get(profile["workloadId"]) != profile
            or context.support_key(current) not in manifest["support"]):
        return {**out, "reason": "outside_measured_scope"}
    if current.max_num_seqs != manifest["guard"]["baselineMaxNumSeqs"]:
        return {**out, "reason": "current_setting_not_in_validation"}
    if manifest.get("holdoutAccounting") != legacy.HOLDOUT_ACCOUNTING:
        return {**out, "reason": "holdout_requires_downtime_evaluation"}
    if manifest.get("validationPassed") is not True or manifest["holdoutPassed"] is not True:
        return {**out, "reason": "holdout_gate_failed"}
    model = joblib.load(path)
    kind = manifest["predictorKind"]
    if kind == "neighbors_by_workload":
        model = model[current.scope["workloadId"]]
    elif kind != "regressor":
        raise ValueError("unknown policy predictor kind")
    predictions = regression.predict(model, current, manifest["contextSettings"], manifest["features"], manifest["support"])
    if not predictions:
        return {**out, "reason": "current_metrics_outside_measured_scope"}
    ranked = (regression.ranked_forecast(current, predictions, manifest["validationMargin"], manifest["guard"])
              if kind == "regressor" else predictions)
    action, candidate, reason = legacy.choose(current, ranked, manifest["validationMargin"], manifest["guard"])
    out.update({"action": action, "candidateMaxNumSeqs": candidate, "reason": reason,
                "predictions": predictions, "validationMargin": manifest["validationMargin"]})
    if action == "UPDATE":
        out["inferenceArgs"] = ["--max-num-seqs", str(candidate)]
    return out


def main():
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
    shadow.add_argument("--transition-evidence", type=Path)
    args = parser.parse_args()
    try:
        if args.command == "train":
            result = train(args.dataset, json.loads(args.guard.read_text()), args.destination)
        else:
            if args.transition_evidence and not args.publish_dir:
                raise ValueError("--transition-evidence requires --publish-dir")
            row = json.loads(args.sample.read_text())
            result = preview(row, args.artifact)
            if args.publish_dir:
                transition = json.loads(args.transition_evidence.read_text()) if args.transition_evidence else None
                if transition is not None and not isinstance(transition, dict):
                    raise ValueError("transition evidence must be an object")
                result["proposalId"] = publish(row, result, json.loads((args.artifact / "manifest.json").read_text()),
                                               args.publish_dir, transition)
    except (KeyError, OSError, TypeError, ValueError) as error:
        parser.exit(1, f"{error}\n")
    print(json.dumps(result, indent=2, allow_nan=False))


if __name__ == "__main__":
    main()
