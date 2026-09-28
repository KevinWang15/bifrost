"""Experiment-only forwarder: record judge verdicts and usage, never credentials."""
import json
import threading
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

records = []
lock = threading.Lock()


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_GET(self):
        if self.path != "/records":
            self.send_error(404)
            return
        with lock:
            raw = json.dumps(records).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def do_POST(self):
        if self.path != "/v1/chat/completions":
            self.send_error(404)
            return
        raw = self.rfile.read(int(self.headers["Content-Length"]))
        payload = json.loads(raw)
        if payload.get("stream"):
            self.send_error(400, "Recorder accepts buffered judge calls only")
            return
        req = urllib.request.Request("https://openrouter.ai/api/v1/chat/completions", data=raw,
            headers={"Content-Type": "application/json", "Authorization": self.headers.get("Authorization", "")})
        start = time.monotonic()
        try:
            with urllib.request.urlopen(req, timeout=55) as response:
                status, result = response.status, response.read()
        except urllib.error.HTTPError as error:
            status, result = error.code, error.read()
        except (OSError, urllib.error.URLError):
            status, result = 502, b'{"error":{"message":"judge connection failed"}}'
        parsed = json.loads(result)
        choice = parsed.get("choices", [{}])[0]
        with lock:
            records.append({"model": payload["model"], "status": status,
                            "elapsed_s": round(time.monotonic() - start, 3),
                            "content": choice.get("message", {}).get("content"),
                            "finish_reason": choice.get("finish_reason"), "usage": parsed.get("usage")})
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(result)))
        self.end_headers()
        self.wfile.write(result)


ThreadingHTTPServer(("0.0.0.0", 9001), Handler).serve_forever()
