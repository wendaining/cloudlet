import { execFileSync } from "node:child_process";
import { test, expect } from "@playwright/test";
import { loadConfig } from "../../scripts/development/config.mjs";

const projectName = (suffix) =>
  `操作验收 ${suffix} ${process.pid}-${Date.now()}`;

test("项目详情通过真实 API 展示操作状态与空态", async ({ page }) => {
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

  await page
    .getByRole("textbox", { name: "项目名称" })
    .fill(projectName("有记录"));
  await page.getByRole("button", { name: "创建项目", exact: true }).click();
  await expect(page.getByText("操作记录", { exact: true })).toBeVisible();
  const projectID = new URL(page.url()).pathname.split("/").at(-1);
  const seeded = JSON.parse(
    execFileSync("go", ["run", "./scripts/e2e-seed-operations", projectID], {
      encoding: "utf8",
      env: config.env,
    }),
  );
  expect(seeded).toHaveLength(3);

  const responsePromise = page.waitForResponse(
    (response) =>
      response.url().includes(`/api/v1/projects/${projectID}/operations`) &&
      response.status() === 200,
  );
  await page.reload();
  const response = await responsePromise;
  const operations = await response.json();
  expect(operations.map((operation) => operation.id).sort()).toEqual(
    seeded.map((operation) => operation.ID).sort(),
  );
  const operationCard = page.locator(".ant-card").filter({
    has: page.getByText("操作记录", { exact: true }),
  });
  await expect(operationCard.getByRole("table")).toBeVisible();
  const inProgress = operationCard
    .getByRole("row")
    .filter({ hasText: "e2e_deploy_in_progress" });
  await expect(inProgress).toContainText("已接收");
  await expect(inProgress).toContainText("进行中");
  const succeeded = operationCard
    .getByRole("row")
    .filter({ hasText: "e2e_deploy_succeeded" });
  await expect(succeeded).toContainText("准备中");
  await expect(succeeded).toContainText("成功");
  const failed = page.getByRole("row").filter({ hasText: "e2e_deploy_failed" });
  await expect(failed).toContainText("已接收");
  await expect(failed).toContainText("失败");
  await expect(failed.locator("code")).toHaveText(
    seeded.find((record) => record.Action === "e2e_deploy_failed").ID,
  );
  const createdAt = new Intl.DateTimeFormat("zh-CN", {
    dateStyle: "medium",
    timeStyle: "medium",
  }).format(
    new Date(
      seeded.find((record) => record.Action === "e2e_deploy_failed").CreatedAt,
    ),
  );
  const updatedAt = new Intl.DateTimeFormat("zh-CN", {
    dateStyle: "medium",
    timeStyle: "medium",
  }).format(
    new Date(
      seeded.find((record) => record.Action === "e2e_deploy_failed").UpdatedAt,
    ),
  );
  await expect(failed).toContainText(createdAt);
  await expect(failed).toContainText(updatedAt);
  await failed.getByRole("button", { name: "查看详情" }).click();
  const detail = page.getByRole("dialog");
  await expect(
    detail.getByText(
      seeded.find((record) => record.Action === "e2e_deploy_failed").ID,
      { exact: true },
    ),
  ).toBeVisible();
  await expect(detail).toContainText("已接收");
  await expect(detail).toContainText("失败");
  await expect(detail).toContainText(createdAt);
  await expect(detail).toContainText(updatedAt);
  await detail.getByRole("button", { name: "返回操作列表" }).click();
  await expect(detail).toBeHidden();

  await page.getByRole("link", { name: "返回项目列表" }).click();
  await page
    .getByRole("textbox", { name: "项目名称" })
    .fill(projectName("空态"));
  await page.getByRole("button", { name: "创建项目", exact: true }).click();
  await expect(page.getByText("操作记录", { exact: true })).toBeVisible();
  await expect(page.getByText("暂无操作记录")).toBeVisible();
  expect(pageErrors).toEqual([]);
});
