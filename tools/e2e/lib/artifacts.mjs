/**
 * 现场与回看：一次验证跑完，产出「可以直接打开看」的东西。
 *
 * 目录结构（默认 `<临时目录>/<调用方给的 root>/runs/<时间戳>/`）：
 *   shots/01-ok-页面加载.png      每个检查自动截图（成功/失败都留）
 *   video/<页面>.mp4              整场录屏（Playwright 只能录 webm，收尾时转成兼容性最好的 mp4）
 *   report.html                   回看页：录屏 + 按顺序排列的截图 + 失败原因
 *   run.log                       控制台输出全文
 *   fail-*.png                    断言失败时的额外截图（带当时页面状态）
 *
 * 为什么默认开着：截图与录屏能把「界面到底长什么样、什么时候变的」直接摆出来，省掉人工再跑一遍；
 * 录屏还能看出时序（例如弹窗何时遮挡、点击何时被拦截），这是静态截图给不了的。
 *
 * 为什么录屏最终是 mp4：Playwright 只能录 webm，而 webm 出了浏览器就处处碰壁（QuickTime、
 * Office、手机相册都不认）。这里只负责目录与回看页，转码本身在 lib/video.mjs，
 * 由 report.finish() 在浏览器关闭、webm 落盘之后触发。
 */
