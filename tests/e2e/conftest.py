"""
Pytest configuration and fixtures for FLMX E2E tests.
"""

import json
import os
import socket
import time
from pathlib import Path
from typing import Any, Callable, Generator, Optional

import pytest
from playwright.sync_api import APIRequestContext, BrowserContext, Page, Playwright

from utils.api_client import APIClient, TestUser

DEFAULT_BACKEND_PORT = 6365
DEFAULT_FRONTEND_PORT = 3000
DEFAULT_JWT_SECRET = "test-secret-e2e-key-do-not-use-in-production"
# Must match the backend seed (go-backend repository.go seedData). The old
# value "admin_user" is only a default-*password candidate* in the backend,
# never a username — every login with it failed by construction (X02-002).
DEFAULT_ADMIN_USER = "admin@user"
DEFAULT_ADMIN_PASSWORD = "admin@user"
# The seed admin's token is restricted to /user/updatePassword until the
# default password is changed (backend M-05), so authenticated fixtures
# change it once per server lifetime and use this password afterwards.
E2E_ADMIN_PASSWORD = os.getenv("E2E_ADMIN_PASSWORD", "E2eAdmin!2026x")


def _login_wait_rate_limit(api_client: APIClient, username: str, password: str) -> dict:
    """Login, waiting out the per-IP rate-limit bucket (10/min) if needed.

    Only relevant when re-running against a long-lived server whose
    bucket is still warm from a previous run; a fresh server (the
    supported mode: with_server.py / CI) never triggers the wait.
    """
    response = api_client.login(username, password)
    for _ in range(6):
        if response.get("code") != 429:
            break
        time.sleep(10)
        response = api_client.login(username, password)
    return response


def bootstrap_admin_client(api_client: APIClient) -> APIClient:
    """Log in as the seed admin and return a client with a full token.

    Flow: login with the seed credentials; if the response asks for a
    password change, change it to E2E_ADMIN_PASSWORD and re-login. If the
    seed login fails outright, the password was probably already changed
    against a long-lived server — fall back to E2E_ADMIN_PASSWORD.
    """
    response = _login_wait_rate_limit(api_client, DEFAULT_ADMIN_USER, DEFAULT_ADMIN_PASSWORD)
    if response.get("code") != 0:
        response = _login_wait_rate_limit(api_client, DEFAULT_ADMIN_USER, E2E_ADMIN_PASSWORD)
        if response.get("code") != 0:
            raise RuntimeError(f"E2E admin login failed: {response}")
        return api_client

    if response.get("data", {}).get("requirePasswordChange"):
        change = api_client.post(
            "/user/updatePassword",
            {
                "newUsername": DEFAULT_ADMIN_USER,
                "currentPassword": DEFAULT_ADMIN_PASSWORD,
                "newPassword": E2E_ADMIN_PASSWORD,
                "confirmPassword": E2E_ADMIN_PASSWORD,
            },
        )
        if change.get("code") != 0:
            raise RuntimeError(f"E2E admin password change failed: {change}")
        relogin = _login_wait_rate_limit(api_client, DEFAULT_ADMIN_USER, E2E_ADMIN_PASSWORD)
        if relogin.get("code") != 0:
            raise RuntimeError(f"E2E admin re-login failed: {relogin}")
    return api_client


def get_server_info() -> dict:
    """Get server info from environment or .server_info.json."""
    info_file = Path(__file__).parent / ".server_info.json"
    if info_file.exists():
        with open(info_file) as f:
            return json.load(f)
    return {
        "backend_port": int(os.getenv("E2E_BACKEND_PORT", DEFAULT_BACKEND_PORT)),
        "frontend_port": int(os.getenv("E2E_FRONTEND_PORT", DEFAULT_FRONTEND_PORT)),
        "jwt_secret": os.getenv("E2E_JWT_SECRET", DEFAULT_JWT_SECRET),
    }


@pytest.fixture(scope="session")
def server_info() -> dict:
    """Server configuration info."""
    return get_server_info()


@pytest.fixture(scope="session")
def backend_url(server_info: dict) -> str:
    """Backend API base URL."""
    return f"http://localhost:{server_info['backend_port']}"


@pytest.fixture(scope="session")
def frontend_url(server_info: dict) -> str:
    """Frontend base URL."""
    return f"http://localhost:{server_info['frontend_port']}"


@pytest.fixture(scope="session")
def api_base_url(backend_url: str) -> str:
    """API base URL for APIRequestContext."""
    return f"{backend_url}/api/v1"


@pytest.fixture(scope="session")
def browser_type_launch_args():
    """Browser launch arguments."""
    return {
        "headless": True,
    }


@pytest.fixture(scope="session")
def browser_context_args():
    """Browser context arguments."""
    return {
        "viewport": {"width": 1280, "height": 720},
        "locale": "zh-CN",
    }


@pytest.fixture
def page(context: BrowserContext) -> Generator[Page, None, None]:
    """Create a new page with standard settings."""
    p = context.new_page()
    p.set_default_timeout(10000)
    yield p
    p.close()


@pytest.fixture
def api_client(backend_url: str) -> APIClient:
    """Create API client instance."""
    return APIClient(backend_url)


