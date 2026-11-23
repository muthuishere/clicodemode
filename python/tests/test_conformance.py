"""Drive the repo's language-neutral conformance suite through the Python API."""

import pytest

import conformance_runner as cr

if not cr.CASES_DIR.is_dir():  # e.g. tests run from an sdist outside the repo
    pytest.skip(f"conformance cases not found at {cr.CASES_DIR}", allow_module_level=True)

CASES = cr.load_cases()


def test_case_count() -> None:
    assert len(CASES) >= 250


@pytest.mark.parametrize("case", CASES, ids=[c["id"] for c in CASES])
def test_conformance(case) -> None:  # noqa: ANN001
    outcome, msg = cr.classify(case)
    cr.record(case["id"], outcome, msg)
    if outcome == "unsupported":
        pytest.skip(f"unsupported by the Python wrapper: {msg}")
    if outcome == "deviation":
        pytest.skip(f"known Go deviation (still fails here): {msg}")
    if outcome == "fixed":
        pytest.skip("known Go deviation, but this build passes the fixture")
    if outcome == "known_issue":
        pytest.skip(f"known issue: {msg}")
    assert outcome == "pass", msg
