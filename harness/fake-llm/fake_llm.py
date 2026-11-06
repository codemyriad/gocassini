#!/usr/bin/env python3
"""A fake OpenAI-compatible endpoint for harness scenarios (D-895).

Serves exactly what the recorder's summary step and the operator's model
discovery call, and nothing else:

  GET  <any prefix>/models            one model, MODEL_ID
  POST <any prefix>/chat/completions  a canned markdown reply
  GET  /healthz                       200, for readiness polling

The reply carries CANNED_SENTENCE and the SHA-256 of the request's system
message, so a scenario can prove which prompt arrived from the published
summary alone: Cassini's workflow hash is taken over exactly the system prompt
it sends, so the echoed digest equals the template's listed sha256 when the
configured template is the one that ran.

Every request is appended to --log as one JSON line: method, path and the body
(parsed when it is JSON, the raw text otherwise). The log is opened once at
startup, and the server refuses to start when it cannot be; a later write
failure is reported on stderr and the request is still answered. No key is
checked and none is needed. Standard library only, so it runs from a stock
python image and in the offline test with the runner's own python3.

Request bodies are read by Content-Length only: a chunked body (no
Content-Length) reads as empty. The recorder's Go client always sends
Content-Length, so this fake does not decode chunked transfer encoding.
"""

import argparse
import hashlib
import json
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

MODEL_ID = "harness/fake-summary"
CANNED_SENTENCE = "This summary was written by the harness fake LLM endpoint."
MAX_BODY_BYTES = 8 << 20


def system_prompt(body):
    """The first system message's content, or "" when there is none."""
    if not isinstance(body, dict):
        return ""
    for message in body.get("messages") or []:
        if isinstance(message, dict) and message.get("role") == "system":
            content = message.get("content")
            return content if isinstance(content, str) else ""
    return ""


def canned_reply(system):
    digest = hashlib.sha256(system.encode("utf-8")).hexdigest()
    return (
        "## Summary\n\n"
        f"{CANNED_SENTENCE}\n\n"
        f"System prompt SHA-256: {digest}\n"
    )


class Handler(BaseHTTPRequestHandler):
    server_version = "cassini-fake-llm/1"
    log_file = None
    log_lock = threading.Lock()

    def log_message(self, fmt, *args):  # quieter than the default, to stderr
        sys.stderr.write("fake-llm: " + (fmt % args) + "\n")

    def _record(self, body):
        if self.log_file is None:
            return
        line = json.dumps({"method": self.command, "path": self.path, "body": body}, sort_keys=True)
        try:
            with self.log_lock:
                self.log_file.write(line + "\n")
                self.log_file.flush()
        except OSError as error:  # a lost log line must not cost the reply
            sys.stderr.write(f"fake-llm: could not log {self.command} {self.path}: {error}\n")

    def _send_json(self, status, payload):
        data = json.dumps(payload).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        path = self.path.split("?", 1)[0].rstrip("/")
        self._record(None)
        if path == "/healthz":
            self._send_json(200, {"ok": True})
        elif path.endswith("/models"):
            self._send_json(200, {"object": "list", "data": [
                {"id": MODEL_ID, "object": "model", "owned_by": "harness", "name": "Harness fake"},
            ]})
        else:
            self._send_json(404, {"error": {"message": f"no route for GET {path}"}})

    def do_POST(self):
        path = self.path.split("?", 1)[0].rstrip("/")
        length = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(min(length, MAX_BODY_BYTES)).decode("utf-8", errors="replace")
        try:
            body = json.loads(raw)
        except ValueError:
            body = raw
        self._record(body)
        if not path.endswith("/chat/completions"):
            self._send_json(404, {"error": {"message": f"no route for POST {path}"}})
            return
        if not isinstance(body, dict):
            self._send_json(400, {"error": {"message": "the body is not a JSON object"}})
            return
        content = canned_reply(system_prompt(body))
        self._send_json(200, {
            "id": "chatcmpl-harness-fake",
            "object": "chat.completion",
            "model": body.get("model") or MODEL_ID,
            "choices": [{
                "index": 0,
                "message": {"role": "assistant", "content": content},
                "finish_reason": "stop",
            }],
            "usage": {"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0},
        })


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--host", default="0.0.0.0")
    parser.add_argument("--port", type=int, default=8000, help="0 picks a free port")
    parser.add_argument("--log", default="", help="append every request here as JSON lines")
    parser.add_argument("--port-file", default="", help="write the bound port here once listening")
    args = parser.parse_args(argv)

    if args.log:
        try:
            Handler.log_file = open(args.log, "a", encoding="utf-8")
        except OSError as error:
            sys.stderr.write(f"fake-llm: cannot open the request log {args.log}: {error}\n")
            return 1
    server = ThreadingHTTPServer((args.host, args.port), Handler)
    port = server.server_address[1]
    if args.port_file:
        with open(args.port_file, "w", encoding="utf-8") as handle:
            handle.write(f"{port}\n")
    sys.stderr.write(f"fake-llm: listening on {args.host}:{port}\n")
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()
        if Handler.log_file is not None:
            Handler.log_file.close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
