#!/usr/bin/env python3
"""Run a paired, read-only Codex evaluation on synthetic review tasks."""

import argparse
import hashlib
import json
import pathlib
import subprocess
import tempfile
import time

SKILLS = pathlib.Path(__file__).resolve().parents[1] / "plugins/harnessmesh-codex/skills"
TASKS = (
    ("mcp-auth-review", "tenant-regression", "A mock handler now returns results for the requested tenant. Previously it compared the requested tenant with the authenticated tenant first.", "tenant_boundary"),
    ("mcp-auth-review", "tenant-control", "A mock handler changes its display label. It still compares the requested tenant with the authenticated tenant before accessing results.", ""),
    ("security-diff-review", "logging-regression", "A mock service replaces a fixed authentication log message with a message that includes the entire Authorization header.", "credential_logging"),
    ("security-diff-review", "logging-control", "A mock service changes the wording of a fixed authentication log message. The log still contains only that fixed string and a request ID.", ""),
)


def evaluate(task, guidance, model, workspace):
    skill, task_id, scenario, expected = task
    prompt = (
        "Review this synthetic change. Do not use tools or access files. "
        "Return exactly one JSON object with a findings array. Use only the IDs "
        "tenant_boundary or credential_logging for confirmed issues, and [] for no issue.\n"
        + scenario
    )
    if guidance:
        prompt += "\nApply these review instructions:\n" + guidance
    start = time.monotonic()
    result = subprocess.run(
        ["codex", "--ask-for-approval", "never", "exec", "--ignore-user-config",
         "--ephemeral", "--skip-git-repo-check", "--sandbox", "read-only", "--json",
         "--model", model, "-"],
        cwd=workspace, input=prompt, text=True, capture_output=True, timeout=120,
        check=False,
    )
    if result.returncode:
        raise RuntimeError(f"{task_id}: Codex failed ({result.returncode}): {result.stderr.strip() or result.stdout[-500:]}")
    events = [json.loads(line) for line in result.stdout.splitlines() if line.strip()]
    items = [event["item"] for event in events if event.get("type") == "item.completed"]
    messages = [item["text"] for item in items if item.get("type") == "agent_message"]
    if not messages:
        raise RuntimeError(f"{task_id}: Codex returned no answer")
    answer = messages[-1].strip()
    if answer.startswith("```json") and answer.endswith("```"):
        answer = answer[len("```json"):-3].strip()
    findings = json.loads(answer)["findings"]
    if not isinstance(findings, list) or not all(isinstance(item, str) for item in findings):
        raise ValueError(f"{task_id}: expected a list of finding IDs")
    target = [expected] if expected else []
    return dict(tenant="synthetic-pilot", task_id=task_id, input=scenario,
                success=sorted(findings) == target, policy_ok=all(
                    item.get("type") in ("reasoning", "agent_message") for item in items),
                human_approved=False, wall_time_ms=round((time.monotonic() - start) * 1000),
                verified_findings=int(bool(expected) and expected in findings),
                output=json.dumps(findings))


def load_guidance(skill):
    directory = SKILLS / ("harnessmesh-" + skill)
    raw = (directory / "SKILL.md").read_bytes()
    manifest = json.loads((directory / "skill.json").read_text())
    if hashlib.sha256(raw).hexdigest() != manifest["sha256"]:
        raise ValueError(f"{skill}: SKILL.md hash mismatch")
    if set(manifest["tools"]) != {"change.status", "change.diff"}:
        raise ValueError(f"{skill}: unexpected tools in the pilot manifest")
    return raw.decode("utf-8")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--model")
    parser.add_argument("--output", type=pathlib.Path)
    parser.add_argument("--preflight", action="store_true", help="check skill hashes and tool declarations locally")
    args = parser.parse_args()
    skills = sorted({task[0] for task in TASKS})
    guidance = {skill: load_guidance(skill) for skill in skills}
    if args.preflight:
        print("Validated skill manifests and hashes: " + ", ".join(skills))
        return
    if not args.model or args.output is None:
        parser.error("--model and --output are required for a live run")
    if args.output.exists():
        parser.error("--output directory already exists")
    rows = {"baseline": [], "candidate": []}
    with tempfile.TemporaryDirectory(prefix="harnessmesh-skill-eval-") as workspace:
        for index, task in enumerate(TASKS):
            order = ("baseline", "candidate") if index % 2 == 0 else ("candidate", "baseline")
            for arm in order:
                rows[arm].append(evaluate(task, guidance[task[0]] if arm == "candidate" else "", args.model, workspace))
    args.output.mkdir(parents=True, exist_ok=False)
    for arm, data in rows.items():
        (args.output / (arm + ".jsonl")).write_text("".join(json.dumps(row) + "\n" for row in data))


if __name__ == "__main__":
    main()
