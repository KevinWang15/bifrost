"""Controllable OpenAI-compatible upstream; never calls an external model."""
import json
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

lock = threading.Lock()
state = {}

def route(name):
    return state.setdefault(name, {"status": 200, "delay": 0, "requests": [], "prefixes": []})

class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def reply(self, status, body):
        data = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        if self.path == "/state":
            with lock:
                self.reply(200, state)
        else:
            self.reply(200, {"object": "list", "data": [{"id": "test-model", "object": "model"}]})

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", 0))))
        if self.path == "/control":
            with lock:
                s = route(body["route"])
                s.update({k: v for k, v in body.items() if k in ("status", "delay")})
            return self.reply(200, {"ok": True})
        name = self.path.strip("/").split("/")[0]
        messages = body.get("messages", body.get("input", []))
        prefix = json.dumps(messages, sort_keys=True)
        with lock:
            s = route(name)
            status, delay = s["status"], s["delay"]
            cached = prefix in s["prefixes"]
            if status == 200:
                s["prefixes"].append(prefix)
            s["requests"].append({"model": body.get("model"), "status": status,
                "messages": messages, "stream": body.get("stream", False), "at": time.time()})
        time.sleep(delay)
        if status != 200:
            return self.reply(status, {"error": {"message": "simulated outage", "type": "server_error"}})
        usage = {"prompt_tokens": 100, "completion_tokens": 1, "total_tokens": 101,
                 "prompt_tokens_details": {"cached_tokens": 100 if cached else 0}}
        if self.path.endswith("/responses"):
            part = {"type": "output_text", "text": name, "annotations": []}
            message = {"id": "msg_" + name, "type": "message", "role": "assistant", "status": "completed", "content": [part]}
            response = {"id": "resp_" + name, "object": "response", "created_at": int(time.time()),
                        "status": "completed", "model": body.get("model"), "output": [message],
                        "usage": {"input_tokens": 100, "output_tokens": 1, "total_tokens": 101,
                                  "input_tokens_details": {"cached_tokens": 100 if cached else 0}}}
            if not body.get("stream"):
                return self.reply(200, response)
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.end_headers()
            events = [
                {"type": "response.created", "response": {**response, "status": "in_progress", "output": []}},
                {"type": "response.output_item.added", "output_index": 0, "item": {**message, "status": "in_progress", "content": []}},
                {"type": "response.content_part.added", "item_id": message["id"], "output_index": 0, "content_index": 0, "part": {**part, "text": ""}},
                {"type": "response.output_text.delta", "item_id": message["id"], "output_index": 0, "content_index": 0, "delta": name},
                {"type": "response.output_text.done", "item_id": message["id"], "output_index": 0, "content_index": 0, "text": name},
                {"type": "response.content_part.done", "item_id": message["id"], "output_index": 0, "content_index": 0, "part": part},
                {"type": "response.output_item.done", "output_index": 0, "item": message},
                {"type": "response.completed", "response": response},
            ]
            for sequence, event in enumerate(events):
                event["sequence_number"] = sequence
                self.wfile.write(("event: " + event["type"] + "\ndata: " + json.dumps(event) + "\n\n").encode())
                self.wfile.flush()
            return
        result = {"id": "fake-" + name, "object": "chat.completion", "created": int(time.time()),
                  "model": body.get("model"), "choices": [{"index": 0,
                  "message": {"role": "assistant", "content": name}, "finish_reason": "stop"}], "usage": usage}
        if not body.get("stream"):
            return self.reply(200, result)
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        result["object"] = "chat.completion.chunk"
        result["choices"] = [{"index": 0, "delta": {"role": "assistant", "content": name}, "finish_reason": None}]
        self.wfile.write(("data: " + json.dumps(result) + "\n\n").encode())
        result["choices"] = [{"index": 0, "delta": {}, "finish_reason": "stop"}]
        self.wfile.write(("data: " + json.dumps(result) + "\n\ndata: [DONE]\n\n").encode())
        self.wfile.flush()

ThreadingHTTPServer(("0.0.0.0", 9000), Handler).serve_forever()
