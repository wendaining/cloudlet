import { test, expect } from "@playwright/test";
import { loadConfig } from "../../scripts/development/config.mjs";

const projectName = (suffix) =>
  `站点验收 ${suffix} ${process.pid}-${Date.now()}`;

function crc32(buffer) {
  let crc = 0xffffffff;
  for (const byte of buffer) {
    crc ^= byte;
    for (let bit = 0; bit < 8; bit += 1) {
      crc = (crc >>> 1) ^ (crc & 1 ? 0xedb88320 : 0);
    }
  }
  return (crc ^ 0xffffffff) >>> 0;
}

function buildZip(entries) {
  const localParts = [];
  const centralParts = [];
  let offset = 0;
  for (const entry of entries) {
    const name = Buffer.from(entry.name, "utf8");
    const data = Buffer.from(entry.content, "utf8");
    const sum = crc32(data);

    const local = Buffer.alloc(30 + name.length);
    local.writeUInt32LE(0x04034b50, 0);
    local.writeUInt16LE(20, 4);
    local.writeUInt16LE(0x0800, 6);
    local.writeUInt16LE(0, 8);
    local.writeUInt16LE(0, 10);
    local.writeUInt16LE(0, 12);
    local.writeUInt32LE(sum, 14);
    local.writeUInt32LE(data.length, 18);
    local.writeUInt32LE(data.length, 22);
    local.writeUInt16LE(name.length, 26);
    local.writeUInt16LE(0, 28);
    name.copy(local, 30);
    localParts.push(local, data);

    const central = Buffer.alloc(46 + name.length);
    central.writeUInt32LE(0x02014b50, 0);
    central.writeUInt16LE(20, 4);
    central.writeUInt16LE(20, 6);
    central.writeUInt16LE(0x0800, 8);
    central.writeUInt16LE(0, 10);
    central.writeUInt16LE(0, 12);
    central.writeUInt16LE(0, 14);
    central.writeUInt32LE(sum, 16);
    central.writeUInt32LE(data.length, 20);
    central.writeUInt32LE(data.length, 24);
    central.writeUInt16LE(name.length, 28);
    central.writeUInt16LE(0, 30);
    central.writeUInt16LE(0, 32);
    central.writeUInt16LE(0, 34);
    central.writeUInt16LE(0, 36);
    central.writeUInt32LE(0, 38);
    central.writeUInt32LE(offset, 42);
    name.copy(central, 46);
    centralParts.push(central);

    offset += local.length + data.length;
  }

  const centralDirectory = Buffer.concat(centralParts);
  const end = Buffer.alloc(22);
  end.writeUInt32LE(0x06054b50, 0);
  end.writeUInt16LE(0, 4);
  end.writeUInt16LE(0, 6);
  end.writeUInt16LE(entries.length, 8);
  end.writeUInt16LE(entries.length, 10);
  end.writeUInt32LE(centralDirectory.length, 12);
  end.writeUInt32LE(offset, 16);
  end.writeUInt16LE(0, 20);

  return Buffer.concat([...localParts, centralDirectory, end]);
}

test(
  "控制台展示静态站点活动版本、域名并可打开公开页面",
  { timeout: 120_000 },
  async ({ page }) => {
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
      .fill(projectName("有站点"));
    await page.getByRole("button", { name: "创建项目", exact: true }).click();
    await expect(page.getByText("静态站点", { exact: true })).toBeVisible();

    const projectID = new URL(page.url()).pathname.split("/").at(-1);
    const siteHTML =
      '<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><title>Cloudlet E2E Site</title></head><body><h1>Cloudlet 静态站点验收</h1></body></html>';
    const archive = buildZip([{ name: "index.html", content: siteHTML }]);

    const draftUpload = await page.request.post(
      `/api/v1/projects/${projectID}/sites/draft-site/versions`,
      {
        headers: { "Content-Type": "application/zip" },
        data: archive,
      },
    );
    expect(draftUpload.status()).toBe(201);

    const liveUpload = await page.request.post(
      `/api/v1/projects/${projectID}/sites/live-site/versions`,
      {
        headers: { "Content-Type": "application/zip" },
        data: archive,
      },
    );
    expect(liveUpload.status()).toBe(201);
    const liveVersion = await liveUpload.json();

    const hostname = `e2e-site-${process.pid}-${Date.now()}.localhost`;
    const publishResponse = await page.request.post(
      `/api/v1/projects/${projectID}/sites/live-site/versions/${liveVersion.id}/publish`,
      {
        data: { hostname },
      },
    );
    expect(publishResponse.status()).toBe(200);
    const publication = await publishResponse.json();
    expect(publication.hostname).toBe(hostname);
    expect(publication.version_id).toBe(liveVersion.id);

    await page.reload();
    const siteCard = page.locator(".ant-card").filter({
      has: page.getByText("静态站点", { exact: true }),
    });
    await expect(siteCard.getByRole("table")).toBeVisible();

    const draftRow = siteCard
      .getByRole("row")
      .filter({ hasText: "draft-site" });
    await expect(draftRow).toContainText("未发布");

    const liveRow = siteCard.getByRole("row").filter({ hasText: "live-site" });
    await expect(liveRow).toContainText("已发布");
    await expect(liveRow).toContainText(publication.version_id);
    await expect(liveRow).toContainText(publication.hostname);

    const publicLink = liveRow.getByRole("link", { name: publication.url });
    await expect(publicLink).toHaveAttribute("href", publication.url);
    const [popup] = await Promise.all([
      page.waitForEvent("popup"),
      publicLink.click(),
    ]);
    await popup.waitForLoadState("domcontentloaded");
    expect(popup.url()).toBe(publication.url);
    await expect(
      popup.getByRole("heading", { name: "Cloudlet 静态站点验收" }),
    ).toBeVisible();
    await popup.close();

    await page.getByRole("link", { name: "返回项目列表" }).click();
    await page
      .getByRole("textbox", { name: "项目名称" })
      .fill(projectName("空态"));
    await page.getByRole("button", { name: "创建项目", exact: true }).click();
    await expect(page.getByText("静态站点", { exact: true })).toBeVisible();
    await expect(page.getByText("暂无静态站点")).toBeVisible();

    await page.evaluate(async () => {
      const response = await fetch("/api/v1/auth/logout", { method: "POST" });
      if (!response.ok) throw new Error(`logout returned ${response.status}`);
    });
    await page.reload();
    await expect(
      page.getByRole("button", { name: "登录", exact: true }),
    ).toBeVisible();
    await expect(page.getByText("静态站点", { exact: true })).toHaveCount(0);
    expect(pageErrors).toEqual([]);
  },
);