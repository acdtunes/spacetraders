"""Keeps the test suite's protobuf descriptor in lockstep with routing.proto.

WHY THIS EXISTS (sp-07igx). The generated Python stubs are BUILD ARTIFACTS, not
source: gobot/.gitignore ignores `services/routing-service/generated/*.py` and
whitelists only `__init__.py`. The service copes with that because run.sh
regenerates them on every start when they are missing or older than the proto.
pytest had no equivalent step, so the suite imported whatever happened to be on
disk — which meant it validated a schema the service did not necessarily run.

That is worse than a plain red suite, because it fails in BOTH directions:

  * On a developer's checkout the stubs exist but can be STALE. When sp-9idvn
    added `gate_fees` as field 13, tour_handler.py iterated a field the stale
    descriptor did not have, every OptimizeTradeTour raised
    AttributeError('gate_fees'), and 10 tests went red — including
    test_handler_forwards_per_origin_gate_fees, the test that feature's own lane
    wrote to prove the bridge worked. It merged red and nothing saw it.
  * In a FRESH worktree the stubs are absent entirely, because they are ignored
    and were never committed. The suite then cannot even be collected.

Neither result tells you anything about the code under test, and a green run on
a stale descriptor is the dangerous half.

THE RULE HERE IS run.sh's RULE, deliberately: regenerate when the stubs are
MISSING or when routing.proto is NEWER than them. run.sh's own comment records
why the older absence-only guard was not enough — a redeploy that reused a
pre-existing generated/ from before `stock_sources` was added left a stale
runtime descriptor, and the daemon masked the resulting error as an empty "tour
unavailable", aborting all tours for hours (sp-qzej).

IT SHELLS OUT TO generate_protos.sh RATHER THAN REIMPLEMENTING IT. There are
already two copies of the protoc invocation in this tree (run.sh's and
generate_protos.sh's, which differ in their -I root and their sed). A third,
in Python, is exactly the drift this file exists to prevent, so the generator
stays the single definition of how a stub is built.

IT FAILS LOUD. If regeneration cannot run, the suite is stopped rather than
allowed to proceed on an unverifiable descriptor — a passing result on the wrong
schema is the outcome this whole bead is about.
"""

import os
import subprocess
import sys
from pathlib import Path

import pytest

SERVICE_DIR = Path(__file__).resolve().parent
PROTO = SERVICE_DIR.parent.parent / "pkg" / "proto" / "routing" / "routing.proto"
GENERATED = SERVICE_DIR / "generated"
PB2 = GENERATED / "routing_pb2.py"
PB2_GRPC = GENERATED / "routing_pb2_grpc.py"
GENERATOR = SERVICE_DIR / "generate_protos.sh"


def _why_stale():
    """Return why the stubs need rebuilding, or '' when they are current.

    Mirrors run.sh: missing counts, and so does a proto newer than either output.
    A missing routing.proto is NOT treated as "current" — that would silently
    accept whatever stubs are lying around, which is the failure mode above.
    """
    if not PROTO.exists():
        return f"routing.proto not found at {PROTO}"
    if not PB2.exists() or not PB2_GRPC.exists():
        return "generated stubs are missing"
    proto_mtime = PROTO.stat().st_mtime
    if proto_mtime > PB2.stat().st_mtime or proto_mtime > PB2_GRPC.stat().st_mtime:
        return "routing.proto is newer than the generated stubs"
    return ""


def pytest_configure(config):
    """Regenerate before COLLECTION, not before the first test.

    It has to be this hook: the test modules do `from generated import routing_pb2`
    at import time, so a fixture — which runs after collection — would already be
    too late and the stale module would be bound.
    """
    reason = _why_stale()
    if not reason:
        return

    # Put the interpreter running pytest at the front of PATH so the generator's
    # bare `python3` resolves to it. Otherwise a venv-installed grpc_tools is
    # invisible to a script invoked from a different shell environment.
    env = dict(os.environ)
    env["PATH"] = os.pathsep.join([str(Path(sys.executable).parent), env.get("PATH", "")])

    result = subprocess.run(
        ["bash", str(GENERATOR)], capture_output=True, text=True, env=env, cwd=str(SERVICE_DIR)
    )
    if result.returncode != 0 or not PB2.exists() or not PB2_GRPC.exists():
        raise pytest.UsageError(
            f"protobuf stubs are out of date ({reason}) and could not be regenerated, so the "
            f"suite would be testing a schema the service does not run. Refusing to continue.\n"
            f"generate_protos.sh exit={result.returncode}\n"
            f"stdout:\n{result.stdout}\nstderr:\n{result.stderr}\n"
            f"If grpc_tools is missing, install requirements.txt into the interpreter running "
            f"pytest ({sys.executable})."
        )
