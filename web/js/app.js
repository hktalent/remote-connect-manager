// ====== State ======
let connections = [];
let currentSession = null;
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
  document.getElementById('btn-zoom-in').addEventListener('click', () => changeZoom(0.1));
  document.getElementById('btn-zoom-out').addEventListener('click', () => changeZoom(-0.1));
  document.getElementById('btn-zoom-reset').addEventListener('click', () => setZoom(1));
  document.getElementById('btn-fullscreen').addEventListener('click', toggleFullscreen);
});

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
    card.className = 'card' + (conn.screenshot ? '' : ' no-screenshot');
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
        <button class="btn-secondary danger" onclick="deleteKey('${k.name}')">删除</button>
      </div>
    `;
    list.appendChild(item);
  });
}

async function scanSSHKeys() {
  const res = await fetch('/api/keys/scan', { method: 'POST' });
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
async function openSession(conn) {
  // 列表接口不返回密码，这里取完整配置（VNC/RDP 需要密码）
  try {
    const res = await fetch(`/api/connections/${conn.id}`);
    if (res.ok) conn = await res.json();
  } catch (e) {}
  document.getElementById('session-title').textContent = `${conn.name} (${conn.type.toUpperCase()})`;
  document.getElementById('session-viewer').classList.remove('hidden');
  zoomLevel = 1;
  const body = document.getElementById('session-body');
  body.innerHTML = '';

  if (conn.type === 'ssh') {
    startSSHSession(conn);
  } else if (conn.type === 'vnc') {
    startVNCSession(conn);
  } else if (conn.type === 'rdp') {
    startRDPSession(conn);
  }
}

function closeSession() {
  if (currentSession) {
    // 先截图再关闭，避免断开后画面被清空导致背景图丢失
    try { currentSession.capture && currentSession.capture(); } catch (e) {}
    try { currentSession.close(); } catch (e) {}
    currentSession = null;
  }
  document.getElementById('session-viewer').classList.add('hidden');
  document.getElementById('session-body').innerHTML = '';
  loadConnections(); // refresh to update screenshot
}

// ====== SSH ======
function startSSHSession(conn) {
  const body = document.getElementById('session-body');
  const termDiv = document.createElement('div');
  termDiv.id = 'terminal';
  body.appendChild(termDiv);

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
  fitAddon.fit();

  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  const ws = new WebSocket(`${proto}://${location.host}/ws/ssh/${conn.id}`);
  ws.binaryType = 'arraybuffer';

  ws.onopen = () => {
    const dims = fitAddon.proposeDimensions();
    ws.send(JSON.stringify({ type: 'resize', cols: dims.cols, rows: dims.rows }));
  };
  ws.onmessage = (ev) => {
    if (typeof ev.data === 'string') {
      term.write(ev.data);
    } else {
      term.write(new Uint8Array(ev.data));
    }
  };
  ws.onclose = () => {
    captureAndSaveScreenshot(conn.id, termDiv);
    term.write('\r\n\x1b[31m[连接已关闭]\x1b[0m');
  };
  ws.onerror = () => term.write('\r\n\x1b[31m[连接错误]\x1b[0m');

  term.onData((data) => {
    if (ws.readyState === WebSocket.OPEN) ws.send(data);
  });

  window.addEventListener('resize', () => {
    fitAddon.fit();
    const dims = fitAddon.proposeDimensions();
    if (ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type: 'resize', cols: dims.cols, rows: dims.rows }));
    }
  });

  currentSession = {
    close: () => ws.close(),
    element: termDiv,
    capture: () => captureAndSaveScreenshot(conn.id, termDiv),
  };
}

// ====== VNC ======
function startVNCSession(conn) {
  if (!window.RFB) {
    alert('noVNC 尚未加载完成，请稍后重试');
    return;
  }
  const body = document.getElementById('session-body');
  const div = document.createElement('div');
  div.id = 'vnc-container';
  div.style.width = '100%';
  div.style.height = '100%';
  body.appendChild(div);

  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  const url = `${proto}://${location.host}/ws/vnc/${conn.id}`;
  const rfb = new window.RFB(div, url);
  rfb.scaleViewport = true;
  rfb.resizeSession = false;

  // VNC 服务器需要密码时自动带出已保存的密码，或提示输入
  rfb.addEventListener('credentialsrequired', () => {
    const pwd = conn.password || prompt('请输入 VNC 密码：') || '';
    rfb.sendCredentials({ password: pwd });
  });

  rfb.addEventListener('disconnect', () => {
    captureAndSaveScreenshotFromElement(conn.id, div);
  });

  currentSession = {
    close: () => rfb.disconnect(),
    element: div,
    capture: () => captureAndSaveScreenshotFromElement(conn.id, div),
  };
}

// ====== RDP (Guacamole) ======
function startRDPSession(conn) {
  if (!window.Guacamole) {
    alert('Guacamole 库尚未加载完成，请稍后重试');
    return;
  }
  const body = document.getElementById('session-body');
  const div = document.createElement('div');
  div.id = 'rdp-container';
  div.style.width = '100%';
  div.style.height = '100%';
  body.appendChild(div);

  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  const tunnel = new Guacamole.WebSocketTunnel(`${proto}://${location.host}/ws/guacamole/${conn.id}`);
  const client = new Guacamole.Client(tunnel);

  div.appendChild(client.getDisplay().getElement());
  client.getDisplay().getElement().style.display = 'block';
  client.getDisplay().getElement().style.margin = 'auto';

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
    captureAndSaveScreenshotFromElement(conn.id, div);
  };

  currentSession = {
    close: () => client.disconnect(),
    element: div,
    capture: () => captureAndSaveScreenshotFromElement(conn.id, div),
  };
}

// ====== Screenshot ======
function captureAndSaveScreenshot(connId, termDiv) {
  // xterm canvas 渲染器会分层绘制（背景/文字/光标），需要逐层叠加
  const layers = Array.from(termDiv.querySelectorAll('.xterm-screen canvas'));
  if (layers.length === 0) return;
  const canvas = document.createElement('canvas');
  canvas.width = layers[0].width;
  canvas.height = layers[0].height;
  const ctx = canvas.getContext('2d');
  ctx.fillStyle = '#000';
  ctx.fillRect(0, 0, canvas.width, canvas.height);
  layers.forEach(layer => {
    try { ctx.drawImage(layer, 0, 0); } catch (e) {}
  });
  try {
    saveScreenshot(connId, canvas.toDataURL('image/png'));
  } catch (e) {}
}

function captureAndSaveScreenshotFromElement(connId, element) {
  const canvas = element.querySelector('canvas');
  if (canvas) {
    try {
      const data = canvas.toDataURL('image/png');
      saveScreenshot(connId, data);
    } catch (e) {}
  }
}

async function saveScreenshot(connId, dataUrl) {
  try {
    await fetch(`/api/connections/${connId}/screenshot`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ data: dataUrl }),
    });
  } catch (e) {}
}

// ====== Zoom ======
function changeZoom(delta) {
  setZoom(Math.max(0.1, Math.min(3, zoomLevel + delta)));
}

function setZoom(level) {
  zoomLevel = level;
  const body = document.getElementById('session-body');
  if (currentSession && currentSession.element) {
    currentSession.element.style.transform = `scale(${zoomLevel})`;
    currentSession.element.style.transformOrigin = 'center center';
  }
}

function toggleFullscreen() {
  const el = document.getElementById('session-viewer');
  if (!document.fullscreenElement) {
    el.requestFullscreen?.();
  } else {
    document.exitFullscreen?.();
  }
}
