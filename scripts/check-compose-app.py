#!/usr/bin/env python3
"""Validate the app profile with synthetic env files; never start containers."""

import argparse
import json
import os
from pathlib import Path
import shlex
import subprocess
import sys
import tempfile


def require(condition, message):
    if not condition:
        raise ValueError(message)


def configuration(root, directory, app_name, overrides, profile):
    values = {
        "DEV_MYSQL_ROOT_PASSWORD": "compose-fixture-only",
        "DEV_REDIS_PASSWORD": "compose-fixture-only",
        "DEV_APP_NAME": app_name,
        **overrides,
    }
    env_file = directory / "compose.env"
    env_file.write_text("".join(f"{key}={value}\n" for key, value in values.items()))
    env_file.chmod(0o600)
    environment = {
        key: value for key, value in os.environ.items()
        if not key.startswith(("DEV_", "APP_", "COMPOSE_"))
    }
    command = [
        "docker", "compose", "--project-name", "compose-contract-fixture",
        "--env-file", str(env_file), "-f", str(root / "compose.yaml"),
    ]
    if profile:
        command += ["--profile", "app"]
    result = subprocess.run(
        command + ["config", "--format", "json"], env=environment,
        text=True, capture_output=True, check=True, timeout=20,
    )
    return json.loads(result.stdout)


def validate_app(model, app_name):
    app = model["services"]["app"]
    require(app["profiles"] == ["app"], "application must require its explicit profile")
    require(app["build"]["args"]["APP_NAME"] == app_name, "build identity drift")
    require(app["command"] == ["server"], "application command drift")
    entry = app["entrypoint"]
    require(entry[:2] == ["/bin/sh", "-ec"], "missing checked entrypoint")
    require("id -u" in entry[2] and "-gt 0" in entry[2], "missing non-root guard")
    require('exec /app/bin/server' in entry[2], "server must replace the entry shell")
    require(app["stop_signal"] == "SIGTERM", "stop signal drift")
    require(app["stop_grace_period"] in {"3m", "3m0s", "180s"}, "shutdown grace drift")
    require(app["healthcheck"]["test"] == [
        "CMD", "wget", "-q", "-O", "/dev/null", "http://127.0.0.1:8008/ready",
    ], "readiness probe drift")
    for dependency in ["mysql", "redis"]:
        require(app["depends_on"][dependency]["condition"] == "service_healthy", "dependency health drift")
    mounts = {volume["target"]: volume for volume in app["volumes"]}
    for target in ["/app/config/env/env.yml", "/app/config/pem", "/app/logs"]:
        volume = mounts[target]
        require(volume["type"] == "bind", "unexpected managed volume")
        require(not volume.get("bind", {}).get("create_host_path", False), "missing mount must fail")
    require(mounts["/app/config/env/env.yml"]["read_only"], "config must be read-only")
    require(mounts["/app/config/pem"]["read_only"], "keys must be read-only")
    require(not mounts["/app/logs"].get("read_only", False), "logs must be writable")
    environment = app["environment"]
    require(environment["APP_ROOT"] == "/app", "APP_ROOT drift")
    require(environment["APP_APPLICATION__HOST"] == "0.0.0.0", "container bind drift")
    require(environment["APP_APPLICATION__PORT"] == "8008", "container port drift")
    require(environment["APP_ENV"] == "dev", "profile must remain local development")
    require(environment["APP_LOG__PATH"] == "/app/logs/app", "log path drift")
    require(app["ports"][0]["host_ip"] == "127.0.0.1", "host port must remain loopback")
    return app


def verify_entry_guard(directory, entry):
    # Execute only the shell guard in a host fixture. It proves argument/exit
    # wiring, not Docker UID mapping or an application container's PID 1.
    tools = directory / "tools"
    tools.mkdir()
    identity = tools / "id"
    identity.write_text('#!/bin/sh\nprintf "%s\\n" "$FIXTURE_UID"\n')
    identity.chmod(0o700)
    server = tools / "server"
    server.write_text('#!/bin/sh\nprintf "%s %s\\n" "$$" "$*" > "$FIXTURE_TRACE"\n')
    server.chmod(0o700)
    trace = directory / "trace"
    script = entry[2].replace("$$", "$").replace("/app/bin/server", shlex.quote(str(server)))
    environment = {
        **os.environ, "PATH": str(tools) + os.pathsep + os.defpath,
        "FIXTURE_TRACE": str(trace), "FIXTURE_UID": "0",
    }
    rejected = subprocess.run(
        ["/bin/sh", "-ec", script, "compose-app", "server"],
        env=environment, capture_output=True, text=True, check=False,
    )
    require(rejected.returncode != 0 and not trace.exists(), "root guard allowed server startup")
    environment["FIXTURE_UID"] = "501"
    accepted = subprocess.Popen(
        ["/bin/sh", "-ec", script, "compose-app", "server"],
        env=environment, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
    )
    try:
        stdout, stderr = accepted.communicate(timeout=5)
    except subprocess.TimeoutExpired:
        accepted.kill()
        accepted.communicate()
        raise
    require(accepted.returncode == 0, "non-root entry guard failed: " + stdout + stderr)
    require(trace.read_text().strip() == f"{accepted.pid} server", "exec/argument ownership drift")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--app-name", required=True)
    args = parser.parse_args()
    root = Path(__file__).resolve().parent.parent
    with tempfile.TemporaryDirectory(prefix="gin-api-compose-contract-") as temporary:
        directory = Path(temporary)
        default = configuration(root, directory, args.app_name, {}, False)
        active = sorted(name for name, service in default["services"].items() if not service.get("profiles"))
        require(active == ["mysql", "redis"], "default startup set changed")
        app = validate_app(configuration(root, directory, args.app_name, {}, True), args.app_name)
        require(app["user"] == "1000:1000", "default runtime identity drift")
        require(app["environment"]["APP_DATABASE__HOST"] == "mysql", "default DB service drift")
        require(app["environment"]["APP_REDIS__ADDR"] == "redis:6379", "default Redis service drift")
        verify_entry_guard(directory, app["entrypoint"])
        custom = validate_app(configuration(root, directory, args.app_name, {
            "DEV_APP_UID": "501", "DEV_APP_GID": "20", "DEV_APP_PORT": "18559",
            "DEV_APP_DB_HOST": "host.docker.internal", "DEV_APP_DB_NAME": "owned_fixture",
            "DEV_APP_DB_PASSWORD": "", "DEV_APP_REDIS_ADDR": "host.docker.internal:6379",
            "DEV_APP_REDIS_PASSWORD": "", "DEV_BUILD_COMMIT": "fixture", "DEV_BUILD_TIME": "unknown",
        }, True), args.app_name)
        require(custom["user"] == "501:20", "custom runtime identity drift")
        require(str(custom["ports"][0]["published"]) == "18559", "host port override drift")
        require(custom["environment"]["APP_DATABASE__PASSWORD"] == "", "explicit empty DB password lost")
        require(custom["environment"]["APP_REDIS__PASSWORD"] == "", "explicit empty Redis password lost")
        require(custom["environment"]["APP_DATABASE__NAME"] == "owned_fixture", "DB name override lost")
        require(custom["build"]["args"]["BUILD_COMMIT"] == "fixture", "build identity override lost")
    print("OK: Compose profile/default set, mounts, local overrides, empty passwords, health, exec and non-root guard")


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, KeyError, subprocess.SubprocessError) as failure:
        print(f"FAIL: Compose contract: {failure}", file=sys.stderr)
        sys.exit(1)
