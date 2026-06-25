#!/usr/bin/env python3
"""Submit or resume an AnyAI HTTP run and wait for its terminal result."""

from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import sys
import time
import urllib.error
import urllib.parse
import urllib.request


STATE_DIR = Path("/tmp") / "anyai_http_run_state"
TERMINAL_STATUSES = {"completed", "failed", "aborted"}
ACTIVE_STATUSES = {"queued", "running"}
SESSION_INPUT_STORED = "session.input.stored"
SESSION_OUTPUT_STORED = "session.output.stored"


def request_json(method: str, url: str, payload: dict | None = None, timeout: int = 30) -> dict:
    data = None
    headers = {"Accept": "application/json"}
    if payload is not None:
        data = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        headers["Content-Type"] = "application/json"

    req = urllib.request.Request(url, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            body = resp.read().decode("utf-8")
    except urllib.error.HTTPError as exc:
        body = exc.read().decode("utf-8", errors="replace")
        raise RuntimeError(f"HTTP {exc.code} {url}: {body}") from exc
    except urllib.error.URLError as exc:
        raise RuntimeError(f"request failed {url}: {exc.reason}") from exc
    except OSError as exc:
        raise RuntimeError(f"request failed {url}: {exc}") from exc

    if not body.strip():
        return {}
    try:
        return json.loads(body)
    except json.JSONDecodeError as exc:
        raise RuntimeError(f"invalid JSON response from {url}: {body[:500]}") from exc


def build_url(base_url: str, path: str, query: dict[str, str] | None = None) -> str:
    url = f"{base_url.rstrip('/')}{path}"
    if query:
        url = f"{url}?{urllib.parse.urlencode(query)}"
    return url


def quote_path(value: str) -> str:
    return urllib.parse.quote(value, safe="")


def status_of(run: dict | None) -> str:
    if not isinstance(run, dict):
        return ""
    return str(run.get("status", "")).strip().lower()


def parse_time(value: str) -> float:
    value = str(value or "").strip()
    if not value or value.startswith("0001-01-01"):
        return 0.0
    if value.endswith("Z"):
        value = value[:-1] + "+00:00"
    try:
        from datetime import datetime

        return datetime.fromisoformat(value).timestamp()
    except ValueError:
        return 0.0


def run_sort_key(run: dict) -> tuple[int, float, float]:
    status = status_of(run)
    active_rank = 2 if status == "running" else 1 if status == "queued" else 0
    return (
        active_rank,
        parse_time(str(run.get("started_at") or "")),
        parse_time(str(run.get("created_at") or "")),
    )


def message_id_for(agent_id: str, session_id: str, text: str) -> str:
    digest = hashlib.sha256(
        "\0".join([agent_id.strip(), session_id.strip(), text]).encode("utf-8")
    ).hexdigest()
    return f"msg_{digest[:32]}"


def state_file_for(agent_id: str, session_id: str) -> Path:
    STATE_DIR.mkdir(mode=0o700, exist_ok=True)
    safe_agent = agent_id.replace("/", "_")
    safe_session = session_id.replace("/", "_")
    return STATE_DIR / f"anyai_run_{safe_agent}_{safe_session}.json"


def load_state(agent_id: str, session_id: str) -> dict | None:
    path = state_file_for(agent_id, session_id)
    try:
        state = json.loads(path.read_text(encoding="utf-8"))
    except (FileNotFoundError, OSError, json.JSONDecodeError):
        return None
    if not isinstance(state, dict):
        return None
    if state.get("agent_id") != agent_id or state.get("session_id") != session_id:
        return None
    if not str(state.get("run_id") or "").strip():
        return None
    return state


def save_state(agent_id: str, session_id: str, run_id: str, message_id: str = "") -> None:
    if not run_id:
        return
    path = state_file_for(agent_id, session_id)
    payload = {
        "run_id": run_id,
        "agent_id": agent_id,
        "session_id": session_id,
        "message_id": message_id,
        "saved_at": time.time(),
    }
    try:
        path.write_text(json.dumps(payload, ensure_ascii=False), encoding="utf-8")
    except OSError:
        pass


def clear_state(agent_id: str, session_id: str) -> None:
    try:
        state_file_for(agent_id, session_id).unlink()
    except FileNotFoundError:
        pass
    except OSError:
        pass


def unwrap_event(event: dict) -> dict:
    nested = event.get("event")
    if isinstance(nested, dict):
        return nested
    return event


def event_name(event: dict) -> str:
    return str(unwrap_event(event).get("name") or "").strip()


def event_payload(event: dict) -> dict:
    payload = unwrap_event(event).get("payload") or {}
    if isinstance(payload, dict):
        return payload
    return {}


def event_run_id(event: dict) -> str:
    event = unwrap_event(event)
    payload = event_payload(event)
    return str(event.get("run_id") or payload.get("run_id") or "").strip()


def event_text(event: dict) -> str:
    payload = event_payload(event)
    return str(payload.get("text") or payload.get("summary") or "").strip()


def list_runs(
    base_url: str,
    agent_id: str = "",
    session_id: str = "",
    status: str = "",
    limit: int = 0,
) -> list[dict]:
    query: dict[str, str] = {}
    if agent_id:
        query["agent_id"] = agent_id
    if session_id:
        query["session_id"] = session_id
    if status:
        query["status"] = status
    if limit > 0:
        query["limit"] = str(limit)

    parsed = request_json("GET", build_url(base_url, "/api/runs", query or None))
    runs = parsed.get("runs") or []
    if not isinstance(runs, list):
        return []
    return [run for run in runs if isinstance(run, dict)]


def get_run(base_url: str, run_id: str) -> dict:
    parsed = request_json("GET", build_url(base_url, f"/api/runs/{quote_path(run_id)}"))
    run = parsed.get("run") or {}
    if not isinstance(run, dict):
        return {}
    return run


def session_events(base_url: str, agent_id: str, session_id: str, limit: int = 500) -> list[dict]:
    path = f"/api/sessions/{quote_path(agent_id)}/{quote_path(session_id)}/events"
    parsed = request_json("GET", build_url(base_url, path, {"limit": str(max(1, limit))}))
    events = parsed.get("events")
    if events is None:
        events = (parsed.get("session") or {}).get("events") or []
    if not isinstance(events, list):
        return []
    return [unwrap_event(event) for event in events if isinstance(event, dict)]


def run_for_message_id(base_url: str, agent_id: str, session_id: str, message_id: str) -> dict | None:
    if not message_id:
        return None
    try:
        events = session_events(base_url, agent_id, session_id)
    except RuntimeError:
        return None

    run_ids: list[str] = []
    for event in events:
        if event_name(event) != SESSION_INPUT_STORED:
            continue
        payload = event_payload(event)
        if str(payload.get("entry_id") or "").strip() != message_id:
            continue
        run_id = event_run_id(event)
        if run_id:
            run_ids.append(run_id)

    for run_id in reversed(run_ids):
        try:
            run = get_run(base_url, run_id)
        except RuntimeError:
            continue
        if run.get("id"):
            return run
    return None


def find_active_session_run(base_url: str, agent_id: str, session_id: str) -> dict | None:
    runs = list_runs(
        base_url,
        agent_id=agent_id,
        session_id=session_id,
        status="queued,running",
        limit=20,
    )
    runs = [
        run
        for run in runs
        if str(run.get("agent_id") or "").strip() == agent_id
        and str(run.get("session_id") or "").strip() == session_id
        and status_of(run) in ACTIVE_STATUSES
    ]
    if not runs:
        return None
    return max(runs, key=run_sort_key)


def latest_session_output(
    base_url: str,
    agent_id: str,
    session_id: str,
    run_id: str = "",
    skip_run_id: str = "",
) -> dict | None:
    try:
        events = session_events(base_url, agent_id, session_id, limit=1000)
    except RuntimeError:
        return None

    for event in reversed(events):
        if event_name(event) != SESSION_OUTPUT_STORED:
            continue
        found_run_id = event_run_id(event)
        if run_id and found_run_id and found_run_id != run_id:
            continue
        if skip_run_id and found_run_id == skip_run_id:
            continue
        text = event_text(event)
        if not text:
            continue
        return {
            "text": text,
            "run_id": found_run_id,
        }
    return None


def previous_run_output(base_url: str, agent_id: str, session_id: str, current_run_id: str) -> dict | None:
    try:
        runs = list_runs(
            base_url,
            agent_id=agent_id,
            session_id=session_id,
            status="completed,failed,aborted",
            limit=20,
        )
    except RuntimeError:
        return None

    for run in sorted(runs, key=run_sort_key, reverse=True):
        run_id = str(run.get("id") or "").strip()
        if not run_id or run_id == current_run_id:
            continue
        output = str(run.get("output") or "").strip()
        if output:
            return {
                "text": output,
                "run_id": run_id,
            }
    return None


def resolve_output(base_url: str, agent_id: str, session_id: str, run: dict) -> dict:
    run_id = str(run.get("id") or "").strip()
    output = str(run.get("output") or "").strip()
    if output:
        return output_payload(text=output, run_id=run_id)

    found_output = latest_session_output(base_url, agent_id, session_id, run_id=run_id)
    if found_output is None:
        found_output = latest_session_output(base_url, agent_id, session_id, skip_run_id=run_id)
    if found_output is None:
        found_output = previous_run_output(base_url, agent_id, session_id, run_id)
    if found_output is None:
        return output_payload(run_id=run_id)

    return output_payload(
        text=str(found_output.get("text") or ""),
        run_id=str(found_output.get("run_id") or run_id),
    )


def submit_run(base_url: str, agent_id: str, session_id: str, text: str, message_id: str) -> str:
    payload = {
        "agent_id": agent_id,
        "session_id": session_id,
        "message_id": message_id,
        "inputs": [{"type": "text", "text": text}],
    }
    parsed = request_json("POST", build_url(base_url, "/api/runs"), payload)
    run = parsed.get("run") or {}
    run_id = str(run.get("id") or "").strip()
    if not run_id:
        raise RuntimeError(f"missing run.id in create response: {json.dumps(parsed, ensure_ascii=False)}")
    return run_id


def wait_run(base_url: str, run_id: str, timeout_seconds: int, poll_seconds: float) -> dict:
    deadline = time.time() + timeout_seconds
    last_run: dict = {}
    last_error: Exception | None = None

    while time.time() < deadline:
        try:
            run = get_run(base_url, run_id)
        except Exception as exc:  # noqa: BLE001 - return the last useful context below
            last_error = exc
            time.sleep(min(poll_seconds, max(0.0, deadline - time.time())))
            continue

        if run:
            last_run = run
        if status_of(run) in TERMINAL_STATUSES:
            return run
        time.sleep(min(poll_seconds, max(0.0, deadline - time.time())))

    message = f"timed out waiting for run {run_id}; last status={last_run.get('status', 'unknown')}"
    if last_error is not None:
        message += f"; last error={last_error}"
    raise TimeoutError(message)


def output_payload(text: str = "", run_id: str = "") -> dict:
    return {
        "text": text,
        "run_id": run_id,
    }


def result_payload(
    *,
    ok: bool,
    error_message: str,
    error_type: str,
    output: dict | None = None,
) -> dict:
    return {
        "ok": ok,
        "error_message": error_message,
        "error_type": error_type,
        "output": output or output_payload(),
    }


def error_payload(*, run_id: str, exc: Exception) -> dict:
    return result_payload(
        ok=False,
        error_message=str(exc),
        error_type=exc.__class__.__name__,
        output=output_payload(run_id=run_id),
    )


def error_message_for_run(run: dict) -> str:
    status = status_of(run)
    error = str(run.get("error") or "").strip()
    if error:
        return error
    if status:
        run_id = str(run.get("id") or "").strip()
        return f"run {run_id} {status}" if run_id else f"run {status}"
    return ""


def select_or_create_run(
    base_url: str,
    agent_id: str,
    session_id: str,
    message_id: str,
    text: str,
) -> tuple[str, dict | None]:
    state = load_state(agent_id, session_id)
    state_run_id = str((state or {}).get("run_id") or "").strip()
    if state_run_id:
        run = get_run(base_url, state_run_id)
        status = status_of(run)
        if status in ACTIVE_STATUSES:
            return state_run_id, None
        if status == "completed":
            return state_run_id, run
        # Keep failed/aborted/unknown state until a replacement run is saved below.

    existing = run_for_message_id(base_url, agent_id, session_id, message_id)
    if existing and status_of(existing) in ACTIVE_STATUSES | {"completed"}:
        run_id = str(existing.get("id") or "").strip()
        save_state(agent_id, session_id, run_id, message_id)
        selected = existing if status_of(existing) == "completed" else None
        return run_id, selected

    active = find_active_session_run(base_url, agent_id, session_id)
    if active:
        run_id = str(active.get("id") or "").strip()
        save_state(agent_id, session_id, run_id, message_id)
        return run_id, None

    run_id = submit_run(base_url, agent_id, session_id, text, message_id)
    save_state(agent_id, session_id, run_id, message_id)
    return run_id, None


def main() -> int:
    parser = argparse.ArgumentParser(
        description=__doc__,
        epilog="Output JSON fields: ok, error_message, error_type, output.",
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--agent-id", required=True)
    parser.add_argument("--session-id", required=True)
    parser.add_argument("--message-id", help="Stable client message id for request recovery")
    text_group = parser.add_mutually_exclusive_group(required=True)
    text_group.add_argument("--text")
    text_group.add_argument("--text-file", help="Read task text from this file, or '-' for stdin")
    parser.add_argument(
        "--timeout",
        type=int,
        default=1800,
        help="Maximum wait in seconds for a terminal run status; default is 1800 (30 minutes)",
    )
    parser.add_argument("--poll", type=float, default=2.0)
    parser.add_argument(
        "--wait-mode",
        choices=("events", "poll"),
        default="poll",
        help="Accepted for compatibility; this simplified client waits by polling run status",
    )
    parser.add_argument("--event-read-timeout", type=int, default=60, help=argparse.SUPPRESS)
    args = parser.parse_args()

    text = args.text
    if args.text_file:
        if args.text_file == "-":
            text = sys.stdin.read()
        else:
            text = Path(args.text_file).read_text(encoding="utf-8")

    message_id = args.message_id or message_id_for(args.agent_id, args.session_id, text or "")
    run_id = ""

    try:
        run_id, selected_run = select_or_create_run(
            args.base_url,
            args.agent_id,
            args.session_id,
            message_id,
            text or "",
        )
        run = selected_run or wait_run(args.base_url, run_id, args.timeout, args.poll)
    except Exception as exc:
        print(
            json.dumps(
                error_payload(
                    run_id=run_id,
                    exc=exc,
                ),
                ensure_ascii=False,
                indent=2,
            )
        )
        return 1

    status = status_of(run)
    output = resolve_output(args.base_url, args.agent_id, args.session_id, run)

    if status == "completed":
        clear_state(args.agent_id, args.session_id)
    else:
        save_state(args.agent_id, args.session_id, str(run.get("id") or run_id), message_id)

    exc = None if status == "completed" else RuntimeError(error_message_for_run(run))
    print(
        json.dumps(
            result_payload(
                ok=status == "completed",
                error_message="" if exc is None else str(exc),
                error_type="" if exc is None else exc.__class__.__name__,
                output=output,
            ),
            ensure_ascii=False,
            indent=2,
        )
    )
    return 0 if status == "completed" else 2


if __name__ == "__main__":
    raise SystemExit(main())
