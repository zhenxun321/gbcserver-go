'use strict';

const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const { spawn, spawnSync } = require('node:child_process');
const { DatabaseSync } = require('node:sqlite');
const protocol = require('minecraft-protocol');
const { workspace, testPassword, writeJSON, createFixture, migrate, listen, http } = require('./fixture.cjs');

const delay = milliseconds => new Promise(resolve => setTimeout(resolve, milliseconds));
async function until(check, description, timeout = 30000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    if (await check()) return;
    await delay(100);
  }
  throw new Error('等待超时：' + description);
}
async function freePort() {
  const reservation = http.createServer(), port = await listen(reservation);
  await new Promise(resolve => reservation.close(resolve));
  return port;
}

async function main() {
  const executable = path.resolve(process.argv[2] || path.join(workspace, '.codex-tools/gbcserver-integration.exe'));
  const java = process.argv[3] || process.env.JAVA_HOME && path.join(process.env.JAVA_HOME, 'bin/java.exe');
  if (!java) throw new Error('请在第二个参数提供 Java 21 路径，或设置 JAVA_HOME');
  const serverRoot = path.join(workspace, '.codex-tools/leaves-integration/server');
  const eula = path.join(serverRoot, 'eula.txt');
  if (!fs.existsSync(eula) || !/^eula=true\s*$/m.test(fs.readFileSync(eula, 'utf8'))) {
    throw new Error('请先确认 Minecraft EULA 并在隔离服 eula.txt 写入 eula=true');
  }
  const root = fs.mkdtempSync(path.join(workspace, '目标文件/Leaves联调-'));
  console.log('联调报告目录：' + root);
  const fixture = createFixture(path.join(root, 'fixture'));
  const databasePath = path.join(root, 'normalized.db');
  migrate(executable, fixture.dbPath, databasePath);
  const db = new DatabaseSync(databasePath);
  db.exec('PRAGMA busy_timeout=5000;');
  const environment = Object.fromEntries(Object.entries(process.env).filter(([key]) => !key.startsWith('GBC_')));
  function issue(scope) {
    const result = spawnSync(executable, ['plugin-token', 'issue', '--db', databasePath, '--server-id', 'main', '--scope', scope], { encoding: 'utf8', env: environment, windowsHide: true });
    if (result.status !== 0) throw new Error('签发测试 token 失败');
    return JSON.parse(result.stdout);
  }
  const whitelistCredential = issue('whitelist'), couponCredential = issue('coupon.redeem');
  const whitelistToken = whitelistCredential.secret, couponToken = couponCredential.secret;
  assert.equal(typeof whitelistToken, 'string'); assert.equal(typeof couponToken, 'string');
  const webPort = await freePort(), whitelistPort = await freePort(), couponPort = await freePort();
  const dataDirectory = path.join(root, 'data');
  fs.mkdirSync(dataDirectory, { recursive: true });
  const children = new Set(), clients = new Set(), checks = [];
  const logs = { web: '', plugin: '', leaves: '' };
  const redact = value => value.replaceAll(whitelistToken, '<whitelist-token>').replaceAll(couponToken, '<coupon-token>');
  function start(command, args, name, cwd = workspace) {
    const child = spawn(command, args, { cwd, env: environment, windowsHide: true, stdio: ['pipe', 'pipe', 'pipe'] });
    children.add(child); child.once('exit', () => children.delete(child));
    child.on('error', error => { logs[name] += String(error); });
    for (const pipe of [child.stdout, child.stderr]) pipe.setEncoding('utf8').on('data', data => { logs[name] += redact(data); });
    return child;
  }
  async function stop(child, graceful = false) {
    if (!child || child.exitCode !== null) return;
    if (graceful) child.stdin.end('stop\n'); else child.kill();
    await until(() => child.exitCode !== null || child.signalCode !== null, '进程停止', 30000).catch(() => child.kill());
  }
  async function check(name, action) {
    await action(); checks.push({ name, result: '通过' }); console.log('通过：' + name);
  }
  async function request(port, route, fields, token, headers = {}) {
    const response = await fetch(`http://127.0.0.1:${port}${route}`, {
      method: fields === undefined ? 'GET' : 'POST',
      headers: { ...(fields === undefined ? {} : { 'Content-Type': 'application/json' }), ...(token ? { Authorization: 'Bearer ' + token } : {}), ...headers },
      body: fields === undefined ? undefined : JSON.stringify(fields), signal: AbortSignal.timeout(10000)
    });
    const text = await response.text();
    return { status: response.status, body: text.startsWith('{') ? JSON.parse(text) : text };
  }
  function connect(username = 'PlayerOne') {
    const client = protocol.createClient({ host: '127.0.0.1', port: 25589, username, auth: 'offline', version: '1.21.8', hideErrors: true });
    clients.add(client);
    const state = { client, joined: false, ended: false, reason: '', messages: [] };
    client.once('playerJoin', () => { state.joined = true; });
    client.on('kick_disconnect', packet => { state.reason = JSON.stringify(packet.reason); });
    client.on('disconnect', packet => { state.reason = JSON.stringify(packet.reason); });
    client.on('system_chat', packet => state.messages.push(JSON.stringify(packet)));
    client.on('error', error => { state.reason = error.message; });
    client.once('end', () => { state.ended = true; clients.delete(client); });
    client.on('position', packet => { if (packet.teleportId !== undefined) client.write('teleport_confirm', { teleportId: packet.teleportId }); });
    return state;
  }
  function session() { return db.prepare('SELECT login_at,last_seen_at,lease_expires_at FROM plugin_online_sessions WHERE server_id=?').get('main'); }
  function consoleCommand(command) { leaves.stdin.write(command + '\n'); }
  let leaves, plugin, web;
  let finalError;
  try {
    web = start(executable, [
      'web-serve', '--data-dir', dataDirectory, '--db', databasePath, '--listen', `127.0.0.1:${webPort}`,
      '--server-id', 'main', '--status-apis', 'none'
    ], 'web');
    plugin = start(executable, [
      'plugin-serve', '--data-dir', dataDirectory, '--db', databasePath,
      '--whitelist-listen', `127.0.0.1:${whitelistPort}`, '--coupon-listen', `127.0.0.1:${couponPort}`
    ], 'plugin');
    await until(async () => { try { return (await request(webPort, '/healthz')).status === 200; } catch { return false; } }, 'Go Web 就绪');
    await until(async () => { try { const response = await request(whitelistPort, '/check/PlayerOne', {}, whitelistToken); return response.status === 403 && response.body.reason === 'not_authorized'; } catch { return false; } }, 'Go 插件端口就绪');
    for (const [folder, jar] of [['WhiteListSync', 'whitelist/WhiteListSync-2.0.1.jar'], ['CouponPlugin', 'coupon/CouponPlugin-1.3.1.jar']]) {
      fs.mkdirSync(path.join(serverRoot, 'plugins', folder), { recursive: true });
      fs.copyFileSync(path.join(workspace, '目标文件/plugins', jar), path.join(serverRoot, 'plugins', path.basename(jar)));
    }
    fs.writeFileSync(path.join(serverRoot, 'plugins/WhiteListSync/config.yml'), `api-url: "http://127.0.0.1:${whitelistPort}"\nwhitelist-token: "${whitelistToken}"\nrequest-timeout-seconds: 2\n`, 'utf8');
    const couponConfigPath = path.join(serverRoot, 'plugins/CouponPlugin/config.yml');
    fs.writeFileSync(couponConfigPath, `api-url: "http://127.0.0.1:${couponPort}/api"\ncoupon-token: "${couponToken}"\nmax-item-amount: 2304\n`, 'utf8');
    leaves = start(java, [`-Duser.home=${serverRoot}`, '-Dterminal.jline=false', '-Dterminal.ansi=false', '-Dfile.encoding=UTF-8', '-Xms512M', '-Xmx1200M', '-jar', '../leaves-1.21.8.jar', '--nogui'], 'leaves', serverRoot);
    await until(() => { if (leaves.exitCode !== null) throw new Error('Leaves 提前退出，见日志'); return /Done \([\d.]+s\)!/.test(logs.leaves); }, 'Leaves 启动', 90000);
    await check('Leaves 1.21.8 / Java 21 加载两个插件', async () => {
      assert.ok(logs.leaves.includes('[WhiteListSync] Enabling WhiteListSync v2.0.1'), '白名单插件未启用');
      assert.ok(logs.leaves.includes('Coupon plugin enabled'), '兑换插件未启用');
      assert.doesNotMatch(logs.leaves, /Error occurred while enabling|Could not load plugin/);
    });
    await check('未网页授权玩家被拒绝入服', async () => {
      const denied = connect(); await until(() => denied.ended, '拒绝未授权登录'); assert.equal(denied.joined, false); assert.match(denied.reason, /网页|授权|白名单/);
    });
    const login = await request(webPort, '/api/webLogin', { username: 'PlayerOne', password: testPassword });
    assert.equal(login.body.success, true);
    const authHeaders = { 'X-Web-Session': login.body.session };
    await check('网页登录授权不提前累计在线时长', async () => {
      const result = await request(webPort, '/api', { method: 'login', name: 'PlayerOne', time: 600000 }, null, authHeaders);
      assert.equal(result.status, 200); assert.equal(session().login_at, null);
    });
    let player = connect();
    await check('异步预登录与真实 Join 确认', async () => {
      await until(() => player.joined, '玩家进入 PLAY'); await until(() => !!session()?.login_at, '后端确认 Join');
      await delay(500); assert.equal(player.ended, false);
    });
    consoleCommand('gamemode creative PlayerOne'); consoleCommand('clear PlayerOne');
    await delay(500);
    await check('真实 /coupon 发放旧式物品 ID 并保存背包', async () => {
      db.prepare('UPDATE coupon_items SET item_id=? WHERE coupon_code=?').run('stone', 'FIXTURE123');
      player.client.chat('/coupon FIXTURE123');
      await until(() => player.messages.some(message => message.includes('Coupon reward delivered')), '奖励到账');
      assert.equal(db.prepare('SELECT COUNT(*) AS n FROM coupon_usages WHERE coupon_code=?').get('FIXTURE123').n, 1);
      assert.equal(db.prepare('SELECT COUNT(*) AS n FROM plugin_coupon_requests').get().n, 1);
      consoleCommand('clear PlayerOne minecraft:stone 0');
      await until(() => /Found 2 matching item/.test(logs.leaves), '背包中存在两个石头');
    });
    await check('同一 request ID 重放不重复消费兑换码', async () => {
      const saved = db.prepare('SELECT request_id FROM plugin_coupon_requests LIMIT 1').get();
      const result = await request(couponPort, '/api', { method: 'useCoupon', username: 'PlayerOne', code: 'FIXTURE123', requestId: saved.request_id }, couponToken, { 'X-Request-ID': saved.request_id });
      assert.equal(result.body.success, true);
      assert.equal(db.prepare('SELECT COUNT(*) AS n FROM coupon_usages WHERE coupon_code=?').get('FIXTURE123').n, 1);
    });
    await check('玩家 Quit 关闭会话并累计真实在线时长', async () => {
      player.client.end('integration quit'); await until(() => !session(), 'Quit 上报');
      assert.ok(db.prepare('SELECT online_time_ms FROM users WHERE name=?').get('PlayerOne').online_time_ms > 1234);
    });
    await check('持久化待发账本经服务重启自动恢复', async () => {
      await stop(leaves, true);
      const uuid = player.client.uuid;
      const ledger = `pending-deliveries:\n  ${uuid}:\n    integration-recovery:\n      player-name: PlayerOne\n      redemption-path: ""\n      items:\n      - itemId: minecraft:diamond\n        amount: 3\n`;
      fs.writeFileSync(path.join(serverRoot, 'plugins/CouponPlugin/pending-deliveries.yml'), ledger, 'utf8');
      const previousLogLength = logs.leaves.length;
      leaves = start(java, [`-Duser.home=${serverRoot}`, '-Dterminal.jline=false', '-Dterminal.ansi=false', '-Dfile.encoding=UTF-8', '-Xms512M', '-Xmx1200M', '-jar', '../leaves-1.21.8.jar', '--nogui'], 'leaves', serverRoot);
      await until(() => /Done \([\d.]+s\)!/.test(logs.leaves.slice(previousLogLength)), 'Leaves 重启', 90000);
      await request(webPort, '/api', { method: 'login', name: 'PlayerOne', time: 600000 }, null, authHeaders);
      player = connect(); await until(() => player.joined, '恢复玩家登录');
      await until(() => player.messages.some(message => message.includes('Coupon reward delivered')), '待发奖励恢复');
      consoleCommand('clear PlayerOne minecraft:diamond 0');
      await until(() => /Found 3 matching item/.test(logs.leaves), '背包中恢复三个钻石');
      assert.doesNotMatch(fs.readFileSync(path.join(serverRoot, 'plugins/CouponPlugin/pending-deliveries.yml'), 'utf8'), /integration-recovery/);
    });
    await check('独立 token 撤销后登录按 fail-closed 拒绝', async () => {
      player.client.end('token rotation'); await until(() => !session(), '退出');
      await request(webPort, '/api', { method: 'login', name: 'PlayerOne', time: 600000 }, null, authHeaders);
      const revoke = spawnSync(executable, ['plugin-token', 'revoke', '--db', databasePath, '--token-id', whitelistCredential.token_id], { env: environment, encoding: 'utf8', windowsHide: true });
      assert.equal(revoke.status, 0);
      const denied = connect(); await until(() => denied.ended, '撤销 token 拒绝'); assert.equal(denied.joined, false);
    });
    await check('白名单后端不可用时按 fail-closed 拒绝', async () => {
      await stop(plugin);
      const denied = connect(); await until(() => denied.ended, '服务故障拒绝'); assert.equal(denied.joined, false);
      assert.match(denied.reason, /不可用|白名单/);
    });
  } catch (error) {
    finalError = error; console.error(error.message);
  } finally {
    for (const client of clients) client.end('integration cleanup');
    await stop(leaves, true); await stop(plugin); await stop(web);
    for (const child of children) child.kill();
    for (const [name, log] of Object.entries(logs)) fs.writeFileSync(path.join(root, name + '.log'), log, 'utf8');
    writeJSON(path.join(root, '联调结果.json'), { generatedAt: new Date().toISOString(), minecraft: '1.21.8', java: '21', passed: checks.length, checks, error: finalError?.message || null });
    db.close();
  }
  if (finalError) throw finalError;
  console.log('Leaves 联调全部通过：' + checks.length + ' 项；测试进程已停止。');
}

main().catch(error => { console.error(error.stack); process.exitCode = 1; });
