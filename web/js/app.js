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
  document.getElementById('btn-upload').addEventListener('click', pickFileForUpload);
  document.getElementById('btn-sftp').addEventListener('click', () => {
    const s = activeSession();
    if (s) openSftpModal(s.conn);
  });
  document.getElementById('btn-tunnel').addEventListener('click', () => {
    const s = activeSession();
    if (s) openTunnelModal(s.conn);
  });
  document.getElementById('btn-history').addEventListener('click', toggleHistoryPanel);
  document.getElementById('history-close').addEventListener('click', hideHistoryPanel);
  document.getElementById('history-list').addEventListener('click', onHistoryListClick);
  document.getElementById('history-select-all').addEventListener('change', (e) => {
    document.querySelectorAll('#history-list .history-check').forEach((cb) => { cb.checked = e.target.checked; });
    updateHistorySel();
  });
  document.getElementById('history-del-sel').addEventListener('click', deleteSelectedHistory);
  document.getElementById('history-clear').addEventListener('click', clearHistoryAll);
  document.getElementById('history-limit').addEventListener('change', onHistoryLimitChange);
  document.getElementById('btn-drive').addEventListener('click', () => {
    const s = activeSession();
    if (s) openDriveModal(s.conn);
  });
  document.getElementById('sftp-upload-btn').addEventListener('click', uploadSftpFile);
  document.getElementById('sftp-mkdir-btn').addEventListener('click', mkdirSftp);
  document.getElementById('sftp-del-btn').addEventListener('click', () => {
    removeSftpItems(Array.from(sftpSelected, ([path, dir]) => ({ path, dir })), true);
  });
  document.getElementById('sftp-sync-btn').addEventListener('click', () => {
    if (!sftpConn) return;
    syncToLocal(sftpSource(sftpConn.id), sftpCurrent || '.');
  });
  document.getElementById('drive-sync-btn').addEventListener('click', () => {
    syncToLocal(driveSource(), driveCurrent || '');
  });
  document.getElementById('sftp-up').addEventListener('click', () => {
    if (!sftpCurrent) return;
    const parent = sftpCurrent.replace(/\/+$/, '').split('/').slice(0, -1).join('/') || '/';
    loadSftp(parent);
  });
  document.getElementById('sftp-go').addEventListener('click', () => {
    loadSftp(document.getElementById('sftp-path').value.trim() || '/');
  });
  document.getElementById('sftp-path').addEventListener('keydown', (e) => {
    if (e.key === 'Enter') loadSftp(e.target.value.trim() || '/');
  });
  document.getElementById('tunnel-form').addEventListener('submit', createTunnel);
  document.querySelectorAll('input[name="tunnel_mode"]').forEach((el) => {
    el.addEventListener('change', () => toggleTunnelMode());
  });
  document.getElementById('drive-refresh').addEventListener('click', () => loadDrive(driveCurrent));
  document.getElementById('drive-up').addEventListener('click', () => {
    if (!driveCurrent) return;
    const parent = driveCurrent.split('/').slice(0, -1).join('/');
    loadDrive(parent);
  });
  // 系统剪贴板粘贴（Cmd+V / Ctrl+V）直接转发到当前会话
  document.addEventListener('paste', pasteToSession, true);
  // 系统复制命令（Cmd+C / 菜单复制）把终端选区写入系统剪贴板
  document.addEventListener('copy', copySessionSelection, true);
  // 拖拽文件/文件夹上传（仅 SSH 会话）
  const viewer = document.getElementById('session-viewer');
  viewer.addEventListener('dragover', onDragOver);
  viewer.addEventListener('dragleave', onDragLeave);
  viewer.addEventListener('drop', onDrop);
  document.getElementById('upload-close').addEventListener('click', closeUploadPanel);
});

// 浏览器菜单或系统复制快捷键触发：把会话里选中的文本写进剪贴板
function copySessionSelection(e) {
  // 弹窗里的输入框保留浏览器默认复制
  if (e.target && e.target.closest && e.target.closest('.modal')) return;
  const sess = activeSession();
  if (!sess || typeof sess.getSelection !== 'function') return;
  const text = sess.getSelection();
  if (!text) return;
  if (e.clipboardData) {
    e.clipboardData.setData('text/plain', text);
    e.preventDefault();
  }
}

// 捕获阶段处理粘贴：避免依赖终端内部 textarea 是否获得焦点
function pasteToSession(e) {
  const viewer = document.getElementById('session-viewer');
  if (!viewer || viewer.classList.contains('hidden')) return;
  // 弹窗里的输入框保留浏览器默认粘贴；xterm 的隐藏 textarea 交给会话处理
  if (e.target && e.target.closest && e.target.closest('.modal')) return;
  const sess = activeSession();
  if (!sess) return;

  // 剪贴板里是文件：SSH 会话直接上传，其余协议提示不支持
  const files = clipboardFiles(e.clipboardData || window.clipboardData);
  if (files.length) {
    e.preventDefault();
    e.stopPropagation();
    if (sess.conn.type === 'ssh') {
      uploadItemsToSession(sess, files.map((f) => ({ file: f, rel: f.name })));
    } else {
      showUploadUnsupported();
    }
    return;
  }

  if (typeof sess.paste !== 'function') return;
  const cd = e.clipboardData || window.clipboardData;
  const text = cd ? cd.getData('text') : '';
  if (!text) return;
  e.preventDefault();
  e.stopPropagation();
  sess.paste(text);
}

// clipboardFiles 从剪贴板取出文件列表（浏览器对普通文件的粘贴支持不一致，能取到就用）
function clipboardFiles(cd) {
  if (!cd) return [];
  if (cd.files && cd.files.length) return Array.from(cd.files);
  const out = [];
  if (cd.items) {
    for (const it of Array.from(cd.items)) {
      if (it.kind !== 'file') continue;
      const f = it.getAsFile ? it.getAsFile() : null;
      if (f) out.push(f);
    }
  }
  return out;
}

async function loadConnections() {
  const res = await fetch('/api/connections');
  connections = await res.json();
  // 监听卡片也要跟连接卡片一起展示，这里顺带拉一次监听状态
  try {
    const nres = await fetch('/api/netcat/listeners');
    ncListeners = (await nres.json()).listeners || [];
  } catch (e) { /* 监听接口不可用时不影响连接卡片 */ }
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
  renderNcCards(grid);
}

