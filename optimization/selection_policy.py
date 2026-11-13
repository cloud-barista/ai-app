"""Select a scheduler predictor by guarded validation decisions."""

import argparse
from hashlib import sha256
import json
from pathlib import Path
from statistics import median
import warnings

import joblib
import numpy as np
import sklearn
from sklearn.ensemble import RandomForestRegressor
from sklearn.exceptions import ConvergenceWarning
from sklearn.gaussian_process import GaussianProcessRegressor
from sklearn.gaussian_process.kernels import ConstantKernel, Matern, WhiteKernel
from sklearn.multioutput import MultiOutputRegressor
from sklearn.pipeline import make_pipeline
from sklearn.preprocessing import StandardScaler

if __package__:
    from . import context_policy as context
    from .publication import publish
else:
    import context_policy as context
    from publication import publish

legacy = context.legacy
VERSION = "khu-vllm-max-num-seqs/v4"
SELECTION = "validation-guarded-goodput/v1"
ALGORITHMS = ("random_forest", "gaussian_process")


def selection_key(candidate):
    report = candidate["validation"]
    return (report["unsafeUpdates"], report["weakUpdates"],
            -report["meanEffectiveGoodputRps"], report["sloBreaches"], report["abstentions"],
            len(candidate["features"]), candidate["normalizedValidationError"])


def support_envelope(groups, names, baseline):
    observed = {}
    for windows in context.windows_by_context(groups):
        current = windows[baseline]
        values = observed.setdefault(context.support_key(current), {name: [] for name in names})
        for name, value in zip(names, context.features(current, baseline, names)):
            values[name].append(value)
    return {key: {name: context.variation_bounds(values) for name, values in fields.items()}
            for key, fields in observed.items()}


def feature_sets(groups, baseline):
    current = [windows[baseline] for windows in context.windows_by_context(groups)]
    names = (*context.BASE_FEATURES, *context.CONTEXT_FEATURES)
    for name in context.OPTIONAL_FEATURES:
        present = (all("promptTokensP95" in row.observation["policyLoad"] for row in current)
                   if name == "promptTokensP95" else
                   all("kvCacheUsage" in row.observation["metrics"]["serving"] for row in current))
        if present:
            names += (name,)
    try:
        for row in current:
            context.features(row, baseline, names)
    except (KeyError, TypeError, ValueError):
        return [context.BASE_FEATURES, (*context.BASE_FEATURES, "promptTokensP50", "maxOutputTokens")]
    return [context.BASE_FEATURES, (*context.BASE_FEATURES, "promptTokensP50", "maxOutputTokens"), names]


def new_model(algorithm, feature_count):
    if algorithm == "random_forest":
        return MultiOutputRegressor(RandomForestRegressor(
            n_estimators=128, min_samples_leaf=2, random_state=42, n_jobs=1))
    if algorithm == "gaussian_process":
        kernel = (ConstantKernel(1, (.01, 100)) *
                  Matern(length_scale=np.ones(feature_count), length_scale_bounds=(.1, 100), nu=1.5) +
                  WhiteKernel(.01, (1e-6, 1)))
        return make_pipeline(StandardScaler(), MultiOutputRegressor(GaussianProcessRegressor(
            kernel=kernel, normalize_y=True, random_state=42, n_restarts_optimizer=0)))
    raise ValueError("unknown scheduler predictor")


def predict(model, current, settings, names, support):
    try:
        if context.supported(current, support, names):
            result = context.forecast(model, current, settings[context.support_key(current)], names)
            for values in result.values():
                for key in legacy.OUTPUTS:
                    legacy.number(values[key])
                if values["errorRate"] > 1:
                    return {}
            return result
    except (KeyError, TypeError, ValueError):
        pass
    return {}


