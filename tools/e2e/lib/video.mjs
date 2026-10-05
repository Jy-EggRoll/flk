/**
 * 录屏转码层：把 Playwright 录下的 webm 变成「到哪儿都能放」的 mp4。
 *
 * 为什么必须转：Playwright 的 `recordVideo` 只能产出 webm（VP8）。webm 在浏览器里能放，
 * 出了浏览器就处处碰壁——QuickTime 打不开、PowerPoint/Keynote 插不进去、手机相册不认、
 * 不少剪辑软件也不收。H.264 的 mp4 是唯一一个从网页到手机到 Office 都被认的组合。
 *
 * 兼容性靠这几个参数保住，缺一个就会在某一类播放器上失败（都是本机实测出来的）：
 *
 *   1. `-pix_fmt yuv420p`：作为显式声明留着。实测 Playwright 的 VP8 录屏本来就是 yuv420p，
 *      不指定也会保持；但输入一旦不是 4:2:0（RGB、带 alpha 的 VP9 等），libx264 会挑
 *      yuv444p / High 4:4:4 Predictive，而硬件解码器基本只认 4:2:0，QuickTime / Windows
 *      上会黑屏或拒播。
 *   2. `crop=trunc(iw/2)*2:trunc(ih/2)*2`：yuv420p 要求宽高都是偶数，而录屏尺寸等于视口
 *      尺寸，视口给奇数就转不出来。实测 1281x721 时不处理会直接失败：
 *      `width not divisible by 2 (1281x721)` + `Error while opening encoder`。
 *      这里用 crop 而不是 scale——scale 会**重采样整帧**把界面文字糊掉：同一素材下
 *      scale 版与 crop 版相差 PSNR 29.68dB（约等于整帧模糊一遍），而 crop 只是切掉多出来的
 *      那一行/一列像素。视口宽高本来就是偶数时 crop 是空操作（实测两种写法的输出完全一致，
 *      PSNR 无穷大），所以这条对最常见的偶数视口零成本。
 *   3. `setsar=1`：把像素宽高比显式设为 1:1。实测 crop 本身已经保持 1:1（加不加输出都一样），
 *      这一条是为输出契约留的保障：不依赖上游输入恰好奇形怪状，也防着以后换滤镜时悄悄变成非方形
 *      像素——早先那版 `scale` 写法就会把 SAR 变成 1647:1648 这类值，播放器会照着它拉伸画面。
 *   4. `-movflags +faststart`：把 moov 挪到文件头。不加则 moov 在文件尾，边下边播要等
 *      整个文件下载完才出画面。实测 box 顺序从 `ftyp/mdat/moov` 变成 `ftyp/moov/mdat`。
 *   5. `-profile:v main`：它是 high 的严格子集——能用 high 的解码器都能用 main，反过来不一定，
 *      所以取 main 是拿一点体积换更宽的兼容面。对界面这种内容，少几个编码工具带来的体积差
 *      可以忽略。不写 level，让 x264 按分辨率自己定。
 *
 * 固定帧率用 `fps=30` 滤镜，而不是输出侧的 `-r 30`：VFR 的 mp4 在部分播放器与剪辑软件里
 * 时间轴会算错，而用输出侧参数就得按版本二选一——ffmpeg 4.x 只有 `-vsync cfr`、5.1 起换成
 * `-fps_mode cfr`、9.0 又**删掉了** `-vsync`（实测报 `Unrecognized option 'vsync'`）。
 * `fps` 滤镜跨版本可用，代码也少一段探测。
 * 需要说明的是：实测本机 Chromium 交给 Playwright 的录屏本身就是 25fps 恒定帧率
 * （151 帧 / 6.04s，帧间隔恒为 0.040s），并不稀疏；对这种输入 `fps=30` 与 `-r 30`
 * 输出的时长与帧数完全一致（都是 6.033s / 181 帧）。
 *
 * 找不到 ffmpeg 时**不报错**，保留 webm 并把结论带回去：验证结果本身不该因为缺一个转码器
 * 就作废，但必须说清楚「这段视频兼容性不好」以及怎么装。
 *
 * 还有一条实测出来的问题，跟转码无关但会毁掉转码：**`browser.close()` 返回时录屏还没写完。**
 * 那一刻 webm 已经存在，但大小是 0 字节，约 150ms 后才一次性写完（实测 0B → 7209B）。
 * 回看页只引用路径，所以侥幸一直没暴露；转码要读内容，读到 0 字节就会产出一个空视频。
 * 因此这里**不论转不转码都先等文件落定**（见 {@link waitUntilWritten}）：降级保留 webm 时
 * 同样需要一个完整的 webm，否则现场里留下的是个永远打不开的坏文件。
 */

