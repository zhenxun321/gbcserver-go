'use strict';

const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const { spawn } = require('node:child_process');
const speakeasy = require('speakeasy');
const {
  workspace, testPassword, adminName, adminPassword, totpSecret, writeJSON,
  createLegacy, migrate, listen, smtpSink, http
} = require('./fixture.cjs');

async function main() {
  const executable = path.resolve(process.argv[2] || path.join(workspace, '目标文件/gbcserver-windows-amd64.exe'));
  const root = fs.mkdtempSync(path.join(workspace, '目标文件/兼容性验证-'));
  console.log('隔离目录：' + root);
  const legacy = createLegacy(path.join(root, 'legacy'));
  const normalizedDB = path.join(root, 'normalized.db');
  migrate(executable, legacy.dbPath, normalizedDB);
  console.log('脱敏 fixture 导入完成，准备启动双端服务。');
  const legacyServer = http.createServer(legacy.app);
  const legacyPort = await listen(legacyServer);
  const mail = await smtpSink();
  const portReservation = http.createServer();
  const goPort = await listen(portReservation);
  await new Promise(resolve => portReservation.close(resolve));
  const dataDirectory = path.join(root, 'data');
  fs.mkdirSync(dataDirectory, { recursive: true });
  const adminPasswordHash = require('bcryptjs').hashSync(adminPassword, 10);
  fs.mkdirSync(path.join(root, 'go-avatars'));
  const environment = Object.fromEntries(Object.entries(process.env).filter(([key]) => !key.startsWith('GBC_')));
  const child = spawn(executable, [
    'web-serve', '--data-dir', dataDirectory, '--db', normalizedDB, '--listen', `127.0.0.1:${goPort}`,
    '--static-dir', path.join(root, 'legacy/public'), '--avatar-dir', path.join(root, 'go-avatars'),
    '--server-id', 'main', '--status-apis', 'none', '--admin-username', adminName,
    '--admin-password-hash', adminPasswordHash, '--admin-totp-secret', totpSecret,
    '--mail-host', '127.0.0.1', '--mail-port', String(mail.port), '--mail-secure=false', '--mail-from-address', 'fixture@example.invalid'
  ], { env: environment, windowsHide: true, stdio: ['ignore', 'pipe', 'pipe'] });
  let goLog = '';
  child.stdout.on('data', data => { goLog += data; });
  child.stderr.on('data', data => { goLog += data; });
  const backends = [
    { name: 'Node', base: `http://127.0.0.1:${legacyPort}`, session: '', admin: '', generated: new Map() },
    { name: 'Go', base: `http://127.0.0.1:${goPort}`, session: '', admin: '', generated: new Map() }
  ];
  const checks = [];
  const scrub = (value, backend, key = '') => {
    if (Array.isArray(value)) return value.map(item => scrub(item, backend));
    if (value && typeof value === 'object') {
      if (value.code === backend.coupon) {
        assert.equal(typeof value.expiresAt, 'string');
        assert.ok(Math.abs(Date.parse(value.expiresAt) - Date.parse(value.createdAt) - 72 * 3600000) < 1000, '购买兑换码有效期必须是 72 小时');
        value = { ...value, expiresAt: '<createdAt+72h:string>' };
      }
      return Object.fromEntries(Object.entries(value).sort(([a], [b]) => a.localeCompare(b)).map(([name, entry]) => [name, scrub(entry, backend, name)]));
    }
    if (typeof value === 'string') {
      if (key === 'session' && value) return '<session>';
      if (backend.generated.has(value)) return backend.generated.get(value);
      if (['lastLogin', 'createdAt', 'updatedAt', 'usedAt'].includes(key) && value && !value.startsWith('2025-01-')) return '<time:string>';
      if (['avatarUrl', 'imageUrl', 'avatarPath', 'avatar'].includes(key) && value) return value.replace(/[^/]+(?=\.(png|jpe?g|gif|webp)$)/, '<file>');
    }
    if (key === 'time' && typeof value === 'number') return '<time:number>';
    return value;
  };
  async function request(backend, url, options = {}) {
    const headers = { ...(options.headers || {}) };
    if (options.auth === 'web') headers['X-Web-Session'] = backend.session;
    if (options.auth === 'admin') headers['X-Admin-Session'] = backend.admin;
    const body = typeof options.body === 'function' ? options.body(backend) : options.body;
    if (body !== undefined && !options.form) headers['Content-Type'] = 'application/json';
    if (options.form) headers['Content-Type'] = 'application/x-www-form-urlencoded';
    const response = await fetch(backend.base + (typeof url === 'function' ? url(backend) : url), {
      method: options.method || (body !== undefined ? 'POST' : 'GET'), headers,
      body: body === undefined ? undefined : options.form ? new URLSearchParams(body) : JSON.stringify(body), signal: AbortSignal.timeout(15000)
    });
    const text = await response.text();
    let value = text;
    if ((response.headers.get('content-type') || '').startsWith('application/json')) value = JSON.parse(text);
    if (options.capture) options.capture(value, backend);
    return {
      status: response.status,
      headers: Object.fromEntries(['content-type', 'cache-control', 'pragma', 'expires', 'retry-after'].map(key => [key, response.headers.get(key)])),
      body: scrub(value, backend)
    };
  }
  async function check(name, url, options = {}) {
    const results = [];
    for (const backend of backends) {
      try { results.push(await request(backend, url, options)); }
      catch (error) { results.push({ requestError: error.name + ': ' + error.message }); }
    }
    try { assert.deepEqual(results[1], results[0]); checks.push({ name, result: '一致' }); }
    catch { checks.push({ name, result: options.approvedDifference ? '已批准差异' : '不一致', reason: options.approvedDifference, node: results[0], go: results[1] }); }
    if (checks.length % 20 === 0) console.log('已对照 ' + checks.length + ' 项');
  }
  const api = (name, method, fields = {}, extra = {}) => check(name, '/api', { body: { method, ...fields }, ...extra });
  try {
    for (let attempt = 0; attempt < 100; attempt++) {
      if (child.exitCode !== null) throw new Error('Go fixture 服务提前退出：' + goLog);
      try { const health = await fetch(backends[1].base + '/healthz'); if (health.ok) break; } catch {}
      if (attempt === 99) throw new Error('Go fixture 启动超时：' + goLog);
      await new Promise(resolve => setTimeout(resolve, 100));
    }
    await check('公开配置', '/api/publicConfig');
    await check('端口视图', '/ports');
    await check('空状态列表', '/api/serverStatus');
    const methods = ['getAllUsers', 'activateUser', 'banUser', 'unbanUser', 'deleteUser', 'updateEmail', 'changePassword', 'addShopItem', 'updateShopItem', 'deleteShopItem', 'getShopItems', 'purchaseItem', 'generateCoupon', 'useCoupon', 'getCoupons', 'deleteCoupon', 'login', 'logout', 'regist', 'verifyRegist', 'repasswd', 'verifyCode'];
    for (const method of methods) await api('缺参/' + method, method, {}, method === 'useCoupon' ? { approvedDifference: '兑换入口隔离：Web listener 必须拒绝，插件改用专用令牌。' } : {});
    await api('未知method', 'unknown');
    await api('禁止空格', 'login', { name: 'Player One' });
    await api('禁止UUID', 'login', { name: 'PlayerOne', uuid: 'fake' });
    await api('禁止客户端时间', 'login', { name: 'PlayerOne', onlineTime: 1 });
    await check('body覆盖query', '/api?method=unknown', { body: { method: 'getShopItems' } });
    await check('表单参数', '/api', { body: { method: 'getShopItems' }, form: true });
    await check('网页登录/缺参', '/api/webLogin', { body: {} });
    for (const [username, password] of [['MissingOne', testPassword], ['BannedOne', testPassword], ['SleepyOne', testPassword], ['PlayerOne', 'incorrect']]) {
      await check('网页登录/' + username + (password === 'incorrect' ? '/错误密码' : ''), '/api/webLogin', { body: { username, password } });
    }
    await check('网页登录/历史用户名', '/api/webLogin', { body: { username: '老玩家', password: testPassword } });
    await check('网页登录/大小写', '/api/webLogin', { body: { username: 'playerone', password: testPassword }, capture(value, backend) { backend.session = value.session; } });
    await check('资料/无会话', '/api/userInfo');
    await check('资料/有效会话', '/api/userInfo', { auth: 'web' });
    await check('资料/冒用他人', '/api/userInfo?username=SleepyOne', { auth: 'web' });
    await check('签到历史', '/api/signHistory', { auth: 'web' });
    await check('排行榜/未登录', '/api/leaderboard');
    await check('排行榜/玩家', '/api/leaderboard', { auth: 'web' });
    await check('签到/首次', '/api/sign', { body: { username: 'PlayerOne' }, auth: 'web' });
    await check('签到/重复GET', '/api/sign', { auth: 'web' });
    await check('签到/他人', '/api/sign', { body: { username: 'SleepyOne' }, auth: 'web' });
    await check('管理员/缺参', '/api/adminLogin', { body: {} });
    await check('管理员/错误密码', '/api/adminLogin', { body: { name: adminName, passwd: 'incorrect', totp: '000000' } });
    await check('管理员/错误TOTP', '/api/adminLogin', { body: { name: adminName, passwd: adminPassword, totp: 'invalid' } });
    await check('管理员/成功', '/api/adminLogin', {
      body: () => ({ name: adminName, passwd: adminPassword, totp: speakeasy.totp({ secret: totpSecret, encoding: 'base32' }) }),
      capture(value, backend) { backend.admin = value.session; }
    });
    await check('排行榜/管理员', '/api/leaderboard', { auth: 'admin' });
    await api('用户列表/管理员', 'getAllUsers', {}, { auth: 'admin' });
    await api('用户列表/玩家越权', 'getAllUsers', {}, { auth: 'web' });
    for (const method of ['activateUser', 'banUser', 'unbanUser']) {
      await api(method + '/不存在', method, { name: 'MissingOne' }, { auth: 'admin' });
      await api(method + '/成功', method, { name: 'SleepyOne' }, { auth: 'admin' });
    }
    await api('邮箱/重复', 'updateEmail', { name: 'PlayerOne', newEmail: 'fixture1@example.invalid' }, { auth: 'admin' });
    await api('邮箱/成功', 'updateEmail', { name: 'PlayerOne', newEmail: 'changed@example.invalid' }, { auth: 'admin' });
    await api('改密/错误旧密码', 'changePassword', { name: 'PlayerOne', oldpasswd: 'incorrect', newpasswd: 'new-fixture-password' }, { auth: 'web' });
    await api('改密/成功', 'changePassword', { name: 'PlayerOne', oldpasswd: testPassword, newpasswd: 'new-fixture-password' }, { auth: 'web' });
    await api('商品删除/不存在', 'deleteShopItem', { itemId: 'missing' }, { auth: 'admin' });
    await api('商品更新/不存在', 'updateShopItem', { id: 'missing', name: 'Test', itemId: 'stone', amount: 1, points: 2 }, { auth: 'admin' });
    await api('商品新增/数量零按默认值', 'addShopItem', { name: 'Test', itemId: 'stone', amount: 0, points: 2 }, {
      auth: 'admin', capture(value, backend) { if (value.item?.id) backend.generated.set(value.item.id, '<default-amount-item>'); }
    });
    await api('商品新增/成功', 'addShopItem', { name: 'Test', itemId: 'minecraft:diamond', amount: 1, points: 12, stock: 2 }, {
      auth: 'admin', capture(value, backend) { if (value.item?.id) { backend.itemID = value.item.id; backend.generated.set(value.item.id, '<new-item>'); } }
    });
    await check('商品更新/成功', '/api', { auth: 'admin', body: backend => ({ method: 'updateShopItem', id: backend.itemID, name: 'Updated', itemId: 'minecraft:diamond', amount: 2, points: 15 }) });
    await api('商品列表/更新后', 'getShopItems');
    await api('购买/无会话', 'purchaseItem', { username: 'PlayerOne', itemId: 'fixture-item' });
    await api('购买/冒用他人', 'purchaseItem', { username: 'SleepyOne', itemId: 'fixture-item' }, { auth: 'web' });
    await api('购买/成功', 'purchaseItem', { username: 'PlayerOne', itemId: 'fixture-item' }, {
      auth: 'web', capture(value, backend) { if (value.coupon) { backend.coupon = value.coupon; backend.generated.set(value.coupon, '<purchase-coupon>'); } }
    });
    await api('兑换列表/管理员', 'getCoupons', {}, { auth: 'admin' });
    await api('生成兑换/错误类型', 'generateCoupon', { type: 'invalid', items: [], expiresAt: '2099-01-01T00:00:00Z' }, { auth: 'admin' });
    await api('生成兑换/成功', 'generateCoupon', { type: 'bundle', items: [{ itemId: 'stone', amount: 2 }], expiresAt: '2099-01-01T00:00:00.000Z' }, {
      auth: 'admin', capture(value, backend) { if (value.couponCode) { backend.newCoupon = value.couponCode; backend.generated.set(value.couponCode, '<admin-coupon>'); } }
    });
    await check('兑换删除/成功', '/api', { auth: 'admin', body: backend => ({ method: 'deleteCoupon', code: backend.newCoupon?.toLowerCase() }) });
    await check('商品删除/成功', '/api', { auth: 'admin', body: backend => ({ method: 'deleteShopItem', itemId: backend.itemID }) });
    await api('网页授权/管理员名', 'login', { name: 'admin' });
    await api('网页授权/无会话', 'login', { name: 'PlayerOne' });
    await api('网页授权/成功', 'login', { name: 'PlayerOne', time: 600000 }, { auth: 'web' });
    await api('网页授权/延长', 'login', { name: 'PlayerOne', time: 600000 }, { auth: 'web' });
    await check('资料/仅授权未入服', '/api/userInfo', { auth: 'web', approvedDifference: '新插件采用 check/join 两阶段，网页授权不再等同真实在线。' });
    await api('网页登出/已授权', 'logout', { name: 'PlayerOne' }, { auth: 'web' });
    await api('网页登出/重复', 'logout', { name: 'PlayerOne' }, { auth: 'web' });
    await api('注册/保留名', 'regist', { name: 'admin', passwd: testPassword, email: 'new@example.invalid' });
    await api('注册/重复', 'regist', { name: 'PlayerOne', passwd: testPassword, email: 'changed@example.invalid' });
    await api('注册/错误邮箱', 'regist', { name: 'NewOne', passwd: testPassword, email: 'invalid' });
    await api('验证注册/错误码', 'verifyRegist', { email: 'new@example.invalid', code: '000000' });
    await api('重置密码/不存在', 'repasswd', { name: 'MissingOne', newpasswd: testPassword });
    await api('重置密码/邮箱不符', 'repasswd', { name: 'PlayerOne', newpasswd: testPassword, email: 'wrong@example.invalid' });
    await api('验证重置/错误码', 'verifyCode', { email: 'new@example.invalid', code: '000000' });
    await check('头像/未登录', '/api/uploadAvatar', { body: { username: 'PlayerOne', avatar: '' } });
    await check('头像/错误格式', '/api/uploadAvatar', { body: { username: 'PlayerOne', avatar: 'bad' }, auth: 'web' });
    await check('商品图片/未登录', '/api/uploadItemImage', { body: { image: '', name: 'test' } });
    await check('商品图片/错误格式', '/api/uploadItemImage', { body: { image: 'bad', name: 'test' }, auth: 'admin' });
    await check('webhook/未知服务器', '/webhook/update-port', { body: { name: 'unknown', host: '127.0.0.1', port: 1234 } });
    await api('删除用户/成功', 'deleteUser', { name: 'SleepyOne' }, { auth: 'admin' });
    await api('删除用户/不存在', 'deleteUser', { name: 'SleepyOne' }, { auth: 'admin' });
    const differences = checks.filter(check => check.result === '不一致');
    writeJSON(path.join(root, '接口对照结果.json'), { generatedAt: new Date().toISOString(), total: checks.length, passed: checks.filter(check => check.result === '一致').length, approved: checks.filter(check => check.result === '已批准差异').length, differences: differences.length, checks });
    console.log(`接口对照：${checks.length} 项，一致 ${checks.filter(check => check.result === '一致').length}，已批准差异 ${checks.filter(check => check.result === '已批准差异').length}，不一致 ${differences.length}`);
    console.log('报告：' + path.join(root, '接口对照结果.json'));
    for (const difference of differences) console.log('不一致：' + difference.name);
    if (legacy.errors.length) console.log('旧处理器异常数：' + legacy.errors.length);
    process.exitCode = differences.length ? 1 : 0;
  } finally {
    child.kill();
    legacyServer.closeAllConnections();
    await new Promise(resolve => legacyServer.close(resolve));
    mail.close();
    legacy.DB.close();
  }
}

main().catch(error => { console.error(error); process.exitCode = 1; });
