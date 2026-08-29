const state = {
  view: 'overview',
  data: {
    messages: [],
    credentials: [],
    destinations: [],
  },
};

const basePath = new URL('.', document.currentScript.src).pathname.replace(/\/$/, '');

const views = {
  overview: '概览',
  messages: '邮件记录',
  credentials: '接入 API Key',
  destinations: 'Bark 设备',
};

const statusText = {
  pending: '待处理',
  processing: '处理中',
  retrying: '重试中',
  delivered: '已投递',
  dead_letter: '死信',
  ignored: '已忽略',
};

const $ = (selector) => document.querySelector(selector);
const $$ = (selector) => [...document.querySelectorAll(selector)];

function esc(value) {
  return String(value ?? '').replace(/[&<>"']/g, (char) => ({
    '&': '&amp;',
    '<': '&lt;',
    '>': '&gt;',
    '"': '&quot;',
    "'": '&#39;',
  }[char]));
}

async function api(path, options = {}) {
  const response = await fetch(`${basePath}/${path.replace(/^\/+/, '')}`, {
    headers: { 'Content-Type': 'application/json' },
    ...options,
  });

  if (!response.ok) {
    const body = await response.json().catch(() => ({}));
    throw new Error(body.error || `请求失败（${response.status}）`);
  }

  return response.status === 204 ? null : response.json();
}

function toast(message) {
  const element = $('#toast');
  element.textContent = message;
  element.classList.add('show');
  clearTimeout(toast.timer);
  toast.timer = setTimeout(() => element.classList.remove('show'), 3000);
}

function formatTime(value) {
  if (!value || value.startsWith('0001-')) return '-';
  return new Date(value).toLocaleString('zh-CN', {
    month: 'numeric',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  });
}

function statusBadge(status) {
  return `<span class="pill pill-${esc(status)}">${esc(statusText[status] || status)}</span>`;
}

function tableRows(element, rows, columns, emptyText = '暂无数据') {
  element.innerHTML = rows.length
    ? rows.map((row) => `<tr>${columns.flatMap((column) => column(row)).join('')}</tr>`).join('')
    : `<tr><td class="empty" colspan="${columns.length}">${esc(emptyText)}</td></tr>`;
}

async function load() {
  try {
    const [messages, credentials, destinations] = await Promise.all([
      api('/v1/messages'),
      api('/v1/smtp/credentials'),
      api('/v1/destinations'),
    ]);

    state.data = {
      messages: messages || [],
      credentials: credentials || [],
      destinations: destinations || [],
    };
    render();
  } catch (error) {
    toast(error.message);
  }
}

function render() {
  const { messages, credentials, destinations } = state.data;
  const active = (status) => messages.filter((message) => message.status === status).length;

  $('#stat-pending').textContent = active('pending') + active('processing');
  $('#stat-delivered').textContent = active('delivered');
  $('#stat-retrying').textContent = active('retrying');
  $('#stat-dead').textContent = active('dead_letter');

  tableRows(
    $('#overview-messages'),
    messages.slice(0, 6),
    [
      (message) => `<td class="subject">${esc(message.subject || '（无主题）')}</td>`,
      (message) => `<td>${esc(message.to)}</td>`,
      (message) => statusBadge(message.status),
      (message) => `<td>${message.attempts}</td>`,
      (message) => `<td class="muted">${formatTime(message.created_at)}</td>`,
    ],
    '还没有收到邮件',
  );

  tableRows(
    $('#messages-list'),
    messages,
    [(message) => [
      `<td class="subject">${esc(message.subject || '（无主题）')}</td>`,
      `<td class="muted">${esc(message.from || '-')}</td>`,
      `<td>${esc(message.to)}</td>`,
      `<td>${statusBadge(message.status)}</td>`,
      `<td>${message.attempts}</td>`,
      `<td class="muted">${formatTime(message.created_at)}</td>`,
      `<td>${['dead_letter', 'ignored'].includes(message.status)
        ? `<button class="action-link" data-retry="${message.id}">重新投递</button>`
        : ''}</td>`,
    ]],
    '还没有收到邮件',
  );

  tableRows(
    $('#credentials-list'),
    credentials,
    [(credential) => {
      const destination = destinations.find((item) => item.id === credential.destination_id);
      return [
        `<td class="subject">${esc(credential.name)}</td>`,
        '<td class="mono">mail2bark</td>',
        `<td>${credential.allowed_ips.map(esc).join('<br>')}</td>`,
        `<td>${credential.recipients.map(esc).join('<br>')}</td>`,
        `<td>${esc(destination?.name || '所有启用设备')}</td>`,
        `<td>${credential.enabled ? '<span class="pill pill-delivered">使用中</span>' : '<span class="muted">已停用</span>'}</td>`,
      ];
    }],
    '还没有创建 API Key',
  );

  tableRows(
    $('#destinations-list'),
    destinations,
    [(destination) => [
      `<td class="subject">${esc(destination.name)}</td>`,
      `<td class="muted">${esc(destination.server)}</td>`,
      `<td>${esc(destination.group || '-')}</td>`,
      `<td>${esc(destination.level || '-')}</td>`,
      `<td>${destination.enabled ? '<span class="pill pill-delivered">使用中</span>' : '<span class="muted">已停用</span>'}</td>`,
    ]],
    '还没有添加 Bark 设备',
  );
}

