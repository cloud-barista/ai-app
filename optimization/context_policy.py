"""Context-aware scheduler policy; the original policy remains reproducible."""

import argparse
from dataclasses import replace
from hashlib import sha256
import json
import math
from pathlib import Path
from statistics import mean, median, stdev

import joblib
import sklearn
from sklearn.ensemble import RandomForestRegressor
from sklearn.multioutput import MultiOutputRegressor

if __package__:
    from . import policy as legacy
    from .publication import publish
else:
    import policy as legacy
    from publication import publish


VERSION = "khu-vllm-max-num-seqs/v3"
BASE_FEATURES = ("offeredRps", "candidateMaxNumSeqs", *legacy.OUTPUTS)
CONTEXT_FEATURES = ("promptTokensP50", "maxOutputTokens", "waitingRequests", "meanGPUUtilization", "minimumFreeVRAM")
OPTIONAL_FEATURES = ("promptTokensP95", "kvCacheUsage")
SUPPORT_STDDEV_MULTIPLIER = 3.0


def bounded(value, lower, upper):
    return ((lower <= value or math.isclose(value, lower, rel_tol=1e-12, abs_tol=1e-12))
            and (value <= upper or math.isclose(value, upper, rel_tol=1e-12, abs_tol=1e-12)))


def within_baseline_bounds(current, bounds):
    return all(bounded(value, *bounds[str(current.offered_rps)][key])
               for key, value in current.outcomes.items())


def validation_margin(groups, forecast, baseline):
    margin = legacy.ZERO_MARGIN.copy()
    for windows in windows_by_context(groups):
        predictions = forecast(windows[baseline])
        for setting, window in windows.items():
            if setting != baseline:
                for key in legacy.OUTPUTS:
                    margin[key] = max(margin[key], abs(window.outcomes[key] - predictions[setting][key]))
    return margin


def context(current):
    return current.scope["workloadId"], current.offered_rps


def workload_profile(current):
    out = {key: current.scope[key] for key in ("workloadId", "promptTokensP50", "maxOutputTokens")}
    if "promptTokensP95" in current.observation["policyLoad"]:
        out["promptTokensP95"] = legacy.count(current.observation["policyLoad"]["promptTokensP95"], 1)
    return out


def scope(current):
    return {key: value for key, value in current.scope.items()
            if key not in ("workloadId", "promptTokensP50", "maxOutputTokens")}


def windows_by_context(groups):
    for rows in groups:
        grouped = {}
        for row in rows:
            grouped.setdefault(context(row), {})[row.max_num_seqs] = row
        yield from grouped.values()


def features(current, setting, names):
    metrics = current.observation["metrics"]
    gpus = metrics["gpus"].values()
    values = {"offeredRps": current.offered_rps, "candidateMaxNumSeqs": setting,
              **current.outcomes, "promptTokensP50": current.scope["promptTokensP50"],
              "maxOutputTokens": current.scope["maxOutputTokens"]}
    if "waitingRequests" in names:
        values["waitingRequests"] = legacy.measurement(metrics["serving"]["waitingRequests"])
    if "meanGPUUtilization" in names:
        values["meanGPUUtilization"] = mean(legacy.measurement(gpu["meanUtilization"]) for gpu in gpus)
        if values["meanGPUUtilization"] > 100:
            raise ValueError("invalid GPU utilization")
    if "minimumFreeVRAM" in names:
        values["minimumFreeVRAM"] = min(legacy.measurement(gpu["minimumFreeVRAM"]) for gpu in gpus)
    if "promptTokensP95" in names:
        values["promptTokensP95"] = legacy.count(current.observation["policyLoad"]["promptTokensP95"], 1)
        if values["promptTokensP95"] < values["promptTokensP50"]:
            raise ValueError("invalid prompt token percentiles")
    if "kvCacheUsage" in names:
        values["kvCacheUsage"] = legacy.measurement(metrics["serving"]["kvCacheUsage"])
        if values["kvCacheUsage"] > 1:
            raise ValueError("invalid KV cache utilization")
    return tuple(legacy.number(values[name]) for name in names)


def read_sample(row, guard):
    current = legacy.read_sample(row, guard)
    # The load report stays separate from the signed observation in publications.
    observation = {**current.observation, "policyLoad": row["load"]}
    return replace(current, observation=observation)


