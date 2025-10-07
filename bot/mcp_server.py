"""Backward-compatible import shim for the MCP server entrypoint."""

from __future__ import annotations

from typing import Any, Dict, Iterable, List

from mcp.types import TextContent

from spacetraders_bot.core import market_data as _market_data
from spacetraders_bot.integrations.mcp_bridge import (
    build_parser,
    handle_market,
    handle_players,
    main,
)

__all__ = [
    "build_parser",
    "handle_market",
    "handle_players",
    "main",
    "call_tool",
]


def _format_goods_table(goods: Iterable[Dict[str, Any]]) -> str:
    lines = []
    for entry in goods:
        lines.append(
            " - {symbol}: buy {buy} cr, sell {sell} cr, supply {supply}, activity {activity}".format(
                symbol=entry.get("good_symbol", "?"),
                buy=entry.get("purchase_price", "?"),
                sell=entry.get("sell_price", "?"),
                supply=entry.get("supply", "unknown"),
                activity=entry.get("activity", "unknown"),
            )
        )
    return "\n".join(lines) if lines else " - No goods available."


async def call_tool(name: str, arguments: Dict[str, Any]) -> List[TextContent]:
    """Async wrapper used by tests and MCP adapters.

    Returns a list of ``TextContent`` items matching the shape of the original
    MCP server implementation.
    """

    if name == "bot_market_waypoint":
        waypoint_symbol = arguments["waypoint_symbol"]
        good_symbol = arguments.get("good_symbol")
        if good_symbol:
            entry = _market_data.get_waypoint_good(waypoint_symbol, good_symbol)
            if not entry:
                body = f"No market data available for {good_symbol} at {waypoint_symbol}."
            else:
                body = (
                    f"Market intel for {waypoint_symbol} ({good_symbol})\n"
                    f" - Buy price: {entry.get('purchase_price', 'n/a')}\n"
                    f" - Sell price: {entry.get('sell_price', 'n/a')}\n"
                    f" - Supply: {entry.get('supply', 'unknown')}\n"
                    f" - Activity: {entry.get('activity', 'unknown')}"
                )
        else:
            goods = _market_data.get_waypoint_goods(waypoint_symbol)
            body = (
                f"Market intel for {waypoint_symbol}\n" + _format_goods_table(goods)
            )
        return [TextContent(type="text", text=body)]

    if name == "bot_market_find_sellers":
        markets = _market_data.find_markets_selling(
            arguments["good_symbol"],
            system=arguments.get("system"),
            min_supply=arguments.get("min_supply"),
            updated_within_hours=arguments.get("updated_within_hours"),
            limit=arguments.get("limit"),
        )
        if not markets:
            body = "No sellers found for {symbol} in {system}.".format(
                symbol=arguments["good_symbol"],
                system=arguments.get("system", "the selected region"),
            )
        else:
            lines = [
                "Best selling markets for {symbol} in {system}:".format(
                    symbol=arguments["good_symbol"],
                    system=arguments.get("system", "the sector"),
                )
            ]
            for idx, entry in enumerate(markets, start=1):
                lines.append(
                    " {idx}. {waypoint} – {price} cr ({supply} supply)".format(
                        idx=idx,
                        waypoint=entry.get("waypoint_symbol", "?"),
                        price=entry.get("purchase_price", "?"),
                        supply=entry.get("supply", "unknown"),
                    )
                )
            body = "\n".join(lines)
        return [TextContent(type="text", text=body)]

    if name == "bot_market_find_buyers":
        markets = _market_data.find_markets_buying(
            arguments["good_symbol"],
            system=arguments.get("system"),
            min_activity=arguments.get("min_activity"),
            updated_within_hours=arguments.get("updated_within_hours"),
            limit=arguments.get("limit"),
        )
        if not markets:
            body = "No buyers found for {symbol} in {system}.".format(
                symbol=arguments["good_symbol"],
                system=arguments.get("system", "the selected region"),
            )
        else:
            lines = [
                "Best buying markets for {symbol} in {system}:".format(
                    symbol=arguments["good_symbol"],
                    system=arguments.get("system", "the sector"),
                )
            ]
            for idx, entry in enumerate(markets, start=1):
                lines.append(
                    " {idx}. {waypoint} – {price} cr ({activity} activity)".format(
                        idx=idx,
                        waypoint=entry.get("waypoint_symbol", "?"),
                        price=entry.get("sell_price", "?"),
                        activity=entry.get("activity", "unknown"),
                    )
                )
            body = "\n".join(lines)
        return [TextContent(type="text", text=body)]

    if name == "bot_market_summarize_good":
        summary = _market_data.summarize_good(
            arguments["good_symbol"],
            system=arguments.get("system"),
        )
        system = arguments.get("system", "the sector")
        if not summary:
            body = "No market summary available for {symbol} in {system}.".format(
                symbol=arguments["good_symbol"],
                system=system,
            )
        else:
            lines = [
                f"Summary for {summary['good_symbol']} in {summary.get('system', system)}:",
                f" - Average sell: {summary.get('average_sell_price', 'n/a')} cr",
                f" - Average buy: {summary.get('average_buy_price', 'n/a')} cr",
                f" - Sellers: {summary.get('seller_count', 0)}",
                f" - Buyers: {summary.get('buyer_count', 0)}",
            ]
            body = "\n".join(lines)
        return [TextContent(type="text", text=body)]

    raise ValueError(f"Unknown tool requested: {name}")
