/**
 * 通用浏览器层：定位一个 Chromium，用 Playwright 启动它。
 *
 * 这里只补 Playwright 本身不管的三件事，其余（locator、自动等待、断言、trace）直接用
 * Playwright 的原生 API——不要把 Playwright 再包一层，那只会挡住它的能力。
 *
 *   1. 定位可执行文件：优先显式指定，其次 Playwright 自己登记的版本，再其次扫描
 *      Playwright 的共享浏览器缓存（本机跑过一次 playwright 就已经下载好了），最后看 PATH。
 *   2. 绕过环境代理：不这么做的话 Chromium 会继承 http_proxy/https_proxy，访问测试用的
 *      假域名会被代理回 502——这是实测出现过的问题，不是理论风险。
 *   3. 假域名映射：`--host-resolver-rules` 把测试域名指到 127.0.0.1，不用改 /etc/hosts，
 *      也不用动被测服务的绑定地址。
 */
import { existsSync, mkdirSync, readdirSync } from 'node:fs'
import { homedir } from 'node:os'
import { join } from 'node:path'
import { chromium } from 'playwright-core'
import { which } from './toolchain.mjs'

/** 显式指定浏览器可执行文件的环境变量（两者都认，前者为本工具包专用）。 */
const OVERRIDE_VARS = ['BROWSER_VERIFY_CHROMIUM', 'TL_CHROMIUM']

/**
 * 定位一个可用的 Chromium。
 *
 * 顺序是有讲究的：Playwright 自己登记的版本排第二，因为那是与已装 playwright-core 版本
 * 匹配的构建；缓存里其他版本排第三（能用，只是版本不一定匹配）；PATH 排最后，因为系统
 * 自带的 Chrome 可能缺 headless 依赖或被发行版改动过。
 * @returns {{ path: string, source: string }} 可执行文件路径与来源说明。
 */
export function resolveChromium() {
  for (const name of OVERRIDE_VARS) {
    const value = process.env[name]
    if (value === undefined || value === '') continue
    if (!existsSync(value)) throw new Error(`${name} 指向的文件不存在：${value}`)
    return { path: value, source: name }
  }

  try {
    const registered = chromium.executablePath()
    if (typeof registered === 'string' && existsSync(registered)) {
      return { path: registered, source: 'playwright-core 登记的版本' }
    }
  } catch {
    /* 没装对应构建时 executablePath() 会抛；落到缓存扫描 */
  }

  const cacheRoot = join(homedir(), '.cache', 'ms-playwright')
  if (existsSync(cacheRoot)) {
    const revisions = readdirSync(cacheRoot)
      .filter((name) => /^chromium-\d+$/.test(name))
      .sort((left, right) => Number(right.slice('chromium-'.length)) - Number(left.slice('chromium-'.length)))
    for (const revision of revisions) {
      for (const layout of readdirSync(join(cacheRoot, revision))) {
        const candidate = join(cacheRoot, revision, layout, 'chrome')
        if (existsSync(candidate)) return { path: candidate, source: `playwright 缓存 ${revision}` }
      }
    }
  }

  for (const command of ['chromium', 'chromium-browser', 'google-chrome', 'google-chrome-stable']) {
    const path = which(command)
    if (path !== undefined) return { path, source: `PATH 上的 ${command}` }
  }

  throw new Error(
    [
      '找不到 Chromium。三种办法：',
      `  1. 指定可执行文件：${OVERRIDE_VARS[0]}=/path/to/chrome`,
      '  2. 在装了 playwright-core 的目录跑一次 `npx playwright install chromium`',
      '  3. 在 PATH 上装一个 chromium / google-chrome',
    ].join('\n'),
  )
}

/**
 * 从子进程环境里摘掉所有代理变量。
 *
 * 为什么必须做这一步：这台机器上同时设了 `http_proxy`/`https_proxy`/`all_proxy`（后者还是
 * SOCKS），Chromium 会继承它们并把测试用的假域名请求交给代理，结果拿到 502 或
 * `net::ERR_PROXY_CONNECTION_FAILED`。Playwright 的 `proxy: { server: 'direct://' }` 在这台
 * 机器上不足以盖掉这些变量（实测仍报 ERR_PROXY_CONNECTION_FAILED），所以直接清洗环境
 * 并加 `--no-proxy-server`。
 * @param {NodeJS.ProcessEnv} source - 原环境。
 * @returns {NodeJS.ProcessEnv} 去掉代理变量的副本。
 */
function withoutProxyEnvironment(source) {
  const cleaned = { ...source }
  for (const key of Object.keys(cleaned)) {
    if (/^(https?|all|no)_proxy$/i.test(key)) delete cleaned[key]
  }
  delete cleaned.NODE_USE_ENV_PROXY
  return cleaned
}

