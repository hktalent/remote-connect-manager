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
assert(arr.length === 2, '相邻重复只记一条: ' + JSON.stringify(arr));
assert(arr[0] === 'cd /var/log', '最新命令在最前');

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
