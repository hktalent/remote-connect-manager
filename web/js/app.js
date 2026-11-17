// ====== State ======
let connections = [];
// 会话按连接 id 保活：最小化只是把窗口缩回卡片位置，连接不中断
const sessions = new Map();
let activeSessionId = null;
let zoomLevel = 1;

const typeIcons = { ssh: '🖥', vnc: '🖱', rdp: '🪟' };
const typeColors = { ssh: '#4f8cff', vnc: '#28a745', rdp: '#ffc107' };

// ====== Init ======
document.addEventListener('DOMContentLoaded', () => {
  loadConnections();
  document.getElementById('btn-add').addEventListener('click', () => openConfigModal());
  document.getElementById('btn-keys').addEventListener('click', openKeysModal);
  document.getElementById('config-form').addEventListener('submit', onSaveConfig);
  document.getElementById('btn-close-session').addEventListener('click', closeSession);
  document.getElementById('btn-minimize').addEventListener('click', minimizeSession);
  document.getElementById('btn-zoom-in').addEventListener('click', () => changeZoom(0.1));
  document.getElementById('btn-zoom-out').addEventListener('click', () => changeZoom(-0.1));
  document.getElementById('btn-zoom-reset').addEventListener('click', () => setZoom(1));
  document.getElementById('btn-fullscreen').addEventListener('click', toggleFullscreen);
  document.getElementById('btn-about').addEventListener('click', openAboutModal);
  // 系统剪贴板粘贴（Cmd+V / Ctrl+V）直接转发到当前会话
  document.addEventListener('paste', pasteToSession, true);
});

// 捕获阶段处理粘贴：避免依赖终端内部 textarea 是否获得焦点
function pasteToSession(e) {
  const viewer = document.getElementById('session-viewer');
  if (!viewer || viewer.classList.contains('hidden')) return;
  // 弹窗里的输入框保留浏览器默认粘贴；xterm 的隐藏 textarea 交给会话处理
  if (e.target && e.target.closest && e.target.closest('.modal')) return;
  const sess = activeSession();
  if (!sess || typeof sess.paste !== 'function') return;
  const cd = e.clipboardData || window.clipboardData;
  const text = cd ? cd.getData('text') : '';
  if (!text) return;
  e.preventDefault();
  e.stopPropagation();
  sess.paste(text);
}

async function loadConnections() {
  const res = await fetch('/api/connections');
  connections = await res.json();
  renderCards();
}

function renderCards() {
  const grid = document.getElementById('card-grid');
  grid.innerHTML = '';
  connections.forEach(conn => {
    const card = document.createElement('div');
    card.className = 'card' + (conn.screenshot ? '' : ' no-screenshot') + (sessions.has(conn.id) ? ' live' : '');
    card.dataset.id = conn.id;

    const bg = document.createElement('div');
    bg.className = 'card-bg';
    if (conn.screenshot) {
      bg.style.backgroundImage = `url('${conn.screenshot}?t=${Date.now()}')`;
    } else {
      bg.dataset.typeIcon = typeIcons[conn.type] || '🔗';
    }

    const overlay = document.createElement('div');
    overlay.className = 'card-overlay';

    const badge = document.createElement('div');
    badge.className = 'card-type-badge';
    badge.textContent = conn.type.toUpperCase();
    badge.style.color = typeColors[conn.type] || '#fff';

    const btns = document.createElement('div');
    btns.className = 'card-btns';
    btns.innerHTML = `
      <button class="icon-btn" title="编辑配置" onclick="event.stopPropagation(); openConfigModal('${conn.id}')">⚙</button>
      <button class="icon-btn danger" title="删除" onclick="event.stopPropagation(); deleteConnection('${conn.id}')">✕</button>
    `;

    const info = document.createElement('div');
    info.className = 'card-info';
    const left = document.createElement('div');
    left.innerHTML = `
      <div class="card-name">${escapeHtml(conn.name)}</div>
      <div class="card-meta">${conn.host}:${conn.port} · ${conn.username || ''}</div>
    `;
    info.appendChild(left);

    card.appendChild(bg);
    card.appendChild(overlay);
    card.appendChild(badge);
    card.appendChild(btns);
    card.appendChild(info);

    card.addEventListener('click', () => openSession(conn));
    grid.appendChild(card);
  });
}

function escapeHtml(s) {
  const d = document.createElement('div');
  d.textContent = s;
  return d.innerHTML;
}