/**
 * 启动浏览器。
 * @param {{ hostRules?: Record<string, string>, headless?: boolean, args?: string[], slowMoMs?: number, proxyServer?: string }} [options]
 *   hostRules：假域名 → 目标地址（例如 `{ 'tl-e2e.test': '127.0.0.1' }`）；args：追加的 Chromium 参数；
 *   proxyServer：确实需要走代理时显式给出，例如 `http://127.0.0.1:7890`（默认不走任何代理）。
 * @returns {Promise<{ browser: import('playwright-core').Browser, executablePath: string, source: string }>}
 *   Playwright 的 Browser 本体（自己 `newPage()`），外加定位信息。
 */
export async function launchBrowser(options = {}) {
  const { path, source } = resolveChromium()
  const rules = Object.entries(options.hostRules ?? {}).map(([host, address]) => `MAP ${host} ${address}`)
  const explicitProxy = options.proxyServer
  const browser = await chromium.launch({
    executablePath: path,
    headless: options.headless ?? true,
    env: withoutProxyEnvironment(process.env),
    ...(explicitProxy === undefined ? {} : { proxy: { server: explicitProxy } }),
    ...(options.slowMoMs === undefined ? {} : { slowMo: options.slowMoMs }),
    args: [
      ...(explicitProxy === undefined ? ['--no-proxy-server'] : []),
      ...(rules.length > 0 ? [`--host-resolver-rules=${rules.join(', ')}`] : []),
      ...(options.args ?? []),
    ],
  })
  return { browser, executablePath: path, source }
}

/**
 * 开一个页面并带上常用上下文。
 *
 * 传了 `videoDir` 就**整场录屏**：Playwright 在上下文关闭时把 webm 落盘（浏览器关闭时会
 * 一起收尾）。录屏能看到时序——弹窗什么时候挡住点击、界面什么时候变——静态截图给不了这个。
 * 这里录出来的是 webm，最终产物由 `report.finish()` 转成兼容性最好的 mp4（见 `lib/video.mjs`）。
 *
 * `extraHTTPHeaders` 给这个上下文的所有请求加请求头。实测**对 WebSocket 握手同样生效**
 * （用 ttyd 的 `--auth-header` 验证过：不加头 WebSocket 被拒，加了头终端才连上），
 * 所以可以拿它测「靠请求头鉴权、同时又要开 WebSocket」的服务。
 *
 * `deviceScaleFactor` 配上 `isMobile` 才是真正的手机模拟，**只给 deviceScaleFactor 会漏掉
 * 布局视口这一层**：`isMobile` 才会启用真机那套「没有 viewport meta 就退回约 980px 虚拟
 * 布局视口」的行为。实测差别很大——同一个终端，只给 dpr=3 时布局宽度是 390，加上
 * `isMobile: true` 后没有 viewport meta 的页面变成 980（终端算出 124 行 x 106 列）。
 *
 * `deviceScaleFactor` 也是验手机渲染的关键。用
 * `{ viewport: { width: 390, height: 844 }, deviceScaleFactor: 3, isMobile: true }` 能**忠实
 * 复现真机问题**：同一个终端在模拟与真机上表现逐条一致——webgl 渲染器整屏全黑（但命令正常
 * 执行）、canvas 渲染器字形放大约 3 倍并裁掉右侧、dom 渲染器正常。
 *
 * 注意「截图里有东西」不等于「画面正确」：放大类故障的截图字节数反而很大，看起来完全正常，
 * 必须另验几何（打印 20 行就应当看到 20 行、右侧不裁）。
 * @param {import('playwright-core').Browser} browser - 由 {@link launchBrowser} 返回的浏览器。
 * @param {{ viewport?: { width: number, height: number }, deviceScaleFactor?: number, locale?: string, videoDir?: string, extraHTTPHeaders?: Record<string, string> }} [options]
 *   上下文参数；`videoDir` 为录屏输出目录（通常用 Run 的 `videoDir`）。
 * @returns {Promise<import('playwright-core').Page>} 页面。
 */
export async function openPage(browser, options = {}) {
  const viewport = options.viewport ?? { width: 1280, height: 820 }
  const video =
    options.videoDir === undefined
      ? {}
      : { recordVideo: { dir: options.videoDir, size: { width: viewport.width, height: viewport.height } } }
  const context = await browser.newContext({
    viewport,
    deviceScaleFactor: options.deviceScaleFactor ?? 1,
    ...video,
    ...(options.locale === undefined ? {} : { locale: options.locale }),
    ...(options.extraHTTPHeaders === undefined ? {} : { extraHTTPHeaders: options.extraHTTPHeaders }),
    ...(options.isMobile === undefined ? {} : { isMobile: options.isMobile }),
    ...(options.hasTouch === undefined ? {} : { hasTouch: options.hasTouch }),
  })
  if (options.videoDir !== undefined) mkdirSync(options.videoDir, { recursive: true })
  return context.newPage()
}