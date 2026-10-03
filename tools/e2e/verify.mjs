#!/usr/bin/env node
/**
 * flk serve 的 WebUI（cmd/ui/config.html）真实浏览器 E2E 验证脚本
 *
 * 契约来源：《flk serve 一体化改造》规格第 4 节（B1–B13）；规格文档本身没有入库，
 * 断言编号与规格逐条对应，改断言时要连同下面的英文残留清单一起维护
 * 复用能力：vendor 自 browser-verify 技能的 lib/（Playwright + 本机 Chromium），
 * 来源与同步方式见同目录 README.md
 *
 * 设计要点（都是实测踩过或推演出的坑，写在这里避免以后重复踩）：
 *   1. 被测二进制由 --binary 指定，脚本本身不关心是旧版还是新版，同一份脚本两版都跑
 *   2. 端口用 node:net listen(0) 由系统分配，再释放后交给被测进程；被测侧本身还有
 *      「端口被占则 +1 顺延」逻辑，所以即便有极小概率的竞态也不会撞车
 *   3. store 文件里的路径一律指向本次运行的临时目录，且真实创建了文件/符号链接/硬链接，
 *      让 /api/check 能同时产出「有效」与「无效」两种真实状态，而不是一堆假路径
 *   4. `--lang zh-CN` 下被测进程的启动行是中文（服务已启动：http://<host>:N/?token=...）。
 *      解析只认「http(s)://host:port」这个形状，不认固定主机名：地址里的主机名由 webui 按
 *      实际绑定地址生成（默认 127.0.0.1，--host 换成别的就跟着变），写死 localhost 会解析不到。
 *      导航一律走 127.0.0.1（回环地址在白名单内），token 从同一行的 `?token=` 里取出。
 *      这一条是硬要求：webui 的 tokenGate 保护 "/" 与 "/api/" 前缀，地址与请求不带 token 时
 *      页面是 401 白屏、断言只会看到「元素找不到」，因此取不到 token 必须当场报错而不是继续跑
 *   5. 旧实现的 watchStoreFile 轮询里 lastModTime 是零值，服务起来约 1 秒后必然触发
 *      一次「假变更」SSE + 重载。脚本在开页面之前先等过这一跳，避免它污染 B5/B6 的时序
 *   6. 每条断言前都从 pristine 测试数据重置 store（外部写盘 + 轮询确认服务端已读回内存），
 *      再整页重新加载，保证条目之间互不污染；重置必须轮询确认，否则 GET 可能拿到旧数据
 *   7. 断言失败信息一律带实际值（元素个数 / 真实文本 / 真实类名），不留「断言失败」四个字
 *   8. 所有产物只写到 --root 指定的目录（Taskfile 默认指向 build/e2e，已被 .gitignore 忽略），
 *      绝不写进仓库；脚本内不含任何仓库外的绝对路径，换机器、换目录都能跑
 *   9. 「保存窗口内继续编辑」这类时序断言（X12）不靠 sleep 造窗口：用 page.route 拦住
 *      POST /api/config 的响应，先 route.fetch() 把请求真正发出去（服务端此刻就写盘），
 *      再用闸门卡住 fulfil，等窗口内的输入做完才放行。固定 sleep 造窗口会与 slowMo
 *      （每个动作 +350ms）抢时间，窗口长度不可控，必然 flaky
 *
 * 用法（--binary 与产物目录 --root 都是必填，缺了直接报参数错误并以退出码 2 结束）：
 *   node verify.mjs --binary=build/flk --root=build/e2e
 *   node verify.mjs --binary=build/flk --root=../e2e-out --label=old    # 换一支二进制/换个现场目录对比
 *   node verify.mjs --binary=... --root=... --fast        # 关掉 slowMo（跑得快，录屏快得看不清）
 *   node verify.mjs --binary=... --root=... --only=B1,B2  # 只跑指定编号
 *   node verify.mjs --binary=... --root=... --headful     # 显示浏览器窗口，便于盯着看
 */
import { spawn } from 'node:child_process'
import { createServer } from 'node:net'
import {
  existsSync,
  linkSync,
  lstatSync,
  mkdirSync,
  readFileSync,
  rmSync,
  statSync,
  symlinkSync,
  writeFileSync,
} from 'node:fs'
import { join, resolve } from 'node:path'

// lib/ 是 browser-verify 技能的 vendor 副本（同目录 README.md 记了来源与同步方法），
// 用相对路径 import：脚本跟着仓库走，不再依赖任何人机器上的技能目录
import { launchBrowser, openPage, resolveChromium } from './lib/browser.mjs'
import { createRun } from './lib/artifacts.mjs'
import { createReport } from './lib/report.mjs'

/* ---------- 常量 ---------- */

/** 目标条目所在的平台/设备/类型；平台键必须是 linux，因为 /api/check 只对 runtime.GOOS 出结果 */
const PLAT = 'linux'
const DEV = 'laptop'
const TYPE = 'symlink'

/** 各链接类型的字段顺序（与 cmd/ui/config.html 的 TYPE_FIELDS 一致，用于旧 UI 的无 key 文本定位） */
const TYPE_FIELDS = {
  symlink: ['real', 'fake'],
  hardlink: ['prim', 'seco'],
  copy: ['src', 'dst'],
}

/** 外部改写时写入的独特标记，用来判断页面是否真的拿到了外部内容 */
const EXTERNAL_MARKER = 'z-external-real.txt'
/** B7/B9 里敲进输入框的未保存内容，必须足够独特，避免和测试数据里的真实路径混淆 */
const UNSAVED_MARKER = 'MY-UNSAVED-EDIT-VALUE'

/**
 * SSE 连通性探针写入的值
 *
 * 为什么需要它：旧实现（git show HEAD:cmd/ui/config.html 的 init()）是 `setTimeout(connectSSE, 2000)`，
 * 即页面加载 2 秒后才建立 EventSource，而这 2 秒内发生的文件变更事件没有任何监听者、永久丢失；
 * 且旧 `#statusText` 的「已连接」是 loadConfig() 结尾自己 setOnline(true) 设上的假信号，
 * 不能用来代表 SSE 真的连通。实测后果：同一份脚本「只跑 B6」通过、全量跑到第 6 条时失败，
 * 因为外部改写恰好落在 2s 窗口内。因此凡是「外部改文件 → 页面应自动响应」的断言，
 * 都必须先确认页面的 SSE 真的连通，否则断言本身是赛跑（flaky）
 */
const PROBE_MARKER = 'zz-probe-sse-live.txt'

/** 中文模式下不允许出现的英文源串（硬约束见 spec 第 5 节） */
const ENGLISH_RESIDUE = [
  'Edit',
  'Delete',
  'Actions',
  'Status',
  'Re-check',
  'Save',
  'Discard changes',
  'Pending re-check',
  'Unsaved changes',
  'Keep my changes',
  'Migrate',
  'Platform',
  'Device',
  'Link type',
  'No entries',
  '+ Add entry',
  'Valid',
  'Invalid',
  'Cancel',
  'Loading',
  'Save failed',
  'Saved successfully',
]

/* ---------- 参数 ---------- */

function parseArgs(argv) {
  const options = { binary: undefined, root: undefined, label: 'local', fast: false, headful: false, only: undefined }
  for (const arg of argv) {
    if (arg === '--fast') options.fast = true
    else if (arg === '--headful') options.headful = true
    else if (arg.startsWith('--binary=')) options.binary = arg.slice('--binary='.length)
    else if (arg.startsWith('--root=')) options.root = arg.slice('--root='.length)
    else if (arg.startsWith('--label=')) options.label = arg.slice('--label='.length)
    else if (arg.startsWith('--only=')) {
      options.only = arg
        .slice('--only='.length)
        .split(',')
        .map((s) => s.trim().toUpperCase())
        .filter(Boolean)
    }
  }
  // 必填参数缺失时说清缺的是哪个、该怎么给，并以 2 退出：与「断言失败」的 1 分开，
  // 免得把「压根没跑起来」误读成「跑过但没通过」
  for (const [name, value, hint] of [
    ['--binary', options.binary, '被测 flk 可执行文件的路径'],
    ['--root', options.root, '本次运行的产物目录（现场 runs/ 与 work/ 都落在它下面）'],
  ]) {
    if (value === undefined || value === '') {
      console.error(`参数错误：缺少 ${name}=<${hint}>`)
      console.error(
        '用法：node verify.mjs --binary=<flk 可执行文件> --root=<产物目录> [--label=名字] [--only=B1,B2] [--fast] [--headful]',
      )
      process.exit(2)
    }
  }
  return options
}

/* ---------- 小工具 ---------- */

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

/** 断言：条件为假就抛错，错误信息必须带实际值 */
function expect(condition, message) {
  if (!condition) throw new Error(message)
}

function escapeRegExp(text) {
  return text.replaceAll(/[.*+?^${}()|[\]\\]/g, '\\$&')
}

/** 取一个当前空闲的端口：监听 0 拿到系统分配值，随即释放 */
async function pickFreePort() {
  return new Promise((resolve, reject) => {
    const server = createServer()
    server.once('error', reject)
    server.listen(0, '127.0.0.1', () => {
      const { port } = server.address()
      server.close(() => resolve(port))
    })
  })
}

/** 轮询直到 predicate 为真；超时抛错并带上最后一次观察值 */
async function waitUntil(predicate, timeoutMs, description, intervalMs = 200) {
  const deadline = Date.now() + timeoutMs
  let last
  for (;;) {
    last = await predicate()
    if (last === true || (last !== false && last !== undefined && last !== null)) return last
    if (Date.now() > deadline) {
      throw new Error(`${description}（等待 ${timeoutMs}ms 超时；最后一次观察值：${JSON.stringify(last)}）`)
    }
    await sleep(intervalMs)
  }
}

/* ---------- 测试数据：真实文件 + 三种链接类型 + 多平台多设备 ---------- */

/**
 * 在临时目录里造出一份完全真实的 store 测试数据
 *
 * 「真实」的含义：symlink 条目造出真的符号链接、hardlink 条目造出真的硬链接、
 * copy 条目造出真的两个文件；同时每个类型都留一条路径不存在，这样 /api/check
 * 会同时返回有效与无效，界面上的状态徽标才有两种真实状态可看
 * 条目顺序刻意按 Go 的排序规则（entrySortValues 即「值升序后逐位比较」）排好，
 * 避免保存后服务端重新排序导致「第 0 行」在断言之间漂移
 */
function buildFixture(dataDir) {
  mkdirSync(dataDir, { recursive: true })
  const path = (name) => join(dataDir, name)
  const makeFile = (name, content) => {
    writeFileSync(path(name), content)
    return path(name)
  }
  const makeSymlink = (target, name) => {
    rmSync(path(name), { force: true })
    // 目标必须用绝对路径：符号链接的**相对**目标是相对「链接自身所在目录」解析的，
    // 而这里的 target 是相对仓库根（--root 传相对路径时 dataDir 就是相对的）拼出来的，
    // 直接用会让目标被拼接两次（<dataDir>/<dataDir>/a-real.txt）而指向不存在的路径——
    // 于是测试数据里那条「本应有效」的符号链接记录实际是无效的，与测试数据自己的意图（有效/无效各一条）不符
    symlinkSync(resolve(target), path(name))
    return path(name)
  }
  const makeHardlink = (target, name) => {
    rmSync(path(name), { force: true })
    linkSync(target, path(name))
    return path(name)
  }

  // linux/laptop：每类型两条（一条有效一条无效），条目顺序已按排序规则排好
  const aReal = makeFile('a-real.txt', 'a real file\n')
  makeSymlink(aReal, 'a-link')
  const aPrim = makeFile('a-prim.txt', 'a primary file\n')
  makeHardlink(aPrim, 'a-seco.txt')
  const aSrc = makeFile('a-src.txt', 'a source file\n')
  makeFile('a-dst.txt', 'a destination file\n')
  const bReal = makeFile('b-real.txt', 'b real file\n')
  const bPrim = makeFile('b-prim.txt', 'b primary file\n')
  const bSrc = makeFile('b-src.txt', 'b source file\n')

  // linux/desktop：另一台设备
  const dReal = makeFile('d-real.txt', 'd real file\n')
  makeSymlink(dReal, 'd-link')
  const dPrim = makeFile('d-prim.txt', 'd primary file\n')
  makeHardlink(dPrim, 'd-seco.txt')

  // darwin/macbook：另一个平台（前端不展示其状态徽标，但应能正常渲染条目）
  const mReal = makeFile('m-real.txt', 'm real file\n')
  makeSymlink(mReal, 'm-link')
  makeFile('m-src.txt', 'm source file\n')
  makeFile('m-dst.txt', 'm destination file\n')

  // windows/workstation：第三个平台
  makeFile('w-prim.txt', 'w primary file\n')
  makeFile('w-seco.txt', 'w secondary file\n')
  makeFile('w-src.txt', 'w source file\n')
  makeFile('w-dst.txt', 'w destination file\n')

  return {
    linux: {
      laptop: {
        symlink: [
          { real: aReal, fake: path('a-link') },
          { real: bReal, fake: path('b-link-missing') },
        ],
        hardlink: [
          { prim: aPrim, seco: path('a-seco.txt') },
          { prim: bPrim, seco: path('b-seco-missing.txt') },
        ],
        copy: [
          { src: aSrc, dst: path('a-dst.txt') },
          { src: bSrc, dst: path('b-dst-missing.txt') },
        ],
      },
      desktop: {
        symlink: [{ real: dReal, fake: path('d-link') }],
        hardlink: [{ prim: dPrim, seco: path('d-seco.txt') }],
      },
    },
    darwin: {
      macbook: {
        symlink: [{ real: mReal, fake: path('m-link') }],
        copy: [
          { src: path('m-src.txt'), dst: path('m-dst.txt') },
        ],
      },
    },
    windows: {
      workstation: {
        hardlink: [{ prim: path('w-prim.txt'), seco: path('w-seco.txt') }],
        copy: [{ src: path('w-src.txt'), dst: path('w-dst.txt') }],
      },
    },
  }
}

