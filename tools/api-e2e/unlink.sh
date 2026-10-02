#!/usr/bin/env bash
#
# flk serve HTTP 层验收：POST /api/unlink（网页版「解除链接」）
#
# 为什么要有这份脚本
#   「解除链接」的契约里有很大一部分落在服务端副作用上：回收站里到底多没多东西、清单文件里的记录
#   有没有被摘掉、两个并发请求会不会互相踩、服务端的 stdout 会不会被文件系统操作的输出污染。
#   这些浏览器 E2E（tools/e2e）看不到——它验的是页面交互；Go 单测又只能贴着函数边界打，够不着
#   HTTP 这一层。所以这里单独做一层 HTTP 断言，与 tools/e2e 各管一层、互不重叠。
#   它按需跑：只在改动 serve / unlink，或者交付前执行；刻意不挂进 verify / default，
#   因为它要构建并起一个真实服务，代价比静态检查高一个量级。
#
# 覆盖的场景（下文每个 == 小标题是一组，每组由若干条 chk 断言构成）
#   1. 初始检测：三种链接类型的记录都有效
#   2. symlink 解除（默认进回收站）：派生位置换成独立的真实文件、清单记录消失、回收站 +1、旧链接可找回
#   3. hardlink 解除（noTrash=true）：真实删除，不进回收站
#   4. copy 记录解除：只摘掉清单记录，文件系统不动
#   5. 并发解除两条不同记录：互不干扰，两条都完成
#   6. 并发解除同一条记录：恰好一个成功、一个报「找不到」，不产生重复动作
#   7. 无效记录 / 缺定位信息的请求：被拒（success=false）
#   8. 服务端终端不得出现文件系统操作的输出（writer 注入生效），且进程仍存活
#
# 隔离手段（跑测试不碰真实环境，跑完不留垃圾）
#   - 二进制、store、夹具、服务日志全放在 mktemp 出来的临时目录里，脚本退出时整体删除
#   - 回收站靠 XDG_DATA_HOME 重定向到临时目录（internal/trash 优先读 $XDG_DATA_HOME/flk/trash），
#     真实家目录 ~/.local/share/flk/trash 与用户的真实文件都不受影响
#   - 端口不写死，也不自己做「探测空闲端口」：探测用的 socket 关掉到被测进程绑定之间是有一段
#     竞态窗口的。这里反过来——给 flk 一个随机起始端口，它内部的端口策略（webui.Sequential）
#     会从该端口起顺延到可用端口，并把**实际**用上的端口与访问 token 一起打进启动摘要
#     「Service started: http://<host>:<port>/?token=<token>」，本脚本再从日志里把它读出来
#     （主机名由 webui 按实际绑定地址生成，因此解析只认「http://host:port」这个形状；
#     token 是 webui 的门禁要求，见下面 BASE 之后的取用与包装）。
#     这与 tools/e2e/verify.mjs「等启动行解析真实端口」的做法是同一套约定，且不再需要
#     python3 之类的空闲端口探测工具
#
# 断言框架
#   ok/bad 计数，chk 比较实际值与期望值。所有断言都会跑完（不中途退出）后统一汇总，
#   只要 FAIL 不为 0 就以退出码 1 结束，Taskfile 据此判定失败。
#   用法错误（参数不对、缺少依赖）以退出码 2 结束，与「断言失败」区分开
#
# 依赖
#   bash / curl / jq；未给 --binary 时还需要 go（用它构建被测二进制）
#   stat 取 inode 的参数在 GNU 与 BSD 上不同，脚本内已做兼容
#
# 用法
#   task verify:api                                  # 推荐：Taskfile 会先做依赖检查并给出安装提示
#   bash tools/api-e2e/unlink.sh                     # 直接跑：脚本从仓库根构建一份临时二进制
#   bash tools/api-e2e/unlink.sh --binary=build/flk  # 用指定二进制跑（换二进制做前后对比）
#
set -euo pipefail

usage() {
  cat <<'EOF'
用法：bash tools/api-e2e/unlink.sh [--binary=/path/to/flk]

  --binary=PATH  被测二进制；不给时用仓库根下的源码构建一份临时二进制
  -h, --help     打印本说明

对 /api/unlink 的服务端行为逐条断言，全部断言都会跑完，有失败时以退出码 1 结束；
参数或依赖有问题时以退出码 2 结束（两种情况刻意区分，便于判断是脚本没用对还是功能坏了）
EOF
}

