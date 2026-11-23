import sys
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "examples"))

from clicodemode import CodeMode  # noqa: E402


@pytest.fixture
def cm():
    engine = CodeMode("1.0.0")
    yield engine
    engine.close()


def pytest_terminal_summary(terminalreporter, exitstatus, config):  # noqa: ANN001
    import conformance_runner as cr

    if cr.TALLY:
        terminalreporter.section("conformance totals (Python wrapper)")
        terminalreporter.write_line(cr.summary())
        for kind in ("unsupported", "fail"):
            for cid, why in cr.DETAILS[kind]:
                terminalreporter.write_line(f"  {kind}: {cid}: {why}")