/* ---------- store 内容比对 ---------- */

/**
 * 把 store 内容规范化成可精确比较的字符串
 *
 * 为什么不能用子串判断「文件是否已回到 pristine」：测试数据里 a 行的值是 `a-real.txt`，
 * 而保存过之后会变成 `a-real.txt.saved`——`a-real.txt` 恰好是它的前缀，
 * 于是「包含 a-real.txt」这种判断会在**服务端还没读回新内容**时也对陈旧数据成立，
 * 重置提前返回、页面读到上一条断言留下的旧值（B5 就是这么被自己坑失败的）
 *
 * 规范化规则：对象键全部排序；每个 plat/dev/type 下的条目列表也排序后再比较，
 * 这样既不受 Go 侧 map 键序影响，也不受 sortEntrySlice 造成的条目顺序影响
 */
function canonicalStore(data) {
  const result = {}
  for (const plat of Object.keys(data ?? {}).sort()) {
    result[plat] = {}
    for (const device of Object.keys(data[plat] ?? {}).sort()) {
      result[plat][device] = {}
      for (const type of Object.keys(data[plat][device] ?? {}).sort()) {
        result[plat][device][type] = (data[plat][device][type] ?? [])
          .map((entry) => {
            const sorted = {}
            for (const key of Object.keys(entry ?? {}).sort()) sorted[key] = entry[key]
            return JSON.stringify(sorted)
          })
          .sort()
      }
    }
  }
  return JSON.stringify(result)
}

/* ---------- 页面侧通用读取 ---------- */

/**
 * 读表格每一行的字段值，新旧两版 UI 都能读
 * 新版是 input.cell-input（带 data-key，可直接按 key 取），旧版是 .cell-text（只有文本顺序）
 */
async function readRows(page) {
  return page.$$eval('tbody tr', (trs) =>
    trs.map((tr) => {
      const inputs = Array.from(tr.querySelectorAll('input.cell-input'))
      if (inputs.length > 0) {
        const byKey = {}
        for (const input of inputs) byKey[input.dataset.key] = input.value
        return byKey
      }
      return { __cells: Array.from(tr.querySelectorAll('td .cell-text')).map((el) => el.textContent) }
    }),
  )
}

/** 按字段值找行下标；新版按 key 取，旧版按字段顺序取，找不到返回 -1 */
async function findRowIndexByField(page, fieldKey, value, rows) {
  const list = rows ?? (await readRows(page))
  const position = TYPE_FIELDS[TYPE].indexOf(fieldKey)
  for (let i = 0; i < list.length; i += 1) {
    const row = list[i]
    const actual = row.__cells === undefined ? row[fieldKey] : row.__cells[position]
    if (actual === value) return i
  }
  return -1
}

/** 当前表格里某字段的所有取值（用于「值是否出现」这种与行序无关的断言） */
async function fieldValues(page, fieldKey) {
  const rows = await readRows(page)
  const position = TYPE_FIELDS[TYPE].indexOf(fieldKey)
  return rows.map((row) => (row.__cells === undefined ? row[fieldKey] : row.__cells[position]))
}

/** 读 #dirtyCount 里的数字；元素不存在返回 undefined，文本无数字也返回 undefined */
async function dirtyCountOf(page) {
  const locator = page.locator('#dirtyCount')
  if ((await locator.count()) === 0) return undefined
  const text = (await locator.first().textContent()) ?? ''
  const matched = text.match(/\d+/)
  return matched === null ? undefined : Number(matched[0])
}

/** 读 #dirtyCount 的原始文本（错误信息里要带实际文案） */
async function dirtyTextOf(page) {
  const locator = page.locator('#dirtyCount')
  if ((await locator.count()) === 0) return '<#dirtyCount 不存在>'
  return (await locator.first().textContent()) ?? ''
}

/** 元素必须存在且可见，否则抛错并说明实际状态 */
async function requireVisible(page, selector, description) {
  const locator = page.locator(selector)
  const count = await locator.count()
  if (count === 0) {
    throw new Error(`期望${description}（${selector}）可见；实际：页面上不存在该元素（匹配数 0）`)
  }
  const visible = await locator.first().isVisible()
  if (!visible) {
    throw new Error(`期望${description}（${selector}）可见；实际：元素存在（匹配数 ${count}）但 isVisible=false`)
  }
  return locator.first()
}

/* ---------- 主流程 ---------- */

