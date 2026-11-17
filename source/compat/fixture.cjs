'use strict';

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const crypto = require('node:crypto');
const http = require('node:http');
const net = require('node:net');
const { DatabaseSync } = require('node:sqlite');
const { spawnSync } = require('node:child_process');
const bcrypt = require('bcryptjs');

const workspace = path.resolve(__dirname, '../..');
const testPassword = 'Fixture-Only-Password!';
const adminName = 'fixture-admin';
const adminPassword = 'Fixture-Only-Admin!';
const totpSecret = 'JBSWY3DPEHPK3PXP';

function writeJSON(file, value) {
  fs.writeFileSync(file, JSON.stringify(value, null, 2) + '\n', 'utf8');
}

function copyTree(source, target) {
  fs.mkdirSync(target, { recursive: true });
  for (const entry of fs.readdirSync(source, { withFileTypes: true })) {
    const from = path.join(source, entry.name), to = path.join(target, entry.name);
    if (entry.isDirectory()) copyTree(from, to);
    else if (entry.isFile()) fs.copyFileSync(from, to);
    else throw new Error('测试静态目录包含不支持的文件类型');
  }
}

function createFixture(root) {
  fs.mkdirSync(root, { recursive: true });
  fs.mkdirSync(path.join(root, 'avatars'), { recursive: true });
  copyTree(path.join(workspace, 'source/web'), path.join(root, 'public'));
  const passwordHash = bcrypt.hashSync(testPassword, 10);
  const users = ['PlayerOne', 'SleepyOne', 'BannedOne', '老玩家'].map((name, index) => ({
    name,
    uuid: `123e4567-e89b-12d3-a456-42661417400${index}`,
    passwd: passwordHash,
    email: `fixture${index}@example.invalid`,
    status: ['active', 'inactive', 'banned', 'active'][index],
    joinDate: '2025-01-01',
    onlineTime: 1234 + index,
    lastLogin: '2025-01-02 00:00:00',
    avatarPath: null
  }));
  const values = {
    whitedata: users,
    signData: Object.fromEntries(users.map(user => [user.name, {
      totalDays: 1, consecutiveDays: 1, lastSign: '2025-01-01', points: 500,
      signHistory: { '2025-01-01': true }
    }])),
    shopItems: [{
      id: 'fixture-item', name: 'Stone', itemId: 'minecraft:stone', amount: 2,
      points: 10, stock: 5, image: 'images/default_item.png',
      description: '隔离测试商品', createdAt: '2025-01-01T00:00:00.000Z'
    }],
    coupons: [{
      code: 'FIXTURE123', type: 'item', items: [{ itemId: 'minecraft:stone', amount: 2 }],
      designatedPlayer: 'PlayerOne', expiresAt: '2099-01-01T00:00:00.000Z', oneTimeUse: true,
      createdAt: '2025-01-01T00:00:00.000Z', used: false, usedBy: []
    }],
    ports: { main: 25565, '127.0.0.1': 25565 }
  };
  const dbPath = path.join(root, 'legacy.sqlite');
  const db = new DatabaseSync(dbPath);
  db.exec('CREATE TABLE kv_json(k TEXT PRIMARY KEY,v TEXT NOT NULL,updated_at INTEGER NOT NULL)');
  const insert = db.prepare('INSERT INTO kv_json(k,v,updated_at) VALUES(?,?,0)');
  for (const [key, value] of Object.entries(values)) insert.run(key, JSON.stringify(value));
  db.close();
  return { dbPath, values, passwordHash };
}

// 原处理器仅可访问临时 fixture 路径，不接触扫描快照内的配置或数据。
function guardedFS(root) {
  const prefix = path.resolve(root).toLowerCase() + path.sep;
  function guard(value) {
    const resolved = path.resolve(String(value)).toLowerCase();
    if (resolved !== prefix.slice(0, -1) && !resolved.startsWith(prefix)) {
      throw new Error('隔离路径之外的文件访问已拒绝');
    }
  }
  const wrapped = {};
  for (const [key, value] of Object.entries(fs)) {
    if (typeof value !== 'function') { wrapped[key] = value; continue; }
    wrapped[key] = (...args) => {
      guard(args[0]);
      if (/^(rename|copyFile)/.test(key)) guard(args[1]);
      return value(...args);
    };
  }
  wrapped.promises = {};
  for (const [key, value] of Object.entries(fs.promises)) {
    if (typeof value !== 'function') continue;
    wrapped.promises[key] = (...args) => {
      guard(args[0]);
      if (/^(rename|copyFile)/.test(key)) guard(args[1]);
      return value(...args);
    };
  }
  return wrapped;
}