import { execFileSync } from 'node:child_process'
import { existsSync, readdirSync, rmSync, statSync } from 'node:fs'
import { homedir } from 'node:os'
import { basename, join } from 'node:path'
import { which } from './toolchain.mjs'

/** 显式指定 ffmpeg 可执行文件的环境变量。 */
const OVERRIDE_VAR = 'BROWSER_VERIFY_FFMPEG'

/** 缺失 ffmpeg 时给出的安装办法，按平台给最省事的那条。 */
const INSTALL_HINT = [
  `装一个 ffmpeg 即可（任选）：`,
  `  mise:  mise use -g ffmpeg        或        brew:  brew install ffmpeg`,
  `  Debian/Ubuntu:  sudo apt install ffmpeg`,
  `也可以用 ${OVERRIDE_VAR}=/path/to/ffmpeg 指定现成的可执行文件。`,
]

/** 环境变量指错了路径时给的提示：该改的是变量，不是再装一个 ffmpeg。 */
const OVERRIDE_HINT = [
  `改掉这个环境变量，或者 unset 它让工具自己去 PATH / mise / Homebrew 里找：`,
  `  unset ${OVERRIDE_VAR}`,
]

/** 一段录屏转码所需的时间上限：不设上限的话 ffmpeg 卡住会连带整个验证脚本一起挂住。 */
const TRANSCODE_TIMEOUT_MS = 10 * 60 * 1000

/**
 * 回看时的默认慢放倍数。
 *
 * 验证是跑给人以外的机器看的，跑得越快越好；但同一段录屏事后要拿给人复审，原速下
 * 每一步只占几百毫秒，眨眼就过去了。所以默认把成品拉长到这个倍数——跑不加时间，只有
 * 看的时候变慢。想要原速就传 `reviewSlowdown: 1`。
 */
const DEFAULT_REVIEW_SLOWDOWN = 3

/** 等录屏写完的上限；超过就认为这次录制真的没产出内容。 */
const VIDEO_WRITE_TIMEOUT_MS = 5000

/** 轮询录屏写入进度的间隔。 */
const VIDEO_WRITE_POLL_MS = 50

/**
 * ffmpeg 在真正报错之前会先吐一些噪声行，它们不是病因。
 * 实测：一个 17 字节的垃圾 webm 的首行是 `Truncating packet of size 13344 to 13`
 * （13344 是它误解析出来的长度），拿这句当原因只会把人带偏。
 */
const NOISE_LINE = /^(Truncating packet|Duplicate element|Last message repeated)/i

/** 像是「根因」的行。找不到根因时才退回第一行。 */
const ROOT_CAUSE_LINE = /\b(Error|Invalid|Unrecognized|No such|not found)\b/i

/**
 * 定位一个可用的 ffmpeg。
 *
 * 顺序：显式指定 → PATH → mise / Homebrew / 系统的常见安装位置。
 * 与 Chromium 不同，ffmpeg 缺失是**可以接受的**，所以这里从不抛错：找不到就返回 `problem`，
 * 由调用方降级成「保留 webm + 说清怎么装」。显式指定却指错了也一样——一个环境变量笔误
 * 不该让整次验证连同回看页一起作废，但必须**点名**说清是哪个路径不存在。
 * @returns {{ path: string, source: string } | { path: undefined, problem: string }}
 *   找到时前者；找不到时后者，`problem` 是一句可以直接打印给人看的说明。
 */
