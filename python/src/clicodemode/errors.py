"""Exceptions raised by the library itself (a failing program is a Result, not an exception)."""

from __future__ import annotations


class CodeModeError(Exception):
    """Base class."""


class UsageError(CodeModeError, ValueError):
    """Invalid use of the API (bad operation name, duplicate registration...)."""


class EngineError(CodeModeError):
    """The engine rejected a request (for example unknown operation in ``describe``)."""


class GuestCrashed(CodeModeError):
    """The WebAssembly engine stopped unexpectedly. The next call starts a fresh one."""