function createLegacy(root) {
  const fixture = createFixture(root);
  writeJSON(path.join(root, 'config.json'), {
    admin: { default: { username: adminName, passwordHash: bcrypt.hashSync(adminPassword, 10), totpSecret } },
    http: { staticDir: 'public' }, storage: { databasePath: 'legacy.sqlite', avatarsDir: 'avatars' },
    game: { onlineMode: false }, status: { servers: [] },
    mail: { host: 'mail.example.invalid', fromAddress: 'fixture@example.invalid', auth: { user: '', pass: '' } }
  });
  class SQLiteAdapter {
    constructor(file) { this.database = new DatabaseSync(file); }
    exec(sql) { return this.database.exec(sql); }
    prepare(sql) { return this.database.prepare(sql); }
    close() { this.database.close(); }
  }
  const messages = [];
  const errors = [];
  const denyNetwork = () => { throw new Error('隔离对照禁止外部网络'); };
  const modules = {
    express: require('express'), 'body-parser': require('body-parser'), bcryptjs: bcrypt,
    speakeasy: require('speakeasy'), 'better-sqlite3': SQLiteAdapter,
    fs: guardedFS(root), path, crypto,
    util: require('node:util'),
    http: { request: denyNetwork, get: denyNetwork },
    https: { request: denyNetwork, get: denyNetwork },
    net: { Socket: class { constructor() { denyNetwork(); } } },
    nodemailer: { createTransport: () => ({ sendMail: async message => { messages.push(message); return { accepted: [message.to] }; } }) },
    child_process: { exec: (_command, callback) => callback(new Error('隔离对照禁止创建子进程')) }
  };
  const sandbox = vm.createContext({
    require(name) { if (!(name in modules)) throw new Error('未授权模块：' + name); return modules[name]; },
    __dirname: root, __filename: path.join(root, 'legacy-handler.js'),
    process: { env: { CONFIG_PATH: path.join(root, 'config.json') }, pid: process.pid, exit(code) { throw new Error('旧处理器尝试退出：' + code); } },
    console: { log() {}, warn() {}, error(...args) { errors.push(args.map(String).join(' ')); } },
    URL, Buffer, TextEncoder, TextDecoder, setTimeout, clearTimeout,
    setInterval() { return { unref() {} }; }, clearInterval() {}
  });
  const sourcePath = path.join(workspace, '遗留输入/扫描快照/whiteHitBlack.js');
  const source = fs.readFileSync(sourcePath, 'utf8');
  const boundary = source.indexOf('// 创建HTTPS服务器（如证书存在）');
  if (boundary < 0) throw new Error('找不到旧处理器监听前的隔离边界，停止执行');
  vm.runInContext(source.slice(0, boundary), sandbox, { filename: 'isolated-legacy-handler.js', timeout: 10000 });
  const state = vm.runInContext('({ app, DB, verificationCodes })', sandbox);
  return { ...fixture, ...state, messages, errors };
}

function migrate(executable, source, target) {
  const result = spawnSync(executable, ['migrate', '--source', source, '--target', target, '--apply'], { encoding: 'utf8', windowsHide: true });
  if (result.status !== 0) throw new Error('fixture 规范化导入失败：' + (result.stderr || result.error || result.stdout));
}

async function listen(server) {
  await new Promise((resolve, reject) => { server.once('error', reject); server.listen(0, '127.0.0.1', resolve); });
  return server.address().port;
}

async function smtpSink() {
  const messages = [];
  const sockets = new Set();
  const server = net.createServer(socket => {
    sockets.add(socket); socket.on('close', () => sockets.delete(socket));
    socket.setEncoding('utf8'); socket.write('220 fixture SMTP\r\n');
    let buffer = '', body = null;
    socket.on('data', data => {
      buffer += data;
      let newline;
      while ((newline = buffer.indexOf('\r\n')) >= 0) {
        const line = buffer.slice(0, newline); buffer = buffer.slice(newline + 2);
        if (body !== null) {
          if (line === '.') { messages.push(body.join('\r\n')); body = null; socket.write('250 accepted\r\n'); }
          else body.push(line);
        } else if (/^(EHLO|HELO)/i.test(line)) socket.write('250 fixture\r\n');
        else if (/^DATA/i.test(line)) { body = []; socket.write('354 send data\r\n'); }
        else if (/^QUIT/i.test(line)) socket.end('221 bye\r\n');
        else socket.write('250 ok\r\n');
      }
    });
    socket.on('error', () => {});
  });
  const port = await listen(server);
  return { port, messages, close() { for (const socket of sockets) socket.destroy(); server.close(); } };
}

module.exports = { workspace, testPassword, adminName, adminPassword, totpSecret, writeJSON, createFixture, createLegacy, migrate, listen, smtpSink, http };
