from datetime import datetime, timedelta, timezone
from hashlib import sha256
import json
from pathlib import Path
from tempfile import TemporaryDirectory
import unittest

from policy import preview, preview_artifact, train
from publication import publish


GUARD = {
    "dataKind": "fixture", "baselineMaxNumSeqs": 16, "p95LimitSeconds": 0.5,
    "peakVRAMLimitMiB": 12000, "errorRateLimit": 0.02, "minimumGainFraction": 0.1,
}


def rows():
    start = datetime(2026, 10, 1, tzinfo=timezone.utc)
    for experiment in range(10):
        for rps in (4, 8):
            for setting in (16, 32):
                sample_id = f"{experiment}-{rps}-{setting}"
                yield {
                    "experimentId": f"repeat-{experiment}",
                    "observation": {
                        "observationId": sample_id, "correlationId": "fixture-flow", "revision": experiment + 1,
                        "placement": {"nsId": "etri", "infraId": "travel", "nodeId": "vm1", "deploymentId": "dep-runtime"},
                        "inferenceRunId": "eng-runtime", "applicationRunId": "eng-app", "applicationDeploymentId": "dep-app",
                        "runtimeId": "vllm", "modelId": "model", "artifactId": "artifact", "specId": "test-gpu",
                        "applicationName": "langflow", "applicationVersion": 1, "maxNumSeqs": setting,
                        "appliedAt": (start - timedelta(minutes=1)).isoformat(),
                        "window": {"start": start.isoformat(), "end": (start + timedelta(minutes=3)).isoformat()},
                        "metrics": {
                            "complete": True,
                            "serving": {"latencyP95": {"quality": "measured", "value": 0.25 if setting == 16 else 0.2}},
                            "gpus": {"GPU-fixture": {"peakUsedVRAM": {"quality": "measured", "value": 7000 if setting == 16 else 8000}}},
                        },
                    },
                    "load": {
                        "observationId": sample_id, "workloadId": "fixed-requests-v1", "runtimeImage": "fixture-vllm",
                        "window": {"start": start.isoformat(), "end": (start + timedelta(minutes=3)).isoformat()},
                        "promptTokensP50": 128, "maxOutputTokens": 64, "offeredRps": rps, "sloSeconds": 0.5,
                        "attempted": 180 * rps, "failed": 0, "withinSLO": rps * (50 if setting == 16 else 160),
                    },
                }
                start += timedelta(minutes=4)


