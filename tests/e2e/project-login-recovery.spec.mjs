import { test, expect } from '@playwright/test';
import { loadConfig } from '../../scripts/development/config.mjs';
import { capture } from '../../scripts/development/process.mjs';

test('登录后恢复项目深链并保留越权项目的通用 404', async ({ page, browser }) => {
  const config = await loadConfig();
  const projectName = '深链恢复 ' + process.pid + '-' + Date.now();

  await page.goto('/');
  await page.getByRole('textbox', { name: '邮箱' }).fill(config.env.CLOUDLET_DEV_EMAIL);
  await page.getByLabel('密码', { exact: true }).fill(config.env.CLOUDLET_DEV_PASSWORD);
  await page.getByRole('button', { name: '登录', exact: true }).click();
  await expect(page.getByRole('heading', { name: '环境概览' })).toBeVisible();

  await page.getByRole('button', { name: '进入项目控制台' }).click();
  await page.getByRole('textbox', { name: '项目名称' }).fill(projectName);
  await page.getByRole('button', { name: '创建项目', exact: true }).click();
  await expect(page.getByText(projectName, { exact: true }).first()).toBeVisible();
  const detailURL = page.url();

  await page.goto('/');
  await expect(page.getByRole('heading', { name: '环境概览' })).toBeVisible();
  await page.getByRole('button', { name: '退出登录' }).click();
  await expect(page.getByRole('textbox', { name: '邮箱' })).toBeVisible();
  await page.goto(detailURL);
  await expect(page.getByRole('textbox', { name: '邮箱' })).toBeVisible();
  await page.getByRole('textbox', { name: '邮箱' }).fill(config.env.CLOUDLET_DEV_EMAIL);
  await page.getByLabel('密码', { exact: true }).fill(config.env.CLOUDLET_DEV_PASSWORD);
  await page.getByRole('button', { name: '登录', exact: true }).click();

  await expect(page.getByText(projectName, { exact: true }).first()).toBeVisible();
  expect(page.url()).toBe(detailURL);
  await page.reload();
  await expect(page.getByText(projectName, { exact: true }).first()).toBeVisible();

  await page.goto('/projects/00000000-0000-4000-8000-000000000000');
  await expect(page.getByText('项目不存在或没有访问权限')).toBeVisible();

  const outsiderEmail = `outsider-${process.pid}-${Date.now()}@cloudlet.local`;
  const outsiderPassword = 'outsider-integration-password';
  await capture('go', ['run', './cmd/admin', 'seed-development'], {
    ...config.env,
    CLOUDLET_ENV: 'development',
    CLOUDLET_DEV_EMAIL: outsiderEmail,
    CLOUDLET_DEV_PASSWORD: outsiderPassword,
  });
  const outsider = await browser.newContext();
  try {
    const outsiderPage = await outsider.newPage();
    await outsiderPage.goto(detailURL);
    await outsiderPage.getByRole('textbox', { name: '邮箱' }).fill(outsiderEmail);
    await outsiderPage.getByLabel('密码', { exact: true }).fill(outsiderPassword);
    await outsiderPage.getByRole('button', { name: '登录', exact: true }).click();
    await expect(outsiderPage.getByText('项目不存在或没有访问权限')).toBeVisible();
    await expect(outsiderPage.getByText(projectName, { exact: true })).toHaveCount(0);
    expect(outsiderPage.url()).toBe(detailURL);
  } finally {
    await outsider.close();
  }
});
