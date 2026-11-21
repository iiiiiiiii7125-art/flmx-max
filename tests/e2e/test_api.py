"""
Test API endpoints for FLMX backend.
Tests API responses directly without browser.
"""

import os
import shutil
import socket
import subprocess
import time
from pathlib import Path

import pytest

from utils.api_client import ADMIN_SEED_PASSWORD, ADMIN_USER, APIClient, TestUser


@pytest.mark.api
class TestAPIHealth:
    """API health check tests."""

    def test_api_endpoint_reachable(self, api_client: APIClient):
        """Test that API endpoint is reachable."""
        response = api_client.post("/captcha/check")
        assert "code" in response

    def test_captcha_check_endpoint(self, api_client: APIClient):
        """Test captcha check endpoint."""
        response = api_client.post("/captcha/check")
        assert response["code"] == 0
        assert "data" in response


@pytest.mark.api
class TestAPIAuthentication:
    """API authentication tests."""

    def test_login_success(self, api_client: APIClient):
        """Test successful login."""
        response = api_client.login(*TestUser.DEFAULT_ADMIN)
        assert response["code"] == 0
        assert "token" in response["data"]
        assert api_client.token is not None

    def test_login_invalid_user(self, api_client: APIClient):
        """Test login with invalid user."""
        response = api_client.login("nonexistent", "password")
        assert response["code"] != 0

    def test_login_invalid_password(self, api_client: APIClient):
        """Test login with invalid password."""
        response = api_client.login("admin@user", "wrong_password")
        assert response["code"] != 0

    def test_login_empty_username(self, api_client: APIClient):
        """Test login with empty username."""
        response = api_client.login("", "password")
        assert response["code"] != 0

    def test_login_empty_password(self, api_client: APIClient):
        """Test login with empty password."""
        response = api_client.login("admin@user", "")
        assert response["code"] != 0

    def test_protected_endpoint_without_token(self, api_client: APIClient):
        """Test that protected endpoint rejects requests without token."""
        response = api_client.post("/user/list")
        assert response["code"] == 401

    def test_protected_endpoint_with_token(self, authenticated_api: APIClient):
        """Test that protected endpoint accepts requests with token."""
        response = authenticated_api.post("/user/list")
        assert response["code"] == 0
        assert isinstance(response["data"], list)


@pytest.mark.api
class TestAPIUserManagement:
    """API user management tests."""

    def test_list_users(self, authenticated_api: APIClient):
        """Test listing users."""
        response = authenticated_api.post("/user/list")
        assert response["code"] == 0
        users = response["data"]
        assert isinstance(users, list)

    def test_create_and_delete_user(self, authenticated_api: APIClient):
        """Test creating and deleting a user."""
        import uuid

        username = f"test_api_user_{uuid.uuid4().hex[:8]}"

        create_response = authenticated_api.create_user(username, "test123", name="Test User")
        assert create_response.get("code") == 0, f"Failed to create user: {create_response}"

        users = authenticated_api.list_users(username)
        user_id = None
        for u in users:
            if u.get("user") == username:
                user_id = u.get("id")
                break

        assert user_id is not None, f"User {username} not found in list"

        delete_response = authenticated_api.delete_user(user_id)
        assert delete_response.get("code") == 0

    def test_create_duplicate_user(self, authenticated_api: APIClient):
        """Test that creating duplicate user fails."""
        import uuid

        username = f"test_dup_user_{uuid.uuid4().hex[:8]}"

        create1 = authenticated_api.create_user(username, "test123")
        assert create1.get("code") == 0, f"Failed to create first user: {create1}"

        create2 = authenticated_api.create_user(username, "test456")
        assert create2.get("code") != 0, "Creating duplicate user should fail"

        users = authenticated_api.list_users(username)
        for u in users:
            if u.get("user") == username:
                authenticated_api.delete_user(u.get("id"))
                break

    def test_user_package_endpoint(self, authenticated_api: APIClient):
        """Test user package endpoint."""
        response = authenticated_api.post("/user/package")
        assert response["code"] == 0
        assert "userInfo" in response["data"]
        assert "tunnelPermissions" in response["data"]


@pytest.mark.api
class TestAPIConfig:
    """API configuration tests."""

    def test_get_configs(self, authenticated_api: APIClient):
        """Test getting all configs."""
        response = authenticated_api.post("/config/list")
        assert response["code"] == 0
        assert isinstance(response["data"], dict)

    def test_get_single_config(self, authenticated_api: APIClient):
        """Test getting a single config."""
        response = authenticated_api.post("/config/get", {"name": "app_name"})
        # app_name is a seeded config (always present); a non-zero code
        # must fail here, not silently skip the assertions below.
        assert response["code"] == 0, f"config/get app_name failed: {response}"
        assert "value" in response["data"]


@pytest.mark.api
class TestAPINodeManagement:
    """API node management tests."""

    def test_list_nodes(self, authenticated_api: APIClient):
        """Test listing nodes."""
        response = authenticated_api.post("/node/list")
        assert response["code"] == 0
        assert isinstance(response["data"], list)


@pytest.mark.api
class TestAPITunnelManagement:
    """API tunnel management tests."""

    def test_list_tunnels(self, authenticated_api: APIClient):
        """Test listing tunnels."""
        response = authenticated_api.post("/tunnel/list")
        assert response["code"] == 0
        assert isinstance(response["data"], list)


