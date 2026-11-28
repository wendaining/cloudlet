import { execFileSync } from "node:child_process";
import { test, expect } from "@playwright/test";
import { loadConfig } from "../../scripts/development/config.mjs";

const projectName = (suffix) =>
  `操作验收 ${suffix} ${process.pid}-${Date.now()}`;

async function loginAndCreateProject(page, config, suffix) {
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
    .fill(projectName(suffix));
  await page.getByRole("button", { name: "创建项目", exact: true }).click();
  await expect(page.getByText("操作记录", { exact: true })).toBeVisible();
  return new URL(page.url()).pathname.split("/").at(-1);
}

test("项目详情通过真实 API 展示操作状态与空态", async ({ page }) => {
  const config = await loadConfig();
  const pageErrors = [];
  page.on("pageerror", (error) => pageErrors.push(error.message));
  const projectID = await loginAndCreateProject(page, config, "有记录");
  const seeded = JSON.parse(
    execFileSync("go", ["run", "./scripts/e2e-seed-operations", projectID], {
      encoding: "utf8",
      env: config.env,
    }),
  );
  expect(seeded).toHaveLength(3);

  const response = await page.request.get(
    `/api/v1/projects/${projectID}/operations`,
  );
  expect(response.ok()).toBeTruthy();
  const operations = await response.json();
  expect(operations.map((operation) => operation.id).sort()).toEqual(
    seeded.map((operation) => operation.ID).sort(),
  );
  await page.reload();
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
  const failed = operationCard
    .getByRole("row")
    .filter({ hasText: "e2e_deploy_failed" });
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

  await page.evaluate(async () => {
    const response = await fetch("/api/v1/auth/logout", { method: "POST" });
    if (!response.ok) throw new Error(`logout returned ${response.status}`);
  });
  await page.reload();
  await expect(
    page.getByRole("button", { name: "登录", exact: true }),
  ).toBeVisible();
  await expect(page.getByText("操作记录", { exact: true })).toHaveCount(0);
  expect(pageErrors).toEqual([]);
});

test("项目操作列表支持按阶段与结果筛选、翻页、刷新保留与清除", async ({
  page,
}) => {
  const config = await loadConfig();
  const pageErrors = [];
  page.on("pageerror", (error) => pageErrors.push(error.message));
  const projectID = await loginAndCreateProject(page, config, "筛选分页");
  const seeded = JSON.parse(
    execFileSync(
      "go",
      ["run", "./scripts/e2e-seed-operations", projectID, "--paged"],
      {
        encoding: "utf8",
        env: config.env,
      },
    ),
  );
  expect(seeded).toHaveLength(15);

  await page.reload();
  const operationCard = page.locator(".ant-card").filter({
    has: page.getByText("操作记录", { exact: true }),
  });
  const rows = operationCard.locator("tbody tr.ant-table-row");
  const visibleDropdownOption = (text) =>
    page
      .locator(
        ".ant-select-dropdown:not(.ant-select-dropdown-hidden) .ant-select-item-option",
        { hasText: text },
      )
      .first();

  await expect(operationCard.getByText("共 15 条")).toBeVisible();
  await expect(rows).toHaveCount(10);

  // 切换到“进行中”筛选
  await operationCard.getByRole("combobox", { name: "结果筛选" }).click();
  await visibleDropdownOption("进行中").click();
  await expect(operationCard.getByText("共 2 条")).toBeVisible();
  await expect(
    operationCard.getByRole("row").filter({ hasText: "e2e_deploy_in_progress" }),
  ).toBeVisible();
  await expect(
    operationCard.getByRole("row").filter({ hasText: "e2e_saving_in_progress" }),
  ).toBeVisible();

  // 切换到“失败”筛选
  await operationCard.getByRole("combobox", { name: "结果筛选" }).click();
  await visibleDropdownOption("失败").click();
  await expect(operationCard.getByText("共 2 条")).toBeVisible();
  await expect(
    operationCard.getByRole("row").filter({ hasText: "e2e_deploy_failed" }),
  ).toBeVisible();
  await expect(
    operationCard.getByRole("row").filter({ hasText: "e2e_activating_failed" }),
  ).toBeVisible();

  // 切换到“成功”筛选，翻页后刷新页面验证条件和页码保留
  await operationCard.getByRole("combobox", { name: "结果筛选" }).click();
  await visibleDropdownOption("成功").click();
  await expect(operationCard.getByText("共 11 条")).toBeVisible();
  await operationCard
    .locator(".ant-pagination-item")
    .filter({ hasText: /^2$/ })
    .click();
  await expect(rows).toHaveCount(1);
  await expect(
    operationCard.getByRole("row").filter({ hasText: "e2e_paged_succeeded_10" }),
  ).toBeVisible();
  expect(new URL(page.url()).searchParams.get("outcome")).toBe("succeeded");
  expect(new URL(page.url()).searchParams.get("page")).toBe("2");

  await page.reload();
  await expect(operationCard.getByText("共 11 条")).toBeVisible();
  await expect(rows).toHaveCount(1);
  await expect(
    operationCard.getByRole("row").filter({ hasText: "e2e_paged_succeeded_10" }),
  ).toBeVisible();
  expect(new URL(page.url()).searchParams.get("outcome")).toBe("succeeded");
  expect(new URL(page.url()).searchParams.get("page")).toBe("2");

  // 叠加阶段筛选为“已接收”，验证无匹配空态与保留筛选栏
  await operationCard.getByRole("combobox", { name: "阶段筛选" }).click();
  await visibleDropdownOption("已接收").click();
  await expect(operationCard.getByText("没有符合条件的操作记录")).toBeVisible();
  expect(new URL(page.url()).searchParams.get("phase")).toBe("received");
  expect(new URL(page.url()).searchParams.get("outcome")).toBe("succeeded");

  // 清除筛选后恢复全部 15 条记录
  await operationCard.getByRole("button", { name: "清除筛选" }).click();
  await expect(operationCard.getByText("共 15 条")).toBeVisible();
  await expect(rows).toHaveCount(10);
  expect(new URL(page.url()).search).toBe("");
  expect(pageErrors).toEqual([]);
});