// ====== Config Modal ======
async function openConfigModal(id) {
  const form = document.getElementById('config-form');
  form.reset();
  document.getElementById('modal-title').textContent = id ? '编辑连接' : '新建连接';

  loadKeyOptions();

  if (id) {
    // 列表里没有密码，编辑时取完整配置，避免保存时把密码清空
    let conn = connections.find(c => c.id === id);
    try {
      const res = await fetch(`/api/connections/${id}`);
      if (res.ok) conn = await res.json();
    } catch (e) {}
    if (conn) {
      form.id.value = conn.id;
      form.name.value = conn.name;
      form.type.value = conn.type;
      form.host.value = conn.host;
      form.port.value = conn.port;
      form.username.value = conn.username;
      form.auth_type.value = conn.auth_type || 'key';
      form.key_name.value = conn.key_name || '';
      form.password.value = conn.password || '';
      form.width.value = conn.width || 1280;
      form.height.value = conn.height || 720;
      form.color_depth.value = conn.color_depth || 24;
    }
  } else {
    form.id.value = '';
    form.type.value = 'ssh';
    form.port.value = 22;
    form.width.value = 1280;
    form.height.value = 720;
  }
  onTypeChange();
  onAuthChange();
  onKeySelectChange();
  document.getElementById('config-modal').classList.remove('hidden');
}

function closeModal() {
  document.getElementById('config-modal').classList.add('hidden');
}

function onTypeChange() {
  const type = document.getElementById('conn-type').value;
  const portMap = { ssh: 22, vnc: 5900, rdp: 3389 };
  const portInput = document.querySelector('input[name="port"]');
  if (!portInput.value || [22, 5900, 3389].includes(parseInt(portInput.value))) {
    portInput.value = portMap[type];
  }
  // SSH 用密钥/密码二选一；VNC/RDP 只需要密码
  const authSection = document.getElementById('auth-section');
  authSection.style.display = '';
  document.getElementById('auth-type').parentElement.classList.toggle('hidden', type !== 'ssh');
  // 画质（色深）只对 RDP 生效
  document.getElementById('quality-row').classList.toggle('hidden', type !== 'rdp');
  if (type === 'ssh') {
    onAuthChange();
  } else {
    document.getElementById('key-section').classList.add('hidden');
    document.getElementById('pass-section').classList.remove('hidden');
  }
}

function onAuthChange() {
  const auth = document.getElementById('auth-type').value;
  document.getElementById('key-section').classList.toggle('hidden', auth !== 'key');
  document.getElementById('pass-section').classList.toggle('hidden', auth !== 'password');
}

async function loadKeyOptions() {
  try {
    const res = await fetch('/api/keys');
    const keys = await res.json();
    window._keysCache = keys;
    const sel = document.getElementById('key-select');
    sel.innerHTML = '<option value="">-- 选择密钥 --</option>';
    keys.forEach(k => {
      const opt = document.createElement('option');
      opt.value = k.name;
      opt.textContent = k.encrypted
        ? `${k.name}（加密）`
        : (k.source ? `${k.name}  (${k.source})` : k.name);
      sel.appendChild(opt);
    });
  } catch (e) {}
}

// 选中加密密钥时，密码栏变为"密钥口令"并显示
function onKeySelectChange() {
  const sel = document.getElementById('key-select');
  const keys = window._keysCache || [];
  const k = keys.find(x => x.name === sel.value);
  const passSection = document.getElementById('pass-section');
  const passLabel = passSection.querySelector('label');
  const passInput = passSection.querySelector('input');
  if (k && k.encrypted) {
    passSection.classList.remove('hidden');
    passLabel.textContent = '密钥口令（该私钥已加密）';
    passInput.placeholder = '输入私钥口令';
  } else {
    passLabel.textContent = '密码';
    passInput.placeholder = '';
    document.getElementById('pass-section').classList.toggle(
      'hidden', document.getElementById('auth-type').value !== 'password');
  }
}

async function onSaveConfig(e) {
  e.preventDefault();
  const form = e.target;
  const isSSH = form.type.value === 'ssh';
  const data = {
    name: form.name.value,
    type: form.type.value,
    host: form.host.value,
    port: parseInt(form.port.value),
    username: form.username.value,
    auth_type: isSSH ? form.auth_type.value : 'password',
    key_name: isSSH ? form.key_name.value : '',
    password: form.password.value,
    width: parseInt(form.width.value),
    height: parseInt(form.height.value),
    color_depth: parseInt(form.color_depth.value) || 24,
  };
  const id = form.id.value;
  const url = id ? `/api/connections/${id}` : '/api/connections';
  const method = id ? 'PUT' : 'POST';
  await fetch(url, {
    method,
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(data),
  });
  closeModal();
  loadConnections();
}

