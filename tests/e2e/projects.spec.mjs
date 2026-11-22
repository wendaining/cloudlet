import { test, expect } from '@playwright/test';
import { loadConfig } from '../../scripts/development/config.mjs';

const projectName = `E2E 项目 ${process.pid}-${Date.now()}`;

test('登录后创建项目、显示列表、读取详情并刷新', async ({ page }, info) => {
  const config = await loadConfig();
  const pageErrors = [];
  page.on('pageerror', error => pageErrors.push(error.message));

  await page.goto('/');
  await page.getByRole('textbox', { name: '邮箱' }).fill(config.env.CLOUDLET_DEV_EMAIL);
  await page.getByLabel('密码', { exact: true }).fill(config.env.CLOUDLET_DEV_PASSWORD);
  await page.getByRole('button', { name: '登录', exact: true }).click();
  await expect(page.getByRole('heading', { name: '环境概览' })).toBeVisible();
  await page.getByRole('button', { name: '进入项目控制台' }).click();
  await expect(page.getByRole('heading', { name: '项目' })).toBeVisible();
  await expect(page.getByRole('textbox', { name: '项目名称' })).toBeVisible();

  await page.getByRole('textbox', { name: '项目名称' }).fill(projectName);
  await page.getByRole('button', { name: '创建项目', exact: true }).click();
  await expect(page.getByText('项目标识')).toBeVisible();

  const detailURL = page.url();
  const projectID = detailURL.split('/projects/')[1];
  await page.reload();
  await expect(page.getByText(projectName, { exact: true }).first()).toBeVisible();
  expect(projectID).toMatch(/^[0-9a-f-]{36}$/i);

  await page.getByRole('link', { name: '返回项目列表' }).click();
  await expect(page.getByRole('link', { name: projectName })).toBeVisible();
  await page.screenshot({ path: info.outputPath('projects.png'), fullPage: true });
  expect(pageErrors).toEqual([]);
});
