import { test, expect } from '@playwright/test'

test.describe('Navigation', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/')
  })

  test('renders header and nav', async ({ page }) => {
    await expect(page.getByTestId('app-header')).toBeVisible()
    await expect(page.getByTestId('app-nav')).toBeVisible()
  })

  test('nav links are present', async ({ page }) => {
    const navLinks = ['nav-agent', 'nav-live', 'nav-chat', 'nav-skills', 'nav-mcp', 'nav-memory', 'nav-status', 'nav-query', 'nav-documents', 'nav-settings']
    for (const id of navLinks) {
      await expect(page.getByTestId(id)).toBeVisible()
    }
  })

  test('language switcher works', async ({ page }) => {
    await page.getByTestId('lang-zh').click()
    await expect(page.getByTestId('lang-zh')).toBeVisible()
    await page.getByTestId('lang-en').click()
    await expect(page.getByTestId('lang-en')).toBeVisible()
  })

  test('navigates to chat page', async ({ page }) => {
    await page.getByTestId('nav-chat').click()
    await expect(page).toHaveURL(/\/chat/)
  })

  test('navigates to skills page', async ({ page }) => {
    await page.getByTestId('nav-skills').click()
    await expect(page).toHaveURL(/\/skills/)
  })

  test('navigates to mcp page', async ({ page }) => {
    await page.getByTestId('nav-mcp').click()
    await expect(page).toHaveURL(/\/mcp/)
  })

  test('navigates to memory page', async ({ page }) => {
    await page.getByTestId('nav-memory').click()
    await expect(page).toHaveURL(/\/memory/)
  })

  test('navigates to status page', async ({ page }) => {
    await page.getByTestId('nav-status').click()
    await expect(page).toHaveURL(/\/status/)
  })

  test('navigates to query page', async ({ page }) => {
    await page.getByTestId('nav-query').click()
    await expect(page).toHaveURL(/\/query/)
  })

  test('navigates to documents page', async ({ page }) => {
    await page.getByTestId('nav-documents').click()
    await expect(page).toHaveURL(/\/documents/)
  })
})