export function resolveFfmpeg() {
  const override = process.env[OVERRIDE_VAR]
  if (override !== undefined && override !== '') {
    if (existsSync(override)) return { path: override, source: OVERRIDE_VAR }
    return { path: undefined, problem: `${OVERRIDE_VAR} 指向的文件不存在：${override}` }
  }

  const onPath = which('ffmpeg')
  if (onPath !== undefined) return { path: onPath, source: 'PATH 上的 ffmpeg' }

  for (const candidate of fallbackCandidates()) {
    if (existsSync(candidate.path)) return candidate
  }
  return { path: undefined, problem: '没有可用的 ffmpeg' }
}

/**
 * PATH 之外的常见安装位置。
 *
 * 为什么需要：非登录 shell 里 mise 的 shims 可能不在 PATH 上，而这类工具恰恰常常是
 * 通过 mise / Homebrew 装的。
 * @returns {{ path: string, source: string }[]} 候选列表。
 */
function fallbackCandidates() {
  const candidates = []
  const miseShims = join(homedir(), '.local', 'share', 'mise', 'shims', 'ffmpeg')
  candidates.push({ path: miseShims, source: 'mise shims' })

  const miseInstalls = join(homedir(), '.local', 'share', 'mise', 'installs', 'ffmpeg')
  if (existsSync(miseInstalls)) {
    // 按版本号倒序（numeric 比较，否则 "10.0" 会被字典序排到 "9.0.2" 后面），优先试最新的。
    const versions = readdirSync(miseInstalls).sort((left, right) => right.localeCompare(left, undefined, { numeric: true }))
    for (const version of versions) {
      for (const layout of ['.mise-bins', 'bin']) {
        candidates.push({
          path: join(miseInstalls, version, layout, 'ffmpeg'),
          source: `mise 安装的 ffmpeg ${version}`,
        })
      }
    }
  }

  candidates.push({ path: '/opt/homebrew/bin/ffmpeg', source: 'Homebrew（Apple Silicon）' })
  candidates.push({ path: '/usr/local/bin/ffmpeg', source: 'Homebrew / 手动安装' })
  candidates.push({ path: '/usr/bin/ffmpeg', source: '系统包管理器' })
  return candidates
}

/**
 * 把录屏目录里的 webm 全部转成 mp4。
 *
 * 转码成功即删掉原始 webm（回看页里只留一份能普遍播放的成品）。任何一段转码失败都只影响
 * 它自己：原始文件保留、半成品删掉，其余照常。
 *
 * 无论有没有 ffmpeg，**第一件事都是等录屏落定**。这一步不能只放在转码分支里：缺 ffmpeg 时
 * 会走降级（保留 webm），而如果那时文件还是 0 字节，留下的是一个永远打不开的坏文件——
 * 尤其在 `finish()` 之后立刻 `process.exit()` 的场景（CI 收尾、被信号打断）会被永久固化。
 * @param {string} videoDir - 录屏目录。
 * @param {{ reviewSlowdown?: number }} [options] - `reviewSlowdown`：回看慢放倍数，默认
 *   {@link DEFAULT_REVIEW_SLOWDOWN}，传 1 表示原速。
 * @returns {{ converted: { file: string, output: string }[], kept: { file: string, reason: string }[], ffmpeg: { path: string, source: string } | undefined, hint: string[] | undefined, slowdown: number }}
*   `converted`：已转成 mp4 的；`kept`：仍以 webm 保留的（含原因）；`ffmpeg`：缺失时为 undefined；
 *   `slowdown`：本次实际用的慢放倍数（降级时也要让调用方说得出成品是什么节奏）。
 */
