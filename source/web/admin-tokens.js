(() => {
  const sessionKey = 'adminSession';
  const scope = document.querySelector('#plugin-token-scope');
  const apiHost = document.querySelector('#plugin-api-host');
  const expiresAt = document.querySelector('#plugin-token-expiry');
  const status = document.querySelector('#plugin-token-status');
  const list = document.querySelector('#plugin-token-list');
  const issuedPanel = document.querySelector('#issued-plugin-token');
  const snippet = document.querySelector('#plugin-config-snippet');
  let tokenData = null;
  let apiHostChanged = false;

  function setStatus(message, isError = false) {
    if (!status) return;
    status.textContent = message || '';
    status.classList.toggle('error', isError);
  }

  async function request(path, method = 'GET', body) {
    const options = {
      method,
      cache: 'no-store',
      headers: { 'X-Admin-Session': localStorage.getItem(sessionKey) || '' }
    };
    if (body) {
      options.headers['Content-Type'] = 'application/json';
      options.body = JSON.stringify(body);
    }
    const response = await fetch(path, options);
    const data = await response.json().catch(() => ({}));
    if (!response.ok || data.success === false) {
      const error = new Error(data.message || '插件令牌操作失败');
      error.status = response.status;
      throw error;
    }
    return data;
  }

  function defaultURL() {
    if (!tokenData) return '';
    return scope.value === 'coupon.redeem' ? tokenData.couponURL : tokenData.whitelistURL;
  }

  function defaultHost() {
    const value = defaultURL();
    if (!value) return '';
    return new URL(value).hostname.replace(/^\[|\]$/g, '');
  }

  function renderTokens(items) {
    list.textContent = '';
    if (!items.length) {
      const empty = document.createElement('p');
      empty.className = 'plugin-token-empty';
      empty.textContent = '还没有签发令牌。';
      list.append(empty);
      return;
    }
    for (const item of items) {
      const row = document.createElement('div');
      row.className = 'plugin-token-row';

      const identity = document.createElement('span');
      identity.textContent = `${item.token_id}  ${item.server_id}`;
      const permission = document.createElement('span');
      permission.textContent = item.scope === 'whitelist' ? '白名单' : '兑换码';
      const state = document.createElement('span');
      const expired = item.expires_at && item.expires_at <= Date.now();
      state.textContent = item.revoked_at ? '已撤销' : expired ? '已过期' : '有效';
      const created = document.createElement('span');
      const createdDate = new Date(item.created_at);
      const expiresDate = item.expires_at ? new Date(item.expires_at) : null;
      const createdText = Number.isNaN(createdDate.getTime()) ? '' : createdDate.toLocaleString();
      const expiresText = !expiresDate ? '长期有效' : Number.isNaN(expiresDate.getTime()) ? '' : expiresDate.toLocaleString();
      created.textContent = `签发 ${createdText}，过期 ${expiresText}`;
      row.append(identity, permission, state, created);

      if (!item.revoked_at && !expired) {
        const revoke = document.createElement('button');
        revoke.className = 'admin-button danger';
        revoke.type = 'button';
        revoke.textContent = '撤销';
        revoke.addEventListener('click', async () => {
          if (!window.confirm(`撤销令牌 ${item.token_id}？`)) return;
          revoke.disabled = true;
          try {
            await request(`/api/admin/plugin-tokens/${encodeURIComponent(item.token_id)}`, 'DELETE');
            await loadTokens();
            setStatus('令牌已撤销。');
          } catch (error) {
            revoke.disabled = false;
            setStatus(error.message, true);
          }
        });
        row.append(revoke);
      }
      list.append(row);
    }
  }

  async function loadTokens() {
    try {
      tokenData = await request('/api/admin/plugin-tokens');
      document.querySelector('#plugin-server-id').textContent = tokenData.serverID;
      if (!apiHostChanged) apiHost.value = defaultHost();
      renderTokens(tokenData.tokens || []);
      setStatus('');
    } catch (error) {
      setStatus(error.message, true);
      if (error.status === 401) {
        localStorage.removeItem(sessionKey);
        window.logout?.();
      }
    }
  }

  window.loadPluginTokens = loadTokens;
  document.querySelector('#refresh-plugin-tokens')?.addEventListener('click', loadTokens);
  apiHost?.addEventListener('input', () => { apiHostChanged = true; });
  scope?.addEventListener('change', () => {
    if (!apiHostChanged) apiHost.value = defaultHost();
  });

  document.querySelector('#issue-plugin-token')?.addEventListener('click', async event => {
    const button = event.currentTarget;
    button.disabled = true;
    issuedPanel.hidden = true;
    snippet.textContent = '';
    setStatus('');
    try {
      const payload = { scope: scope.value, apiHost: apiHost.value.trim() };
      if (expiresAt.value) payload.expiresAt = new Date(expiresAt.value).toISOString();
      const result = await request('/api/admin/plugin-tokens', 'POST', payload);
      const issued = result.token;
      const apiKey = issued.scope === 'whitelist' ? 'whitelist-token' : 'coupon-token';
      snippet.textContent = `api-url: ${JSON.stringify(result.apiURL)}\n${apiKey}: ${JSON.stringify(issued.secret)}`;
      issuedPanel.hidden = false;
      await loadTokens();
      setStatus('令牌已签发。明文只显示这一次，请立即保存配置。');
    } catch (error) {
      setStatus(error.message, true);
      if (error.status === 401) {
        localStorage.removeItem(sessionKey);
        window.logout?.();
      }
    } finally {
      button.disabled = false;
    }
  });

  document.querySelector('#copy-plugin-config')?.addEventListener('click', async () => {
    try {
      await navigator.clipboard.writeText(snippet.textContent);
      setStatus('插件配置已复制。');
    } catch (_) {
      setStatus('浏览器未允许复制，请手动复制配置。', true);
    }
  });
})();
