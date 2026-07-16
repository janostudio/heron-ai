#!/usr/bin/env node
// install.js - postinstall script for heron-ai
// Auto-detects platform and downloads the correct Go binary from GitHub Releases

const os = require('os');
const fs = require('fs');
const path = require('path');
const { execSync } = require('child_process');

const VERSION = 'v' + require('./package.json').version;
const REPO = 'heron-ai/heron-ai';
const MAX_RETRIES = 3;

const PLATFORM_MAP = {
  'linux-x64':   { binary: 'heron-linux-amd64', ext: '' },
  'linux-arm64': { binary: 'heron-linux-arm64', ext: '' },
  'darwin-x64':   { binary: 'heron-darwin-amd64', ext: '' },
  'darwin-arm64': { binary: 'heron-darwin-arm64', ext: '' },
  'win32-x64':    { binary: 'heron-windows-amd64', ext: '.exe' },
};

function getPlatformKey() {
  const platform = os.platform();
  const arch = os.arch() === 'x64' ? 'x64' : os.arch();
  return `${platform}-${arch}`;
}

function downloadWithRetry(url, dest, retries) {
  for (let i = 0; i < retries; i++) {
    try {
      console.log(`Downloading heron binary (attempt ${i + 1}/${retries})...`);
      execSync(`curl -fsSL --retry 3 -o "${dest}" "${url}"`, { stdio: 'inherit' });
      return true;
    } catch (err) {
      if (i === retries - 1) {
        console.error(`Failed to download after ${retries} attempts: ${err.message}`);
        return false;
      }
      const delay = Math.pow(2, i) * 1000;
      console.log(`Retrying in ${delay / 1000}s...`);
      Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, delay);
    }
  }
  return false;
}

function install() {
  const key = getPlatformKey();
  const target = PLATFORM_MAP[key];

  if (!target) {
    console.error(`Unsupported platform: ${key}`);
    console.error('Supported platforms: ' + Object.keys(PLATFORM_MAP).join(', '));
    process.exit(1);
  }

  const binaryName = target.binary + target.ext;
  const url = `https://github.com/${REPO}/releases/download/${VERSION}/${binaryName}`;
  const dest = path.join(__dirname, 'bin', binaryName);

  // Create bin directory if not exists
  const binDir = path.join(__dirname, 'bin');
  if (!fs.existsSync(binDir)) {
    fs.mkdirSync(binDir, { recursive: true });
  }

  // Check if binary already exists (skip download)
  if (fs.existsSync(dest)) {
    console.log(`Binary already exists: ${binaryName}`);
    fs.chmodSync(dest, 0o755);
    return;
  }

  if (!downloadWithRetry(url, dest, MAX_RETRIES)) {
    console.error('\nManual installation:');
    console.error(`  Download from: https://github.com/${REPO}/releases`);
    console.error(`  Place binary at: ${dest}`);
    process.exit(1);
  }

  fs.chmodSync(dest, 0o755);
  console.log(`Heron installed successfully! (${binaryName})`);
}

install();
