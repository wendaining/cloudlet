import { expect, test } from "@playwright/test";
import { loadConfig } from "../../scripts/development/config.mjs";

test("在项目控制台创建真实浏览器会话并导航", async ({ page }) => {
  test.setTimeout(120_000);
  const config = await loadConfig();
  const fixture = process.env.CLOUDLET_TEST_BROWSER_URL;
  if (!fixture) throw new Error("缺少浏览器导航测试页面地址");

  await page.goto("/");
  await page
    .getByRole("textbox", { name: "邮箱" })
    .fill(config.env.CLOUDLET_DEV_EMAIL);
  await page
    .getByLabel("密码", { exact: true })
    .fill(config.env.CLOUDLET_DEV_PASSWORD);
  await page.getByRole("button", { name: "登录", exact: true }).click();
  await page.getByRole("button", { name: "进入项目控制台" }).click();
  await page
    .getByRole("textbox", { name: "项目名称" })
    .fill("浏览器验收 " + test.info().project.name);
  await page.getByRole("button", { name: "创建项目", exact: true }).click();

  const sessionResponse = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      response.url().endsWith("/browser-sessions"),
    { timeout: 50_000 },
  );
  await page
    .getByRole("button", { name: "创建浏览器会话", exact: true })
    .click();
  const created = await sessionResponse;
  expect(created.status()).toBe(201);
  const session = await created.json();

  try {
    const input = page.getByRole("textbox", { name: "导航地址" });
    const navigate = page.getByRole("button", { name: "导航", exact: true });
    await input.fill("file:///文档");
    await navigate.click();
    await expect(
      page.getByText("请输入不带凭据的 HTTP 或 HTTPS 地址", { exact: true }),
    ).toBeVisible();

    await input.fill(fixture + "/redirect");
    await navigate.click();
    await expect(
      page.getByText("Cloudlet 浏览器验收", { exact: true }),
    ).toBeVisible();
    await expect(
      page.getByText(fixture + "/page", { exact: true }),
    ).toBeVisible();

    await page.reload();
    await expect(navigate).toBeVisible();
    await expect(
      page.getByRole("button", { name: "创建浏览器会话", exact: true }),
    ).toHaveCount(0);
    await input.fill(fixture + "/redirect");
    await navigate.click();
    await expect(
      page.getByText("Cloudlet 浏览器验收", { exact: true }),
    ).toBeVisible();
    await expect(
      page.getByText(fixture + "/page", { exact: true }),
    ).toBeVisible();

    await input.fill(fixture + "/slow");
    await navigate.click();
    await expect(
      page.getByRole("alert").filter({ hasText: "浏览器导航超时" }),
    ).toBeVisible({ timeout: 20_000 });
  } finally {
    const closed = await page.request.delete(session.endpoint);
    expect(closed.ok()).toBeTruthy();
  }
});
