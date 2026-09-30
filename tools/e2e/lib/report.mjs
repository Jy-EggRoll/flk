/**
 * 断言与报告层。
 *
 * 核心只有两件事：
 *   1. 每条断言输出 `ok N - 说明` / `FAIL N - 说明`，失败时带上实际值；
 *   2. **每条断言自动截一张图**（成功也截，失败尤其要截），交给 {@link Run} 存盘并生成回看页。
 *
 * 截图是默认行为而不是可选项：验证跑完你只需要打开 report.html，就能看到界面每一步的样子，
 * 不必自己再点一遍。
 */
import { finalizeVideos } from './video.mjs'

/**
 * 建一个报告器。
 * @param {{ title: string, run?: import('./artifacts.mjs').Run, reviewSlowdown?: number }} options - 标题、现场目录，
 *   以及回看慢放倍数（默认见 lib/video.mjs 的 `DEFAULT_REVIEW_SLOWDOWN`，传 1 表示原速）。
 * @returns {Report} 报告器。
 */
export function createReport(options) {
  return new Report(options)
}

/** 报告器：按顺序执行断言，记录成功/失败/跳过，并逐条截图。 */
export class Report {
  constructor(options) {
    this.title = options.title
    this.run = options.run
    this.reviewSlowdown = options.reviewSlowdown
    this.passed = 0
    this.failed = 0
    this.skipped = 0
    this.failures = []
    /** 失败钩子（CLI 用来在失败时额外留存现场）。 */
    this.failureHooks = []
    /** 当前页面提供者：由调用方注册，用于自动截图。 */
    this.pageProvider = undefined
    this.warnedAboutMissingPage = false
    this.lines = []
    console.log(`\n# ${this.title}`)
  }

  /** 打印并记录一行输出。 */
  log(text) {
    this.lines.push(text)
    console.log(text)
  }

  /**
   * 注册「当前页面」提供者：每条断言结束后会拿它截图。
   * @param {() => import('playwright-core').Page | undefined} provider - 返回当前页面。
   */
  attachPage(provider) {
    this.pageProvider = provider
  }

  /** 打印一段小节标题。 */
  section(text) {
    this.log(`\n## ${text}`)
  }

  /** 打印一条不参与计数的说明。 */
  note(text) {
    this.log(`   · ${text}`)
  }

  /**
   * 主动截一张图（验证流程里想留某个中间状态时用）。
   * @param {string} label - 说明。
   * @returns {Promise<string | undefined>} 截图路径。
   */
  async shot(label) {
    if (this.run === undefined) return undefined
    const page = this.pageProvider?.()
    const path = await this.run.shot(page, label)
    if (path !== undefined) this.note(`截图：${path}`)
    return path
  }

  /**
   * 注册失败钩子。
   * @param {(context: { name: string, error: Error }) => Promise<string | undefined> | string | undefined} hook - 钩子。
   */
  onFailure(hook) {
    this.failureHooks.push(hook)
  }

  /**
   * 跑一条断言，并在结束后自动截图。
   * @param {string} name - 断言说明（一句话说清「应该发生什么」）。
   * @param {() => unknown | Promise<unknown>} body - 断言体；抛错即失败。
   * @returns {Promise<boolean>} 是否通过。
   */
  async check(name, body) {
    const index = this.passed + this.failed + 1
    let error
    // 在 try 之外声明：截图发生在 catch 之后，那时要往这条失败上补一个截图序号。
    let failure
    try {
      await body()
      this.passed += 1
      this.log(`ok ${index} - ${name}`)
    } catch (caught) {
      error = caught
      this.failed += 1
      failure = { name, error: caught, index }
      this.failures.push(failure)
      this.log(`FAIL ${index} - ${name}`)
      this.log(`   ${String(caught?.message ?? caught).split('\n').join('\n   ')}`)
      for (const hook of this.failureHooks) {
        try {
          const artifact = await hook(failure)
          if (artifact !== undefined) this.log(`   现场：${artifact}`)
        } catch (hookError) {
          this.log(`   （留存现场失败：${hookError.message}）`)
        }
      }
    }
    // 每条断言都留一张图：成功时是「这一步之后界面长这样」，失败时是证据。
    if (this.run !== undefined && this.pageProvider === undefined && !this.warnedAboutMissingPage) {
      this.warnedAboutMissingPage = true
      this.log('   ！没有注册当前页面（report.attachPage(() => page)），本次不会自动截图')
    }
    if (this.run !== undefined && this.pageProvider !== undefined) {
      const page = this.pageProvider?.()
      const path = await this.run.shot(page, name, {
        state: error === undefined ? 'ok' : 'fail',
        message: error === undefined ? undefined : String(error?.message ?? error),
      })
      if (path !== undefined) this.log(`   截图：${path}`)
      // 记下这次失败对应哪张图，回看页里「失败原因」就能直接锚到现场。
      if (failure !== undefined && path !== undefined) failure.shotIndex = this.run.shots.at(-1)?.index
    }
    return error === undefined
  }

