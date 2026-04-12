import { defineConfig, devices } from '@playwright/test'

export default defineConfig({
  testDir: './specs',
  timeout: 30000,
  use: {
    baseURL: 'http://localhost:7127',
    headless: true,
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
})
