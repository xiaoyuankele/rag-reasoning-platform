"""Zero-cost OpenAI-compatible Provider used by local recovery verification.

The first embedding and generation request can be delayed independently. Later
requests return immediately, which lets a replacement Worker finish after the
first Worker is killed. This server must never be used as a production Provider.
"""

from __future__ import annotations

import json
import os
import signal
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any


def _read_non_negative_float(name: str, default: float) -> float:
    """Read a non-negative duration from the environment."""

    raw_value = os.getenv(name, str(default)).strip()
    value = float(raw_value)
    if value < 0:
        raise ValueError(f"{name} must not be negative")
    return value


def _read_positive_int(name: str, default: int) -> int:
    """Read a positive integer from the environment."""

    raw_value = os.getenv(name, str(default)).strip()
    value = int(raw_value)
    if value <= 0:
        raise ValueError(f"{name} must be positive")
    return value


class RequestState:
    """Store thread-safe request counters for the two fake endpoints."""

    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._counts = {"embedding": 0, "generation": 0}

    def next_count(self, kind: str) -> int:
        """Increment and return the request count for one endpoint kind."""

        with self._lock:
            self._counts[kind] += 1
            return self._counts[kind]

    def snapshot(self) -> dict[str, int]:
        """Return a copy suitable for the verification script."""

        with self._lock:
            return dict(self._counts)


STATE = RequestState()
EMBEDDING_FIRST_DELAY = _read_non_negative_float(
    "FAKE_EMBEDDING_FIRST_DELAY_SECONDS",
    20.0,
)
GENERATION_FIRST_DELAY = _read_non_negative_float(
    "FAKE_GENERATION_FIRST_DELAY_SECONDS",
    20.0,
)
DEFAULT_DIMENSIONS = _read_positive_int("FAKE_EMBEDDING_DIMENSIONS", 1536)


class FakeProviderHandler(BaseHTTPRequestHandler):
    """Serve the minimal response contracts consumed by the Go clients."""

    server_version = "rag-fake-provider/1"

    def do_GET(self) -> None:  # noqa: N802 - BaseHTTPRequestHandler contract
        """Expose readiness and request counters without user content."""

        if self.path == "/health":
            self._write_json(200, {"status": "ok"})
            return
        if self.path == "/stats":
            self._write_json(200, STATE.snapshot())
            return
        self._write_json(404, {"error": "not found"})

    def do_POST(self) -> None:  # noqa: N802 - BaseHTTPRequestHandler contract
        """Dispatch OpenAI-compatible embedding or generation requests."""

        try:
            payload = self._read_json_body()
        except (json.JSONDecodeError, UnicodeDecodeError, ValueError) as error:
            self._write_json(400, {"error": str(error)})
            return

        if self.path.endswith("/embeddings"):
            self._handle_embeddings(payload)
            return
        if self.path.endswith("/chat/completions"):
            self._handle_generation()
            return
        self._write_json(404, {"error": "not found"})

    def _read_json_body(self) -> dict[str, Any]:
        """Read one bounded JSON object from the request body."""

        raw_length = self.headers.get("Content-Length", "0")
        content_length = int(raw_length)
        if content_length <= 0 or content_length > 4 * 1024 * 1024:
            raise ValueError("request body length is invalid")
        payload = json.loads(self.rfile.read(content_length).decode("utf-8"))
        if not isinstance(payload, dict):
            raise ValueError("request body must be a JSON object")
        return payload

    def _handle_embeddings(self, payload: dict[str, Any]) -> None:
        """Return deterministic vectors and delay only the first call."""

        request_count = STATE.next_count("embedding")
        self._record_event("fake_embedding_request_started", request_count)
        if request_count == 1:
            time.sleep(EMBEDDING_FIRST_DELAY)

        dimensions_value = payload.get("dimensions", DEFAULT_DIMENSIONS)
        dimensions = int(dimensions_value)
        if dimensions <= 0 or dimensions > 4096:
            self._write_json(400, {"error": "dimensions are invalid"})
            return

        raw_input = payload.get("input", [])
        input_items = raw_input if isinstance(raw_input, list) else [raw_input]
        if not input_items:
            self._write_json(400, {"error": "input is required"})
            return

        vector = [1.0] + [0.0] * (dimensions - 1)
        data = [
            {"index": index, "embedding": vector}
            for index, _ in enumerate(input_items)
        ]
        prompt_tokens = sum(max(len(str(item).split()), 1) for item in input_items)
        self._write_json(
            200,
            {
                "data": data,
                "model": payload.get("model", "fake-embedding"),
                "usage": {
                    "prompt_tokens": prompt_tokens,
                    "total_tokens": prompt_tokens,
                },
            },
        )

    def _handle_generation(self) -> None:
        """Return a cited answer and delay only the first generation call."""

        request_count = STATE.next_count("generation")
        self._record_event("fake_generation_request_started", request_count)
        if request_count == 1:
            time.sleep(GENERATION_FIRST_DELAY)

        self._write_json(
            200,
            {
                "choices": [
                    {"message": {"content": "fault recovery answer [1]"}}
                ],
                "usage": {
                    "prompt_tokens": 8,
                    "completion_tokens": 4,
                    "total_tokens": 12,
                },
            },
        )

    def _write_json(self, status_code: int, payload: dict[str, Any]) -> None:
        """Write JSON while tolerating a Worker that was intentionally killed."""

        encoded = json.dumps(payload, separators=(",", ":")).encode("utf-8")
        try:
            self.send_response(status_code)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(encoded)))
            self.end_headers()
            self.wfile.write(encoded)
        except (BrokenPipeError, ConnectionResetError):
            self._record_event("fake_provider_client_disconnected", 0)

    def _record_event(self, event: str, request_count: int) -> None:
        """Emit content-free structured evidence to container stdout."""

        print(
            json.dumps(
                {"event": event, "request_count": request_count},
                separators=(",", ":"),
            ),
            flush=True,
        )

    def log_message(self, _format: str, *_args: Any) -> None:
        """Disable the default request log because it is not needed here."""


def main() -> None:
    """Start the local threaded Provider and stop cleanly on SIGTERM."""

    port = _read_positive_int("FAKE_PROVIDER_PORT", 18080)
    server = ThreadingHTTPServer(("0.0.0.0", port), FakeProviderHandler)

    def request_shutdown(_signum: int, _frame: Any) -> None:
        threading.Thread(target=server.shutdown, daemon=True).start()

    signal.signal(signal.SIGTERM, request_shutdown)
    signal.signal(signal.SIGINT, request_shutdown)
    print(
        json.dumps(
            {"event": "fake_provider_ready", "port": port},
            separators=(",", ":"),
        ),
        flush=True,
    )
    try:
        server.serve_forever(poll_interval=0.1)
    finally:
        server.server_close()


if __name__ == "__main__":
    main()