BIN=""
for arg in "$@"; do
  case "$arg" in
    --binary=*) BIN="${arg#*=}" ;;
    -h|--help) usage; exit 0 ;;
    *) printf '未知参数：%s\n\n' "$arg" >&2; usage >&2; exit 2 ;;
  esac
done

# 依赖自检：脚本可以脱离 Taskfile 单独跑，所以自己也把话说清楚（Taskfile 那条路径另有一份
# 更友好的安装提示，两处承担的场景不同，不是重复实现）
for tool in curl jq; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    printf '缺少 %s：本脚本用它发请求 / 解析服务端返回的 JSON\n  安装（brew）：brew install %s\n' "$tool" "$tool" >&2
    exit 2
  fi
done

# 仓库根从脚本自身位置推导（本脚本位于 <repo>/tools/api-e2e/ 下），脚本内不含任何写死的绝对路径
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

# 隔离区：下面所有产物都落在这里，退出时整体删除，所以跑测试不会往仓库里留东西
WORK="$(mktemp -d "${TMPDIR:-/tmp}/flk-api-e2e.XXXXXX")"
STORE="$WORK/store/flk-store.json"
DATA="$WORK/data"
F="$WORK/files"
SERVE_PID=""
PASS=0
FAIL=0

cleanup() {
  # 先停被测进程：精确用记录的 pid，不用 pkill 之类的模糊匹配，避免误杀别人的进程
  if [ -n "$SERVE_PID" ] && kill -0 "$SERVE_PID" 2>/dev/null; then
    kill "$SERVE_PID" 2>/dev/null || true
    wait "$SERVE_PID" 2>/dev/null || true
  fi
  rm -rf "$WORK"
  echo "[clean] 已删除临时目录（含被测二进制、store、夹具、服务日志）"
}
trap cleanup EXIT

if [ -z "$BIN" ]; then
  if ! command -v go >/dev/null 2>&1; then
    printf '缺少 go：没给 --binary 时本脚本要用仓库源码构建被测二进制\n  安装（brew/mise）：brew install go\n' >&2
    exit 2
  fi
  BIN="$WORK/flk"
  # -trimpath 与 Taskfile 的构建口径一致：去掉二进制里带着的开发机绝对路径
  (cd "$REPO_ROOT" && go build -trimpath -o "$BIN" .)
fi

ok()  { PASS=$((PASS + 1)); echo "  PASS: $1"; }
bad() { FAIL=$((FAIL + 1)); echo "  FAIL: $1"; }
chk() { if [ "$2" = "$3" ]; then ok "$1（$2）"; else bad "$1（实得 $2，期望 $3）"; fi; }

mkdir -p "$WORK/store" "$F" "$DATA"
export XDG_DATA_HOME="$DATA"

# 三种链接类型各造一份真实夹具：symlink 用真的符号链接，hardlink 用真的硬链接，copy 是两个独立文件
printf 'hello-symlink\n' > "$F/real1.txt"
ln -s "$F/real1.txt" "$F/link1.txt"

printf 'hello-hardlink\n' > "$F/prim2.txt"
ln "$F/prim2.txt" "$F/seco2.txt"

printf 'hello-copy\n' > "$F/src3.txt"
cp "$F/src3.txt" "$F/dst3.txt"

cat > "$STORE" <<JSON
{
  "linux": {
    "dev": {
      "symlink": [{ "real": "$F/real1.txt", "fake": "$F/link1.txt" }],
      "hardlink": [{ "prim": "$F/prim2.txt", "seco": "$F/seco2.txt" }],
      "copy": [{ "src": "$F/src3.txt", "dst": "$F/dst3.txt" }]
    }
  }
}
JSON

# 随机起始端口：并行跑两份验收时不会总是从同一个端口开始互相顺延；实际端口从启动摘要里读
PORT_BASE=$(( (RANDOM % 2000) + 18000 ))
"$BIN" --store-path "$STORE" serve --port "$PORT_BASE" --no-open --lang en > "$WORK/serve.log" 2>&1 &
SERVE_PID=$!

