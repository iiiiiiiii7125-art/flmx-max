"""
Test authentication flow for FLMX.
Tests login, logout, session management, and protected routes.
"""

import pytest
from playwright.sync_api import Page, expect

from pages import LoginPage, DashboardPage
from utils.api_client import ADMIN_USER, APIClient


@pytest.mark.e2e
class TestAuthentication:
    """Authentication E2E tests."""

    def test_login_page_loads(self, page: Page, frontend_url: str):
        """Test that login page loads correctly."""
        login_page = LoginPage(page, frontend_url)
        login_page.goto()

        expect(page).to_have_url(f"{frontend_url}/")
        expect(login_page.username_input).to_be_visible()
        expect(login_page.password_input).to_be_visible()
        expect(login_page.login_button).to_be_visible()

    def test_login_with_invalid_credentials(self, page: Page, frontend_url: str):
        """Test login fails with invalid credentials."""
        login_page = LoginPage(page, frontend_url)
        login_page.goto()

        result = login_page.login("invalid_user", "invalid_password")
        assert not result, "Login should fail with invalid credentials"

        expect(page).to_have_url(f"{frontend_url}/")

    def test_login_with_empty_username(self, page: Page, frontend_url: str):
        """Test login validation for empty username."""
        login_page = LoginPage(page, frontend_url)
        login_page.goto()

        login_page.password_input.fill("some_password")
        login_page.login_button.click()

        page.wait_for_timeout(500)

        expect(page).to_have_url(f"{frontend_url}/")

    def test_login_with_empty_password(self, page: Page, frontend_url: str):
        """Test login validation for empty password."""
        login_page = LoginPage(page, frontend_url)
        login_page.goto()

        login_page.username_input.fill("some_user")
        login_page.login_button.click()

        page.wait_for_timeout(500)

        expect(page).to_have_url(f"{frontend_url}/")

    def test_protected_route_redirects_to_login(self, page: Page, frontend_url: str):
        """Test that protected routes redirect to login when not authenticated."""
        page.goto(f"{frontend_url}/dashboard")
        page.wait_for_load_state("networkidle")

        expect(page).to_have_url(f"{frontend_url}/")

    def test_session_persists_on_refresh(
        self, page: Page, frontend_url: str, browser_admin_password: str
    ):
        """Test that session persists after page refresh."""
        login_page = LoginPage(page, frontend_url)
        login_page.goto()
        result = login_page.login(ADMIN_USER, browser_admin_password)
        assert result, "Login should succeed with valid credentials"

        assert "/dashboard" in page.url

        page.reload()
        page.wait_for_load_state("networkidle")

        assert "/dashboard" in page.url

    def test_logout_clears_session(
        self, page: Page, frontend_url: str, backend_url: str, browser_admin_password: str
    ):
        """Test that logout clears the session."""
        login_page = LoginPage(page, frontend_url)
        login_page.goto()
        result = login_page.login(ADMIN_USER, browser_admin_password)
        assert result, "Login should succeed with valid credentials"

        assert "/dashboard" in page.url

        # Capture the session token before logging out: the server must
        # revoke it, so a later API call with it has to come back 401.
        token = page.evaluate("localStorage.getItem('token')")
        assert token, "expected a session token in localStorage after login"

        # Real product logout path: the /profile button runs safeLogout()
        # (POST /user/logout revokes server-side, then local state clears
        # and the app routes back to the login page).
        page.goto(f"{frontend_url}/profile")
        page.wait_for_load_state("networkidle")
        dashboard_page = DashboardPage(page, frontend_url)
        expect(dashboard_page.logout_button.first).to_be_visible()
        dashboard_page.logout_button.first.click()
        expect(page).to_have_url(f"{frontend_url}/")

        client = APIClient(backend_url)
        client.set_token(token)
        response = client.post("/user/list", {})
        assert response["code"] == 401, (
            f"revoked token must be rejected with code 401, got: {response}"
        )