async function deleteConnection(id) {
  if (!confirm('确认删除该连接？')) return;
  await fetch(`/api/connections/${id}`, { method: 'DELETE' });
  loadConnections();
}

// ====== Keys Modal ======
async function openKeysModal() {
  await loadKeysList();
  document.getElementById('keys-modal').classList.remove('hidden');
}

function closeKeysModal() {
  document.getElementById('keys-modal').classList.add('hidden');
}

async function loadKeysList() {
  const res = await fetch('/api/keys');
  const keys = await res.json();
  const list = document.getElementById('keys-list');
  list.innerHTML = '';
  keys.forEach(k => {
    const item = document.createElement('div');
    item.className = 'key-item';
    item.dataset.name = k.name;
    item.innerHTML = `
      <div class="key-item-header">
        <span class="key-item-name">${escapeHtml(k.name)}${
          k.source ? ` <span class="key-source">${escapeHtml(k.source)}</span>` : ''
        }</span>
        <span class="key-item-date">${k.created_at}</span>
      </div>
      <div class="key-pub">${escapeHtml(k.public_key || '(无公钥)')}</div>
      <div class="key-actions">
        <button class="btn-secondary" onclick="copyPubKey('${k.name}')">复制公钥</button>
        <button class="btn-secondary" onclick="editKey(this)">编辑</button>
        <button class="btn-secondary danger" onclick="deleteKey('${k.name}')">删除</button>
      </div>
    `;
    list.appendChild(item);
  });
}

// ====== 密钥编辑 ======
async function editKey(btn) {
  const item = btn.closest('.key-item');
  if (item.querySelector('.key-edit')) return;
  const name = item.dataset.name;
  let priv = '';
  try {
    const res = await fetch(`/api/keys/${encodeURIComponent(name)}`);
    const data = await res.json();
    priv = data.private_key || '';
  } catch (e) {}

  const pubText = item.querySelector('.key-pub').textContent.trim();
  const editor = document.createElement('div');
  editor.className = 'key-edit';
  editor.innerHTML = `
    <label>密钥名称</label>
    <input class="edit-name" value="${escapeHtml(name)}">
    <label>私钥内容</label>
    <textarea class="edit-priv" spellcheck="false"></textarea>
    <label>公钥内容（留空则自动从私钥推导）</label>
    <textarea class="edit-pub" spellcheck="false"></textarea>
    <div class="key-edit-actions">
      <button type="button" class="btn-secondary" onclick="cancelKeyEdit(this)">取消</button>
      <button type="button" class="btn-primary" onclick="saveKeyEdit(this)">保存</button>
    </div>`;
  editor.querySelector('.edit-priv').value = priv;
  editor.querySelector('.edit-pub').value = pubText.startsWith('(') ? '' : pubText;
  item.appendChild(editor);
  editor.querySelector('.edit-name').focus();
}

function cancelKeyEdit(btn) {
  const editor = btn.closest('.key-edit');
  if (editor) editor.remove();
}