# --lang en 是刻意的：第 8 节要断言服务端不会把文件系统操作的英文文案打出来，
# 用英文语言包时这些文案与 l10n 的英文源串逐字一致，断言才有意义
PORT=""
for _ in $(seq 1 150); do
  LINE="$(grep -oE 'https?://[^/[:space:]]+:[0-9]+' "$WORK/serve.log" | head -n 1 || true)"
  if [ -n "$LINE" ]; then PORT="${LINE##*:}"; break; fi
  # 进程都已经不在了就没必要继续等，直接跳出走下面的失败分支
  kill -0 "$SERVE_PID" 2>/dev/null || break
  sleep 0.1
done
if [ -z "$PORT" ]; then
  echo "服务未打印启动摘要（可能启动即失败），日志如下：" >&2
  cat "$WORK/serve.log" >&2
  exit 1
fi
BASE="http://localhost:$PORT"

# webui 的 tokenGate 保护 "/" 与 "/api/" 前缀，所有 /api/ 请求都必须带 token。
# 用一层同名函数携带凭据：脚本里有十几处 curl 调用，逐处补参数既啰嗦又必然漏掉某一处，
# 而漏掉的那处会以 401 的形式表现为「断言值不对」，非常难查。
# 走请求头而不是查询参数：下面有些调用自带 query string，拼查询参数更容易出错
TOKEN="$(grep -oE 'token=[A-Za-z0-9_-]+' "$WORK/serve.log" | head -n 1 | cut -d= -f2 || true)"
if [ -z "$TOKEN" ]; then
  echo "启动摘要里没有 token，无法访问受保护的 /api/ 端点，日志如下：" >&2
  cat "$WORK/serve.log" >&2
  exit 1
fi
curl() { command curl -H "X-WebUI-Token: $TOKEN" "$@"; }

# 启动摘要出现不等于 HTTP 已经能服务，再探一次 /api/meta
for _ in $(seq 1 150); do
  if curl -sf "$BASE/api/meta" > /dev/null 2>&1; then break; fi
  sleep 0.1
done
if ! curl -sf "$BASE/api/meta" > /dev/null 2>&1; then
  echo "服务未就绪，日志如下：" >&2
  cat "$WORK/serve.log" >&2
  exit 1
fi
echo "[setup] 服务已就绪 pid=$SERVE_PID $BASE，store=$STORE"

post() { curl -s -X POST "$BASE/api/unlink" -H 'Content-Type: application/json' -d "$1"; }

# 只统计回收站里的叶子条目（文件/符号链接），不含沿途被创建的目录，否则计数会随路径深度变化
trash_count() { { find "$DATA/flk/trash" -mindepth 1 ! -type d 2>/dev/null || true; } | wc -l | tr -d ' '; }

# 取 inode 用于判断「是不是同一个文件」：GNU stat 与 BSD stat 的参数不同，这里做兼容
inode_of() { stat -c %i "$1" 2>/dev/null || stat -f %i "$1"; }

# 用 POST /api/config 安装新清单：这条路径会同时改内存与磁盘，避免直接改文件后
# 还要等 1 秒轮询才可见（那会让后续请求基于过期内存，测出假失败）
set_store() { curl -s -X POST "$BASE/api/config" -H 'Content-Type: application/json' -d "$1" | jq -c '{success}'; }

# 轮询等待服务端的有效性检测达到期望条数，避免刚写完清单就发请求
wait_valid() {
  for _ in $(seq 1 50); do
    [ "$(curl -s "$BASE/api/check" | jq '[.results[]|select(.valid)]|length')" = "$1" ] && return 0
    sleep 0.1
  done
  return 1
}

echo "== 1. 初始检测：三条记录都应有效 =="
CHECK=$(curl -s "$BASE/api/check")
chk "初始有效条目数" "$(echo "$CHECK" | jq '[.results[]|select(.valid)]|length')" "3"

