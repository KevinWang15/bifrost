"""Two paid requests checking a real tool continuation through both gateways."""
import json
import uuid
from live import OUTPUT, chat, stats, usage

session = "live-tool-" + str(uuid.uuid4())
tools = [{"type": "function", "function": {"name": "lookup", "description": "Look up a value by key",
          "parameters": {"type": "object", "properties": {"key": {"type": "string"}}, "required": ["key"],
                         "additionalProperties": False}}}]
messages = [{"role": "user", "content": "Use the lookup tool with key demo. Then reply with exactly the returned value."}]
before = stats()["classifier"]["total_requests"]
results = {"usage_before": usage(), "session": session, "cases": []}
first = chat("", session=session, history=messages, tools=tools,
             tool_choice={"type": "function", "function": {"name": "lookup"}})
results["cases"].append(first)
if first["status"] == 200:
    message = first["response"]["choices"][0]["message"]
    calls = message.get("tool_calls", [])
    if calls:
        messages += [message] + [{"role": "tool", "tool_call_id": c["id"], "content": "READY"} for c in calls]
        second = chat("", session=session, history=messages, tools=tools, tool_choice="none")
        results["cases"].append(second)
after = stats()["classifier"]["total_requests"]
results["classifier_calls"] = after - before
results["usage_after"] = usage()
results["passed"] = (len(results["cases"]) == 2 and results["classifier_calls"] == 1
                     and all(c["status"] == 200 for c in results["cases"])
                     and first["response"]["model"] == second["response"]["model"]
                     and second["response"]["choices"][0]["message"].get("content", "").strip() == "READY")
OUTPUT.joinpath("live-tools.json").write_text(json.dumps(results, ensure_ascii=False, indent=2) + "\n")
print(json.dumps({"passed": results["passed"], "classifier_calls": results["classifier_calls"],
                  "models": [c["response"].get("model") for c in results["cases"]]}), flush=True)
raise SystemExit(0 if results["passed"] else 1)