def split_samples(samples, baseline):
    ordered = sorted(samples, key=lambda row: row.start)
    if not ordered or len({row.observation["observationId"] for row in ordered}) != len(ordered):
        raise ValueError("empty dataset or duplicate observations")
    if any(scope(row) != scope(ordered[0]) for row in ordered):
        raise ValueError("mixed node, GPU, model, application or runtime scopes")
    if any(a.end > b.start for a, b in zip(ordered, ordered[1:])):
        raise ValueError("measurement windows overlap")
    groups = {}
    profiles = {}
    for row in ordered:
        groups.setdefault(row.experiment, []).append(row)
        profile = workload_profile(row)
        previous = profiles.setdefault(profile["workloadId"], profile)
        if previous != profile:
            raise ValueError("workload token metadata changed")
    experiments = list(groups.values())
    grid = {(context(row), row.max_num_seqs) for row in experiments[0]}
    if len(experiments) < 5 or any(a[-1].end > b[0].start for a, b in zip(experiments, experiments[1:])):
        raise ValueError("supply five chronological, non-interleaved experiment groups")
    if any(len(group) != len(grid) or {(context(row), row.max_num_seqs) for row in group} != grid
           for group in experiments):
        raise ValueError("repeat the same workload, load and setting grid in each experiment")
    for windows in windows_by_context(experiments[:1]):
        if baseline not in windows or len(windows) < 2:
            raise ValueError("each context needs a baseline and candidate")
    for workload in profiles:
        if len({row.offered_rps for row in ordered if row.scope["workloadId"] == workload}) < 2:
            raise ValueError("each workload needs at least two offered rates")
    n = len(experiments)
    return profiles, (experiments[:n * 3 // 5], experiments[n * 3 // 5:n * 4 // 5], experiments[n * 4 // 5:])


def forecast(model, current, settings, names):
    candidates = [setting for setting in settings if setting != current.max_num_seqs]
    predicted = model.predict([features(current, setting, names) for setting in candidates]).tolist()
    return {current.max_num_seqs: current.outcomes,
            **{setting: dict(zip(legacy.OUTPUTS, values)) for setting, values in zip(candidates, predicted)}}


def evaluate(groups, predictor, margin, guard):
    reports = [legacy.evaluate([list(windows.values())], predictor, margin, guard)
               for windows in windows_by_context(groups)]
    totals = {key: sum(report[key] for report in reports)
              for key in ("windows", "updates", "unsafeUpdates", "weakUpdates", "sloBreaches")}
    return {**totals, **{key: mean(report[key] for report in reports)
                        for key in ("meanGoodputRps", "meanEffectiveGoodputRps")},
            "abstentions": sum(legacy.choose(windows[guard["baselineMaxNumSeqs"]],
                                predictor(windows[guard["baselineMaxNumSeqs"]]), margin, guard)[0] == "ABSTAIN"
                               for windows in windows_by_context(groups))}


def support_key(current):
    return json.dumps(context(current), separators=(",", ":"))


def supported(current, support, names):
    limits = support.get(support_key(current))
    values = dict(zip(names, features(current, current.max_num_seqs, names)))
    return limits is not None and all(bounded(values[key], *interval) for key, interval in limits.items())


def variation_bounds(values):
    pad = SUPPORT_STDDEV_MULTIPLIER * stdev(values) if len(values) > 1 else 0.0
    return [max(0, min(values) - pad), max(values) + pad]


def fixed_reference(groups, setting, guard):
    contexts = list(windows_by_context(groups))
    values = [windows[setting].outcomes for windows in contexts]
    changes = setting != guard["baselineMaxNumSeqs"]
    fraction = legacy.update_serving_fraction(guard) if changes else 1.0
    safe = [legacy.limits_ok(value, legacy.ZERO_MARGIN, guard) for value in values]
    return {"maxNumSeqs": setting, "windows": len(values), "updates": len(values) if changes else 0,
            "unsafeUpdates": sum(not value for value in safe) if changes else 0,
            "weakUpdates": sum(windows[setting].outcomes["goodputRps"] * fraction
                               <= windows[guard["baselineMaxNumSeqs"]].outcomes["goodputRps"] * (1 + guard["minimumGainFraction"])
                               for windows in contexts) if changes else 0,
            "sloBreaches": sum(not value for value in safe), "abstentions": 0,
            "meanGoodputRps": mean(value["goodputRps"] for value in values),
            "meanEffectiveGoodputRps": mean(value["goodputRps"] * fraction for value in values)}


def train(dataset, guard, destination):
    baseline = legacy.count(guard["baselineMaxNumSeqs"], 1)
    for key in ("p95LimitSeconds", "peakVRAMLimitMiB", "minimumGainFraction"):
        legacy.number(guard[key], 1e-6)
    if legacy.number(guard["errorRateLimit"]) > 1 or guard["dataKind"] not in ("measurement", "fixture"):
        raise ValueError("invalid policy guard")
    legacy.update_serving_fraction(guard)
    raw = dataset.read_bytes()
    samples = [read_sample(json.loads(line), guard) for line in raw.splitlines() if line.strip()]
    profiles, (training, validation, test) = split_samples(samples, baseline)
    fit = [row for group in training for row in group]
    context_settings = {support_key(windows[baseline]): sorted(windows)
                        for windows in windows_by_context(training[:1])}
    settings = sorted({setting for measured in context_settings.values() for setting in measured})
    context_names = (*BASE_FEATURES, *CONTEXT_FEATURES)
    for name in OPTIONAL_FEATURES:
        available = (all("promptTokensP95" in row.observation["policyLoad"] for row in fit)
                     if name == "promptTokensP95" else
                     all("kvCacheUsage" in row.observation["metrics"]["serving"] for row in fit))
        if available:
            context_names += (name,)
    models = []
    for names in (BASE_FEATURES, context_names):
        inputs, outcomes = [], []
        for windows in windows_by_context(training):
            current = windows[baseline]
            for setting, candidate in windows.items():
                if setting != baseline:
                    inputs.append(features(current, setting, names))
                    outcomes.append([candidate.outcomes[key] for key in legacy.OUTPUTS])
        model = MultiOutputRegressor(RandomForestRegressor(
            n_estimators=128, min_samples_leaf=2, random_state=42, n_jobs=1))
        model.fit(inputs, outcomes)
        predictor = lambda current: forecast(model, current, context_settings[support_key(current)], names)
        margin = validation_margin(validation, predictor, baseline)
        error = sum(margin[key] / scale for key, scale in zip(legacy.OUTPUTS, (
            guard["p95LimitSeconds"], max(row.offered_rps for row in fit), guard["peakVRAMLimitMiB"], 1)))
        models.append((error, len(names), names, model, margin))
    _, _, names, model, margin = min(models, key=lambda item: item[:2])
    support = {}
    lookup = {}
    for windows in windows_by_context(training):
        current = windows[baseline]
        key = support_key(current)
        support.setdefault(key, {name: [] for name in names})
        for name, value in zip(names, features(current, baseline, names)):
            support[key][name].append(value)
        for setting, row in windows.items():
            lookup.setdefault((key, setting), []).append(row.outcomes)
    support = {key: {name: variation_bounds(values) for name, values in metrics.items()}
               for key, metrics in support.items()}
    # Calibrate support on validation only; the final test never changes it.
    for windows in windows_by_context(validation):
        current = windows[baseline]
        for key, value in zip(names, features(current, baseline, names)):
            limits = support[support_key(current)][key]
            pad = max(limits[0] - value, value - limits[1], 0)
            limits[0], limits[1] = max(0, limits[0] - pad), limits[1] + pad
    lookup = {key: {name: median(row[name] for row in measured) for name in legacy.OUTPUTS}
              for key, measured in lookup.items()}

    def measured(current):
        return {setting: (current.outcomes if setting == baseline else lookup[support_key(current), setting])
                for setting in context_settings[support_key(current)]}

    def predict(current):
        return forecast(model, current, context_settings[support_key(current)], names) if supported(current, support, names) else {}

    lookup_margin = validation_margin(validation, measured, baseline)
    report = {"randomForest": evaluate(test, predict, margin, guard),
              "measuredLookup": evaluate(test, measured, lookup_margin, guard),
              "keep": fixed_reference(test, baseline, guard)}
    safe_fixed = [setting for setting in settings if all(
        setting in windows and legacy.limits_ok(windows[setting].outcomes, legacy.ZERO_MARGIN, guard)
        for windows in windows_by_context(training))]
    selected_fixed = max(safe_fixed, key=lambda setting: mean(
        windows[setting].outcomes["goodputRps"] for windows in windows_by_context(training))) if safe_fixed else baseline
    report["fixedSetting"] = fixed_reference(test, selected_fixed, guard)
    rf = report["randomForest"]
    passed = (rf["updates"] > 0 and rf["unsafeUpdates"] == 0 and rf["weakUpdates"] == 0
              and rf["meanEffectiveGoodputRps"] > report["keep"]["meanEffectiveGoodputRps"]
              and rf["meanEffectiveGoodputRps"] >= report["measuredLookup"]["meanEffectiveGoodputRps"])
    destination.mkdir(parents=True, exist_ok=False)
    path = destination / "model.joblib"
    joblib.dump(model, path)
    manifest = {"version": VERSION, "scikitLearnVersion": sklearn.__version__,
                "policySourceSha256": sha256(Path(__file__).read_bytes()).hexdigest(),
                "datasetSha256": sha256(raw).hexdigest(), "artifactSha256": sha256(path.read_bytes()).hexdigest(),
                "scope": scope(fit[0]), "profiles": profiles, "maxNumSeqs": settings,
                "contextSettings": context_settings,
                "guard": guard, "features": names, "support": support,
                "supportStddevMultiplier": SUPPORT_STDDEV_MULTIPLIER, "validationMargin": margin,
                "lookupValidationMargin": lookup_margin, "trainingRows": sum(len(group) - 1 for group in windows_by_context(training)),
                "featureAblation": [{"features": item[2], "normalizedValidationError": item[0]} for item in models],
                "holdout": report, "holdoutPassed": passed, "holdoutAccounting": legacy.HOLDOUT_ACCOUNTING,
                "split": {name: [group[0].experiment for group in groups]
                          for name, groups in (("train", training), ("validation", validation), ("test", test))}}
    (destination / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    return manifest


def preview(row, directory):
    if not isinstance(row, dict):
        raise ValueError("a policy sample must be a JSON object")
    manifest = json.loads((directory / "manifest.json").read_text())
    path = directory / "model.joblib"
    if (manifest["version"] != VERSION or manifest["scikitLearnVersion"] != sklearn.__version__
            or sha256(path.read_bytes()).hexdigest() != manifest["artifactSha256"]):
        raise ValueError("policy artifact version or digest mismatch")
    obs = row.get("observation")
    obs = obs if isinstance(obs, dict) else {}
    placement = obs.get("placement")
    placement = placement if isinstance(placement, dict) else {}
    out = {"mode": "shadow", "action": "ABSTAIN", "modelVersion": VERSION,
           "artifactSha256": manifest["artifactSha256"], "datasetSha256": manifest["datasetSha256"],
           "dataKind": manifest["guard"]["dataKind"], "observationId": obs.get("observationId"),
           "correlationId": obs.get("correlationId"), "revision": obs.get("revision"),
           "deploymentId": placement.get("deploymentId")}
    try:
        current = read_sample(row, manifest["guard"])
        features(current, current.max_num_seqs, manifest["features"])
    except (KeyError, TypeError, ValueError):
        return {**out, "reason": "incomplete_observation_or_load_test"}
    out.update({"currentMaxNumSeqs": current.max_num_seqs,
                "baseline": {"action": "KEEP", "measured": current.outcomes}})
    profile = workload_profile(current)
    if (scope(current) != manifest["scope"] or manifest["profiles"].get(profile["workloadId"]) != profile
            or support_key(current) not in manifest["support"]):
        return {**out, "reason": "outside_measured_scope"}
    if current.max_num_seqs != manifest["guard"]["baselineMaxNumSeqs"]:
        return {**out, "reason": "current_setting_not_in_validation"}
    if manifest.get("holdoutAccounting") != legacy.HOLDOUT_ACCOUNTING:
        return {**out, "reason": "holdout_requires_downtime_evaluation"}
    if manifest["holdoutPassed"] is not True:
        return {**out, "reason": "holdout_gate_failed"}
    if not supported(current, manifest["support"], manifest["features"]):
        return {**out, "reason": "current_metrics_outside_measured_scope"}
    predictions = forecast(joblib.load(path), current, manifest["contextSettings"][support_key(current)], manifest["features"])
    action, candidate, reason = legacy.choose(current, predictions, manifest["validationMargin"], manifest["guard"])
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
