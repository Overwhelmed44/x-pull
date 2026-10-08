"""Smoke test the image against a host HTTP endpoint; needs Docker and Python."""
import json
import subprocess
import sys
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
IMAGE = sys.argv[1] if len(sys.argv) > 1 else "x-pull:local"
snapshot = json.loads((ROOT / "configs/users.example.json").read_text())


def docker(*args):
    return subprocess.check_output(["docker", *args], text=True, timeout=30).strip()


class Source(BaseHTTPRequestHandler):
    def do_GET(self):
        body = json.dumps(snapshot).encode()
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass


server = ThreadingHTTPServer(("0.0.0.0", 0), Source)
threading.Thread(target=server.serve_forever, daemon=True).start()
container = None
try:
    with tempfile.TemporaryDirectory() as directory:
        config = json.loads((ROOT / "configs/poller.example.json").read_text())
        config["poll_interval"] = "100ms"
        config["inbounds"][0]["source"]["url"] = f"http://host.docker.internal:{server.server_port}/users"
        path = Path(directory) / "poller.json"
        path.write_text(json.dumps(config))
        container = docker(
            "run", "-d", "--add-host=host.docker.internal:host-gateway",
            "-e", "NO_PROXY=localhost,127.0.0.1,host.docker.internal",
            "--mount", f"type=bind,src={path},dst=/etc/x-pull/poller.json,readonly", IMAGE,
        )

        def users():
            output = docker("exec", container, "/usr/local/bin/xray", "api", "inbounduser",
                            "--server=127.0.0.1:10085", "--timeout=1", "-tag=vless-in")
            return json.loads(output).get("users", [])

        def wait_for(check):
            deadline = time.monotonic() + 20
            while time.monotonic() < deadline:
                try:
                    if check():
                        return
                except subprocess.CalledProcessError:
                    pass
                time.sleep(0.1)
            raise AssertionError("container did not reach expected state")

        wait_for(lambda: len(users()) == 2)
        snapshot["users"] = snapshot["users"][:1]
        snapshot["users"][0]["level"] = 2
        wait_for(lambda: len(users()) == 1 and users()[0].get("level") == 2)
        # Restart just Xray through the installed Supervisor, then repopulate.
        docker("exec", container, "supervisorctl", "restart", "xray")
        wait_for(lambda: len(users()) == 1)
        snapshot["users"] = []
        wait_for(lambda: users() == [])
        processes = docker("top", container)
        assert "supervisord" in processes and "xray run" in processes and "x-pull -config" in processes
        docker("stop", "--time=25", container)
        assert docker("inspect", "-f", "{{.State.ExitCode}}", container) == "0"
        print("Container passed: HTTP sync, Supervisor restart, clean shutdown.")
finally:
    if container:
        subprocess.run(["docker", "logs", "--tail=5", container])
        subprocess.run(["docker", "rm", "-f", "-v", container], stdout=subprocess.DEVNULL)
    server.shutdown()
    server.server_close()