// 每个 Netcat 监听也是一张卡片：点卡片进监听详情，可直接开终端
function renderNcCards(grid) {
  (ncListeners || []).forEach((l) => {
    const card = document.createElement('div');
    card.className = 'card nc-card no-screenshot' + (l.running ? ' live' : '');
    card.dataset.ncid = l.id;
    card.innerHTML = `
      <div class="card-bg" data-type-icon="🛰"></div>
      <div class="card-overlay"></div>
      <div class="card-type-badge">NETCAT</div>
      <div class="card-btns">
        <button class="icon-btn" title="${l.running ? '停止监听' : '启动监听'}">${l.running ? '■' : '▶'}</button>
        <button class="icon-btn" title="编辑监听配置">⚙</button>
        <button class="icon-btn danger" title="删除监听">✕</button>
      </div>
      <div class="card-info"><div>
        <div class="card-name">${escapeHtml(l.name)}</div>
        <div class="card-meta">${escapeHtml(l.bind || '0.0.0.0')}:${l.port} · ${l.mode} · ${l.active} 个会话${l.running ? '' : ' · 未启动'}</div>
      </div></div>`;
    const [btnToggle, btnEdit, btnDel] = card.querySelectorAll('.card-btns .icon-btn');
    btnToggle.addEventListener('click', async (e) => {
      e.stopPropagation();
      await toggleListener(l);
      loadConnections();
    });
    btnEdit.addEventListener('click', (e) => {
      e.stopPropagation();
      openListenerForm(l.id);
    });
    btnDel.addEventListener('click', async (e) => {
      e.stopPropagation();
      await deleteListener(l.id);
      loadConnections();
    });
    card.addEventListener('click', () => openNetcatFor(l.id));
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
  resetConnNcSection();

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
      form.vnc_quality.value = conn.vnc_quality || 'medium';
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

// 每次打开「新建连接」都把 Netcat 区恢复成默认，避免上一次的规则残留
function resetConnNcSection() {
  const form = document.getElementById('config-form');
  const box = document.getElementById('conn-nc-rules');
  if (box) box.innerHTML = '';
  if (!form) return;
  if (form.nc_mode) form.nc_mode.value = 'shell';
  if (form.nc_auto_start) form.nc_auto_start.checked = true;
  if (form.nc_auto_cmds) form.nc_auto_cmds.value = '';
  if (form.nc_java_code_base) form.nc_java_code_base.value = '';
  if (form.nc_java_class_name) form.nc_java_class_name.value = 'Exploit';
  if (form.nc_serve_payload_dir) form.nc_serve_payload_dir.checked = true;
  onConnNcModeChange();
  // RMI / 规则都要按 payload 文件选择
  loadNcPayloadOptions().then(() => {
    const sel = document.getElementById('nc-conn-payload-file');
    if (sel) fillPayloadSelect(sel, '');
  });
}

function onConnNcModeChange() {
  const modeEl = document.getElementById('nc-conn-mode');
  if (!modeEl) return;
  const mode = modeEl.value;
  document.getElementById('nc-conn-autocmd-row').classList.toggle('hidden', mode !== 'shell');
  document.getElementById('nc-conn-ldap').classList.toggle('hidden', mode !== 'ldap');
  document.getElementById('nc-conn-http').classList.toggle('hidden', mode !== 'http');
  document.getElementById('nc-conn-rmi').classList.toggle('hidden', mode !== 'rmi');
}

function onTypeChange() {
  const type = document.getElementById('conn-type').value;
  const portMap = { ssh: 22, vnc: 5900, rdp: 3389, telnet: 23, netcat: 8888 };
  const portInput = document.querySelector('input[name="port"]');
  if (!portInput.value || [22, 5900, 3389, 23, 8888].includes(parseInt(portInput.value))) {
    portInput.value = portMap[type];
  }
  const isNc = type === 'netcat';
  // 监听不需要主机 / 用户名 / 分辨率这些连接字段
  document.querySelectorAll('#config-form .conn-only').forEach((el) => el.classList.toggle('hidden', isNc));
  document.getElementById('nc-conn-section').classList.toggle('hidden', !isNc);
  document.getElementById('auth-section').style.display = isNc ? 'none' : '';
  document.getElementById('quality-row').classList.toggle('hidden', isNc || type !== 'rdp');
  document.getElementById('vnc-quality-row').classList.toggle('hidden', isNc || type !== 'vnc');
  document.querySelector('input[name="host"]').required = !isNc;
  const portLabel = document.getElementById('port-label');
  if (portLabel) portLabel.textContent = isNc ? '监听端口' : '端口';

  if (isNc) {
    onConnNcModeChange();
    return;
  }

  // SSH 用密钥/密码二选一；VNC/RDP/Telnet 只需要密码
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

  // 「Netcat 监听」不写连接库，直接建一条监听
  if (form.type.value === 'netcat') {
    const body = {
      name: (form.name.value || '').trim() || `监听 ${form.port.value}`,
      bind: '0.0.0.0',
      port: parseInt(form.port.value, 10),
      mode: form.nc_mode.value,
      auto_start: form.nc_auto_start.checked,
      banner: '',
      auto_cmds: parseNcAutoCmds(form.nc_auto_cmds.value),
      rules: collectNcRules(document.getElementById('conn-nc-rules')),
      java_code_base: (form.nc_java_code_base.value || '').trim(),
      java_class_name: (form.nc_java_class_name.value || '').trim(),
      serve_payload_dir: !!form.nc_serve_payload_dir.checked,
      payload_file: form.nc_payload_file.value,
    };
    const res = await fetch('/api/netcat/listeners', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
    const d = await res.json().catch(() => ({}));
    if (!res.ok) {
      alert('创建监听失败：' + (d.error || res.statusText));
      return;
    }
    closeModal();
    if (d.listener && d.listener.id) ncSelectedId = d.listener.id;
    await loadConnections();
    return;
  }

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
    vnc_quality: form.vnc_quality.value || 'medium',
  };
  const id = form.id.value;
  const url = id ? `/api/connections/${id}` : '/api/connections';
  const method = id ? 'PUT' : 'POST';
  const res = await fetch(url, {
    method,
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(data),
  });
  if (!res.ok) {
    const d = await res.json().catch(() => ({}));
    alert('保存失败：' + (d.error || res.statusText));
    return;
  }
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
  // 文件上传只对 RDP（共享盘）提供；SFTP 与端口转发只对 SSH 提供
  const type = sess.conn.type;
  document.getElementById('btn-upload').classList.toggle('hidden', type !== 'rdp');
  document.getElementById('btn-drive').classList.toggle('hidden', type !== 'rdp');
  document.getElementById('btn-sftp').classList.toggle('hidden', type !== 'ssh');
  document.getElementById('btn-tunnel').classList.toggle('hidden', type !== 'ssh');
  document.getElementById('btn-history').classList.toggle('hidden', type !== 'ssh');
  hideHistoryPanel();
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
  else if (conn.type === 'rdp' || conn.type === 'telnet') sess = startGuacSession(conn, stage);
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

// ====== SSH 命令历史 ======
const HIST_LIMIT_KEY = 'rcm.cmdHistoryLimit';
const HIST_DEFAULT_LIMIT = 200;
let historyConnId = null;

function histStorageKey(id) { return `rcm.cmdHistory.${id}`; }

function getHistLimit() {
  const v = parseInt(localStorage.getItem(HIST_LIMIT_KEY) || '', 10);
  if (!Number.isFinite(v) || v < 10) return HIST_DEFAULT_LIMIT;
  return Math.min(v, 2000);
}

function loadHistory(id) {
  if (!id) return [];
  try {
    const arr = JSON.parse(localStorage.getItem(histStorageKey(id)) || '[]');
    return Array.isArray(arr) ? arr : [];
  } catch (e) {
    return [];
  }
}

function saveHistory(id, arr) {
  try {
    localStorage.setItem(histStorageKey(id), JSON.stringify(arr.slice(0, getHistLimit())));
  } catch (e) {}
}

// 追加一条命令：同一命令执行多次也各记一条，方便回看执行顺序
function pushHistory(id, cmd) {
  cmd = String(cmd || '').trim();
  if (!cmd) return;
  const arr = loadHistory(id);
  arr.unshift(cmd);
  saveHistory(id, arr);
  if (historyConnId === id && !document.getElementById('history-panel').classList.contains('hidden')) {
    renderHistory();
  }
}

// 读取终端当前行（含提示符）到光标位置的文本，用于还原「整条命令」
function readLineText(term, maxLines = 8) {
  try {
    const buf = term.buffer.active;
    const endY = buf.baseY + buf.cursorY;
    const startY = Math.max(0, endY - (maxLines - 1));
    let text = '';
    for (let y = startY; y <= endY; y++) {
      const line = buf.getLine(y);
      if (!line) continue;
      text += y === endY
        ? line.translateToString(true, 0, buf.cursorX)
        : line.translateToString(true);
    }
    return text;
  } catch (e) {
    return '';
  }
}

// 读取从 promptStartY（绝对行号）到光标所在行的文本，命令从提示符那一行开始
function readCommandText(term, promptStartY) {
  if (!term || !term.buffer) return '';
  try {
    const buf = term.buffer.active;
    const endY = buf.baseY + buf.cursorY;
    if (typeof promptStartY !== 'number' || promptStartY > endY) return readLineText(term);
    let text = '';
    for (let y = promptStartY; y <= endY; y++) {
      const line = buf.getLine(y);
      if (!line) continue;
      text += y === endY
        ? line.translateToString(true, 0, buf.cursorX)
        : line.translateToString(true);
    }
    return text;
  } catch (e) {
    return '';
  }
}

// 从终端输入里取出「已提交的命令行」，忽略方向键等 ESC 序列。
// 客户端只看得到按键：Tab 补全、方向键回放都由远端 shell 完成，
// 因此回车后以终端里实际显示的那一行（提示符之后）为准，按键缓冲只作兜底。
function feedHistoryBuffer(id, st, data, term) {
  for (let i = 0; i < data.length; i++) {
    const ch = data[i];
    if (st.esc) {
      // CSI/SS3 序列：读到终止字符为止
      if (ch >= '@' && ch <= '~' && ch !== '[' && ch !== 'O') st.esc = false;
      continue;
    }
    if (ch === '\x1b') { st.esc = true; continue; }
    if (ch === '\r' || ch === '\n') {
      // 回车这一刻光标还在命令行末尾，立刻读，避免读到下一行的新提示符
      commitHistoryLine(id, st, term ? readCommandText(term, st.startY) : '');
      continue;
    }
    if (ch === '\x7f' || ch === '\b') { st.cur = st.cur.slice(0, -1); continue; }
    if (ch === '\x03' || ch === '\x15') {
      // Ctrl-C / Ctrl-U：放弃当前未完成的（多行）命令
      st.cur = '';
      st.prompt = null;
      st.startY = null;
      st.lines = [];
      continue;
    }
    if (ch < ' ') continue;
    if (!st.prompt && term) {
      // 只取光标所在那一段（zsh 多行提示符的末行），并记住命令起始的绝对行号
      st.prompt = readLineText(term, 1);
      try { st.startY = term.buffer.active.baseY + term.buffer.active.cursorY; } catch (e) { st.startY = null; }
    }
    st.cur += ch;
  }
}

// 行尾是否以未转义的反斜杠结尾（奇数个 \ 才算续行）
function endsWithContinuation(line) {
  let n = 0;
  for (let i = line.length - 1; i >= 0 && line[i] === '\\'; i--) n++;
  return n % 2 === 1;
}

// 引号是否还没闭合（' " `），决定 shell 是否进入 PS2 续行
function hasUnclosedQuote(text) {
  let quote = '';
  for (let i = 0; i < text.length; i++) {
    const ch = text[i];
    if (quote === '') {
      if (ch === "'" || ch === '"' || ch === '`') quote = ch;
      continue;
    }
    if (ch === '\\' && quote !== "'") { i++; continue; } // 双引号/反引号内反斜杠转义
    if (ch === quote) quote = '';
  }
  return quote !== '';
}

// 提交一条历史：以「回车时终端上光标之前那一行」为准（含 Tab 补全内容），
// 按键缓冲兜底。行尾反斜杠续行、引号未闭合时继续累积，最后合成完整一条命令。
function commitHistoryLine(id, st, termLine) {
  const typed = st.cur.trim();
  const prompt = st.prompt || '';
  st.cur = '';
  st.prompt = null;
  st.startY = null;
  if (!typed && !(st.lines && st.lines.length)) return;

  let shown = String(termLine || '').replace(/\s+$/, '');
  let stripped = false;
  if (prompt) {
    if (shown.startsWith(prompt)) {
      shown = shown.slice(prompt.length);
      stripped = true;
    } else {
      // 提示符可能被重绘/换行打断，退回到最后一次出现的提示符
      const idx = shown.lastIndexOf(prompt);
      if (idx >= 0) { shown = shown.slice(idx + prompt.length); stripped = true; }
    }
  }
  shown = shown.replace(/\s+$/, '').trim();
  // 没剥掉提示符就别用终端文本（会把 ┌──(root…)# 写进历史），退回按键缓冲
  const line = (stripped && shown.length >= typed.length) ? shown : typed;
  if (!line) return;

  st.lines = st.lines || [];
  st.lines.push(line);
  const joined = st.lines.join('\n');
  // 还没输完：继续收集下一行（PS2 提示符会在下一行重新捕获）
  if (endsWithContinuation(line) || hasUnclosedQuote(joined)) return;

  st.lines = [];
  pushHistory(id, joined);
}

function renderHistory() {
  const panel = document.getElementById('history-panel');
  const list = document.getElementById('history-list');
  document.getElementById('history-limit').value = getHistLimit();
  document.getElementById('history-select-all').checked = false;
  const arr = loadHistory(historyConnId);
  if (!arr.length) {
    list.innerHTML = '<div class="history-empty">还没有记录到命令，在终端里执行命令后会自动出现</div>';
    updateHistorySel();
    return;
  }
  list.innerHTML = arr.map((cmd, i) => {
    const oneLine = String(cmd).replace(/\s*\n\s*/g, ' ⏎ ');
    const badge = String(cmd).includes('\n') ? '<span class="history-badge">多行</span>' : '';
    return `
    <div class="history-row" data-i="${i}">
      <input type="checkbox" class="history-check">
      ${badge}<code class="history-cmd" title="${escapeHTML(cmd)}">${escapeHTML(oneLine)}</code>
      <button class="history-del" title="删除这条">✕</button>
    </div>`;
  }).join('');
  updateHistorySel();
  if (panel) panel.scrollTop = 0;
}

function updateHistorySel() {
  const rows = document.querySelectorAll('#history-list .history-row');
  const selected = Array.from(document.querySelectorAll('#history-list .history-check')).filter((cb) => cb.checked).length;
  document.getElementById('history-del-sel').disabled = selected === 0;
  document.getElementById('history-del-sel').textContent = selected ? `删除选中(${selected})` : '删除选中';
  const all = document.getElementById('history-select-all');
  if (all) all.checked = rows.length > 0 && selected === rows.length;
}

function renumberHistoryRows() {
  document.querySelectorAll('#history-list .history-row').forEach((row, i) => { row.dataset.i = i; });
}

// 单条删除：只摘掉这一行 DOM，不重绘整个列表
function removeHistoryRow(rowEl) {
  const arr = loadHistory(historyConnId);
  const idx = Number(rowEl.dataset.i);
  if (Number.isNaN(idx) || idx < 0 || idx >= arr.length) return;
  arr.splice(idx, 1);
  saveHistory(historyConnId, arr);
  rowEl.remove();
  renumberHistoryRows();
  if (!arr.length) renderHistory();
  else updateHistorySel();
}

function onHistoryListClick(e) {
  const row = e.target.closest('.history-row');
  if (!row) return;
  if (e.target.classList.contains('history-check')) { updateHistorySel(); return; }
  if (e.target.classList.contains('history-del')) { removeHistoryRow(row); return; }
  const arr = loadHistory(historyConnId);
  const cmd = arr[Number(row.dataset.i)];
  if (!cmd) return;
  const sess = activeSession();
  if (!sess || !sess.sendInput) return;
  sess.sendInput(e.shiftKey ? cmd + '\r' : cmd);
  if (e.shiftKey) hideHistoryPanel();
}

// 多条删除：批量摘 DOM 行，无整表重绘、无闪烁
function deleteSelectedHistory() {
  const rows = Array.from(document.querySelectorAll('#history-list .history-row'));
  const idx = rows
    .filter((row) => row.querySelector('.history-check').checked)
    .map((row) => Number(row.dataset.i));
  if (!idx.length) return;
  const arr = loadHistory(historyConnId);
  idx.sort((a, b) => b - a).forEach((i) => {
    if (i >= 0 && i < arr.length) arr.splice(i, 1);
  });
  saveHistory(historyConnId, arr);
  rows.forEach((row) => {
    if (row.querySelector('.history-check').checked) row.remove();
  });
  renumberHistoryRows();
  if (!arr.length) renderHistory();
  else updateHistorySel();
}

function clearHistoryAll() {
  if (!historyConnId) return;
  if (!confirm('清空该会话的全部命令历史？')) return;
  saveHistory(historyConnId, []);
  renderHistory();
}

function onHistoryLimitChange(e) {
  const v = Math.max(10, Math.min(2000, parseInt(e.target.value, 10) || HIST_DEFAULT_LIMIT));
  e.target.value = v;
  try { localStorage.setItem(HIST_LIMIT_KEY, String(v)); } catch (err) {}
  if (!historyConnId) return;
  let arr = loadHistory(historyConnId);
  if (arr.length > v) {
    arr = arr.slice(0, v);
    saveHistory(historyConnId, arr);
    // 只摘掉超出的行，不重绘
    document.querySelectorAll('#history-list .history-row').forEach((row) => {
      if (Number(row.dataset.i) >= v) row.remove();
    });
    renumberHistoryRows();
    updateHistorySel();
  }
}

function toggleHistoryPanel() {
  const panel = document.getElementById('history-panel');
  if (!panel.classList.contains('hidden')) { hideHistoryPanel(); return; }
  const sess = activeSession();
  if (!sess) return;
  historyConnId = sess.conn.id;
  renderHistory();
  panel.classList.remove('hidden');
}

function hideHistoryPanel() {
  const panel = document.getElementById('history-panel');
  if (panel) panel.classList.add('hidden');
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

  // canvas 渲染的选区不是 DOM 选区，浏览器原生 Cmd+C 拷不到；
  // macOS 用 Cmd+C，Windows/Linux 用 Ctrl+Shift+C（Ctrl+C 必须留给 SIGINT）
  term.attachCustomKeyEventHandler((e) => {
    if (e.type !== 'keydown') return true;
    const key = String(e.key || '').toLowerCase();
    const macCopy = e.metaKey && !e.ctrlKey && !e.altKey && key === 'c';
    const winCopy = e.ctrlKey && e.shiftKey && !e.metaKey && key === 'c';
    if (!macCopy && !winCopy) return true;
    const sel = term.getSelection();
    if (!sel) return true;
    writeLocalClipboard(sel);
    e.preventDefault();
    return false;
  });

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
    getSelection: () => term.getSelection(),
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

  sess.sendInput = (text) => {
    if (ws.readyState === WebSocket.OPEN) ws.send(text);
  };

  // 记录用户敲下的命令（回车提交），供会话头部的「命令历史」面板使用
  const histBuf = { cur: '', esc: false, prompt: null, lines: [], startY: null };
  term.onData((data) => {
    feedHistoryBuffer(conn.id, histBuf, data, term);
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
  // 画质预设：qualityLevel 越高越清晰，compressionLevel 越高越省带宽
  const presets = { high: [9, 3], medium: [6, 6], low: [3, 9] };
  const [ql, cl] = presets[conn.vnc_quality] || presets.medium;
  try {
    rfb.qualityLevel = ql;
    rfb.compressionLevel = cl;
  } catch (e) {}

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

  // 远端桌面里复制文本 → 本地系统剪贴板
  rfb.addEventListener('clipboard', (e) => {
    const text = e.detail && e.detail.text;
    if (text) writeLocalClipboard(text);
  });

  return sess;
}

// ====== RDP / Telnet（Guacamole） ======
function startGuacSession(conn, stage) {
  if (!window.Guacamole) {
    alert('Guacamole 库尚未加载完成，请稍后重试');
    return null;
  }
  const isTelnet = conn.type === 'telnet';
  const div = document.createElement('div');
  div.id = 'guac-container';
  div.style.width = '100%';
  div.style.height = '100%';
  stage.appendChild(div);

  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  const path = isTelnet ? 'telnet' : 'guacamole';
  const tunnel = new Guacamole.WebSocketTunnel(`${proto}://${location.host}/ws/${path}/${conn.id}`);
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
    // RDP 共享盘上传：远端 Windows 在 \\tsclient\rcm，xrdp 在 ~/thinclient_drives/rcm
    upload: isTelnet ? null : (file) => uploadToDrive(client, file),
    close: () => {
      sess.closed = true;
      try { client.disconnect(); } catch (e) {}
    },
  };

  // 远端剪贴板 → 本地系统剪贴板
  client.onclipboard = (stream, mimetype) => {    if (mimetype !== 'text/plain') {
      try { stream.sendAck('Unsupported', 0x0100); } catch (e) {}
      return;
    }
    const reader = new Guacamole.StringReader(stream);
    let text = '';
    reader.ontext = (chunk) => { text += chunk; };
    reader.onend = () => { if (text) writeLocalClipboard(text); };
  };

  // RDP 共享盘：远端写入的文件以 body 指令下发，直接触发浏览器下载。
  // 注意 guacd 下发的 filesystem 名称固定为 "Shared Drive"，不能用 drive-name 判断。
  client.onfilesystem = (object) => {
    object.onbody = (stream, mimetype, filename) => {
      try {
        const reader = new Guacamole.BlobReader(stream, mimetype || 'application/octet-stream');
        reader.onend = () => saveBlob(reader.getBlob(), filename);
      } catch (e) {
        console.error('共享盘下载失败', e);
      }
    };
  };

  // guacd 1.5 把共享盘下载以 file 指令推给浏览器（部分版本走 filesystem/body）
  client.onfile = (stream, mimetype, filename) => {
    try {
      const reader = new Guacamole.BlobReader(stream, mimetype || 'application/octet-stream');
      reader.onend = () => saveBlob(reader.getBlob(), filename);
    } catch (e) {
      console.error('共享盘下载失败', e);
    }
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
    console.error('Guacamole 错误', status);
  };
  client.onend = () => {
    if (!sess.closed) refreshCardBackground(sess);
  };

  return sess;
}

function pickFileForUpload() {
  const sess = activeSession();
  if (!sess || typeof sess.upload !== 'function') return;
  const input = document.createElement('input');
  input.type = 'file';
  input.onchange = () => {
    if (input.files && input.files[0]) sess.upload(input.files[0]);
  };
  input.click();
}

function uploadToDrive(client, file) {
  try {
    const name = file.name.replace(/[\\/]/g, '_');
    const stream = client.createFileStream(file.type || 'application/octet-stream', `rcm\\${name}`);
    const writer = new Guacamole.BlobWriter(stream);
    writer.sendBlob(file);
  } catch (e) {
    console.error('上传失败', e);
  }
}

function writeLocalClipboard(text) {
  if (navigator.clipboard && window.isSecureContext) {
    navigator.clipboard.writeText(text).catch(() => fallbackCopyText(text));
  } else {
    fallbackCopyText(text);
  }
}

function fallbackCopyText(text) {
  const ta = document.createElement('textarea');
  ta.value = text;
  ta.style.position = 'fixed';
  ta.style.opacity = '0';
  document.body.appendChild(ta);
  ta.select();
  try { document.execCommand('copy'); } catch (e) {}
  ta.remove();
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

// ====== SFTP 文件管理 ======
let sftpConn = null;
let sftpCurrent = '';
let sftpEntries = [];
let sftpSelected = new Map(); // path -> dir，批量删除用
let sftpOpening = false;      // 防止连点：打开流程未结束时忽略重复点击

// 图标按钮的忙碌态：禁用 + 转圈图标 + 提示文案，避免“点了没反应”反复点
function setSftpBusy(on) {
  const btn = document.getElementById('btn-sftp');
  if (!btn) return;
  btn.disabled = on;
  btn.classList.toggle('loading', on);
  btn.textContent = on ? '⏳' : '🗂';
  btn.title = on ? '正在打开文件管理…' : '文件管理（SFTP）';
}

async function openSftpModal(conn) {
  if (sftpOpening) return;
  sftpOpening = true;
  setSftpBusy(true);
  sftpConn = conn;
  sftpCurrent = '';
  sftpEntries = [];
  sftpSelected.clear();
  document.getElementById('sftp-title').textContent = `文件管理 - ${conn.name}`;
  document.getElementById('sftp-path').value = '';
  // 立刻弹出面板并给出占位提示，后面联网取目录不阻塞用户感知
  document.getElementById('sftp-list').innerHTML = '<div class="sftp-empty">正在连接远端…</div>';
  document.getElementById('sftp-modal').classList.remove('hidden');
  const sftpBtn = document.getElementById('btn-sftp');
  if (sftpBtn) sftpBtn.classList.add('active');
  updateSftpSelUI();
  let start = '.';
  try {
    // 优先打开该 SSH 会话当前的目录，接口异常 / 超时不拖住面板
    const ctrl = new AbortController();
    const timer = setTimeout(() => ctrl.abort(), 1500);
    try {
      const res = await fetch(`/api/ssh/${conn.id}/cwd`, { signal: ctrl.signal });
      if (res.ok) {
        const d = await res.json();
        if (d && d.cwd) start = d.cwd;
      }
    } finally {
      clearTimeout(timer);
    }
  } catch (e) {}
  try {
    await loadSftp(start);
  } finally {
    sftpOpening = false;
    setSftpBusy(false);
  }
}

function closeSftpModal() {
  document.getElementById('sftp-modal').classList.add('hidden');
  const btn = document.getElementById('btn-sftp');
  if (btn) btn.classList.remove('active');
}

async function loadSftp(p) {
  if (!sftpConn) return;
  const list = document.getElementById('sftp-list');
  list.innerHTML = '<div class="sftp-empty loading">正在读取目录…</div>';
  try {
    const res = await fetch(`/api/sftp/${sftpConn.id}/list?path=${encodeURIComponent(p)}`);
    const data = await res.json();
    if (!res.ok) throw new Error(data.error || res.statusText);
    sftpCurrent = data.path;
    document.getElementById('sftp-path').value = data.path;
    renderSftpEntries(data.entries || []);
  } catch (e) {
    list.innerHTML = `<div class="sftp-empty">${escapeHTML(e.message)}</div>`;
  }
}

function renderSftpEntries(entries) {
  const list = document.getElementById('sftp-list');
  sftpEntries = entries;
  sftpSelected.clear();
  updateSftpSelUI();
  if (!entries.length) {
    list.innerHTML = '<div class="sftp-empty">空目录</div>';
    return;
  }
  list.innerHTML = `
    <div class="sftp-row sftp-head">
      <input type="checkbox" id="sftp-check-all" title="全选当前目录">
      <span></span>
      <span class="meta">名称</span>
      <span class="meta">大小</span>
      <span class="meta">修改时间</span>
      <span></span>
    </div>` + entries.map((e) => `
    <div class="sftp-row" data-path="${escapeHTML(e.path)}">
      <input type="checkbox" class="sftp-check" title="选择" data-path="${escapeHTML(e.path)}" data-dir="${e.dir}">
      <span>${e.dir ? '📁' : '📄'}</span>
      <span class="name ${e.dir ? 'dir' : ''}" data-path="${escapeHTML(e.path)}" data-dir="${e.dir}" data-name="${escapeHTML(e.name)}">${escapeHTML(e.name)}</span>
      <span class="meta">${e.dir ? '-' : formatSize(e.size)}</span>
      <span class="meta">${(e.mod_time || '').replace('T', ' ').slice(0, 19)}</span>
      <span>${e.dir ? '' : `<a href="/api/sftp/${sftpConn.id}/download?path=${encodeURIComponent(e.path)}" download>下载</a>`}<button data-del="${escapeHTML(e.path)}" data-dir="${e.dir}" title="删除">✕</button></span>
    </div>`).join('');

  list.querySelectorAll('.name').forEach((el) => {
    el.addEventListener('click', () => {
      if (el.dataset.dir === 'true') loadSftp(el.dataset.path);
      else downloadSftp(el.dataset.path);
    });
  });
  list.querySelectorAll('.name[data-dir="false"]').forEach((el) => {
    bindDragOut(el, `/api/sftp/${sftpConn.id}/download?path=${encodeURIComponent(el.dataset.path)}`, el.dataset.name);
  });
  list.querySelectorAll('[data-del]').forEach((el) => {
    el.addEventListener('click', () =>
      removeSftpItems([{ path: el.dataset.del, dir: el.dataset.dir === 'true' }], true));
  });
  list.querySelectorAll('.sftp-check').forEach((el) => {
    el.addEventListener('change', () => {
      const p = el.dataset.path;
      if (el.checked) sftpSelected.set(p, el.dataset.dir === 'true');
      else sftpSelected.delete(p);
      const row = el.closest('.sftp-row');
      if (row) row.classList.toggle('selected', el.checked);
      syncSftpCheckAll();
      updateSftpSelUI();
    });
  });
  const all = document.getElementById('sftp-check-all');
  if (all) {
    all.addEventListener('change', () => {
      list.querySelectorAll('.sftp-check').forEach((el) => {
        if (el.checked === all.checked) return;
        el.checked = all.checked;
        const p = el.dataset.path;
        if (el.checked) sftpSelected.set(p, el.dataset.dir === 'true');
        else sftpSelected.delete(p);
        const row = el.closest('.sftp-row');
        if (row) row.classList.toggle('selected', el.checked);
      });
      syncSftpCheckAll();
      updateSftpSelUI();
    });
  }
}

// 表头全选框跟随当前选择状态
function syncSftpCheckAll() {
  const all = document.getElementById('sftp-check-all');
  if (!all) return;
  const boxes = Array.from(document.querySelectorAll('#sftp-list .sftp-check'));
  const on = boxes.filter((b) => b.checked).length;
  all.checked = boxes.length > 0 && on === boxes.length;
  all.indeterminate = on > 0 && on < boxes.length;
}

function updateSftpSelUI() {
  const btn = document.getElementById('sftp-del-btn');
  if (!btn) return;
  btn.textContent = `删除选中 (${sftpSelected.size})`;
  btn.classList.toggle('hidden', sftpSelected.size === 0);
}

// 删除成功后只摘掉对应行，不重绘整表，避免闪烁与滚动位置丢失
function dropSftpRows(paths) {
  const list = document.getElementById('sftp-list');
  paths.forEach((p) => {
    const row = Array.from(list.querySelectorAll('.sftp-row:not(.sftp-head)'))
      .find((r) => r.dataset.path === p);
    if (row) row.remove();
    sftpSelected.delete(p);
    sftpEntries = sftpEntries.filter((e) => e.path !== p);
  });
  if (!list.querySelectorAll('.sftp-row:not(.sftp-head)').length) {
    list.innerHTML = '<div class="sftp-empty">空目录</div>';
  }
  syncSftpCheckAll();
  updateSftpSelUI();
}

// 批量/单项删除；失败项仅标红，不影响已成功的项
async function removeSftpItems(items, needConfirm) {
  if (!sftpConn || !items.length) return;
  if (needConfirm) {
    const tip = items.length > 1 ? `确定删除选中的 ${items.length} 项（目录将递归删除）？` : `确定删除 ${items[0].path}${items[0].dir ? '（目录）' : ''}？`;
    if (!confirm(tip + (items.length > 1 ? `\n${items.map((i) => i.path).join('\n')}` : ''))) return;
  }
  const list = document.getElementById('sftp-list');
  const rowsOf = (p) => Array.from(list.querySelectorAll('.sftp-row')).find((r) => r.dataset.path === p);
  const rows = items.map((i) => rowsOf(i.path)).filter(Boolean);
  rows.forEach((r) => {
    r.classList.remove('failed');
    r.classList.add('removing');
  });
  try {
    const res = await fetch(`/api/sftp/${sftpConn.id}/remove`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ paths: items }),
    });
    const d = await res.json().catch(() => ({}));
    if (!res.ok && !(d.failed && d.failed.length)) {
      rows.forEach((r) => r.classList.remove('removing'));
      alert('删除失败：' + (d.error || res.statusText));
      return;
    }
    dropSftpRows(d.removed || []);
    (d.failed || []).forEach((f) => {
      const row = rowsOf(f.path);
      if (!row) return;
      row.classList.remove('removing');
      row.classList.add('failed');
      const name = row.querySelector('.name');
      if (name) name.title = `删除失败：${f.error}`;
    });
    if ((d.failed || []).length) {
      alert(`${d.failed.length} 项删除失败，已在列表中标红（鼠标悬停文件名可看原因）`);
    }
  } catch (e) {
    rows.forEach((r) => r.classList.remove('removing'));
    alert('删除失败：' + e.message);
  }
}

function downloadSftp(p) {
  const a = document.createElement('a');
  a.href = `/api/sftp/${sftpConn.id}/download?path=${encodeURIComponent(p)}`;
  a.download = '';
  document.body.appendChild(a);
  a.click();
  a.remove();
}

function uploadSftpFile() {
  if (!sftpConn) return;
  const input = document.createElement('input');
  input.type = 'file';
  input.onchange = async () => {
    const f = input.files && input.files[0];
    if (!f) return;
    const fd = new FormData();
    fd.append('file', f);
    fd.append('path', sftpCurrent || '/');
    try {
      const res = await fetch(`/api/sftp/${sftpConn.id}/upload`, { method: 'POST', body: fd });
      if (!res.ok) {
        const d = await res.json().catch(() => ({}));
        alert('上传失败：' + (d.error || res.statusText));
      }
    } catch (e) {
      alert('上传失败：' + e.message);
    }
    loadSftp(sftpCurrent);
  };
  input.click();
}

async function mkdirSftp() {
  if (!sftpConn) return;
  const name = prompt('新目录名称');
  if (!name) return;
  const p = `${sftpCurrent.replace(/\/+$/, '')}/${name}`;
  const res = await fetch(`/api/sftp/${sftpConn.id}/mkdir`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ path: p }),
  });
  if (!res.ok) {
    const d = await res.json().catch(() => ({}));
    alert('创建失败：' + (d.error || res.statusText));
  }
  loadSftp(sftpCurrent);
}

