const fs = require('fs');
const vm = require('vm');
const store = new Map();
const el = () => ({ classList: { contains: () => true, add(){}, remove(){}, toggle(){} }, addEventListener(){}, querySelector: () => null, querySelectorAll: () => [] });
const ctx = {
  console,
  localStorage: {
    getItem: (k) => (store.has(k) ? store.get(k) : null),
    setItem: (k, v) => store.set(k, String(v)),
  },
  document: {
    addEventListener(){},
    getElementById: () => el(),
    querySelector: () => null,
    querySelectorAll: () => [],
    createElement: () => el(),
  },
  window: {},
  location: { protocol: 'http:', host: 'x' },
  WebSocket: function(){},
  Terminal: function(){},
  fetch: () => Promise.reject(new Error('no net')),
  setTimeout, clearTimeout, requestAnimationFrame: (f) => f(),
  alert(){}, confirm: () => true,
};
ctx.window = ctx;
vm.createContext(ctx);
vm.runInContext(fs.readFileSync('web/js/app.js', 'utf8'), ctx);

function assert(cond, msg) { if (!cond) { console.error('FAIL:', msg); process.exitCode = 1; } else console.log('ok:', msg); }

// 1) 记录命令与相邻去重
ctx.pushHistory('c1', 'ls -la');
ctx.pushHistory('c1', 'ls -la');
ctx.pushHistory('c1', 'cd /var/log');
let arr = ctx.loadHistory('c1');
assert(arr.length === 3, '同一命令执行两次各记一条: ' + JSON.stringify(arr));
assert(arr[0] === 'cd /var/log', '最新命令在最前');
assert(arr[2] === 'ls -la' && arr[1] === 'ls -la', '重复命令都保留');

// 2) 终端输入解析：回车提交、退格、Ctrl-U、方向键忽略
const st = { cur: '', esc: false };
ctx.feedHistoryBuffer('c1', st, 'git status\r');
ctx.feedHistoryBuffer('c1', st, 'echo adc\x7f\x7fd\r');
ctx.feedHistoryBuffer('c1', st, '\x1b[A\x1b[A');
ctx.feedHistoryBuffer('c1', st, 'pwd\r');
ctx.feedHistoryBuffer('c1', st, 'wrong\x15echo ok\r');
arr = ctx.loadHistory('c1');
assert(arr[0] === 'echo ok', 'Ctrl-U 清行: ' + arr[0]);
assert(arr[1] === 'pwd', '方向键不污染命令行: ' + arr[1]);
assert(arr[2] === 'echo ad', '退格生效: ' + arr[2]);
assert(arr[3] === 'git status', '普通命令入栈: ' + arr[3]);

// 3) 数量上限
ctx.onHistoryLimitChange({ target: { value: '10' } });
for (let i = 0; i < 20; i++) ctx.pushHistory('c1', 'cmd' + i);
arr = ctx.loadHistory('c1');
assert(arr.length === 10, '上限 10 生效, 实际 ' + arr.length);

// 4) Tab 补全 / 方向键回放的命令：以终端实际显示行为准
function fakeTerm() {
  let line = '';
  const api = {
    buffer: {
      active: {
        baseY: 0,
        cursorY: 0,
        cursorX: 0,
        getLine: (y) => (y === 0 ? {
          translateToString: (trimRight, startCol, endCol) => {
            const text = line.slice(0, endCol === undefined ? line.length : endCol);
            return trimRight ? text.replace(/\s+$/, '') : text;
          },
        } : null),
      },
    },
    setLine: (text) => { line = text; api.buffer.active.cursorX = text.length; },
    getLine: () => line,
  };
  return api;
}

(async () => {
  const term = fakeTerm();
  term.setLine('kali@kali:~$ ');
  const st2 = { cur: '', esc: false, prompt: null };
  // 用户只敲了 "cd /ho"，其余由远端 shell 的 Tab 补全完成
  ctx.feedHistoryBuffer('c2', st2, 'cd /ho', term);
  // 回显行尾部有终端补白，且回车后会出现下一行的新提示符，不能带进历史
  term.setLine('kali@kali:~$ cd /home/kali' + ' '.repeat(40));
  ctx.feedHistoryBuffer('c2', st2, '\r', term);
  const list = ctx.loadHistory('c2');
  assert(list[0] === 'cd /home/kali', '补全后的完整命令被记录: ' + JSON.stringify(list[0]));
  assert(!/\s{2,}/.test(list[0]), '记录里不含补白空格');
})();

// 5) 反斜杠续行的多行命令合成一条
function typeLine(term, id, st, shown, padding) {
  term.setLine(shown + (padding ? ' '.repeat(padding) : ''));
  ctx.feedHistoryBuffer(id, st, '\r', term);
}