@pytest.fixture(scope="session")
def authenticated_api(backend_url: str, server_info: dict) -> Generator[APIClient, None, None]:
    """Create authenticated API client (full, unrestricted token).

    Session-scoped on purpose: the backend rate-limits logins per IP
    (10/min), so bootstrapping per test (login + password change +
    re-login) exhausts the bucket and later tests fail with 429.
    The no-server gate runs here too: session fixtures are instantiated
    before the function-scoped autouse gate, so without this check a
    missing backend surfaced as a raw ConnectionError instead of the
    explicit skip/fail (defined below; resolved at call time).

    Teardown restores the seed password (best effort) so the suite
    leaves the server as it found it: test_login_success logs in with
    the seed credentials, and repeated runs against a long-lived
    server must stay idempotent.
    """
    _gate_no_server(_probe_missing([(server_info["backend_port"], "backend")]))
    client = bootstrap_admin_client(APIClient(backend_url))
    yield client
    try:
        client.post(
            "/user/updatePassword",
            {
                "newUsername": DEFAULT_ADMIN_USER,
                "currentPassword": E2E_ADMIN_PASSWORD,
                "newPassword": DEFAULT_ADMIN_PASSWORD,
                "confirmPassword": DEFAULT_ADMIN_PASSWORD,
            },
        )
    except Exception:
        pass


@pytest.fixture
def test_user(authenticated_api: APIClient) -> Generator[TestUser, None, None]:
    """Create a test user for the test."""
    user = TestUser.create_test_user(authenticated_api)
    yield user
    user.cleanup()


@pytest.fixture
def clean_users(authenticated_api: APIClient) -> Generator[Callable[..., dict], None, None]:
    """Clean up test users after test."""
    created_ids: list[int] = []

    def _create_user(username: str, password: str = "test123", **kwargs: Any) -> dict:
        response = authenticated_api.create_user(username, password, **kwargs)
        if response.get("code") == 0:
            user_id = response.get("data", {}).get("id")
            if user_id:
                created_ids.append(user_id)
        return response

    yield _create_user

    for user_id in created_ids:
        try:
            authenticated_api.delete_user(user_id)
        except Exception:
            pass


@pytest.fixture
def clean_nodes(authenticated_api: APIClient) -> Generator[Callable[..., dict], None, None]:
    """Clean up test nodes after test."""
    created_ids: list[int] = []

    def _create_node(name: str, address: str = "127.0.0.1", **kwargs: Any) -> dict:
        response = authenticated_api.create_node(name, address, **kwargs)
        if response.get("code") == 0:
            node_id = response.get("data", {}).get("id")
            if node_id:
                created_ids.append(node_id)
        return response

    yield _create_node

    for node_id in created_ids:
        try:
            authenticated_api.delete_node(node_id)
        except Exception:
            pass


@pytest.fixture
def api_context(playwright: Playwright, api_base_url: str) -> Generator[APIRequestContext, None, None]:
    """API request context for testing backend directly."""
    context = playwright.request.new_context(base_url=api_base_url)
    yield context
    context.dispose()


@pytest.fixture
def auth_token(authenticated_api: APIClient) -> Optional[str]:
    """Get a full (unrestricted) authentication token for API calls."""
    return authenticated_api.token


@pytest.fixture
def fresh_db_path(tmp_path: Path) -> str:
    """Path for a fresh test database."""
    return str(tmp_path / "test.db")


_no_server_skips = {"count": 0}


def _probe_missing(ports) -> list:
    """Return descriptions of the given (port, name) servers not listening."""
    missing = []
    for port, name in ports:
        try:
            with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
                s.settimeout(1)
                s.connect(("localhost", port))
        except OSError:
            missing.append(f"{name} server not available on port {port}")
    return missing


def _gate_no_server(missing) -> None:
    """Skip (explicit E2E_SKIP_OK only) or hard-fail when servers are missing."""
    if not missing:
        return
    detail = "; ".join(missing)
    if os.getenv("E2E_SKIP_OK"):
        _no_server_skips["count"] += 1
        pytest.skip(f"{detail} (E2E_SKIP_OK set)")
    pytest.fail(
        f"{detail}; start the servers first (see with_server.py) "
        "or set E2E_SKIP_OK=1 to skip explicitly"
    )


@pytest.fixture(autouse=True)
def skip_if_no_server(request, server_info: dict):
    """Gate tests on the servers they actually need.

    API-marked tests only need the backend; everything else needs the
    frontend too. Missing servers used to skip the whole suite silently
    (pytest still exited 0 — X02-002). Now a missing server is a hard
    failure unless E2E_SKIP_OK is explicitly set, and explicit skips are
    totalled in the terminal summary so they cannot pass unnoticed.
    """
    ports = [(server_info["backend_port"], "backend")]
    if request.node.get_closest_marker("api") is None:
        ports.append((server_info["frontend_port"], "frontend"))
    _gate_no_server(_probe_missing(ports))


def pytest_terminal_summary(terminalreporter, exitstatus, config):
    """Make explicit no-server skips visible in the run summary."""
    if _no_server_skips["count"]:
        terminalreporter.write_sep(
            "=",
            f"E2E: no-server skip gate fired {_no_server_skips['count']} "
            "time(s); affected tests were skipped (E2E_SKIP_OK was set)",
        )


def pytest_configure(config):
    """Configure pytest markers."""
    config.addinivalue_line("markers", "e2e: End-to-end test requiring running servers")
    config.addinivalue_line("markers", "auth: Test requires authentication")
    config.addinivalue_line("markers", "slow: Slow running test")
    config.addinivalue_line("markers", "api: API-only test (no browser needed)")
