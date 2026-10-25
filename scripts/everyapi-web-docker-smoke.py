#!/usr/bin/env python3
"""Exercise a built Docker image with disposable data and a local upstream."""
import http.cookiejar
import http.server
import json
import os
import secrets
import socket
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.parse
import urllib.request

DOCKER = os.environ.get("DOCKER", "docker")
IMAGE = sys.argv[1] if len(sys.argv) > 1 else "everyapi-web:local"
UPSTREAM_KEY = "docker-smoke-upstream-123456789"
NAME = "everyapi-web-smoke-" + secrets.token_hex(4)


def docker(*args):
    return subprocess.check_output([DOCKER, *args], text=True, stderr=subprocess.STDOUT, timeout=60).strip()


class Upstream(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_args):
        pass

    def do_CONNECT(self):
        self.send_error(502)

    def answer(self, status, body):
        data = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def authorized(self):
        return (
            self.headers.get("Authorization") == "Bearer " + UPSTREAM_KEY
            or self.headers.get("x-api-key") == UPSTREAM_KEY
            or self.headers.get("x-goog-api-key") == UPSTREAM_KEY
        )

    def do_GET(self):
        if not self.authorized():
            return self.answer(401, {"error": "fixture requires upstream key"})
        if self.path == "/v1/models":
            return self.answer(200, {"object": "list", "data": [{"id": "docker-smoke-model", "object": "model"}]})
        if self.path == "/v1beta/models":
            return self.answer(200, {"models": [{"name": "models/docker-smoke-model", "supportedGenerationMethods": ["generateContent"]}]})
        self.answer(404, {})

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", "0"))))
        if not self.authorized():
            return self.answer(401, {"error": "wrong upstream credential"})
        if self.path == "/v1/chat/completions" and body.get("model") == "docker-smoke-model":
            return self.answer(200, {"id": "chat-docker-smoke", "object": "chat.completion", "model": "docker-smoke-model", "choices": [{"index": 0, "message": {"role": "assistant", "content": "container relay passed"}, "finish_reason": "stop"}]})
        self.answer(404, {})


def main():
    fixture = http.server.ThreadingHTTPServer(("0.0.0.0", 0), Upstream)
    fixture_thread = threading.Thread(target=fixture.serve_forever, daemon=True)
    fixture_thread.start()
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        port = sock.getsockname()[1]
    base = f"http://127.0.0.1:{port}"
    upstream = f"http://host.docker.internal:{fixture.server_port}"
    jar = http.cookiejar.CookieJar()
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(jar))

    def request(path, key=None, body=None, form=False):
        headers = {"Accept": "text/html" if path == "/" else "application/json"}
        if key:
            headers["Authorization"] = "Bearer " + key
        data = None
        if body is not None:
            headers["Origin"] = base
            headers["Content-Type"] = "application/x-www-form-urlencoded" if form else "application/json"
            data = (urllib.parse.urlencode(body) if form else json.dumps(body)).encode()
        req = urllib.request.Request(base + path, data=data, headers=headers)
        try:
            response = opener.open(req, timeout=25)
        except urllib.error.HTTPError as err:
            response = err
        with response:
            return response.status, response.read().decode()

    def start():
        docker("run", "-d", "--init", "--name", NAME,
               "--read-only", "--tmpfs", "/tmp:size=256m,mode=1777",
               "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
               "-p", f"127.0.0.1:{port}:8080", "-v", NAME + ":/var/lib/everyapi-web",
               "-e", "EVERYAPI_API_BASE=https://api.everyapi.ai",
               "-e", "EVERYAPI_PUBLIC_URL=" + base,
               "-e", "HTTP_PROXY=" + upstream, "-e", "HTTPS_PROXY=" + upstream,
               "-e", "NO_PROXY=host.docker.internal,127.0.0.1,localhost", IMAGE)
        deadline = time.monotonic() + 35
        while time.monotonic() < deadline:
            try:
                if request("/")[0] == 401:
                    return
            except (OSError, urllib.error.URLError):
                pass
            time.sleep(0.25)
        raise AssertionError("container did not serve its sign-in page")

    try:
        docker("volume", "create", NAME)
        start()
        admin = docker("exec", NAME, "cat", "/var/lib/everyapi-web/admin-key")
        client = docker("exec", NAME, "cat", "/var/lib/everyapi-web/client-key")
        assert len(admin) >= 16 and len(client) >= 16 and admin != client
        assert request("/v1/models")[0] == 401
        for key in [admin, UPSTREAM_KEY, "arbitrary-invalid-client-key"]:
            assert request("/v1/models", key)[0] == 401, "non-client key bypassed authentication"
        assert request("/_magpie/sign-in", body={"key": admin, "back": "/"}, form=True)[0] == 200
        assert request("/api/provider/save", body={"id": "everyapi", "name": "EveryAPI", "preset": "everyapi", "new": True,
                       "key": UPSTREAM_KEY, "chat": upstream + "/v1", "responses": upstream + "/v1",
                       "anthropic": upstream, "gemini": upstream + "/v1beta"})[0] == 200
        docker("exec", NAME, "everyapi-web", "--healthcheck")
        assert json.loads(docker("inspect", "--format", "{{json .Config.User}}", NAME)) == "65532:65532"
        status, body = request("/api/provider/models", body={"id": "everyapi"})
        assert status == 200 and json.loads(body)["count"] > 0, "Web model refresh failed"
        status, body = request("/v1/models", client)
        assert status == 200 and any(m["id"] == "everyapi/docker-smoke-model" for m in json.loads(body)["data"])
        status, body = request("/v1/chat/completions", client, {"model": "everyapi/docker-smoke-model", "messages": [{"role": "user", "content": "hello"}]})
        assert status == 200 and json.loads(body)["choices"][0]["message"]["content"] == "container relay passed"
        status, body = request("/api/caller-keys")
        key_id = json.loads(body)["keys"][0]["id"]
        assert request("/api/caller-keys/remove-key", body={"key": key_id})[0] == 200
        logs = docker("logs", NAME)
        assert all(key not in logs for key in [admin, client, UPSTREAM_KEY]), "startup log leaked credentials"
        docker("stop", "-t", "10", NAME)
        docker("rm", NAME)
        jar.clear()
        start()
        assert docker("exec", NAME, "cat", "/var/lib/everyapi-web/admin-key") == admin, "administrator key changed after recreation"
        assert request("/v1/models", client)[0] == 401, "recreated container restored a revoked key"
        assert request("/_magpie/sign-in", body={"key": admin, "back": "/"}, form=True)[0] == 200
        status, body = request("/api/providers")
        state = json.loads(body)
        assert status == 200 and state["gateway"]["requireClientKey"] and state["gateway"]["url"] == base
        assert any(m["id"] == "docker-smoke-model" for m in state["providers"][0]["models"]), "model list was lost after recreation"
        print("PASS: nonroot read-only container, blank-key startup, healthcheck, sign-in, Web provider setup, issued-key authentication, discovery, relay, persistent state and revocation")
    finally:
        subprocess.run([DOCKER, "rm", "-f", NAME], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=30)
        subprocess.run([DOCKER, "volume", "rm", NAME], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=30)
        fixture.shutdown()
        fixture.server_close()
        fixture_thread.join(timeout=5)


if __name__ == "__main__":
    main()