(async () => {
  const term = fakeTerm();
  const st3 = { cur: '', esc: false, prompt: null, lines: [] };
  term.setLine('kali@kali:~$ ');
  ctx.feedHistoryBuffer('c3', st3, 'gh repo create ws-scrcpy \\', term);
  typeLine(term, 'c3', st3, 'kali@kali:~$ gh repo create ws-scrcpy \\', 20);
  term.setLine('> ');
  ctx.feedHistoryBuffer('c3', st3, '--public \\', term);
  typeLine(term, 'c3', st3, '> --public \\', 20);
  term.setLine('> ');
  ctx.feedHistoryBuffer('c3', st3, '--license mit', term);
  typeLine(term, 'c3', st3, '> --license mit', 20);

  const list = ctx.loadHistory('c3');
  const expect = 'gh repo create ws-scrcpy \\\n--public \\\n--license mit';
  assert(list.length === 1, '续行只记录一条, 实际 ' + list.length);
  assert(list[0] === expect, '多行完整命令: ' + JSON.stringify(list[0]));

  // 6) 引号未闭合也按续行处理
  const st4 = { cur: '', esc: false, prompt: null, lines: [] };
  term.setLine('kali@kali:~$ ');
  ctx.feedHistoryBuffer('c4', st4, 'echo "hello', term);
  typeLine(term, 'c4', st4, 'kali@kali:~$ echo "hello', 10);
  term.setLine('> ');
  ctx.feedHistoryBuffer('c4', st4, 'world"', term);
  typeLine(term, 'c4', st4, '> world"', 10);
  const l4 = ctx.loadHistory('c4');
  assert(l4.length === 1 && l4[0] === 'echo "hello\nworld"', '引号续行合成: ' + JSON.stringify(l4[0]));

  // 7) Ctrl-C 丢弃未完成的多行命令
  const st5 = { cur: '', esc: false, prompt: null, lines: [] };
  term.setLine('kali@kali:~$ ');
  ctx.feedHistoryBuffer('c5', st5, 'rm -rf /tmp/x \\', term);
  typeLine(term, 'c5', st5, 'kali@kali:~$ rm -rf /tmp/x \\', 10);
  ctx.feedHistoryBuffer('c5', st5, '\x03', term);
  assert(ctx.loadHistory('c5').length === 0, 'Ctrl-C 放弃未完成命令');
})();

// 8) zsh 多行提示符 + 上一行命令输出：不能把提示符/输出写进历史
function zshTerm(initialLines) {
  const state = { lines: initialLines.slice() };
  const active = {
    baseY: 0,
    get cursorY() { return state.lines.length - 1; },
    get cursorX() { return state.lines[state.lines.length - 1].length; },
    getLine(y) {
      const text = state.lines[y];
      if (text === undefined) return null;
      return {
        translateToString(trimRight, startCol, endCol) {
          let out = text.slice(startCol || 0, endCol === undefined ? text.length : endCol);
          return trimRight ? out.replace(/\s+$/, '') : out;
        },
      };
    },
  };
  return {
    buffer: { active },
    setLine(text) { state.lines[state.lines.length - 1] = text; },
    pushLine(text) { state.lines.push(text); },
  };
}

(async () => {
  const term = zshTerm([
    'ok',
    '┌──(root㉿lkhjkjjjjjjjkh)-[/home/kali]',
    '└─# ',
  ]);
  const st8 = { cur: '', esc: false, prompt: null, lines: [], startY: null };
  ctx.feedHistoryBuffer('c8', st8, 'echo "ok"', term);
  term.setLine('└─# echo "ok"' + ' '.repeat(30));
  ctx.feedHistoryBuffer('c8', st8, '\r', term);
  const l8 = ctx.loadHistory('c8');
  assert(l8.length === 1, 'zsh 场景只记一条, 实际 ' + l8.length);
  assert(l8[0] === 'echo "ok"', '不含提示符/补白: ' + JSON.stringify(l8[0]));
  assert(!/┌──|└─/.test(l8[0]), '记录里没有提示符字符');
})();

// 9) 引号内反斜杠续行（多行字符串）：换行必须保留，不能被粘成一行
(async () => {
  const term = zshTerm(['└─# ']);
  const st9 = { cur: '', esc: false, prompt: null, lines: [], startY: null };
  ctx.feedHistoryBuffer('c9', st9, 'echo "o\\', term);
  term.setLine('└─# echo "o\\' + ' '.repeat(20));
  ctx.feedHistoryBuffer('c9', st9, '\r', term);        // 引号未闭合 → 继续
  term.pushLine('> ');
  ctx.feedHistoryBuffer('c9', st9, 'k"', term);
  term.setLine('> k"' + ' '.repeat(20));
  ctx.feedHistoryBuffer('c9', st9, '\r', term);        // 引号闭合 → 提交

  const l9 = ctx.loadHistory('c9');
  const expect9 = 'echo "o\\\nk"';
  assert(l9.length === 1, '多行字符串只记一条, 实际 ' + l9.length);
  assert(l9[0] === expect9, '换行保留: ' + JSON.stringify(l9[0]));
  assert(l9[0].includes('\n'), '记录中含真实换行');
})();

