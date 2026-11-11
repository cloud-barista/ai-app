from datetime import datetime
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from threading import Thread
import unittest
from urllib.parse import urlsplit

from optimization.capture_load import capture, validate


PROFILE = {
    "model": "fixture-model",
    "runtimeImage": "vllm/vllm-openai@sha256:" + "a" * 64,
    "promptTokenIds": [[1, 2], [3, 4, 5], [6, 7]],
    "maxOutputTokens": 64,
}


class CaptureLoadTest(unittest.TestCase):
    def test_counts_failures_and_retains_the_window(self):
        requests = []

        class Endpoint(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_POST(self):
                request = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                requests.append((self.path, self.headers["Authorization"], request))
                status = 503 if request["prompt"][0] == 3 else 200
                body = json.dumps({
                    "choices": [{"finish_reason": "length"}],
                    "usage": {"prompt_tokens": 42 if request["prompt"][0] == 6 else len(request["prompt"])},
                }).encode()
                self.send_response(status)
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

        with ThreadingHTTPServer(("127.0.0.1", 0), Endpoint) as server:
            thread = Thread(target=server.serve_forever)
            thread.start()
            try:
                endpoint = urlsplit(f"http://127.0.0.1:{server.server_port}/v1/completions")
                validate(PROFILE, endpoint, 0.5, 120, 2)
                result = capture(PROFILE, endpoint, 1.5, 2, 2, "fixture-bearer")
            finally:
                server.shutdown()
                thread.join()
        self.assertEqual((result["attempted"], result["failed"], result["withinSLO"]), (3, 2, 1))
        self.assertEqual(result["offeredRps"], 1.5)
        self.assertEqual(result["promptTokensP50"], 2)
        self.assertEqual(result["maxOutputTokens"], 64)
        self.assertEqual({record[0] for record in requests}, {"/v1/completions"})
        self.assertEqual({record[1] for record in requests}, {"Bearer fixture-bearer"})
        self.assertEqual({tuple(record[2]["prompt"]) for record in requests}, {tuple(ids) for ids in PROFILE["promptTokenIds"]})
        self.assertTrue(all(record[2]["max_tokens"] == 64 and record[2]["stream"] is False for record in requests))
        self.assertNotIn("fixture-bearer", json.dumps(result))
        start, end = (datetime.fromisoformat(result["window"][key].replace("Z", "+00:00")) for key in ("start", "end"))
        self.assertEqual(start.microsecond, 0)
        self.assertEqual(end.microsecond, 0)
        self.assertGreaterEqual((end - start).total_seconds(), 2)

    def test_rejects_ambiguous_rate_or_target(self):
        endpoint = urlsplit("http://127.0.0.1:8000/v1/completions")
        with self.assertRaisesRegex(ValueError, "whole number"):
            validate(PROFILE, endpoint, 1, 121, 1)
        with self.assertRaisesRegex(ValueError, "whole number"):
            validate(PROFILE, endpoint, 0.501, 120, 1)
        with self.assertRaisesRegex(ValueError, "duration"):
            validate(PROFILE, endpoint, 3, 119, 1)
        with self.assertRaisesRegex(ValueError, "completions URL"):
            validate(PROFILE, urlsplit("http://user:pass@localhost/v1/completions"), 3, 120, 1)


if __name__ == "__main__":
    unittest.main()