function formatSize(n) {
  if (!n && n !== 0) return '-';
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
  return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`;
}

// ====== RDP 共享盘（guacd drive-path 落地目录）======
let driveCurrent = '';

function openDriveModal(conn) {
  document.getElementById('drive-title').textContent = `共享盘 - ${conn.name}`;
  document.getElementById('drive-modal').classList.remove('hidden');
  loadDrive('');
}

function closeDriveModal() {
  document.getElementById('drive-modal').classList.add('hidden');
}

async function loadDrive(p) {
  const list = document.getElementById('drive-list');
  list.innerHTML = '<div class="sftp-empty loading">正在读取目录…</div>';
  try {
    const res = await fetch(`/api/rdp-drive/list?path=${encodeURIComponent(p || '')}`);
    const data = await res.json();
    if (!res.ok) throw new Error(data.error || res.statusText);
    driveCurrent = (data.path || '/').replace(/^\/+/, '');
    document.getElementById('drive-path').value = data.path || '/';
    renderDriveEntries(data.entries || []);
  } catch (e) {
    list.innerHTML = `<div class="sftp-empty">${escapeHTML(e.message)}</div>`;
  }
}

function renderDriveEntries(entries) {
  const list = document.getElementById('drive-list');
  if (!entries.length) {
    list.innerHTML = '<div class="sftp-empty">共享盘中暂无文件</div>';
    return;
  }
  list.innerHTML = entries.map((e) => `
    <div class="sftp-row">
      <span>${e.dir ? '📁' : '📄'}</span>
      <span class="name ${e.dir ? 'dir' : ''}" data-path="${escapeHTML(e.path)}" data-dir="${e.dir}" data-name="${escapeHTML(e.name)}">${escapeHTML(e.name)}</span>
      <span class="meta">${e.dir ? '-' : formatSize(e.size)}</span>
      <span class="meta">${(e.mod_time || '').replace('T', ' ').slice(0, 19)}</span>
      <span>${e.dir ? '' : `<a href="/api/rdp-drive/download?path=${encodeURIComponent(e.path)}" download>下载</a>`}</span>
    </div>`).join('');

  list.querySelectorAll('.name').forEach((el) => {
    el.addEventListener('click', () => {
      if (el.dataset.dir === 'true') loadDrive(el.dataset.path);
    });
  });
  list.querySelectorAll('.name[data-dir="false"]').forEach((el) => {
    bindDragOut(el, `/api/rdp-drive/download?path=${encodeURIComponent(el.dataset.path)}`, el.dataset.name);
  });
}

// 触发浏览器保存 Blob（RDP 共享盘下载）
function saveBlob(blob, filename) {
  const base = (filename || 'download').split(/[\\/]/).pop() || 'download';
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = base;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 10000);
}

// ====== 拖出下载 & 同步到本地目录 ======
let syncBusy = false;
let syncCancelled = false;
let syncAbort = null;

function supportsDirectoryPicker() {
  return typeof window.showDirectoryPicker === 'function';
}

// 文件行支持拖出：拖到 Finder / 资源管理器即落成文件（Chrome / Edge）
function bindDragOut(el, url, name) {
  el.setAttribute('draggable', 'true');
  el.classList.add('drag-out');
  el.title = '拖到桌面或本地文件夹即可下载';
  el.addEventListener('dragstart', (e) => {
    const safe = String(name || 'download').replace(/[\\/:*?"<>|\r\n]/g, '_');
    const abs = new URL(url, location.href).href;
    e.dataTransfer.setData('DownloadURL', `application/octet-stream:${safe}:${abs}`);
    e.dataTransfer.effectAllowed = 'copy';
  });
}

function sftpSource(connId) {
  return {
    list: (p) => fetchJSON(`/api/sftp/${connId}/list?path=${encodeURIComponent(p || '.')}`),
    fileUrl: (p) => `/api/sftp/${connId}/download?path=${encodeURIComponent(p)}`,
  };
}

function driveSource() {
  return {
    list: (p) => fetchJSON(`/api/rdp-drive/list?path=${encodeURIComponent(p || '')}`),
    fileUrl: (p) => `/api/rdp-drive/download?path=${encodeURIComponent(p)}`,
  };
}

async function fetchJSON(url) {
  const res = await fetch(url);
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || res.statusText);
  return data;
}

function closeUploadPanel() {
  document.getElementById('upload-panel').classList.add('hidden');
  // 同步任务进行中关闭浮层即视为取消
  if (syncBusy) {
    syncCancelled = true;
    if (syncAbort) syncAbort.abort();
  }
}

// 递归收集远端目录下的文件与子目录（返回的文件顺序与目录顺序都是父在前）
async function collectRemoteTree(source, remotePath, relBase, files, dirs) {
  if (syncCancelled) return;
  const data = await source.list(remotePath);
  for (const e of data.entries || []) {
    const rel = relBase ? `${relBase}/${e.name}` : e.name;
    if (e.dir) {
      dirs.push(rel);
      await collectRemoteTree(source, e.path, rel, files, dirs);
    } else {
      files.push({ path: e.path, rel, size: e.size || 0 });
    }
  }
}

async function dirHandleAt(root, rel, create) {
  let h = root;
  for (const part of String(rel || '').split('/').filter(Boolean)) {
    h = await h.getDirectoryHandle(part, { create });
  }
  return h;
}

async function fileHandleAt(root, rel) {
  const parts = String(rel).split('/').filter(Boolean);
  const name = parts.pop();
  const dir = await dirHandleAt(root, parts.join('/'), true);
  return dir.getFileHandle(name, { create: true });
}

function localRootName(p) {
  const name = String(p || '')
    .replace(/[\\/]+$/, '')
    .split('/')
    .filter((s) => s && s !== '.' && s !== '..')
    .pop();
  return (name || 'remote-files').replace(/[\\/:*?"<>|\r\n]/g, '_');
}

// syncToLocal 把远端目录递归写入用户选择的本地文件夹（File System Access API）
async function syncToLocal(source, remotePath) {
  if (!supportsDirectoryPicker()) {
    alert('当前浏览器不支持「同步到本地目录」，请使用 Chrome 或 Edge（需 HTTPS 或 localhost 访问）。');
    return;
  }
  if (syncBusy || uploading) {
    alert('已有任务进行中，请稍候');
    return;
  }
  let dest;
  try {
    dest = await window.showDirectoryPicker({ mode: 'readwrite', id: 'rcm-sync' });
  } catch (e) {
    return; // 用户取消选择
  }

  const ui = uploadPanel();
  ui.panel.classList.remove('hidden');
  ui.errors.innerHTML = '';
  ui.fill.style.width = '0%';
  ui.title.textContent = '同步到本地';
  ui.status.textContent = '正在读取远端目录…';
  syncBusy = true;
  syncCancelled = false;
  syncAbort = new AbortController();
  let doneCount = 0;

  try {
    const files = [];
    const dirs = [];
    await collectRemoteTree(source, remotePath, '', files, dirs);
    if (syncCancelled) return;

    const rootName = localRootName(remotePath);
    const root = await dest.getDirectoryHandle(rootName, { create: true });
    ui.title.textContent = `同步 ${rootName} 到本地`;

    for (const d of dirs) {
      if (syncCancelled) return;
      await dirHandleAt(root, d, true);
    }

    const totalBytes = files.reduce((n, f) => n + (f.size || 0), 0);
    let doneBytes = 0;
    const failed = [];

    for (let i = 0; i < files.length; i++) {
      if (syncCancelled) break;
      const f = files[i];
      ui.status.textContent = `同步中 ${i + 1}/${files.length}：${f.rel}`;
      try {
        const res = await fetch(source.fileUrl(f.path), { signal: syncAbort.signal });
        if (!res.ok) {
          const d = await res.json().catch(() => ({}));
          throw new Error(d.error || res.statusText);
        }
        const fh = await fileHandleAt(root, f.rel);
        const w = await fh.createWritable();
        if (res.body) {
          await res.body.pipeTo(w);
        } else {
          await w.write(await res.blob());
          await w.close();
        }
      } catch (err) {
        if (syncCancelled) break;
        failed.push(`${f.rel}：${err.message}`);
        ui.errors.innerHTML = failed.map((m) => `<div>${escapeHTML(m)}</div>`).join('');
      }
      doneCount++;
      doneBytes += f.size || 0;
      const pct = totalBytes
        ? (doneBytes / totalBytes) * 100
        : ((i + 1) / files.length) * 100;
      ui.fill.style.width = `${Math.min(100, Math.round(pct))}%`;
    }

    if (syncCancelled) {
      ui.status.textContent = `已取消，完成 ${doneCount}/${files.length} 个文件`;
      return;
    }
    ui.fill.style.width = '100%';
    if (!files.length) {
      ui.status.textContent = `远端目录为空，已创建本地文件夹「${rootName}」`;
    } else {
      ui.status.textContent = failed.length
        ? `完成 ${doneCount - failed.length}/${files.length} 个文件，失败 ${failed.length} 个`
        : `已同步 ${files.length} 个文件到本地「${rootName}」`;
    }
    if (!failed.length) {
      setTimeout(() => {
        if (!uploading && !syncBusy) ui.panel.classList.add('hidden');
      }, 5000);
    }
  } catch (err) {
    ui.status.textContent = '同步失败：' + err.message;
  } finally {
    syncBusy = false;
    syncAbort = null;
  }
}

// ====== SSH 端口转发 ======
let tunnelConn = null;

function openTunnelModal(conn) {
  tunnelConn = conn;
  document.getElementById('tunnel-title').textContent = `端口转发 - ${conn.name}`;
  document.getElementById('tunnel-modal').classList.remove('hidden');
  loadTunnels();
}

function closeTunnelModal() {
  document.getElementById('tunnel-modal').classList.add('hidden');
}

async function loadTunnels() {
  const box = document.getElementById('tunnel-list');
  box.innerHTML = '<div class="hint">加载中…</div>';
  try {
    const res = await fetch('/api/tunnels');
    const data = await res.json();
    const list = data.tunnels || [];
    if (!list.length) {
      box.innerHTML = '<div class="hint">暂无隧道</div>';
      return;
    }
    box.innerHTML = list.map((t) => {
      const mode = t.mode === 'remote' ? '反向' : '正向';
      const from = t.mode === 'remote'
        ? `<code>${escapeHTML(t.connection_name)}:${t.listen_port}</code>`
        : `<code>localhost:${t.listen_port}</code>`;
      const to = t.mode === 'remote'
        ? `本机 <code>${escapeHTML(t.target_host)}:${t.target_port}</code>`
        : `<code>${escapeHTML(t.target_host)}:${t.target_port}</code>`;
      return `
      <div class="tunnel-item">
        <span><em class="tunnel-tag">${mode}</em> ${from} → ${to}</span>
        <button data-tunnel="${escapeHTML(t.id)}" title="删除">✕</button>
      </div>`;
    }).join('');
    box.querySelectorAll('[data-tunnel]').forEach((el) => {
      el.addEventListener('click', () => deleteTunnel(el.dataset.tunnel));
    });
  } catch (e) {
    box.innerHTML = `<div class="hint">加载失败：${escapeHTML(e.message)}</div>`;
  }
}

function tunnelMode() {
  const el = document.querySelector('input[name="tunnel_mode"]:checked');
  return el ? el.value : 'local';
}

function toggleTunnelMode() {
  const remote = tunnelMode() === 'remote';
  document.getElementById('tunnel-local-fields').classList.toggle('hidden', remote);
  document.getElementById('tunnel-remote-fields').classList.toggle('hidden', !remote);
}

async function createTunnel(e) {
  e.preventDefault();
  if (!tunnelConn) return;
  const form = e.target;
  const mode = tunnelMode();
  let body = { connection_id: tunnelConn.id, mode };
  if (mode === 'remote') {
    const listenPort = parseInt(form.listen_port.value);
    const localPort = parseInt(form.local_port.value);
    if (!listenPort || !localPort) {
      alert('反向转发需要填写「远端监听端口」和「本机服务端口」');
      return;
    }
    body.listen_port = listenPort;
    body.listen_host = form.listen_host.value.trim() || '127.0.0.1';
    body.local_host = form.local_host.value.trim() || '127.0.0.1';
    body.local_port = localPort;
  } else {
    const targetPort = parseInt(form.remote_port.value);
    if (!targetPort) {
      alert('正向转发需要填写「远端目标端口」（例如 MySQL 就是 3306）');
      return;
    }
    body.remote_host = form.remote_host.value.trim() || '127.0.0.1';
    body.remote_port = targetPort;
  }
  const res = await fetch('/api/tunnels', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  const data = await res.json();
  if (!res.ok) {
    alert('创建失败：' + (data.error || res.statusText));
    return;
  }
  if (mode === 'remote') {
    form.listen_port.value = '';
    form.local_port.value = '';
  } else {
    form.remote_port.value = '';
  }
  loadTunnels();
}

async function deleteTunnel(id) {
  if (!confirm('关闭该隧道？')) return;
  await fetch(`/api/tunnels/${id}`, { method: 'DELETE' });
  loadTunnels();
}

// ====== 粘贴 / 拖拽上传（仅 SSH 会话）======
let uploading = false;

function hasFileDrag(e) {
  const dt = e.dataTransfer;
  if (!dt || !dt.types) return false;
  return Array.from(dt.types).includes('Files');
}

function uploadPanel() {
  return {
    panel: document.getElementById('upload-panel'),
    title: document.getElementById('upload-title'),
    fill: document.getElementById('upload-bar-fill'),
    status: document.getElementById('upload-status'),
    errors: document.getElementById('upload-errors'),
  };
}

function showUploadUnsupported() {
  const ui = uploadPanel();
  ui.panel.classList.remove('hidden');
  ui.title.textContent = '不支持上传';
  ui.fill.style.width = '0%';
  ui.status.textContent = '仅 SSH 会话支持拖拽 / 粘贴上传文件';
  ui.errors.innerHTML = '';
}

function onDragOver(e) {
  if (!hasFileDrag(e)) return;
  const sess = activeSession();
  if (!sess || sess.conn.type !== 'ssh') return;
  e.preventDefault();
  e.dataTransfer.dropEffect = 'copy';
  document.getElementById('session-viewer').classList.add('drop-active');
  document.getElementById('drop-hint').classList.remove('hidden');
}

function onDragLeave(e) {
  const viewer = document.getElementById('session-viewer');
  if (e.relatedTarget && viewer.contains(e.relatedTarget)) return;
  dragEnd();
}

function dragEnd() {
  document.getElementById('session-viewer').classList.remove('drop-active');
  document.getElementById('drop-hint').classList.add('hidden');
}

async function onDrop(e) {
  if (!hasFileDrag(e)) return;
  e.preventDefault();
  dragEnd();
  const sess = activeSession();
  if (!sess) return;
  if (sess.conn.type !== 'ssh') {
    showUploadUnsupported();
    return;
  }
  const items = await collectDropItems(e.dataTransfer);
  if (items.length) uploadItemsToSession(sess, items);
}

// collectDropItems 优先用 webkitGetAsEntry 递归读取文件夹（必须在事件同步阶段取 entry）
async function collectDropItems(dt) {
  const out = [];
  const entries = [];
  if (dt.items) {
    for (const it of Array.from(dt.items)) {
      if (it.kind !== 'file') continue;
      entries.push(it.webkitGetAsEntry ? it.webkitGetAsEntry() : null);
    }
  }
  if (entries.some(Boolean)) {
    for (const entry of entries) {
      if (entry) await walkEntry(entry, '', out);
    }
  }
  if (!out.length) {
    for (const f of Array.from(dt.files || [])) out.push({ file: f, rel: f.name });
  }
  return out;
}

async function walkEntry(entry, base, out) {
  if (entry.isFile) {
    const file = await new Promise((resolve, reject) => entry.file(resolve, reject));
    out.push({ file, rel: base ? `${base}/${file.name}` : file.name });
    return;
  }
  if (!entry.isDirectory) return;
  const prefix = base ? `${base}/${entry.name}` : entry.name;
  const reader = entry.createReader();
  // readEntries 每次最多返回一批，需要循环读到空
  while (true) {
    const batch = await new Promise((resolve, reject) => reader.readEntries(resolve, reject));
    if (!batch.length) break;
    for (const child of batch) await walkEntry(child, prefix, out);
  }
}

// resolveUploadDir 目标目录：文件面板当前目录优先，否则登录后的 home
async function resolveUploadDir(conn) {
  if (sftpConn && sftpConn.id === conn.id && sftpCurrent) return sftpCurrent;
  const res = await fetch(`/api/sftp/${conn.id}/list?path=.`);
  const data = await res.json();
  if (!res.ok) throw new Error(data.error || res.statusText);
  return data.path;
}

async function uploadItemsToSession(sess, items) {
  const ui = uploadPanel();
  ui.panel.classList.remove('hidden');
  ui.errors.innerHTML = '';
  ui.fill.style.width = '0%';
  ui.title.textContent = '上传文件';

  if (uploading) {
    ui.status.textContent = '已有上传任务进行中，请稍候';
    return;
  }
  uploading = true;

  let dir = '';
  try {
    dir = await resolveUploadDir(sess.conn);
  } catch (err) {
    ui.status.textContent = '无法确定目标目录：' + err.message;
    uploading = false;
    return;
  }
  ui.title.textContent = `上传到 ${dir}`;
  ui.status.textContent = '准备中…';

  const total = items.length;
  const failed = [];
  let done = 0;

  for (const it of items) {
    ui.status.textContent = `上传中 ${done + 1}/${total}：${it.rel}`;
    try {
      const fd = new FormData();
      fd.append('file', it.file, it.file.name);
      fd.append('rel', it.rel);
      fd.append('path', dir);
      const res = await fetch(`/api/sftp/${sess.conn.id}/upload`, { method: 'POST', body: fd });
      if (!res.ok) {
        const d = await res.json().catch(() => ({}));
        throw new Error(d.error || res.statusText);
      }
    } catch (err) {
      failed.push(`${it.rel}：${err.message}`);
    }
    done++;
    ui.fill.style.width = `${Math.round((done / total) * 100)}%`;
    if (failed.length) {
      ui.errors.innerHTML = failed.map((m) => `<div>${escapeHTML(m)}</div>`).join('');
    }
  }

  ui.status.textContent = failed.length
    ? `完成 ${total - failed.length}/${total}，失败 ${failed.length} 个`
    : `完成 ${total}/${total} 个文件`;
  uploading = false;

  // 文件面板正开着就刷新到目标目录，便于确认结果
  if (sftpConn && sftpConn.id === sess.conn.id) {
    sftpCurrent = dir;
    loadSftp(dir);
  }
  if (!failed.length) {
    setTimeout(() => {
      if (!uploading && !syncBusy) ui.panel.classList.add('hidden');
    }, 5000);
  }
}

// ====== Web Netcat（网络监听 / 反弹 shell / 特征应答） ======
let ncListeners = [];
let ncSelectedId = null;
let ncTab = 'sessions';
let ncSessionsCache = [];
let ncEventsCache = [];
let ncTimer = null;
let ncPayloadFiles = [];
let ncTerm = null;

document.addEventListener('DOMContentLoaded', () => {
  document.getElementById('btn-netcat').addEventListener('click', openNetcatModal);
  document.getElementById('nc-new-btn').addEventListener('click', () => openListenerForm());
  document.getElementById('nc-payload-btn').addEventListener('click', openPayloadModal);
  document.getElementById('nc-form').addEventListener('submit', saveListenerForm);
  document.getElementById('nc-term-close').addEventListener('click', closeNcTerminal);
  document.getElementById('nc-term-kill').addEventListener('click', killNcTerminalSession);
  document.getElementById('nc-term-send').addEventListener('click', sendNcTermCommand);
  document.getElementById('nc-term-cmd').addEventListener('keydown', (e) => {
    if (e.key === 'Enter') { e.preventDefault(); sendNcTermCommand(); }
  });
  window.addEventListener('resize', () => {
    if (ncTerm) { try { ncTerm.fit.fit(); } catch (e) {} }
  });
});

function openNetcatFor(id) {
  openNetcatModal(id);
}

function openNetcatModal(id) {
  // 顶栏按钮会把 click 事件传进来，只有字符串才当作监听 ID
  if (typeof id === 'string') ncSelectedId = id;
  document.getElementById('netcat-modal').classList.remove('hidden');
  loadNcListeners(true);
  if (ncTimer) clearInterval(ncTimer);
  // 每 2.5s 刷新一次状态/会话数，弹窗关闭即停
  ncTimer = setInterval(() => {
    if (document.getElementById('netcat-modal').classList.contains('hidden')) return;
    loadNcListeners(false);
  }, 2500);
}

function closeNetcatModal() {
  document.getElementById('netcat-modal').classList.add('hidden');
  if (ncTimer) { clearInterval(ncTimer); ncTimer = null; }
}

async function loadNcListeners(pickFirst) {
  try {
    const res = await fetch('/api/netcat/listeners');
    const data = await res.json();
    ncListeners = data.listeners || [];
    if (!ncSelectedId || !ncListeners.some((l) => l.id === ncSelectedId)) {
      ncSelectedId = ncListeners.length ? ncListeners[0].id : null;
    }
    renderNcList();
    if (pickFirst || ncSelectedId) await loadNcDetail();
  } catch (e) {
    document.getElementById('nc-list').innerHTML =
      `<div class="nc-empty">加载失败：${escapeHtml(e.message)}</div>`;
  }
}

function ncSelected() {
  return ncListeners.find((l) => l.id === ncSelectedId) || null;
}

function renderNcList() {
  const box = document.getElementById('nc-list');
  if (!ncListeners.length) {
    box.innerHTML = '<div class="nc-empty">还没有监听<br>点右上「新建监听」</div>';
    return;
  }
  box.innerHTML = ncListeners.map((l) => `
    <div class="nc-item ${l.running ? 'running' : ''} ${l.id === ncSelectedId ? 'selected' : ''}" data-id="${l.id}">
      <div class="nc-item-title"><span class="dot"></span>${escapeHtml(l.name)}</div>
      <div class="nc-item-meta">
        <span><code>${escapeHtml(l.bind)}:${l.port}</code> · ${l.mode}</span>
        <span class="nc-item-sessions">${l.active}/${l.sessions}</span>
      </div>
    </div>`).join('');
  box.querySelectorAll('.nc-item').forEach((el) => {
    el.addEventListener('click', () => selectNcListener(el.dataset.id));
  });
}

function selectNcListener(id) {
  ncSelectedId = id;
  ncTab = 'sessions';
  renderNcList();
  loadNcDetail();
}

async function loadNcDetail() {
  const l = ncSelected();
  if (!l) {
    document.getElementById('nc-detail').innerHTML = '<div class="nc-empty">左侧选一个监听查看会话与事件</div>';
    return;
  }
  try {
    const [sres, eres] = await Promise.all([
      fetch(`/api/netcat/listeners/${l.id}/sessions?limit=40`),
      fetch(`/api/netcat/listeners/${l.id}/events?limit=120`),
    ]);
    ncSessionsCache = (await sres.json()).sessions || [];
    ncEventsCache = (await eres.json()).events || [];
  } catch (e) {
    ncSessionsCache = [];
    ncEventsCache = [];
  }
  renderNcDetail();
}

function ncModeLabel(mode) {
  return {
    shell: 'shell 终端', raw: '特征应答', redis: 'Redis Rogue',
    ldap: 'LDAP/JNDI', rmi: 'RMI', http: 'HTTP CodeBase',
  }[mode] || mode;
}

function renderNcDetail() {
  const l = ncSelected();
  const box = document.getElementById('nc-detail');
  if (!l) { box.innerHTML = '<div class="nc-empty">左侧选一个监听</div>'; return; }
  const addr = l.running ? (l.address || `${l.bind}:${l.port}`) : `${l.bind}:${l.port}（未启动）`;
  box.innerHTML = `
    <div class="nc-detail-head">
      <div>
        <div class="nc-title">${escapeHtml(l.name)} <span class="hint">${ncModeLabel(l.mode)}</span></div>
        <div class="nc-sub"><code>${escapeHtml(addr)}</code>${l.started_at ? ' · 启动于 ' + escapeHtml(l.started_at) : ''}</div>
      </div>
      <div class="nc-actions">
        <button data-act="toggle">${l.running ? '停止' : '启动'}</button>
        <button data-act="edit">编辑</button>
        <button data-act="delete" class="danger">删除</button>
      </div>
    </div>
    <div class="nc-stats">
      <span>会话 <b>${l.sessions}</b>（活跃 <b>${l.active}</b>）</span>
      <span>收 <b>${formatSize(l.total_in || 0)}</b> · 发 <b>${formatSize(l.total_out || 0)}</b></span>
      <span>规则命中 <b>${l.rule_hits || 0}</b></span>
      <span>规则数 <b>${(l.rules || []).length}</b></span>
    </div>
    ${l.error ? `<div class="nc-err">${escapeHtml(l.error)}</div>` : ''}
    <div class="nc-tabs">
      <button class="nc-tab ${ncTab === 'sessions' ? 'active' : ''}" data-tab="sessions">会话</button>
      <button class="nc-tab ${ncTab === 'rules' ? 'active' : ''}" data-tab="rules">规则</button>
      <button class="nc-tab ${ncTab === 'events' ? 'active' : ''}" data-tab="events">事件</button>
    </div>
    <div class="nc-pane" id="nc-pane"></div>`;
  box.querySelector('[data-act="toggle"]').addEventListener('click', () => toggleListener(l));
  box.querySelector('[data-act="edit"]').addEventListener('click', () => openListenerForm(l.id));
  box.querySelector('[data-act="delete"]').addEventListener('click', () => deleteListener(l.id));
  box.querySelectorAll('.nc-tab').forEach((b) => {
    b.addEventListener('click', () => { ncTab = b.dataset.tab; renderNcDetail(); });
  });
  renderNcPane();
}

function renderNcPane() {
  const pane = document.getElementById('nc-pane');
  if (!pane) return;
  if (ncTab === 'rules') {
    const rules = (ncSelected().rules || []);
    pane.innerHTML = rules.length ? rules.map((r) => `
      <div class="nc-rule-chip">
        <div>
          <div class="name">${escapeHtml(r.name || r.id.slice(0, 8))}
            ${r.enabled ? '' : '<span class="hint">（已停用）</span>'}</div>
          <div class="p">${r.match_type} <code>${escapeHtml(r.pattern)}</code>
            → ${r.payload_type}${r.payload_file ? '<code>' + escapeHtml(r.payload_file) + '</code>' : ''}
            ${r.wrap === 'bulk' ? '（bulk 包装）' : ''}${r.close_after ? ' · 发送后断开' : ''}</div>
        </div>
        <div class="hint">${r.send_times ? '最多 ' + r.send_times + ' 次' : '不限次数'}</div>
      </div>`).join('') : '<div class="nc-empty">还没有规则，点「编辑」添加：例如特征 <code>exp.so</code> → 推送 payload 文件 exp.so</div>';
    return;
  }
  if (ncTab === 'events') {
    pane.innerHTML = ncEventsCache.length ? ncEventsCache.map((e) => `
      <div class="nc-event ${e.type}">
        <span class="t">${escapeHtml(e.time)}</span>
        <span class="ty">${escapeHtml(e.type)}</span>
        <span class="rm">${escapeHtml(e.remote || '')}</span>
        <span class="dt">${escapeHtml(e.detail || '')}</span>
      </div>`).join('') : '<div class="nc-empty">暂无事件</div>';
    return;
  }
  pane.innerHTML = ncSessionsCache.length ? ncSessionsCache.map((s) => `
    <div class="nc-sess-item ${s.closed ? 'closed' : ''}">
      <div class="nc-sess-main">
        <div class="nc-sess-name">${escapeHtml(s.remote)}
          ${s.closed ? '<span class="hint">已结束</span>' : '<span class="attached">在线</span>'}
          ${s.attached ? '<span class="hint">· 终端已挂载</span>' : ''}</div>
        <div class="nc-sess-meta">${escapeHtml(s.started_at)} · 收 ${formatSize(s.bytes_in)} / 发 ${formatSize(s.bytes_out)}${s.auto_cmds ? ' · 自动命令 ' + s.auto_cmds + ' 条' : ''}</div>
      </div>
      <button data-term="${s.id}">终端</button>
      ${s.closed ? '' : `<button data-kill="${s.id}" class="danger">断开</button>`}
    </div>`).join('') : '<div class="nc-empty">这个监听还没有连接进来</div>';
  pane.querySelectorAll('[data-term]').forEach((b) => {
    b.addEventListener('click', () => openNcTerminal(b.dataset.term));
  });
  pane.querySelectorAll('[data-kill]').forEach((b) => {
    b.addEventListener('click', () => killNcSession(b.dataset.kill));
  });
}

async function toggleListener(l) {
  const act = l.running ? 'stop' : 'start';
  const res = await fetch(`/api/netcat/listeners/${l.id}/${act}`, { method: 'POST' });
  if (!res.ok) {
    const d = await res.json().catch(() => ({}));
    alert('操作失败：' + (d.error || res.statusText));
  }
  loadNcListeners(false);
  loadConnections();
}

async function deleteListener(id) {
  if (!confirm('删除该监听？正在接入的会话会被断开。')) return;
  await fetch(`/api/netcat/listeners/${id}`, { method: 'DELETE' });
  if (ncSelectedId === id) ncSelectedId = null;
  loadNcListeners(true);
  loadConnections();
}

async function killNcSession(id) {
  if (!confirm('断开这个会话？')) return;
  await fetch(`/api/netcat/sessions/${id}`, { method: 'DELETE' });
  loadNcDetail();
}

// ---------- 监听配置表单 ----------

async function loadNcPayloadOptions() {
  try {
    const res = await fetch('/api/netcat/payloads');
    ncPayloadFiles = (await res.json()).payloads || [];
  } catch (e) { ncPayloadFiles = []; }
  return ncPayloadFiles;
}

function fillPayloadSelect(sel, cur) {
  if (!sel) return;
  sel.innerHTML = '<option value="">-- 不发送 --</option>' +
    ncPayloadFiles.map((p) => `<option value="${escapeHtml(p.name)}">${escapeHtml(p.name)}（${formatSize(p.size)}）</option>`).join('');
  if (cur) sel.value = cur;
}

function openListenerForm(id) {
  const l = id ? ncListeners.find((x) => x.id === id) : null;
  const form = document.getElementById('nc-form');
  form.reset();
  document.getElementById('nc-form-title').textContent = l ? `编辑监听 - ${l.name}` : '新建监听';
  form.elements.id.value = l ? l.id : '';
  document.getElementById('nc-rules').innerHTML = '';
  document.getElementById('nc-form-modal').classList.remove('hidden');

  loadNcPayloadOptions().then(() => {
    fillPayloadSelect(document.getElementById('nc-payload-file'), l ? l.payload_file : '');
    if (!l) return;
    form.elements.name.value = l.name || '';
    form.elements.bind.value = l.bind || '0.0.0.0';
    form.elements.port.value = l.port || '';
    form.elements.mode.value = l.mode || 'shell';
    form.elements.max_conns.value = l.max_conns || 64;
    form.elements.initial_delay_ms.value = l.initial_delay_ms || 0;
    form.elements.auto_start.checked = !!l.auto_start;
    form.elements.banner.value = l.banner || '';
    form.elements.java_class_name.value = l.java_class_name || '';
    form.elements.java_factory.value = l.java_factory || '';
    form.elements.java_code_base.value = l.java_code_base || '';
    form.elements.jndi_ref_address.value = l.jndi_ref_address || '';
    form.elements.java_serialized_payload.value = l.java_serialized_payload || '';
    form.elements.ldap_referral.value = l.ldap_referral || '';
    form.elements.http_status.value = l.http_status || 200;
    form.elements.http_content_type.value = l.http_content_type || '';
    form.elements.http_body.value = l.http_body || '';
    form.elements.serve_payload_dir.checked = !!l.serve_payload_dir;
    form.elements.payload.value = l.payload || '';
    document.getElementById('nc-auto-cmds').value = formatNcAutoCmds(l.auto_cmds || []);
    (l.rules || []).forEach((r) => addNcRule(r));
    onNcModeChange();
  });
  onNcModeChange();
}

function closeListenerForm() {
  document.getElementById('nc-form-modal').classList.add('hidden');
}

function onNcModeChange() {
  const mode = document.getElementById('nc-mode').value;
  document.querySelectorAll('#nc-form .nc-mode-ldap').forEach((el) => el.classList.toggle('hidden', mode !== 'ldap'));
  document.querySelectorAll('#nc-form .nc-mode-http').forEach((el) => el.classList.toggle('hidden', mode !== 'http'));
  document.querySelectorAll('#nc-form .nc-mode-rmi').forEach((el) => el.classList.toggle('hidden', mode !== 'rmi'));
  document.getElementById('nc-autocmd-section').classList.toggle('hidden', mode !== 'shell');
  // 默认 payload 文件只对 rmi/http/redis 有意义
  document.getElementById('nc-payload-file-row').classList.toggle('hidden', !['rmi', 'http', 'redis', 'raw'].includes(mode));
}

function ncRuleRowHTML(r) {
  r = r || {};
  const ptype = r.payload_type || 'text';
  const opts = (sel, val) => sel.map((o) => `<option value="${o}" ${o === val ? 'selected' : ''}>${o}</option>`).join('');
  return `
  <div class="nc-rule" data-rule-id="${r.id || ''}">
    <div class="nc-rule-top">
      <input type="text" class="r-name" placeholder="规则名称（可选）" value="${escapeHtml(r.name || '')}">
      <label class="nc-rule-flag"><input type="checkbox" class="r-enabled" ${r.enabled === false ? '' : 'checked'}>启用</label>
      <button type="button" class="nc-rule-del" onclick="this.closest('.nc-rule').remove()" title="删除规则">✕</button>
    </div>
    <div class="nc-rule-grid">
      <label>匹配方式<select class="r-match">
        ${opts(['contains', 'prefix', 'suffix', 'regex', 'hex'], r.match_type || 'contains')}
      </select></label>
      <label>特征/正则<input type="text" class="r-pattern" placeholder="exp.so / ldap:// / \\x00\\x01" value="${escapeHtml(r.pattern || '')}"></label>
      <label>匹配窗口(字节)<input type="number" class="r-window" min="0" placeholder="4096" value="${r.window || ''}"></label>
    </div>
    <div class="nc-rule-grid">
      <label>载荷类型<select class="r-ptype" onchange="onNcRuleTypeChange(this)">
        ${opts(['text', 'base64', 'file'], ptype)}
      </select></label>
      <label class="r-file-wrap ${ptype === 'file' ? '' : 'hidden'}">Payload 文件<select class="r-pfile"></select></label>
      <label>发送包装<select class="r-wrap">
        ${['', 'bulk'].map((v) => `<option value="${v}" ${v === (r.wrap || '') ? 'selected' : ''}>${v === 'bulk' ? 'redis bulk（$len…）' : '原样发送'}</option>`).join('')}
      </select></label>
    </div>
    <label class="wide r-payload-wrap ${ptype === 'file' ? 'hidden' : ''}">载荷内容（text 或 base64）
      <textarea class="r-payload" rows="2" spellcheck="false">${escapeHtml(r.payload || '')}</textarea>
    </label>
    <div class="nc-rule-grid">
      <label>每连接最多命中<input type="number" class="r-times" min="0" value="${r.send_times || 0}" placeholder="0=不限"></label>
      <label>延迟发送(ms)<input type="number" class="r-delay" min="0" value="${r.delay_ms || 0}"></label>
      <label class="nc-rule-flag"><input type="checkbox" class="r-icase" ${r.ignore_case ? 'checked' : ''}>忽略大小写</label>
      <label class="nc-rule-flag"><input type="checkbox" class="r-close" ${r.close_after ? 'checked' : ''}>发送后断连</label>
    </div>
  </div>`;
}

function addNcRule(rule, root) {
  const box = root || document.getElementById('nc-rules');
  if (!box) return null;
  const div = document.createElement('div');
  div.innerHTML = ncRuleRowHTML(rule);
  const row = div.firstElementChild;
  box.appendChild(row);
  const sel = row.querySelector('.r-pfile');
  fillPayloadSelect(sel, rule && rule.payload_file);
  return row;
}

function onNcRuleTypeChange(sel) {
  const row = sel.closest('.nc-rule');
  const isFile = sel.value === 'file';
  row.querySelector('.r-file-wrap').classList.toggle('hidden', !isFile);
  row.querySelector('.r-payload-wrap').classList.toggle('hidden', isFile);
}

function formatNcAutoCmds(cmds) {
  return (cmds || []).map((c) => {
    let line = c.cmd || '';
    if (c.delay_ms) line += ` | ${c.delay_ms}`;
    if (c.wait_for) line += ` @ ${c.wait_for}`;
    return line;
  }).join('\n');
}

function parseNcAutoCmds(text) {
  return (text || '').split('\n').map((l) => l.trim()).filter(Boolean).map((line) => {
    let cmd = line, delay = 0, wait = '';
    const at = line.indexOf('@');
    if (at > 0) { wait = line.slice(at + 1).trim(); cmd = line.slice(0, at).trim(); }
    const bar = cmd.lastIndexOf('|');
    if (bar > 0) {
      const t = cmd.slice(bar + 1).trim();
      if (/^\d+$/.test(t)) { delay = parseInt(t, 10); cmd = cmd.slice(0, bar).trim(); }
    }
    return { cmd, delay_ms: delay, wait_for: wait };
  }).filter((c) => c.cmd);
}

function collectNcRules(root) {
  const box = root || document.getElementById('nc-rules');
  if (!box) return [];
  return Array.from(box.querySelectorAll('.nc-rule')).map((row) => ({
    id: row.dataset.ruleId || '',
    name: row.querySelector('.r-name').value.trim(),
    enabled: row.querySelector('.r-enabled').checked,
    match_type: row.querySelector('.r-match').value,
    pattern: row.querySelector('.r-pattern').value,
    ignore_case: row.querySelector('.r-icase').checked,
    window: parseInt(row.querySelector('.r-window').value, 10) || 0,
    payload_type: row.querySelector('.r-ptype').value,
    payload: row.querySelector('.r-payload').value,
    payload_file: row.querySelector('.r-pfile').value,
    wrap: row.querySelector('.r-wrap').value,
    send_times: parseInt(row.querySelector('.r-times').value, 10) || 0,
    delay_ms: parseInt(row.querySelector('.r-delay').value, 10) || 0,
    close_after: row.querySelector('.r-close').checked,
  }));
}

async function saveListenerForm(e) {
  e.preventDefault();
  const f = e.target;
  const body = {
    id: f.elements.id.value,
    name: f.elements.name.value.trim(),
    bind: f.elements.bind.value.trim() || '0.0.0.0',
    port: parseInt(f.elements.port.value, 10),
    mode: f.elements.mode.value,
    max_conns: parseInt(f.elements.max_conns.value, 10) || 64,
    initial_delay_ms: parseInt(f.elements.initial_delay_ms.value, 10) || 0,
    auto_start: f.elements.auto_start.checked,
    banner: f.elements.banner.value,
    payload_file: f.elements.payload_file.value,
    auto_cmds: parseNcAutoCmds(document.getElementById('nc-auto-cmds').value),
    rules: collectNcRules(),
    java_class_name: f.elements.java_class_name.value.trim(),
    java_factory: f.elements.java_factory.value.trim(),
    java_code_base: f.elements.java_code_base.value.trim(),
    jndi_ref_address: f.elements.jndi_ref_address.value.trim(),
    java_serialized_payload: f.elements.java_serialized_payload.value.trim(),
    ldap_referral: f.elements.ldap_referral.value.trim(),
    http_status: parseInt(f.elements.http_status.value, 10) || 200,
    http_content_type: f.elements.http_content_type.value.trim(),
    http_body: f.elements.http_body.value,
    serve_payload_dir: f.elements.serve_payload_dir.checked,
    payload: f.elements.payload.value.trim(),
  };
  const isEdit = !!body.id;
  const url = isEdit ? `/api/netcat/listeners/${body.id}` : '/api/netcat/listeners';
  const res = await fetch(url, {
    method: isEdit ? 'PUT' : 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) { alert('保存失败：' + (data.error || res.statusText)); return; }
  closeListenerForm();
  if (data.listener && data.listener.id) ncSelectedId = data.listener.id;
  loadNcListeners(true);
  loadConnections();
}

// ---------- Payload 文件管理 ----------

async function openPayloadModal() {
  document.getElementById('nc-payload-modal').classList.remove('hidden');
  await loadPayloadList();
}

function closePayloadModal() {
  document.getElementById('nc-payload-modal').classList.add('hidden');
}

async function loadPayloadList() {
  const box = document.getElementById('nc-payload-list');
  box.innerHTML = '<div class="hint">加载中…</div>';
  try {
    const res = await fetch('/api/netcat/payloads');
    const data = await res.json();
    const list = data.payloads || [];
    if (!list.length) {
      box.innerHTML = '<div class="hint">还没有 payload 文件，选文件后点「上传」</div>';
      return;
    }
    box.innerHTML = list.map((p) => `
      <div class="tunnel-item">
        <span><code>${escapeHtml(p.name)}</code> · ${formatSize(p.size)} · ${escapeHtml(p.modified)}</span>
        <button data-del="${escapeHtml(p.name)}" title="删除">✕</button>
      </div>`).join('');
    box.querySelectorAll('[data-del]').forEach((b) => {
      b.addEventListener('click', async () => {
        if (!confirm(`删除 ${b.dataset.del}？`)) return;
        await fetch(`/api/netcat/payloads/${encodeURIComponent(b.dataset.del)}`, { method: 'DELETE' });
        loadPayloadList();
      });
    });
  } catch (e) {
    box.innerHTML = `<div class="hint">加载失败：${escapeHtml(e.message)}</div>`;
  }
}

async function uploadNcPayload() {
  const input = document.getElementById('nc-payload-input');
  const file = input.files && input.files[0];
  if (!file) { alert('请先选择文件'); return; }
  const fd = new FormData();
  fd.append('file', file, file.name);
  const res = await fetch('/api/netcat/payloads', { method: 'POST', body: fd });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) { alert('上传失败：' + (data.error || res.statusText)); return; }
  input.value = '';
  loadPayloadList();
  loadNcPayloadOptions();
}

// ---------- 浏览器终端（挂到 nc 会话） ----------

async function openNcTerminal(sessionId) {
  const s = ncSessionsCache.find((x) => x.id === sessionId);
  closeNcTerminal();
  const box = document.getElementById('nc-term');
  box.classList.remove('hidden');
  const title = document.getElementById('nc-term-title');
  title.textContent = s ? `${ncSelected()?.name || '会话'} · ${s.remote} · ${s.started_at}` : '会话';

  const body = document.getElementById('nc-term-body');
  body.innerHTML = '';
  const term = new Terminal({
    cursorBlink: true,
    fontSize: 14,
    fontFamily: 'Menlo, Monaco, "Courier New", monospace',
    theme: { background: '#000000', foreground: '#e6e6e6' },
    scrollback: 5000,
  });
  const fit = new FitAddon.FitAddon();
  term.loadAddon(fit);
  if (window.CanvasAddon && CanvasAddon.CanvasAddon) {
    try { term.loadAddon(new CanvasAddon.CanvasAddon()); } catch (e) {}
  }
  term.open(body);
  try { fit.fit(); } catch (e) {}

  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  const ws = new WebSocket(`${proto}://${location.host}/ws/netcat/${sessionId}`);
  ws.binaryType = 'arraybuffer';

  ws.onopen = () => {
    try { fit.fit(); } catch (e) {}
    term.focus();
  };
  ws.onmessage = (ev) => {
    if (typeof ev.data === 'string') term.write(ev.data);
    else term.write(new Uint8Array(ev.data));
  };
  ws.onclose = () => {
    term.write('\r\n\x1b[31m[终端已断开]\x1b[0m\r\n');
    const st = document.querySelector('#nc-term-title .status');
    if (st) { st.textContent = '已结束'; st.classList.add('closed'); }
  };
  ws.onerror = () => term.write('\r\n\x1b[31m[连接错误]\x1b[0m\r\n');
  term.onData((data) => {
    if (ws.readyState === WebSocket.OPEN) ws.send(data);
  });

  ncTerm = { term, fit, ws, id: sessionId };
  setTimeout(() => { try { fit.fit(); } catch (e) {} }, 60);
  setTimeout(() => loadNcDetail(), 1200);
}

function closeNcTerminal() {
  document.getElementById('nc-term').classList.add('hidden');
  if (!ncTerm) return;
  try { ncTerm.ws.close(); } catch (e) {}
  try { ncTerm.term.dispose(); } catch (e) {}
  ncTerm = null;
  document.getElementById('nc-term-body').innerHTML = '';
}

async function sendNcTermCommand() {
  const input = document.getElementById('nc-term-cmd');
  const cmd = input.value;
  if (!cmd) return;
  input.value = '';
  if (ncTerm && ncTerm.ws.readyState === WebSocket.OPEN) {
    ncTerm.ws.send(cmd + '\n');
    if (ncTerm.term) ncTerm.term.focus();
    return;
  }
  if (ncTerm) {
    await fetch(`/api/netcat/sessions/${ncTerm.id}/exec`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ cmd }),
    });
  }
}

async function killNcTerminalSession() {
  if (!ncTerm) return;
  if (!confirm('断开这个会话？')) return;
  await fetch(`/api/netcat/sessions/${ncTerm.id}`, { method: 'DELETE' });
}
