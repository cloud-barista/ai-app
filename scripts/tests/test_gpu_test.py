import json
import os
from pathlib import Path
import subprocess
import sys
from tempfile import TemporaryDirectory
import unittest


SCRIPT = Path(__file__).resolve().parents[1] / "gpu-test.sh"
FAKE_CURL = r'''
import json
import os
from pathlib import Path
import sys
from urllib.parse import urlsplit

args = sys.argv[1:]
url = next(value for value in args if value.startswith(("http://", "https://")))
path = urlsplit(url).path
method = args[args.index("-X") + 1] if "-X" in args else ("POST" if "-d" in args else "GET")
headers = [args[index + 1] for index, value in enumerate(args) if value == "-H"]
call = {"method": method, "path": path, "headers": headers}
if "-d" in args:
    call["body"] = json.loads(args[args.index("-d") + 1])
with open(os.environ["GPU_TEST_CALLS"], "a") as stream:
    stream.write(json.dumps(call) + "\n")

dry_run = os.environ.get("GPU_TEST_DRY_RUN", "true") == "true"
status = os.environ.get("GPU_TEST_FINAL_STATUS", "dry-run" if dry_run else "running")
node = {"target": {"nsId": "default", "infraId": "gpu-test", "nodeId": "gpu-1"},
        "status": status, "gpu": True, "gpuCheck": "CUDA Tesla T4"}
record = {"id": "test-deployment", "type": "inference", "status": status,
          "dryRun": dry_run, "nodes": [node]}
code = 200
if path.endswith("/readyz"):
    body = {"message": "Service is ready"}
elif path.endswith("/status"):
    body = {"dryRun": dry_run, "tumblebug": {"reachable": True}, "stage": {"writable": True},
            "semaphore": {"authenticated": os.environ.get("GPU_TEST_SEMAPHORE", "true") == "true"}}
elif path.endswith("/deployments") and method == "POST":
    body = {"id": "test-deployment", "status": "pending"}
    code = 202
elif path.endswith("/progress"):
    body = {"done": True, "status": status, "nodes": [node]}
elif path.endswith("/deployments/test-deployment"):
    body = record
elif path.endswith("/endpoints"):
    endpoint = {"available": True, "runtimeId": "ollama", "modelId": "qwen3-vl-8b",
                "url": "http://192.0.2.10:11434/v1"}
    body = {"endpoints": [] if os.environ.get("GPU_TEST_NO_ENDPOINT") else [endpoint]}
elif path == "/api/generate":
    body = {"model": "qwen3-vl:8b", "done": True,
            "response": os.environ.get("GPU_TEST_ANSWER", "4"), "eval_count": 1}
else:
    body = {"message": "Unknown fixture route"}
    code = 404
print(json.dumps(body))
if "-w" in args:
    print(code)
if code >= 400 and "--fail-with-body" in args:
    sys.exit(22)
'''


class GpuTestScriptTests(unittest.TestCase):
    def setUp(self):
        directory = TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name)
        self.calls = self.root / "calls.jsonl"
        curl = self.root / "curl"
        curl.write_text(f"#!{sys.executable}\n" + FAKE_CURL)
        curl.chmod(0o755)
        docker = self.root / "docker"
        docker.write_text(
            f"#!{sys.executable}\n"
            "import json, os, sys\n"
            "with open(os.environ['GPU_TEST_CALLS'], 'a') as stream:\n"
            "    stream.write(json.dumps({'docker': sys.argv[1:]}) + '\\n')\n"
        )
        docker.chmod(0o755)

    def run_script(self, command, *arguments, **settings):
        env = dict(os.environ)
        env.update(
            PATH=str(self.root) + os.pathsep + env.get("PATH", ""),
            GPU_TEST_CALLS=str(self.calls),
            AIAPP_URL="http://test.invalid:8090/aiapp",
            AIAPP_AUTH_TOKEN="fixture-token",
        )
        env.update(settings)
        return subprocess.run(
            ["bash", str(SCRIPT), command, *arguments],
            env=env, stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=15,
        )

    def requests(self):
        if not self.calls.exists():
            return []
        return [json.loads(line) for line in self.calls.read_text().splitlines()]

    def test_up_uses_existing_compose_profile(self):
        result = self.run_script("up")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.requests(), [{"docker": ["compose", "--profile", "deploy", "up", "-d", "--build"]}])

    def test_status_uses_existing_status_example(self):
        result = self.run_script("status")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual([call["path"] for call in self.requests()], ["/aiapp/readyz", "/aiapp/status"])

    def test_dry_run_succeeds_without_model_invocation(self):
        result = self.run_script("dry-run", "default", "gpu-test", "gpu-1")
        self.assertEqual(result.returncode, 0, result.stderr)
        calls = self.requests()
        post = next(call for call in calls if call["method"] == "POST")
        self.assertTrue(post["body"]["inference"]["gpu"])
        self.assertEqual(post["body"]["targets"][0]["nodeId"], "gpu-1")
        self.assertFalse(any(call["path"] == "/api/generate" for call in calls))

    def test_wrong_mode_stops_before_deployment(self):
        result = self.run_script("deploy", "default", "gpu-test", "gpu-1")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("AIAPP_TUMBLEBUG_DRY_RUN=false", result.stderr)
        self.assertFalse(any(call["method"] == "POST" for call in self.requests()))

    def test_failed_deployment_is_an_error(self):
        for status in ("failed", "partial", "interrupted"):
            with self.subTest(status=status):
                result = self.run_script(
                    "deploy", "default", "gpu-test", "gpu-1",
                    GPU_TEST_DRY_RUN="false", GPU_TEST_FINAL_STATUS=status,
                )
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("06-logs.sh test-deployment", result.stderr)
        self.assertFalse(any(call["path"] == "/api/generate" for call in self.requests()))

    def test_semaphore_authentication_is_checked_before_deployment(self):
        result = self.run_script(
            "deploy", "default", "gpu-test", "gpu-1",
            GPU_TEST_DRY_RUN="false", GPU_TEST_SEMAPHORE="false",
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Semaphore", result.stderr)
        self.assertFalse(any(call["method"] == "POST" for call in self.requests()))

    def test_deploy_checks_inference_without_forwarding_gateway_token(self):
        result = self.run_script("deploy", "default", "gpu-test", "gpu-1", GPU_TEST_DRY_RUN="false")
        self.assertEqual(result.returncode, 0, result.stderr)
        calls = self.requests()
        model_call = next(call for call in calls if call["path"] == "/api/generate")
        self.assertEqual(model_call["body"]["model"], "qwen3-vl:8b")
        self.assertNotIn("Authorization: Bearer fixture-token", model_call["headers"])
        gateway_call = next(call for call in calls if call["path"] == "/aiapp/status")
        self.assertIn("Authorization: Bearer fixture-token", gateway_call["headers"])

    def test_missing_endpoint_and_empty_answer_are_errors(self):
        for setting in ({"GPU_TEST_NO_ENDPOINT": "true"}, {"GPU_TEST_ANSWER": ""}, {"GPU_TEST_ANSWER": " \n"}):
            with self.subTest(setting=setting):
                result = self.run_script("test", "default", "gpu-test", "gpu-1", **setting)
                self.assertNotEqual(result.returncode, 0)

    def test_invalid_command_does_not_call_services(self):
        result = self.run_script("unknown")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.requests(), [])


if __name__ == "__main__":
    unittest.main()