class SchedulerPolicyTests(unittest.TestCase):
    def train_fixture(self, directory: Path, samples, guard=GUARD):
        dataset = directory / "observations.jsonl"
        dataset.write_text("\n".join(json.dumps(sample) for sample in samples), encoding="utf-8")
        artifact = directory / "artifact"
        return artifact, train(dataset, guard, artifact)

    def test_model_preview_is_bound_to_scope_and_a_measured_configuration(self):
        samples = list(rows())
        with TemporaryDirectory() as tmp:
            guard = {**GUARD, "evaluationHorizonSeconds": 1200, "maxUpdateDowntimeSeconds": 120}
            artifact, manifest = self.train_fixture(Path(tmp), samples, guard)
            self.assertTrue(manifest["holdoutPassed"], manifest["holdout"])
            report = manifest["holdout"]["randomForest"]
            self.assertAlmostEqual(report["meanEffectiveGoodputRps"], report["meanGoodputRps"] * 0.9)
            self.assertEqual(manifest["split"]["test"], ["repeat-8", "repeat-9"])
            result = preview(samples[0], artifact)
            self.assertEqual(result["action"], "UPDATE")
            self.assertEqual(result["inferenceArgs"], ["--max-num-seqs", "32"])
            self.assertEqual(result["mode"], "shadow")
            self.assertEqual(result["dataKind"], "fixture")
            self.assertEqual(result["observationId"], "0-4-16")
            samples[0]["observation"]["observationId"] = sha256(b"fixture-observation").hexdigest()
            samples[0]["load"]["observationId"] = samples[0]["observation"]["observationId"]
            result = preview(samples[0], artifact)
            published = Path(tmp) / "proposals"
            proposal_id = publish(samples[0], result, manifest, published)
            self.assertEqual(publish(samples[0], result, manifest, published), proposal_id)
            raw = (published / (proposal_id + ".json")).read_bytes()
            self.assertEqual(sha256(raw).hexdigest(), proposal_id)
            payload = json.loads(raw)
            self.assertEqual(payload["decision"]["dataKind"], "fixture")
            self.assertEqual(payload["decision"]["candidateMaxNumSeqs"], 32)
            self.assertEqual(payload["decision"]["forecast"], result["predictions"][32])
            transition = {"correlationId": "prior-rollout", "revision": 2}
            measured_id = publish(samples[0], result, manifest, published, transition)
            measured = (published / (measured_id + ".json")).read_bytes()
            self.assertNotEqual(measured_id, proposal_id)
            self.assertEqual(sha256(measured).hexdigest(), measured_id)
            self.assertEqual(json.loads(measured)["transitionEvidence"], transition)
            prior = {**manifest, "guard": {**guard, "dataKind": "measurement"}}
            del prior["holdoutAccounting"]
            old = preview_artifact(samples[0], artifact, prior)
            self.assertEqual(old["action"], "ABSTAIN")
            self.assertEqual(old["reason"], "holdout_requires_downtime_evaluation")
            samples[0]["load"]["offeredRps"] = 32
            self.assertEqual(preview(samples[0], artifact)["reason"], "outside_measured_scope")
            samples[0]["load"]["window"] = samples[1]["load"]["window"]
            self.assertEqual(preview(samples[0], artifact)["reason"], "incomplete_observation_or_load_test")
            samples[0]["observation"]["metrics"]["complete"] = False
            self.assertEqual(preview(samples[0], artifact)["reason"], "incomplete_observation_or_load_test")

    def test_model_abstains_when_a_held_out_update_breaches_slo(self):
        samples = list(rows())
        samples[-1]["observation"]["metrics"]["serving"]["latencyP95"]["value"] = 0.8
        with TemporaryDirectory() as tmp:
            artifact, manifest = self.train_fixture(Path(tmp), samples)
            self.assertFalse(manifest["holdoutPassed"])
            self.assertGreater(manifest["holdout"]["randomForest"]["unsafeUpdates"], 0)
            result = preview(samples[0], artifact)
            self.assertEqual(result["action"], "ABSTAIN")
            self.assertEqual(result["reason"], "holdout_gate_failed")
            self.assertEqual(result["currentMaxNumSeqs"], 16)
            self.assertEqual(result["baseline"]["action"], "KEEP")
            self.assertEqual(result["baseline"]["measured"]["p95Seconds"], 0.25)
            self.assertNotIn("inferenceArgs", result)

    def test_candidate_forecast_uses_current_latency_at_the_same_load(self):
        samples = list(rows())
        for sample in samples:
            observation = sample["observation"]
            slow = observation["revision"] % 2 == 0
            latency = 0.25 if observation["maxNumSeqs"] == 16 else 0.20
            observation["metrics"]["serving"]["latencyP95"]["value"] = latency + (0.10 if slow else 0)
        with TemporaryDirectory() as tmp:
            artifact, manifest = self.train_fixture(Path(tmp), samples)
            self.assertTrue(manifest["holdoutPassed"], manifest["holdout"])
            fast, slow = (preview(samples[index], artifact) for index in (0, 4))
            self.assertEqual(fast["action"], "UPDATE")
            self.assertEqual(slow["action"], "UPDATE")
            self.assertLess(fast["predictions"][32]["p95Seconds"], slow["predictions"][32]["p95Seconds"])
            samples[0]["observation"]["metrics"]["serving"]["latencyP95"]["value"] = 0.49
            outside = preview(samples[0], artifact)
            self.assertEqual(outside["action"], "ABSTAIN")
            self.assertEqual(outside["reason"], "current_metrics_outside_measured_scope")

    def test_update_that_only_improves_raw_goodput_is_held(self):
        samples = list(rows())
        guard = {**GUARD, "evaluationHorizonSeconds": 120, "maxUpdateDowntimeSeconds": 90}
        with TemporaryDirectory() as tmp:
            artifact, manifest = self.train_fixture(Path(tmp), samples, guard)
            self.assertFalse(manifest["holdoutPassed"])
            self.assertEqual(manifest["holdout"]["randomForest"]["updates"], 0)
            self.assertEqual(preview(samples[0], artifact)["reason"], "holdout_gate_failed")

    def test_rejects_duplicate_observation_instead_of_counting_it_twice(self):
        samples = list(rows())
        with TemporaryDirectory() as tmp:
            with self.assertRaisesRegex(ValueError, "duplicate observations"):
                self.train_fixture(Path(tmp), [*samples, samples[0]])