async function saveKeyEdit(btn) {
  const item = btn.closest('.key-item');
  const oldName = item.dataset.name;
  const newName = item.querySelector('.edit-name').value.trim();
  const priv = item.querySelector('.edit-priv').value;
  const pub = item.querySelector('.edit-pub').value.trim();
  if (!newName) return alert('密钥名称不能为空');
  if (!priv) return alert('私钥内容不能为空');

  const res = await fetch(`/api/keys/${encodeURIComponent(oldName)}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ new_name: newName, private_key: priv, public_key: pub }),
  });
  if (!res.ok) {
    let msg = '保存失败';
    try { msg = (await res.json()).error || msg; } catch (e) {}
    return alert(msg);
  }
  await loadKeysList();
}

async function scanSSHKeys() {  const res = await fetch('/api/keys/scan', { method: 'POST' });
  const data = await res.json();
  const imported = data.imported || [];
  const skipped = data.skipped || [];
  let msg = `扫描目录: ${data.dir}\n新导入 ${imported.length} 个`;
  if (imported.length) msg += `: ${imported.join(', ')}`;
  if (skipped.length) msg += `\n跳过 ${skipped.length} 个（已存在/加密/非私钥）: ${skipped.join(', ')}`;
  alert(msg);
  loadKeysList();
}

async function generateKey() {
  const name = document.getElementById('new-key-name').value.trim();
  if (!name) return alert('请输入密钥名称');
  await fetch('/api/keys/generate', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name }),
  });
  document.getElementById('new-key-name').value = '';
  loadKeysList();
}

async function copyPubKey(name) {
  const res = await fetch(`/api/keys/${name}/public`);
  const data = await res.json();
  navigator.clipboard.writeText(data.public_key);
  alert('公钥已复制到剪贴板，请添加到目标服务器的 ~/.ssh/authorized_keys');
}

async function deleteKey(name) {
  if (!confirm(`确认删除密钥 ${name}？`)) return;
  await fetch(`/api/keys/${name}`, { method: 'DELETE' });
  loadKeysList();
}

// ====== Session ======
function activeSession() {
  return activeSessionId ? sessions.get(activeSessionId) : null;
}

function sessionPool() {
  return document.getElementById('session-pool');
}

function showViewer(sess) {
  document.getElementById('session-title').textContent =
    `${sess.conn.name} (${sess.conn.type.toUpperCase()})`;
  document.getElementById('session-viewer').classList.remove('hidden');
}

function markCardLive(connId, live) {
  const card = document.querySelector(`.card[data-id="${connId}"]`);
  if (card) card.classList.toggle('live', live);
}

async function openSession(conn) {
  // 已有存活会话：从卡片位置等比放大回来，不重建连接
  const alive = sessions.get(conn.id);
  if (alive) {
    restoreSession(alive);
    return;
  }

  // 列表接口不返回密码，这里取完整配置（VNC/RDP 需要密码）
  try {
    const res = await fetch(`/api/connections/${conn.id}`);
    if (res.ok) conn = await res.json();
  } catch (e) {}

  const body = document.getElementById('session-body');
  body.innerHTML = '';
  const stage = document.createElement('div');
  stage.className = 'session-stage';
  body.appendChild(stage);

  let sess = null;
  if (conn.type === 'ssh') sess = startSSHSession(conn, stage);
  else if (conn.type === 'vnc') sess = startVNCSession(conn, stage);
  else if (conn.type === 'rdp') sess = startRDPSession(conn, stage);
  if (!sess) {
    body.innerHTML = '';
    return;
  }

  sessions.set(conn.id, sess);
  activeSessionId = conn.id;
  zoomLevel = 1;
  sess.zoom = 1;
  showViewer(sess);
  markCardLive(conn.id, true);
  startBackgroundUpdater(sess);
}

// 计算舞台与卡片之间的等比缩放和位移，最小化/恢复共用
function stageCardDelta(sess) {
  const card = document.querySelector(`.card[data-id="${sess.conn.id}"]`);
  if (!card) return null;
  const from = sess.stage.getBoundingClientRect();
  const to = card.getBoundingClientRect();
  if (!from.width || !from.height || !to.width || !to.height) return null;
  return {
    scale: Math.min(to.width / from.width, to.height / from.height),
    dx: to.left + to.width / 2 - (from.left + from.width / 2),
    dy: to.top + to.height / 2 - (from.top + from.height / 2),
  };
}

// 最小化：等比例缩小并动画归位到该连接的卡片，连接保持存活
function minimizeSession() {
  const sess = activeSession();
  if (!sess) return;

  const delta = stageCardDelta(sess);
  const header = document.querySelector('.session-header');
  let anim = null;
  if (delta) {
    anim = sess.stage.animate([
      { transform: 'translate(0, 0) scale(1)', opacity: 1 },
      { transform: `translate(${delta.dx}px, ${delta.dy}px) scale(${delta.scale})`, opacity: 0.2 },
    ], { duration: 420, easing: 'cubic-bezier(0.4, 0, 0.2, 1)', fill: 'forwards' });
  }
  header.animate([{ opacity: 1 }, { opacity: 0 }], { duration: 240, fill: 'forwards' });

  const finish = () => {
    if (anim) anim.cancel();
    header.getAnimations().forEach(a => a.cancel());
    document.getElementById('session-viewer').classList.add('hidden');
    // 移到离屏容器保活，后台继续按 10 秒间隔更新卡片背景
    sessionPool().appendChild(sess.stage);
    activeSessionId = null;
  };
  if (anim) anim.onfinish = finish;
  else setTimeout(finish, 0);
}

// 从卡片位置等比放大恢复会话窗口
function restoreSession(sess) {
  const body = document.getElementById('session-body');
  body.innerHTML = '';
  body.appendChild(sess.stage);
  showViewer(sess);
  if (sess.refit) sess.refit();

  activeSessionId = sess.conn.id;
  zoomLevel = sess.zoom || 1;
  applyZoom();

  const delta = stageCardDelta(sess);
  if (delta) {
    sess.stage.animate([
      { transform: `translate(${delta.dx}px, ${delta.dy}px) scale(${delta.scale})`, opacity: 0.2 },
      { transform: 'translate(0, 0) scale(1)', opacity: 1 },
    ], { duration: 420, easing: 'cubic-bezier(0.4, 0, 0.2, 1)' });
  }
  document.querySelector('.session-header')
    .animate([{ opacity: 0 }, { opacity: 1 }], { duration: 240 });
}

async function closeSession() {
  const sess = activeSession();
  document.getElementById('session-viewer').classList.add('hidden');
  if (!sess) return;

  stopBackgroundUpdater(sess);
  // 先截图并等上传完成，再断开连接，保证关闭后卡片背景图是最新画面
  try {
    const path = await uploadScreenshot(sess.conn.id, snapshotDataUrl(sess));
    if (path) updateCardBackground(sess.conn.id, path);
  } catch (e) {}

  sess.closed = true;
  try { sess.close(); } catch (e) {}
  try { sess.stage.remove(); } catch (e) {}
  sessions.delete(sess.conn.id);
  activeSessionId = null;
  markCardLive(sess.conn.id, false);
  document.getElementById('session-body').innerHTML = '';
  await loadConnections();
}

// ====== SSH ======
function startSSHSession(conn, stage) {
  const termDiv = document.createElement('div');
  termDiv.id = 'terminal';
  stage.appendChild(termDiv);

  const term = new Terminal({
    cursorBlink: true,
    fontSize: 14,
    fontFamily: 'Menlo, Monaco, "Courier New", monospace',
    theme: { background: '#000000', foreground: '#e6e6e6' },
  });
  const fitAddon = new FitAddon.FitAddon();
  term.loadAddon(fitAddon);
  // canvas 渲染器：xterm 默认 DOM 渲染无法截图，加载后才有 canvas 可导出
  if (window.CanvasAddon && CanvasAddon.CanvasAddon) {
    try { term.loadAddon(new CanvasAddon.CanvasAddon()); } catch (e) {}
  }
  term.open(termDiv);

  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  const ws = new WebSocket(`${proto}://${location.host}/ws/ssh/${conn.id}`);
  ws.binaryType = 'arraybuffer';

  const fit = () => { try { fitAddon.fit(); } catch (e) {} };
  const resize = () => {
    let dims = null;
    try { dims = fitAddon.proposeDimensions(); } catch (e) {}
    if (dims && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type: 'resize', cols: dims.cols, rows: dims.rows }));
    }
  };
  const onResize = () => { fit(); resize(); };

  const sess = {
    conn, stage, element: termDiv, zoom: 1, timer: null, closed: false,
    snapshot: () => compositeTerminalCanvas(termDiv),
    refit: onResize,
    paste: (text) => {
      try { term.paste(text); } catch (e) {
        if (ws.readyState === WebSocket.OPEN) ws.send(text);
      }
    },
    close: () => {
      sess.closed = true;
      window.removeEventListener('resize', onResize);
      try { ro && ro.disconnect(); } catch (e) {}
      try { ws.close(); } catch (e) {}
    },
  };

  // 容器尺寸变化（浏览器窗口缩放、全屏切换、布局变化）时同步终端行列数
  let ro = null;
  if (window.ResizeObserver) {
    ro = new ResizeObserver(() => {
      if (sess.closed) return;
      requestAnimationFrame(() => { fit(); resize(); });
    });
    ro.observe(stage);
  }

  ws.onopen = () => { fit(); resize(); };
  ws.onmessage = (ev) => {
    if (typeof ev.data === 'string') {
      term.write(ev.data);
    } else {
      term.write(new Uint8Array(ev.data));
    }
  };
  ws.onclose = () => {
    term.write('\r\n\x1b[31m[连接已关闭]\x1b[0m');
    if (!sess.closed) refreshCardBackground(sess);
  };
  ws.onerror = () => term.write('\r\n\x1b[31m[连接错误]\x1b[0m');

  term.onData((data) => {
    if (ws.readyState === WebSocket.OPEN) ws.send(data);
  });

  window.addEventListener('resize', onResize);
  fit();
  return sess;
}

