#!/bin/bash
# SwarmMind 备份脚本（maturity P0 备份与恢复条目）：备份 hub/node 的 data_dir 与配置文件。
# 用法：
#   scripts/backup.sh --data-dir <路径> [--config <文件>]... [--out <备份目录>] [--no-home] [--no-logs]
#   scripts/backup.sh --help
# 行为（详见 docs/OPERATIONS.md「备份与恢复」）：
#   - 停机目标：脚本对 data_dir/lock 非阻塞抢锁并全程持有至脚本退出（探测即持锁，
#     备份期间实例无法启动，杜绝「探测说停机、复制中实例启动」的竞态）；全量拷贝，
#     hub_state.db 连同 -wal/-shm 三件套同拷（恢复时合并回放）。
#   - 运行中目标：db 经 sqlite3 ".backup" 取一致快照（-wal/-shm 不拷，避免过时
#     wal 混入快照）；其余文件均为 temp+rename 原子写，照拷安全。运行中直拷路径
#     绝不触碰 db；备份期间新出现的 db/账本会被出现性检查拦截，拒绝交付。
#   - 运行中且 sqlite3 缺失：报错退出。绝不产出静默坏备份——运行中直接 cp
#     hub_state.db 会得到 integrity_check=ok 的空库（活数据在未拷走的 -wal）。
#   - lock（单实例锁）与 *.tmp / *.tmp-*（原子写临时文件，含 local_token.tmp-* 等
#     CreateTemp 形态）不备份；--no-home/--no-logs 跳过 node data_dir 下的大目录。
#   - 备份目录 0700、文件 0600：创建任何产物之前先收 umask 077，复制期间即受保护；
#     产物含 admin_token、TLS 私钥、节点令牌，敏感等同生产凭据。
#   - 显式 --config 缺失、--config 平铺同名冲突、备份目录已存在：一律报错退出，
#     不静默跳过、不静默覆盖。
# 中途失败或被 INT/TERM/HUP 中断：自动删除本次排他创建的备份目录，不留可误信的残缺产物。
set -eu

# 权限保护前移：先收 umask 再创建任何产物，备份目录从 mkdir 起就是 0700、文件从
# 落盘起就是 0600，不给凭据留下「复制期间松权限」的窗口；收尾 chmod 仅作兜底
# （拷入文件可能继承源文件权限位）。
umask 077

err() { echo "backup.sh: $*" >&2; }
usage() { grep '^# ' "$0" | sed 's/^# \{0,1\}//'; }

DATA_DIR=""
OUT=""
NO_HOME=0
NO_LOGS=0
CONFIGS=()

while [ $# -gt 0 ]; do
  case "$1" in
    --data-dir)
      [ $# -ge 2 ] || { err "--data-dir 需要路径参数"; exit 2; }
      DATA_DIR="$2"; shift 2 ;;
    --config)
      [ $# -ge 2 ] || { err "--config 需要文件参数"; exit 2; }
      CONFIGS+=("$2"); shift 2 ;;
    --out)
      [ $# -ge 2 ] || { err "--out 需要目录参数"; exit 2; }
      OUT="$2"; shift 2 ;;
    --no-home) NO_HOME=1; shift ;;
    --no-logs) NO_LOGS=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) err "未知参数 $1（--help 看用法）"; exit 2 ;;
  esac
done

[ -n "$DATA_DIR" ] || { err "缺 --data-dir"; usage >&2; exit 2; }
[ -d "$DATA_DIR" ] || { err "data_dir 不存在：$DATA_DIR"; exit 2; }
DATA_DIR=$(cd "$DATA_DIR" && pwd)
DB="$DATA_DIR/hub_state.db"
LOCK="$DATA_DIR/lock"
LEDGER="$DATA_DIR/ledger.json"

