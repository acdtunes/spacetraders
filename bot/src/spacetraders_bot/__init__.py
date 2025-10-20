"""SpaceTraders bot package."""

from importlib import metadata
import sys
import types

try:
    __version__ = metadata.version("spacetraders-mcp-server")
except metadata.PackageNotFoundError:  # pragma: no cover - during local dev
    __version__ = "0.0.0"

from . import operations as _operations_module
from .core import (
    api_client as _api_client_module,
    ship_assignment_repository as _assignment_manager_module,
    daemon_manager as _daemon_manager_module,
    database as _database_module,
    market_data as _market_data_module,
    operation_checkpointer as _operation_controller_module,
    route_planner as _routing_module,
    market_scout as _scout_coordinator_module,
    ship as _ship_controller_module,
    smart_navigator as _smart_navigator_module,
    system_graph_provider as _system_graph_provider_module,
    utils as _utils_module,
)

_COMPAT_MODULES = {
    "api_client": _api_client_module,
    "assignment_manager": _assignment_manager_module,
    "daemon_manager": _daemon_manager_module,
    "database": _database_module,
    "market_data": _market_data_module,
    "operation_controller": _operation_controller_module,
    "routing": _routing_module,
    "scout_coordinator": _scout_coordinator_module,
    "ship_controller": _ship_controller_module,
    "smart_navigator": _smart_navigator_module,
    "system_graph_provider": _system_graph_provider_module,
    "utils": _utils_module,
    "operations": _operations_module,
}

for alias, module in _COMPAT_MODULES.items():  # pragma: no cover - shim for legacy imports
    sys.modules.setdefault(alias, module)

# Provide compatibility namespace for legacy `lib.*` imports
_legacy_lib = types.ModuleType("lib")
_legacy_lib.__path__ = []  # type: ignore[attr-defined]
sys.modules.setdefault("lib", _legacy_lib)
for alias, module in _COMPAT_MODULES.items():  # pragma: no cover
    setattr(_legacy_lib, alias, module)
    sys.modules.setdefault(f"lib.{alias}", module)

__all__ = ["__version__"]