def ranked_forecast(current, predictions, margin, guard):
    action, best, _ = legacy.choose(current, predictions, margin, guard)
    if action != "UPDATE":
        return predictions
    error = margin["goodputRps"]
    current_upper = max(current.outcomes["goodputRps"], predictions[current.max_num_seqs]["goodputRps"] + error)
    feasible = [setting for setting, values in predictions.items()
                if setting != current.max_num_seqs and legacy.limits_ok(values, margin, guard)
                and (values["goodputRps"] - error) * legacy.update_serving_fraction(guard)
                > current_upper * (1 + guard["minimumGainFraction"])
                and predictions[best]["goodputRps"] - values["goodputRps"] <= 2 * error]
    # Overlapping empirical error bands do not establish a benefit from more concurrency.
    selected = min(feasible)
    return {current.max_num_seqs: predictions[current.max_num_seqs], selected: predictions[selected]}


def train(dataset, guard, destination):
    baseline = legacy.count(guard["baselineMaxNumSeqs"], 1)
    for key in ("p95LimitSeconds", "peakVRAMLimitMiB", "minimumGainFraction"):
        legacy.number(guard[key], 1e-6)
    if legacy.number(guard["errorRateLimit"]) > 1 or guard["dataKind"] not in ("measurement", "fixture"):
        raise ValueError("invalid policy guard")
    legacy.update_serving_fraction(guard)
    raw = dataset.read_bytes()
    samples = [context.read_sample(json.loads(line), guard) for line in raw.splitlines() if line.strip()]
    profiles, (training, validation, test) = context.split_samples(samples, baseline)
    settings = {context.support_key(windows[baseline]): sorted(windows)
                for windows in context.windows_by_context(training[:1])}
    models = []
    for algorithm in ALGORITHMS:
        for names in feature_sets(training + validation, baseline):
            inputs, targets = [], []
            for windows in context.windows_by_context(training):
                for setting, candidate in windows.items():
                    if setting != baseline:
                        inputs.append(context.features(windows[baseline], setting, names))
                        targets.append([candidate.outcomes[key] for key in legacy.OUTPUTS])
            model = new_model(algorithm, len(names))
            with warnings.catch_warnings(record=True) as caught:
                warnings.simplefilter("always", ConvergenceWarning)
                model.fit(inputs, targets)
            forecast = lambda current: context.forecast(model, current, settings[context.support_key(current)], names)
            margin = context.validation_margin(validation, forecast, baseline)
            support = support_envelope(training + validation, names, baseline)
            report = context.evaluate(validation, lambda current: ranked_forecast(
                current, predict(model, current, settings, names, support), margin, guard), margin, guard)
            error = sum(margin[key] / scale for key, scale in zip(legacy.OUTPUTS, (
                guard["p95LimitSeconds"], max(row.offered_rps for group in training for row in group),
                guard["peakVRAMLimitMiB"], 1)))
            candidate = {"algorithm": algorithm, "features": list(names), "normalizedValidationError": error,
                         "validation": report, "validationMargin": margin,
                         "fitWarnings": [str(item.message) for item in caught]}
            models.append((candidate, model))
    selected, model = min(models, key=lambda pair: selection_key(pair[0]))
    names, margin = selected["features"], selected["validationMargin"]
    # Validation extremes need a variation envelope, not an endpoint fitted to one observation.
    support = support_envelope(training + validation, names, baseline)
    lookup = {}
    for windows in context.windows_by_context(training):
        key = context.support_key(windows[baseline])
        for setting, row in windows.items():
            lookup.setdefault((key, setting), []).append(row.outcomes)
    lookup = {key: {name: median(row[name] for row in values) for name in legacy.OUTPUTS}
              for key, values in lookup.items()}

    def measured(current):
        return {setting: current.outcomes if setting == baseline else lookup[context.support_key(current), setting]
                for setting in settings[context.support_key(current)]}

    lookup_margin = context.validation_margin(validation, measured, baseline)
    report = {"selectedPolicy": context.evaluate(test, lambda current: ranked_forecast(
                  current, predict(model, current, settings, names, support), margin, guard), margin, guard),
              "measuredLookup": context.evaluate(test, measured, lookup_margin, guard),
              "keep": context.fixed_reference(test, baseline, guard)}
    all_settings = sorted({setting for measured_settings in settings.values() for setting in measured_settings})
    safe_fixed = [setting for setting in all_settings if all(
        setting in windows and legacy.limits_ok(windows[setting].outcomes, legacy.ZERO_MARGIN, guard)
        for windows in context.windows_by_context(training))]
    fixed = max(safe_fixed, key=lambda setting: sum(
        windows[setting].outcomes["goodputRps"] for windows in context.windows_by_context(training))) if safe_fixed else baseline
    report["fixedSetting"] = context.fixed_reference(test, fixed, guard)
    validation_keep = context.fixed_reference(validation, baseline, guard)
    validated = selected["validation"]
    validation_passed = (validated["updates"] > 0 and validated["unsafeUpdates"] == 0 and validated["weakUpdates"] == 0
                         and validated["meanEffectiveGoodputRps"] > validation_keep["meanEffectiveGoodputRps"])
    final = report["selectedPolicy"]
    passed = (validation_passed and final["updates"] > 0 and final["unsafeUpdates"] == 0 and final["weakUpdates"] == 0
              and final["meanEffectiveGoodputRps"] > report["keep"]["meanEffectiveGoodputRps"]
              and final["meanEffectiveGoodputRps"] >= report["measuredLookup"]["meanEffectiveGoodputRps"])
    destination.mkdir(parents=True, exist_ok=False)
    path = destination / "model.joblib"
    joblib.dump(model, path)
    manifest = {"version": VERSION, "algorithm": selected["algorithm"], "scikitLearnVersion": sklearn.__version__,
                "policySourceSha256": sha256(Path(__file__).read_bytes()).hexdigest(),
                "datasetSha256": sha256(raw).hexdigest(), "artifactSha256": sha256(path.read_bytes()).hexdigest(),
                "scope": context.scope(training[0][0]), "profiles": profiles, "features": names,
                "contextSettings": settings, "maxNumSeqs": all_settings, "guard": guard,
                "support": support, "supportStddevMultiplier": context.SUPPORT_STDDEV_MULTIPLIER,
                "supportCalibration": {"method": "pooled-training-validation-baselines",
                    "experiments": [group[0].experiment for group in training + validation], "finalTestExcluded": True},
                "selectionCriterion": SELECTION, "validationCandidates": [candidate for candidate, _ in models],
                "candidateRanking": "smallest-feasible-setting-with-overlapping-validation-error/v1",
                "validationPassed": validation_passed, "validationMargin": margin, "lookupValidationMargin": lookup_margin,
                "trainingRows": sum(len(windows) - 1 for windows in context.windows_by_context(training)),
                "holdout": report, "holdoutPassed": passed, "holdoutAccounting": legacy.HOLDOUT_ACCOUNTING,
                "split": {name: [group[0].experiment for group in groups]
                          for name, groups in (("train", training), ("validation", validation), ("test", test))}}
    (destination / "manifest.json").write_text(json.dumps(manifest, indent=2, allow_nan=False) + "\n")
    return manifest


def preview(row, directory):
    if not isinstance(row, dict):
        raise ValueError("a policy sample must be a JSON object")
    manifest = json.loads((directory / "manifest.json").read_text())
    path = directory / "model.joblib"
    if (manifest["version"] != VERSION or manifest["scikitLearnVersion"] != sklearn.__version__
            or manifest["policySourceSha256"] != sha256(Path(__file__).read_bytes()).hexdigest()
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
    predictions = predict(joblib.load(path), current, manifest["contextSettings"], manifest["features"], manifest["support"])
    if not predictions:
        return {**out, "reason": "current_metrics_outside_measured_scope"}
    action, candidate, reason = legacy.choose(current, ranked_forecast(
        current, predictions, manifest["validationMargin"], manifest["guard"]), manifest["validationMargin"], manifest["guard"])
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
