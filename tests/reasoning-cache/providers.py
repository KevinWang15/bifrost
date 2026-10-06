"""Local upstreams with incompatible opaque reasoning, and request accounting."""
import json
import threading
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

lock = threading.Lock()
requests = []
control = {"a_down": False, "always_reject": False, "stream_fail": False}


def tokens(value):
    if isinstance(value, dict):
        for key, child in value.items():
            if key in ("encrypted_content", "signature", "data") and isinstance(child, str):
                yield child
            elif isinstance(child, (dict, list)):
                yield from tokens(child)
    elif isinstance(value, list):
        for child in value:
            yield from tokens(child)


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def reply(self, status, body):
        encoded = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(encoded)))
        self.end_headers()
        self.wfile.write(encoded)

    def events(self, events, anthropic=False):
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Connection", "close")
        self.end_headers()
        for event in events:
            if anthropic:
                self.wfile.write(("event: " + event["type"] + "\n").encode())
            self.wfile.write(("data: " + json.dumps(event) + "\n\n").encode())
            self.wfile.flush()
        if not anthropic:
            self.wfile.write(b"data: [DONE]\n\n")
        self.close_connection = True

    def do_GET(self):
        if self.path == "/harness/stats":
            with lock:
                self.reply(200, {"id": "stats", "object": "chat.completion", "choices": [{"index": 0, "message": {"role": "assistant", "content": "local statistics"}, "finish_reason": "stop"}], "requests": list(requests)})
        elif self.path == "/stats":
            with lock:
                self.reply(200, list(requests))
        else:
            self.reply(200, {"ok": True})

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", 0))))
        if self.path == "/control":
            with lock:
                control.update(body)
            self.reply(200, control)
            return
        target = "A" if self.path.startswith("/a/") else "B"
        anthropic = self.path.startswith("/anthropic/")
        with lock:
            mode = dict(control)
            requests.append({"target": target, "path": self.path, "body": body})
        if target == "A" and mode["a_down"]:
            self.reply(503, {"error": {"message": "provider A unavailable", "type": "server_error"}})
            return
        foreign = any(token.startswith("A::" if target == "B" else "B::") for token in tokens(body))
        if foreign or mode["always_reject"]:
            message = "Invalid signature in thinking block" if anthropic else "unable to decode encrypted reasoning blocks"
            self.reply(400, {"type": "error", "error": {"type": "invalid_request_error", "code": "invalid_encrypted_content", "message": message}})
            return
        token = target + "::" + uuid.uuid4().hex
        model = body.get("model", "gpt-5.6-sol")
        if anthropic:
            content = [{"type": "thinking", "thinking": "plan", "signature": token}, {"type": "text", "text": "ok"}]
            response = {"id": "msg_local", "type": "message", "role": "assistant", "model": model, "content": content, "stop_reason": "end_turn", "stop_sequence": None, "usage": {"input_tokens": 3, "output_tokens": 2}}
            if not body.get("stream"):
                self.reply(200, response)
                return
            self.events([
                {"type": "message_start", "message": dict(response, content=[], stop_reason=None)},
                {"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}},
                {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "ok"}},
                {"type": "content_block_stop", "index": 0},
                {"type": "message_delta", "delta": {"stop_reason": "end_turn"}, "usage": {"output_tokens": 2}},
                {"type": "message_stop"},
            ], anthropic=True)
            return
        response = {"id": "resp_local", "object": "response", "model": model, "status": "completed", "output": [
            {"id": "rs_local", "type": "reasoning", "summary": [{"type": "summary_text", "text": "plan"}], "encrypted_content": token},
            {"id": "msg_local", "type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "ok", "annotations": []}]},
        ], "usage": {"input_tokens": 3, "output_tokens": 2, "total_tokens": 5}}
        if not body.get("stream"):
            self.reply(200, response)
        elif mode["stream_fail"]:
            self.events([{"type": "response.created", "sequence_number": 0, "response": dict(response, output=[], status="in_progress")}, {"type": "response.failed", "sequence_number": 1, "response": dict(response, status="failed", error={"code": "server_error", "message": "late stream failure"})}])
        else:
            self.events([
                {"type": "response.output_text.delta", "sequence_number": 0, "output_index": 1, "content_index": 0, "item_id": "msg_local", "delta": "ok"},
                {"type": "response.completed", "sequence_number": 1, "response": response},
            ])


ThreadingHTTPServer(("0.0.0.0", 9000), Handler).serve_forever()