// ====== VNC ======
function startVNCSession(conn, stage) {
  if (!window.RFB) {
    alert('noVNC 尚未加载完成，请稍后重试');
    return null;
  }
  const div = document.createElement('div');
  div.id = 'vnc-container';
  div.style.width = '100%';
  div.style.height = '100%';
  stage.appendChild(div);

  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  const url = `${proto}://${location.host}/ws/vnc/${conn.id}`;
  const rfb = new window.RFB(div, url);
  rfb.scaleViewport = true;
  rfb.resizeSession = false;

  const sess = {
    conn, stage, element: div, zoom: 1, timer: null, closed: false,
    snapshot: () => div.querySelector('canvas'),
    refit: () => { try { rfb.scaleViewport = true; } catch (e) {} },
    paste: (text) => { try { rfb.clipboardPasteFrom(text); } catch (e) {} },
    close: () => {
      sess.closed = true;
      try { rfb.disconnect(); } catch (e) {}
    },
  };

  // VNC 服务器需要密码时自动带出已保存的密码，或提示输入
  rfb.addEventListener('credentialsrequired', () => {
    const pwd = conn.password || prompt('请输入 VNC 密码：') || '';
    rfb.sendCredentials({ password: pwd });
  });

  rfb.addEventListener('disconnect', () => {
    if (!sess.closed) refreshCardBackground(sess);
  });

  return sess;
}

