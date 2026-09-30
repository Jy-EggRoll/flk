/**
 * 外部可执行文件的定位。
 *
 * 这里只放「两个层都要用、且必须完全一致」的那一件事：在 PATH 上找命令。
 * 浏览器层要找 Chromium，录屏转码层要找 ffmpeg；两边都得走**登录 shell**，
 * 因为本机工具链由 mise 管理，`ffmpeg` 这类命令只在登录 shell 里才进 PATH，
 * 直接读 `process.env.PATH` 会找不到。
 */

import { spawnSync } from 'node:child_process'

/**
 * 在登录 shell 的 PATH 上找一个可执行文件。
 *
 * 命令名通过位置参数传给 shell（`$0`），不拼进脚本文本里，避免命令名影响脚本本身。
 * @param {string} command - 命令名，例如 `ffmpeg`。
 * @returns {string | undefined} 绝对路径；找不到返回 undefined。
 */
export function which(command) {
  const result = spawnSync('bash', ['-lc', 'command -v -- "$0"', command], { encoding: 'utf8' })
  const path = result.stdout?.trim()
  return path !== undefined && path !== '' ? path : undefined
}