// 10) Tab 补全：按键缓冲里只有半截，终端里才是完整命令 → 以终端为准
(async () => {
  const term = zshTerm(['└─# ']);
  const st10 = { cur: '', esc: false, prompt: null, lines: [], startY: null };
  ctx.feedHistoryBuffer('c10', st10, 'cd /home/ka', term);
  term.setLine('└─# cd /home/kali' + ' '.repeat(24));
  ctx.feedHistoryBuffer('c10', st10, '\r', term);
  const l10 = ctx.loadHistory('c10');
  assert(l10.length === 1, '补全命令只记一条');
  assert(l10[0] === 'cd /home/kali', '记录为完整路径: ' + JSON.stringify(l10[0]));
})();

// 11) 同一条命令连跑两次，两次都在
(async () => {
  const term = zshTerm(['└─# ']);
  const st11 = { cur: '', esc: false, prompt: null, lines: [], startY: null };
  for (let k = 0; k < 2; k++) {
    ctx.feedHistoryBuffer('c11', st11, 'cd /home/kali', term);
    term.setLine('└─# cd /home/kali' + ' '.repeat(24));
    ctx.feedHistoryBuffer('c11', st11, '\r', term);
    term.setLine('└─# ');
  }
  const l11 = ctx.loadHistory('c11');
  assert(l11.length === 2, '两条都记录: ' + JSON.stringify(l11));
  assert(l11[0] === 'cd /home/kali' && l11[1] === 'cd /home/kali', '内容一致');
})();

// 12) 多行字符串：不含提示符、不含补白，行内换行保留
(async () => {
  const term = zshTerm(['└─# ']);
  const st12 = { cur: '', esc: false, prompt: null, lines: [], startY: null };
  ctx.feedHistoryBuffer('c12', st12, 'echo "o\\', term);
  term.setLine('└─# echo "o\\' + ' '.repeat(16));
  ctx.feedHistoryBuffer('c12', st12, '\r', term);
  term.pushLine('quote> ');
  ctx.feedHistoryBuffer('c12', st12, 'k"', term);
  term.setLine('quote> k"' + ' '.repeat(16));
  ctx.feedHistoryBuffer('c12', st12, '\r', term);

  const l12 = ctx.loadHistory('c12');
  assert(l12.length === 1, '多行只一条');
  assert(l12[0] === 'echo "o\\\nk"', '内容正确: ' + JSON.stringify(l12[0]));
  assert(!/└─|quote>|  /.test(l12[0]), '不含提示符与补白');
})();

// 13) 没捕获到 PS2 提示符时，也要把 dquote> 这类续行标记剥掉
(async () => {
  const term = zshTerm(['└─# ']);
  const st13 = { cur: '', esc: false, prompt: null, lines: [], startY: null };
  ctx.feedHistoryBuffer('c13', st13, 'echo "o\\', term);
  term.setLine('└─# echo "o\\');
  ctx.feedHistoryBuffer('c13', st13, '\r', term);
  term.pushLine('');                       // 光标行还没画出续行提示符
  ctx.feedHistoryBuffer('c13', st13, 'k"', term);  // prompt 捕获为空
  term.setLine('dquote> k"' + ' '.repeat(10));
  ctx.feedHistoryBuffer('c13', st13, '\r', term);

  const l13 = ctx.loadHistory('c13');
  assert(l13.length === 1, '多行只一条');
  assert(l13[0] === 'echo "o\\\nk"', '剥掉 dquote> 且保留换行: ' + JSON.stringify(l13[0]));
})();

// 14) Ctrl+R 反查历史后回车：要记录被检索出来的那条命令，而不是检索词
(async () => {
  const term = zshTerm(['└─# ']);
  const st14 = { cur: '', esc: false, prompt: null, lines: [], startY: null };
  // 用户按 Ctrl+R（\x12 被忽略），再敲检索词
  ctx.feedHistoryBuffer('c14', st14, '\x12', term);
  term.setLine("(reverse-i-search)`air': ");
  ctx.feedHistoryBuffer('c14', st14, 'air', term);
  // shell 反查出整条命令，光标行变成 `air': ps -ef|grep "aireplay-ng"`
  term.setLine("(reverse-i-search)`air': ps -ef|grep \"aireplay-ng\"" + ' '.repeat(6));
  ctx.feedHistoryBuffer('c14', st14, '\r', term);

  const l14 = ctx.loadHistory('c14');
  assert(l14.length === 1, '反查执行后记录一条: ' + JSON.stringify(l14));
  assert(l14[0] === 'ps -ef|grep "aireplay-ng"', '记录完整命令: ' + JSON.stringify(l14[0]));
})();