// ====== RDP (Guacamole) ======
function startRDPSession(conn, stage) {
  if (!window.Guacamole) {
    alert('Guacamole 库尚未加载完成，请稍后重试');
    return null;
  }
  const div = document.createElement('div');
  div.id = 'rdp-container';
  div.style.width = '100%';
  div.style.height = '100%';
  stage.appendChild(div);

  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  const tunnel = new Guacamole.WebSocketTunnel(`${proto}://${location.host}/ws/guacamole/${conn.id}`);
  const client = new Guacamole.Client(tunnel);

  div.appendChild(client.getDisplay().getElement());
  client.getDisplay().getElement().style.display = 'block';
  client.getDisplay().getElement().style.margin = 'auto';

  const sess = {
    conn, stage, element: div, zoom: 1, timer: null, closed: false,
    snapshot: () => div.querySelector('canvas'),
    refit: () => {},
    // 通过 clipboard 流把文本送入远端剪贴板，需在远端桌面内粘贴
    paste: (text) => {
      try {
        const writer = new Guacamole.StringWriter(client.createClipboardStream('text/plain'));
        writer.sendText(text);
        writer.sendEnd();
      } catch (e) {}
    },
    close: () => {
      sess.closed = true;
      try { client.disconnect(); } catch (e) {}
    },
  };

  // 握手由后端完成，这里不需要传连接参数
  client.connect('');

  // 输入处理
  const display = client.getDisplay();
  const kb = new Guacamole.Keyboard(document);
  kb.onkeydown = (keysym) => client.sendKeyEvent(1, keysym);
  kb.onkeyup = (keysym) => client.sendKeyEvent(0, keysym);

  const mouse = new Guacamole.Mouse(display.getElement());
  mouse.onmousedown = (state) => client.sendMouseState(state);
  mouse.onmouseup = (state) => client.sendMouseState(state);
  mouse.onmousemove = (state) => client.sendMouseState(state);

  client.onerror = (status) => {
    console.error('RDP 错误', status);
  };
  client.onend = () => {
    if (!sess.closed) refreshCardBackground(sess);
  };

  return sess;
}

// ====== Screenshot ======
// xterm canvas 渲染器会分层绘制（背景/文字/光标），需要逐层叠加
function compositeTerminalCanvas(termDiv) {
  const layers = Array.from(termDiv.querySelectorAll('.xterm-screen canvas'));
  if (layers.length === 0) return null;
  const canvas = document.createElement('canvas');
  canvas.width = layers[0].width;
  canvas.height = layers[0].height;
  const ctx = canvas.getContext('2d');
  ctx.fillStyle = '#000';
  ctx.fillRect(0, 0, canvas.width, canvas.height);
  layers.forEach(layer => {
    try { ctx.drawImage(layer, 0, 0); } catch (e) {}
  });
  return canvas;
}

// 降采样后再编码，避免每次更新都上传整屏 PNG
function canvasToDataUrl(canvas, maxWidth = 1280) {
  if (!canvas || !canvas.width || !canvas.height) return null;
  let src = canvas;
  if (canvas.width > maxWidth) {
    const scale = maxWidth / canvas.width;
    const tmp = document.createElement('canvas');
    tmp.width = maxWidth;
    tmp.height = Math.max(1, Math.round(canvas.height * scale));
    tmp.getContext('2d').drawImage(canvas, 0, 0, tmp.width, tmp.height);
    src = tmp;
  }
  try { return src.toDataURL('image/jpeg', 0.85); } catch (e) { return null; }
}

function snapshotDataUrl(sess) {
  if (!sess || !sess.snapshot) return null;
  let canvas = null;
  try { canvas = sess.snapshot(); } catch (e) { return null; }
  return canvasToDataUrl(canvas);
}

async function uploadScreenshot(connId, dataUrl) {
  if (!dataUrl) return null;
  try {
    const res = await fetch(`/api/connections/${connId}/screenshot`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ data: dataUrl }),
    });
    if (!res.ok) return null;
    const data = await res.json();
    return data.screenshot || null;
  } catch (e) {
    return null;
  }
}

