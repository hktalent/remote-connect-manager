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
