import { test, expect } from "@playwright/test";
import { spawn, type ChildProcess } from "node:child_process";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { resolve, join } from "node:path";
import { createServer, connect, type Server } from "node:net";
import { createSocket, type Socket } from "node:dgram";
import { DatabaseSync } from "node:sqlite";

let dir: string, address: string, password: string, credential: string;
const suffix = process.platform === "win32" ? ".exe" : "";
let server: ChildProcess,
  client: ChildProcess,
  target: Server,
  udpTarget: Socket,
  targetPort: number;

async function launchServer() {
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
  await expect
    .poll(
      () =>
        output
          .replace(/临时管理密码：\s+\S+/g, "临时管理密码：***")
          .replace(/客户端接入凭据：\s+\S+/g, "客户端接入凭据：***"),
      { timeout: 15000 },
    )
    .toMatch(/监听=127\.0\.0\.1:\d+/);
  address = output.match(/监听=(127\.0\.0\.1:\d+)/)![1];
  const temporary = output.match(/临时管理密码：\s+(\S+)/);
  if (temporary) password = temporary[1];
  credential = output.match(/客户端接入凭据：\s+(\S+)/)![1];
}

async function launchClient() {
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
  let output = "";
  const collect = (b: Buffer) => {
    output += b.toString();
  };
  client.stdout!.on("data", collect);
  client.stderr!.on("data", collect);
  await expect.poll(() => output, { timeout: 15000 }).toContain("设备已接入");
}

async function stopChild(child: ChildProcess) {
  if (child.exitCode !== null) return;
  const exited = new Promise<void>((resolve) =>
    child.once("exit", () => resolve()),
  );
  child.kill();
  await exited;
}

test.beforeAll(async () => {
  dir = await mkdtemp(join(tmpdir(), "tunnelquay-browser-"));
  await launchServer();
  await launchClient();
  target = createServer((socket) => socket.pipe(socket));
  await new Promise<void>((resolve) => target.listen(0, "127.0.0.1", resolve));
  targetPort = (target.address() as { port: number }).port;
  udpTarget = createSocket("udp4");
  udpTarget.on("message", (message, source) => {
    udpTarget.send(message, source.port, source.address);
  });
  await new Promise<void>((resolve) =>
    udpTarget.bind(targetPort, "127.0.0.1", resolve),
  );
});

