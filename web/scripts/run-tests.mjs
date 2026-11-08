// web 测试运行器：web/src 下的测试不使用测试框架，这里用 esbuild 逐个打包后直接执行。
// 用法：node scripts/run-tests.mjs（或 npm test）。
import { build } from 'esbuild';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const webDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const srcDir = path.join(webDir, 'src');

function collectTests(dir) {
  const tests = [];
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    if (entry.name === 'node_modules') continue;
    const fullPath = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      tests.push(...collectTests(fullPath));
    } else if (/\.test\.tsx?$/.test(entry.name)) {
      tests.push(fullPath);
    }
  }
  return tests.sort();
}

// 只支持 Vite import.meta.glob 的常见形式：目录固定，文件名用 * / ? 通配。
function expandGlob(pattern) {
  const dir = path.dirname(pattern);
  const base = path.basename(pattern);
  if (/[*?]/.test(dir)) {
    throw new Error(`暂不支持带通配符的目录：${pattern}`);
  }
  const source = base.replace(/[.+^${}()|[\]\\]/g, '\\$&').replace(/\*/g, '[^/]*').replace(/\?/g, '[^/]');
  const matcher = new RegExp(`^${source}$`);
  return fs
    .readdirSync(dir, { withFileTypes: true })
    .filter((entry) => entry.isFile() && matcher.test(entry.name))
    .map((entry) => path.join(dir, entry.name))
    .sort();
}

// Vite 的 import.meta.glob 由 Vite 编译，esbuild 不认；这里把它展开成静态对象。
const importMetaGlobPlugin = {
  name: 'import-meta-glob',
  setup(buildContext) {
    buildContext.onLoad({ filter: /\.(ts|tsx)$/ }, async (args) => {
      let contents = await fs.promises.readFile(args.path, 'utf8');
      if (!contents.includes('import.meta.glob')) return undefined;
      const sourceDir = path.dirname(args.path);
      let replaced = false;
      contents = contents.replace(
        /import\.meta\.glob(?:<[^>]*>)?\(\s*(['"])([^'"]+)\1\s*(?:,\s*\{[^}]*\})?\s*\)/g,
        (_match, _quote, pattern) => {
          replaced = true;
          const entries = expandGlob(path.resolve(sourceDir, pattern)).map((file) => {
            let key = path.relative(sourceDir, file).split(path.sep).join('/');
            if (!key.startsWith('.')) key = `./${key}`;
            // query: '?url' 的返回值；测试只关心图标名与来源，不解析真实资源。
            const url = `/${path.relative(webDir, file).split(path.sep).join('/')}`;
            return `${JSON.stringify(key)}: ${JSON.stringify(url)}`;
          });
          return `{ ${entries.join(', ')} }`;
        }
      );
      // 只有注释里提到 import.meta.glob 的文件不需要改写。
      if (!replaced) return undefined;
      return { contents, loader: args.path.endsWith('.tsx') ? 'tsx' : 'ts' };
    });
  }
};

// 测试实际用到的浏览器桩：window 与动画帧。不引入 jsdom。
// react-dom/server 内部会 require('stream')，打包成 ESM 后需要补一个 createRequire。
const browserStubBanner = `
import { createRequire as __mcodeCreateRequire } from 'node:module';
const require = __mcodeCreateRequire(import.meta.url);
if (typeof globalThis.window === 'undefined') {
  globalThis.window = globalThis;
}
if (typeof globalThis.window.location === 'undefined') {
  globalThis.window.location = { origin: 'http://localhost', href: 'http://localhost/' };
}
if (typeof globalThis.requestAnimationFrame === 'undefined') {
  globalThis.requestAnimationFrame = (callback) => setTimeout(() => callback(Date.now()), 0);
  globalThis.cancelAnimationFrame = (handle) => clearTimeout(handle);
}
`;

async function bundleTest(testFile, outfile) {
  await build({
    entryPoints: [testFile],
    outfile,
    bundle: true,
    platform: 'node',
    format: 'esm',
    jsx: 'automatic',
    target: 'node20',
    logLevel: 'silent',
    banner: { js: browserStubBanner },
    define: {
      'import.meta.url': JSON.stringify(pathToFileURL(testFile).href),
      'import.meta.env.BASE_URL': JSON.stringify('/')
    },
    // CSS 与图片只被打包进依赖图，测试本身不读取它们。
    loader: {
      '.css': 'empty',
      '.svg': 'empty',
      '.png': 'empty',
      '.jpg': 'empty',
      '.jpeg': 'empty',
      '.webp': 'empty',
      '.gif': 'empty',
      '.woff': 'empty',
      '.woff2': 'empty',
      '.ttf': 'empty'
    },
    plugins: [importMetaGlobPlugin]
  });
}

async function run() {
  const tests = collectTests(srcDir);
  if (tests.length === 0) {
    console.error('未找到 web/src 下的 *.test.ts / *.test.tsx');
    process.exit(1);
  }

  const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), 'mcode-web-tests-'));
  const failed = [];
  let index = 0;

  try {
    for (const testFile of tests) {
      index += 1;
      const label = path.relative(webDir, testFile);
      const outfile = path.join(tmpDir, `${index}-${path.basename(testFile).replace(/\.tsx?$/, '')}.mjs`);
      const startedAt = Date.now();
      try {
        await bundleTest(testFile, outfile);
      } catch (error) {
        failed.push({ label, reason: `打包失败：${error instanceof Error ? error.message : String(error)}` });
        console.log(`✗ ${label}（打包失败）`);
        continue;
      }

      const result = spawnSync(process.execPath, [outfile], {
        stdio: 'inherit',
        timeout: 30_000
      });
      const elapsed = Date.now() - startedAt;

      if (result.error) {
        const reason = result.error.code === 'ETIMEDOUT' ? '执行超时' : `执行失败：${result.error.message}`;
        failed.push({ label, reason });
        console.log(`✗ ${label}（${reason}，${elapsed}ms）`);
      } else if (result.status !== 0) {
        failed.push({ label, reason: `退出码 ${result.status ?? result.signal}` });
        console.log(`✗ ${label}（${elapsed}ms）`);
      } else {
        console.log(`✓ ${label}（${elapsed}ms）`);
      }
    }
  } finally {
    fs.rmSync(tmpDir, { recursive: true, force: true });
  }

  const passed = tests.length - failed.length;
  console.log(`\nweb 测试：${passed} 通过，${failed.length} 失败，共 ${tests.length} 个`);
  if (failed.length > 0) {
    for (const item of failed) {
      console.log(`  ${item.label}：${item.reason}`);
    }
    process.exit(1);
  }
}

run().catch((error) => {
  console.error(error);
  process.exit(1);
});
