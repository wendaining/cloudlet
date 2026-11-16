import { test, expect } from '@playwright/test';
import { loadConfig } from '../../scripts/development/config.mjs';

test('登录、持久化会话和退出', async ({ page }, info) => {
  const config = await loadConfig();
  const pageErrors = [];
  page.on('pageerror', error => pageErrors.push(error.message));
  await page.goto('/');
  await page.getByRole('textbox', { name: '邮箱' }).fill(config.env.CLOUDLET_DEV_EMAIL);
  await page.getByLabel('密码', { exact: true }).fill('incorrect-password');
  await page.getByRole('button', { name: '登录', exact: true }).click();
  await expect(page.getByRole('alert')).toHaveText('邮箱或密码错误');
  await page.getByLabel('密码', { exact: true }).fill(config.env.CLOUDLET_DEV_PASSWORD);
  await page.getByRole('button', { name: '登录', exact: true }).click();
  await expect(page.getByRole('heading', { name: '环境概览' })).toBeVisible();
  await expect(page.getByText(config.instance, { exact: true })).toBeVisible();
  await page.reload();
  await expect(page.getByRole('heading', { name: '环境概览' })).toBeVisible();
  const width = await page.evaluate(() => ({ content: document.documentElement.scrollWidth, viewport: innerWidth }));
  expect(width.content).toBeLessThanOrEqual(width.viewport);
  await page.screenshot({ path: info.outputPath('overview.png'), fullPage: true });
  await page.getByRole('button', { name: '退出登录' }).click();
  await expect(page.getByRole('textbox', { name: '邮箱' })).toBeVisible();
  expect(pageErrors).toEqual([]);
});
