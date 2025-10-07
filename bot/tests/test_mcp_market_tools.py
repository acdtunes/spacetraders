#!/usr/bin/env python3
"""Tests for market-related MCP server tools."""

import importlib
import sys
import types
from pathlib import Path

import pytest

REPO_ROOT = Path(__file__).parent.parent

src_dir = REPO_ROOT / "src"
if str(src_dir) not in sys.path:
    sys.path.insert(0, str(src_dir))
if str(REPO_ROOT) not in sys.path:
    sys.path.insert(0, str(REPO_ROOT))

# Provide lightweight MCP stubs if the real package isn't available.
if "mcp.server" not in sys.modules:
    mcp_pkg = types.ModuleType("mcp")
    server_mod = types.ModuleType("mcp.server")
    stdio_mod = types.ModuleType("mcp.server.stdio")
    types_mod = types.ModuleType("mcp.types")

    class _StubServer:
        def __init__(self, name: str):
            self.name = name

        def list_tools(self):
            def decorator(func):
                return func

            return decorator

        def call_tool(self):
            def decorator(func):
                return func

            return decorator

    def _stub_stdio_server(*args, **kwargs):  # pragma: no cover - not used in tests
        raise NotImplementedError

    class _StubTool:
        def __init__(self, name, description, inputSchema=None):
            self.name = name
            self.description = description
            self.inputSchema = inputSchema or {}

    class _StubTextContent:
        def __init__(self, type: str, text: str):
            self.type = type
            self.text = text

    server_mod.Server = _StubServer
    stdio_mod.stdio_server = _stub_stdio_server
    types_mod.Tool = _StubTool
    types_mod.TextContent = _StubTextContent

    sys.modules["mcp"] = mcp_pkg
    sys.modules["mcp.server"] = server_mod
    sys.modules["mcp.server.stdio"] = stdio_mod
    sys.modules["mcp.types"] = types_mod

mcp_server = importlib.import_module("mcp_server")
market_data = importlib.import_module("spacetraders_bot.core.market_data")


@pytest.mark.asyncio
async def test_market_waypoint_lists_all_goods(monkeypatch):
    """Waypoint tool should format multiple goods into readable text."""

    def fake_get_waypoint_goods(waypoint_symbol, *, db=None, db_path=None):
        assert waypoint_symbol == "X1-TEST-A1"
        return [
            {
                "waypoint_symbol": waypoint_symbol,
                "good_symbol": "IRON_ORE",
                "purchase_price": 45,
                "sell_price": 70,
                "supply": "HIGH",
                "activity": "STRONG",
                "trade_volume": 120,
                "last_updated": "2025-10-05T14:15:00",
            },
            {
                "waypoint_symbol": waypoint_symbol,
                "good_symbol": "COPPER",
                "purchase_price": 110,
                "sell_price": 155,
                "supply": "MODERATE",
                "activity": "FAIR",
                "trade_volume": 80,
                "last_updated": "2025-10-05T13:55:00",
            },
        ]

    def fake_get_waypoint_good(waypoint_symbol, good_symbol, *, db=None, db_path=None):
        pytest.fail("Single-good lookup should not be called when good_symbol is omitted")

    monkeypatch.setattr(market_data, "get_waypoint_goods", fake_get_waypoint_goods)
    monkeypatch.setattr(market_data, "get_waypoint_good", fake_get_waypoint_good)

    result = await mcp_server.call_tool(
        "bot_market_waypoint",
        {"waypoint_symbol": "X1-TEST-A1"},
    )

    assert len(result) == 1
    text = result[0].text
    assert "Market intel for X1-TEST-A1" in text
    assert "IRON_ORE" in text
    assert "COPPER" in text


@pytest.mark.asyncio
async def test_market_find_sellers_formats_results(monkeypatch):
    """Selling search should show ordered list with pricing."""

    def fake_find_markets_selling(
        good_symbol,
        *,
        system=None,
        min_supply=None,
        updated_within_hours=None,
        limit=10,
        db=None,
        db_path=None,
    ):
        assert good_symbol == "IRON_ORE"
        assert system == "X1-TEST"
        assert min_supply == "MODERATE"
        assert updated_within_hours == 4.5
        assert limit == 3
        return [
            {
                "waypoint_symbol": "X1-TEST-A2",
                "purchase_price": 42,
                "supply": "ABUNDANT",
                "last_updated": "2025-10-05T14:00:00",
            },
            {
                "waypoint_symbol": "X1-TEST-B1",
                "purchase_price": 48,
                "supply": "HIGH",
                "last_updated": "2025-10-05T13:00:00",
            },
        ]

    monkeypatch.setattr(market_data, "find_markets_selling", fake_find_markets_selling)

    result = await mcp_server.call_tool(
        "bot_market_find_sellers",
        {
            "good_symbol": "IRON_ORE",
            "system": "X1-TEST",
            "min_supply": "MODERATE",
            "updated_within_hours": 4.5,
            "limit": 3,
        },
    )

    assert len(result) == 1
    text = result[0].text
    assert "Best selling markets for IRON_ORE" in text
    assert "1. X1-TEST-A2" in text
    assert "2. X1-TEST-B1" in text


@pytest.mark.asyncio
async def test_market_summarize_handles_missing_data(monkeypatch):
    """Summary tool should report when no data available."""

    def fake_summarize_good(good_symbol, *, system=None, db=None, db_path=None):
        assert good_symbol == "ALUMINUM_ORE"
        assert system == "X1-TEST"
        return None

    monkeypatch.setattr(market_data, "summarize_good", fake_summarize_good)

    result = await mcp_server.call_tool(
        "bot_market_summarize_good",
        {"good_symbol": "ALUMINUM_ORE", "system": "X1-TEST"},
    )

    assert len(result) == 1
    text = result[0].text
    assert "No market summary available" in text
    assert "X1-TEST" in text
