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

from utils.api_client import ADMIN_SEED_PASSWORD, ADMIN_USER, APIClient, TestUser

DEFAULT_BACKEND_PORT = 6365
DEFAULT_FRONTEND_PORT = 3000
DEFAULT_JWT_SECRET = "test-secret-e2e-key-do-not-use-in-production"
# Must match the backend seed (go-backend repository.go seedData). Since
# 2.0.0 the seed password is random unless the operator injects one via
# the FLMX_ADMIN_PASSWORD env var; the harness (CI job / with_server.py)
# always injects it, and the fixtures read the same variable through
# utils.api_client (FIX-CI-1). The old literal value was retired with
# the public default password — logging in with it fails by design.
DEFAULT_ADMIN_USER = ADMIN_USER
DEFAULT_ADMIN_PASSWORD = ADMIN_SEED_PASSWORD
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
            hint = ""
            if not DEFAULT_ADMIN_PASSWORD:
                hint = (
                    " (FLMX_ADMIN_PASSWORD is not set in the test environment:"
                    " start the backend with it — see the CI e2e-api job or"
                    " with_server.py — or read the one-time initial password"
                    " file named in the backend startup log)"
                )
            raise RuntimeError(f"E2E admin login failed: {response}{hint}")
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


@pytest.fixture
def browser_admin_password(authenticated_api: APIClient) -> str:
    """Effective admin password for browser login.

    Requesting authenticated_api runs the session bootstrap once, so by
    the time a browser test logs in the admin password is
    E2E_ADMIN_PASSWORD (not the seed) and requirePasswordChange is
    false — browser login therefore lands on /dashboard. This keeps
    browser tests deterministic regardless of run order: a bare seed
    login would land on /change-password on a fresh server but on
    /dashboard once any api test has bootstrapped.
    """
    return E2E_ADMIN_PASSWORD


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


@pytest.fixture(scope="function")
def browser(browser_type, browser_type_launch_args):
    """Fresh browser per test (overrides pytest-playwright's session one).

    A single browser process accumulates memory over the suite's 21
    heavy page loads (unminified dev bundles, one fresh context per
    test): late in a run page loads degrade past every timeout and the
    browser process itself has been observed dying mid-run (SIGILL/CFI
    crash in the sandbox probe). A per-test browser keeps each test
    isolated and bounds that growth; the launch cost (~1s per test)
    is small next to the page loads.
    """
    b = browser_type.launch(**browser_type_launch_args)
    yield b
    b.close()


@pytest.fixture(scope="session")
def admin_storage_state(tmp_path_factory, browser_type, browser_type_launch_args, authenticated_api, frontend_url) -> str:
    """One logged-in browser session, reused by marked browser tests.

    The suite performs ~20 logins in ~2 min against the backend's
    10/min login IP bucket, so per-test form logins get 429'd mid-run
    — LoginPage.login retries, but they only sometimes outlast the
    60s window (run 11 green, runs 10/12 not). Log in once via the UI,
    save the storage state, and let marked classes reuse it: the only
    UI logins left are this one plus test_auth's own flows. Requesting
    authenticated_api runs the session bootstrap first, so the admin
    password is E2E_ADMIN_PASSWORD by the time we log in. The token
    is minted after any earlier logout revocation (the fixture is
    instantiated lazily at the first marked test, which runs after
    test_auth), so it is not caught by the TokensRevokedBefore
    watermark.
    """
    from pages import LoginPage

    state_path = tmp_path_factory.mktemp("e2e-login") / "admin-storage-state.json"
    browser = browser_type.launch(**browser_type_launch_args)
    try:
        context = browser.new_context(viewport={"width": 1280, "height": 720}, locale="zh-CN")
        page = context.new_page()
        page.set_default_timeout(30000)
        login_page = LoginPage(page, frontend_url)
        login_page.goto()
        if not login_page.login(ADMIN_USER, E2E_ADMIN_PASSWORD):
            raise RuntimeError(
                f"storage-state login failed: {login_page.last_login_error}"
            )
        context.storage_state(path=str(state_path))
        return str(state_path)
    finally:
        browser.close()


@pytest.fixture
def browser_context_args(request):
    """Browser context arguments.

    Tests marked reuse_login get a context restored from the
    session's logged-in storage state (see admin_storage_state), so
    they start authenticated without a per-test form login; every
    other test gets a fresh logged-out context. The storage state is
    only instantiated for marked tests (getfixturevalue), so API-only
    runs never launch a browser.
    """
    args = {
        "viewport": {"width": 1280, "height": 720},
        "locale": "zh-CN",
    }
    if request.node.get_closest_marker("reuse_login"):
        args["storage_state"] = request.getfixturevalue("admin_storage_state")
    return args


@pytest.fixture
def page(context: BrowserContext) -> Generator[Page, None, None]:
    """Create a new page with standard settings.

    30s default timeout: page loads go through the Vite dev server,
    whose on-demand transforms are slow under load (shared CI runners
    and this sandbox alike) — the previous 10s made goto/networkidle
    flake late in a run, long after the app itself had loaded. The
    per-test pytest-timeout (60s, pyproject addopts) still bounds
    every test, so a genuinely hung page still fails fast.
    """
    p = context.new_page()
    p.set_default_timeout(30000)
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
    _refresh_admin_token_if_revoked(authenticated_api)
    user = TestUser.create_test_user(authenticated_api)
    _resolve_test_user_id(authenticated_api, user)
    yield user
    user.cleanup()


def _refresh_admin_token_if_revoked(api_client: APIClient) -> None:
    """Re-login the session API client if its token was revoked.

    A browser logout test revokes every token issued before the logout
    (TokensRevokedBefore watermark, E1-02), including this session
    client's token minted at bootstrap — later fixture creates then
    fail with 401 and the user UI shows an empty list. Probe with a
    cheap authenticated call; on 401 re-login directly with the
    post-bootstrap password (one attempt, rate-limit-aware).
    """
    probe = api_client.post("/user/package")
    if probe.get("code") != 401:
        return
    response = _login_wait_rate_limit(api_client, DEFAULT_ADMIN_USER, E2E_ADMIN_PASSWORD)
    if response.get("code") != 0:
        raise RuntimeError(f"E2E admin re-login failed: {response}")


def _resolve_test_user_id(api_client: APIClient, user: TestUser) -> None:
    """Fill in TestUser.user_id so cleanup() can actually delete it.

    /user/create returns code 0 with no data/id, so create_test_user
    leaves user_id None and cleanup() is a no-op — test users then
    accumulate and a later create of the same username fails as a
    duplicate (the test still passes on the leftover row, for the
    wrong reason). Resolve the id from /user/list instead.
    """
    if user.user_id is not None:
        return
    listing = api_client.post("/user/list")
    if listing.get("code") != 0:
        return
    for item in listing.get("data") or []:
        if item.get("user") == user.username:
            user.user_id = item.get("id")
            return


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
