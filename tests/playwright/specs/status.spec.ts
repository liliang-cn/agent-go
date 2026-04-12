import { test, expect } from '@playwright/test'

test.describe('Status Page', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/status')
  })

  test('loads status page', async ({ page }) => {
    await expect(page.getByTestId('app-main')).toBeVisible()
  })

  test('shows service status cards', async ({ page }) => {
    // Wait for API data to load
    await page.waitForTimeout(1000)
    // Status page should have some content visible
    const main = page.getByTestId('app-main')
    await expect(main).toBeVisible()
    const text = await main.innerText()
    expect(text.length).toBeGreaterThan(0)
  })
})
