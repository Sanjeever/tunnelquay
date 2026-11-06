import { readFile, writeFile } from "node:fs/promises";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";
import { resolve } from "node:path";
const require = createRequire(new URL("../web/package.json", import.meta.url));
const semver = require("semver");
const root = fileURLToPath(new URL("../", import.meta.url));
const packagePath = resolve(root, "web/package.json");
const pkg = JSON.parse(await readFile(packagePath, "utf8"));
const [command = "check", requested] = process.argv.slice(2);
try {
  let version = pkg.version;
  if (command === "bump")
    version = semver.inc(version, requested) || semver.valid(requested);
  else if (command !== "check" && command !== "print")
    throw new Error("命令须为 check、print 或 bump");
  if (!version || semver.valid(version) !== version)
    throw new Error("程序版本不符合 SemVer");
  if (command !== "print") {
    const log = await readFile(
      resolve(root, `docs/releases/${version}.md`),
      "utf8",
    );
    if (log.trim().length < 30 || !/[\u3400-\u9fff]/u.test(log))
      throw new Error("发布前须编写非空中文版本日志");
    if (command === "check" && requested && requested !== `v${version}`)
      throw new Error("标签与程序版本不一致");
  }
  if (command === "bump") {
    pkg.version = version;
    await writeFile(packagePath, `${JSON.stringify(pkg, null, 2)}\n`);
  }
  console.log(version);
} catch (err) {
  console.error(`版本检查失败：${err.message}`);
  process.exitCode = 1;
}
