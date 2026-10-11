#!/usr/bin/env python3
"""Verify store-library lifecycle, worker inactivity and local configuration."""
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import tempfile
from urllib.request import urlopen

from home_plugin_host import Plugin


def run(library, scenario):
    data = Path(os.environ["CPA_HOME_EVAL_DATA_DIR"])
    port = int(os.environ["CPA_HOME_EVAL_LISTEN"].split(":")[-1])
    plugin = Plugin(library)
    registration = plugin.register()
    if scenario == "incomplete":
        assert not registration.get("ok")
        assert registration["error"]["code"] == "home_panel_start_failed"
        assert not data.exists()
    else:
        assert registration["ok"], registration
        assert registration["result"]["metadata"]["Name"] == "cpa-home-codex-eval"
        assert plugin.call("management.register")["result"] == {"routes": [], "resources": []}
        assert plugin.register("plugin.reconfigure")["ok"]
        if scenario == "worker":
            assert not data.exists(), "worker wrote panel state"
        else:
            with urlopen(f"http://127.0.0.1:{port}/", timeout=5) as response:
                assert "window.CPA_HOME_EVAL = true" in response.read().decode()
            assert data.is_dir()
    plugin.close()
    with socket.socket() as sock:
        sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        sock.bind(("127.0.0.1", port))
    if scenario == "active":
        # Go libraries may remain mapped after dlclose; re-enable the same instance.
        assert plugin.register()["ok"]
        with urlopen(f"http://127.0.0.1:{port}/", timeout=5) as response:
            assert response.status == 200
        plugin.close()
    print(json.dumps({"scenario": scenario, "passed": True}))


if __name__ == "__main__":
    library = str(Path(sys.argv[1]).resolve())
    if len(sys.argv) > 2:
        run(library, sys.argv[2])
    else:
        for scenario in ("worker", "incomplete", "active"):
            with tempfile.TemporaryDirectory(prefix="cpa-home-plugin-") as tmp:
                with socket.socket() as sock:
                    sock.bind(("127.0.0.1", 0))
                    port = sock.getsockname()[1]
                env = {k: v for k, v in os.environ.items() if not k.startswith("CPA_HOME_")}
                env.update(CPA_HOME_URL="http://127.0.0.1:9", CPA_HOME_EVAL_LISTEN=f"127.0.0.1:{port}", CPA_HOME_EVAL_DATA_DIR=str(Path(tmp, "state")))
                if scenario != "worker":
                    env["CPA_HOME_MANAGEMENT_KEY"] = "synthetic-home-key"
                if scenario == "active":
                    env["CPA_HOME_EVAL_KEY"] = "synthetic-panel-key"
                subprocess.run([sys.executable, __file__, library, scenario], env=env, check=True)
        print("Home store plugin lifecycle passed: inert workers, no partial configuration, reconfigure, disable, re-enable and port release.")
