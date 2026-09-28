"""Inspect live verdicts using compose.observe.yaml, then compare one fixed-model answer."""
import json
from live import OUTPUT, chat, fetch, stats, usage

baseline = json.loads(OUTPUT.joinpath("live.json").read_text())
results = {"usage_before": usage(), "cases": []}
for name in ["chinese", "concurrency", "algorithm"]:
    prompt = next(c["prompt"] for c in baseline["cases"] if c["name"] == name)
    offset = len(fetch("http://judge-recorder:9001/records")[1])
    answer = chat(prompt, max_tokens=1024)
    verdicts = fetch("http://judge-recorder:9001/records")[1][offset:]
    result = {"name": name, **answer, "judge_calls": verdicts}
    results["cases"].append(result)
    print(json.dumps({"name": name, "status": answer["status"], "model": answer["response"].get("model"),
                      "verdicts": verdicts}), flush=True)

prompt = next(c["prompt"] for c in baseline["cases"] if c["name"] == "algorithm")
answer = chat(prompt, route="big", max_tokens=1024)
results["cases"].append({"name": "algorithm_big_baseline", **answer})
print(json.dumps({"name": "algorithm_big_baseline", "status": answer["status"],
                  "model": answer["response"].get("model")}), flush=True)
results["stats_after"] = stats()
results["usage_after"] = usage()
OUTPUT.joinpath("verdicts.json").write_text(json.dumps(results, indent=2, ensure_ascii=False) + "\n")
raise SystemExit(0 if all(c["status"] == 200 for c in results["cases"]) else 1)
