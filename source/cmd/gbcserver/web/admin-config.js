(() => {
  const sessionKey = 'adminSession';
  const clearableConfigKeys = new Set(['mailHost', 'mailUsername', 'mailFromName', 'mailFromAddress', 'statusAlertEmail', 'statusServers', 'statusAPIs']);
  const form = document.querySelector('#configForm');
  const message = document.querySelector('#message');
  const status = document.querySelector('#admin-config-status');
  const defaultStatusAPIs = [
    { name: 'api.mcsrvstat.us', url: 'https://api.mcsrvstat.us/3/{hostport}' },
    { name: 'mcapi.us', url: 'https://mcapi.us/server/status?ip={host}&port={port}' },
    { name: 'api.minetools.eu', url: 'https://api.minetools.eu/ping/{host}/{port}' }
  ];

  function setMessage(text) {
    if (message) message.textContent = text || '';
  }

  function currentSession() {
    return localStorage.getItem(sessionKey) || '';
  }

  async function responseJSON(response, fallback) {
    const data = await response.json().catch(() => ({}));
    if (!response.ok || data.success === false) throw new Error(data.message || fallback);
    return data;
  }

  async function requestConfig(method, values) {
    const options = { method, headers: { 'X-Admin-Session': currentSession() } };
    if (values) {
      options.headers['Content-Type'] = 'application/json';
      options.body = JSON.stringify(values);
    }
    return responseJSON(await fetch('/api/admin/config', options), '管理员会话已失效');
  }

  async function requestServiceControl(action) {
    const response = await fetch('/api/admin/control', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'X-Admin-Session': currentSession() },
      body: JSON.stringify({ action })
    });
    return responseJSON(response, '服务操作失败');
  }

  function parsePort(value) {
    const port = Number.parseInt(String(value ?? '').trim(), 10);
    return Number.isInteger(port) && port > 0 && port <= 65535 ? port : '';
  }

  function parseLegacyHostPort(value) {
    const text = String(value || '').trim();
    if (text.startsWith('[')) {
      const closing = text.indexOf(']:');
      if (closing > 0) return { host: text.slice(1, closing), port: parsePort(text.slice(closing + 2)) };
    }
    const match = text.match(/^(.*):([0-9]+)$/);
    if (match) return { host: match[1], port: parsePort(match[2]) };
    return { host: text, port: '' };
  }

  function parseConfigList(key, raw) {
    if (Array.isArray(raw)) return raw;
    const text = String(raw ?? '').trim();
    if (!text) return key === 'statusAPIs' ? defaultStatusAPIs.map(item => ({ ...item })) : [];
    if (text.toLowerCase() === 'none') return [];
    try {
      const parsed = JSON.parse(text);
      const values = Array.isArray(parsed)
        ? parsed
        : (key === 'statusServers' ? parsed?.servers : parsed?.apis);
      if (Array.isArray(values)) return values;
    } catch (_) {
      // 继续按旧版逗号格式解析。
    }
    return text.split(',').map(item => item.trim()).filter(Boolean).map(item => {
      const separator = item.indexOf('=');
      if (separator < 1) return null;
      const name = item.slice(0, separator).trim();
      const value = item.slice(separator + 1).trim();
      if (key === 'statusServers') {
        const endpoint = parseLegacyHostPort(value);
        return { name, host: endpoint.host, port: endpoint.port };
      }
      return { name, url: value };
    }).filter(Boolean);
  }

  function normalizedListValue(key, item) {
    if (key === 'statusServers') {
      const endpoint = parseLegacyHostPort(item?.host || '');
      return {
        name: String(item?.name ?? '').trim(),
        host: endpoint.host,
        port: parsePort(item?.port ?? item?.defaultPort ?? endpoint.port)
      };
    }
    return {
      name: String(item?.name ?? '').trim(),
      url: String(item?.url ?? item?.URL ?? item?.URLTemplate ?? item?.template ?? '').trim()
    };
  }

  function createListInput(label, field, value, type = 'text') {
    const wrapper = document.createElement('label');
    wrapper.className = 'status-list-control';
    const caption = document.createElement('span');
    caption.textContent = label;
    const input = document.createElement('input');
    input.type = type;
    input.dataset.field = field;
    input.value = value ?? '';
    input.autocomplete = 'off';
    input.setAttribute('aria-label', label);
    wrapper.append(caption, input);
    return wrapper;
  }

  function renderStatusList(key, raw) {
    const container = document.querySelector(`#${key}-list`);
    const hidden = document.querySelector(`#${key}`);
    if (!container || !hidden) return;
    container.textContent = '';
    const values = parseConfigList(key, raw).map(item => normalizedListValue(key, item));
    values.forEach(item => {
      const row = document.createElement('div');
      row.className = 'status-list-row';
      row.classList.add(key === 'statusServers' ? 'status-server-row' : 'status-api-row');
      if (key === 'statusServers') {
        row.append(
          createListInput('名称', 'name', item.name),
          createListInput('主机', 'host', item.host),
          createListInput('端口', 'port', item.port, 'number')
        );
      } else {
        row.append(
          createListInput('名称', 'name', item.name),
          createListInput('URL 模板', 'url', item.url)
        );
      }
      const remove = document.createElement('button');
      remove.type = 'button';
      remove.className = 'admin-button danger status-list-remove';
      remove.innerHTML = '<i class="fas fa-trash" aria-hidden="true"></i><span>删除</span>';
      remove.title = '删除此项';
      remove.addEventListener('click', () => {
        row.remove();
        syncStatusList(key);
      });
      row.append(remove);
      container.append(row);
    });
    syncStatusList(key);
  }

  function syncStatusList(key) {
    const container = document.querySelector(`#${key}-list`);
    const hidden = document.querySelector(`#${key}`);
    if (!container || !hidden) return;
    const values = [...container.querySelectorAll('.status-list-row')].map(row => {
      const item = {};
      row.querySelectorAll('[data-field]').forEach(input => { item[input.dataset.field] = input.value.trim(); });
      if (key === 'statusServers') item.port = parsePort(item.port);
      return item;
    });
    hidden.value = JSON.stringify(values);
  }

  function addStatusListRow(key) {
    const hidden = document.querySelector(`#${key}`);
    const current = hidden ? parseConfigList(key, hidden.value) : [];
    current.push(key === 'statusServers' ? { name: '', host: '', port: '' } : { name: '', url: '' });
    renderStatusList(key, current);
    document.querySelector(`#${key}-list .status-list-row:last-child [data-field="name"]`)?.focus();
  }

  function fillConfig(values = {}) {
    form?.querySelectorAll('[data-key]').forEach(input => {
      const key = input.dataset.key;
      if (key === 'statusServers' || key === 'statusAPIs') return;
      if (input.type === 'checkbox') {
        input.checked = values[key] === true || String(values[key]).toLowerCase() === 'true';
        return;
      }
      if (['adminPassword', 'adminTOTPSecret', 'mailPassword'].includes(key)) {
        input.value = '';
        return;
      }
      if (values[key] !== undefined) input.value = String(values[key]);
    });
    renderStatusList('statusServers', values.statusServers);
    renderStatusList('statusAPIs', values.statusAPIs);
    const password = form?.querySelector('[data-key="adminPassword"]');
    const totp = form?.querySelector('[data-key="adminTOTPSecret"]');
    const mail = form?.querySelector('[data-key="mailPassword"]');
    if (password) password.placeholder = values.adminPasswordConfigured ? '留空表示不修改' : '请设置管理员密码';
    if (totp) totp.placeholder = values.adminTOTPConfigured ? '留空表示不修改' : '请填写 TOTP 密钥';
    if (mail) mail.placeholder = values.mailPasswordConfigured ? '留空表示不修改' : 'SMTP 密码';
    const webhook = form?.querySelector('[data-key="statusAlertWebhookURL"]');
    if (webhook) webhook.placeholder = values.statusAlertWebhookConfigured ? '已配置，留空表示不修改' : 'https://example.com/webhook';
    syncAlertTargets();
  }

  function syncAlertTargets() {
    form?.querySelectorAll('[data-alert-target]').forEach(target => {
      const toggle = form.querySelector(`[data-key="${target.dataset.alertTarget}"]`);
      target.hidden = !toggle?.checked;
    });
  }

  function applyReleaseProfile(enabled) {
    form?.querySelectorAll('[data-github-hide]').forEach(element => { element.hidden = enabled; });
    document.body.dataset.githubRelease = String(enabled);
    const heading = form?.closest('.admin-panel')?.querySelector('.admin-panel-heading h2');
    const kicker = form?.closest('.admin-panel')?.querySelector('.admin-panel-kicker');
    if (heading) heading.textContent = enabled ? '管理系统' : '运行配置';
    if (kicker) kicker.textContent = enabled ? 'MANAGEMENT SETTINGS' : 'DATABASE SETTINGS';
    const webGroup = [...(form?.querySelectorAll('fieldset') || [])].find(group => group.querySelector('#adminPath'));
    if (webGroup?.querySelector('legend')) webGroup.querySelector('legend').textContent = enabled ? '管理员账户' : 'Web 与管理员';
    const configNav = document.querySelector('.nav-item[data-page="admin-config"] .nav-text');
    if (configNav) configNav.textContent = enabled ? '管理系统' : '运行配置';
  }

  async function loadConfig() {
    const result = await requestConfig('GET');
    applyReleaseProfile(Boolean(result.githubRelease));
    fillConfig(result.config || {});
    if (status) status.textContent = '管理员配置已从数据库读取。';
  }

  window.loadAdminConfig = async () => {
    try {
      await loadConfig();
    } catch (error) {
      setMessage(error.message);
      if (/会话/.test(error.message)) {
        localStorage.removeItem(sessionKey);
        window.logout?.();
      }
    }
  };

  form?.addEventListener('submit', async event => {
    event.preventDefault();
    setMessage('');
    syncStatusList('statusServers');
    syncStatusList('statusAPIs');
    const values = {};
    form.querySelectorAll('[data-key]').forEach(input => {
      const key = input.dataset.key;
      if (['adminPassword', 'adminTOTPSecret', 'mailPassword'].includes(key)) return;
      if (input.type === 'checkbox') {
        values[key] = String(input.checked);
        return;
      }
      if (input.value !== '' || clearableConfigKeys.has(key)) values[key] = input.value;
    });
    const button = form.querySelector('[type="submit"]');
    button.disabled = true;
    try {
      const result = await requestConfig('PUT', values);
      fillConfig(result.config || {});
      setMessage(result.reloadRequired
        ? '配置已保存。监听和插件设置需重新加载服务后生效。'
        : '配置已保存。');
    } catch (error) {
      setMessage(error.message);
      if (/会话/.test(error.message)) {
        localStorage.removeItem(sessionKey);
        window.logout?.();
      }
    } finally {
      button.disabled = false;
    }
  });

  document.querySelector('#refreshButton')?.addEventListener('click', () => window.loadAdminConfig());
  document.querySelector('#logoutButton')?.addEventListener('click', () => window.logout?.());
  document.querySelector('#reloadServiceButton')?.addEventListener('click', async event => {
    const button = event.currentTarget;
    button.disabled = true;
    try {
      const result = await requestServiceControl('reload');
      setMessage(result.message || '重新加载请求已发送。');
    } catch (error) {
      setMessage(error.message);
    } finally {
      button.disabled = false;
    }
  });
  document.querySelector('#stopServiceButton')?.addEventListener('click', async event => {
    if (!window.confirm('停止服务后网页会暂时无法访问。确定停止吗？')) return;
    const button = event.currentTarget;
    button.disabled = true;
    try {
      const result = await requestServiceControl('stop');
      setMessage(result.message || '停止请求已发送。');
    } catch (error) {
      setMessage(error.message);
    } finally {
      button.disabled = false;
    }
  });
  document.querySelector('#installAutostartButton')?.addEventListener('click', async event => {
    const button = event.currentTarget;
    button.disabled = true;
    try {
      const result = await requestServiceControl('install-autostart');
      setMessage(result.message || '已安装当前用户开机自启。');
    } catch (error) {
      setMessage(error.message);
    } finally {
      button.disabled = false;
    }
  });
  form?.querySelectorAll('.admin-toggle input[type="checkbox"]').forEach(input => {
    input.addEventListener('change', syncAlertTargets);
  });

  fetch('/api/publicConfig', { cache: 'no-store' })
    .then(response => response.json())
    .then(result => applyReleaseProfile(Boolean(result.githubRelease)))
    .catch(() => {});
  document.querySelector('#uninstallAutostartButton')?.addEventListener('click', async event => {
    if (!window.confirm('移除当前用户的开机自启？')) return;
    const button = event.currentTarget;
    button.disabled = true;
    try {
      const result = await requestServiceControl('uninstall-autostart');
      setMessage(result.message || '已移除当前用户开机自启。');
    } catch (error) {
      setMessage(error.message);
    } finally {
      button.disabled = false;
    }
  });
  document.querySelector('#addStatusServerButton')?.addEventListener('click', () => addStatusListRow('statusServers'));
  document.querySelector('#addStatusAPIButton')?.addEventListener('click', () => addStatusListRow('statusAPIs'));

  document.querySelector('#admin-setup-form')?.addEventListener('submit', async event => {
    event.preventDefault();
    const button = event.currentTarget.querySelector('[type="submit"]');
      const notice = document.querySelector('#admin-auth-status');
    button.disabled = true;
    if (notice) notice.textContent = '';
    try {
      await responseJSON(await fetch('/api/admin/setup', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          username: document.querySelector('#setup-username').value,
          password: document.querySelector('#setup-password').value,
          totpSecret: document.querySelector('#setup-totp').value
        })
      }), '管理员初始化失败');
      document.querySelector('#admin-setup-panel').style.display = 'none';
      document.querySelector('#login-page .login-card').style.display = '';
      document.querySelector('#login-username').value = document.querySelector('#setup-username').value;
      if (notice) notice.textContent = '初始化完成，请登录。';
    } catch (error) {
      if (notice) notice.textContent = error.message;
    } finally {
      button.disabled = false;
    }
  });

  window.addEventListener('load', async () => {
    const loginCard = document.querySelector('#login-page .login-card');
    const setupPanel = document.querySelector('#admin-setup-panel');
    if (loginCard) loginCard.style.display = 'none';
    if (setupPanel) setupPanel.style.display = 'none';
    try {
      const result = await responseJSON(await fetch('/api/admin/status', { cache: 'no-store' }), '无法读取管理员状态');
      if (result.initialized) {
        if (loginCard) loginCard.style.display = '';
        if (status) status.textContent = '管理员已初始化，请登录。';
      } else if (setupPanel) {
        setupPanel.style.display = 'block';
        if (status) status.textContent = '首次运行，请初始化管理员账户。';
      }
    } catch (error) {
      if (loginCard) loginCard.style.display = '';
      if (status) status.textContent = error.message;
    }
  });
})();