test.afterAll(async () => {
  client?.kill();
  server?.kill();
  target?.close();
  udpTarget?.close();
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
  await page.getByRole("button", { name: "编辑 浏览器验收隧道" }).click();
  await expect(page.getByLabel("QUIC UDP 承载", { exact: true })).toHaveCount(
    0,
  );
  await page.getByLabel("协议", { exact: true }).selectOption("both");
  await expect(page.getByLabel("QUIC UDP 承载", { exact: true })).toHaveValue(
    "datagram",
  );
  await page
    .getByLabel("QUIC UDP 承载", { exact: true })
    .selectOption("stream");
  await page.screenshot({
    path: "test-results/udp-stream-form.png",
    fullPage: true,
  });
  await page.getByRole("button", { name: "保存", exact: true }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect
    .poll(async () => {
      const response = await page.request.get(`http://${address}/api/tunnels`);
      return (await response.json())[0].applied.udp_mode;
    })
    .toBe("stream");
  await Promise.all(
    [1, 2, 3].map(
      (source) =>
        new Promise<void>((resolve, reject) => {
          const socket = createSocket("udp4");
          const payload = Buffer.alloc(8192, source);
          const timeout = setTimeout(() => {
            socket.close();
            reject(new Error("可靠 UDP 回包超时"));
          }, 5000);
          socket.once("message", (reply) => {
            clearTimeout(timeout);
            socket.close();
            if (reply.equals(payload)) resolve();
            else reject(new Error("可靠 UDP 来源或内容不符"));
          });
          socket.once("error", (error) => {
            clearTimeout(timeout);
            socket.close();
            reject(error);
          });
          socket.send(payload, port, "127.0.0.1");
        }),
    ),
  );
  await page
    .getByRole("button", { name: "浏览器验收隧道", exact: true })
    .click();
  await expect(page.getByRole("dialog").getByText(/可靠流/)).toHaveCount(2);
  await page.getByRole("button", { name: "关闭", exact: true }).click();
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

  await page.goto("about:blank");
  await stopChild(client);
  await stopChild(server);
  const database = new DatabaseSync(join(dir, "server", "tunnelquay.db"));
  const existing = JSON.parse(
    String(
      database.prepare("SELECT payload FROM tunnels LIMIT 1").get()!.payload,
    ),
  );
  const legacyID = "f".repeat(48),
    tunnelID = "e".repeat(48);
  const legacyPort = existing.desired.public_port + 1;
  database.prepare("INSERT INTO devices(id,payload) VALUES(?,?)").run(
    legacyID,
    JSON.stringify({
      id: legacyID,
      name: "旧身份设备",
      hostname: "legacy-host",
      os: "linux",
      arch: "amd64",
      version: "0.1.0",
      last_seen: new Date().toISOString(),
      online: false,
      revoked: false,
    }),
  );
  database.prepare("INSERT INTO tunnels(id,payload) VALUES(?,?)").run(
    tunnelID,
    JSON.stringify({
      desired: {
        ...existing.desired,
        id: tunnelID,
        device_id: legacyID,
        name: "待迁移隧道",
        public_port: legacyPort,
        revision: 1,
        enabled: true,
      },
      applied: null,
      status: "offline",
      target_status: "unknown",
    }),
  );
  database.close();
  await launchServer();
  await launchClient();
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto(`http://${address}`);
  await page
    .getByLabel("管理密码", { exact: true })
    .fill("browser-test-password-123");
  await page.getByRole("button", { name: "登录", exact: true }).click();
  await page.getByRole("link", { name: "设备", exact: true }).click();
  await expect(page.getByText("待迁移", { exact: true })).toBeVisible();
  await expect(page.getByText("在线", { exact: true })).toBeVisible();
  await page
    .getByRole("button", { name: "迁移 旧身份设备 的隧道", exact: true })
    .click();
  await expect(
    page.getByRole("textbox", { name: "原设备身份", exact: true }),
  ).toHaveValue(legacyID);
  const deviceResponse = await page.request.get(
    `http://${address}/api/devices`,
  );
  const authenticated = (await deviceResponse.json()).find(
    (d: { public_key?: string }) => !!d.public_key,
  );
  await page
    .getByLabel("新设备", { exact: true })
    .selectOption(authenticated.id);
  await expect(
    page.getByRole("textbox", { name: "新设备身份", exact: true }),
  ).toHaveValue(authenticated.id);
  await page.screenshot({
    path: "test-results/identity-migration-desktop.png",
    fullPage: true,
  });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.screenshot({
    path: "test-results/identity-migration-mobile.png",
    fullPage: true,
  });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  await page
    .getByRole("button", { name: "迁移并撤销原身份", exact: true })
    .click();
  await expect(
    page.getByRole("button", { name: "待迁移隧道", exact: true }),
  ).toBeVisible();
  await expect
    .poll(async () => {
      const response = await page.request.get(`http://${address}/api/tunnels`);
      const moved = (await response.json()).find(
        (t: { desired: { id: string } }) => t.desired.id === tunnelID,
      );
      return moved.applied?.device_id;
    })
    .toBe(authenticated.id);
  await new Promise<void>((resolve, reject) => {
    const socket = createSocket("udp4"),
      payload = Buffer.from("迁移后的可靠 UDP");
    const timeout = setTimeout(() => {
      socket.close();
      reject(new Error("迁移后转发超时"));
    }, 3000);
    socket.once("message", (reply) => {
      clearTimeout(timeout);
      socket.close();
      if (reply.equals(payload)) resolve();
      else reject(new Error("迁移后内容错误"));
    });
    socket.once("error", (error) => {
      clearTimeout(timeout);
      socket.close();
      reject(error);
    });
    socket.send(payload, legacyPort, "127.0.0.1");
  });
  await page.getByRole("link", { name: "设备", exact: true }).click();
  page.once("dialog", (dialog) => dialog.accept());
  await page
    .getByRole("button", { name: "撤销 本地 QUIC 设备 的身份", exact: true })
    .click();
  await expect(page.getByText("已撤销", { exact: true })).toHaveCount(2);
  await expect.poll(() => client.exitCode).toBe(1);
  await page.screenshot({
    path: "test-results/identity-revoked-mobile.png",
    fullPage: true,
  });
  expect(errors).toEqual([]);
});
