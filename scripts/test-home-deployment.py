#!/usr/bin/env python3
"""Exercise the real panel binary against local fake Home APIs, then restart it."""
import argparse
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen

BASE = "/v0/management/plugins/cpa-codex-candy-eval"
HOME_KEY = "synthetic-management-key"
PANEL_KEY = "synthetic-panel-key"
ACCOUNT = "00000000-0000-0000-0000-000000000001"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", default="./dist/cpa-home-codex-eval")
    args = parser.parse_args()
    binary = str(Path(args.binary).resolve())
    calls, failures = [], []
    lock = threading.Lock()

    class MockHome(BaseHTTPRequestHandler):
        def log_message(self, *_args):
            pass

        def respond(self, status, body):
            raw = json.dumps(body, ensure_ascii=False).encode()
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(raw)))
            self.end_headers()
            self.wfile.write(raw)

        def do_GET(self):
            self.handle_api()

        def do_POST(self):
            self.handle_api()

        def handle_api(self):
            try:
                assert self.headers.get("Authorization") == "Bearer " + HOME_KEY
                route = self.path.split("?")[0]
                if route == "/v8/management/credentials":
                    result = {"files": [{"id": ACCOUNT, "auth_index": ACCOUNT, "name": "mock-codex.json", "provider": "codex", "source": "db", "priority": 0, "disabled": False, "email": "mock@example.test"}]}
                elif route == "/v8/management/credentials/models":
                    result = {"models": [{"id": "gpt-test"}]}
                elif route == "/v8/management/credentials/download":
                    result = {"account_id": "mock-chatgpt-account", "plan_type": "plus", "access_token": "must-stay-server-side", "refresh_token": "never-expose-this"}
                elif route == "/v8/management/config/oauth/model-alias/codex":
                    result = []
                elif route == "/v8/management/requests/api-call":
                    assert self.command == "POST"
                    req = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                    assert req["auth_index"] == ACCOUNT
                    assert req["url"] == "https://chatgpt.com/backend-api/codex/responses"
                    assert req["method"] == "POST"
                    assert req["header"]["Authorization"] == "Bearer $TOKEN$"
                    assert req["header"]["Chatgpt-Account-Id"] == "mock-chatgpt-account"
                    body = json.loads(req["data"])
                    assert body["model"] == "gpt-test" and body["stream"] is True and body["store"] is False
                    assert isinstance(body["input"], list) and "temperature" not in body
                    text = json.dumps(body, ensure_ascii=False)
                    if "黑色的袋子" in text:
                        kind, answer = "candy", "最少取出 21 个。"
                    elif "1 到 355" in text:
                        kind, answer = "modeltrace", ", ".join(str((i * 73) % 355 + 1) for i in range(335))
                    elif "鹈鹕" in text:
                        kind, answer = "pelican", '<!doctype html><html><body><svg xmlns="http://www.w3.org/2000/svg"><circle r="20" cx="30" cy="30"/></svg></body></html>'
                    else:
                        kind = "fingerprint"
                        if "color" in text:
                            answer = "red"
                        elif "animal" in text:
                            answer = "cat"
                        else:
                            answer = "42"
                    with lock:
                        calls.append(kind)
                    response = {"status": "completed", "output": [{"type": "message", "content": [{"type": "output_text", "text": answer}]}], "usage": {"input_tokens": 10, "output_tokens": 5}}
                    result = {"status_code": 200, "body": 'data: ' + json.dumps({"type": "response.completed", "response": response}, ensure_ascii=False) + '\n\n'}
                else:
                    raise AssertionError("unexpected Home route: " + route)
                self.respond(200, result)
            except Exception as error:
                with lock:
                    failures.append(str(error))
                self.respond(500, {"error": "mock rejected request"})

    home = ThreadingHTTPServer(("127.0.0.1", 0), MockHome)
    home.daemon_threads = True
    thread = threading.Thread(target=home.serve_forever, daemon=True)
    thread.start()
    with socket.socket() as reservation:
        reservation.bind(("127.0.0.1", 0))
        panel_port = reservation.getsockname()[1]
    panel_origin = f"http://127.0.0.1:{panel_port}"

    def request(route, method="GET", body=None, key=PANEL_KEY):
        headers = {"Authorization": "Bearer " + key}
        data = None if body is None else json.dumps(body).encode()
        if data is not None:
            headers["Content-Type"] = "application/json"
        req = Request(panel_origin + route, data=data, method=method, headers=headers)
        try:
            with urlopen(req, timeout=5) as response:
                status, raw = response.status, response.read().decode()
        except HTTPError as error:
            status, raw = error.code, error.read().decode()
        for secret in (HOME_KEY, "must-stay-server-side", "never-expose-this"):
            assert secret not in raw, "panel exposed a backend secret"
        return status, raw

    process = None
    try:
        with tempfile.TemporaryDirectory(prefix="cpa-home-eval-simulation-") as tmp:
            env = dict(os.environ, CPA_HOME_URL=f"http://127.0.0.1:{home.server_port}", CPA_HOME_MANAGEMENT_KEY=HOME_KEY, CPA_HOME_EVAL_KEY=PANEL_KEY, CPA_HOME_EVAL_LISTEN=f"127.0.0.1:{panel_port}", CPA_HOME_EVAL_DATA_DIR=tmp)

            def start():
                child = subprocess.Popen([binary], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, text=True)
                deadline = time.monotonic() + 10
                while time.monotonic() < deadline:
                    if child.poll() is not None:
                        raise RuntimeError("panel failed to start: " + child.stderr.read())
                    try:
                        if request("/")[0] == 200:
                            return child
                    except (URLError, ConnectionError):
                        pass
                    time.sleep(0.05)
                child.terminate()
                child.wait(timeout=5)
                raise RuntimeError("panel did not start")

            def stop(child):
                child.terminate()
                child.wait(timeout=5)
                assert child.returncode == 0, "panel shutdown failed"
                child.stderr.close()

            process = start()
            assert request(BASE + "/state", key="wrong")[0] == 401
            assert request("/v8/management/credentials/download?id=" + ACCOUNT)[0] == 404
            assert request("/v0/management/auth-files/status", "PATCH", {"name": ACCOUNT, "disabled": True})[0] == 404
            assert request("/api/home/catalog")[0] == 200
            expected = {}
            for kind, route, history, running in (("candy", "/run", "results", "running"), ("fingerprint", "/fingerprint/run", "fingerprints", "fingerprint_running"), ("modeltrace", "/modeltrace/run", "modeltraces", "modeltrace_running"), ("pelican", "/pelican/run", "pelicans", "pelican_running")):
                body = {"auth_ids": [ACCOUNT], "model": "gpt-test", "effort": "low", "runs": 1, "mode": "quick", "concurrency": 3}
                assert request(BASE + route, "POST", body)[0] == 200, "could not start " + kind
                deadline = time.monotonic() + 30
                while True:
                    status, raw = request(BASE + "/state")
                    assert status == 200
                    auth = json.loads(raw)["auths"][0]
                    if auth[history] and not auth.get(running):
                        break
                    assert time.monotonic() < deadline, kind + " did not finish"
                    time.sleep(0.05)
                result = auth[history][0]
                assert not result.get("error"), (kind, result)
                if kind == "candy":
                    assert result["ok"] is True
                if kind == "fingerprint":
                    assert result["done"] == 60 and result["valid"] == 60 and result["errors"] == 0
                if kind in ("fingerprint", "modeltrace"):
                    assert result["status"] == "completed"
                if kind != "candy":
                    assert request(BASE + f"/{kind}/record?auth_id={ACCOUNT}&id={result['id']}")[0] == 200
                expected[history] = result
            assert not failures, failures
            assert {kind: calls.count(kind) for kind in set(calls)} == {"candy": 1, "fingerprint": 60, "modeltrace": 3, "pelican": 1}, calls
            stop(process)
            process = None
            saved = json.loads(Path(tmp, "cpa-home-codex-eval-state.json").read_text())
            assert len(saved["results"][ACCOUNT]) == 1
            process = start()
            restored = json.loads(request(BASE + "/state")[1])["auths"][0]
            for history, result in expected.items():
                assert restored[history] == [result], "history not restored: " + history
            stop(process)
            process = None
            print(f"Simulated deployment passed: separate ports Home={home.server_port}, panel={panel_port}; all four tests; 65 mock model calls; history restored after restart; no real accounts used.")
    finally:
        if process is not None and process.poll() is None:
            process.terminate()
            process.wait(timeout=5)
        home.shutdown()
        home.server_close()


if __name__ == "__main__":
    main()