// 立即截图上传并把该连接卡片背景换成新图
async function refreshCardBackground(sess) {
  const path = await uploadScreenshot(sess.conn.id, snapshotDataUrl(sess));
  if (path) updateCardBackground(sess.conn.id, path);
}

function updateCardBackground(connId, path) {
  if (!path) return;
  const card = document.querySelector(`.card[data-id="${connId}"]`);
  if (card) {
    card.classList.remove('no-screenshot');
    const bg = card.querySelector('.card-bg');
    if (bg) bg.style.backgroundImage = `url('${path}?t=${Date.now()}')`;
  }
  // 同步内存数据，避免下次整表渲染时回退为旧图
  const c = connections.find(x => x.id === connId);
  if (c) c.screenshot = path;
}

// 画面变化检测：降采样成小图后做指纹比对
function canvasHash(canvas) {
  if (!canvas || !canvas.width || !canvas.height) return null;
  const w = 32, h = 18;
  const tmp = document.createElement('canvas');
  tmp.width = w;
  tmp.height = h;
  const ctx = tmp.getContext('2d', { willReadFrequently: true });
  try { ctx.drawImage(canvas, 0, 0, w, h); } catch (e) { return null; }
  let data = null;
  try { data = ctx.getImageData(0, 0, w, h).data; } catch (e) { return null; }
  let hash = 2166136261;
  for (let i = 0; i < data.length; i += 4) {
    const v = (data[i] << 16) | (data[i + 1] << 8) | data[i + 2];
    hash = ((hash ^ v) * 16777619) >>> 0;
  }
  return hash;
}

const BG_POLL_MS = 2000;     // 每 2 秒检测一次界面是否变化
const BG_UPLOAD_MS = 10000;  // 有变化时，最短每 10 秒更新一次卡片背景

function startBackgroundUpdater(sess) {
  if (sess.timer) return;
  let baseline = null;
  let pending = false;
  let lastUpload = 0;
  sess.timer = setInterval(() => {
    if (sess.closed) return;
    const canvas = sess.snapshot && sess.snapshot();
    if (!canvas) return;
    const hash = canvasHash(canvas);
    if (hash === null) return;
    if (baseline === null) { baseline = hash; return; }
    if (hash !== baseline) { baseline = hash; pending = true; }
    if (!pending) return;
    const now = Date.now();
    if (now - lastUpload < BG_UPLOAD_MS) return;
    pending = false;
    lastUpload = now;
    refreshCardBackground(sess);
  }, BG_POLL_MS);
}

function stopBackgroundUpdater(sess) {
  if (sess.timer) {
    clearInterval(sess.timer);
    sess.timer = null;
  }
}

// ====== Zoom ======
function changeZoom(delta) {
  setZoom(Math.max(0.1, Math.min(3, zoomLevel + delta)));
}

function setZoom(level) {
  zoomLevel = level;
  const sess = activeSession();
  if (sess) sess.zoom = level;
  applyZoom();
}

// 缩放只作用于舞台内部元素，避免与最小化动画的 transform 冲突
function applyZoom() {
  const sess = activeSession();
  if (!sess || !sess.element) return;
  sess.element.style.transformOrigin = 'center center';
  sess.element.style.transform = zoomLevel === 1 ? '' : `scale(${zoomLevel})`;
}

function toggleFullscreen() {
  const el = document.getElementById('session-viewer');
  if (!document.fullscreenElement) {
    el.requestFullscreen?.();
  } else {
    document.exitFullscreen?.();
  }
}

// ====== 关于（渲染 README） ======
const REPO_URL = 'https://github.com/hktalent/remote-connect-manager';

async function openAboutModal() {
  document.getElementById('about-modal').classList.remove('hidden');
  const box = document.getElementById('readme-content');
  if (box.dataset.loaded === '1') return;
  try {
    const res = await fetch('/api/readme');
    const data = await res.json();
    box.innerHTML = renderMarkdown(data.markdown || '');
    box.dataset.loaded = '1';
  } catch (e) {
    box.textContent = 'README 加载失败：' + e.message;
  }
}

function closeAboutModal() {
  document.getElementById('about-modal').classList.add('hidden');
}

function escapeHTML(s) {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}

