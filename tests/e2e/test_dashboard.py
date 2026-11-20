"""
Test dashboard and navigation for FLMX.
Tests dashboard rendering, sidebar navigation, and user interactions.
"""

import pytest
from playwright.sync_api import Page, expect

from pages import DashboardPage, LoginPage
from utils.api_client import ADMIN_USER


@pytest.mark.e2e
class TestDashboard:
    """Dashboard E2E tests."""

    @pytest.fixture(autouse=True)
    def login(self, page: Page, frontend_url: str, browser_admin_password: str):
        """Login before each test."""
        login_page = LoginPage(page, frontend_url)
        login_page.goto()
        result = login_page.login(ADMIN_USER, browser_admin_password)
        assert result, "Login should succeed with valid credentials"

    def test_login_redirects_to_dashboard(self, page: Page, frontend_url: str):
        """Test that login with a changed admin password redirects to dashboard."""
        assert "/dashboard" in page.url

        page.wait_for_load_state("networkidle")

        assert page.locator("nav, [data-testid='sidebar'], aside").count() > 0

    def test_dashboard_shows_user_info(self, page: Page):
        """Test that page shows user information."""
        page.wait_for_load_state("networkidle")

        # The dashboard greets the logged-in admin by name (dashboard.tsx
        # renders the session username), so the name must be visible.
        expect(page.locator("text=admin@user").first).to_be_visible()

    def test_sidebar_navigation(self, page: Page, frontend_url: str):
        """Test sidebar navigation links."""
        page.wait_for_load_state("networkidle")

        # Sidebar items are <nav> entries with Chinese labels and onClick
        # navigation (layouts/admin.tsx) — not <a href> links, so locate
        # them by label inside the nav and assert each landing URL.
        nav_items = [
            ("规则", "/forward"),
            ("节点", "/node"),
            ("用户", "/user"),
            ("设置", "/config"),
        ]

        for label, path in nav_items:
            link = page.locator(f"nav >> text={label}")
            expect(link.first).to_be_visible()
            link.first.click()
            page.wait_for_load_state("networkidle")
            assert path in page.url, (
                f"After clicking sidebar {label!r}, expected {path} in URL, got {page.url}"
            )

    def test_dashboard_responsive_layout(self, page: Page, frontend_url: str):
        """Test dashboard responsive layout."""
        page.set_viewport_size({"width": 375, "height": 667})
        page.wait_for_timeout(500)

        # No horizontal overflow at mobile width, and the dashboard still
        # greets the logged-in user.
        assert page.evaluate("document.documentElement.scrollWidth") <= 375, (
            "dashboard must not overflow horizontally at 375px width"
        )
        expect(page.locator("text=admin@user").first).to_be_visible()

        page.set_viewport_size({"width": 1920, "height": 1080})
        page.wait_for_timeout(500)

        expect(page.locator("nav").first).to_be_visible()


@pytest.mark.e2e
class TestNavigation:
    """Navigation E2E tests."""

    @pytest.fixture(autouse=True)
    def login(self, page: Page, frontend_url: str, browser_admin_password: str):
        """Login before each test."""
        login_page = LoginPage(page, frontend_url)
        login_page.goto()
        result = login_page.login(ADMIN_USER, browser_admin_password)
        assert result, "Login should succeed with valid credentials"

    def test_navigate_to_user_page(self, page: Page, frontend_url: str):
        """Test navigation to user management page."""
        page.goto(f"{frontend_url}/user")
        page.wait_for_load_state("networkidle")

        assert "/user" in page.url

    def test_navigate_to_node_page(self, page: Page, frontend_url: str):
        """Test navigation to node management page."""
        page.goto(f"{frontend_url}/node")
        page.wait_for_load_state("networkidle")

        assert "/node" in page.url

    def test_navigate_to_tunnel_page(self, page: Page, frontend_url: str):
        """Test navigation to tunnel management page."""
        page.goto(f"{frontend_url}/tunnel")
        page.wait_for_load_state("networkidle")

        assert "/tunnel" in page.url

    def test_navigate_to_forward_page(self, page: Page, frontend_url: str):
        """Test navigation to forward management page."""
        page.goto(f"{frontend_url}/forward")
        page.wait_for_load_state("networkidle")

        assert "/forward" in page.url

    def test_navigate_to_config_page(self, page: Page, frontend_url: str):
        """Test navigation to config page."""
        page.goto(f"{frontend_url}/config")
        page.wait_for_load_state("networkidle")

        assert "/config" in page.url
