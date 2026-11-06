import { test, expect } from "@playwright/test";
import { spawn, type ChildProcess } from "node:child_process";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { resolve, join } from "node:path";
import { createServer, connect, type Server } from "node:net";

let dir: string, address: string, password: string;
let server: ChildProcess,
  client: ChildProcess,
  target: Server,
  targetPort: number;

test.beforeAll(async () => {
  dir = await mkdtemp(join(tmpdir(), "tunnelquay-browser-"));
  const suffix = process.platform === "win32" ? ".exe" : "";
  server = spawn(resolve("..", "bin", `tunnelquay-server${suffix}`), [
    "-listen",
    "127.0.0.1:0",
    "-data-dir",
    join(dir, "server"),
  ]);
  let output = "";
  const collect = (b: Buffer) => {
    output += b.toString();
  };
  server.stdout!.on("data", collect);
  server.stderr!.on("data", collect);
  await expect.poll(() => /监听=127\.0\.0\.1:\d+/.test(output)).toBe(true);
  address = output.match(/监听=(127\.0\.0\.1:\d+)/)![1];
  password = output.match(/临时管理密码：\s+(\S+)/)![1];
  const credential = output.match(/客户端接入凭据：\s+(\S+)/)![1];
  client = spawn(resolve("..", "bin", `tunnelquay-client${suffix}`), [
    "-address",
    "127.0.0.1",
    "-port",
    address.split(":")[1],
    "-credential",
    credential,
    "-transport",
    "quic",
    "-data-dir",
    join(dir, "client"),
  ]);
  target = createServer((socket) => socket.pipe(socket));
  await new Promise<void>((resolve) => target.listen(0, "127.0.0.1", resolve));
  targetPort = (target.address() as { port: number }).port;
});

test.afterAll(async () => {
  client?.kill();
  server?.kill();
  target?.close();
  await Promise.all(
    [client, server].filter(Boolean).map(
      (child) =>
        new Promise<void>((resolve) => {
          if (child.exitCode !== null) resolve();
          else child.once("exit", () => resolve());
        }),
    ),
  );
  if (dir) await rm(dir, { recursive: true, force: true });
});

test("真实设备、隧道管理、连接筛选及响应式布局", async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", (err) => errors.push(err.message));
  await page.goto(`http://${address}`);
  await page.getByLabel("管理密码", { exact: true }).fill(password);
  await page.getByRole("button", { name: "登录", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "修改临时密码" }),
  ).toBeVisible();
  await page.getByLabel("临时密码", { exact: true }).fill(password);
  await page
    .getByLabel("新密码", { exact: true })
    .fill("browser-test-password-123");
  await page.getByRole("button", { name: "保存密码" }).click();
  await page.getByRole("link", { name: "设置", exact: true }).click();
  await page.getByLabel("客户端接入地址").fill(address);
  await page.getByRole("button", { name: "保存设置" }).click();
  await expect(page.getByRole("button", { name: "已保存" })).toBeVisible();
  await page.getByRole("link", { name: "设备", exact: true }).click();
  await expect(page.getByText("在线", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "客户端升级", exact: true }).click();
  await expect(page.getByText("当前构建未配置正式发行验证公钥")).toBeVisible();
  await expect(
    page.getByRole("button", { name: "发起升级", exact: true }),
  ).toBeDisabled();
  await page.getByText("发行包", { exact: true }).click();
  await expect(page.getByLabel("发行清单", { exact: true })).toBeVisible();
  await page.screenshot({
    path: "test-results/upgrade-desktop.png",
    fullPage: true,
  });
  await page.getByRole("button", { name: "关闭", exact: true }).click();
  await page.getByRole("button", { name: /重命名/ }).click();
  await page.getByLabel("设备名称").fill("本地 QUIC 设备");
  await page.getByRole("button", { name: "保存", exact: true }).click();
  await expect(page.getByText("本地 QUIC 设备", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "接入设备", exact: true }).click();
  await expect(page.getByLabel("客户端命令")).toHaveValue(
    /tunnelquay-client -address 127.0.0.1/,
  );
  await page.getByRole("button", { name: "关闭", exact: true }).click();
  await page.getByRole("link", { name: "隧道", exact: true }).click();
  await page.getByRole("button", { name: "新建隧道", exact: true }).click();
  await page.getByLabel("名称", { exact: true }).fill("浏览器验收隧道");
  await page.getByLabel("目标端口", { exact: true }).fill(String(targetPort));
  await page.getByRole("button", { name: "保存", exact: true }).click();
  await expect(page.getByText("监听就绪", { exact: true })).toBeVisible();
  const tunnels = await page.request.get(`http://${address}/api/tunnels`);
  const data = await tunnels.json();
  const port = data[0].desired.public_port;
  await new Promise<void>((resolve, reject) => {
    const socket = connect(port, "127.0.0.1", () =>
      socket.write("browser-data"),
    );
    socket.setTimeout(3000, () => {
      socket.destroy();
      reject(new Error("转发超时"));
    });
    socket.on("data", (b) => {
      if (b.toString() === "browser-data") {
        socket.end();
        resolve();
      }
    });
    socket.on("error", reject);
  });
  await page.screenshot({
    path: "test-results/tunnels-desktop.png",
    fullPage: true,
  });
  await page.getByRole("button", { name: "编辑 浏览器验收隧道" }).click();
  await page.getByLabel("名称", { exact: true }).fill("已编辑隧道");
  await page.getByRole("button", { name: "保存", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "已编辑隧道", exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "已编辑隧道", exact: true }).click();
  await expect(page.getByText("可达", { exact: true })).toBeVisible();
  await page.getByRole("link", { name: "查看连接" }).click();
  await page.getByRole("tab", { name: "历史记录" }).click();
  await expect(page.locator("tbody tr")).toHaveCount(1);
  await page.getByLabel("来源 IP", { exact: true }).fill("192.0.2.254");
  await page.getByRole("button", { name: "筛选", exact: true }).click();
  await expect(page.getByText("暂无连接记录")).toBeVisible();
  await page.getByRole("button", { name: "重置", exact: true }).click();
  await expect(page.locator("tbody tr")).toHaveCount(1);
  await page.getByRole("link", { name: "隧道", exact: true }).click();
  await page.getByRole("switch", { name: "启用 已编辑隧道" }).uncheck();
  await expect(page.getByText("已停用", { exact: true })).toBeVisible();
  await page.getByRole("switch", { name: "启用 已编辑隧道" }).check();
  await expect(page.getByText("监听就绪", { exact: true })).toBeVisible();
  await page.setViewportSize({ width: 390, height: 844 });
  await page.screenshot({
    path: "test-results/tunnels-mobile.png",
    fullPage: true,
  });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
  ).toBe(true);
  await page.getByRole("button", { name: "编辑 已编辑隧道" }).click();
  await expect(page.getByRole("dialog")).toBeVisible();
  await page.screenshot({
    path: "test-results/form-mobile.png",
    fullPage: true,
  });
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog")).not.toBeVisible();
  expect(errors).toEqual([]);
});
