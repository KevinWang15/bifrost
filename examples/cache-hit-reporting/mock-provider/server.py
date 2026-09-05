#!/usr/bin/env python3
"""Small OpenAI-compatible upstream used by the cache-reporting example."""

import hashlib
import json
import os
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


lock = threading.Lock()
request_count = 0
embedding_request_count = 0


class Handler(BaseHTTPRequestHandler):
    def json_response(self, status, payload):
        body = json.dumps(payload, separators=(",", ":")).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):  # noqa: N802
        if self.path == "/health":
            self.json_response(200, {"status": "ok"})
            return
        if self.path == "/v1/models":
            self.json_response(
                200,
                {
                    "object": "list",
                    "data": [
                        {"id": "mock-model", "object": "model", "owned_by": "local"},
                        {"id": "mock-embedding", "object": "model", "owned_by": "local"},
                    ],
                },
            )
            return
        if self.path == "/stats":
            with lock:
                chat_count = request_count
                embedding_count = embedding_request_count
            self.json_response(
                200,
                {"chat_completion_requests": chat_count, "embedding_requests": embedding_count},
            )
            return
        self.json_response(404, {"error": "not found"})

    def do_POST(self):  # noqa: N802
        global embedding_request_count, request_count
        if self.path not in ("/v1/chat/completions", "/v1/embeddings"):
            self.json_response(404, {"error": {"message": "not found", "type": "not_found"}})
            return
        try:
            length = int(self.headers.get("Content-Length", "0"))
            request = json.loads(self.rfile.read(length))
        except (ValueError, json.JSONDecodeError):
            self.json_response(400, {"error": {"message": "invalid JSON", "type": "invalid_request_error"}})
            return

        if self.path == "/v1/embeddings":
            inputs = request.get("input", "")
            if not isinstance(inputs, list):
                inputs = [inputs]
            data = []
            for index, value in enumerate(inputs):
                digest = hashlib.sha256(str(value).encode()).digest()
                vector = [(byte - 127.5) / 127.5 for byte in digest[:8]]
                data.append({"object": "embedding", "index": index, "embedding": vector})
            with lock:
                embedding_request_count += 1
            self.json_response(
                200,
                {
                    "object": "list",
                    "data": data,
                    "model": request.get("model", "mock-embedding"),
                    "usage": {"prompt_tokens": len(inputs), "total_tokens": len(inputs)},
                },
            )
            return

        with lock:
            request_count += 1
            sequence = request_count
        model = request.get("model", "mock-model")
        messages = request.get("messages") or []
        prompt = messages[-1].get("content", "") if messages else ""
        now = int(time.time())
        self.json_response(
            200,
            {
                "id": f"chatcmpl-local-{sequence}",
                "object": "chat.completion",
                "created": now,
                "model": model,
                "choices": [
                    {
                        "index": 0,
                        "message": {"role": "assistant", "content": f"mock response for: {prompt}"},
                        "finish_reason": "stop",
                    }
                ],
                "usage": {"prompt_tokens": 8, "completion_tokens": 6, "total_tokens": 14},
            },
        )

    def log_message(self, message_format, *args):
        print(f"mock-provider: {message_format % args}", flush=True)


if __name__ == "__main__":
    port = int(os.getenv("PORT", "8000"))
    print(f"mock provider listening on 0.0.0.0:{port}", flush=True)
    ThreadingHTTPServer(("0.0.0.0", port), Handler).serve_forever()