async function main() {
  const options = parseArgs(process.argv.slice(2))

  // 清理代理变量：被测服务只在本机回环上，Node 侧 fetch 若继承 http_proxy 会绕远路甚至失败
  // （lib/browser.mjs 的 launchBrowser 只清洗它自己拉起的 Chromium 子进程，管不到本进程）
  for (const key of Object.keys(process.env)) {
    if (/^(https?|all)_proxy$/i.test(key)) delete process.env[key]
  }

  // 前置检查：本机没有 Chromium 时，在起服务、开页面之前就把话讲清楚
  // 提示内容由 lib/browser.mjs 给出（它才知道自己会按什么顺序找浏览器），这里只负责把失败
  // 时机提前，并且不让一条 ENOENT 堆栈糊住使用者——退出码 2 与「断言失败」的 1 区分开
  try {
    resolveChromium()
  } catch (error) {
    console.error(`前置检查失败：${error.message}`)
    // 上面那份提示是通用写法（库不知道本仓库用 pnpm 管依赖），这里补一条能直接照抄的命令
    console.error('在 flk 仓库里，装浏览器用：cd tools/e2e && pnpm exec playwright-core install chromium')
    process.exit(2)
  }

  const stamp = new Date().toISOString().replaceAll(/[:.]/g, '-').slice(0, 19)
  const workDir = join(options.root, 'work', `${options.label}-${stamp}`)
  const dataDir = join(workDir, 'data')
  const storeFile = join(workDir, 'flk-store.json')

  const fixture = buildFixture(dataDir)
  const pristineText = JSON.stringify(fixture, null, 4)
  writeFileSync(storeFile, pristineText)
  const canonicalPristine = canonicalStore(fixture)

  // 外部改写版本：把目标条目的 real 换成独特标记，用来判断页面显示的是外部内容
  const externalFixture = buildFixture(dataDir) // 幂等重建，保证文件存在
  externalFixture[PLAT][DEV][TYPE][0].real = join(dataDir, EXTERNAL_MARKER)
  const externalText = JSON.stringify(externalFixture, null, 4)

  // SSE 连通探针版本：只改第 1 行的 real（不影响第 0 行），写入后页面若真的收到事件就会自动重载出这个值
  const probeFixture = buildFixture(dataDir)
  probeFixture[PLAT][DEV][TYPE][1].real = join(dataDir, PROBE_MARKER)
  const probeText = JSON.stringify(probeFixture, null, 4)

  const run = createRun({
    root: options.root,
    title: `flk serve 一体化 B1–B13（${options.label}）`,
  })
  const report = createReport({
    title: `flk serve 一体化 B1–B13（${options.label}）`,
    run,
  })

  report.note(`被测二进制：${options.binary}`)
  report.note(`二进制 mtime：${existsSync(options.binary) ? new Date(statSafe(options.binary)).toISOString() : '不存在'}`)
  report.note(`store 文件：${storeFile}`)
  report.note(`测试数据目录：${dataDir}`)
  report.note(`语言：zh-CN（固定，顺带验证中文文案无英文残留）`)
  report.note(`slowMo：${options.fast ? '关闭（--fast）' : '350ms（默认，便于录屏）'}`)
  if (options.only !== undefined) report.note(`只跑：${options.only.join(', ')}`)

  const port = await pickFreePort()
  const child = spawn(
    options.binary,
    ['--store-path', storeFile, 'serve', '--port', String(port), '--no-open', '--lang', 'zh-CN'],
    { stdio: ['ignore', 'pipe', 'pipe'], env: { ...process.env } },
  )
  const pid = child.pid
  report.note(`被测进程 pid=${pid}，命令行：${options.binary} --store-path ${storeFile} serve --port ${port} --no-open --lang zh-CN`)

  const stdoutChunks = []
  const stderrChunks = []
  let startupLineSeen = ''
  let base = ''
  let token = ''
  child.stdout.on('data', (chunk) => {
    const text = chunk.toString()
    stdoutChunks.push(text)
    for (const line of text.split('\n')) {
      if (base === '' && /https?:\/\/[^\s/]+:\d+/.test(line)) {
        startupLineSeen = line.trim()
        base = `http://127.0.0.1:${line.match(/https?:\/\/[^\s/]+:(\d+)/)[1]}`
        token = (line.match(/[?&]token=([A-Za-z0-9_-]+)/) || [])[1] || ''
      }
    }
  })
  child.stderr.on('data', (chunk) => stderrChunks.push(chunk.toString()))
  // spawn 失败（二进制不存在/无执行权限）不会抛错，只会发 error 事件；不接住的话表现为「等启动行超时」看不出原因
  child.on('error', (error) => stderrChunks.push(`\n[spawn error] ${error.message}\n`))

  /**
   * api 把 token 拼进查询参数后返回完整地址。
   * 为什么 Node 侧统一用查询参数而不是请求头：webui 的 tokenGate 两者都接受，
   * 而查询参数让 GET / POST / SSE 三种调用点的写法一致，不必让十几处调用各自记得加一个头
   */
  const api = (path) => `${base}${path}${path.includes('?') ? '&' : '?'}token=${encodeURIComponent(token)}`

  let browser
  let page
  let code = 1
  let setupFailed = false
  try {
    // 等启动行；同时容忍端口顺延（解析到的是真实端口）
    await waitUntil(() => base !== '', 15000, '等待启动行（含 http://host:port）')
    report.note(`启动行原文：${JSON.stringify(startupLineSeen)}`)
    // token 缺失只可能是启动摘要的格式变了（例如退回旧实现）；此时立刻失败并说清原因，
    // 否则后续每条断言都会以「找不到元素」的面目失败，看不出真正的原因
    if (token === '') {
      throw new Error(`启动行里没有 ?token= 查询参数，无法访问受保护页面；启动行原文=${JSON.stringify(startupLineSeen)}`)
    }

    // 等 /api/meta 就绪
    await waitUntil(
      async () => {
        try {
          const resp = await fetch(api('/api/meta'))
          return resp.ok
        } catch {
          return false
        }
      },
      15000,
      '等待 /api/meta 就绪',
      250,
    )

    // 越过旧实现 watchStoreFile 的首次「假变更」通知（约 1s 后必然触发一次）
    await sleep(1600)
    report.note('已越过服务启动后首次 watch 轮询（旧实现必然触发一次假变更事件）')

    const launched = await launchBrowser({
      headless: !options.headful,
      slowMoMs: options.fast ? undefined : 350,
    })
    browser = launched.browser
    report.note(`Chromium：${launched.executablePath}（来源：${launched.source}）`)
    page = await openPage(browser, {
      videoDir: run.videoDir,
      viewport: { width: 1280, height: 820 },
      locale: 'zh-CN',
    })
    page.setDefaultTimeout(6000)
    page.setDefaultNavigationTimeout(15000)
    report.attachPage(() => page)

    // 失败时留一份 DOM + 可见文本，便于离线复盘（不额外加断言，只做证据）
    report.onFailure(async ({ name, index }) => {
      try {
        const html = await page.content()
        const text = await page.locator('body').innerText()
        const file = `fail-${String(index).padStart(2, '0')}-dom.html`
        run.writeText(file, `<!-- 失败断言：${name} -->\n<!-- 页面可见文本：\n${text}\n-->\n${html}`)
        return join(run.dir, file)
      } catch {
        return undefined
      }
    })

    const allowed = (id) => options.only === undefined || options.only.includes(id)

    /**
     * 每条断言跑完后统一记一份「页面可观测状态」快照
     * 为什么要有它：断言里写的是判定条件，通过时不会把实际数值打出来，
     * 而交付时需要逐条给出「实际测得的值」，所以在这里统一兜住
     * 即使断言失败也要记（放在 finally 里），失败现场同样需要这些数值
     */
    const snapshotPage = async () => {
      try {
        return await page.evaluate(() => {
          const text = (selector) => {
            const el = document.querySelector(selector)
            return el === null ? null : (el.textContent ?? '').trim()
          }
          const visible = (selector) => {
            const el = document.querySelector(selector)
            return el === null ? null : !el.classList.contains('hidden') && getComputedStyle(el).display !== 'none'
          }
          return {
            行数: document.querySelectorAll('tbody tr').length,
            可编辑格数: document.querySelectorAll('input.cell-input').length,
            纯文本格数: document.querySelectorAll('.cell-text').length,
            脏格数: document.querySelectorAll('input.cell-input.dirty').length,
            脏计数文本: text('#dirtyCount'),
            冲突条存在数: document.querySelectorAll('#conflictBar').length,
            冲突条可见: visible('#conflictBar'),
            保存区可见: visible('#saveActions'),
            迁移按钮可见: visible('#migrateBtn'),
            最后更新: text('#updateInfo'),
            检测信息: text('#checkInfo'),
            行徽标: Array.from(document.querySelectorAll('tbody .health-badge')).map((el) => (el.textContent ?? '').trim()),
          }
        })
      } catch (error) {
        return { 快照失败: error.message }
      }
    }

    const check = async (id, title, body) => {
      if (!allowed(id)) return
      await report.check(`${id} ${title}`, async () => {
        try {
          await body()
        } finally {
          report.note(`页面状态快照=${JSON.stringify(await snapshotPage())}`)
        }
      })
    }

    /* ---- 现场重置：外部写回 pristine，并确认服务端已读回内存 ---- */
    const resetStore = async () => {
      writeFileSync(storeFile, pristineText)
      // 必须按规范化 JSON 精确比对，不能用子串：`a-real.txt` 是 `a-real.txt.saved` 的前缀，
      // 子串判断会在服务端尚未读回新内容时就对陈旧数据成立，导致重置提前返回
      const deadline = Date.now() + 10000
      let lastSeen = ''
      for (;;) {
        try {
          const payload = await (await fetch(api('/api/config'))).json()
          lastSeen = canonicalStore(payload)
          if (lastSeen === canonicalPristine) return
        } catch (error) {
          lastSeen = `<请求失败：${error.message}>`
        }
        if (Date.now() > deadline) {
          throw new Error(
            `重置 store 后 10s 内服务端仍未读回 pristine 内容；` +
              `实际（规范化截断）=${lastSeen.slice(0, 400)}；期望（规范化截断）=${canonicalPristine.slice(0, 400)}`,
          )
        }
        await sleep(200)
      }
    }

    /** 重置 store + 整页重载 + 切到目标平台/设备/类型 */
    const resetAndOpen = async () => {
      await resetStore()
      await page.goto(`${base}/?token=${encodeURIComponent(token)}`, { waitUntil: 'domcontentloaded' })
      await page.waitForSelector('#panel:not(.hidden)', { timeout: 10000 })
      await page.waitForFunction(() => document.querySelectorAll('tbody tr').length > 0, undefined, {
        timeout: 10000,
      })
      // 平台/类型页签是普通按钮，直接点
      await page.locator(`#platformTabs .tab[data-plat="${PLAT}"]`).first().click()
      // 设备页签在新版里是 <span class="tab" data-dev><button class="btn-dev-del">×</button></span>
      // 点正中间有概率点到删除按钮（会弹 confirm），因此固定点左侧留白处，事件仍冒泡到 span 本身
      await page.locator(`#deviceTabs .tab[data-dev="${DEV}"]`).first().click({ position: { x: 4, y: 4 } })
      await page.locator(`#typeTabs .tab[data-type="${TYPE}"]`).first().click()
      await page.waitForFunction(() => document.querySelectorAll('tbody tr').length > 0, undefined, {
        timeout: 10000,
      })
    }

    const markerA = join(dataDir, 'a-real.txt')

    /**
     * 确认「页面的 SSE 已经连通」，作为所有「外部改文件 → 页面应自动响应」断言的前置条件
     *
     * 做法与实现无关：反复写入探针版本（每次写盘都会刷新 mtime，服务端每秒轮询必然看到一次变更），
     * 直到页面表格里出现探针值——页面出现探针值就等价于「页面的 EventSource 已连通且事件被处理」。
     * 为什么不是「等 2 秒」：那是把旧实现的内部时序写进断言，换一版实现就可能失效；
     * 探针只依赖可观察结果，两版实现都适用，且失败时能说清「页面始终没收到事件」
     */
    const ensureSseLive = async () => {
      await waitUntil(
        async () => {
          writeFileSync(storeFile, probeText)
          const values = await fieldValues(page, 'real')
          return values.some((value) => value.includes(PROBE_MARKER))
        },
        15000,
        `页面 SSE 连通（探针：外部写入后表格出现 ${PROBE_MARKER}）`,
        1500,
      )
      // 排空在途事件：探针期间可能连写了多次，SSE 是覆盖式的，等一会儿让页面把残留事件处理完，
      // 免得后续「填输入框」与「重载配置」两条路径在极短窗口里互相踩踏（那会让 B7/B9 偶发丢改动）
      await sleep(1500)
    }

    /* ================= B1 ================= */
    await check('B1', '页面加载后每个数据格都是 input.cell-input，且不存在 #editBtn / #cancelBtn', async () => {
      await resetAndOpen()
      const rows = await page.locator('tbody tr').count()
      const inputCount = await page.locator('tbody tr input.cell-input').count()
      const textCount = await page.locator('tbody .cell-text').count()
      const fieldCount = TYPE_FIELDS[TYPE].length
      const editBtn = await page.locator('#editBtn').count()
      const cancelBtn = await page.locator('#cancelBtn').count()
      expect(
        rows > 0 && inputCount === rows * fieldCount && textCount === 0,
        `期望：表格 ${rows} 行的每个字段格都是 input.cell-input（应共 ${rows * fieldCount} 个），且不存在 .cell-text；` +
          `实际：input.cell-input=${inputCount} 个，.cell-text=${textCount} 个`,
      )
      expect(
        editBtn === 0 && cancelBtn === 0,
        `期望：页面上不存在 #editBtn 与 #cancelBtn（匹配数应为 0）；实际：#editBtn=${editBtn}，#cancelBtn=${cancelBtn}`,
      )
      /* 默认平台页签 = 宿主平台族（linux-amd64 → linux）：
         曾经的缺陷是按字母序取第一个（多平台清单往往落在 darwin），而「检测/修复/解除」
         只作用于宿主平台——页签不对齐时徽标全程显示「—」、行内按钮不渲染，像功能坏了
         断言刻意自校准：期望值从 /api/meta 实时读取，而不是硬编码 linux，
         这样这条断言在任何宿主平台上跑都是对的（CI 换机器也不需要改） */
      const meta = await fetch(api('/api/meta')).then((r) => r.json())
      const hostFamily = String(meta.platform || '').split('-')[0]
      const activePlatform = await page.locator('#platformTabs .tab.active').textContent()
      expect(
        activePlatform === hostFamily,
        `期望：默认激活的平台页签是宿主平台族 ${hostFamily}（/api/meta 上报 ${meta.platform}）；` +
          `实际激活的是 ${JSON.stringify(activePlatform)}`,
      )
    })

    /* ================= B2 ================= */
    await check('B2', '改动一个 cell-input 后：saveActions 可见、dirtyCount 含 1、input 带 .dirty、该行徽标变待重检', async () => {
      await resetAndOpen()
      const rows = await readRows(page)
      const rowIndex = await findRowIndexByField(page, 'real', markerA, rows)
      expect(
        rowIndex >= 0,
        `期望：表格里存在 real=${markerA} 的目标行；实际：该值不在表格中，实际 real 取值=${JSON.stringify(await fieldValues(page, 'real'))}`,
      )
      // 这条断言必须用真实输入框；旧 UI 查看态只有 .cell-text，会在这里失败并给出实际个数
      const input = page.locator(`tbody tr[data-idx="${rowIndex}"] input[data-key="real"]`)
      const inputCount = await input.count()
      expect(
        inputCount === 1,
        `期望：目标行内存在 input.cell-input[data-key=real]；实际：#${rowIndex} 行内该输入框匹配数=${inputCount}（旧 UI 查看态这里是 .cell-text 文本格）`,
      )
      await input.fill(`${markerA}.edited`)
      const saveActionsVisible = await page.locator('#saveActions').isVisible()
      expect(
        saveActionsVisible,
        `期望：改动后 #saveActions 可见；实际：isVisible=${saveActionsVisible}（#saveActions 匹配数=${await page.locator('#saveActions').count()}）`,
      )
      const dirtyText = await dirtyTextOf(page)
      const dirtyNumber = await dirtyCountOf(page)
      expect(
        dirtyNumber === 1 && /未保存改动\s*1\s*处/.test(dirtyText),
        `期望：#dirtyCount 文本为「未保存改动 1 处」（含数字 1）；实际文本=${JSON.stringify(dirtyText)}`,
      )
      const classes = (await input.getAttribute('class')) ?? ''
      expect(
        classes.split(/\s+/).includes('dirty'),
        `期望：被改动的 input 带 .dirty 类；实际 class=${JSON.stringify(classes)}`,
      )
      const badge = page.locator(`tbody tr[data-idx="${rowIndex}"] .health-badge`)
      const badgeCount = await badge.count()
      expect(badgeCount >= 1, `期望：目标行内存在 .health-badge；实际：匹配数=${badgeCount}`)
      const badgeClass = (await badge.first().getAttribute('class')) ?? ''
      const badgeText = (await badge.first().textContent()) ?? ''
      expect(
        badgeClass.split(/\s+/).includes('health-pending') && badgeText.trim() === '待重检',
        `期望：该行徽标为 health-pending 且文案「待重检」；实际：class=${JSON.stringify(badgeClass)}，文本=${JSON.stringify(badgeText)}`,
      )
      const saveText = (await page.locator('#saveBtn').textContent()) ?? ''
      const discardText = (await page.locator('#discardBtn').textContent()) ?? ''
      expect(
        saveText.trim() === '保存' && discardText.trim() === '撤销改动',
        `期望：#saveBtn 文案「保存」、#discardBtn 文案「撤销改动」；实际：#saveBtn=${JSON.stringify(saveText)}，#discardBtn=${JSON.stringify(discardText)}`,
      )
    })

    /* ================= B3 ================= */
    await check('B3', '点 #discardBtn 后：值回到原值、saveActions 隐藏、磁盘文件未被改动', async () => {
      await resetAndOpen()
      const rows = await readRows(page)
      const rowIndex = await findRowIndexByField(page, 'real', markerA, rows)
      expect(rowIndex >= 0, `期望：存在 real=${markerA} 的目标行；实际：找不到，实际取值=${JSON.stringify(await fieldValues(page, 'real'))}`)
      const input = page.locator(`tbody tr[data-idx="${rowIndex}"] input[data-key="real"]`)
      expect(
        (await input.count()) === 1,
        `期望：目标行内存在 input.cell-input[data-key=real]；实际：匹配数=${await input.count()}`,
      )
      const original = await input.inputValue()
      const diskBefore = readFileSync(storeFile, 'utf8')
      await input.fill(`${original}.edited`)
      await requireVisible(page, '#saveActions', '改动后出现的保存操作区')
      await requireVisible(page, '#discardBtn', '撤销改动按钮')
      await page.locator('#discardBtn').click()
      // 撤销会重建表格行，因此按「值」重新定位，不能复用渲染前的行下标
      await waitUntil(
        async () => (await fieldValues(page, 'real')).includes(original),
        5000,
        `撤销后表格里 a-real 行回到原值 ${JSON.stringify(original)}`,
      )
      const relocate = await findRowIndexByField(page, 'real', original)
      expect(
        relocate >= 0,
        `期望：撤销后能按原值 ${JSON.stringify(original)} 找到目标行；实际 real 取值=${JSON.stringify(await fieldValues(page, 'real'))}`,
      )
      const after = await page.locator(`tbody tr[data-idx="${relocate}"] input[data-key="real"]`).inputValue()
      expect(after === original, `期望：撤销后输入框值回到原值 ${JSON.stringify(original)}；实际=${JSON.stringify(after)}`)
      const saveActionsVisible = await page.locator('#saveActions').isVisible()
      expect(
        saveActionsVisible === false,
        `期望：撤销后 #saveActions 隐藏；实际：isVisible=${saveActionsVisible}`,
      )
      const diskAfter = readFileSync(storeFile, 'utf8')
      expect(
        diskAfter === diskBefore,
        `期望：撤销不写盘，磁盘内容与操作前逐字节相同；实际：操作前 ${diskBefore.length} 字节，操作后 ${diskAfter.length} 字节，是否相同=${diskAfter === diskBefore}`,
      )
      const apiPayload = await (await fetch(api('/api/config'))).json()
      const apiValues = (apiPayload?.[PLAT]?.[DEV]?.[TYPE] ?? []).map((entry) => entry.real)
      expect(
        apiValues.includes(original) && !apiValues.includes(`${original}.edited`),
        `期望：GET /api/config 里仍是原值 ${JSON.stringify(original)} 且不含改动值；实际该组 real 取值=${JSON.stringify(apiValues)}`,
      )
    })

    /* ================= B4 ================= */
    await check('B4', '点 #saveBtn 后：saveActions 隐藏、成功提示出现、磁盘文件确实已更新', async () => {
      await resetAndOpen()
      const newValue = `${markerA}.saved`
      const rows = await readRows(page)
      const rowIndex = await findRowIndexByField(page, 'real', markerA, rows)
      expect(rowIndex >= 0, `期望：存在 real=${markerA} 的目标行；实际：找不到，实际取值=${JSON.stringify(await fieldValues(page, 'real'))}`)
      const input = page.locator(`tbody tr[data-idx="${rowIndex}"] input[data-key="real"]`)
      expect((await input.count()) === 1, `期望：存在 input.cell-input[data-key=real]；实际：匹配数=${await input.count()}`)
      await input.fill(newValue)
      await requireVisible(page, '#saveBtn', '保存按钮')
      await page.locator('#saveBtn').click()
      // 先抓成功提示再等 #saveActions 隐藏：旧实现的成功提示只显示 3 秒（showSuccess 里 setTimeout 3000），
      // 若顺序反过来，慢一点的机器上可能刚好把提示等没了，把「提示已消失」误判成「没出提示」
      let successVisible = false
      let successText = ''
      try {
        await waitUntil(
          async () => {
            successVisible = await page.locator('#success').isVisible()
            successText = ((await page.locator('#success').textContent()) ?? '').trim()
            return successVisible && successText !== ''
          },
          6000,
          '保存后出现成功提示（#success 可见且非空）',
          150,
        )
      } catch (error) {
        throw new Error(
          `${error.message}——已观察到的最后状态：#success isVisible=${successVisible}，文本=${JSON.stringify(successText)}`,
        )
      }
      await waitUntil(
        async () => (await page.locator('#saveActions').isVisible()) === false,
        8000,
        '保存后 #saveActions 隐藏',
      )
      const onDisk = JSON.parse(readFileSync(storeFile, 'utf8'))
      const entries = onDisk?.[PLAT]?.[DEV]?.[TYPE] ?? []
      const found = entries.filter((entry) => entry.real === newValue).length
      expect(
        found === 1,
        `期望：磁盘文件里 ${PLAT}/${DEV}/${TYPE} 有一条 real=${JSON.stringify(newValue)}；实际：匹配 ${found} 条，该组实际 real 取值=${JSON.stringify(entries.map((e) => e.real))}`,
      )
      const apiPayload = await (await fetch(api('/api/config'))).json()
      const apiValues = (apiPayload?.[PLAT]?.[DEV]?.[TYPE] ?? []).map((entry) => entry.real)
      expect(
        apiValues.includes(newValue),
        `期望：GET /api/config 也返回新值 ${JSON.stringify(newValue)}；实际该组 real 取值=${JSON.stringify(apiValues)}`,
      )
    })

    /* ================= B5 ================= */
    /**
 * 装一条「页面内时间线」探针：挂钩 EventSource 的 updated 监听、fetch、以及 #conflictBar 的 class 变化
 * 必须在页面脚本执行前装（addInitScript），否则看不到 init() 里建立 SSE 前后发生的事
 * B5 与 X10 共用这一份，避免同一段探针写两遍
 */
    let timelineProbeInstalled = false

    const installTimelineProbe = () => {
      // addInitScript 是「叠加」的：同一个 page 上装两次就会有两条探针同时记录，时间线里每条都出现两遍
      // （B5 与 X10 用的是同一个 page），所以只装一次
      if (timelineProbeInstalled) return Promise.resolve()
      timelineProbeInstalled = true
      return page.addInitScript(() => {
        window.__timeline = []
        const now = () => Math.round(performance.now())
        const origAdd = EventSource.prototype.addEventListener
        EventSource.prototype.addEventListener = function (type, listener, opts) {
          if (type !== 'updated') return origAdd.call(this, type, listener, opts)
          const wrapped = function (event) {
            window.__timeline.push({ t: now(), kind: 'sse-updated', data: String(event.data ?? '') })
            return listener.apply(this, arguments)
          }
          return origAdd.call(this, type, wrapped, opts)
        }
        const origFetch = window.fetch
        window.fetch = function (...args) {
          const url = String(args[0])
          const method = args[1]?.method ?? 'GET'
          window.__timeline.push({ t: now(), kind: 'fetch', url, method })
          const promise = origFetch.apply(this, args)
          if (url.includes('/api/meta') || (url.includes('/api/config') && method === 'POST')) {
            promise
              .then((resp) =>
                resp
                  .clone()
                  .json()
                  .then((body) => window.__timeline.push({ t: now(), kind: 'resp-rev', url, rev: body?.rev ?? null }))
                  .catch(() => {}),
              )
              .catch(() => {})
          }
          return promise
        }
        document.addEventListener('DOMContentLoaded', () => {
          const el = document.getElementById('conflictBar')
          if (el === null) return
          window.__timeline.push({ t: now(), kind: 'bar-init', cls: el.className })
          new MutationObserver(() => {
            window.__timeline.push({
              t: now(),
              kind: 'bar-class',
              cls: el.className,
              style: el.getAttribute('style'),
              visible: !el.classList.contains('hidden') && getComputedStyle(el).display !== 'none',
            })
          }).observe(el, { attributes: true, attributeFilter: ['class', 'style'] })
        })
      })
    }

    await check('B5', '自己保存触发的 SSE（rev 相同）不得弹出 #conflictBar（含一闪而过的高频采样）', async () => {
      await installTimelineProbe()
      await resetAndOpen()
      const rows = await readRows(page)
      const rowIndex = await findRowIndexByField(page, 'real', markerA, rows)
      expect(rowIndex >= 0, `期望：存在 real=${markerA} 的目标行；实际：找不到，实际取值=${JSON.stringify(await fieldValues(page, 'real'))}`)
      const input = page.locator(`tbody tr[data-idx="${rowIndex}"] input[data-key="real"]`)
      expect((await input.count()) === 1, `期望：存在 input.cell-input[data-key=real]；实际：匹配数=${await input.count()}`)
      const conflictCount = await page.locator('#conflictBar').count()
      expect(
        conflictCount === 1,
        `期望：页面上存在 #conflictBar（spec 2.2），否则「没弹出」只是空真；实际：匹配数=${conflictCount}`,
      )
      await input.fill(`${markerA}.b5`)
      await requireVisible(page, '#saveBtn', '保存按钮')
      // 25ms 高频采样：只看「保存结束后一眼」是不够的——自己保存被误判成外部修改时，
      // 提示条会先 showConflict 弹出、随后被 saveConfig 里的 hideConflict() 抹掉，一闪而过也算「弹出」
      await page.evaluate(() => {
        window.__b5 = []
        const el = document.getElementById('conflictBar')
        const started = performance.now()
        const record = (why) => {
          const hidden = el === null || el.classList.contains('hidden') || getComputedStyle(el).display === 'none'
          window.__b5.push({
            t: Math.round(performance.now() - started),
            visible: hidden === false,
            why,
            cls: el?.className ?? null,
            style: el?.getAttribute('style') ?? null,
          })
        }
        // 不能只用定时采样抓闪烁：实测这个窗口只有 3–5ms，25ms 的采样会因相位误差漏掉（4 次里漏了 1 次），
        // 于是改成监听 class/style 的每一次变化——只要提示条变可见就必然留下记录，不受采样相位影响；
        // 另外保留一路 25ms 轮询兜底，防止实现改用别的显示方式（不触发属性变化）而漏检
        window.__b5Observer = new MutationObserver(() => record('mutation'))
        window.__b5Observer.observe(el, { attributes: true, attributeFilter: ['class', 'style'] })
        record('start')
        window.__b5Timer = setInterval(() => record('poll'), 25)
      })
      // 时间线里打一个锚点，便于判断「提示条可见」发生在点击保存之前还是之后
      await page.evaluate(() => {
        window.__timeline?.push({ t: Math.round(performance.now()), kind: 'mark-before-save-click' })
      })
      await page.locator('#saveBtn').click()
      await waitUntil(async () => (await page.locator('#saveActions').isVisible()) === false, 8000, '保存后 #saveActions 隐藏')
      // 保存会立刻触发一次 SSE，随后 watch 轮询（≤1s）也会因文件变化再触发一次，两者 rev 都等于响应里的 rev
      await sleep(2500)
      const samples = await page.evaluate(() => {
        clearInterval(window.__b5Timer)
        window.__b5Observer.disconnect()
        return window.__b5
      })
      const flashed = samples.filter((sample) => sample.visible)
      const timeline = await page.evaluate(() =>
        (window.__timeline ?? []).filter(
          (event) =>
            event.kind === 'sse-updated' ||
            event.kind === 'bar-class' ||
            event.kind === 'mark-before-save-click' ||
            event.kind === 'resp-rev',
        ),
      )
      report.note(
        `B5 记录 ${samples.length} 条（class/style 变化监听 + 25ms 轮询兜底），其中 #conflictBar 可见 ${flashed.length} 条；首个可见记录=${JSON.stringify(flashed[0] ?? null)}`,
      )
      report.note(`B5 时间线（SSE/提示条/响应，t 为页面内 performance.now 毫秒）=${JSON.stringify(timeline)}`)
      expect(
        flashed.length === 0,
        `期望：自己保存引起的 SSE 不得让 #conflictBar 出现（哪怕一闪）；实际：可见 ${flashed.length}/${samples.length} 条记录，首个可见记录=${JSON.stringify(flashed[0] ?? null)}；时间线=${JSON.stringify(timeline).slice(0, 1200)}`,
      )
      const finalVisible = await page.locator('#conflictBar').isVisible()
      expect(finalVisible === false, `期望：保存结束后 #conflictBar 处于隐藏；实际：isVisible=${finalVisible}`)
    })

    /* ================= B6 ================= */
    await check('B6', '无未保存改动时外部改文件 → 页面自动显示新值，#conflictBar 保持隐藏', async () => {
      await resetAndOpen()
      // 先确认 SSE 真连通再谈自动重载：否则「外部改动」可能落在页面尚未监听的窗口里而必然丢失
      await ensureSseLive()
      const before = await fieldValues(page, 'real')
      expect(
        before.includes(markerA),
        `期望：外部改写前表格里已有 ${markerA}；实际 real 取值=${JSON.stringify(before)}`,
      )
      writeFileSync(storeFile, externalText)
      // 自诊断：失败时要说清是「服务端没读出外部内容」还是「页面没收到 SSE / 没重载」，
      // 否则只看到一句超时，下一次还得重跑才知道差在哪
      let serverHasMarker = false
      let updateInfoText = ''
      await waitUntil(
        async () => {
          try {
            const serverText = await (await fetch(api('/api/config'))).text()
            serverHasMarker = serverText.includes(EXTERNAL_MARKER)
          } catch {
            serverHasMarker = false
          }
          updateInfoText = await page
            .locator('#updateInfo')
            .textContent()
            .then((text) => (text ?? '').trim())
            .catch(() => '<页面无 #updateInfo 元素>')
          return (await fieldValues(page, 'real')).some((value) => value.includes(EXTERNAL_MARKER))
        },
        12000,
        `外部改写后页面自动显示新值（含 ${EXTERNAL_MARKER}）——自诊断：Node 侧 GET /api/config 含新值=${serverHasMarker}；页面 #updateInfo 文本=${JSON.stringify(updateInfoText)}（旧 UI 只有收到 SSE updated 才会写这行，为空即页面从未收到事件）`,
        300,
      )
      const after = await fieldValues(page, 'real')
      expect(
        after.some((value) => value.includes(EXTERNAL_MARKER)),
        `期望：页面自动重载后 real 列出现外部新值；实际 real 取值=${JSON.stringify(after)}（写入前=${JSON.stringify(before)}）`,
      )
      const conflictVisible = await page.locator('#conflictBar').isVisible()
      const conflictCount = await page.locator('#conflictBar').count()
      // 为什么这里要连着断言「元素存在」：只断言 isVisible=false 的话，页面上根本没有 #conflictBar
      // 时也会通过——那是空真。实测旧 UI 就是这样「通过」了 B6，而它恰恰是本次改造要消除的形态。
      // #conflictBar 本身是 spec 2.2 的硬 DOM 契约，所以把存在性并进 B6 不算收紧超出契约
      expect(
        conflictCount === 1,
        `期望：页面上存在 #conflictBar（spec 2.2），且此时保持隐藏；实际：匹配数=${conflictCount}（为 0 时「保持隐藏」只是空真，不能算通过）`,
      )
      expect(
        conflictVisible === false,
        `期望：无未保存改动时外部改写不弹 #conflictBar；实际：isVisible=${conflictVisible}`,
      )
    })

    /* ================= B7 ================= */
    await check('B7', '有未保存改动时外部改文件 → #conflictBar 出现且我的改动仍在输入框里', async () => {
      await resetAndOpen()
      await ensureSseLive()
      const rows = await readRows(page)
      const rowIndex = await findRowIndexByField(page, 'real', markerA, rows)
      expect(rowIndex >= 0, `期望：存在 real=${markerA} 的目标行；实际：找不到，实际取值=${JSON.stringify(await fieldValues(page, 'real'))}`)
      const input = page.locator(`tbody tr[data-idx="${rowIndex}"] input[data-key="real"]`)
      expect((await input.count()) === 1, `期望：存在 input.cell-input[data-key=real]；实际：匹配数=${await input.count()}`)
      await input.fill(UNSAVED_MARKER)
      writeFileSync(storeFile, externalText)
      await waitUntil(async () => await page.locator('#conflictBar').isVisible(), 12000, '#conflictBar 出现', 300)
      const conflictText = ((await page.locator('#conflictText').textContent()) ?? '').trim()
      expect(
        conflictText === '配置文件已在页面外被修改',
        `期望：#conflictText 文案「配置文件已在页面外被修改」；实际=${JSON.stringify(conflictText)}`,
      )
      const reloadText = ((await page.locator('#conflictReloadBtn').textContent()) ?? '').trim()
      const keepText = ((await page.locator('#conflictKeepBtn').textContent()) ?? '').trim()
      expect(
        reloadText === '重新加载（丢弃我的改动）' && keepText === '保留我的改动',
        `期望：两个冲突按钮文案为「重新加载（丢弃我的改动）」与「保留我的改动」；实际：#conflictReloadBtn=${JSON.stringify(reloadText)}，#conflictKeepBtn=${JSON.stringify(keepText)}`,
      )
      const currentValues = await fieldValues(page, 'real')
      expect(
        currentValues.includes(UNSAVED_MARKER),
        `期望：冲突期间我的未保存改动仍在输入框里（${UNSAVED_MARKER}）；实际 real 取值=${JSON.stringify(currentValues)}`,
      )
      expect(
        (await page.locator('#saveActions').isVisible()) === true,
        `期望：冲突期间 #saveActions 仍可见；实际：isVisible=${await page.locator('#saveActions').isVisible()}`,
      )
    })

    /* ================= B8 ================= */
    await check('B8', 'B7 状态下点 #conflictReloadBtn → #conflictBar 隐藏、显示外部新值、saveActions 隐藏', async () => {
      await resetAndOpen()
      await ensureSseLive()
      const rows = await readRows(page)
      const rowIndex = await findRowIndexByField(page, 'real', markerA, rows)
      expect(rowIndex >= 0, `期望：存在 real=${markerA} 的目标行；实际：找不到，实际取值=${JSON.stringify(await fieldValues(page, 'real'))}`)
      const input = page.locator(`tbody tr[data-idx="${rowIndex}"] input[data-key="real"]`)
      expect((await input.count()) === 1, `期望：存在 input.cell-input[data-key=real]；实际：匹配数=${await input.count()}`)
      await input.fill(UNSAVED_MARKER)
      writeFileSync(storeFile, externalText)
      await waitUntil(async () => await page.locator('#conflictBar').isVisible(), 12000, '#conflictBar 出现（B8 前置状态）', 300)
      await page.locator('#conflictReloadBtn').click()
      await waitUntil(async () => (await page.locator('#conflictBar').isVisible()) === false, 8000, '点击重新加载后 #conflictBar 隐藏')
      await waitUntil(
        async () => (await fieldValues(page, 'real')).some((value) => value.includes(EXTERNAL_MARKER)),
        8000,
        '重新加载后表格出现外部新值',
      )
      const values = await fieldValues(page, 'real')
      expect(
        values.some((value) => value.includes(EXTERNAL_MARKER)) && !values.includes(UNSAVED_MARKER),
        `期望：重新加载后显示外部新值且我的改动被丢弃；实际 real 取值=${JSON.stringify(values)}`,
      )
      const saveActionsVisible = await page.locator('#saveActions').isVisible()
      expect(saveActionsVisible === false, `期望：重新加载后 #saveActions 隐藏；实际：isVisible=${saveActionsVisible}`)
    })

    /* ================= B9 ================= */
    await check('B9', 'B7 状态下点 #conflictKeepBtn → #conflictBar 隐藏、我的改动仍在、saveActions 仍可见', async () => {
      await resetAndOpen()
      await ensureSseLive()
      const rows = await readRows(page)
      const rowIndex = await findRowIndexByField(page, 'real', markerA, rows)
      expect(rowIndex >= 0, `期望：存在 real=${markerA} 的目标行；实际：找不到，实际取值=${JSON.stringify(await fieldValues(page, 'real'))}`)
      const input = page.locator(`tbody tr[data-idx="${rowIndex}"] input[data-key="real"]`)
      expect((await input.count()) === 1, `期望：存在 input.cell-input[data-key=real]；实际：匹配数=${await input.count()}`)
      await input.fill(UNSAVED_MARKER)
      writeFileSync(storeFile, externalText)
      await waitUntil(async () => await page.locator('#conflictBar').isVisible(), 12000, '#conflictBar 出现（B9 前置状态）', 300)
      await page.locator('#conflictKeepBtn').click()
      await waitUntil(async () => (await page.locator('#conflictBar').isVisible()) === false, 8000, '点击保留后 #conflictBar 隐藏')
      const values = await fieldValues(page, 'real')
      expect(
        values.includes(UNSAVED_MARKER),
        `期望：保留我的改动后输入框里仍是 ${UNSAVED_MARKER}；实际 real 取值=${JSON.stringify(values)}`,
      )
      const saveActionsVisible = await page.locator('#saveActions').isVisible()
      expect(saveActionsVisible === true, `期望：保留我的改动后 #saveActions 仍可见；实际：isVisible=${saveActionsVisible}`)
      const dirtyText = await dirtyTextOf(page)
      expect(
        (await dirtyCountOf(page)) === 1,
        `期望：保留我的改动后脏计数仍为 1；实际：#dirtyCount 文本=${JSON.stringify(dirtyText)}`,
      )
    })

    /* ================= B10 ================= */
    await check('B10', '点 #addEntryBtn 新增一条 → 行数 +1、计数 +1、新行徽标为待重检', async () => {
      await resetAndOpen()
      const rowsBefore = await page.locator('tbody tr').count()
      await requireVisible(page, '#addEntryBtn', '新增条目按钮')
      await page.locator('#addEntryBtn').click()
      await waitUntil(async () => (await page.locator('tbody tr').count()) === rowsBefore + 1, 6000, '新增后行数 +1')
      const rowsAfter = await page.locator('tbody tr').count()
      expect(
        rowsAfter === rowsBefore + 1,
        `期望：新增后行数 ${rowsBefore + 1}；实际：${rowsAfter}`,
      )
      const dirtyText = await dirtyTextOf(page)
      expect(
        (await dirtyCountOf(page)) === 1,
        `期望：新增一条后脏计数为 1；实际：#dirtyCount 文本=${JSON.stringify(dirtyText)}`,
      )
      const pending = await page.$$eval('tbody .health-badge', (badges) =>
        badges.map((b) => ({ className: b.className, text: (b.textContent ?? '').trim() })),
      )
      const pendingOk = pending.filter((b) => b.className.split(/\s+/).includes('health-pending') && b.text === '待重检')
      expect(
        pendingOk.length >= 1,
        `期望：至少一行徽标为 health-pending 且文案「待重检」；实际全部徽标=${JSON.stringify(pending)}`,
      )
    })

    /* ================= B11 ================= */
    await check('B11', '点 .btn-dell 删除一条 → 行数 -1、计数 +1', async () => {
      await resetAndOpen()
      const rowsBefore = await page.locator('tbody tr').count()
      const deleteButtons = await page.locator('tbody .btn-dell').count()
      expect(
        deleteButtons === rowsBefore,
        `期望：非编辑状态下每行都有 .btn-dell（应 ${rowsBefore} 个）；实际：${deleteButtons} 个`,
      )
      await page.locator('tbody .btn-dell').first().click()
      await waitUntil(async () => (await page.locator('tbody tr').count()) === rowsBefore - 1, 6000, '删除后行数 -1')
      const rowsAfter = await page.locator('tbody tr').count()
      expect(rowsAfter === rowsBefore - 1, `期望：删除后行数 ${rowsBefore - 1}；实际：${rowsAfter}`)
      const dirtyText = await dirtyTextOf(page)
      expect(
        (await dirtyCountOf(page)) === 1,
        `期望：删除一条后脏计数为 1；实际：#dirtyCount 文本=${JSON.stringify(dirtyText)}`,
      )
      // 用规范化精确比对代替「不含 .saved/.edited」这种子串判断：后者在值本身以这些字符串结尾时会误判
      const apiCanonical = canonicalStore(await (await fetch(api('/api/config'))).json())
      const diskCanonical = canonicalStore(JSON.parse(readFileSync(storeFile, 'utf8')))
      expect(
        apiCanonical === canonicalPristine && diskCanonical === canonicalPristine,
        `期望：仅删除未保存时既不改内存也不写盘（内容应与 pristine 完全一致）；实际：GET 与 pristine 相同=${apiCanonical === canonicalPristine}，磁盘与 pristine 相同=${diskCanonical === canonicalPristine}，磁盘实际=${diskCanonical.slice(0, 300)}`,
      )
    })

    /* ================= B12 ================= */
    await check('B12', '非编辑状态下复选框列 / .drag-handle / .btn-dell 存在且可用', async () => {
      await resetAndOpen()
      const rows = await page.locator('tbody tr').count()
      const headCb = await page.locator('#tableHead th.cb-col').count()
      const cbAll = await page.locator('#cbAll').count()
      const dragHead = await page.locator('#tableHead th.drag-handle').count()
      const dragCells = await page.locator('tbody .drag-handle').count()
      const deleteButtons = await page.locator('tbody .btn-dell').count()
      expect(
        headCb === 1 && cbAll === 1,
        `期望：表头常驻复选框列 th.cb-col 与 input#cbAll；实际：th.cb-col=${headCb}，#cbAll=${cbAll}`,
      )
      expect(
        dragHead === 1 && dragCells === rows,
        `期望：表头 th.drag-handle 常驻且每行一个 .drag-handle（应 ${rows} 个）；实际：表头=${dragHead}，行内=${dragCells}`,
      )
      expect(
        deleteButtons === rows,
        `期望：每行都有 .btn-dell（应 ${rows} 个）；实际：${deleteButtons} 个`,
      )
      // 「可用」落到可观察结果：勾选全选复选框后，迁移按钮应出现（spec 2.3：选中数 >0 时才显示）
      await page.locator('#cbAll').check()
      await waitUntil(async () => await page.locator('#migrateBtn').isVisible(), 6000, '全选后 #migrateBtn 出现')
      const checkedRows = await page.locator('tbody .cb-row:checked').count()
      const migrateVisible = await page.locator('#migrateBtn').isVisible()
      expect(
        migrateVisible && checkedRows === rows,
        `期望：全选后每行复选框都被勾选（应 ${rows} 行）且 #migrateBtn 可见；实际：勾选 ${checkedRows} 行，#migrateBtn isVisible=${migrateVisible}`,
      )
    })

    /* ================= B13 ================= */
    await check('B13', '保存成功后再次改动 → 计数从 1 重新开始（快照已更新，不累计历史）', async () => {
      await resetAndOpen()
      const rows = await readRows(page)
      const rowIndex = await findRowIndexByField(page, 'real', markerA, rows)
      expect(rowIndex >= 0, `期望：存在 real=${markerA} 的目标行；实际：找不到，实际取值=${JSON.stringify(await fieldValues(page, 'real'))}`)
      const input = page.locator(`tbody tr[data-idx="${rowIndex}"] input[data-key="real"]`)
      expect((await input.count()) === 1, `期望：存在 input.cell-input[data-key=real]；实际：匹配数=${await input.count()}`)
      await input.fill(`${markerA}.b13-first`)
      const firstText = await dirtyTextOf(page)
      expect((await dirtyCountOf(page)) === 1, `期望：第一次改动后计数为 1；实际：#dirtyCount 文本=${JSON.stringify(firstText)}`)
      await requireVisible(page, '#saveBtn', '保存按钮')
      await page.locator('#saveBtn').click()
      await waitUntil(async () => (await page.locator('#saveActions').isVisible()) === false, 8000, '保存后 #saveActions 隐藏')
      // 保存后再改另一个字段：快照已更新，计数必须从 1 重新开始而不是变成 2
      // 保存会让服务端重排序条目，所以按刚保存的值重新定位目标行，不复用旧下标
      const savedValue = `${markerA}.b13-first`
      const relocate = await findRowIndexByField(page, 'real', savedValue)
      expect(
        relocate >= 0,
        `期望：保存后能按 ${JSON.stringify(savedValue)} 找到目标行；实际 real 取值=${JSON.stringify(await fieldValues(page, 'real'))}`,
      )
      const fakeInput = page.locator(`tbody tr[data-idx="${relocate}"] input[data-key="fake"]`)
      expect((await fakeInput.count()) === 1, `期望：存在 input.cell-input[data-key=fake]；实际：匹配数=${await fakeInput.count()}`)
      await fakeInput.fill(`${dataDir}/a-link-b13`)
      const secondText = await dirtyTextOf(page)
      const secondNumber = await dirtyCountOf(page)
      expect(
        secondNumber === 1,
        `期望：保存后再次改动，计数从 1 重新开始；实际：#dirtyCount 文本=${JSON.stringify(secondText)}（解析出 ${secondNumber}）`,
      )
      expect(
        secondText === '未保存改动 1 处',
        `期望：#dirtyCount 完整文案「未保存改动 1 处」；实际=${JSON.stringify(secondText)}`,
      )
    })

    /* ====== 以下为附加断言（不属于 B1–B13，单独标注，便于定位问题归属） ====== */

    await check('X1', '附加：中文模式下界面无英文残留（spec 第 5 节硬约束）', async () => {
      await resetAndOpen()
      const bodyText = await page.locator('body').innerText()
      const hits = ENGLISH_RESIDUE.filter((phrase) => new RegExp(`\\b${escapeRegExp(phrase)}\\b`).test(bodyText))
      expect(
        hits.length === 0,
        `期望：中文模式下可见文本不含英文源串；实际命中 ${hits.length} 个：${JSON.stringify(hits)}；页面可见文本=${JSON.stringify(bodyText.slice(0, 1200))}`,
      )
    })

    await check('X2', '附加：页头结构 —— #recheckBtn 常驻中文、#conflictBar 存在且默认隐藏', async () => {
      await resetAndOpen()
      const recheck = page.locator('#recheckBtn')
      const recheckCount = await recheck.count()
      expect(recheckCount === 1, `期望：#recheckBtn 常驻存在；实际：匹配数=${recheckCount}`)
      const recheckText = ((await recheck.textContent()) ?? '').trim()
      expect(recheckText === '重新检测', `期望：#recheckBtn 文案「重新检测」；实际=${JSON.stringify(recheckText)}`)
      const conflictCount = await page.locator('#conflictBar').count()
      expect(
        conflictCount === 1,
        `期望：#conflictBar 元素存在（spec 2.2）；实际：匹配数=${conflictCount}（为 0 时 B6 里「保持隐藏」是空真）`,
      )
      const conflictVisible = await page.locator('#conflictBar').isVisible()
      expect(conflictVisible === false, `期望：#conflictBar 默认 hidden；实际：isVisible=${conflictVisible}`)
    })

    await check('X3', '附加：真实拖拽一行改变顺序（HTML5 DnD，可能 flaky，仅供参考）', async () => {
      await resetAndOpen()
      const handles = await page.locator('tbody .drag-handle').count()
      expect(handles >= 2, `期望：至少两行有 .drag-handle 才能测拖拽；实际：${handles} 个`)
      const before = await fieldValues(page, 'real')
      await page.locator('tbody tr[data-idx="0"] .drag-handle').first().dragTo(page.locator('tbody tr[data-idx="1"]'), { timeout: 6000 })
      await sleep(600)
      const after = await fieldValues(page, 'real')
      expect(
        JSON.stringify(before) !== JSON.stringify(after),
        `期望：拖拽后行的顺序发生变化；实际：拖拽前=${JSON.stringify(before)}，拖拽后=${JSON.stringify(after)}`,
      )
    })

    await check('X4', '附加：GET /api/meta 返回 rev 字段（spec 3.2）', async () => {
      const payload = await (await fetch(api('/api/meta'))).json()
      expect(
        typeof payload.rev === 'string' && payload.rev.length > 0,
        `期望：/api/meta 含非空字符串 rev；实际：rev=${JSON.stringify(payload.rev)}，全部字段=${JSON.stringify(Object.keys(payload))}`,
      )
    })

    await check('X5', '附加：POST /api/config 返回 rev（spec 3.3）', async () => {
      const current = await (await fetch(api('/api/config'))).json()
      const resp = await fetch(api('/api/config'), {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify(current),
      })
      const payload = await resp.json()
      expect(
        payload.success === true && typeof payload.rev === 'string' && payload.rev.length > 0,
        `期望：POST /api/config 返回 {"success":true,"rev":"<非空>"}；实际响应=${JSON.stringify(payload)}（HTTP ${resp.status}）`,
      )
    })

    await check('X6', '附加：SSE updated 事件的 data 是含 rev 的合法 JSON（spec 3.4）', async () => {
      await resetStore()
      const controller = new AbortController()
      const timer = setTimeout(() => controller.abort(), 12000)
      let dataLine
      let rawBuffer = ''
      try {
        const resp = await fetch(api('/api/events'), {
          signal: controller.signal,
          headers: { accept: 'text/event-stream' },
        })
        const reader = resp.body.getReader()
        // 连接建立之后再写文件，保证收到的第一个事件就来自这次外部修改
        writeFileSync(storeFile, externalText)
        const decoder = new TextDecoder()
        for (;;) {
          const { value, done } = await reader.read()
          if (done) break
          rawBuffer += decoder.decode(value, { stream: true })
          const matches = [...rawBuffer.matchAll(/^data: (.*)$/gm)]
          if (matches.length > 0) {
            dataLine = matches[matches.length - 1][1]
            break
          }
        }
        await reader.cancel().catch(() => undefined)
      } catch (error) {
        report.note(`X6 读取 SSE 时中断：${error.message}`)
      } finally {
        clearTimeout(timer)
      }
      expect(dataLine !== undefined, `期望：12s 内收到至少一条 SSE data 行；实际原始字节=${JSON.stringify(rawBuffer.slice(0, 500))}`)
      let parsed
      try {
        parsed = JSON.parse(dataLine)
      } catch (error) {
        throw new Error(`期望：SSE data 是合法 JSON；实际 data=${JSON.stringify(dataLine)}，JSON.parse 报错：${error.message}`)
      }
      expect(
        typeof parsed.rev === 'string' && parsed.rev.length > 0,
        `期望：SSE data 含非空 rev；实际：data=${JSON.stringify(dataLine)}，解析结果 rev=${JSON.stringify(parsed.rev)}`,
      )
      await resetStore()
    })

    /* ====== X7 / X8：委派者特别关心的两条接缝（附加，不计入 B 结论） ====== */

    const inputAt = (idx, key = 'real') => page.locator(`tbody tr[data-idx="${idx}"] input[data-key="${key}"]`)

    /** 取元素几何中心与左上角，用于真实鼠标拖拽 */
    const centerOf = async (locator) => {
      const box = await locator.boundingBox()
      if (box === null) return null
      return { x: box.x, y: box.y, w: box.width, h: box.height, cx: box.x + box.width / 2, cy: box.y + box.height / 2 }
    }

    /**
     * 在 tbody 上装捕获阶段的 dragstart/drop 监听，记录「谁发起的拖拽、落到哪一行」
     * 挂在 tbody 本身而不是每一行：renderTable 只替换 tbody 的 innerHTML，挂在 tbody 上的监听不会被冲掉
     */
    const instrumentDrag = async () => {
      await page.evaluate(() => {
        window.__dragEvents = []
        const body = document.querySelector('tbody')
        if (body === null) return
        for (const type of ['dragstart', 'drop']) {
          body.addEventListener(
            type,
            (event) => {
              const target = event.target
              window.__dragEvents.push({
                type: event.type,
                tag: target?.tagName ?? null,
                cls: typeof target?.className === 'string' ? target.className : '',
                row: target?.closest?.('tr')?.dataset?.idx ?? null,
              })
            },
            true,
          )
        }
      })
    }
    const dragEvents = async () => page.evaluate(() => window.__dragEvents ?? [])

    // X7 的正对照是前面的 X3：X3 若不能把「拖手柄」变成换序，说明这套真实鼠标拖拽在本浏览器里
    // 根本触发不了 HTML5 DnD，那么下面 X7.x 的「顺序没变」就只是空真、不能当结论用
    await check('X7.1', '附加：行内拖选文本不应被误判成整行拖拽（同一行内，未预选）', async () => {
      await resetAndOpen()
      await instrumentDrag()
      const before = await fieldValues(page, 'real')
      const idx = await findRowIndexByField(page, 'real', markerA)
      expect(idx >= 0, `期望：存在 real=${markerA} 的目标行；实际 real 取值=${JSON.stringify(before)}`)
      const input = inputAt(idx)
      const geo = await centerOf(input)
      expect(geo !== null && geo.w > 40, `期望：输入框有足够宽度以拖动选中文字；实际 boundingBox=${JSON.stringify(geo)}`)
      const distance = Math.min(86, geo.w - 6)
      await page.mouse.move(geo.x + 6, geo.cy)
      await page.mouse.down()
      await page.mouse.move(geo.x + 6 + distance, geo.cy, { steps: 12 })
      await page.mouse.up()
      await sleep(400)
      const after = await fieldValues(page, 'real')
      const selection = await input.evaluate((el) => ({ start: el.selectionStart, end: el.selectionEnd }))
      const events = await dragEvents()
      report.note(`X7.1 拖动后选区 start=${selection.start} end=${selection.end}；捕获到的拖拽事件=${JSON.stringify(events)}`)
      expect(
        JSON.stringify(after) === JSON.stringify(before),
        `期望：同一行内水平拖选不应改变行顺序；实际：拖动前=${JSON.stringify(before)}，拖动后=${JSON.stringify(after)}，选区=${JSON.stringify(selection)}，拖拽事件=${JSON.stringify(events)}`,
      )
      // D3 的原始现象是「在输入框里拖选文字选不中」（上一轮实测 selectionStart/End=0,0，文字根本选不上）
      // 既然实现者声称文字能正常选中，这里就必须断言选区真的有内容，只断言「行序没变」会漏掉 D3
      expect(
        selection.end > selection.start,
        `期望：在输入框里按住拖动应真正选中文字（不能被整行拖拽劫持）；实际 selectionStart/End=${selection.start}/${selection.end}，拖拽事件=${JSON.stringify(events)}`,
      )
    })

    await check('X7.2', '附加：跨行拖选文本不应把第 1 行重排（第 1 行 input → 第 2 行 input，未预选）', async () => {
      await resetAndOpen()
      await instrumentDrag()
      const before = await fieldValues(page, 'real')
      const idx0 = await findRowIndexByField(page, 'real', markerA)
      expect(idx0 >= 0, `期望：存在 real=${markerA} 的目标行；实际 real 取值=${JSON.stringify(before)}`)
      const idx1 = idx0 + 1
      const rowCount = await page.locator('tbody tr').count()
      expect(rowCount > idx1, `期望：至少 ${idx1 + 1} 行才能跨行拖选；实际：${rowCount} 行`)
      const g0 = await centerOf(inputAt(idx0))
      const g1 = await centerOf(inputAt(idx1))
      expect(g0 !== null && g1 !== null, `期望：两行输入框都有几何信息；实际=${JSON.stringify({ g0, g1 })}`)
      await page.mouse.move(g0.x + 6, g0.cy)
      await page.mouse.down()
      await page.mouse.move(g1.x + 6, g1.cy, { steps: 16 })
      await page.mouse.up()
      await sleep(500)
      const after = await fieldValues(page, 'real')
      const selection = await inputAt(idx0).evaluate((el) => ({ start: el.selectionStart, end: el.selectionEnd }))
      const events = await dragEvents()
      const dirtyText = await dirtyTextOf(page)
      report.note(
        `X7.2 拖动后第 1 行选区 start=${selection.start} end=${selection.end}；脏计数文本=${JSON.stringify(dirtyText)}；捕获到的拖拽事件=${JSON.stringify(events)}`,
      )
      expect(
        JSON.stringify(after) === JSON.stringify(before),
        `期望：跨行拖选不应触发整行重排；实际：拖动前=${JSON.stringify(before)}，拖动后=${JSON.stringify(after)}，脏计数=${JSON.stringify(dirtyText)}，拖拽事件=${JSON.stringify(events)}`,
      )
    })

    await check('X7.3', '附加：从已有选区里拖走文本不应把第 1 行重排（最易触发原生拖拽的路径）', async () => {
      await resetAndOpen()
      await instrumentDrag()
      const idx0 = await findRowIndexByField(page, 'real', markerA)
      const idx1 = idx0 + 1
      const rowCount = await page.locator('tbody tr').count()
      expect(rowCount > idx1, `期望：至少 ${idx1 + 1} 行；实际：${rowCount} 行`)
      const input0 = inputAt(idx0)
      // 先双击形成真实文本选区：Chromium 里「按住已选中的文字再拖」才是最常见的原生拖拽触发路径
      await input0.dblclick()
      const preSelection = await input0.evaluate((el) => ({ start: el.selectionStart, end: el.selectionEnd }))
      expect(
        preSelection.end > preSelection.start,
        `期望：双击后形成非空文本选区；实际 start=${preSelection.start} end=${preSelection.end}`,
      )
      const before = await fieldValues(page, 'real')
      const g0 = await centerOf(input0)
      const g1 = await centerOf(inputAt(idx1))
      await page.mouse.move(g0.cx, g0.cy)
      await page.mouse.down()
      await page.mouse.move(g1.cx, g1.cy, { steps: 16 })
      await page.mouse.up()
      await sleep(500)
      const after = await fieldValues(page, 'real')
      const events = await dragEvents()
      const dirtyText = await dirtyTextOf(page)
      report.note(
        `X7.3 预选范围 ${preSelection.start}-${preSelection.end}；拖动后顺序=${JSON.stringify(after)}；脏计数文本=${JSON.stringify(dirtyText)}；捕获到的拖拽事件=${JSON.stringify(events)}`,
      )
      expect(
        JSON.stringify(after) === JSON.stringify(before),
        `期望：从已有选区里拖走文本不应触发整行重排；实际：拖动前=${JSON.stringify(before)}，拖动后=${JSON.stringify(after)}，预选范围=${preSelection.start}-${preSelection.end}，脏计数=${JSON.stringify(dirtyText)}，拖拽事件=${JSON.stringify(events)}`,
      )
    })

    await check('X8.1', '附加：SSE 自动重载重建 DOM 后，同一 input 的焦点与选区被保留（契约 4.3）', async () => {
      await resetAndOpen()
      await ensureSseLive()
      const idx = await findRowIndexByField(page, 'real', markerA)
      expect(idx >= 0, `期望：存在 real=${markerA} 的目标行；实际=${JSON.stringify(await fieldValues(page, 'real'))}`)
      const input = inputAt(idx)
      await input.click()
      await page.keyboard.press('Home')
      for (let i = 0; i < 8; i += 1) await page.keyboard.press('Shift+ArrowRight')
      const before = await input.evaluate((el) => ({
        start: el.selectionStart,
        end: el.selectionEnd,
        value: el.value,
        focused: document.activeElement === el,
      }))
      expect(
        before.focused === true && before.end > before.start,
        `期望：目标输入框已聚焦并形成非空选区；实际=${JSON.stringify(before)}`,
      )
      // 触发被动重渲染：外部改文件 → SSE 自动重载；刻意不点按钮，避免「点按钮会抢焦点」这个与本次断言无关的副作用
      // 探针改的是第 1 行的 real，被聚焦的这一行值不变，选区范围才有可比性
      writeFileSync(storeFile, probeText)
      await waitUntil(
        async () => (await fieldValues(page, 'real')).some((value) => value.includes(PROBE_MARKER)),
        12000,
        '外部改写触发页面自动重载（表格出现探针值）',
        300,
      )
      const after = await input.evaluate((el) => ({
        start: el.selectionStart,
        end: el.selectionEnd,
        value: el.value,
        focused: document.activeElement === el,
      }))
      const activeNow = await page.evaluate(() => {
        const el = document.activeElement
        if (el === null) return 'null'
        return `${el.tagName}.${typeof el.className === 'string' ? el.className : ''}[key=${el.dataset?.key ?? ''}]`
      })
      expect(
        after.focused === true,
        `期望：重渲染后同一个 input 仍有焦点；实际：document.activeElement=${activeNow}，快照=${JSON.stringify(after)}`,
      )
      expect(
        after.start === before.start && after.end === before.end,
        `期望：重渲染后选区与之前一致（${before.start}-${before.end}）；实际=${after.start}-${after.end}（值是否相同=${after.value === before.value}）`,
      )
      expect(after.value === before.value, `期望：重渲染后该 input 的值不变；实际：前=${JSON.stringify(before.value)}，后=${JSON.stringify(after.value)}`)
    })

    await check('X8.2', '附加：可用性检测完成重建 DOM 后，同一 input 的焦点与选区被保留（契约 4.3 / 2.4）', async () => {
      await resetAndOpen()
      const idx = await findRowIndexByField(page, 'real', markerA)
      expect(idx >= 0, `期望：存在 real=${markerA} 的目标行；实际=${JSON.stringify(await fieldValues(page, 'real'))}`)
      const input = inputAt(idx)
      await input.click()
      await page.keyboard.press('Home')
      for (let i = 0; i < 6; i += 1) await page.keyboard.press('Shift+ArrowRight')
      const before = await input.evaluate((el) => ({
        start: el.selectionStart,
        end: el.selectionEnd,
        value: el.value,
        focused: document.activeElement === el,
      }))
      expect(
        before.focused === true && before.end > before.start,
        `期望：目标输入框已聚焦并形成非空选区；实际=${JSON.stringify(before)}`,
      )
      // 在 window 上存一个旧节点引用：「它已脱离文档」就是「renderTable 真的重建了 DOM」的确定性信号，
      // 比等 #checkInfo 文案变化可靠（文案里的时间戳同一秒内可能没变化）
      await page.evaluate(() => {
        window.__x8Old = document.activeElement
      })
      // 用合成 click 触发 Re-check：真实点击会把焦点移到按钮上，那样「重渲染后焦点还在输入框」的前提就不成立，
      // 测到的会是「点按钮会抢焦点」这个与 4.3 无关的现象。这里要的是「检测在后台完成时正在输入」这个真实时序
      await page.evaluate(() => document.getElementById('recheckBtn').click())
      await waitUntil(
        async () => (await page.evaluate(() => window.__x8Old?.isConnected === false)) === true,
        15000,
        '可用性检测完成后 renderTable 重建了 DOM（旧节点脱离文档）',
        150,
      )
      const after = await input.evaluate((el) => ({
        start: el.selectionStart,
        end: el.selectionEnd,
        value: el.value,
        focused: document.activeElement === el,
      }))
      const activeNow = await page.evaluate(() => {
        const el = document.activeElement
        if (el === null) return 'null'
        return `${el.tagName}.${typeof el.className === 'string' ? el.className : ''}[key=${el.dataset?.key ?? ''}]`
      })
      expect(
        after.focused === true,
        `期望：检测完成重渲染后同一个 input 仍有焦点；实际：document.activeElement=${activeNow}，快照=${JSON.stringify(after)}`,
      )
      expect(
        after.start === before.start && after.end === before.end,
        `期望：重渲染后选区与之前一致（${before.start}-${before.end}）；实际=${after.start}-${after.end}`,
      )
    })

    await check('X9', '附加（加压）：把回给页面的 POST 响应人为延后 800ms，保存自己触发的 SSE 仍不得弹出 #conflictBar', async () => {
      await resetAndOpen()
      await ensureSseLive()
      const idx = await findRowIndexByField(page, 'real', markerA)
      expect(idx >= 0, `期望：存在 real=${markerA} 的目标行；实际=${JSON.stringify(await fieldValues(page, 'real'))}`)
      const input = inputAt(idx)
      await input.fill(`${markerA}.x9`)
      await requireVisible(page, '#saveBtn', '保存按钮')
      // 只把「回给页面的响应」拖后：route.fetch() 立刻把请求真正发出去（服务端此刻就保存并 notify），
      // 再 sleep 后才 fulfill，于是「自己保存的 SSE 事件」必定早于「POST 响应」到达页面——
      // 这正是 B5 想防的时序，只是正常本机环境下响应太快、窗口太小，需要人为放大才看得见
      await page.route('**/api/config', async (route) => {
        if (route.request().method() !== 'POST') {
          await route.continue()
          return
        }
        const response = await route.fetch()
        const body = await response.text()
        await sleep(800)
        await route.fulfill({ response, body })
      })
      await page.evaluate(() => {
        window.__x9 = []
        const el = document.getElementById('conflictBar')
        const started = performance.now()
        window.__x9Timer = setInterval(() => {
          const hidden = el === null || el.classList.contains('hidden') || getComputedStyle(el).display === 'none'
          window.__x9.push({
            t: Math.round(performance.now() - started),
            visible: hidden === false,
            text: (el?.textContent ?? '').trim(),
          })
        }, 25)
      })
      await page.locator('#saveBtn').click()
      await sleep(2800)
      const samples = await page.evaluate(() => {
        clearInterval(window.__x9Timer)
        return window.__x9
      })
      await page.unroute('**/api/config')
      const flashed = samples.filter((sample) => sample.visible)
      report.note(
        `X9 采样 ${samples.length} 次（25ms 一次），其中 #conflictBar 可见 ${flashed.length} 次；首个可见样本=${JSON.stringify(flashed[0] ?? null)}；末次样本=${JSON.stringify(samples[samples.length - 1] ?? null)}`,
      )
      expect(
        flashed.length === 0,
        `期望：响应被延后时，保存自己触发的 SSE 也不得让 #conflictBar 出现；实际：25ms 采样中可见 ${flashed.length}/${samples.length} 次，首个可见样本=${JSON.stringify(flashed[0] ?? null)}`,
      )
    })

    await check('X10', '附加（诊断）：记录 SSE updated / fetch / 提示条 class 变化的时间线，定位 B5 那一下闪烁的触发时序', async () => {
      // 探针与 B5 共用同一份实现（含 class/style 变化与 visible 判定），见 installTimelineProbe 注释
      await installTimelineProbe()
      await resetAndOpen()
      const idx = await findRowIndexByField(page, 'real', markerA)
      expect(idx >= 0, `期望：存在 real=${markerA} 的目标行；实际=${JSON.stringify(await fieldValues(page, 'real'))}`)
      await inputAt(idx).fill(`${markerA}.x10`)
      const metaRev = await page.evaluate(async () => {
        // 页面内直连 /api/meta 必须自带 token：app.js 的 apiFetch 封装在 IIFE 里，evaluate 拿不到它，
        // 因此这里自己从 location.search 取一次（webui 的门禁同时接受查询参数与 X-WebUI-Token 头）
        const token = new URLSearchParams(location.search).get('token') || ''
        const meta = await (await fetch('/api/meta', { headers: { 'X-WebUI-Token': token } })).json()
        return meta.rev ?? null
      })
      await page.evaluate(() => {
        window.__timeline.push({ t: Math.round(performance.now()), kind: 'mark-before-save-click' })
      })
      await page.locator('#saveBtn').click()
      await sleep(3000)
      const timeline = await page.evaluate(() => window.__timeline)
      report.note(`X10 诊断用 /api/meta rev=${JSON.stringify(metaRev)}`)
      report.note(`X10 时间线（t=页面内 performance.now 毫秒）=${JSON.stringify(timeline)}`)
    })

    /**
     * 派发一次「没有经过认可的拖拽起点」的 drop：用真实 DragEvent + 真实 DataTransfer，
     * 模拟用户从页面外（另一个窗口、文件管理器、聊天工具）往表格里拖东西松手。
     * 必须显式 bubbles:true —— 构造出来的 DragEvent 默认不冒泡，不冒泡就永远到不了绑定在 tr 上的 onDrop，
     * 那样断言只会「空真通过」；所以这条的非空真由「在未加守卫的旧二进制上必须失败」兜底
     */
    const dispatchForeignDrop = async (selector) => {
      const dataTransfer = await page.evaluateHandle(() => new DataTransfer())
      await page.dispatchEvent(selector, 'dragover', { dataTransfer, bubbles: true, cancelable: true })
      await page.dispatchEvent(selector, 'drop', { dataTransfer, bubbles: true, cancelable: true })
      await sleep(400)
    }

    await check('X11.1', '附加：未经过 dragstart 的 drop（模拟从页面外拖入）不得重排行序（针对 onDrop 的 dragSrcIdx<0 守卫）', async () => {
      await resetAndOpen()
      const before = await fieldValues(page, 'real')
      const rows = await page.locator('tbody tr').count()
      expect(rows === 2, `期望：该视图为 2 行；实际：${rows} 行`)
      // 落在第 1 行（idx=0）：若 onDrop 没有 dragSrcIdx<0 守卫，splice(-1,1) 会把整份数据最后一条挪到行首
      await dispatchForeignDrop('tbody tr[data-idx="0"] td.drag-handle')
      const after = await fieldValues(page, 'real')
      const dirty = await dirtyTextOf(page)
      report.note(`X11.1 外来的 drop 落在第 1 行之后：顺序=${JSON.stringify(after)}，脏计数=${JSON.stringify(dirty)}`)
      expect(
        JSON.stringify(after) === JSON.stringify(before),
        `期望：没有经过 dragstart 的 drop 不得改变行序；实际：前=${JSON.stringify(before)}，后=${JSON.stringify(after)}，脏计数=${JSON.stringify(dirty)}`,
      )
      expect(dirty.includes('0 处'), `期望：不得因此产生未保存改动；实际脏计数=${JSON.stringify(dirty)}`)
    })

    await check('X11.2', '附加：3 行时未经过 dragstart 的 drop 落在中间行，同样不得重排', async () => {
      await resetAndOpen()
      await page.locator('#addEntryBtn').click()
      await waitUntil(async () => (await page.locator('tbody tr').count()) === 3, 6000, '新增后变成 3 行')
      const before = await fieldValues(page, 'real')
      // 3 行时落在中间行：若没有守卫，最后一条（空行）会被挪到第 2 行
      await dispatchForeignDrop('tbody tr[data-idx="1"] td.drag-handle')
      const after = await fieldValues(page, 'real')
      report.note(`X11.2 3 行时外来的 drop 落在中间行之后：顺序=${JSON.stringify(after)}（拖动前=${JSON.stringify(before)}）`)
      expect(
        JSON.stringify(after) === JSON.stringify(before),
        `期望：没有经过 dragstart 的 drop 不得改变行序；实际：前=${JSON.stringify(before)}，后=${JSON.stringify(after)}`,
      )
    })

    /* ---- X12：保存窗口内继续编辑不得被误判成「已保存」（审查报告 2.1 的三条可观察量） ---- */

    /**
     * 跑一次「保存窗口内继续编辑」场景，返回三个可观察量，供 X12.1/12.2/12.3 分别断言
     *
     * 窗口怎么造：拦住 POST /api/config 的响应，但先 route.fetch() 把请求真正发出去
     * （服务端此刻就按这份请求体写盘），再用闸门卡住 fulfil 不返回，
     * 于是在「请求已发出、响应未回来」的窗口里，我可以用真实鼠标点回单元格、真实键盘继续打字。
     * 窗口长度由我控制，不与 slowMo 抢时间；用固定 sleep 造窗口会 flaky
     *
     * 结果缓存：三条断言共用同一次场景，避免把整套流程跑三遍；
     * 单独跑某一条（--only=X12.2）时缓存为空，会自己跑一遍，所以三条互相独立
     */
    let saveWindowCache = null
    const measureSaveWindow = async () => {
      if (saveWindowCache !== null) return saveWindowCache
      await resetAndOpen()
      const idx = await findRowIndexByField(page, 'real', markerA)
      expect(idx >= 0, `期望：存在 real=${markerA} 的目标行；实际=${JSON.stringify(await fieldValues(page, 'real'))}`)
      const input = inputAt(idx)
      // 先制造一处未保存改动，让保存按钮出现（这是保存窗口的前提）
      await input.click()
      await page.keyboard.press('End')
      await page.keyboard.type('.win1')
      await requireVisible(page, '#saveBtn', '保存按钮')
      const dirtyBeforeSaveText = await dirtyTextOf(page)

      const postBodies = []
      let postSeq = 0
      let postFulfills = 0
      const gateResolvers = new Map()
      const waitGate = (n) => new Promise((resolve) => gateResolvers.set(n, resolve))
      const releaseGate = (n) => {
        const resolve = gateResolvers.get(n)
        if (resolve !== undefined) {
          gateResolvers.delete(n)
          resolve()
        }
      }
      await page.route('**/api/config', async (route) => {
        if (route.request().method() !== 'POST') {
          await route.continue()
          return
        }
        postSeq += 1
        const seq = postSeq
        postBodies.push({ seq, body: route.request().postData() ?? '' })
        // 请求先真正发出去：服务端此刻就按这份请求体写盘
        const response = await route.fetch()
        const body = await response.text()
        // 响应卡在闸门后面，等窗口内的输入做完
        await waitGate(seq)
        postFulfills += 1
        await route.fulfill({ response, body })
      })

      try {
        await page.locator('#saveBtn').click()
        await waitUntil(async () => postSeq >= 1, 8000, '第一次保存请求已发出', 50)
        // 窗口内继续编辑：真实鼠标点回单元格 + 真实键盘输入
        await input.click()
        await page.keyboard.press('End')
        await page.keyboard.type('.win2')
        const midEditValue = await input.inputValue()
        // 记录「窗口确实存在」的硬证据：此刻响应尚未 fulfil 过任何一次
        const fulfillsAtMidEdit = postFulfills
        releaseGate(1)
        // 等这一次保存真正处理完（saveConfig 的 finally 会重新启用保存按钮），再如实读结果 ——
        // 这里刻意不等待「脏计数 > 0」成立：那是 X12.2 的断言对象，把等待写进场景会让
        // 修复前的二进制只得到一条「等待超时」的技术性失败，看不到真实值
        await waitUntil(async () => (await page.locator('#saveBtn').isEnabled()) === true, 8000, '第一次保存已处理完（保存按钮重新可用）', 100)
        await sleep(300)
        const dirtyAfterFirstText = await dirtyTextOf(page)
        const dirtyAfterFirstCount = await dirtyCountOf(page)
        const saveActionsAfterFirst = await page.locator('#saveActions').isVisible()
        const diskAfterFirst = readFileSync(storeFile, 'utf8')
        // 第二次保存：这次请求体里应当带上窗口内的改动
        // 修复前的实现会把脏值清零、保存按钮隐藏，这一步根本点不到按钮 ——
        // 所以先如实记录「还能不能继续保存」，让 X12.3 用自己的断言报出这个后果，而不是在场景里点空
        let secondSavePossible = false
        let diskAfterSecond = null
        let dirtyAfterSecondText = null
        let dirtyAfterSecondCount = null
        let saveActionsAfterSecond = null
        if (saveActionsAfterFirst) {
          secondSavePossible = true
          await page.locator('#saveBtn').click()
          await waitUntil(async () => postSeq >= 2, 8000, '第二次保存请求已发出', 50)
          releaseGate(2)
          await waitUntil(
            async () => (await page.locator('#saveBtn').isEnabled()) === true && postFulfills >= 2,
            8000,
            '第二次保存已处理完',
            100,
          )
          await sleep(300)
          diskAfterSecond = readFileSync(storeFile, 'utf8')
          dirtyAfterSecondText = await dirtyTextOf(page)
          dirtyAfterSecondCount = await dirtyCountOf(page)
          saveActionsAfterSecond = await page.locator('#saveActions').isVisible()
        }
        saveWindowCache = {
          dirtyBeforeSaveText,
          postBodies,
          midEditValue,
          fulfillsAtMidEdit,
          dirtyAfterFirstText,
          dirtyAfterFirstCount,
          saveActionsAfterFirst,
          diskAfterFirst,
          secondSavePossible,
          dirtyAfterSecondText,
          dirtyAfterSecondCount,
          saveActionsAfterSecond,
          diskAfterSecond,
        }
        return saveWindowCache
      } finally {
        // 无论成败都要放行所有闸门并撤掉路由，否则挂住的响应会让后续断言卡死
        releaseGate(1)
        releaseGate(2)
        await page.unroute('**/api/config')
      }
    }

    await check('X12.1', '附加（2.1 修复）：保存窗口内的编辑不得进入已发出的请求体，也不得落盘', async () => {
      const m = await measureSaveWindow()
      const first = m.postBodies[0]
      expect(first !== undefined, `期望：捕获到第一次保存的请求体；实际：捕获数量=${m.postBodies.length}`)
      report.note(
        `X12.1 第一次请求体（节选）=${first.body.slice(0, 200)}；窗口内输入后输入框值=${JSON.stringify(m.midEditValue)}；` +
          `窗口内输入完成时已 fulfil 的响应数=${m.fulfillsAtMidEdit}（0 才说明窗口真实存在）`,
      )
      expect(
        m.fulfillsAtMidEdit === 0,
        `期望：窗口内的输入必须发生在响应 fulfil 之前（否则本用例恒真）；实际：输入完成时已 fulfil ${m.fulfillsAtMidEdit} 次`,
      )
      expect(first.body.includes('.win1'), `期望：请求体含保存前已有的改动 .win1；实际请求体=${first.body.slice(0, 300)}`)
      expect(!first.body.includes('.win2'), `期望：请求体不得含窗口内输入的 .win2；实际请求体=${first.body.slice(0, 300)}`)
      expect(
        m.diskAfterFirst.includes('.win1') && !m.diskAfterFirst.includes('.win2'),
        `期望：第一次保存应把 .win1 写盘、且不得把窗口内的 .win2 写盘；实际磁盘含 .win1=${m.diskAfterFirst.includes('.win1')}，含 .win2=${m.diskAfterFirst.includes('.win2')}`,
      )
    })

    await check('X12.2', '附加（2.1 修复）：第一次保存成功后，窗口内的编辑仍显示为未保存（脏计数 > 0 且保存按钮仍可见）', async () => {
      const m = await measureSaveWindow()
      report.note(
        `X12.2 第一次保存返回后：脏计数=${JSON.stringify(m.dirtyAfterFirstText)}（${m.dirtyAfterFirstCount}），#saveActions 可见=${m.saveActionsAfterFirst}`,
      )
      expect(
        m.dirtyAfterFirstCount > 0,
        `期望：窗口内的改动仍算未保存，脏计数 > 0；实际脏计数=${m.dirtyAfterFirstCount}，文本=${JSON.stringify(m.dirtyAfterFirstText)}`,
      )
      expect(
        m.saveActionsAfterFirst === true,
        `期望：保存按钮仍可见，不得出现「脏值归零、按钮消失」的假保存；实际 isVisible=${m.saveActionsAfterFirst}（脏计数文本=${JSON.stringify(m.dirtyAfterFirstText)}）`,
      )
    })

    await check('X12.3', '附加（2.1 修复）：窗口内的编辑必须能被第二次保存真正落盘，落盘后 #saveActions 才隐藏', async () => {
      const m = await measureSaveWindow()
      // 先报「还能不能继续保存」：修复前的实现第一次保存后就把保存按钮藏了，
      // 窗口里的改动再也存不下去，这是最直接的后果，必须由本断言自己说清楚
      expect(
        m.secondSavePossible === true,
        `期望：第一次保存后仍能继续保存窗口内的改动；实际：第一次保存后 #saveActions 可见=${m.saveActionsAfterFirst}、脏计数=${JSON.stringify(m.dirtyAfterFirstText)}（改动被当成已保存，再也存不下去）`,
      )
      const second = m.postBodies[1]
      expect(second !== undefined, `期望：捕获到第二次保存的请求体；实际：捕获数量=${m.postBodies.length}`)
      report.note(
        `X12.3 第二次请求体是否含 .win2=${second.body.includes('.win2')}；第二次保存后脏计数=${JSON.stringify(m.dirtyAfterSecondText)}（${m.dirtyAfterSecondCount}），#saveActions 可见=${m.saveActionsAfterSecond}`,
      )
      expect(
        second.body.includes('.win2'),
        `期望：第二次请求体含窗口内的改动 .win2（改动没被吞掉）；实际请求体=${second.body.slice(0, 300)}`,
      )
      expect(
        m.diskAfterSecond !== null && m.diskAfterSecond.includes('.win2'),
        `期望：第二次保存后该值确实落盘；实际磁盘含 .win2=${m.diskAfterSecond !== null && m.diskAfterSecond.includes('.win2')}`,
      )
      expect(
        m.saveActionsAfterSecond === false,
        `期望：第二次保存后 #saveActions 隐藏；实际 isVisible=${m.saveActionsAfterSecond}（脏计数文本=${JSON.stringify(m.dirtyAfterSecondText)}）`,
      )
      expect(
        m.dirtyAfterSecondCount === 0,
        `期望：第二次保存后脏计数归零；实际=${m.dirtyAfterSecondCount}（文本=${JSON.stringify(m.dirtyAfterSecondText)}）`,
      )
    })

    /* ---- X13：WebUI 解除链接（行内「解除」→ 确认弹窗 → POST /api/unlink） ---- */

    /**
     * 覆盖的是「解除链接」这条网页链路里网页特有的部分：
     *   - 解除按钮只长在「检查为有效」的行上（与只长在无效行上的修复按钮方向互补）
     *   - 确认弹窗必须列出要解除的目标、且「真实删除」复选框默认不勾（危险选项不得默认选中）
     *   - 提交后：清单里那条记录消失、派生位置从链接变成一份独立真实副本
     * 服务端侧的删除策略（回收站 vs 真实删除）、并发串行化与 writer 注入由 curl 端到端覆盖，
     * 这里只跑默认策略（不勾真实删除）这一条 UI 路径，避免把同一条结论测两遍
     *
     * 位置说明：本条会改变测试数据的文件系统状态（a-link 变成真实副本），因此固定在全部断言末尾；
     * 它自己开头会 resetStore，不受前面断言的影响
     */
    await check('X13', '附加：行内「解除」→ 弹窗默认不勾真实删除 → 提交后记录消失且派生位置变成真实副本', async () => {
      await resetAndOpen()
      // 解除按钮只在「本行检查为有效」且本轮检测已完成时渲染，它的出现本身就是检测完成的信号
      await waitUntil(
        async () => (await page.locator('tbody .btn-unlink').count()) === 1,
        12000,
        '有效行出现行内解除按钮',
        200,
      )
      const repairCount = await page.locator('tbody .btn-repair').count()
      expect(repairCount === 1, `期望：无效行恰有 1 个行内修复按钮（与解除方向互补）；实际：${repairCount} 个`)

      const linkPath = join(dataDir, 'a-link')
      const idx = await findRowIndexByField(page, 'fake', linkPath)
      expect(
        idx === 0,
        `期望：a-link（有效行）位于第 1 行；实际：下标=${idx}，fake 取值=${JSON.stringify(await fieldValues(page, 'fake'))}`,
      )

      await page.locator('tbody .btn-unlink').first().click()
      await requireVisible(page, '#unlinkModal', '解除确认弹窗')

      const descText = ((await page.locator('#unlinkDesc').textContent()) ?? '').trim()
      expect(
        descText.includes('派生位置'),
        `期望：后果说明显示中文译文（页面内嵌翻译表已覆盖新文案）；实际=${JSON.stringify(descText)}`,
      )
      const confirmText = ((await page.locator('#unlinkConfirmBtn').textContent()) ?? '').trim()
      expect(confirmText === '确认解除', `期望：确认按钮文案「确认解除」；实际=${JSON.stringify(confirmText)}`)

      const checked = await page.locator('#unlinkNoTrash').isChecked()
      expect(checked === false, '期望：「真实删除」复选框默认不勾（不可恢复的危险操作不得默认选中）')
      const listText = ((await page.locator('#unlinkList').textContent()) ?? '').trim()
      report.note(`X13 弹窗列出的目标=${JSON.stringify(listText)}`)
      for (const expectedText of ['symlink', DEV, linkPath]) {
        expect(listText.includes(expectedText), `期望：弹窗里列出 ${expectedText}；实际=${JSON.stringify(listText)}`)
      }

      await page.locator('#unlinkConfirmBtn').click()
      await waitUntil(async () => await page.locator('#unlinkOutput').isVisible(), 20000, '解除结果输出出现', 200)
      const outText = ((await page.locator('#unlinkOutput').textContent()) ?? '').trim()
      report.note(`X13 服务端输出=${JSON.stringify(outText.slice(0, 200))}`)
      expect(
        outText.includes('解除成功 #1'),
        `期望：输出里出现「解除成功 #1」（被测进程以 zh-CN 运行）；实际=${JSON.stringify(outText.slice(0, 300))}`,
      )

      // 页面：那条记录从表格里消失，同一类型的另一条（无效的 b-link-missing）仍在
      await waitUntil(async () => (await page.locator('tbody tr').count()) === 1, 12000, '待解除的那一行从表格消失', 200)
      const fakes = await fieldValues(page, 'fake')
      expect(!fakes.includes(linkPath), `期望：表格里不再有 a-link；实际=${JSON.stringify(fakes)}`)

      // 磁盘清单：解除会改清单，因此这条记录必须真的从文件里消失
      const disk = JSON.parse(readFileSync(storeFile, 'utf8'))
      const symlinks = disk[PLAT][DEV][TYPE]
      expect(
        symlinks.length === 1 && symlinks.every((entry) => entry.fake !== linkPath),
        `期望：磁盘清单里 symlink 只剩 1 条且不含 a-link；实际=${JSON.stringify(symlinks)}`,
      )

      // 文件系统：a-link 从符号链接变成一份内容与权威源一致、inode 独立的真实副本
      expect(lstatSync(linkPath).isSymbolicLink() === false, '期望：a-link 不再是符号链接')
      const linkContent = readFileSync(linkPath, 'utf8')
      expect(
        linkContent === 'a real file\n',
        `期望：a-link 的内容等于权威源；实际=${JSON.stringify(linkContent)}`,
      )
      expect(
        statSync(linkPath).ino !== statSync(join(dataDir, 'a-real.txt')).ino,
        '期望：a-link 与权威源不再是同一 inode（已成为独立副本）',
      )

      await page.locator('#unlinkCloseBtn').click()
      await waitUntil(async () => (await page.locator('#unlinkModal').isVisible()) === false, 6000, '点 Close 后解除弹窗隐藏')
    })
  } catch (error) {
    report.log(`\n！！现场准备阶段失败：${error.message}`)
    setupFailed = true
    code = 1
  } finally {
    try {
      // 准备阶段失败（服务没起来/浏览器没开）时不能让 summary() 的 0 覆盖失败退出码，
      // 否则一个「什么都没跑到」的现场会以退出码 0 收场，看起来像通过
      const summaryCode = report.summary()
      code = setupFailed ? 1 : summaryCode
    } catch (error) {
      report.log(`summary 失败：${error.message}`)
      code = 1
    }
    if (browser !== undefined) {
      try {
        await browser.close()
      } catch {
        /* 浏览器已退出 */
      }
    }
    let gallery
    try {
      gallery = report.finish()
    } catch (error) {
      report.log(`生成回看页失败：${error.message}`)
    }
    // 精确按自己记录的 pid 终止被测进程，绝不做模糊匹配
    await stopProcess(child, pid, report)
    try {
      run.writeText('serve-stdout.log', stdoutChunks.join(''))
      run.writeText('serve-stderr.log', stderrChunks.join(''))
      run.writeText(
        'context.json',
        JSON.stringify(
          {
            binary: options.binary,
            label: options.label,
            pid,
            port,
            base,
            storeFile,
            dataDir,
            startupLine: startupLineSeen,
            fast: options.fast,
          },
          null,
          2,
        ),
      )
    } catch {
      /* 日志写盘失败不影响结论 */
    }
    if (gallery !== undefined) console.log(`\n回看页：${gallery}`)
    console.log(`现场目录：${run.dir}`)
    console.log(`work 目录：${workDir}（刻意保留，便于事后复现；整个目录都在 git 忽略范围内）`)
  }
  process.exitCode = code
}

/** 读文件 mtime（毫秒），读不到返回 0（只用于报告里标注二进制新旧，不参与断言） */
function statSafe(file) {
  try {
    return statSync(file).mtimeMs
  } catch {
    return 0
  }
}

/** 终止被测进程：先 SIGTERM，最多等 5s，仍在则 SIGKILL；全程只针对自己记录的 pid */
async function stopProcess(child, pid, report) {
  if (child === undefined || child.exitCode !== null || child.signalCode !== null) return
  const exited = new Promise((resolve) => child.once('exit', () => resolve(true)))
  try {
    process.kill(pid, 'SIGTERM')
  } catch (error) {
    report.note(`发送 SIGTERM 失败（进程可能已退出）：${error.message}`)
    return
  }
  const graceful = await Promise.race([exited, sleep(5000).then(() => false)])
  if (graceful === false) {
    report.note(`pid=${pid} 5s 内未退出，升级为 SIGKILL`)
    try {
      process.kill(pid, 'SIGKILL')
      await Promise.race([exited, sleep(3000)])
    } catch (error) {
      report.note(`发送 SIGKILL 失败：${error.message}`)
    }
  }
  report.note(`已终止被测进程 pid=${pid}（exitCode=${child.exitCode}）`)
}

await main()