function switchView(view) {
  if (!views[view]) return;
  state.view = view;

  $$('.nav-item').forEach((item) => item.classList.toggle('active', item.dataset.view === view));
  $$('.view').forEach((section) => section.classList.toggle('active-view', section.id === `view-${view}`));
  $('#page-title').textContent = views[view];
}

const formConfig = {
  credential: {
    title: '创建接入 API Key',
    submit: '创建 Key',
    endpoint: '/v1/smtp/credentials',
    fields: [
      { name: 'name', label: '来源名称', placeholder: 'idrac-r740', help: '用于识别设备或监控系统', required: true },
      { name: 'allowed_ips', label: '允许的来源 IP/CIDR', placeholder: '192.168.10.30/32', help: '仅这些地址可以使用此 Key，多个值用逗号分隔', required: true },
      { name: 'destination_id', label: 'Bark 设备', help: '邮件会发送到选中的设备', type: 'select' },
    ],
  },
  destination: {
    title: '添加 Bark 设备',
    submit: '添加设备',
    endpoint: '/v1/destinations',
    fields: [
      { name: 'name', label: '设备名称', placeholder: '办公室 iPhone', help: '用于识别这台 iPhone', required: true },
      { name: 'server', label: 'Bark 服务器', placeholder: 'https://api.day.app', help: '自建服务请填写自己的地址', required: true },
      { name: 'device_key', label: 'Device Key', placeholder: 'Bark App 中复制', help: '只保存在服务端，不会写入日志', required: true, secret: true },
      { name: 'group', label: '通知分组', placeholder: 'infrastructure', help: '可选，用于在通知中心归类' },
      { name: 'sound', label: '通知声音', placeholder: 'alarm', help: '可选，使用 Bark 支持的声音' },
      { name: 'level', label: '通知级别', placeholder: 'active', help: '可选，例如 active、timeSensitive、critical' },
    ],
  },
};

function fieldHtml(field) {
  const id = `field-${field.name}`;
  const common = `id="${id}" name="${field.name}"`;
  const required = field.required ? ' required' : '';

  if (field.type === 'select') {
    const options = field.name === 'destination_id'
      ? state.data.destinations.length
        ? '<option value="0">所有启用设备（默认）</option>' + state.data.destinations
          .map((destination) => `<option value="${destination.id}">${esc(destination.name)} · ${esc(destination.server)}</option>`)
          .join('')
        : '<option value="">请先添加 Bark 设备</option>'
      : '';
    return `<div class="field"><label for="${id}">${esc(field.label)}</label><select ${common} required>${options}</select><small>${esc(field.help)}</small></div>`;
  }

  const secret = field.secret ? ' autocomplete="off"' : '';
  return `<div class="field"><label for="${id}">${esc(field.label)}</label><input ${common} placeholder="${esc(field.placeholder || '')}"${required}${secret}><small>${esc(field.help || '')}</small></div>`;
}

