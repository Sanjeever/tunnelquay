import { readFile, readdir } from "node:fs/promises";
import { createHash, createPublicKey, verify } from "node:crypto";
import { createRequire } from "node:module";
import { execFileSync } from "node:child_process";
import { resolve } from "node:path";
const require = createRequire(new URL("../web/package.json", import.meta.url));
const semver = require("semver");
const [mode = "--check", directory = "bin/releases"] = process.argv.slice(2);
try {
  if (!["--preflight", "--check", "--check-test", "--publish"].includes(mode))
    throw new Error("命令须为 --preflight、--check、--check-test 或 --publish");
  const pkg = JSON.parse(await readFile("web/package.json", "utf8"));
  const tag = `v${pkg.version}`;
  const notes = await readFile(`docs/releases/${pkg.version}.md`, "utf8");
  if (mode === "--preflight") {
    if (
      process.env.GITHUB_REF_NAME !== tag ||
      process.env.GITHUB_REF_TYPE !== "tag"
    )
      throw new Error("发布预检必须来自对应版本标签");
    execFileSync("git", ["merge-base", "--is-ancestor", "HEAD", "origin/main"]);
    if (!process.env.GH_TOKEN || !process.env.GITHUB_REPOSITORY)
      throw new Error("缺少 GitHub 发布凭据");
    const result = await fetch(
      `https://api.github.com/repos/${process.env.GITHUB_REPOSITORY}/releases/tags/${tag}`,
      {
        headers: {
          Authorization: `Bearer ${process.env.GH_TOKEN}`,
          Accept: "application/vnd.github+json",
        },
        signal: AbortSignal.timeout(30000),
      },
    );
    if (result.ok) {
      const existing = await result.json();
      if (!existing.draft) throw new Error("同版本已经公开，不覆盖已发布版本");
    } else if (result.status !== 404)
      throw new Error(`查询发行失败 ${result.status}`);
    console.log("标签、主分支归属和未公开状态检查通过");
    process.exit(0);
  }
  const root = resolve(directory);
  const manifestBytes = await readFile(resolve(root, "release-manifest.json"));
  const signature = Buffer.from(
    (await readFile(resolve(root, "release-manifest.sig"), "utf8")).trim(),
    "base64",
  );
  const testOnly = mode === "--check-test";
  const keyText = testOnly
    ? await readFile(resolve(root, "TEST-ONLY-public-key.txt"), "utf8")
    : process.env.TUNNELQUAY_PUBLIC_KEY || "";
  const rawKey = Buffer.from(keyText.trim(), "base64");
  if (rawKey.length !== 32) throw new Error("缺少有效的发行验证公钥");
  const publicKey = createPublicKey({
    key: Buffer.concat([
      Buffer.from("302a300506032b6570032100", "hex"),
      rawKey,
    ]),
    format: "der",
    type: "spki",
  });
  if (!verify(null, manifestBytes, publicKey, signature))
    throw new Error("发行清单签名错误");
  const manifest = JSON.parse(manifestBytes);
  if (manifest.version !== pkg.version || !semver.valid(pkg.version))
    throw new Error("发行版本与源码不一致");
  const expected = new Set([
    "release-manifest.json",
    "release-manifest.sig",
    "checksums.txt",
  ]);
  if (testOnly) expected.add("TEST-ONLY-public-key.txt");
  for (const component of ["client", "server"])
    for (const os of ["linux", "darwin", "windows"])
      for (const arch of ["amd64", "arm64"])
        expected.add(
          `tunnelquay-${component}_${pkg.version}_${os}_${arch}${os === "windows" ? ".zip" : ".tar.gz"}`,
        );
  const files = await readdir(root);
  if (
    files.length !== expected.size ||
    files.some((name) => !expected.has(name))
  )
    throw new Error("发行附件数量或文件名不符，测试签名产物不能发布");
  if (
    manifest.artifacts.length !== 12 ||
    new Set(manifest.artifacts.map((a) => a.name)).size !== 12
  )
    throw new Error("清单须覆盖十二个不同的平台组件归档");
  for (const artifact of manifest.artifacts) {
    if (!expected.has(artifact.name)) throw new Error("清单包含非发行附件");
    const bytes = await readFile(resolve(root, artifact.name));
    if (
      bytes.length !== artifact.size ||
      createHash("sha256").update(bytes).digest("hex") !== artifact.sha256
    )
      throw new Error(`附件校验失败 ${artifact.name}`);
  }
  const checksumLines = (await readFile(resolve(root, "checksums.txt"), "utf8"))
    .trim()
    .split("\n");
  if (checksumLines.length !== (testOnly ? 15 : 14))
    throw new Error("校验文件须覆盖全部归档、清单与签名");
  const checked = new Set();
  for (const line of checksumLines) {
    const [hash, name] = line.split("  ");
    if (!expected.has(name) || name === "checksums.txt" || checked.has(name))
      throw new Error("校验文件包含无效或重复名称");
    checked.add(name);
    const bytes = await readFile(resolve(root, name));
    if (createHash("sha256").update(bytes).digest("hex") !== hash)
      throw new Error(`校验文件不符 ${name}`);
  }
  console.log("十二个平台组件归档、清单签名和校验文件验证通过");
  if (mode === "--publish") {
    if (
      process.env.GITHUB_REF_NAME !== tag ||
      process.env.GITHUB_REF_TYPE !== "tag"
    )
      throw new Error("公开发布必须来自对应版本标签");
    execFileSync("git", ["merge-base", "--is-ancestor", "HEAD", "origin/main"]);
    const repo = process.env.GITHUB_REPOSITORY;
    const token = process.env.GH_TOKEN;
    if (!repo || !token) throw new Error("缺少 GitHub 发布凭据");
    const apiRoot = `https://api.github.com/repos/${repo}`;
    const headers = {
      Authorization: `Bearer ${token}`,
      Accept: "application/vnd.github+json",
      "X-GitHub-Api-Version": "2022-11-28",
    };
    async function call(
      url,
      method = "GET",
      body,
      contentType = "application/json",
    ) {
      const result = await fetch(url, {
        method,
        headers: { ...headers, "Content-Type": contentType },
        body:
          body === undefined
            ? undefined
            : contentType === "application/json"
              ? JSON.stringify(body)
              : body,
        signal: AbortSignal.timeout(180000),
      });
      if (!result.ok)
        throw new Error(`GitHub 请求失败 ${method} ${result.status}`);
      return result.status === 204 ? null : result.json();
    }
    const lookup = await fetch(`${apiRoot}/releases/tags/${tag}`, {
      headers,
      signal: AbortSignal.timeout(30000),
    });
    if (!lookup.ok && lookup.status !== 404)
      throw new Error(`查询发行失败 ${lookup.status}`);
    let release = lookup.ok ? await lookup.json() : null;
    if (release && !release.draft)
      throw new Error("同版本已经公开，不覆盖已发布版本");
    const prerelease = semver.prerelease(pkg.version) !== null;
    if (!release)
      release = await call(`${apiRoot}/releases`, "POST", {
        tag_name: tag,
        name: `TunnelQuay ${pkg.version}`,
        body: notes,
        draft: true,
        prerelease,
      });
    const latest = await call(`${apiRoot}/releases/${release.id}`);
    if (!latest.draft) throw new Error("发行已被公开，停止修改");
    for (const asset of latest.assets)
      await call(`${apiRoot}/releases/assets/${asset.id}`, "DELETE");
    const upload = release.upload_url.split("{")[0];
    for (const name of files) {
      const bytes = await readFile(resolve(root, name));
      await call(
        `${upload}?name=${encodeURIComponent(name)}`,
        "POST",
        bytes,
        "application/octet-stream",
      );
    }
    const final = await call(`${apiRoot}/releases/${release.id}`);
    if (
      !final.draft ||
      final.assets.length !== files.length ||
      final.assets.some((a) => !expected.has(a.name) || a.state !== "uploaded")
    )
      throw new Error("草稿附件未全部上传完成");
    await call(`${apiRoot}/releases/${release.id}`, "PATCH", {
      draft: false,
      prerelease,
      body: notes,
    });
    console.log(`已公开 ${tag}`);
  }
} catch (err) {
  console.error(`发行校验失败：${err.message}`);
  process.exitCode = 1;
}
