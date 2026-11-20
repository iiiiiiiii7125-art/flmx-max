"""
Test user management UI for FLMX.
Tests user CRUD operations through the web interface.
"""

import pytest
from playwright.sync_api import Page, expect

from pages import LoginPage, UserPage
from utils.api_client import ADMIN_USER


@pytest.mark.e2e
@pytest.mark.slow
class TestUserManagementUI:
    """User management UI E2E tests."""

    @pytest.fixture(autouse=True)
    def login(self, page: Page, frontend_url: str, browser_admin_password: str):
        """Login before each test."""
        login_page = LoginPage(page, frontend_url)
        login_page.goto()
        result = login_page.login(ADMIN_USER, browser_admin_password)
        assert result, "Login should succeed with valid credentials"

    def test_user_page_loads(self, page: Page, frontend_url: str):
        """Test that user management page loads."""
        user_page = UserPage(page, frontend_url)
        user_page.goto()

        page.wait_for_load_state("networkidle")
        assert "/user" in page.url

    def test_user_list_displays(self, page: Page, frontend_url: str, test_user):
        """Test that user list displays correctly."""
        user_page = UserPage(page, frontend_url)
        user_page.goto()
        page.wait_for_load_state("networkidle")

        # The admin (role_id 0) is excluded from /user/list by design
        # (ListUsers filters role_id != 0), so assert on a user that is
        # actually listed: the API-created test_user fixture.
        expect(page.locator("table").first).to_be_visible()
        expect(page.locator(f"text={test_user.username}").first).to_be_visible()

    def test_create_user_dialog(self, page: Page, frontend_url: str):
        """Test opening create user dialog."""
        user_page = UserPage(page, frontend_url)
        user_page.goto()
        page.wait_for_load_state("networkidle")

        create_btn = page.locator('button:has-text("创建"), button:has-text("新增")')
        expect(create_btn.first).to_be_visible()
        create_btn.first.click()
        page.wait_for_timeout(500)

        # The create modal header renders only when the dialog opens.
        expect(page.locator("text=新增用户").first).to_be_visible()

    def test_search_users(self, page: Page, frontend_url: str, test_user):
        """Test user search functionality."""
        user_page = UserPage(page, frontend_url)
        user_page.goto()
        page.wait_for_load_state("networkidle")

        search_input = page.locator('input[placeholder*="搜索"], input[placeholder*="search"]')
        expect(search_input.first).to_be_visible()
        search_input.first.fill(test_user.username)
        search_input.first.press("Enter")
        page.wait_for_load_state("networkidle")

        expect(page.locator(f"text={test_user.username}").first).to_be_visible()

    def test_user_pagination(self, page: Page, frontend_url: str):
        """Test user list pagination."""
        user_page = UserPage(page, frontend_url)
        user_page.goto()
        page.wait_for_load_state("networkidle")

        pagination = page.locator('[data-testid="pagination"], .pagination, nav[aria-label*="pagination"]')
        if pagination.count() > 0:
            next_btn = page.locator('button:has-text("下一页"), button[aria-label*="next"]')
            if next_btn.count() > 0 and not next_btn.first.is_disabled():
                next_btn.first.click()
                page.wait_for_load_state("networkidle")