function openModal(kind) {
  const config = formConfig[kind];
  if (!config) return;

  const form = $('#modal-form');
  form.dataset.kind = kind;
  form.reset();
  $('#modal-title').textContent = config.title;
  $('#modal-submit').textContent = config.submit;
  $('#modal-fields').innerHTML = `<div class="modal-body">${config.fields.map(fieldHtml).join('')}</div>`;
  $('#modal').showModal();
}

function closeModal() {
  $('#modal').close();
}

function parseList(value) {
  return value.split(',').map((item) => item.trim()).filter(Boolean);
}

async function submitModal(event) {
  event.preventDefault();
  const form = event.currentTarget;
  const config = formConfig[form.dataset.kind];
  if (!config) return;

  const data = Object.fromEntries(new FormData(form));
  if (data.allowed_ips) data.allowed_ips = parseList(data.allowed_ips);
  if (data.destination_id) data.destination_id = Number(data.destination_id);

  try {
    const result = await api(config.endpoint, {
      method: 'POST',
      body: JSON.stringify(data),
    });

    closeModal();
    if (form.dataset.kind === 'credential') {
      showCredentialResult(result);
    } else {
      toast('Bark 设备已添加');
    }
    await load();
  } catch (error) {
    toast(error.message);
  }
}

function showCredentialResult(result) {
  const credential = result.credential;
  const dialog = document.createElement('dialog');
  dialog.innerHTML = `
    <div class="modal-head">
      <div>
        <p class="eyebrow">一次性配置信息</p>
        <h2>请立即保存 API Key</h2>
      </div>
      <button type="button" class="icon-btn" aria-label="关闭">×</button>
    </div>
    <div class="modal-body">
      <p class="muted">关闭窗口后将无法再次查看 Key。</p>
      <dl class="credential-result">
        <div><dt>SMTP 服务器</dt><dd>${esc(location.hostname)}（端口见部署配置）</dd></div>
        <div><dt>SMTP 用户名</dt><dd><code>${esc(credential.username)}</code></dd></div>
        <div><dt>SMTP 密码 / API Key</dt><dd><code>${esc(result.password)}</code></dd></div>
        <div><dt>收件地址</dt><dd><code>${esc(credential.recipients.join(', '))}</code></dd></div>
      </dl>
    </div>
    <div class="modal-actions">
      <button type="button" class="primary-btn copy-config">复制配置</button>
      <button type="button" class="secondary-btn close-result">已保存</button>
    </div>
  `;

  dialog.querySelector('.icon-btn').onclick = () => dialog.close();
  dialog.querySelector('.close-result').onclick = () => dialog.close();
  dialog.querySelector('.copy-config').onclick = async () => {
    const text = [
      `SMTP 服务器：${location.hostname}`,
      `SMTP 用户名：${credential.username}`,
      `SMTP 密码：${result.password}`,
      `收件地址：${credential.recipients.join(', ')}`,
    ].join('\n');
    try {
      await navigator.clipboard.writeText(text);
      toast('配置已复制到剪贴板');
    } catch {
      toast('浏览器未允许复制，请手动选择保存');
    }
  };

  dialog.addEventListener('close', () => dialog.remove());
  document.body.appendChild(dialog);
  dialog.showModal();
}

$('#nav').addEventListener('click', (event) => {
  const button = event.target.closest('[data-view]');
  if (button) switchView(button.dataset.view);
});

document.body.addEventListener('click', async (event) => {
  const viewButton = event.target.closest('[data-view]');
  if (viewButton && viewButton.dataset.modal === undefined) switchView(viewButton.dataset.view);

  const modalButton = event.target.closest('[data-modal]');
  if (modalButton) openModal(modalButton.dataset.modal);

  const retryButton = event.target.closest('[data-retry]');
  if (retryButton) {
    try {
      await api(`/v1/messages/${retryButton.dataset.retry}/retry`, { method: 'POST' });
      toast('已重新加入投递队列');
      await load();
    } catch (error) {
      toast(error.message);
    }
  }
});

$('#refresh').addEventListener('click', load);
$('#modal-form').addEventListener('submit', submitModal);
$('#modal-form').addEventListener('click', (event) => {
  if (event.target.closest('[data-cancel]')) closeModal();
});

load();
