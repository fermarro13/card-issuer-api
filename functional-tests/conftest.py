import pytest


@pytest.fixture(autouse=True)
def announce_test_execution(request: pytest.FixtureRequest) -> None:
    print(f"START {request.node.nodeid}")
    yield
    print(f"END {request.node.nodeid}")