@pytest.mark.api
class TestAPIForwardManagement:
    """API forward management tests."""

    def test_list_forwards(self, authenticated_api: APIClient):
        """Test listing forwards."""
        response = authenticated_api.post("/forward/list")
        assert response["code"] == 0
        assert isinstance(response["data"], list)


@pytest.mark.api
class TestAPIBackup:
    """API backup tests."""

    def test_backup_export(self, authenticated_api: APIClient):
        """Test backup export."""
        response = authenticated_api.backup_export()
        assert "version" in response, f"Expected version in backup response: {response}"


@pytest.mark.api
class TestSeedPasswordChangeFlag:
    """Seed-login requirePasswordChange flag (R18-R2-A5-01).

    The /change-password landing is decided by exactly this flag: the
    frontend routes a login to /change-password iff the login response
    carries requirePasswordChange=true (vite-frontend/src/pages/
    index.tsx), and the backend sets it from isStoredDefaultPassword
    (handler.go). The deleted seed_password_admin fixture assumed the
    flag could be re-armed by changing the password back to the seed —
    it cannot: the first real password change clears the initial-admin
    candidate (clearInitialAdminPassword) and the probe that could
    re-register it only runs when the repository opens, so a changed
    password never counts as "default" again. That premise is therefore
    asserted here instead, against a genuinely fresh backend.

    The test deliberately does NOT use the shared backend: by the time
    any other test runs, authenticated_api has already bootstrapped the
    shared server (password changed once), so a seed login there would
    no longer carry the flag. It builds the backend and starts its own
    instance on a free port with a throwaway file DB, so it is
    independent of execution order and of every other test.
    """

    @pytest.mark.timeout(600)
    def test_seed_login_requires_password_change_then_clears(self, tmp_path: Path):
        """Fresh-backend seed login demands a change; after it, it does not."""
        repo_root = Path(__file__).resolve().parent.parent.parent
        go = shutil.which("go")
        if not go:
            pytest.fail("go toolchain not on PATH; cannot build the backend for this test")
        if not ADMIN_SEED_PASSWORD:
            pytest.fail(
                "FLMX_ADMIN_PASSWORD is not set in the test environment; "
                "the harness (CI e2e jobs / with_server.py) must inject it"
            )

        binary = tmp_path / "paneld"
        build = subprocess.run(
            [go, "build", "-o", str(binary), "./cmd/paneld"],
            cwd=repo_root / "go-backend",
            capture_output=True,
            text=True,
            timeout=600,
        )
        assert build.returncode == 0, f"backend build failed: {build.stderr[-2000:]}"

        with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
            s.bind(("127.0.0.1", 0))
            port = s.getsockname()[1]

        log_path = tmp_path / "paneld.log"
        env = {
            **os.environ,
            "SERVER_ADDR": f":{port}",
            "JWT_SECRET": "test-secret-e2e-key-do-not-use-in-production",
            "FLMX_ADMIN_PASSWORD": ADMIN_SEED_PASSWORD,
            "DB_PATH": str(tmp_path / "seed-flag.db"),
        }
        proc: subprocess.Popen | None = None
        try:
            with open(log_path, "w") as log_file:
                proc = subprocess.Popen(
                    [str(binary)], env=env, stdout=log_file, stderr=subprocess.STDOUT
                )
                deadline = time.time() + 30
                while time.time() < deadline:
                    try:
                        with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as probe:
                            probe.settimeout(1)
                            probe.connect(("127.0.0.1", port))
                            break
                    except OSError:
                        if proc.poll() is not None:
                            break
                        time.sleep(0.2)
                else:
                    pytest.fail(f"fresh backend did not listen on :{port}; see {log_path}")
                if proc.poll() is not None:
                    tail = log_path.read_text(errors="replace")[-2000:]
                    pytest.fail(f"fresh backend exited early (code {proc.returncode}): {tail}")

                client = APIClient(f"http://127.0.0.1:{port}")

                # 1. Seed login on a fresh backend must demand a change.
                # This is the exact condition that routes a browser login
                # to /change-password.
                seed_login = client.login(ADMIN_USER, ADMIN_SEED_PASSWORD)
                assert seed_login.get("code") == 0, f"fresh seed login failed: {seed_login}"
                assert seed_login["data"].get("requirePasswordChange") is True, (
                    "fresh seed login must carry requirePasswordChange=true, "
                    f"got: {seed_login['data']}"
                )
                assert seed_login["data"].get("token"), "fresh seed login must issue a token"

                # 2. Change the password with the seed (restricted) token,
                # then the flag must clear: the next login lands on
                # /dashboard, not /change-password.
                changed_password = "E2eChanged!2026x"
                change = client.post(
                    "/user/updatePassword",
                    {
                        "newUsername": ADMIN_USER,
                        "currentPassword": ADMIN_SEED_PASSWORD,
                        "newPassword": changed_password,
                        "confirmPassword": changed_password,
                    },
                )
                assert change.get("code") == 0, f"password change failed: {change}"
                relogin = client.login(ADMIN_USER, changed_password)
                assert relogin.get("code") == 0, f"re-login failed: {relogin}"
                assert relogin["data"].get("requirePasswordChange") is False, (
                    "login after a real password change must carry "
                    f"requirePasswordChange=false, got: {relogin['data']}"
                )
        finally:
            if proc is not None:
                proc.terminate()
                try:
                    proc.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    proc.kill()
                    proc.wait(timeout=5)

