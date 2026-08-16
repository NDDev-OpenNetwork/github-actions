from __future__ import annotations

import hashlib

import httpx
from pydantic import BaseModel, ConfigDict


class Record(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)

    identifier: int
    endpoint: str


def summarize(count: int) -> str:
    digest = hashlib.sha256()
    for identifier in range(count):
        record = Record(
            identifier=identifier,
            endpoint=str(httpx.URL(f"https://benchmark.invalid/items/{identifier}")),
        )
        digest.update(record.model_dump_json().encode())
    return digest.hexdigest()


__all__ = ["Record", "summarize"]
