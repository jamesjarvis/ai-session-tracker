#!/usr/bin/env python3
"""
Cursor command hook: writes ai-session-tracker compatible state under
~/.cursor/session-tracker/ (registry, live state, history.jsonl).

Slug and synthetic pid must stay aligned with state.go (CursorSessionSlug /
negative pid from SHA-256 of conversation_id).
"""
from __future__ import annotations

import hashlib
import json
import os
import sys
import tempfile
import time
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Tuple


def cursor_session_slug(conversation_id: str) -> str:
    return hashlib.sha256(conversation_id.encode("utf-8")).hexdigest()


def synthetic_pid(conversation_id: str) -> int:
    digest = hashlib.sha256(conversation_id.encode("utf-8")).digest()
    n = int.from_bytes(digest[:4], "big")
    # Strictly negative, non-zero (matches Go reader expectations).
    pid = -((n % (2**31 - 1)) + 1)
    return pid


def tracker_dirs() -> Tuple[Path, Path, Path]:
    base = Path.home() / ".cursor" / "session-tracker"
    return base / "sessions", base / "states", base / "history.jsonl"


def resolve_cwd(data: dict[str, Any]) -> str:
    roots = data.get("workspace_roots")
    if isinstance(roots, list) and roots:
        return str(roots[0])
    cwd = data.get("cwd")
    if isinstance(cwd, str) and cwd:
        return cwd
    env = os.environ.get("CURSOR_PROJECT_DIR")
    if env:
        return env
    return os.getcwd()


def resolve_conversation_id(data: dict[str, Any]) -> str:
    cid = data.get("conversation_id") or data.get("session_id")
    if isinstance(cid, str) and cid:
        return cid
    return "unknown"


def tool_display_name(data: dict[str, Any]) -> str | None:
    name = data.get("tool_name")
    if not isinstance(name, str) or not name:
        return None
    return name


