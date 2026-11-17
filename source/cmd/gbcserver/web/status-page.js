(() => {
  'use strict';

  const board = document.querySelector('#statusBoard');
  const globalStatus = document.querySelector('#globalStatus');
  const updatedAt = document.querySelector('#updatedAt');
  let nextSampleAt = 0;
  let isRefreshing = false;

  function element(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = String(text);
    return node;
  }

  function formatTime(value) {
    const date = new Date(Number(value));
    return Number.isNaN(date.getTime()) ? '--' : date.toLocaleString('zh-CN', { hour12: false });
  }

  function formatAddress(host, port) {
    const address = String(host || '');
    const wrapped = address.includes(':') && !address.startsWith('[') ? `[${address}]` : address;
    return `${wrapped}:${port || ''}`;
  }

  function renderHistory(samples = []) {
    const wrapper = element('div', 'status-history-wrap');
    const timeline = element('div', 'status-history');
    const recent = Array.isArray(samples) ? samples.slice(-60) : [];
    const onlineCount = recent.filter(sample => sample.online).length;
    timeline.setAttribute('role', 'img');
    timeline.setAttribute('aria-label', `最近 ${recent.length} 次检测中 ${onlineCount} 次在线`);

    const missing = Math.max(0, 60 - recent.length);
    for (let index = 0; index < 60; index += 1) {
      const sample = index < missing ? null : recent[index - missing];
      const bar = element('span', `status-history-bar ${sample ? (sample.online ? 'is-online' : 'is-offline') : 'is-unknown'}`);
      bar.setAttribute('aria-hidden', 'true');
      if (sample) bar.title = `${sample.online ? '在线' : '离线'} ${formatTime(sample.checkedAt)}`;
      else bar.title = '尚无记录';
      timeline.appendChild(bar);
    }
    const scale = element('div', 'status-history-scale');
    scale.append(element('span', '', '过去'), element('span', '', '现在'));
    wrapper.append(timeline, scale);
    return wrapper;
  }

  function renderDiagnostics(server) {
    const details = element('details', 'status-details');
    details.appendChild(element('summary', '', '检测详情'));
    const rows = element('dl', 'status-diagnostics');
    const players = server.players || {};
    const latency = Number(server.latencyMs);
    const values = [
      ['检测来源', server.source || '状态探测'],
      ['响应时间', Number.isFinite(latency) ? `${Math.round(latency)} ms` : '--'],
      ['版本', server.version || '未知'],
      ['玩家', `${Number(players.online) || 0} / ${Number(players.max) || 0}`],
      ['描述', server.motd || '--']
    ];
    values.forEach(([label, value]) => {
      rows.append(element('dt', '', label), element('dd', '', value));
    });
    details.appendChild(rows);
    return details;
  }

  function renderServer(server) {
    const online = Boolean(server.online);
    const item = element('article', `server-item ${online ? 'is-online' : 'is-offline'}`);
    const heading = element('div', 'server-heading');
    const identity = element('div', 'server-identity');
    identity.append(
      element('h2', 'server-name', server.name || '未命名线路'),
      element('p', 'server-address', formatAddress(server.host, server.port))
    );
    heading.append(identity, element('span', `status-indicator ${online ? 'online' : 'offline'}`, online ? '在线' : '离线'));

    const availability = element('div', 'availability');
    const count = Number(server.sampleCount7d) || 0;
    const percent = Number(server.availabilityPercent);
    availability.append(
      element('span', 'availability-label', count ? '近 7 天可用率' : '等待历史采样'),
      element('strong', 'availability-value', count ? `${percent.toFixed(2)}%` : '--')
    );

    const history = renderHistory(server.samples);
    const historyCaption = element('div', 'history-caption');
    historyCaption.append(
      element('span', '', '近 60 次记录'),
      element('time', 'status-next-sample', '--')
    );

    item.append(heading, availability, historyCaption, history, renderDiagnostics(server));
    return item;
  }

  function setMessage(message, isError = false) {
    board.replaceChildren(element('p', `status-message${isError ? ' is-error' : ''}`, message));
  }

  async function updateStatus() {
    if (isRefreshing) return;
    isRefreshing = true;
    const refreshButton = document.querySelector('#refreshStatus');
    refreshButton?.classList.add('is-refreshing');
    try {
      const response = await fetch(`/api/serverStatus?t=${Date.now()}`, { cache: 'no-store' });
      const data = await response.json();
      if (!response.ok || !data.success || !Array.isArray(data.servers)) {
        throw new Error(data.message || '状态接口暂不可用');
      }
      board.replaceChildren();
      if (data.servers.length === 0) {
        setMessage('尚未配置状态线路');
        globalStatus.className = 'global-status is-neutral';
        globalStatus.lastElementChild.textContent = '等待线路配置';
      } else {
        const offlineCount = data.servers.filter(server => !server.online).length;
        globalStatus.className = `global-status ${offlineCount ? 'is-warning' : 'is-healthy'}`;
        globalStatus.lastElementChild.textContent = offlineCount
          ? `${offlineCount} 条线路离线`
          : '所有线路正常';
        data.servers.forEach(server => board.appendChild(renderServer(server)));
      }
      const lastChecked = Math.max(0, ...data.servers.map(server => Number(server.checkedAt) || 0));
      updatedAt.textContent = lastChecked ? formatTime(lastChecked) : '--';
      nextSampleAt = Number(data.nextSampleAt) || 0;
      updateCountdown();
    } catch (error) {
      setMessage(error.message || '读取线路状态失败', true);
      globalStatus.className = 'global-status is-warning';
      globalStatus.lastElementChild.textContent = '状态读取失败';
    } finally {
      isRefreshing = false;
      refreshButton?.classList.remove('is-refreshing');
    }
  }

  function updateCountdown() {
    if (!nextSampleAt) {
      document.querySelectorAll('.status-next-sample').forEach(node => { node.textContent = '--'; });
      return;
    }
    const remaining = Math.max(0, Math.ceil((nextSampleAt - Date.now()) / 1000));
    const minutes = Math.floor(remaining / 60);
    const seconds = String(remaining % 60).padStart(2, '0');
    const label = remaining === 0
      ? '即将采样'
      : minutes > 0 ? `${minutes} 分 ${seconds} 秒后刷新` : `${remaining} 秒后刷新`;
    document.querySelectorAll('.status-next-sample').forEach(node => { node.textContent = label; });
  }

  document.querySelector('#refreshStatus')?.addEventListener('click', updateStatus);
  window.setInterval(updateCountdown, 1000);
  window.setInterval(updateStatus, 15000);
  updateStatus();
})();