if [ -n "$OUT" ]; then
  case "$OUT" in
    "$DATA_DIR"|"$DATA_DIR"/*)
      err "--out 不能在 data_dir 内（备份目录会被当作源条目拷进自身）：$OUT"; exit 2 ;;
  esac
fi

# —— 显式请求路径先校验（在任何产物创建之前）——
# 显式 --config 缺失是用户明确要备的文件不在，静默跳过会产出「以为备全了」的假备份；
# 平铺同名冲突会静默覆盖（后者吃前者）。都提前报错退出。
CONFIG_NAMES="|"
for c in ${CONFIGS[@]+"${CONFIGS[@]}"}; do
  [ -f "$c" ] || { err "--config 指定的文件不存在：$c"; exit 2; }
  b=$(basename "$c")
  case "$CONFIG_NAMES" in
    *"|$b|"*) err "--config 基名重复（备份目录平铺存放，同名会互相覆盖）：$b"; exit 2 ;;
  esac
  CONFIG_NAMES="$CONFIG_NAMES$b|"
done

# —— 失败收口全覆盖（EXIT + INT/TERM/HUP）——
# ERR trap 只拦命令失败，拦不到 SIGTERM/SIGINT/SIGHUP 的默认杀死行为（实测 TERM 后
# 半成品残留）。统一走退出钩子：成功标记 DONE 未置位时，删掉本次排他创建的备份
# 目录——目录只在 mkdir 排他成功后才存在，清理不可能误伤他人目录。
DONE=0
OUT_OWNED=0
cleanup_partial() {
  if [ -n "${LOCK_HOLDER_PID:-}" ]; then
    kill "$LOCK_HOLDER_PID" 2>/dev/null || true
  fi
  # 只清理本次排他创建成功的目录（OUT_OWNED）：OUT 已存在导致的退出绝不能
  # 把别人的目录当半成品删掉（codex ② 实测复现过删他人标记文件）。
  if [ "${DONE:-0}" = 0 ] && [ "${OUT_OWNED:-0}" = 1 ] && [ -n "${OUT:-}" ] && [ -d "$OUT" ]; then
    rm -rf -- "$OUT"
    err "备份失败或被中断，已删除半成品目录：$OUT"
  fi
}
# 入口先取原退出码、钩子尾显式 exit 还原：正常完成清理路径下防 cleanup_partial
# 内命令失败（如 rm 被拒）以自身状态覆盖退出码、失败路径假报成功。注意 set -e
# 下若清理中途失败，脚本会提前以该失败状态退出、走不到末尾 exit "$es"——显式
# 还原只覆盖「清理成功完成」的场景，不覆盖「清理自身失败」。
trap 'es=$?; cleanup_partial; exit "$es"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP

# —— 运行检测与全程持锁 ——
# hub/node 运行期持有 data_dir/lock 的 flock，进程退出自动释放（锁文件本身留存）。
# 停机判定必须是「非阻塞抢到锁并持有到备份结束」：探测完立刻释放的旧做法存在竞态
# ——探测说停机、复制期间实例启动，停机全量拷贝就会拷到运行中的 db。锁文件不存在
# 不直接判停机：属主相同时补建后持锁（实例启动同样是先建文件后抢锁，本脚本抢到后
# 实例会在同一文件上冲突失败，单实例保护语义不变）；属主不同（实例可能以另一用户
# 运行，异主锁文件会让实例之后打不开自己的锁）不补建，走保守裁决。
# flock（Linux）→ python3 fcntl（macOS，由后台进程代持至脚本结束）→ lsof 三级；
# 全缺失时按「运行中」保守处理（宁可多拦，不产坏备份）。
RUNNING=""
HELD=0
LOCK_HOLDER_PID=""

LOCK_ABSENT=0
if [ ! -e "$LOCK" ]; then
  if [ -O "$DATA_DIR" ] && [ -w "$DATA_DIR" ] && : >> "$LOCK" 2>/dev/null; then
    : # 已补建锁文件，走下方正常持锁探测
  else
    LOCK_ABSENT=1
  fi
fi

if [ "$LOCK_ABSENT" = 1 ]; then
  # 无法持锁：锁文件缺失且 db 存在属异常态（正常停机实例会留下锁文件），绝不判停机；
  # 无 db（实例从未运行）才按停机直拷，实例若在备份期间首启，复制后的出现性检查拦截。
  if [ -f "$DB" ]; then
    RUNNING=unknown
  else
    RUNNING=stopped
    err "警告：$LOCK 不存在且无法补建（data_dir 属主非当前用户），无法持锁；目录无 db，按从未运行处理。"
  fi
elif command -v flock >/dev/null 2>&1; then
  # stderr 抑制只作用于「打开 fd9」这一个动作（组命令作用域）：直接写
  # exec 9<"$LOCK" 2>/dev/null 会在成功路径把当前 shell 的 stderr 永久重定向进
  # /dev/null（实测），此后缺 sqlite3 等报错与指引全部不可见。
  if { exec 9<"$LOCK"; } 2>/dev/null; then
    if flock -n 9 2>/dev/null; then
      RUNNING=stopped  # 锁由本 shell 的 fd9 持有，脚本退出自动释放
      HELD=1
    else
      RUNNING=running
    fi
  else
    RUNNING=unknown    # 锁文件打不开（权限异常），无法持锁
  fi
elif command -v python3 >/dev/null 2>&1; then
  # macOS 无 flock(1)：后台 python 代持锁，退出钩子统一清理（持锁到结束）。
  # 忽略 INT/HUP：终端 Ctrl-C 会连后台进程一起投递，若代持进程先死，锁提前
  # 释放、停机一致性失守；只认退出钩子显式发的 TERM。
  LOCK_STATE=$(mktemp "${TMPDIR:-/tmp}/swarmmind-backup-lockstate.XXXXXX")
  python3 -c 'import fcntl,sys,time,signal
signal.signal(signal.SIGINT, signal.SIG_IGN)
signal.signal(signal.SIGHUP, signal.SIG_IGN)
f = open(sys.argv[1], "a+")
try:
    fcntl.flock(f, fcntl.LOCK_EX | fcntl.LOCK_NB)
except OSError:
    print("running", flush=True)
    sys.exit(0)
print("stopped", flush=True)
while True:
    time.sleep(3600)  # 代持锁直至被主进程清理
' "$LOCK" > "$LOCK_STATE" 2>/dev/null &
  LOCK_HOLDER_PID=$!
  tries=0
  while [ ! -s "$LOCK_STATE" ] && kill -0 "$LOCK_HOLDER_PID" 2>/dev/null && [ "$tries" -lt 100 ]; do
    sleep 0.05
    tries=$((tries+1))
  done
  RUNNING=$(head -n 1 "$LOCK_STATE" 2>/dev/null || true)
  rm -f "$LOCK_STATE"
  case "$RUNNING" in
    stopped) HELD=1 ;;
    running) LOCK_HOLDER_PID="" ;;  # 未抢到锁，代持进程正常退出，使命已毕
    *)
      # 探测超时/无裁决（代持进程可能仍在跑）：必须 kill 并 wait 回收后再清 PID，
      # 不能清空 PID 了事——被弃置的代持进程稍后仍会抢到锁并按 while True 永久
      # 持有（实测注入 7 秒启动延迟：脚本按 unknown 保守路径成功退出后，延迟进程
      # 取得锁、外部 flock 被拒，实例从此无法启动）。kill 用 TERM（代持进程仅忽略
      # INT/HUP），wait 确认已终止——flock 随进程终止自动释放；RUNNING 置空后由
      # 下游按「状态未知」保守处理。
      kill "$LOCK_HOLDER_PID" 2>/dev/null || true
      wait "$LOCK_HOLDER_PID" 2>/dev/null || true
      RUNNING=""; LOCK_HOLDER_PID="" ;;
  esac
elif command -v lsof >/dev/null 2>&1; then
  # lsof 只能探测、无法代持锁：探测说「无人持有」也不可信（下一刻实例即可启动）。
  # 有 db 时绝不据此判停机（宁可走在线快照或拒绝）；无 db 时即便实例正在首启，
  # 复制后的出现性检查也会拦截。
  if [ -n "$(lsof -t -- "$LOCK" 2>/dev/null)" ]; then
    RUNNING=running
  elif [ -f "$DB" ]; then
    RUNNING=unknown
  else
    RUNNING=stopped
    err "警告：无 flock/python3 可用（仅 lsof 探测），无法全程持锁；目录无 db 才按停机处理，实例若在备份中启动会在复制后拦截。"
  fi
fi
[ -n "$RUNNING" ] || RUNNING=unknown

# —— 在线/停机裁决：必须在创建备份目录之前，缺 sqlite3 时干净退出 ——
ONLINE_DB=0
if [ "$RUNNING" != stopped ]; then
  if [ "$RUNNING" = unknown ]; then
    err "警告：无法确认目标是否运行中（锁状态未知），按运行中处理（宁可多拦，不产坏备份）。"
  fi
  if [ -f "$DB" ]; then
    if command -v sqlite3 >/dev/null 2>&1; then
      ONLINE_DB=1
    else
      err "目标疑似运行中（$LOCK 被持有或状态未知）且 sqlite3 不可用，拒绝备份。"
      err "运行中直接拷 hub_state.db 会得到 integrity_check=ok 的空库（活数据在未拷走的 -wal）。"
      err "二选一：① 安装 sqlite3 后重跑（在线一致快照）；② 停机后重跑（全量拷贝）。"
      exit 1
    fi
  fi
  if [ -f "$DB" ]; then
    err "提示：$DATA_DIR 疑似运行中，db 走 sqlite3 在线快照。在线备份次于停机备份，条件允许请停机后重跑。"
  else
    err "提示：$DATA_DIR 疑似运行中且无 hub_state.db（node 目录，或 hub 尚未建库），清单文件照拷（均为 temp+rename 原子写）。"
  fi
fi

TS=$(date +%Y%m%d-%H%M%S)
if [ -z "$OUT" ]; then
  OUT="$(pwd)/$(basename "$DATA_DIR")-backup-$TS"
fi
# —— 排他创建，检查与创建合一 ——
# mkdir 不带 -p、失败不重试：目录已存在（含与检查窗口竞态被并行进程抢先创建）一律
# 失败，不存在「吞掉他人目录」的路径；创建成功即本次独占拥有，此后的失败清理仅限
# 本目录（见 cleanup_partial）。
if ! mkdir "$OUT" 2>/dev/null; then
  err "无法创建备份目录：${OUT}（目录须不存在；父目录须已存在；已存在请换 --out 或清理后重试）"
  exit 2
fi
OUT_OWNED=1

if [ "$ONLINE_DB" = 1 ]; then
  MODE="在线快照（目标运行中，db 经 sqlite3 .backup 合并 wal）"
elif [ "$RUNNING" != stopped ]; then
  MODE="运行中直拷（无 db，清单文件均为 temp+rename 原子写）"
elif [ "$HELD" = 1 ]; then
  MODE="停机全量拷贝（本脚本已持 data_dir 锁，备份期间实例无法启动）"
else
  MODE="停机全量拷贝（无锁文件可持，按从未运行处理；实例若在备份中启动会被出现性检查拦截）"
fi
echo "[backup] 模式：$MODE"
echo "[backup] 源：$DATA_DIR"

# 运行中直拷的安全守卫开关：停机且持锁时实例动不了，无需守卫；其余一切
# 「没有以持锁保证停机」的直拷场景（运行中无 db / 状态未知 / 无法持锁的停机）
# 都要防 db/账本在备份窗口内出现。
GUARD=0
if [ "$ONLINE_DB" = 0 ] && { [ "$RUNNING" != stopped ] || [ "$HELD" = 0 ]; }; then
  GUARD=1
fi

# db 单独处理（热拷陷阱正解）；其余 data_dir 条目循环拷贝。
if [ "$ONLINE_DB" = 1 ]; then
  SNAP="$OUT/hub_state.db"
  # 快照文件名不进点命令参数：路径含撇号/空格等特殊字符时，sqlite3 点命令的
  # 参数切分会把 '' 转义误当引号闭合（实测含撇号路径报 Usage: .backup / cannot
  # open）。改为进入备份目录用固定名执行——快照路径不含任何特殊字符，db 路径
  # 走 argv 不经点命令解析。
  if ! ( cd "$OUT" && sqlite3 "$DB" ".backup hub_state.db" ); then
    err "sqlite3 在线快照失败：$DB"
    exit 1
  fi
  if [ ! -f "$SNAP" ]; then
    err "sqlite3 在线快照未产生目标文件：$SNAP"
    exit 1
  fi
  QUICK=$(sqlite3 "$SNAP" "PRAGMA quick_check;" 2>&1 || true)
  if [ "$QUICK" != ok ]; then
    err "快照校验异常（quick_check=${QUICK}），拒绝交付：$SNAP"
    exit 1
  fi
  echo "[backup] db 快照完成且 quick_check=ok"
  # sqlite3 对 WAL 源做 .backup 后，目标库也带 WAL 头，连接关闭会留下空 -wal/-shm：
  # 快照必须自包含，副作用文件一律清除（空 wal 无害但会误导「要不要一起恢复」的判断）。
  rm -f "$OUT/hub_state.db-wal" "$OUT/hub_state.db-shm"
fi

for entry in "$DATA_DIR"/*; do
  name=$(basename "$entry")
  # lock 是运行时单实例锁文件；*.tmp 与 *.tmp-* 是原子写临时文件（persist 的
  # path+".tmp" 与 os.CreateTemp 的 "<名>.tmp-随机" 形态，如 local_token.tmp-*、
  # prime_token.tmp-*），均为中间态不备份。
  case "$name" in
    lock|*.tmp|*.tmp-*) continue ;;
  esac
  if [ "$ONLINE_DB" = 1 ]; then
    # 在线快照已含 db 数据，活跃源 db 与 -wal/-shm 不再照拷。
    case "$name" in
      hub_state.db|hub_state.db-wal|hub_state.db-shm) continue ;;
    esac
  elif [ "$GUARD" = 1 ]; then
    # 直拷场景绝不触碰 db：裁决时无 db、循环时出现 = 实例启动窗口竞态，
    # 普通拷贝运行中的 db 会得到校验通过的空库，必须拦截。
    case "$name" in
      hub_state.db|hub_state.db-wal|hub_state.db-shm)
        err "目标运行中或状态未知，且 hub_state.db 在备份期间出现，拒绝不安全拷贝；请停机后重跑。"
        exit 1 ;;
    esac
  fi
  if [ -d "$entry" ]; then
    if [ "$name" = home ] && [ "$NO_HOME" = 1 ]; then
      echo "[backup] 跳过 home/（--no-home）"
      continue
    fi
    if [ "$name" = logs ] && [ "$NO_LOGS" = 1 ]; then
      echo "[backup] 跳过 logs/（--no-logs）"
      continue
    fi
    cp -R "$entry" "$OUT/"
  else
    cp "$entry" "$OUT/"
  fi
done

# 出现性复查：实例若在「裁决（无 db）」之后才初始化，db/账本会在复制窗口内出现。
# db 已在循环内拦截；这里拦「glob 没赶上」的漏网文件——清单缺件即残缺备份，
# 恢复后会静默丢状态，拒绝交付。
if [ "$GUARD" = 1 ]; then
  for late in "$DB" "$LEDGER"; do
    if [ -e "$late" ] && [ ! -e "$OUT/$(basename "$late")" ]; then
      err "目标在备份期间出现了 ${late}（疑实例启动窗口），未被一致捕获，拒绝交付残缺备份；请停机后重跑。"
      exit 1
    fi
  done
fi

# 配置文件（data_dir 之外，按清单单独备份），平铺放进备份目录。
# 存在性与配置间同名冲突已提前校验退出；这里拦与 data_dir 清单条目的同名覆盖。
for c in ${CONFIGS[@]+"${CONFIGS[@]}"}; do
  b=$(basename "$c")
  if [ -e "$OUT/$b" ]; then
    err "--config 与 data_dir 条目同名，平铺存放会覆盖：${b}；请确认清单或更换文件名。"
    exit 1
  fi
  cp "$(cd "$(dirname "$c")" && pwd)/$(basename "$c")" "$OUT/"
done

# 锁代持进程（python 后台）必须活到最后：中途被杀 = 锁提前释放 = 停机一致性失守，
# 宁可拒绝交付也不留「以为停机备的」坏备份。
if [ -n "$LOCK_HOLDER_PID" ] && ! jobs -pr 2>/dev/null | grep -qx "$LOCK_HOLDER_PID"; then
  err "锁代持进程意外退出，无法保证停机一致性，拒绝交付；请重跑。"
  exit 1
fi

# 权限兜底收紧：umask 077 已保证新落盘文件 0600、目录 0700；拷入内容可能继承
# 源文件权限位，统一收一遍（产物含凭据与私钥，见 OPERATIONS.md 清单敏感性标注）。
chmod 700 "$OUT"
find "$OUT" -type d -exec chmod 700 {} +
find "$OUT" -type f -exec chmod 600 {} +

NFILES=$(find "$OUT" -type f | wc -l | tr -d ' ')
DONE=1
echo "[backup] 备份完成：$OUT （共 $NFILES 个文件）"
echo "[backup] 注意：产物含 admin_token、TLS 私钥、节点令牌，勿入公共网盘或仓库。"
echo "[backup] 恢复步骤见 docs/OPERATIONS.md「备份与恢复」。"
