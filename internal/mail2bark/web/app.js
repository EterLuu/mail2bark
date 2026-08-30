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
  credentials: '接入列表',
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

function copyButton(value, label = '复制') {
  return `<button class="copy-btn" type="button" data-copy="${esc(value)}" title="复制">${label}</button>`;
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
      (message) => `<td>${esc(message.to)} ${copyButton(message.to)}</td>`,
      (message) => `<td>${statusBadge(message.status)}</td>`,
      (message) => `<td>${message.attempts}</td>`,
      (message) => `<td class="muted">${formatTime(message.created_at)}</td>`,
    ],
    '还没有收到邮件',
  );

  tableRows(
    $('#messages-list'),
    messages,
    [(message) => [
      `<td class="subject"><button class="action-link subject-link" data-message-detail="${message.id}">${esc(message.subject || '（无主题）')}</button></td>`,
      `<td class="muted">${esc(message.from || '-')}</td>`,
      `<td>${esc(message.to)} ${copyButton(message.to)}</td>`,
      `<td>${statusBadge(message.status)}</td>`,
      `<td>${message.attempts}</td>`,
      `<td class="muted">${formatTime(message.created_at)}</td>`,
      `<td><div class="row-actions"><button class="action-link" data-message-detail="${message.id}">详情</button>${['dead_letter', 'ignored'].includes(message.status)
        ? `<button class="action-link" data-retry="${message.id}">重新投递</button>`
        : ''}</div></td>`,
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
        `<td class="mono">mail2bark ${copyButton('mail2bark')}</td>`,
        `<td>${credential.allowed_ips.map(esc).join('<br>')}</td>`,
        `<td>${credential.recipients.map((recipient) => `${esc(recipient)} ${copyButton(recipient)}`).join('<br>')}</td>`,
        `<td>${esc(destination?.name || '所有启用设备')}</td>`,
        `<td>${credential.enabled ? '<span class="pill pill-delivered">使用中</span>' : '<span class="pill pill-ignored">已停用</span>'}</td>`,
      `<td><div class="row-actions">
          <button class="action-link" data-smtp-test="${credential.id}"${credential.enabled ? '' : ' disabled'}>测试</button>
          <button class="action-link" data-view-credential="${credential.id}">查看 Key</button>
          <button class="action-link" data-copy-credential="${credential.id}">复制 Key</button>
          <button class="action-link" data-edit-credential="${credential.id}">编辑</button>
          <button class="action-link" data-rotate-credential="${credential.id}">轮换</button>
          <button class="action-link danger" data-delete-credential="${credential.id}">删除</button>
        </div></td>`,
      ];
    }],
    '还没有创建 API Key',
  );

  tableRows(
    $('#destinations-list'),
    destinations,
    [(destination) => [
      `<td class="subject">${esc(destination.name)}</td>`,
      `<td class="muted">${esc(destination.server)} ${copyButton(destination.server)}</td>`,
      `<td>${esc(destination.group || '-')}</td>`,
      `<td>${esc(destination.level || '-')}</td>`,
      `<td>${destination.enabled ? '<span class="pill pill-delivered">使用中</span>' : '<span class="pill pill-ignored">已停用</span>'}</td>`,
      `<td><div class="row-actions">
        <button class="action-link" data-edit-destination="${destination.id}">编辑</button>
        <button class="action-link danger" data-delete-destination="${destination.id}">删除</button>
      </div></td>`,
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
    editTitle: '编辑接入 API Key',
    submit: '创建 Key',
    editSubmit: '保存修改',
    resource: '/v1/smtp/credentials',
    fields: [
      { name: 'name', label: '来源名称', placeholder: 'idrac-r740', help: '用于识别设备或监控系统', required: true },
      { name: 'allowed_ips', label: '允许的来源 IP/CIDR', placeholder: '留空则允许所有 IPv4 地址', help: '多个值用逗号分隔；留空自动使用 0.0.0.0/0', required: false },
      { name: 'destination_id', label: 'Bark 设备', help: '邮件会发送到选中的设备', type: 'select' },
      { name: 'enabled', label: '启用此 Key', help: '停用后 SMTP 鉴权会立即失败', type: 'checkbox', editOnly: true },
    ],
  },
  destination: {
    title: '添加 Bark 设备',
    editTitle: '编辑 Bark 设备',
    submit: '添加设备',
    editSubmit: '保存修改',
    resource: '/v1/destinations',
    fields: [
      { name: 'name', label: '设备名称', placeholder: '办公室 iPhone', help: '用于识别这台 iPhone', required: true },
      { name: 'server', label: 'Bark 服务器', placeholder: 'https://api.day.app', help: '自建服务请填写自己的地址', required: true },
      { name: 'device_key', label: 'Device Key', placeholder: 'Bark App 中复制', help: '编辑时留空表示保持当前 Key', required: true, optionalOnEdit: true, secret: true },
      { name: 'group', label: '通知分组', help: '可选，用于在通知中心归类', type: 'select-custom', options: [['', '不设置'], ['infrastructure', '基础设施'], ['monitoring', '监控'], ['alert', '告警'], ['security', '安全'], ['system', '系统']] },
      { name: 'sound', label: '通知声音', help: '可选，选择常用声音或输入 Bark 支持的声音', type: 'select-custom', options: [['', '不设置'], ['alarm', 'Alarm'], ['anticipate', 'Anticipate'], ['birdsong', 'Birdsong'], ['blossom', 'Blossom'], ['calypso', 'Calypso'], ['chime', 'Chime'], ['electronic', 'Electronic'], ['fanfare', 'Fanfare'], ['glass', 'Glass'], ['gotit', 'Got It'], ['healthnotification', 'Health Notification'], ['horn', 'Horn'], ['illuminate', 'Illuminate'], ['mailsent', 'Mail Sent'], ['minuet', 'Minuet'], ['multiwayinvitation', 'Multiway Invitation'], ['newmail', 'New Mail'], ['newsflash', 'News Flash'], ['noir', 'Noir'], ['paymentsuccess', 'Payment Success'], ['shake', 'Shake'], ['sirius', 'Sirius'], ['spell', 'Spell'], ['suspense', 'Suspense'], ['telegraph', 'Telegraph'], ['tiptoes', 'Tiptoes'], ['typewriters', 'Typewriters'], ['update', 'Update']] },
      { name: 'level', label: '通知级别', help: '可选，选择 Bark 支持的级别或自定义', type: 'select-custom', options: [['', '不设置'], ['active', 'Active'], ['timeSensitive', 'Time Sensitive'], ['passive', 'Passive'], ['critical', 'Critical']] },
      { name: 'enabled', label: '启用此设备', help: '停用后不会向该设备发送通知', type: 'checkbox', editOnly: true },
    ],
  },
  'smtp-test': {
    title: '发送 SMTP 测试',
    submit: '发送测试',
    resource: '/v1/smtp/credentials',
    fields: [
      { name: 'password', label: 'SMTP API Key', placeholder: '输入创建或轮换时保存的 Key', help: '仅用于本次校验，不会保存', required: true, secret: true },
      { name: 'from', label: '发件地址（可选）', placeholder: 'monitor@example.com', help: '留空使用 mail2bark-test@localhost' },
      { name: 'subject', label: '邮件主题', value: 'mail2bark SMTP 测试', required: true },
      { name: 'body', label: '邮件正文', value: '这是一封由 mail2bark 管理界面生成的 SMTP 测试邮件。', type: 'textarea', required: true },
    ],
  },
};

function fieldHtml(field, value, editing) {
  const id = `field-${field.name}`;
  const common = `id="${id}" name="${field.name}"`;
  const required = field.required && !(editing && field.optionalOnEdit) ? ' required' : '';

  if (field.type === 'select') {
    const options = '<option value="0">所有启用设备（默认）</option>' + state.data.destinations
      .map((destination) => `<option value="${destination.id}"${Number(value) === destination.id ? ' selected' : ''}>${esc(destination.name)}${destination.enabled ? '' : '（已停用）'} · ${esc(destination.server)}</option>`)
      .join('');
    return `<div class="field"><label for="${id}">${esc(field.label)}</label><select ${common}>${options}</select><small>${esc(field.help)}</small></div>`;
  }

  if (field.type === 'select-custom') {
    const customValue = value && !field.options.some(([option]) => option === value) ? value : '';
    const selectedValue = customValue ? '__custom__' : (value ?? '');
    const options = field.options.map(([option, label]) => `<option value="${esc(option)}"${selectedValue === option ? ' selected' : ''}>${esc(label)}</option>`).join('');
    return `<div class="field"><label for="${id}">${esc(field.label)}</label><select ${common} data-custom-select="${id}-custom">${options}<option value="__custom__"${selectedValue === '__custom__' ? ' selected' : ''}>自定义...</option></select><input class="custom-select-input" id="${id}-custom" name="${field.name}_custom" value="${esc(customValue)}" placeholder="输入自定义值"${customValue ? '' : ' hidden'}><small>${esc(field.help || '')}</small></div>`;
  }

  if (field.type === 'checkbox') {
    return `<label class="check-field" for="${id}"><input type="checkbox" ${common}${value ? ' checked' : ''}><span><strong>${esc(field.label)}</strong><small>${esc(field.help)}</small></span></label>`;
  }

  if (field.type === 'textarea') {
    return `<div class="field"><label for="${id}">${esc(field.label)}</label><textarea ${common}${required}>${esc(value ?? field.value ?? '')}</textarea><small>${esc(field.help || '')}</small></div>`;
  }

  const secret = field.secret ? ' type="password" autocomplete="off"' : '';
  return `<div class="field"><label for="${id}">${esc(field.label)}</label><input ${common}${secret} value="${esc(value ?? field.value ?? '')}" placeholder="${esc(field.placeholder || '')}"${required}><small>${esc(field.help || '')}</small></div>`;
}

function openModal(kind, record = null) {
  const config = formConfig[kind];
  if (!config) return;

  const form = $('#modal-form');
  form.dataset.kind = kind;
  form.dataset.id = record?.id || '';
  form.reset();
  const editing = Boolean(record && kind !== 'smtp-test');
  $('#modal-title').textContent = editing ? config.editTitle : config.title;
  $('#modal-submit').textContent = editing ? config.editSubmit : config.submit;
  const fields = config.fields.filter((field) => editing || !field.editOnly);
  $('#modal-fields').innerHTML = `<div class="modal-body">${fields.map((field) => {
    let value = record?.[field.name] ?? field.value ?? '';
    if (field.name === 'allowed_ips' && Array.isArray(value)) value = value.join(', ');
    if (kind === 'smtp-test') value = field.value ?? '';
    return fieldHtml(field, value, editing);
  }).join('')}</div>`;
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
  config.fields.filter((field) => field.type === 'select-custom').forEach((field) => {
    if (data[field.name] === '__custom__') data[field.name] = data[`${field.name}_custom`] || '';
    delete data[`${field.name}_custom`];
  });
  if ('destination_id' in data) data.destination_id = Number(data.destination_id);
  if (form.elements.enabled) data.enabled = form.elements.enabled.checked;

  const id = Number(form.dataset.id);
  const editing = Boolean(id && form.dataset.kind !== 'smtp-test');
  let endpoint = config.resource;
  let method = 'POST';
  if (form.dataset.kind === 'smtp-test') {
    endpoint = `${config.resource}/${id}/test`;
  } else if (editing) {
    endpoint = `${config.resource}/${id}`;
    method = 'PUT';
  }

  try {
    const result = await api(endpoint, {
      method,
      body: JSON.stringify(data),
    });

    closeModal();
    if (form.dataset.kind === 'credential' && !editing) {
      showCredentialResult(result);
    } else if (form.dataset.kind === 'smtp-test') {
      toast('测试邮件已进入投递队列');
      await load();
      switchView('messages');
      return;
    } else {
      toast(editing ? '修改已保存' : 'Bark 设备已添加');
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
        <p class="eyebrow">接入配置信息</p>
        <h2>SMTP 接入 Key</h2>
      </div>
      <button type="button" class="icon-btn" aria-label="关闭">×</button>
    </div>
    <div class="modal-body">
      <p class="muted">Key 可通过此窗口再次查看；轮换后旧 Key 会立即失效。</p>
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

function showMessageDetail(detail) {
  const alert = detail.alert || {};
  const dialog = document.createElement('dialog');
  dialog.className = 'wide-dialog';
  dialog.innerHTML = `
    <div class="modal-head"><div><p class="eyebrow">邮件详情</p><h2>${esc(detail.subject || '（无主题）')}</h2></div><button type="button" class="icon-btn" aria-label="关闭">×</button></div>
    <div class="modal-body detail-body">
      <dl class="message-detail">
        <div><dt>发件地址</dt><dd>${esc(detail.from || '-')} ${copyButton(detail.from || '')}</dd></div>
        <div><dt>收件地址</dt><dd>${esc(detail.to || '-')} ${copyButton(detail.to || '')}</dd></div>
        <div><dt>解析级别</dt><dd>${esc(alert.severity || '-')}</dd></div>
        <div><dt>设备</dt><dd>${esc(alert.device || '-')}</dd></div>
        <div><dt>组件</dt><dd>${esc(alert.component || '-')}</dd></div>
        <div><dt>事件</dt><dd>${esc(alert.event || '-')}</dd></div>
        <div><dt>通知正文</dt><dd><pre>${esc(alert.body || '')}</pre></dd></div>
        <div><dt>原始邮件</dt><dd><pre>${esc(detail.raw || '')}</pre></dd></div>
      </dl>
    </div>
    <div class="modal-actions"><button type="button" class="secondary-btn close-detail">关闭</button></div>`;
  dialog.querySelector('.icon-btn').onclick = () => dialog.close();
  dialog.querySelector('.close-detail').onclick = () => dialog.close();
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

  const editCredentialButton = event.target.closest('[data-edit-credential]');
  if (editCredentialButton) {
    const credential = state.data.credentials.find((item) => item.id === Number(editCredentialButton.dataset.editCredential));
    if (credential) openModal('credential', credential);
  }

  const viewCredentialButton = event.target.closest('[data-view-credential]');
  if (viewCredentialButton) {
    try {
      const credential = await api(`/v1/smtp/credentials/${viewCredentialButton.dataset.viewCredential}`);
      showCredentialResult({ credential });
    } catch (error) {
      toast(error.message);
    }
  }

  const detailButton = event.target.closest('[data-message-detail]');
  if (detailButton) {
    try {
      const detail = await api(`/v1/messages/${detailButton.dataset.messageDetail}`);
      showMessageDetail(detail);
    } catch (error) {
      toast(error.message);
    }
  }

  const copyButtonElement = event.target.closest('[data-copy]');
  if (copyButtonElement) {
    try {
      await navigator.clipboard.writeText(copyButtonElement.dataset.copy);
      toast('已复制');
    } catch {
      toast('浏览器未允许复制');
    }
  }

  const copyCredentialButton = event.target.closest('[data-copy-credential]');
  if (copyCredentialButton) {
    try {
      const credential = await api(`/v1/smtp/credentials/${copyCredentialButton.dataset.copyCredential}`);
      if (!credential.password) throw new Error('该 Key 尚未生成可查看值，请先轮换 Key');
      await navigator.clipboard.writeText(credential.password);
      toast('API Key 已复制');
    } catch (error) {
      toast(error.message);
    }
  }

  const smtpTestButton = event.target.closest('[data-smtp-test]');
  if (smtpTestButton) {
    const credential = state.data.credentials.find((item) => item.id === Number(smtpTestButton.dataset.smtpTest));
    if (credential) openModal('smtp-test', credential);
  }

  const rotateCredentialButton = event.target.closest('[data-rotate-credential]');
  if (rotateCredentialButton) {
    const id = Number(rotateCredentialButton.dataset.rotateCredential);
    if (window.confirm('轮换后旧 Key 会立即失效，确定继续吗？')) {
      try {
        const result = await api(`/v1/smtp/credentials/${id}/rotate`, { method: 'POST' });
        showCredentialResult(result);
      } catch (error) {
        toast(error.message);
      }
    }
  }

  const deleteCredentialButton = event.target.closest('[data-delete-credential]');
  if (deleteCredentialButton) {
    const id = Number(deleteCredentialButton.dataset.deleteCredential);
    if (window.confirm('删除后该 Key 和收件地址会立即失效，历史邮件会保留。确定删除吗？')) {
      try {
        await api(`/v1/smtp/credentials/${id}`, { method: 'DELETE' });
        toast('接入 Key 已删除');
        await load();
      } catch (error) {
        toast(error.message);
      }
    }
  }

  const editDestinationButton = event.target.closest('[data-edit-destination]');
  if (editDestinationButton) {
    const destination = state.data.destinations.find((item) => item.id === Number(editDestinationButton.dataset.editDestination));
    if (destination) openModal('destination', destination);
  }

  const deleteDestinationButton = event.target.closest('[data-delete-destination]');
  if (deleteDestinationButton) {
    const id = Number(deleteDestinationButton.dataset.deleteDestination);
    if (window.confirm('确定删除这个 Bark 设备吗？')) {
      try {
        await api(`/v1/destinations/${id}`, { method: 'DELETE' });
        toast('Bark 设备已删除');
        await load();
      } catch (error) {
        toast(error.message);
      }
    }
  }

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

document.body.addEventListener('change', (event) => {
  const select = event.target.closest('[data-custom-select]');
  if (!select) return;
  const input = document.getElementById(select.dataset.customSelect);
  if (input) input.hidden = select.value !== '__custom__';
});

$('#refresh').addEventListener('click', load);
$('#modal-form').addEventListener('submit', submitModal);
$('#modal-form').addEventListener('click', (event) => {
  if (event.target.closest('[data-cancel]')) closeModal();
});

load();
