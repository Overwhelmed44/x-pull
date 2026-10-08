#!/usr/bin/env python3
"""Exercise the built single-container image against a host HTTP fixture."""
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

ROOT = Path(__file__).resolve().parents[1]
IMAGE = sys.argv[1] if len(sys.argv) > 1 else "x-pull:local"


def docker(*args):
    return subprocess.run(
        ["docker", *args], check=True, text=True, capture_output=True, timeout=30
    ).stdout.strip()


def wait_for(predicate, description):
    deadline = time.monotonic() + 20
    while time.monotonic() < deadline:
        if predicate():
            return
        time.sleep(0.1)
    raise AssertionError(f"Timed out: {description}")


snapshot = {"users": json.loads((ROOT / "configs/users.example.json").read_text())["users"]}
snapshot_lock = threading.Lock()


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        with snapshot_lock:
            body = json.dumps(snapshot).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass


server = ThreadingHTTPServer(("0.0.0.0", 0), Handler)
threading.Thread(target=server.serve_forever, daemon=True).start()
containers = []
try:
    with tempfile.TemporaryDirectory(prefix="x-pull-container-") as directory:
        path = Path(directory)
        # The official image runs as UID 65532, which must read bind mounts.
        path.chmod(0o755)
        config = json.loads((ROOT / "configs/poller.docker.example.json").read_text())
        config["poll_interval"] = "100ms"
        config["inbounds"][0]["allow_empty"] = True
        config["inbounds"][0]["source"]["url"] = (
            f"http://host.docker.internal:{server.server_port}/users"
        )
        poller_path = path / "poller.json"
        poller_path.write_text(json.dumps(config))
        poller_path.chmod(0o644)

        def start(*extra):
            container = docker(
                "run", "-d", "--add-host=host.docker.internal:host-gateway",
                "-e", "NO_PROXY=localhost,127.0.0.1,host.docker.internal",
                "--mount", f"type=bind,src={poller_path},dst=/etc/x-pull/poller.json,readonly",
                *extra, IMAGE,
            )
            containers.append(container)
            return container

        def users(container):
            try:
                result = json.loads(docker(
                    "exec", container, "/usr/local/bin/xray", "api", "inbounduser",
                    "--server=127.0.0.1:10085", "--timeout=1", "-tag=vless-in",
                ))
                return {u["email"]: u for u in result.get("users", [])}
            except (subprocess.CalledProcessError, json.JSONDecodeError):
                return None

        container = start()
        wait_for(lambda: users(container) is not None and len(users(container)) == 2, "HTTP users added")
        processes = docker("top", container)
        assert "/usr/local/bin/x-pull-container" in processes
        assert "/usr/local/bin/xray run" in processes
        assert "/usr/local/bin/x-pull -config" in processes
        with snapshot_lock:
            snapshot["users"] = snapshot["users"][:1]
            snapshot["users"][0]["level"] = 2
        wait_for(
            lambda: (users(container) or {}).get("pull:alice@example.com", {}).get("level") == 2
            and len(users(container) or {}) == 1,
            "user updated and stale user removed",
        )
        with snapshot_lock:
            snapshot["users"] = []
        wait_for(lambda: users(container) == {}, "empty snapshot revokes managed users")
        docker("stop", "--time=12", container)
        assert docker("inspect", "-f", "{{.State.ExitCode}}", container) == "0"
        # JSON logs are on stderr; inspect both streams for graceful child exits.
        result = subprocess.run(["docker", "logs", container], capture_output=True, text=True, check=True)
        assert '"process":"xray"' in result.stderr and '"process":"poller"' in result.stderr
        assert result.stderr.count('"msg":"child stopped"') == 2

        # Invalid configuration must fail before long-running children start.
        bad_config = path / "bad-xray.json"
        bad_config.write_text("{")
        bad_config.chmod(0o644)
        failed = start("--mount", f"type=bind,src={bad_config},dst=/etc/x-pull/xray.json,readonly")
        assert docker("wait", failed) != "0", "invalid configuration did not fail container"

        # Poller configuration is also validated before starting children.
        bad_poller = path / "bad-poller.json"
        bad_poller.write_text("{}")
        bad_poller.chmod(0o644)
        failed = docker(
            "run", "-d", "--mount",
            f"type=bind,src={bad_poller},dst=/etc/x-pull/poller.json,readonly", IMAGE,
        )
        containers.append(failed)
        assert docker("wait", failed) != "0", "invalid poller configuration did not fail container"
    print("Container smoke test passed: both processes, HTTP add/update/remove, graceful shutdown, invalid config.")
except Exception:
    for container in containers:
        subprocess.run(["docker", "logs", "--tail=25", container], check=False)
    raise
finally:
    for container in containers:
        subprocess.run(["docker", "rm", "-f", "-v", container], capture_output=True)
    server.shutdown()
    server.server_close()