echo "== 2. symlink 解除（noTrash=false，默认进回收站）=="
T0=$(trash_count)
RESP=$(post "{\"device\":\"dev\",\"type\":\"symlink\",\"fields\":{\"real\":\"$F/real1.txt\",\"fake\":\"$F/link1.txt\"},\"noTrash\":false}")
echo "$RESP" | jq -c '{success, output}'
chk "响应 success" "$(echo "$RESP" | jq -r .success)" "true"
chk "派生位置已不是符号链接" "$([ -L "$F/link1.txt" ] && echo yes || echo no)" "no"
chk "派生位置是真实文件" "$([ -f "$F/link1.txt" ] && echo yes || echo no)" "yes"
chk "派生位置内容等于权威源" "$(cat "$F/link1.txt")" "hello-symlink"
chk "派生位置与权威源 inode 不同" "$([ "$(inode_of "$F/link1.txt")" = "$(inode_of "$F/real1.txt")" ] && echo same || echo diff)" "diff"
chk "清单里 symlink 记录已消失" "$(jq '.linux.dev.symlink|length' "$STORE")" "0"
chk "回收站新增了 1 条" "$(( $(trash_count) - T0 ))" "1"
chk "旧链接确实进了回收站" "$(find "$DATA/flk/trash" -path "*files/link1.txt" | wc -l | tr -d ' ')" "1"
chk "再检测时该条目不再出现" "$(curl -s "$BASE/api/check" | jq "[.results[]|select(.fake==\"$F/link1.txt\")]|length")" "0"

echo "== 3. hardlink 解除（noTrash=true，真实删除不进回收站）=="
T0=$(trash_count)
RESP=$(post "{\"device\":\"dev\",\"type\":\"hardlink\",\"fields\":{\"prim\":\"$F/prim2.txt\",\"seco\":\"$F/seco2.txt\"},\"noTrash\":true}")
echo "$RESP" | jq -c '{success, output}'
chk "响应 success" "$(echo "$RESP" | jq -r .success)" "true"
chk "派生位置内容等于权威源" "$(cat "$F/seco2.txt")" "hello-hardlink"
chk "派生位置不再是硬链接（inode 不同）" "$([ "$(inode_of "$F/seco2.txt")" = "$(inode_of "$F/prim2.txt")" ] && echo same || echo diff)" "diff"
chk "清单里 hardlink 记录已消失" "$(jq '.linux.dev.hardlink|length' "$STORE")" "0"
chk "noTrash=true 时回收站没有新增" "$(( $(trash_count) - T0 ))" "0"

echo "== 4. copy 记录解除（无文件系统动作）=="
T0=$(trash_count)
RESP=$(post "{\"device\":\"dev\",\"type\":\"copy\",\"fields\":{\"src\":\"$F/src3.txt\",\"dst\":\"$F/dst3.txt\"},\"noTrash\":false}")
echo "$RESP" | jq -c '{success, output}'
chk "响应 success" "$(echo "$RESP" | jq -r .success)" "true"
chk "副本内容未被改动" "$(cat "$F/dst3.txt")" "hello-copy"
chk "清单里 copy 记录已消失" "$(jq '.linux.dev.copy|length' "$STORE")" "0"
chk "回收站无新增" "$(( $(trash_count) - T0 ))" "0"

echo "== 5. 并发：两条不同记录同时解除 =="
printf 'c4\n' > "$F/real4.txt"; ln -s "$F/real4.txt" "$F/link4.txt"
printf 'c5\n' > "$F/real5.txt"; ln -s "$F/real5.txt" "$F/link5.txt"
set_store "{\"linux\":{\"dev\":{\"symlink\":[{\"real\":\"$F/real4.txt\",\"fake\":\"$F/link4.txt\"},{\"real\":\"$F/real5.txt\",\"fake\":\"$F/link5.txt\"}],\"hardlink\":[],\"copy\":[]}}}"
wait_valid 2 || { echo "夹具未就绪"; exit 1; }
curl -s -X POST "$BASE/api/unlink" -H 'Content-Type: application/json' \
  -d "{\"device\":\"dev\",\"type\":\"symlink\",\"fields\":{\"real\":\"$F/real4.txt\",\"fake\":\"$F/link4.txt\"},\"noTrash\":false}" > "$WORK/r4.json" &
P1=$!
curl -s -X POST "$BASE/api/unlink" -H 'Content-Type: application/json' \
  -d "{\"device\":\"dev\",\"type\":\"symlink\",\"fields\":{\"real\":\"$F/real5.txt\",\"fake\":\"$F/link5.txt\"},\"noTrash\":false}" > "$WORK/r5.json" &
P2=$!
# 请求本身失败（比如连接被拒）时不让 wait 的非零状态在 set -e 下直接终止脚本，
# 交给下面的断言把「实得空值」报出来，诊断信息才完整
wait "$P1" "$P2" || true
echo "  请求4：$(jq -c '{success,output}' "$WORK/r4.json" 2>/dev/null || cat "$WORK/r4.json")"
echo "  请求5：$(jq -c '{success,output}' "$WORK/r5.json" 2>/dev/null || cat "$WORK/r5.json")"
chk "并发请求 4 success" "$(jq -r .success "$WORK/r4.json")" "true"
chk "并发请求 5 success" "$(jq -r .success "$WORK/r5.json")" "true"
chk "两条都变成真实文件" "$([ ! -L "$F/link4.txt" ] && [ ! -L "$F/link5.txt" ] && echo yes || echo no)" "yes"
chk "内容分别是各自的权威源内容" "$(cat "$F/link4.txt")/$(cat "$F/link5.txt")" "c4/c5"
chk "清单里两条记录都已消失" "$(jq '.linux.dev.symlink|length' "$STORE")" "0"

echo "== 6. 并发：同一条记录被两个请求同时解除 =="
printf 'c6\n' > "$F/real6.txt"; ln -s "$F/real6.txt" "$F/link6.txt"
set_store "{\"linux\":{\"dev\":{\"symlink\":[{\"real\":\"$F/real6.txt\",\"fake\":\"$F/link6.txt\"}],\"hardlink\":[],\"copy\":[]}}}"
wait_valid 1 || { echo "夹具未就绪"; exit 1; }
BODY="{\"device\":\"dev\",\"type\":\"symlink\",\"fields\":{\"real\":\"$F/real6.txt\",\"fake\":\"$F/link6.txt\"},\"noTrash\":false}"
curl -s -X POST "$BASE/api/unlink" -H 'Content-Type: application/json' -d "$BODY" > "$WORK/r6a.json" &
P1=$!
curl -s -X POST "$BASE/api/unlink" -H 'Content-Type: application/json' -d "$BODY" > "$WORK/r6b.json" &
P2=$!
wait "$P1" "$P2" || true
S6=$(jq -r .success "$WORK/r6a.json")/$(jq -r .success "$WORK/r6b.json")
chk "恰好一个成功、一个报找不到（无重复动作）" "$(echo "$S6" | tr '/' '\n' | sort | tr '\n' ' ' | sed 's/ *$//')" "false true"
chk "派生位置已不是符号链接" "$([ -L "$F/link6.txt" ] && echo yes || echo no)" "no"
chk "派生位置是内容正确的真实文件" "$(cat "$F/link6.txt")" "c6"
chk "清单里该记录已消失" "$(jq '.linux.dev.symlink|length' "$STORE")" "0"
echo "  第二个请求的输出：$(jq -r .output "$WORK/r6b.json" | head -3 | tr '\n' '|')"

echo "== 7. 无效记录 / 无效请求 =="
RESP=$(post "{\"device\":\"dev\",\"type\":\"symlink\",\"fields\":{\"real\":\"/nonexistent\",\"fake\":\"/nonexistent\"},\"noTrash\":false}")
chk "无效记录解除被拒" "$(echo "$RESP" | jq -r .success)" "false"
RESP=$(post "{\"type\":\"symlink\",\"noTrash\":false}")
chk "缺定位信息的请求被拒" "$(echo "$RESP" | jq -r .success)" "false"

echo "== 8. 服务端终端不得出现文件系统操作的输出（writer 注入生效）=="
# 被解除的路径与「删除/移入回收站」这类文案，都是 internal/safeop、internal/trash 里会打到 stdout 的东西；
# 它们一旦出现在服务日志里，说明 HTTP 端点没有把自己的输出重定向进响应缓冲区，服务端终端会被污染
chk "日志里不含被解除的路径" "$(grep -c -e "$F/link1.txt" -e "$F/seco2.txt" -e "$F/link6.txt" "$WORK/serve.log" || true)" "0"
chk "日志里不含删除计划文案" "$(grep -c -e 'moved to the trash' -e 'will be deleted' -e 'Deleting' "$WORK/serve.log" || true)" "0"
chk "服务进程仍存活" "$(kill -0 "$SERVE_PID" 2>/dev/null && echo alive || echo dead)" "alive"

if [ "$FAIL" -ne 0 ]; then
  echo
  echo "===== 失败诊断：服务端日志末尾 20 行 ====="
  tail -n 20 "$WORK/serve.log"
fi

echo
echo "===== 结果：PASS=$PASS FAIL=$FAIL ====="
# 最后一条命令的返回值就是脚本的退出码：FAIL>0 时返回 1，Taskfile 据此判定失败
[ "$FAIL" -eq 0 ]
