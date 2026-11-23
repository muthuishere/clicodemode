"""Plain data types mirroring the engine's JSON (spec ``codemode/v0``)."""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Callable, Dict, List, Optional

SIDE_EFFECTS = ("none", "local", "external")


@dataclass(frozen=True)
class Limits:
    """Per-run bounds enforced inside the engine. ``0`` means the engine default."""

    max_steps: int = 0
    max_call_depth: int = 0
    max_concurrency: int = 0
    timeout_ms: int = 0

    def to_json(self) -> Dict[str, int]:
        return {
            "max_steps": self.max_steps,
            "max_call_depth": self.max_call_depth,
            "max_concurrency": self.max_concurrency,
            "timeout_ms": self.timeout_ms,
        }


@dataclass(frozen=True)
class Operation:
    """One callable unit. Names are dotted segments, e.g. ``window.focus`` (case-sensitive)."""

    name: str
    description: str = ""
    input_schema: Optional[Dict[str, Any]] = None
    output_schema: Optional[Dict[str, Any]] = None
    side_effects: str = "local"
    idempotent: bool = False
    authorization: str = ""
    examples: List[Dict[str, Any]] = field(default_factory=list)

    def to_json(self) -> Dict[str, Any]:
        d: Dict[str, Any] = {
            "name": self.name,
            "description": self.description,
            "side_effects": self.side_effects,
            "idempotent": self.idempotent,
        }
        if self.input_schema is not None:
            d["input_schema"] = self.input_schema
        if self.output_schema is not None:
            d["output_schema"] = self.output_schema
        if self.authorization:
            d["authorization"] = self.authorization
        if self.examples:
            d["examples"] = self.examples
        return d

    @classmethod
    def from_json(cls, d: Dict[str, Any]) -> "Operation":
        return cls(
            name=d["name"],
            description=d.get("description", ""),
            input_schema=d.get("input_schema"),
            output_schema=d.get("output_schema"),
            side_effects=d.get("side_effects", "local"),
            idempotent=bool(d.get("idempotent", False)),
            authorization=d.get("authorization", ""),
            examples=list(d.get("examples") or []),
        )


@dataclass(frozen=True)
class Policy:
    """Gate consulted before *every* operation call.

    ``allow(op)`` returns ``False`` (or raises) to deny; ``True``/``None``
    allow. A denial rejects the call inside the program (catchable with
    try/catch) and the handler never runs.
    """

    allow: Optional[Callable[[Operation], Optional[bool]]] = None


@dataclass(frozen=True)
class Diagnostic:
    severity: str
    file: str
    line: int
    col: int
    message: str
    hint: str = ""

    @classmethod
    def from_json(cls, d: Dict[str, Any]) -> "Diagnostic":
        return cls(
            severity=d.get("severity", "error"),
            file=d.get("file", ""),
            line=int(d.get("line", 0)),
            col=int(d.get("col", 0)),
            message=d.get("message", ""),
            hint=d.get("hint", "") or "",
        )

    def __str__(self) -> str:
        s = f"{self.file}:{self.line}:{self.col}: {self.severity}: {self.message}"
        return s + (f" (hint: {self.hint})" if self.hint else "")


@dataclass
class Result:
    """Outcome of ``run``/``eval``. A program that fails is still a Result."""

    ok: bool
    value: Any = None
    error: str = ""
    diagnostics: List[Diagnostic] = field(default_factory=list)
    stdout: str = ""
    ms: int = 0

    @property
    def nothing_ran(self) -> bool:
        """True when the program was rejected before running (syntax/unsupported)."""
        return (not self.ok) and any(d.severity == "error" for d in self.diagnostics) and (
            "nothing was run" in self.error or "syntax error" in self.error
        )


@dataclass
class CompileResult:
    """Static plan for a program: hash, operations it references, cache key."""

    file: str
    hash: str
    imports: List[str]
    operations: List[str]
    cache_key: str
    diagnostics: List[Diagnostic]


@dataclass
class Capabilities:
    spec: str
    version: str
    fingerprint: str
    language: Dict[str, Any]
    operations: List[Operation]