import { closeSync, existsSync, mkdirSync, openSync, readdirSync, readSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

/**
 * 建一次运行的现场目录。
 * @param {{ root: string, title: string }} options - 根目录与标题。
 * @returns {Run} 现场对象。
 */
export function createRun(options) {
  const stamp = new Date().toISOString().replaceAll(/[:.]/g, '-').slice(0, 19)
  const dir = join(options.root, 'runs', stamp)
  const shotsDir = join(dir, 'shots')
  const videoDir = join(dir, 'video')
  mkdirSync(shotsDir, { recursive: true })
  mkdirSync(videoDir, { recursive: true })
  return new Run({ dir, shotsDir, videoDir, title: options.title })
}

/** 一次运行的现场：截图、录屏目录、回看页。 */
export class Run {
  constructor(options) {
    this.dir = options.dir
    this.shotsDir = options.shotsDir
    this.videoDir = options.videoDir
    this.title = options.title
    /** 已保存的截图：[{ file, label, state, message }]。 */
    this.shots = []
    this.sequence = 0
  }

  /**
   * 把文件名里不能用的字符替换掉。
   * @param {string} text - 原始文本。
   * @returns {string} 安全文件名（限长）。
   */
  static slug(text) {
    return String(text)
      .replaceAll(/[^\p{L}\p{N}]+/gu, '-')
      .replaceAll(/^-+|-+$/g, '')
      .slice(0, 60)
  }

  /**
   * 给当前页面截一张图。
   * @param {import('playwright-core').Page | undefined} page - 页面；没有就跳过。
   * @param {string} label - 说明（会进文件名与回看页）。
   * @param {{ state?: 'ok' | 'fail' | 'step', message?: string }} [options] - 状态与失败说明。
   * @returns {Promise<string | undefined>} 截图路径。
   */
  async shot(page, label, options = {}) {
    if (page === undefined || page.isClosed()) return undefined
    this.sequence += 1
    const state = options.state ?? 'step'
    const file = `${String(this.sequence).padStart(2, '0')}-${state}-${Run.slug(label) || 'shot'}.png`
    const path = join(this.shotsDir, file)
    try {
      await page.screenshot({ path })
    } catch {
      // 页面正在导航或已崩，跳过这一张，不要让截图失败掩盖真正的断言结果。
      this.sequence -= 1
      return undefined
    }
    this.shots.push({ index: this.sequence, file, label, state, message: options.message })
    return path
  }

  /**
   * 本次运行已落盘的录屏文件。
   * @returns {string[]} 文件名（按名称排序）。
   */
  videoFiles() {
    return Run.listFiles(this.videoDir)
  }

  /**
   * 把一段文本写进现场目录。
   * @param {string} name - 文件名。
   * @param {string} text - 内容。
   * @returns {string} 路径。
   */
  writeText(name, text) {
    const path = join(this.dir, name)
    writeFileSync(path, text)
    return path
  }

  /**
   * 列出目录里的文件（缺失时返回空数组）。
   * @param {string} dir - 目录。
   * @returns {string[]} 文件名。
   */
  static listFiles(dir) {
    return existsSync(dir) ? readdirSync(dir).sort() : []
  }

  /**
   * 生成回看页（report.html）：录屏在最前，之后每一步一张卡片。
   * @param {{ passed: number, failed: number, skipped: number, failures: { name: string, error: Error, shotIndex?: number }[] }} summary - 报告统计。
   * @returns {string} report.html 的路径。
   */
  writeGallery(summary) {
    // 跳过打不开的录屏：0 字节的空文件，以及内容根本不是视频的垃圾文件（转码失败时会留下），
    // 嵌进回看页只会让人点开一个打不开的播放器。它们为什么没成，finish() 已在日志里说清。
    const videos = this.videoFiles().filter((file) => looksLikeVideo(join(this.videoDir, file)))
    const shotCards = this.shots.map(renderShotCard).join('\n')
    const videoCards =
      videos.length === 0
        ? '<p class="none">（没有录屏：可能用了 --no-video，或浏览器未正常关闭）</p>'
        : videos.map(renderVideoCard).join('\n')
    const failureList =
      summary.failures.length === 0
        ? ''
        : `<h2>失败原因</h2><ul class="stack">${summary.failures
            .map((failure) => {
              const name = escapeHtml(failure.name)
              // 找得到对应截图就做成锚点：点一下直接跳到那张现场图。
              const title = failure.shotIndex === undefined ? `<b>${name}</b>` : `<b><a href="#shot-${failure.shotIndex}">${name}</a></b>`
              return `<li>${title}<pre>${escapeHtml(String(failure.error?.message ?? failure.error))}</pre></li>`
            })
            .join('')}</ul>`
    const html = `<!doctype html>
<html lang="zh"><head><meta charset="utf-8" />
<title>${escapeHtml(this.title)} — 验证回看</title>
<style>
  :root { color-scheme: light dark; --line: #8884; --muted: #888; --ok: #2f855a; --fail: #c53030; --step: #4a5568; }
  * { box-sizing: border-box; }
  body { font: 14px/1.6 system-ui, -apple-system, "Segoe UI", sans-serif; margin: 0 auto; max-width: 1100px; padding: 24px; }
  h1 { font-size: 20px; margin: 0 0 4px; }
  .meta { color: var(--muted); margin-bottom: 24px; }
  .counts b { font-variant-numeric: tabular-nums; }
  h2 { font-size: 16px; margin: 28px 0 12px; }
  /* 每一步一张卡片：说明、图、报错都关在同一个边框里，就不必猜这段字属于上面还是下面那张图。 */
  .card { border: 1px solid var(--line); border-radius: 10px; padding: 12px; margin-bottom: 16px; }
  .card.ok { border-left: 4px solid var(--ok); }
  .card.fail { border-left: 4px solid var(--fail); }
  .card.step { border-left: 4px solid var(--step); }
  /* 从「失败原因」点进来时把这张卡片框出来，免得跳过来还要自己找。 */
  .card:target { outline: 2px solid var(--fail); outline-offset: 3px; }
  .head { display: flex; align-items: baseline; gap: 8px; flex-wrap: wrap; margin-bottom: 10px; }
  .no { font: 12px/1 ui-monospace, SFMono-Regular, monospace; color: var(--muted); border: 1px solid var(--line); border-radius: 6px; padding: 4px 6px; }
  .label { font-weight: 600; }
  .file { font: 11px/1.4 ui-monospace, SFMono-Regular, monospace; color: var(--muted); margin-left: auto; }
  img, video { max-width: 100%; border: 1px solid var(--line); border-radius: 8px; display: block; }
  .badge { padding: 1px 8px; border-radius: 999px; font-size: 11px; color: #fff; white-space: nowrap; }
  .badge.ok { background: var(--ok); } .badge.fail { background: var(--fail); } .badge.step { background: var(--step); }
  .msg { font-size: 12px; color: var(--fail); white-space: pre-wrap; margin: 10px 0 0; }
  .stack { list-style: none; padding: 0; }
  .stack li { border: 1px solid var(--line); border-radius: 8px; padding: 10px 12px; margin-bottom: 10px; }
  pre { white-space: pre-wrap; background: #8881; padding: 8px; border-radius: 6px; margin: 8px 0 0; }
  .none { color: var(--muted); }
</style></head>
<body>
<h1>${escapeHtml(this.title)}</h1>
<div class="meta counts">${new Date().toLocaleString()} · <b>${summary.passed}</b> 通过 · <b>${summary.failed}</b> 失败 · <b>${summary.skipped}</b> 跳过 · 截图 <b>${this.shots.length}</b> 张 · 录屏 <b>${videos.length}</b> 段</div>
<h2>录屏</h2>
${videoCards}
${failureList}
<h2>每一步（按时间顺序）</h2>
${shotCards || '<p class="none">（没有截图）</p>'}
</body></html>`
    return this.writeText('report.html', html)
  }
}

/** 截图状态在回看页上的中文说法。 */
const STATE_LABELS = { ok: '通过', fail: '失败', step: '过程' }

/**
 * 把一张截图渲染成回看页的卡片。
 *
 * 说明在图的**上面**、且与图同处一个边框内：以前说明在图下面、又没有容器，
 * 一段文字紧贴上图的底边和下图的顶边，看的人分不清它到底在说哪张图。
 * @param {{ index: number, file: string, label: string, state: string, message?: string }} shot - 截图记录。
 * @returns {string} HTML 片段。
 */
function renderShotCard(shot) {
  const message = shot.message === undefined ? '' : `\n  <p class="msg">${escapeHtml(shot.message)}</p>`
  return `<section class="card ${shot.state}" id="shot-${shot.index}">
  <div class="head">
    <span class="no">${String(shot.index).padStart(2, '0')}</span>
    <span class="badge ${shot.state}">${STATE_LABELS[shot.state] ?? shot.state}</span>
    <span class="label">${escapeHtml(shot.label)}</span>
    <span class="file">${escapeHtml(shot.file)}</span>
  </div>
  <img src="shots/${encodeURIComponent(shot.file)}" alt="${escapeHtml(shot.label)}" loading="lazy" />${message}
</section>`
}

/**
 * 把一段录屏渲染成回看页的卡片。
 * @param {string} file - 录屏文件名。
 * @returns {string} HTML 片段。
 */
function renderVideoCard(file) {
  return `<section class="card">
  <div class="head"><span class="label">${escapeHtml(file)}</span></div>
  <video src="video/${encodeURIComponent(file)}" controls preload="metadata"></video>
</section>`
}

/**
 * 转义 HTML 文本。
 * @param {string} text - 原始文本。
 * @returns {string} 转义结果。
 */
function escapeHtml(text) {
  return String(text).replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;').replaceAll('"', '&quot;')
}

/**
 * 这个文件是不是一个能播的视频容器。
 *
 * 只看文件头的魔数，读 12 个字节就够：webm/matroska 以 EBML 头 `1A 45 DF A3` 开头，
 * mp4/mov 在第 4-8 字节是 `ftyp`。用「能不能播」而不是「是不是 0 字节」做判据，是因为
 * 转码失败时留下的不只是空文件——实测还有个 17 字节的文本文件，它也播不了，
 * 但大小不是 0，只看大小就会漏过去。
 * @param {string} path - 文件路径。
 * @returns {boolean} 看起来是不是能播的视频。
 */
function looksLikeVideo(path) {
  let descriptor
  try {
    descriptor = openSync(path, 'r')
    const head = Buffer.alloc(12)
    if (readSync(descriptor, head, 0, 12, 0) < 12) return false
    if (head[0] === 0x1a && head[1] === 0x45 && head[2] === 0xdf && head[3] === 0xa3) return true
    return head.subarray(4, 8).toString('latin1') === 'ftyp'
  } catch {
    return false
  } finally {
    if (descriptor !== undefined) closeSync(descriptor)
  }
}