// 行内格式：图片、链接、行内代码、粗体、斜体
function renderInline(text) {
  let s = escapeHTML(text);
  s = s.replace(/!\[([^\]]*)\]\(([^)\s]+)\)/g, (m, alt, src) => {
    const url = /^https?:\/\//.test(src)
      ? src
      : `https://raw.githubusercontent.com/hktalent/remote-connect-manager/main/${src.replace(/^\.\//, '')}`;
    return `<img alt="${alt}" src="${url}">`;
  });
  s = s.replace(/\[([^\]]+)\]\(([^)\s]+)\)/g, (m, label, href) => {
    const url = /^(https?:|#|\/)/.test(href)
      ? href
      : `${REPO_URL}/blob/main/${href.replace(/^\.\//, '')}`;
    return `<a href="${url}" target="_blank" rel="noopener noreferrer">${label}</a>`;
  });
  s = s.replace(/`([^`]+)`/g, '<code>$1</code>');
  s = s.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>');
  s = s.replace(/(^|[^*])\*([^*]+)\*/g, '$1<em>$2</em>');
  return s;
}

// 轻量 Markdown 渲染：覆盖 README 用到的标题、列表、表格、代码块、引用、分割线
function renderMarkdown(md) {
  const lines = md.replace(/\r\n?/g, '\n').split('\n');
  const out = [];
  let i = 0;

  const isTableSep = (l) => /^\s*\|?[\s:|-]+\|[\s:|-]*$/.test(l) && l.includes('-');
  const isTableStart = (idx) => lines[idx] && lines[idx].includes('|')
    && idx + 1 < lines.length && isTableSep(lines[idx + 1]);
  const splitRow = (l) => l.replace(/^\s*\|/, '').replace(/\|\s*$/, '').split('|').map((c) => c.trim());
  const isListStart = (l) => /^\s*([-*+]|\d+\.)\s+/.test(l);

  while (i < lines.length) {
    const line = lines[i];
    if (!line.trim()) { i++; continue; }

    // 代码块
    if (/^```/.test(line)) {
      const lang = line.slice(3).trim();
      i++;
      const buf = [];
      while (i < lines.length && !/^```/.test(lines[i])) { buf.push(lines[i]); i++; }
      i++;
      out.push(`<pre><code class="lang-${escapeHTML(lang)}">${escapeHTML(buf.join('\n'))}</code></pre>`);
      continue;
    }

    // 标题
    const h = line.match(/^(#{1,6})\s+(.*)$/);
    if (h) {
      out.push(`<h${h[1].length}>${renderInline(h[2])}</h${h[1].length}>`);
      i++;
      continue;
    }

    // 分割线
    if (/^\s*(-{3,}|\*{3,})\s*$/.test(line)) { out.push('<hr>'); i++; continue; }

    // 表格
    if (isTableStart(i)) {
      const head = splitRow(line);
      i += 2;
      const rows = [];
      while (i < lines.length && lines[i].includes('|') && lines[i].trim()) {
        rows.push(splitRow(lines[i]));
        i++;
      }
      out.push('<table><thead><tr>' + head.map((c) => `<th>${renderInline(c)}</th>`).join('') + '</tr></thead><tbody>'
        + rows.map((r) => '<tr>' + r.map((c) => `<td>${renderInline(c)}</td>`).join('') + '</tr>').join('')
        + '</tbody></table>');
      continue;
    }

    // 引用
    if (/^>\s?/.test(line)) {
      const buf = [];
      while (i < lines.length && /^>\s?/.test(lines[i])) {
        buf.push(lines[i].replace(/^>\s?/, ''));
        i++;
      }
      out.push(`<blockquote>${renderInline(buf.join(' '))}</blockquote>`);
      continue;
    }

    // 列表
    if (isListStart(line)) {
      const ordered = /^\s*\d+\.\s+/.test(line);
      const items = [];
      while (i < lines.length && (ordered ? /^\s*\d+\.\s+/ : /^\s*[-*+]\s+/).test(lines[i])) {
        items.push(lines[i].replace(/^\s*([-*+]|\d+\.)\s+/, ''));
        i++;
        // 缩进续行并入上一条
        while (i < lines.length && /^\s{2,}\S/.test(lines[i]) && !isListStart(lines[i])) {
          items[items.length - 1] += ' ' + lines[i].trim();
          i++;
        }
      }
      const tag = ordered ? 'ol' : 'ul';
      out.push(`<${tag}>` + items.map((t) => `<li>${renderInline(t)}</li>`).join('') + `</${tag}>`);
      continue;
    }

    // 段落
    const buf = [];
    while (i < lines.length && lines[i].trim()
      && !/^(```|#{1,6}\s|>\s?)/.test(lines[i])
      && !isListStart(lines[i]) && !isTableStart(i)) {
      buf.push(lines[i].trim());
      i++;
    }
    if (buf.length) out.push(`<p>${renderInline(buf.join(' '))}</p>`);
  }

  return out.join('\n');
}
