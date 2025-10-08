"""Utilities to normalize pytest-bdd data tables for step helpers."""

from __future__ import annotations

from typing import Iterable, Sequence


def table_to_rows(table: str | None = None, datatable: Iterable[Sequence[str]] | None = None) -> list[list[str]]:
    """Return a list of trimmed rows from either legacy string tables or pytest-bdd datatables."""

    if datatable is not None:
        return [[cell.strip() for cell in row] for row in datatable if any(cell.strip() for cell in row)]

    if table is None:
        return []

    rows: list[list[str]] = []
    lines = [line.strip() for line in str(table).strip().split('\n') if line.strip()]
    for line in lines:
        if not line or line.startswith('|-'):
            continue
        rows.append([cell.strip() for cell in line.split('|') if cell.strip()])
    return rows
