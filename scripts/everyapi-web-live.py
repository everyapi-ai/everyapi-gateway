#!/usr/bin/env python3
"""Opt-in acceptance against an upstream; never part of the offline test suite."""
import argparse
import concurrent.futures
import http.cookiejar
import json
import os
from pathlib import Path
import signal
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", default="dist/everyapi-web")
    parser.add_argument("--key-file", required=True, type=Path)
    parser.add_argument("--model", required=True, help="Exact upstream model ID; no automatic model selection")
    parser.add_argument("--additional-model", action="append", default=[], help="Additional model checked on every selected protocol")
    parser.add_argument("--chat-only-model", action="append", default=[], help="Additional model checked on Chat only")
    parser.add_argument("--workers", type=int, choices=range(1, 5), default=1)
    parser.add_argument("--report-file", type=Path, help="Write sanitized per-model results, without credentials or response bodies")
    parser.add_argument("--region", choices=["global", "cn"], default="global")
    parser.add_argument("--api-base", help="Official EveryAPI origin or a loopback fixture origin")
    parser.add_argument("--protocols", nargs="+", choices=["chat", "chat-stream", "responses", "anthropic", "gemini"],
                        default=["chat", "chat-stream", "responses", "anthropic", "gemini"])
    args = parser.parse_args()
    root = args.api_base or ("https://api-cn.everyapi.ai" if args.region == "cn" else "https://api.everyapi.ai")
    target = urllib.parse.urlsplit(root)
    live = root in ["https://api.everyapi.ai", "https://api-cn.everyapi.ai"]
    if not live and not (target.scheme == "http" and target.hostname in ["127.0.0.1", "localhost"]
                         and not target.username and not target.password and not target.query
                         and not target.fragment and target.path in ["", "/"]):
        parser.error("--api-base must be an official origin or a loopback HTTP fixture")
    if args.key_file.stat().st_mode & 0o077:
        parser.error("key file must be private: chmod 600 <key-file>")
    key = args.key_file.read_text().strip()
    if not key or len(key) > 4096:
        parser.error("key file must contain one nonempty upstream key (maximum 4096 characters)")
    binary = str(Path(args.binary).resolve())
    full_models = list(dict.fromkeys([args.model, *args.additional_model]))
    all_models = list(dict.fromkeys([*full_models, *args.chat_only_model]))
    results = []
    child = None
    jar = http.cookiejar.CookieJar()
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(jar))
    port, internal_port = free_port(), free_port()
    while internal_port == port:
        internal_port = free_port()
    base = f"http://127.0.0.1:{port}"

    def request(path, credential=None, body=None, form=False, status=200, auth_header="Authorization"):
        headers = {"Accept": "text/html" if path == "/" else "application/json", "User-Agent": "EveryAPI-Web-Acceptance/0.1"}
        if credential:
            headers[auth_header] = "Bearer " + credential if auth_header == "Authorization" else credential
        if auth_header == "x-api-key":
            headers["anthropic-version"] = "2023-06-01"
        data = None
        if body is not None:
            headers["Origin"] = base
            headers["Content-Type"] = "application/x-www-form-urlencoded" if form else "application/json"
            data = (urllib.parse.urlencode(body) if form else json.dumps(body)).encode()
        try:
            response = opener.open(urllib.request.Request(base + path, data=data, headers=headers), timeout=60)
        except urllib.error.HTTPError as err:
            response = err
        with response:
            raw = response.read(4 * 1024 * 1024 + 1).decode()
            # Never print raw replies, request bodies, cookies, logs or credentials.
            if response.status != status:
                try:
                    error = json.loads(raw).get("error", {})
                    detail = error.get("message", "") if isinstance(error, dict) else ""
                except ValueError:
                    detail = "non-JSON error response"
                raise AssertionError(f"{path}: HTTP {response.status}, expected {status}: {str(detail).replace(key, '[redacted]')[:220]}")
            assert len(raw) <= 4 * 1024 * 1024, f"{path}: reply exceeded acceptance size limit"
            if path != "/api/provider/key":
                assert key not in raw, f"{path}: upstream credential appeared in response"
            return raw

    def json_request(path, **kwargs):
        return json.loads(request(path, **kwargs))

    def stop():
        nonlocal child
        if child is not None:
            def send(sig):
                try:
                    if os.name != "nt":
                        os.killpg(child.pid, sig)
                    elif child.poll() is None:
                        child.send_signal(sig)
                except ProcessLookupError:
                    pass
            try:
                send(signal.SIGTERM)
                child.wait(timeout=15)
            except subprocess.TimeoutExpired:
                pass
            finally:
                if child.poll() is None:
                    send(signal.SIGKILL if os.name != "nt" else signal.SIGTERM)
                child.wait(timeout=5)
        child = None

    with tempfile.TemporaryDirectory(prefix="everyapi-web-live-") as directory:
        data = Path(directory)
        env = os.environ.copy()
        for name in ["EVERYAPI_API_KEY", "EVERYAPI_WEB_KEY", "EVERYAPI_GATEWAY_KEY"]:
            env[name] = env[name + "_FILE"] = ""
        env.update(EVERYAPI_API_BASE=root, EVERYAPI_REGION=args.region, EVERYAPI_PUBLIC_URL=base,
                   EVERYAPI_INTERNAL_ADDR=f"127.0.0.1:{internal_port}")
        with (data / "process.log").open("w+") as log:
            def start():
                nonlocal child
                child = subprocess.Popen([binary, "--data-dir", directory, "--addr", f"127.0.0.1:{port}"],
                                         env=env, stdin=subprocess.DEVNULL, stdout=log, stderr=log,
                                         start_new_session=os.name != "nt")
                deadline = time.monotonic() + 25
                while time.monotonic() < deadline:
                    assert child.poll() is None, "gateway exited during startup; raw logs withheld"
                    try:
                        request("/", status=401)
                        return
                    except (OSError, urllib.error.URLError):
                        time.sleep(0.2)
                raise AssertionError("gateway did not serve its sign-in page within 25 seconds")

            try:
                start()
                admin = (data / "admin-key").read_text().strip()
                client = (data / "client-key").read_text().strip()
                for credential in [None, admin, key, "unissued-acceptance-key"]:
                    request("/v1/models", credential=credential, status=401)
                request("/_magpie/sign-in", body={"key": admin, "back": "/"}, form=True)
                subprocess.run([binary, "--addr", f"127.0.0.1:{port}", "--healthcheck"], check=True,
                               stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=5)
                # Exercise the management Web API, starting without a bootstrap upstream key.
                config = {"id": "everyapi", "preset": "everyapi", "name": "EveryAPI acceptance",
                          "key": key, "chat": root + "/v1", "responses": root + "/v1",
                          "anthropic": root, "gemini": root + "/v1beta", "models": all_models}
                json_request("/api/provider/save", body=config)
                models = json_request("/api/provider/models", body={"id": "everyapi"})
                discovered = {m["id"] for m in models["provider"]["models"]}
                assert set(all_models) <= discovered, "requested model was absent from upstream discovery"
                model = "everyapi/" + args.model
                listing = json_request("/v1/models", credential=client)
                assert {"everyapi/" + m for m in all_models} <= {m["id"] for m in listing["data"]}, "selected model was absent from client API"
                print("PASS: sign-in, healthcheck, Web key configuration, upstream model discovery and credential separation", flush=True)
                prompt = "Reply with the word OK."
                def probe(model_id, protocol):
                    model = "everyapi/" + model_id
                    chat = {"model": model, "max_tokens": 128, "messages": [{"role": "user", "content": prompt}]}
                    if protocol == "chat":
                        reply = json_request("/v1/chat/completions", credential=client, body=chat)
                        assert reply["choices"][0]["message"].get("content"), "Chat returned no assistant text"
                    elif protocol == "chat-stream":
                        raw = request("/v1/chat/completions", credential=client, body={**chat, "stream": True})
                        chunks = [json.loads(line[6:]) for line in raw.splitlines() if line.startswith("data: ") and line[6:] != "[DONE]"]
                        assert "data: [DONE]" in raw, "Chat stream ended without DONE"
                        assert any(c.get("choices", [{}])[0].get("delta", {}).get("content") for c in chunks if c.get("choices")), "Chat stream had no text delta"
                        assert not any(c.get("error") for c in chunks), "Chat stream contained an error"
                    elif protocol == "responses":
                        reply = json_request("/v1/responses", credential=client,
                                             body={"model": model, "input": prompt, "max_output_tokens": 128})
                        assert reply.get("status") == "completed", "Responses did not complete"
                        assert any(part.get("text") for item in reply.get("output", []) for part in item.get("content", []) if part.get("type") == "output_text"), "Responses returned no output text"
                    elif protocol == "anthropic":
                        reply = json_request("/v1/messages", credential=client, body=chat, auth_header="x-api-key")
                        assert any(c.get("text") for c in reply.get("content", []) if c.get("type") == "text"), "Anthropic returned no text"
                    else:
                        reply = json_request("/v1beta/models/" + urllib.parse.quote(model, safe="/") + ":generateContent", credential=client, auth_header="x-goog-api-key",
                                             body={"contents": [{"role": "user", "parts": [{"text": prompt}]}], "generationConfig": {"maxOutputTokens": 128}})
                        assert any(p.get("text") for c in reply.get("candidates", []) for p in c.get("content", {}).get("parts", [])), "Gemini returned no text"
                jobs = [(m, p) for m in full_models for p in dict.fromkeys(args.protocols)]
                jobs += [(m, "chat") for m in all_models if m not in full_models]
                with concurrent.futures.ThreadPoolExecutor(max_workers=args.workers) as pool:
                    pending = {pool.submit(probe, m, p): (m, p, time.monotonic()) for m, p in jobs}
                    for future in concurrent.futures.as_completed(pending):
                        m, p, started = pending[future]
                        result = {"region": args.region, "model": m, "protocol": p}
                        try:
                            future.result()
                            result["status"] = "pass"
                        except (AssertionError, OSError, ValueError, KeyError, IndexError) as err:
                            # Error assertions contain paths/statuses, never raw upstream bodies.
                            detail = str(err).replace(key, "[redacted]").replace(client, "[redacted]").replace(admin, "[redacted]")
                            result.update(status="fail", error=detail[:300])
                        result["elapsed_seconds"] = round(time.monotonic() - started, 2)
                        results.append(result)
                        print(f"{result['status'].upper()}: {args.region} {m} {p}" + (": " + result["error"] if "error" in result else ""), flush=True)
                        if args.report_file:
                            args.report_file.write_text(json.dumps({"upstream": root, "results": results}, indent=2) + "\n")
                keys = json_request("/api/caller-keys")["keys"]
                key_id = keys[0]["id"]
                rotated = json_request("/api/caller-keys/rotate-key", body={"key": key_id})["secret"]
                assert rotated and rotated != client, "rotation did not issue a new credential"
                request("/v1/models", credential=client, status=401)
                json_request("/v1/models", credential=rotated)
                stop()
                jar.clear()
                start()
                assert (data / "admin-key").read_text().strip() == admin, "administrator key changed on restart"
                request("/_magpie/sign-in", body={"key": admin, "back": "/"}, form=True)
                state = json_request("/api/providers")
                saved = next(p for p in state["providers"] if p["id"] == "everyapi")
                assert saved["name"] == "EveryAPI acceptance", "restart replaced Web edits"
                assert any(m["id"] == args.model for m in saved["models"]), "restart lost models"
                assert json_request("/api/provider/key", body={"id": "everyapi"})["key"] == key, "restart lost the Web-configured key"
                request("/v1/models", credential=client, status=401)
                assert any(m["id"] == model for m in json_request("/v1/models", credential=rotated)["data"])
                json_request("/api/caller-keys/remove-key", body={"key": key_id})
                stop()
                jar.clear()
                start()
                request("/v1/models", credential=rotated, status=401)
                print("PASS: key rotation, persistent Web edits/models/credentials and revocation after restart", flush=True)
                log.flush()
                log.seek(0)
                output = log.read()
                assert all(secret not in output for secret in [key, admin, client, rotated]), "process log leaked a credential"
                print("PASS: credential-safe process logs; temporary data and process will be removed", flush=True)
            finally:
                stop()
    failed = sum(r["status"] == "fail" for r in results)
    print(f"RESULT: {len(results) - failed}/{len(results)} passed; {failed} failed", flush=True)
    if failed:
        raise SystemExit(1)


def interrupted(*_):
    raise SystemExit("acceptance interrupted")


if __name__ == "__main__":
    signal.signal(signal.SIGTERM, interrupted)
    main()