def atomic_write_json(path: Path, obj: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp = tempfile.mkstemp(dir=str(path.parent), suffix=".tmp")
    tmp_path = Path(tmp)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as f:
            json.dump(obj, f, indent=2)
            f.flush()
            os.fsync(f.fileno())
        os.replace(tmp_path, path)
    except Exception:
        if tmp_path.exists():
            tmp_path.unlink(missing_ok=True)
        raise


def append_history(history_path: Path, entry: dict[str, Any]) -> None:
    history_path.parent.mkdir(parents=True, exist_ok=True)
    line = json.dumps(entry, separators=(",", ":")) + "\n"
    with history_path.open("a", encoding="utf-8") as f:
        f.write(line)
        f.flush()
        os.fsync(f.fileno())


def emit_response(hook: str, obj: dict[str, Any]) -> None:
    sys.stdout.write(json.dumps(obj))
    sys.stdout.flush()


def default_response(hook: str) -> dict[str, Any]:
    if hook == "preToolUse":
        return {"permission": "allow"}
    return {}


def build_state(
    *,
    conversation_id: str,
    pid: int,
    cwd: str,
    status: str,
    last_event: str,
    current_tool: str | None,
) -> dict[str, Any]:
    return {
        "session_id": conversation_id,
        "pid": pid,
        "cwd": cwd,
        "status": status,
        "current_tool": current_tool,
        "last_event": last_event,
        "timestamp": datetime.now(timezone.utc).isoformat(),
        "tty": None,
    }


def write_registry_if_needed(
    reg_path: Path,
    *,
    conversation_id: str,
    pid: int,
    cwd: str,
    entrypoint: str,
) -> None:
    now_ms = int(time.time() * 1000)
    if reg_path.exists():
        try:
            existing = json.loads(reg_path.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError):
            existing = {}
        started = existing.get("startedAt")
        if not isinstance(started, int):
            started = now_ms
        meta = {
            "pid": pid,
            "sessionId": conversation_id,
            "cwd": cwd or existing.get("cwd") or "",
            "startedAt": started,
            "kind": "cursor",
            "entrypoint": entrypoint
            or existing.get("entrypoint")
            or "composer",
            "name": existing.get("name") or "",
        }
    else:
        meta = {
            "pid": pid,
            "sessionId": conversation_id,
            "cwd": cwd,
            "startedAt": now_ms,
            "kind": "cursor",
            "entrypoint": entrypoint or "composer",
            "name": "",
        }
    atomic_write_json(reg_path, meta)


def persist_transition(
    *,
    slug: str,
    conversation_id: str,
    pid: int,
    cwd: str,
    status: str,
    last_event: str,
    current_tool: str | None,
    sessions_dir: Path,
    states_dir: Path,
    history_path: Path,
) -> None:
    state = build_state(
        conversation_id=conversation_id,
        pid=pid,
        cwd=cwd,
        status=status,
        last_event=last_event,
        current_tool=current_tool,
    )
    state_path = states_dir / f"{slug}.json"
    atomic_write_json(state_path, state)
    hist = {
        "timestamp": state["timestamp"],
        "session_id": conversation_id,
        "pid": pid,
        "cwd": cwd,
        "status": status,
        "last_event": last_event,
        "current_tool": current_tool,
    }
    append_history(history_path, hist)


def main() -> None:
    raw = sys.stdin.read()
    try:
        data = json.loads(raw) if raw.strip() else {}
    except json.JSONDecodeError:
        emit_response("", default_response(""))
        return

    hook = str(data.get("hook_event_name") or "").strip()
    if not hook:
        emit_response(hook, default_response(hook))
        return

    conversation_id = resolve_conversation_id(data)
    slug = cursor_session_slug(conversation_id)
    pid = synthetic_pid(conversation_id)
    cwd = resolve_cwd(data)
    sessions_dir, states_dir, history_path = tracker_dirs()
    reg_path = sessions_dir / f"{slug}.json"

    composer_mode = data.get("composer_mode")
    entrypoint = str(composer_mode) if isinstance(composer_mode, str) else "composer"

    try:
        if hook == "sessionStart":
            write_registry_if_needed(
                reg_path,
                conversation_id=conversation_id,
                pid=pid,
                cwd=cwd,
                entrypoint=entrypoint,
            )
            persist_transition(
                slug=slug,
                conversation_id=conversation_id,
                pid=pid,
                cwd=cwd,
                status="waiting_for_input",
                last_event="sessionStart",
                current_tool=None,
                sessions_dir=sessions_dir,
                states_dir=states_dir,
                history_path=history_path,
            )
        elif hook == "beforeSubmitPrompt":
            write_registry_if_needed(
                reg_path,
                conversation_id=conversation_id,
                pid=pid,
                cwd=cwd,
                entrypoint=entrypoint,
            )
            persist_transition(
                slug=slug,
                conversation_id=conversation_id,
                pid=pid,
                cwd=cwd,
                status="processing",
                last_event="beforeSubmitPrompt",
                current_tool=None,
                sessions_dir=sessions_dir,
                states_dir=states_dir,
                history_path=history_path,
            )
        elif hook == "preToolUse":
            write_registry_if_needed(
                reg_path,
                conversation_id=conversation_id,
                pid=pid,
                cwd=cwd,
                entrypoint=entrypoint,
            )
            tool = tool_display_name(data)
            persist_transition(
                slug=slug,
                conversation_id=conversation_id,
                pid=pid,
                cwd=cwd,
                status="running_tool",
                last_event="preToolUse",
                current_tool=tool,
                sessions_dir=sessions_dir,
                states_dir=states_dir,
                history_path=history_path,
            )
        elif hook == "postToolUse":
            write_registry_if_needed(
                reg_path,
                conversation_id=conversation_id,
                pid=pid,
                cwd=cwd,
                entrypoint=entrypoint,
            )
            persist_transition(
                slug=slug,
                conversation_id=conversation_id,
                pid=pid,
                cwd=cwd,
                status="processing",
                last_event="postToolUse",
                current_tool=None,
                sessions_dir=sessions_dir,
                states_dir=states_dir,
                history_path=history_path,
            )
        elif hook == "postToolUseFailure":
            write_registry_if_needed(
                reg_path,
                conversation_id=conversation_id,
                pid=pid,
                cwd=cwd,
                entrypoint=entrypoint,
            )
            ft = data.get("failure_type")
            last = (
                f"postToolUseFailure:{ft}"
                if isinstance(ft, str)
                else "postToolUseFailure"
            )
            persist_transition(
                slug=slug,
                conversation_id=conversation_id,
                pid=pid,
                cwd=cwd,
                status="processing",
                last_event=last,
                current_tool=None,
                sessions_dir=sessions_dir,
                states_dir=states_dir,
                history_path=history_path,
            )
        elif hook == "preCompact":
            write_registry_if_needed(
                reg_path,
                conversation_id=conversation_id,
                pid=pid,
                cwd=cwd,
                entrypoint=entrypoint,
            )
            persist_transition(
                slug=slug,
                conversation_id=conversation_id,
                pid=pid,
                cwd=cwd,
                status="compacting",
                last_event="preCompact",
                current_tool=None,
                sessions_dir=sessions_dir,
                states_dir=states_dir,
                history_path=history_path,
            )
        elif hook in ("stop", "sessionEnd"):
            write_registry_if_needed(
                reg_path,
                conversation_id=conversation_id,
                pid=pid,
                cwd=cwd,
                entrypoint=entrypoint,
            )
            persist_transition(
                slug=slug,
                conversation_id=conversation_id,
                pid=pid,
                cwd=cwd,
                status="ended",
                last_event=hook,
                current_tool=None,
                sessions_dir=sessions_dir,
                states_dir=states_dir,
                history_path=history_path,
            )
    except OSError as e:
        print(f"cursor-session-state-tracker: {e}", file=sys.stderr)

    emit_response(hook, default_response(hook))


if __name__ == "__main__":
    main()
