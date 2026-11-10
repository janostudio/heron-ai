"use strict";

// 解析当前平台可用的引擎二进制。
//
// 解析顺序：
//   1. 主包 bin/ 下的同名二进制。本地构建（scripts/heron-dev.sh build ai）
//      直接把产物覆盖在这里，优先保证开发时用的是未发布版本。
//   2. npm 平台包 @qinghuangniao/heron-ai-<platform>。发布时每个平台的二进制
//      打成独立包，安装时 npm 按平台包的 os/cpu 字段只拉当前平台那一份，
//      走 registry（国内可用镜像），不依赖 GitHub Releases 的连通性。
//   3. 都没有时返回 binaryPath=null，由调用方决定报错或回退下载。

const fs = require("fs");
const path = require("path");

const PLATFORMS = {
  "linux-x64": "heron-linux-amd64",
  "linux-arm64": "heron-linux-arm64",
  "darwin-x64": "heron-darwin-amd64",
  "darwin-arm64": "heron-darwin-arm64",
  "win32-x64": "heron-windows-amd64.exe",
};

const PLATFORM_PACKAGE_PREFIX = "@qinghuangniao/heron-ai-";

function currentPlatformKey() {
  return `${process.platform}-${process.arch}`;
}

function platformPackageName(platformKey) {
  return PLATFORM_PACKAGE_PREFIX + platformKey;
}

// resolveBinary 返回 { supported, platformKey, binaryName, binaryPath, source, packageName }。
// binaryPath 为 null 表示当前平台受支持但二进制还没就位。
function resolveBinary() {
  const platformKey = currentPlatformKey();
  const binaryName = PLATFORMS[platformKey];
  if (!binaryName) {
    return { supported: false, platformKey, binaryName: null, binaryPath: null, source: null };
  }

  const localPath = path.join(__dirname, "..", "bin", binaryName);
  if (fs.existsSync(localPath)) {
    return { supported: true, platformKey, binaryName, binaryPath: localPath, source: "local" };
  }

  const packageName = platformPackageName(platformKey);
  try {
    const packageJsonPath = require.resolve(`${packageName}/package.json`);
    const candidate = path.join(path.dirname(packageJsonPath), "bin", binaryName);
    if (fs.existsSync(candidate)) {
      return {
        supported: true,
        platformKey,
        binaryName,
        binaryPath: candidate,
        source: "platform-package",
        packageName,
      };
    }
  } catch (err) {
    // 平台包没装：用了 --no-optional、registry 镜像还没同步到当前版本，
    // 或者不是通过 npm 安装的。交给调用方回退。
  }

  return { supported: true, platformKey, binaryName, binaryPath: null, source: null };
}

module.exports = { PLATFORMS, resolveBinary, currentPlatformKey, platformPackageName };