export function finalizeVideos(videoDir, options = {}) {
  const slowdown = options.reviewSlowdown ?? DEFAULT_REVIEW_SLOWDOWN
  const result = { converted: [], kept: [], ffmpeg: undefined, hint: undefined, slowdown }
  const sources = listWebm(videoDir)
  if (sources.length === 0) return result

  const ready = sources.map((file) => ({ file, path: join(videoDir, file) }))
  const sizes = waitUntilWritten(ready.map(({ path }) => path))

  const ffmpeg = resolveFfmpeg()
  if (ffmpeg.path === undefined) {
    result.kept = ready.map(({ file, path }) => ({
      file,
      reason: sizes.get(path) === 0 ? '录屏文件是空的（这次录制没写成）' : ffmpeg.problem,
    }))
    // 环境变量写错时，该改的是那个变量，不是再去装一个 ffmpeg——建议要对得上病因。
    result.hint = ffmpeg.problem.includes(OVERRIDE_VAR) ? OVERRIDE_HINT : INSTALL_HINT
    return result
  }
  result.ffmpeg = { path: ffmpeg.path, source: ffmpeg.source }

  for (const { file, path: input } of ready) {
    const size = sizes.get(input)
    const output = join(videoDir, `${basename(file, '.webm')}.mp4`)
    try {
      if (size === 0) throw new Error('录屏文件是空的（这次录制没写成）')
      execFileSync(ffmpeg.path, transcodeArguments(input, output, slowdown), {
        encoding: 'utf8',
        stdio: ['ignore', 'ignore', 'pipe'],
        timeout: TRANSCODE_TIMEOUT_MS,
      })
      if (!existsSync(output) || statSync(output).size === 0) throw new Error('转码没有产出可用的文件')
      rmSync(input)
      result.converted.push({ file, output: basename(output) })
    } catch (error) {
      // 半成品必须删掉：留着会让回看页嵌一个打不开的文件，比没有更误导人。
      rmSync(output, { force: true })
      result.kept.push({ file, reason: explain(error) })
    }
  }
  return result
}

/**
 * 列出录屏目录里的 webm。
 * @param {string} videoDir - 录屏目录。
 * @returns {string[]} 文件名（按名称排序）。
 */
function listWebm(videoDir) {
  if (!existsSync(videoDir)) return []
  return readdirSync(videoDir)
    .filter((name) => name.toLowerCase().endsWith('.webm'))
    .sort()
}

/**
 * 等这批录屏文件写完，返回每个文件落定后的大小。
 *
 * 为什么需要：`browser.close()` 返回时 webm 才刚被创建、内容是空的（实测 0 字节），
 * 约 150ms 后才一次性写完。判据是「大小非 0 且连续两次读数不变」——Playwright 是一次性
 * 写完的，所以这个判据既不会误判也不会久等。超时仍未写好就返回当时的大小，
 * 由调用方当成「这次录制没产出内容」处理。
 *
 * 一批文件共用一个截止时间，而不是逐个等：正常情况下代价都微不足道，但真有录屏没写成时，
 * 逐个等是「每个文件 5 秒」（实测 3 个空文件要 15 秒），一起等则是总共 5 秒。
 * @param {string[]} paths - 录屏文件路径。
 * @returns {Map<string, number>} 路径 → 落定后的字节数（0 表示没有内容）。
 */
function waitUntilWritten(paths) {
  let previous = new Map(paths.map((path) => [path, sizeOf(path)]))
  const pending = new Set(paths)
  const deadline = Date.now() + VIDEO_WRITE_TIMEOUT_MS
  while (pending.size > 0 && Date.now() < deadline) {
    sleepSync(VIDEO_WRITE_POLL_MS)
    const current = new Map(paths.map((path) => [path, sizeOf(path)]))
    for (const path of pending) {
      if (current.get(path) > 0 && current.get(path) === previous.get(path)) pending.delete(path)
    }
    previous = current
  }
  return previous
}

/**
 * 文件大小；文件不存在时按 0 算。
 * @param {string} path - 文件路径。
 * @returns {number} 字节数。
 */
function sizeOf(path) {
  return existsSync(path) ? statSync(path).size : 0
}

/**
 * 同步等待若干毫秒。
 *
 * `finish()` 是对外保持同步的（调用方无需改成 await），所以这里不能用 `setTimeout`
 * 那套 promise；`Atomics.wait` 是 Node 里同步阻塞的标准做法，也不用去 spawn 一个 sleep 进程。
 * @param {number} ms - 毫秒。
 */