  /**
   * 跳过一条断言。
   * @param {string} name - 断言说明。
   * @param {string} why - 跳过原因。
   */
  skip(name, why) {
    this.skipped += 1
    this.log(`skip - ${name}（${why}）`)
  }

  /**
   * 打印总结，写 run.log 与回看页。
   * @returns {number} 进程退出码：全过为 0。
   */
  summary() {
    const parts = [`${this.passed} 项通过`]
    if (this.skipped > 0) parts.push(`${this.skipped} 项跳过`)
    if (this.failed > 0) parts.push(`${this.failed} 项失败`)
    this.log(`\n${this.failed > 0 ? 'FAILED' : 'PASSED'} — ${parts.join('，')}`)
    if (this.run !== undefined) {
      this.writeRunLog()
      // 这里不写 report.html：录屏要等浏览器关闭才落盘，回看页必须在那之后生成（report.finish()）。
      this.log(`现场目录：${this.run.dir}`)
      this.log(`回看页：${this.run.dir}/report.html（收尾时生成）`)
    }
    return this.failed > 0 ? 1 : 0
  }

  /**
   * 把已经打印过的输出落到 run.log。
   *
   * `summary()` 与 `finish()` 各写一次：转码结论是在 `finish()` 里才产生的，如果只在
   * `summary()` 写，它就永远进不了 run.log——而这个文件的承诺是「控制台输出全文」，
   * 转码失败的原因（保留 webm + 怎么装 ffmpeg）恰恰是最需要留档的那几句。
   */
  writeRunLog() {
    if (this.run === undefined) return
    this.run.writeText('run.log', `${this.lines.join('\n')}\n`)
  }

  /**
   * 收尾：把录屏转成 mp4，再生成回看页（需要浏览器已关闭，录屏才会落盘）。
   * @returns {string | undefined} report.html 的路径。
   */
  finish() {
    if (this.run === undefined) return undefined
    // 转码必须排在 writeGallery() 之前：回看页会把 video/ 目录里当时的文件全嵌进去。
    this.logVideoOutcome(finalizeVideos(this.run.videoDir, { reviewSlowdown: this.reviewSlowdown }))
    const gallery = this.run.writeGallery({
      passed: this.passed,
      failed: this.failed,
      skipped: this.skipped,
      failures: this.failures,
    })
    // 没有录屏是最容易踩的静默失败：要么 openPage 忘了传 videoDir，要么 finish() 抢在
    // browser.close() 之前调用（录屏在上下文关闭时才落盘）。这里直接说清两种可能。
    if (this.run.videoFiles().length === 0) {
      this.log('   ！本次没有录屏文件：检查 openPage 是否传了 videoDir: run.videoDir，')
      this.log('     以及 report.finish() 是否在 browser.close() 之后才调用（录屏那时才落盘）')
    }
    // 再写一次 run.log：把转码结论与上面这条警告也收进去。
    this.writeRunLog()
    return gallery
  }

  /**
   * 报告录屏转码的结果。
   * @param {ReturnType<typeof finalizeVideos>} outcome - lib/video.mjs 的转码结果。
   */
  logVideoOutcome(outcome) {
    if (outcome.converted.length > 0) {
      const pace = outcome.slowdown > 1 ? `，回看慢放 ${outcome.slowdown} 倍` : ''
      this.log(`   · 录屏转码：${outcome.converted.length} 段 webm → mp4（H.264 / yuv420p / faststart${pace}）`)
    }
    if (outcome.kept.length > 0) {
      this.log('   ！以下录屏没能转成 mp4，保留为 webm 原件——QuickTime、Office、手机相册可能打不开：')
      for (const { file, reason } of outcome.kept) this.log(`     ${file} —— ${reason}`)
      for (const line of outcome.hint ?? []) this.log(`     ${line}`)
    }
  }
}