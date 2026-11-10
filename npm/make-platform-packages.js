#!/usr/bin/env node
"use strict";

// 生成 npm 分发产物：主包 + 各平台二进制包。
//
// 二进制随 npm 平台包发布，安装时 npm 按平台包的 os/cpu 只拉当前平台那一份，
// 走 registry（国内可用镜像）。主包的 postinstall 只在平台包缺失时回退下载，
// 不再是唯一获取途径。
//
// 产物结构：
//   <out>/main/                 主包：bin/heron + install.js + lib/
//   <out>/platform-<key>/       平台包：bin/<binary>，带 os/cpu 约束
//
// 用法：
//   node npm/make-platform-packages.js --dist dist --out npm-dist --version 0.0.38

const fs = require("fs");
const path = require("path");

const PACKAGE_NAME = "@qinghuangniao/heron-ai";
const REPO = "janostudio/heron-ai";
const NPM_DIR = __dirname;

const PLATFORMS = [
  { key: "linux-x64", os: "linux", cpu: "x64", binary: "heron-linux-amd64" },
  { key: "linux-arm64", os: "linux", cpu: "arm64", binary: "heron-linux-arm64" },
  { key: "darwin-x64", os: "darwin", cpu: "x64", binary: "heron-darwin-amd64" },
  { key: "darwin-arm64", os: "darwin", cpu: "arm64", binary: "heron-darwin-arm64" },
  { key: "win32-x64", os: "win32", cpu: "x64", binary: "heron-windows-amd64.exe" },
];

function parseArgs(argv) {
  const args = { dist: path.resolve(NPM_DIR, "..", "dist"), out: path.resolve(NPM_DIR, "..", "npm-dist"), version: null };
  for (let i = 0; i < argv.length; i++) {
    const flag = argv[i];
    if (flag === "--dist") args.dist = path.resolve(argv[++i]);
    else if (flag === "--out") args.out = path.resolve(argv[++i]);
    else if (flag === "--version") args.version = argv[++i];
    else throw new Error(`Unknown argument: ${flag}`);
  }
  return args;
}

// 版本必须来自 tag：模板里的 0.0.0-dev 只是占位，按占位发布会覆盖不到任何真实版本。
function readVersion(explicit) {
  const version = explicit || JSON.parse(fs.readFileSync(path.join(NPM_DIR, "package.json"), "utf8")).version;
  if (!version) throw new Error("Version is required: pass --version or set it in npm/package.json");
  if (version.includes("dev")) {
    throw new Error(`Refusing to build packages with placeholder version: ${version}`);
  }
  return version;
}

function writeJSON(file, value) {
  fs.writeFileSync(file, JSON.stringify(value, null, 2) + "\n");
}

function copyInto(source, target) {
  fs.mkdirSync(path.dirname(target), { recursive: true });
  fs.copyFileSync(source, target);
  fs.chmodSync(target, fs.statSync(source).mode & 0o777);
}

// buildMainPackage 复制主包模板并注入版本与实际存在的平台包依赖。
// optionalDependencies 用 npm: 别名指向同一包名的预发布版本，安装时落到
// node_modules/@qinghuangniao/heron-ai-<platform>。
function buildMainPackage(outDir, version) {
  const pkgDir = path.join(outDir, "main");
  fs.mkdirSync(pkgDir, { recursive: true });

  const pkg = JSON.parse(fs.readFileSync(path.join(NPM_DIR, "package.json"), "utf8"));
  pkg.version = version;
  pkg.optionalDependencies = {};
  for (const platform of PLATFORMS) {
    pkg.optionalDependencies[`${PACKAGE_NAME}-${platform.key}`] = `npm:${PACKAGE_NAME}@${version}-${platform.key}`;
  }
  writeJSON(path.join(pkgDir, "package.json"), pkg);

  copyInto(path.join(NPM_DIR, "install.js"), path.join(pkgDir, "install.js"));
  copyInto(path.join(NPM_DIR, "bin", "heron"), path.join(pkgDir, "bin", "heron"));
  copyInto(path.join(NPM_DIR, "lib", "resolve-binary.js"), path.join(pkgDir, "lib", "resolve-binary.js"));

  // npm 页面上的说明来自包内 README.md，图标必须与 README 里的相对路径
  // （assets/icon.png）对得上，否则包页首屏是一张破图。
  const repoRoot = path.resolve(NPM_DIR, "..");
  const readme = path.join(repoRoot, "README.md");
  const icon = path.join(repoRoot, "assets", "icon.png");
  if (fs.existsSync(readme)) copyInto(readme, path.join(pkgDir, "README.md"));
  if (fs.existsSync(icon)) copyInto(icon, path.join(pkgDir, "assets", "icon.png"));

  return pkgDir;
}

function buildPlatformPackage(outDir, version, platform, distDir) {
  const source = path.join(distDir, platform.binary);
  if (!fs.existsSync(source)) {
    throw new Error(`Missing platform binary for ${platform.key}: ${source}`);
  }

  const pkgDir = path.join(outDir, `platform-${platform.key}`);
  const binaryPath = path.join(pkgDir, "bin", platform.binary);
  copyInto(source, binaryPath);
  fs.chmodSync(binaryPath, 0o755);

  writeJSON(path.join(pkgDir, "package.json"), {
    name: PACKAGE_NAME,
    version: `${version}-${platform.key}`,
    description: `Heron AI engine binary for ${platform.key}`,
    os: [platform.os],
    cpu: [platform.cpu],
    files: ["bin/"],
    license: "MIT",
    repository: { type: "git", url: `https://github.com/${REPO}` },
  });

  return pkgDir;
}

function main() {
  const args = parseArgs(process.argv.slice(2));
  const version = readVersion(args.version);

  fs.rmSync(args.out, { recursive: true, force: true });
  fs.mkdirSync(args.out, { recursive: true });

  const platformDirs = PLATFORMS.map((platform) => buildPlatformPackage(args.out, version, platform, args.dist));
  const mainDir = buildMainPackage(args.out, version);

  for (const dir of platformDirs) {
    console.log(`[make-platform-packages] ${path.basename(dir)}`);
  }
  console.log(`[make-platform-packages] main (${version}) -> ${mainDir}`);
}

main();
