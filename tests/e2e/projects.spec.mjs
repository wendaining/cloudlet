import { test, expect } from "@playwright/test";
import { loadConfig } from "../../scripts/development/config.mjs";
import { capture } from "../../scripts/development/process.mjs";

const projectName = `E2E 项目 ${process.pid}-${Date.now()}`;
const renamedProjectName = `${projectName} 已重命名`;
const memberEmail = `member-${process.pid}-${Date.now()}@cloudlet.local`;
const memberPassword = "member-integration-password";

test("所有者管理项目成员且非所有者只读", async ({ page, browser }, info) => {
  test.setTimeout(120_000);
  const config = await loadConfig();
  const pageErrors = [];
  page.on("pageerror", (error) => pageErrors.push(error.message));

  await page.goto("/");
  await page
    .getByRole("textbox", { name: "邮箱" })
    .fill(config.env.CLOUDLET_DEV_EMAIL);
  await page
    .getByLabel("密码", { exact: true })
    .fill(config.env.CLOUDLET_DEV_PASSWORD);
  await page.getByRole("button", { name: "登录", exact: true }).click();
  await expect(page.getByRole("heading", { name: "环境概览" })).toBeVisible();
  await page.getByRole("button", { name: "进入项目控制台" }).click();
  await expect(page.getByRole("heading", { name: "项目" })).toBeVisible();
  await expect(page.getByRole("textbox", { name: "项目名称" })).toBeVisible();

  await page.getByRole("textbox", { name: "项目名称" }).fill(projectName);
  await page.getByRole("button", { name: "创建项目", exact: true }).click();
  await expect(page.getByText("项目标识")).toBeVisible();

  const detailURL = page.url();
  const projectID = detailURL.split("/projects/")[1];
  await page.reload();
  await expect(
    page.getByText(projectName, { exact: true }).first(),
  ).toBeVisible();
  expect(projectID).toMatch(/^[0-9a-f-]{36}$/i);

  await capture("go", ["run", "./cmd/admin", "seed-development"], {
    ...config.env,
    CLOUDLET_ENV: "development",
    CLOUDLET_DEV_EMAIL: memberEmail,
    CLOUDLET_DEV_PASSWORD: memberPassword,
  });
  await page.getByRole("textbox", { name: "成员邮箱" }).fill(memberEmail);
  await page.getByRole("button", { name: "保存成员" }).click();
  await expect(page.getByRole("status")).toContainText("的角色已保存");
  await expect(
    page.getByRole("row", { name: new RegExp(memberEmail) }),
  ).toContainText("开发者");
  const unregisteredEmail = `missing-${process.pid}-${Date.now()}@cloudlet.local`;
  await page.getByRole("textbox", { name: "成员邮箱" }).fill(unregisteredEmail);
  await page.getByRole("button", { name: "保存成员" }).click();
  await expect(page.getByRole("alert")).toBeVisible();
  await expect(page.getByRole("status")).toHaveCount(0);
  await page.reload();
  await expect(
    page.getByRole("row", { name: new RegExp(memberEmail) }),
  ).toContainText("开发者");

  await page.getByRole("textbox", { name: "成员邮箱" }).fill(memberEmail);
  const role = page.getByRole("combobox", { name: "成员角色" });
  await role.click();
  await role.press("ArrowDown");
  await role.press("Enter");
  await page.getByRole("button", { name: "保存成员" }).click();
  await expect(
    page.getByRole("row", { name: new RegExp(memberEmail) }),
  ).toContainText("观察者");
  await page.reload();
  await expect(
    page.getByRole("row", { name: new RegExp(memberEmail) }),
  ).toContainText("观察者");

  await expect(
    page.getByText("当前权限：所有者，可添加、调整或移除非所有者成员。"),
  ).toBeVisible();
  await expect(
    page.getByRole("row", { name: new RegExp(config.env.CLOUDLET_DEV_EMAIL) }),
  ).toContainText("不可移除");

  const memberContext = await browser.newContext();
  const memberPage = await memberContext.newPage();
  await memberPage.goto("/");
  await memberPage.getByRole("textbox", { name: "邮箱" }).fill(memberEmail);
  await memberPage.getByLabel("密码", { exact: true }).fill(memberPassword);
  await memberPage.getByRole("button", { name: "登录", exact: true }).click();
  await expect(
    memberPage.getByRole("heading", { name: "环境概览" }),
  ).toBeVisible();
  await memberPage.goto(`/projects/${projectID}`);
  await expect(
    memberPage.getByRole("row", { name: new RegExp(memberEmail) }),
  ).toContainText("观察者");
  await expect(
    memberPage.getByText(
      "当前权限：观察者（只读），仅项目所有者可以管理项目成员。",
    ),
  ).toBeVisible();
  await expect(
    memberPage.getByRole("button", { name: "保存成员" }),
  ).toHaveCount(0);
  await expect(
    memberPage.getByRole("button", { name: `移除 ${memberEmail}` }),
  ).toHaveCount(0);
  await expect(
    memberPage.getByRole("button", { name: "编辑名称" }),
  ).toHaveCount(0);
  await memberContext.close();

  await page.getByRole("button", { name: "编辑名称" }).click();
  await page.getByRole("textbox", { name: "项目名称" }).fill("   ");
  await page.getByRole("button", { name: "保存名称" }).click();
  await expect(page.getByText("项目名称不能是空白")).toBeVisible();
  const supplementaryProjectName = "𠮷".repeat(100);
  await page
    .getByRole("textbox", { name: "项目名称" })
    .fill(supplementaryProjectName);
  await expect(page.getByText("100 / 100")).toBeVisible();
  await page.getByRole("button", { name: "保存名称" }).click();
  await expect(page.getByText("项目名称已更新")).toBeVisible();
  await expect(
    page.getByText(supplementaryProjectName, { exact: true }).first(),
  ).toBeVisible();
  await page.reload();
  await expect(
    page.getByText(supplementaryProjectName, { exact: true }).first(),
  ).toBeVisible();

  await page.getByRole("button", { name: "编辑名称" }).click();
  await page
    .getByRole("textbox", { name: "项目名称" })
    .fill(renamedProjectName);
  await page.getByRole("button", { name: "保存名称" }).click();
  await expect(page.getByText("项目名称已更新")).toBeVisible();
  await expect(
    page.getByText(renamedProjectName, { exact: true }).first(),
  ).toBeVisible();
  await page.reload();
  await expect(
    page.getByText(renamedProjectName, { exact: true }).first(),
  ).toBeVisible();

  // 二次确认取消后成员仍在，刷新后仍在
  await page.getByRole("button", { name: `移除 ${memberEmail}` }).click();
  const confirmDialog = page.getByRole("dialog", { name: "确认移除成员" });
  await expect(confirmDialog).toBeVisible();
  await confirmDialog.getByRole("button", { name: "取消" }).click();
  await expect(confirmDialog).toHaveCount(0);
  await expect(
    page.getByRole("row", { name: new RegExp(memberEmail) }),
  ).toBeVisible();
  await page.getByRole("button", { name: "刷新成员" }).click();
  await expect(
    page.getByRole("row", { name: new RegExp(memberEmail) }),
  ).toBeVisible();

  // 确认移除并刷新页面验证持久化
  await page.getByRole("button", { name: `移除 ${memberEmail}` }).click();
  await expect(confirmDialog).toBeVisible();
  await confirmDialog.getByRole("button", { name: "确认移除" }).click();
  await expect(page.getByRole("status")).toContainText(`${memberEmail} 已移除`);
  await expect(
    page.getByRole("row", { name: new RegExp(memberEmail) }),
  ).toHaveCount(0);
  await page.reload();
  await expect(
    page.getByRole("row", { name: new RegExp(memberEmail) }),
  ).toHaveCount(0);

  // 重新添加后触发真实后端移除失败（404），核对错误反馈与刷新同步
  await page.getByRole("textbox", { name: "成员邮箱" }).fill(memberEmail);
  await page.getByRole("button", { name: "保存成员" }).click();
  await expect(
    page.getByRole("row", { name: new RegExp(memberEmail) }),
  ).toBeVisible();
  const membersResponse = await page.request.get(
    `/api/v1/projects/${projectID}/members`,
  );
  expect(membersResponse.ok()).toBe(true);
  const currentMembers = await membersResponse.json();
  const targetMember = currentMembers.find(
    (item) => item.email === memberEmail,
  );
  expect(targetMember).toBeTruthy();

  await page.getByRole("button", { name: `移除 ${memberEmail}` }).click();
  await expect(confirmDialog).toBeVisible();
  const deleteResponse = await page.request.delete(
    `/api/v1/projects/${projectID}/members/${targetMember.user_id}`,
  );
  expect(deleteResponse.status()).toBe(204);

  await confirmDialog.getByRole("button", { name: "确认移除" }).click();
  await expect(page.getByRole("alert")).toContainText("项目成员不存在");
  await expect(page.getByRole("status")).toHaveCount(0);
  await page.getByRole("button", { name: "刷新成员" }).click();
  await expect(
    page.getByRole("row", { name: new RegExp(memberEmail) }),
  ).toHaveCount(0);

  await page.getByRole("link", { name: "返回项目列表" }).click();
  await expect(
    page.getByRole("link", { name: renamedProjectName }),
  ).toBeVisible();
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "环境概览" })).toBeVisible();
  await page.getByRole("button", { name: "退出登录" }).click();
  await expect(page.getByRole("button", { name: "登录" })).toBeVisible();

  // 使用被移除账号登录并访问项目
  await page.getByRole("textbox", { name: "邮箱" }).fill(memberEmail);
  await page.getByLabel("密码", { exact: true }).fill(memberPassword);
  await page.getByRole("button", { name: "登录", exact: true }).click();
  await expect(page.getByRole("heading", { name: "环境概览" })).toBeVisible();
  await page.getByRole("button", { name: "进入项目控制台" }).click();
  await expect(page.getByRole("heading", { name: "项目" })).toBeVisible();
  await expect(
    page.getByRole("link", { name: renamedProjectName }),
  ).toHaveCount(0);
  await page.goto(`/projects/${projectID}`);
  await expect(page.getByText("项目不存在或没有访问权限")).toBeVisible();

  await page.screenshot({
    path: info.outputPath("projects.png"),
    fullPage: true,
  });
  expect(pageErrors).toEqual([]);
});
