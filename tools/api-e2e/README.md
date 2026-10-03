# flk `serve` 的 HTTP 接口端到端验收

用 bash + curl 直接打 `flk serve` 暴露的 HTTP 接口，验证**服务端行为**本身。

## 为什么需要它

`tools/e2e/`（Node + 真实 Chromium）覆盖的是**页面交互**；而有些行为只在 HTTP 层可见，浏览器断言够不着：

- `POST /api/unlink` 对三种链接类型（symlink / hardlink / copy）的处理
- 请求里的 `noTrash` 对回收站的实际影响（进回收站 vs 真实删除）
- 解除链接后清单记录被移除、且不再出现在后续查询里
- 两个并发请求不会互相踩
- writer 注入生效：服务端终端不出现本该回给页面的输出

这些断言此前只以一次性脚本的形式存在，验完即弃；固化在这里之后，每次改 serve 都能一条命令重跑。

## 用法

```sh
task verify:api
```

与 `verify:browser` 一样，它**刻意不挂在 `verify` / `default` 之下**：要构建二进制、要起真实服务，代价比静态检查高一个量级，只在改 serve 或交付前跑。

## 隔离手段

- 测试数据、store、二进制全部落在 `mktemp -d` 的临时目录里
- `XDG_DATA_HOME` 指向临时目录，回收站在临时目录内，不会碰真实家目录
- 跑完自动清理；断言失败时先把服务日志尾部打出来再退出，退出码非 0 供 Taskfile 判失败

## 目录

- `unlink.sh` —— `POST /api/unlink` 的断言集

新增同类脚本时平级放置即可（例如将来 `repair.sh`、`config.sh`）。
