import { defineConfig } from '@playwright/test'

export default defineConfig({ testDir: './e2e', workers: 1, timeout: 60000, use: { browserName: 'chromium', viewport: { width: 1440, height: 900 }, trace: 'retain-on-failure' }, reporter: 'list' })
