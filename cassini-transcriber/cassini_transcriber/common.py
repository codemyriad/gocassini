from __future__ import annotations

from typing import Any


def seconds_to_ms(value: Any) -> int:
    return max(0, int(round(float(value) * 1000)))
