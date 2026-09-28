"""Local OpenAI-shaped backend for testing the gateway chain, not routing quality."""
import json
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

calls = []
lock = threading.Lock()


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def send_json(self, payload, status=200):
        raw = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        try:
            self.wfile.write(raw)
        except (BrokenPipeError, ConnectionResetError):
            pass

    def do_GET(self):
        if self.path == "/stats":
            with lock:
                self.send_json(list(calls))
        else:
            self.send_json({"error": "not found"}, 404)

    def do_POST(self):
        data = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        model = data["model"]
        messages = data.get("messages", [])
        with lock:
            calls.append({"model": model, "session": self.headers.get("x-switchyard-session-id"),
                          "roles": [m["role"] for m in messages]})
        users = [str(m.get("content", "")) for m in messages if m["role"] == "user"
                 and "Route the conversation above" not in str(m.get("content", ""))]
        task = users[-1] if users else ""
        message = {"role": "assistant", "content": f"Answered by {model}."}
        finish = "stop"
        if model == "mock-judge":
            if "JUDGE_ERROR" in task:
                return self.send_json({"error": {"message": "injected judge failure", "type": "server_error"}}, 500)
            if "JUDGE_TIMEOUT" in task:
                time.sleep(3)
            verdict = {"crux": "mock integration case", "primary_rule": "SUP-1",
                       "capability_boundary": "supported", "p_solve": 0.1 if "COMPLEX" in task else 0.99}
            message["content"] = "invalid verdict" if "INVALID_VERDICT" in task else json.dumps(verdict)
        elif data.get("tools") and not any(m["role"] == "tool" for m in messages):
            message = {"role": "assistant", "content": None, "tool_calls": [
                {"id": "call_probe", "type": "function", "function": {"name": "lookup", "arguments": '{"key":"demo"}'}}
            ]}
            finish = "tool_calls"
        body = {"id": "chatcmpl-mock", "object": "chat.completion", "created": int(time.time()),
                "model": model, "choices": [{"index": 0, "message": message, "finish_reason": finish}],
                "usage": {"prompt_tokens": 10, "completion_tokens": 10, "total_tokens": 20}}
        if not data.get("stream"):
            return self.send_json(body)
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        for delta, reason in [(message, None), ({}, finish)]:
            chunk = {"id": body["id"], "object": "chat.completion.chunk", "created": body["created"],
                     "model": model, "choices": [{"index": 0, "delta": delta, "finish_reason": reason}]}
            self.wfile.write(("data: " + json.dumps(chunk) + "\n\n").encode())
            self.wfile.flush()
        self.wfile.write(b"data: [DONE]\n\n")


ThreadingHTTPServer(("0.0.0.0", 9000), Handler).serve_forever()