function sleepSync(ms) {
  Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, ms)
}

/**
 * 拼出滤镜链。
 *
 * 关键在顺序：`setpts` 拉长时间轴必须在 `fps` 之前。反过来先拍成 30fps 再拉长，输出就变成
 * 15fps 的「低帧率慢放」，画面一顿一顿；先拉长再拍，fps 会把每一帧补成两帧，得到的是
 * 30fps 的平滑慢放。
 *
 * 为什么慢放放在转码这步做、而不是用 Playwright 的 `slowMo`：`slowMo` 是**真的把验证跑慢**
 * （每个动作都等），而回看是事后才发生的事。放在这里，跑的时候该多快就多快，慢只慢在成品上，
 * 零额外耗时。
 * @param {number} slowdown - 回看慢放倍数（1 = 原速，不加这一级滤镜）。
 * @returns {string} 滤镜链。
 */
function videoFilters(slowdown) {
  return ['crop=trunc(iw/2)*2:trunc(ih/2)*2', 'setsar=1', ...(slowdown === 1 ? [] : [`setpts=${slowdown}*PTS`]), 'fps=30'].join(',')
}

/**
 * 拼出转码参数。
 * @param {string} input - 输入 webm。
 * @param {string} output - 输出 mp4。
 * @param {number} slowdown - 回看慢放倍数（1 = 原速）。
 * @returns {string[]} 参数数组（不经过 shell，无需转义）。
 */
function transcodeArguments(input, output, slowdown) {
  return [
    '-hide_banner',
    '-loglevel',
    'error',
    // 不要从 stdin 读任何东西：被脚本调用时那可能是一个终端或空管道。
    '-nostdin',
    '-y',
    '-i',
    input,
    '-map',
    '0:v:0',
    '-c:v',
    'libx264',
    '-profile:v',
    'main',
    '-pix_fmt',
    'yuv420p',
    // crop 掉奇数尺寸多出来的那 1 行/列（不重采样），钉成方形像素；
    // 再按回看倍数把时间轴拉长；最后拍成固定 30fps。
    '-vf',
    videoFilters(slowdown),
    '-crf',
    '20',
    '-preset',
    'medium',
    '-movflags',
    '+faststart',
    // Playwright 的录屏没有音轨；显式关掉，免得以后换录制方式时悄悄带进一条静音轨。
    '-an',
    output,
  ]
}

/**
 * 把 ffmpeg 的报错压成一行能读的说明。
 *
 * 挑「像根因」的那一行，而不是简单地取首行或末行——两头都实测出现过：
 * 取末行时，用 Playwright 自带的那个不含 libx264 的 ffmpeg 会显示
 * `Error splitting the argument list: Option not found`（病因 `Unrecognized option 'preset'`
 * 在上一行）；取首行时，垃圾 webm 会显示 `Truncating packet of size 13344 to 13`（纯噪声，
 * 病因是后面的 `Invalid data found`）。所以先滤掉噪声行，再找含 Error/Invalid 的那一行，
 * 都没有才退回第一行。
 * @param {unknown} error - execFileSync 抛出的错误。
 * @returns {string} 一行说明。
 */
function explain(error) {
  const stderr = typeof error?.stderr === 'string' ? error.stderr : ''
  const lines = stderr
    .split('\n')
    // ffmpeg 的错误行常带 `[libx264 @ 0x55ab…]` 这样的组件前缀，那是个内存地址，对人没用。
    .map((line) => line.trim().replace(/^\[[^\]]*\]\s*/, ''))
    .filter((line) => line !== '' && !NOISE_LINE.test(line))
  const message = lines.find((line) => ROOT_CAUSE_LINE.test(line)) ?? lines[0] ?? error?.message ?? String(error)
  // 真出了长报错时只留能定位问题的那一句。
  return message.length > 200 ? `${message.slice(0, 200)}…` : message
}