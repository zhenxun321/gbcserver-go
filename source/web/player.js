
            // 页面状态管理
            let isLoggedIn = false;
            
            
            let refreshTimer = null;
            let shopRefreshTimer = null;
            let shopRefreshRequestId = 0;
            let shopLastRefreshAt = 0;
            let latestShopItems = [];
            
            
            
            let currentPage = 'user-center';
            let isRegistering = false;
            const dialogTriggers = new Map();
            let latestLeaderboardData = [];
            let isResettingPassword = false;
            // 每日0点刷新签到状态：避免重复初始化多个定时器
            let dailyResetInitialized = false;
            let dailyResetTimer = null;
            let signRequestPending = false;
            let signData = {
                signedDays: [],
                consecutiveDays: 0,
                totalDays: 0,
                lastSign: "",
                points: 0,
                today: "",
                timezoneOffsetHours: 8
            };
            let userData = {
                username: '',
                password: '',
                avatar: 'data:image/svg+xml,%3Csvg xmlns=\'http://www.w3.org/2000/svg\' width=\'100\' height=\'100\' viewBox=\'0 0 100 100\'%3E%3Ccircle cx=\'50\' cy=\'40\' r=\'20\' fill=\'%233498db\'/%3E%3Ccircle cx=\'50\' cy=\'100\' r=\'40\' fill=\'%233498db\'/%3E%3C/svg%3E',
                status: '在线',
                points: 0,
                joinDate: '',
                lastLogin: '',
                serverStatus: '未登录',
                onlineTime: 0,
                signStatus: {
                    signedDays: [],
                    currentDay: new Date().getDate(),
                    consecutiveDays: 0
                }
            };

            // API基础URL
            const API_BASE_URL = '/api';
            const DEFAULT_ITEM_IMAGE = '/images/default_item.png';
            const DEFAULT_AVATAR_IMAGE = 'data:image/svg+xml,%3Csvg xmlns=\'http://www.w3.org/2000/svg\' width=\'100\' height=\'100\' viewBox=\'0 0 100 100\'%3E%3Ccircle cx=\'50\' cy=\'40\' r=\'20\' fill=\'%233498db\'/%3E%3Ccircle cx=\'50\' cy=\'100\' r=\'40\' fill=\'%233498db\'/%3E%3C/svg%3E';

            
            function getWebSession() {
                return localStorage.getItem('webSession') || '';
            }
            


            // ---------- 安全渲染工具（防XSS） ----------
            function safeSetText(el, text) {
                el.textContent = (text === null || text === undefined) ? '' : String(text);
            }
            function apiUrl(params = {}) {
                const url = new URL(API_BASE_URL, window.location.origin);
                Object.entries(params).forEach(([key, value]) => {
                    if (value !== undefined && value !== null && value !== '') {
                        url.searchParams.set(key, String(value));
                    }
                });
                return `${url.pathname}${url.search}`;
            }
            function samePlayerName(a, b) {
                return String(a || '').trim().toLowerCase() === String(b || '').trim().toLowerCase();
            }
            function safeImageSrc(src, fallback = DEFAULT_ITEM_IMAGE) {
                if (!src) return fallback;
                try {
                    const u = new URL(String(src), window.location.href);
                    // 禁止 data: / javascript:
                    if (u.protocol !== 'http:' && u.protocol !== 'https:') return fallback;
                    return u.href;
                } catch (_) {
                    return fallback;
                }
            }
            function setImageFallback(img, fallback = DEFAULT_ITEM_IMAGE) {
                img.addEventListener('error', () => {
                    if (img.dataset.fallbackApplied === 'true') return;
                    img.dataset.fallbackApplied = 'true';
                    img.src = fallback;
                });
            }
            
            
            
            
            
            
            

            // 显示通知
            let notificationTimer = null;
            function showNotification(message, type = 'success') {
                const notification = document.getElementById('notification');
                notification.textContent = message;
                notification.className = `notification ${type} show`;

                // 清掉上一次的隐藏定时器，避免新通知被提前隐藏
                if (notificationTimer) clearTimeout(notificationTimer);
                notificationTimer = setTimeout(() => {
                    notification.classList.remove('show');
                    notificationTimer = null;
                }, 3000);
            }

            // 显示加载动画
            function showLoading(show) {
                const loadingOverlay = document.getElementById('loading-overlay');
                loadingOverlay.style.display = show ? 'flex' : 'none';
            }
            // 弹窗放到页面根部，避免内容区的动画或层叠上下文影响定位。
            function openDialog(id, focusSelector = '.close-button') {
                const modal = document.getElementById(id);
                dialogTriggers.set(id, document.activeElement);
                document.body.appendChild(modal);
                modal.style.display = 'flex';
                modal.setAttribute('aria-hidden', 'false');
                document.body.classList.add('modal-open');
                const content = modal.querySelector('.modal-body');
                if (content) content.scrollTop = 0;
                window.requestAnimationFrame(() => {
                    if (modal.getAttribute('aria-hidden') === 'false') {
                        modal.querySelector(focusSelector)?.focus({ preventScroll: true });
                    }
                });
            }

            function closeDialog(id) {
                const modal = document.getElementById(id);
                if (modal.getAttribute('aria-hidden') !== 'false') return;
                modal.style.display = 'none';
                modal.setAttribute('aria-hidden', 'true');
                document.body.classList.toggle('modal-open', Boolean(document.querySelector('.modal[aria-hidden="false"]')));
                const trigger = dialogTriggers.get(id);
                dialogTriggers.delete(id);
                if (trigger instanceof HTMLElement && trigger.getClientRects().length) trigger.focus({ preventScroll: true });
            }
            // 打开商品弹窗
            // 打开商品弹窗
            

            // 关闭商品弹窗
            

            // 保存商品
            // 保存商品
            

            // 删除商品

            

            async function purchaseItem(itemId) {
                try {
                    const session = getWebSession();
                    if (!session) {
                        showNotification('玩家会话已过期，请重新登录', 'error');
                        return;
                    }

                    // 获取商品信息
                    const shopItemsResponse = await fetch(apiUrl({ method: 'getShopItems', t: Date.now() }), {
                        cache: 'no-store'
                    });
                    const shopItemsData = await shopItemsResponse.json();

                    if (!shopItemsData.success) {
                        showNotification('获取商品信息失败', 'error');
                        return;
                    }

                    const item = shopItemsData.items.find(i => String(i.id) === String(itemId));
                    if (!item) {
                        showNotification('商品不存在', 'error');
                        return;
                    }

                    const isConfirmed = confirm(`确定要兑换 ${item.name} 吗？需要花费 ${item.points} 积分。`);
                    if (!isConfirmed) return;

                    showLoading(true);

                    const response = await fetch(apiUrl({ method: 'purchaseItem', username: userData.username, itemId }), {
                        headers: { 'X-Web-Session': session }
                    });

                    const data = await response.json();

                    if (data.success) {
                        document.getElementById('coupon-item-name').textContent = data.item?.name || '';
                        document.getElementById('coupon-code').textContent = data.coupon || '';
                        openDialog('coupon-modal');

                        // 兑换后以服务端数据为准，库存、积分和兑换码列表一起同步。
                        await Promise.all([
                            refreshShop({ silent: true }),
                            fetchUserInfo()
                        ]);
                        showNotification(data.message || '兑换成功');
                    } else {
                        showNotification(`兑换失败: ${data.message || '未知错误'}`, 'error');
                    }
                } catch (error) {
                    console.error('兑换商品失败:', error);
                    showNotification('兑换失败', 'error');
                } finally {
                    showLoading(false);
                }
            }
            

            

            

            

            

            

            

            // 添加礼包物品行
            

            // 移除礼包物品行，至少保留一行方便继续填写。
            

            function updateShopSummary(items = latestShopItems) {
    const total = Array.isArray(items) ? items.length : 0;
    const available = (items || []).filter(item => {
        const unlimited = item.stock === null || item.stock === undefined || item.stock === '';
        return unlimited || Number(item.stock) > 0;
    }).length;
    const totalNode = document.getElementById('shop-total-count');
    const availableNode = document.getElementById('shop-available-count');
    if (totalNode) totalNode.textContent = String(total);
    if (availableNode) availableNode.textContent = String(available);
}

            function updateShopRefreshMeta(visibleCount = latestShopItems.length) {
                const statusNode = document.getElementById('shop-sync-status');
                const countNode = document.getElementById('shop-item-count');
                if (statusNode) statusNode.textContent = `已同步 ${new Date().toLocaleTimeString()}`;
                if (countNode) {
                    const total = latestShopItems.length;
                    countNode.textContent = visibleCount === total
                        ? `${total} 件商品`
                        : `显示 ${visibleCount} / ${total} 件商品`;
                }
            }

            function startShopAutoRefresh() {
                if (shopRefreshTimer) clearInterval(shopRefreshTimer);
                shopRefreshTimer = null;
                if (!isLoggedIn || currentPage !== 'shop') return;
                shopRefreshTimer = setInterval(() => {
                    if (document.visibilityState === 'visible' && currentPage === 'shop') {
                        refreshShop({ silent: true });
                    }
                }, 30000);
            }

            function stopShopAutoRefresh() {
                if (shopRefreshTimer) clearInterval(shopRefreshTimer);
                shopRefreshTimer = null;
            }

            function refreshShopOnResume() {
                if (!isLoggedIn || currentPage !== 'shop' || document.visibilityState !== 'visible') return;
                if (Date.now() - shopLastRefreshAt < 1000) return;
                refreshShop({ silent: true });
            }

            // 刷新商城；所有库存、积分和兑换码状态均以服务端响应为准。
            async function refreshShop({ silent = false } = {}) {
                const requestId = ++shopRefreshRequestId;
                if (!silent) showLoading(true);

                try {
                    const itemRequest = fetch(apiUrl({ method: 'getShopItems', t: Date.now() }), {
                        cache: 'no-store'
                    });
                    const requests = [itemRequest];

                    

                    const responses = await Promise.all(requests);
                    const data = await responses[0].json();

                    if (requestId !== shopRefreshRequestId) return;
                    if (!data.success) throw new Error(data.message || '获取商品列表失败');

                    latestShopItems = Array.isArray(data.items) ? data.items : [];
                    renderShopItems(latestShopItems);
                    

                    

                    updateShopSummary(latestShopItems);
                    shopLastRefreshAt = Date.now();
                    if (document.getElementById('shop-item-search')) {
                        updateShopRefreshMeta(document.querySelectorAll('#shop-items-grid .shop-item-card').length);
                    }
                } catch (error) {
                    console.error('刷新商城失败:', error);
                    const statusNode = document.getElementById('shop-sync-status');
                    if (statusNode && requestId === shopRefreshRequestId) {
                        statusNode.textContent = '同步失败，请重试';
                    }
                    if (!silent && requestId === shopRefreshRequestId) {
                        showNotification(`刷新商城失败: ${error.message}`, 'error');
                    }
                } finally {
                    if (!silent && requestId === shopRefreshRequestId) showLoading(false);
                }
            }

            document.addEventListener('visibilitychange', refreshShopOnResume);
            window.addEventListener('focus', refreshShopOnResume);
            function copyCouponCode() {
                const couponCode = document.getElementById('coupon-code').textContent;
                copyText(couponCode).then(() => {
                    showNotification('兑换码已复制到剪贴板！');
                }).catch(() => {
                    showNotification('复制失败，请手动复制', 'error');
                });
            }

            async function copyText(text) {
                const value = String(text || '');
                if (!value) return;
                if (navigator.clipboard?.writeText) {
                    await navigator.clipboard.writeText(value);
                    return;
                }
                const input = document.createElement('textarea');
                input.value = value;
                input.style.position = 'fixed';
                input.style.opacity = '0';
                document.body.appendChild(input);
                input.select();
                document.execCommand('copy');
                input.remove();
            }

            function applyShopFilters() {
                renderShopItems(latestShopItems);
            }

            // 渲染商品列表
            // 渲染商品列表（安全：不使用 innerHTML 注入不可信数据）
            function renderShopItems(items) {
                const grid = document.getElementById('shop-items-grid');
                if (!grid) return;
                grid.innerHTML = '';

                const search = String(document.getElementById('shop-item-search')?.value || '').trim().toLowerCase();
                const stockFilter = document.getElementById('shop-stock-filter')?.value || 'all';
                const sortMode = document.getElementById('shop-sort')?.value || 'default';
                const sourceItems = Array.isArray(items) ? items : [];
                const visibleItems = sourceItems.filter(item => {
                    const name = String(item.name || '').toLowerCase();
                    const itemId = String(item.itemId || '').toLowerCase();
                    const matchesSearch = !search || name.includes(search) || itemId.includes(search);
                    const unlimited = item.stock === null || item.stock === undefined || item.stock === '';
                    const stock = unlimited ? null : Number(item.stock);
                    const matchesStock = stockFilter === 'all'
                        || (stockFilter === 'unlimited' && unlimited)
                        || (stockFilter === 'available' && (unlimited || stock > 0))
                        || (stockFilter === 'soldout' && !unlimited && (!Number.isFinite(stock) || stock <= 0));
                    return matchesSearch && matchesStock;
                });

                visibleItems.sort((a, b) => {
                    if (sortMode === 'points-asc') return Number(a.points || 0) - Number(b.points || 0);
                    if (sortMode === 'points-desc') return Number(b.points || 0) - Number(a.points || 0);
                    if (sortMode === 'stock-asc') {
                        const stockA = a.stock === null || a.stock === undefined || a.stock === '' ? Number.MAX_SAFE_INTEGER : Number(a.stock);
                        const stockB = b.stock === null || b.stock === undefined || b.stock === '' ? Number.MAX_SAFE_INTEGER : Number(b.stock);
                        return stockA - stockB;
                    }
                    if (sortMode === 'name') return String(a.name || '').localeCompare(String(b.name || ''), 'zh-CN');
                    return 0;
                });

                updateShopRefreshMeta(visibleItems.length);
                if (visibleItems.length === 0) {
                    const empty = document.createElement('div');
                    empty.className = 'shop-empty-state';
                    safeSetText(empty, sourceItems.length ? '没有符合条件的商品' : '商城暂时没有商品');
                    grid.appendChild(empty);
                    return;
                }

                visibleItems.forEach(item => {
                    const card = document.createElement('div');
                    card.className = 'shop-item-card';

                    const imageWrap = document.createElement('div');
                    imageWrap.className = 'item-image';

                    const img = document.createElement('img');
                    img.src = safeImageSrc(item.image);
                    img.alt = item?.name ? String(item.name) : '';
                    img.referrerPolicy = 'no-referrer';
                    setImageFallback(img);
                    imageWrap.appendChild(img);

                    const details = document.createElement('div');
                    details.className = 'item-details';

                    const name = document.createElement('h3');
                    name.className = 'item-name';
                    safeSetText(name, item.name);

                    const idp = document.createElement('p');
                    idp.className = 'item-id';
                    safeSetText(idp, `ID: ${item.itemId || ''}`);

                    const desc = document.createElement('p');
                    desc.className = 'item-description';
                    safeSetText(desc, item.description || '暂无描述');

                    const meta = document.createElement('div');
                    meta.className = 'item-meta';

                    const pts = document.createElement('span');
                    pts.className = 'item-points';
                    safeSetText(pts, `${item.points} 积分`);

                    const amt = document.createElement('span');
                    amt.className = 'item-stock';
                    const amountVal = Number.isFinite(parseInt(item.amount, 10)) ? parseInt(item.amount, 10) : 1;
                    safeSetText(amt, `数量: ${amountVal}`);

                    const st = document.createElement('span');
                    st.className = 'item-stock';
                    const unlimited = (item.stock === null || item.stock === undefined || item.stock === '');
                    safeSetText(st, `库存: ${unlimited ? '无限' : String(item.stock)}`);

                    meta.appendChild(pts);
                    meta.appendChild(amt);
                    meta.appendChild(st);

                    const actions = document.createElement('div');
                    actions.className = 'item-actions';

                    {
                        const buyBtn = document.createElement('button');
                        buyBtn.type = 'button';
                        buyBtn.className = 'server-button primary';
                        const stockNum = unlimited ? null : parseInt(item.stock, 10);
                        const disabled = (stockNum !== null && (!Number.isFinite(stockNum) || stockNum <= 0));
                        if (disabled) {
                            buyBtn.classList.add('disabled');
                            buyBtn.disabled = true;
                        }
                        safeSetText(buyBtn, '兑换');
                        buyBtn.addEventListener('click', () => purchaseItem(String(item.id)));
                        actions.appendChild(buyBtn);
                    }

                    details.appendChild(name);
                    details.appendChild(idp);
                    details.appendChild(desc);
                    details.appendChild(meta);
                    details.appendChild(actions);

                    card.appendChild(imageWrap);
                    card.appendChild(details);
                    grid.appendChild(card);
                });
            }

            

            

            
            // 打开兑换码生成弹窗

            

            

            // 关闭兑换码生成弹窗
            

            // 关闭兑换码显示弹窗
            function closeCouponModal() {
                closeDialog('coupon-modal');
            }
            // 切换兑换码类型显示
            
            // 修复前端生成兑换码函数
            
            // 删除兑换码
            
            
            document.getElementById('item-image')?.addEventListener('input', event => {
                updateItemImagePreview(event.target.value);
            });
            
            
            // 更新validateInput函数
            function validateInput(input, isUsername = false) {
                // 禁止空格和中文
                input.value = input.value.replace(/[\u4e00-\u9fa5\s]/g, '');

                // 如果是用户名字段，额外禁止@符号
                if (isUsername) {
                    input.value = input.value.replace(/@/g, '');
                }
            }
            // 在脚本部分添加密码修改功能
            function changePassword() {
                const currentPassword = document.getElementById('current-password').value;
                const newPassword = document.getElementById('new-password').value;
                const confirmPassword = document.getElementById('confirm-password').value;

                if (!currentPassword) {
                    showNotification('请输入当前密码', 'error');
                    return;
                }

                if (!newPassword || newPassword.length < 6) {
                    showNotification('新密码长度至少为6位', 'error');
                    return;
                }

                if (newPassword !== confirmPassword) {
                    showNotification('新密码和确认密码不一致', 'error');
                    return;
                }

                // 显示加载动画
                showLoading(true);

                // 调用修改密码API
                fetch(`${API_BASE_URL}`, {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({
                        method: 'changePassword',
                        name: userData.username,
                        oldpasswd: currentPassword,
                        newpasswd: newPassword,
                        session: getWebSession()
                    })
                })
                    .then(response => response.json())
                    .then(data => {
                        if (data.success) {
                            showNotification('密码修改成功！');
                            clearPasswordFields();
                        } else {
                            showNotification(data.message || '密码修改失败', 'error');
                        }
                    })
                    .catch(error => {
                        console.error('修改密码错误:', error);
                        showNotification('修改密码失败，请稍后再试', 'error');
                    })
                    .finally(() => {
                        showLoading(false);
                    });
            }

            // 清空密码字段
            function clearPasswordFields() {
                document.getElementById('current-password').value = '';
                document.getElementById('new-password').value = '';
                document.getElementById('confirm-password').value = '';
            }
            // 每日0点自动刷新签到状态
            function setupDailyReset() {
                if (dailyResetTimer) clearTimeout(dailyResetTimer);
                const offsetHours = Number(signData.timezoneOffsetHours) || 8;
                const offsetMs = offsetHours * 60 * 60 * 1000;
                const shiftedNow = new Date(Date.now() + offsetMs);
                const nextMidnight = Date.UTC(
                    shiftedNow.getUTCFullYear(),
                    shiftedNow.getUTCMonth(),
                    shiftedNow.getUTCDate() + 1
                );
                const timeUntilMidnight = Math.max(1000, nextMidnight - shiftedNow.getTime() + 1000);

                dailyResetTimer = setTimeout(() => {
                    // 以服务端签到状态为准，避免浏览器时区导致日期错位。
                    if (isLoggedIn) {
                        fetchUserSignData().then(() => {
                            if (currentPage === 'sign-page') {
                                showNotification('新的一天开始了！签到已刷新');
                            }
                        });
                    }

                    // 设置下一个0点的定时器
                    setupDailyReset();
                }, timeUntilMidnight);
            }
            // 初始化页面
            function initPage() {
    if (!userData.signStatus) userData.signStatus = { signedDays: [], consecutiveDays: 0, totalDays: 0 };
    if (!dailyResetInitialized) { dailyResetInitialized = true; setupDailyReset(); }
    if (currentPage === 'leaderboard') refreshLeaderboard();
    const remember = localStorage.getItem('rememberLogin');
    if (remember === 'true') {
        const savedUsername = localStorage.getItem('username');
        if (savedUsername) { document.getElementById('login-username').value = savedUsername; document.getElementById('remember').checked = true; }
    }
    document.getElementById('server-login-btn')?.addEventListener('click', serverLogin);
    document.getElementById('server-logout-btn')?.addEventListener('click', serverLogout);
}
            // 监听ESC键关闭弹窗
            document.addEventListener('keydown', (e) => {
                if (e.key === 'Escape') {
                    closeModal();
                    closeItemModal();
                    closeGenerateCouponModal();
                    closeCouponModal();
                }
                if (e.key === 'Tab') {
                    const modal = document.querySelector('.modal[aria-hidden="false"]');
                    if (!modal) return;
                    const controls = Array.from(modal.querySelectorAll('button:not(:disabled), input:not(:disabled), select:not(:disabled), textarea:not(:disabled), a[href], [tabindex="0"]'))
                        .filter(element => element.getClientRects().length);
                    const first = controls[0];
                    const last = controls[controls.length - 1];
                    if (e.shiftKey && document.activeElement === first) {
                        e.preventDefault();
                        last?.focus();
                    } else if (!e.shiftKey && document.activeElement === last) {
                        e.preventDefault();
                        first?.focus();
                    } else if (!modal.contains(document.activeElement)) {
                        e.preventDefault();
                        first?.focus();
                    }
                }
            });
            // 密码显示/隐藏切换
            function togglePasswordVisibility(button) {
                const input = document.getElementById('login-password');
                const icon = button.querySelector('i');
                if (!input || !icon) return;
                const show = input.type === 'password';
                input.type = show ? 'text' : 'password';
                icon.className = show ? 'fas fa-eye-slash' : 'fas fa-eye';
                button.setAttribute('aria-label', show ? '隐藏密码' : '显示密码');
            }

            
            
            // 登录功能（完整版）
            async function login() {
    const username = document.getElementById('login-username').value.trim();
    const password = document.getElementById('login-password').value;
    const remember = document.getElementById('remember').checked;
    const button = document.getElementById('player-login-btn');
    if (!username || !password) { showNotification('请输入用户名和密码', 'error'); return; }
    button.disabled = true;
    button.textContent = '登录中...';
    showLoading(true);
    try {
        const response = await fetch('/api/webLogin', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ username, password }) });
        const result = await response.json();
        if (!response.ok || !result.success || !result.session) throw new Error(result.message || '登录失败');
        localStorage.setItem('webSession', result.session);
        const displayUsername = result.username || username;
        userData.username = displayUsername;
        if (remember) { localStorage.setItem('rememberLogin', 'true'); localStorage.setItem('username', displayUsername); }
        else { localStorage.removeItem('rememberLogin'); localStorage.removeItem('username'); }
        isLoggedIn = true;
        document.getElementById('login-page').style.display = 'none';
        document.getElementById('main-system').style.display = 'flex';
        document.getElementById('username-display').textContent = displayUsername;
        document.getElementById('account-username').textContent = displayUsername;
        await fetchUserInfo();
        document.getElementById('user-center-kicker').textContent = 'PLAYER OVERVIEW';
        document.getElementById('user-center-title').textContent = '账户概览';
        document.getElementById('user-center-description').textContent = '查看游戏连接状态与账户数据';
        document.getElementById('user-status').textContent = '在线';
        switchPage('user-center');
        showNotification('登录成功');
    } catch (error) {
        showNotification(error.message || '登录失败，请稍后再试', 'error');
    } finally { button.disabled = false; button.textContent = '玩家登录'; showLoading(false); }
}

            // 打开注册/忘记密码弹窗
            function openModal(type) {
                switchAuthTab(type);
                openDialog('auth-modal', `#${type}-form .input`);
            }

            // 关闭弹窗
            function closeModal() {
                const modal = document.getElementById('auth-modal');
                if (!modal || modal.getAttribute('aria-hidden') !== 'false') return;
                closeDialog('auth-modal');
                // 重置表单
                document.getElementById('register-form').style.display = 'grid';
                document.getElementById('forgot-form').style.display = 'none';
                document.getElementById('verification-form').style.display = 'none';
                stopVerificationTimer();
            }

            // 切换认证标签页
            function switchAuthTab(tab) {
                stopVerificationTimer();
                document.getElementById('auth-tabs').hidden = false;
                document.getElementById('auth-modal-icon').className = tab === 'register' ? 'fas fa-user-plus' : 'fas fa-key';
                document.getElementById('register-tab').classList.remove('active');
                document.getElementById('forgot-tab').classList.remove('active');
                document.getElementById('register-tab').setAttribute('aria-selected', 'false');
                document.getElementById('forgot-tab').setAttribute('aria-selected', 'false');
                const activeTab = document.getElementById(tab + '-tab');
                activeTab.classList.add('active');
                activeTab.setAttribute('aria-selected', 'true');

                document.getElementById('register-form').style.display = 'none';
                document.getElementById('forgot-form').style.display = 'none';
                document.getElementById('verification-form').style.display = 'none';

                if (tab === 'register') {
                    document.getElementById('modal-title').textContent = '注册新账号';
                    document.getElementById('register-form').style.display = 'grid';
                } else {
                    document.getElementById('modal-title').textContent = '忘记密码';
                    document.getElementById('forgot-form').style.display = 'grid';
                }
            }

            function showVerificationForm() {
                document.getElementById('auth-tabs').hidden = true;
                document.getElementById('modal-title').textContent = '验证邮箱';
                document.getElementById('auth-modal-icon').className = 'fas fa-envelope';
                document.getElementById('verification-form').style.display = 'grid';
                document.getElementById('verification-code').value = '';
                document.getElementById('verification-code').focus();
            }

            // 注册功能（添加防重复点击）
            async function register() {
                // 防止重复提交
                if (isRegistering) return;
                isRegistering = true;

                const registerButton = document.querySelector('#register-form .login-button');
                const originalText = registerButton.textContent;
                registerButton.textContent = '发送中...';
                registerButton.disabled = true;

                const username = document.getElementById('reg-username').value;
                const password = document.getElementById('reg-password').value;
                const email = document.getElementById('reg-email').value;

                if (!username || !password || !email) {
                    showNotification('请填写所有字段！', 'error');
                    isRegistering = false;
                    registerButton.textContent = originalText;
                    registerButton.disabled = false;
                    return;
                }

                try {
                    // 调用注册API
                    const response = await fetch(`${API_BASE_URL}`, {
                        method: 'POST',
                        headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify({ method: 'regist', name: username, passwd: password, email })
                    });
                    const result = await response.text();

                    if (result.includes('验证码已发送')) {
                        showNotification('验证码已发送至您的邮箱，请查收！');
                        // 显示验证码表单
                        document.getElementById('register-form').style.display = 'none';
                        showVerificationForm();
                        document.getElementById('verify-button').onclick = () => verifyRegistration(email);
                        startVerificationTimer();
                    } else {
                        showNotification(result, 'error');
                    }
                } catch (error) {
                    console.error('注册错误:', error);
                    showNotification('注册失败，请稍后再试', 'error');
                } finally {
                    isRegistering = false;
                    registerButton.textContent = originalText;
                    registerButton.disabled = false;
                }
            }

            // 验证注册
            async function verifyRegistration(email) {
                const code = document.getElementById('verification-code').value;

                if (!code) {
                    showNotification('请输入验证码！', 'error');
                    return;
                }

                try {
                    // 调用验证注册API
                    const response = await fetch(apiUrl({ method: 'verifyRegist', email, code }));
                    // 后端 verifyRegist 返回 JSON：{success,message}
                    const result = await response.json().catch(async () => ({
                        success: false,
                        message: await response.text()
                    }));

                    if (result && result.success) {
                        showNotification(result.message || '注册成功！');
                        closeModal();
                    } else {
                        showNotification((result && result.message) ? result.message : '验证失败', 'error');
                    }
                } catch (error) {
                    console.error('验证注册错误:', error);
                    showNotification('验证失败，请稍后再试', 'error');
                }
            }

            // 忘记密码功能（添加防重复点击）
            async function resetPassword() {
                // 防止重复提交
                if (isResettingPassword) return;
                isResettingPassword = true;

                const resetButton = document.querySelector('#forgot-form .login-button');
                const originalText = resetButton.textContent;
                resetButton.textContent = '发送中...';
                resetButton.disabled = true;

                const username = document.getElementById('forgot-username').value;
                const email = document.getElementById('forgot-email').value;
                const newPassword = document.getElementById('forgot-password').value;

                if (!username || !email || !newPassword) {
                    showNotification('请填写所有字段！', 'error');
                    isResettingPassword = false;
                    resetButton.textContent = originalText;
                    resetButton.disabled = false;
                    return;
                }

                try {
                    // 调用重置密码API
                    const response = await fetch(`${API_BASE_URL}`, {
                        method: 'POST',
                        headers: { 'Content-Type': 'application/json' },
                        // 增加 email 便于后端校验；验证码仍会发送到该用户的注册邮箱
                        body: JSON.stringify({ method: 'repasswd', name: username, newpasswd: newPassword, email })
                    });
                    const result = await response.text();

                    if (result.includes('验证码已发送')) {
                        showNotification('验证码已发送至您的邮箱，请查收！');
                        // 显示验证码表单
                        document.getElementById('forgot-form').style.display = 'none';
                        showVerificationForm();
                        // verifyCode 需要 email + code
                        document.getElementById('verify-button').onclick = () => verifyPasswordReset(email);
                        startVerificationTimer();
                    } else {
                        showNotification(result, 'error');
                    }
                } catch (error) {
                    console.error('重置密码错误:', error);
                    showNotification('重置密码失败，请稍后再试', 'error');
                } finally {
                    isResettingPassword = false;
                    resetButton.textContent = originalText;
                    resetButton.disabled = false;
                }
            }

            // 验证密码重置
            async function verifyPasswordReset(email) {
                const code = document.getElementById('verification-code').value;

                if (!code) {
                    showNotification('请输入验证码！', 'error');
                    return;
                }

                try {
                    // 调用验证API
                    const response = await fetch(apiUrl({ method: 'verifyCode', email, code }));
                    const result = await response.text();

                    if (result.includes('密码修改成功')) {
                        showNotification('密码重置成功！');
                        closeModal();
                    } else {
                        showNotification(result, 'error');
                    }
                } catch (error) {
                    console.error('验证密码重置错误:', error);
                    showNotification('验证失败，请稍后再试', 'error');
                }
            }

            // 开始验证码倒计时
            let verificationTimer = null;
            function startVerificationTimer() {
                // 先清理旧的倒计时，避免多个定时器同时刷新同一个元素
                if (verificationTimer) clearInterval(verificationTimer);
                let timeLeft = 300; // 5分钟
                const countdownElement = document.getElementById('verification-countdown');
                countdownElement.textContent = '验证码有效时间：05:00';

                verificationTimer = setInterval(() => {
                    const minutes = Math.floor(timeLeft / 60);
                    const seconds = timeLeft % 60;

                    countdownElement.textContent = `验证码有效时间：${minutes.toString().padStart(2, '0')}:${seconds.toString().padStart(2, '0')}`;

                    if (timeLeft <= 0) {
                        clearInterval(verificationTimer);
                        verificationTimer = null;
                        countdownElement.textContent = '验证码已过期';
                    }

                    timeLeft--;
                }, 1000);
            }

            function stopVerificationTimer() {
                if (verificationTimer) clearInterval(verificationTimer);
                verificationTimer = null;
            }

            // 网页授权成功后等待插件确认玩家入服，避免把“已授权”误显示为已在线。
            async function serverLogin() {
                try {
                    showLoading(true);
                    const session = getWebSession();
                    if (!session) {
                        showNotification('会话已过期，请重新登录', 'error');
                        return;
                    }
                    const response = await fetch(apiUrl({ method: 'login', name: userData.username, session }));
                    const result = await response.text();

                    // 更灵活的成功判断
                    if (result.includes('成功') || result.includes('确认') || result.includes('延长')) {
                        userData.serverStatus = 'authorized';
                        updateServerStatus(userData.serverStatus);

                        showNotification('服务器授权成功，请进入服务器完成登录', 'success');

                        // 稍后刷新完整用户信息
                        setTimeout(() => {
                            fetchUserInfo();
                        }, 500);
                    } else {
                        showNotification('服务器登录失败: ' + result, 'error');
                    }
                } catch (error) {
                    showNotification('服务器登录失败: ' + error.message, 'error');
                } finally {
                    showLoading(false);
                }
            }

            // 修改：服务器登出功能 - 立即更新状态
            async function serverLogout() {
                try {
                    showLoading(true);
                    const session = getWebSession();
                    if (!session) {
                        showNotification('会话已过期，请重新登录', 'error');
                        return;
                    }
                    const response = await fetch(apiUrl({ method: 'logout', name: userData.username, session }));
                    const result = await response.text();

                    if (result.includes('退出')) {
                        // 立即更新本地状态
                        userData.serverStatus = 'offline';
                        updateServerStatus(userData.serverStatus);

                        showNotification('服务器登出成功！');

                        // 稍后刷新完整用户信息
                        setTimeout(() => {
                            fetchUserInfo();
                        }, 500);
                    } else {
                        showNotification('服务器登出失败: ' + result, 'error');
                    }
                } catch (error) {
                    showNotification('服务器登出失败: ' + error.message, 'error');
                } finally {
                    showLoading(false);
                }
            }
            // 修改 fetchUserInfo 函数
            async function fetchUserInfo() {
                 
                try {
                    const session = getWebSession();
                    // 强制从服务器获取最新数据（不使用缓存）
                    const response = await fetch(`/api/userInfo?username=${encodeURIComponent(userData.username)}&session=${encodeURIComponent(session)}&t=${new Date().getTime()}`);
                    const data = await response.json();
                    if (!response.ok || !data.success) {
                        throw new Error(data.message || `获取用户信息失败（HTTP ${response.status}）`);
                    }

                    // 更新用户数据
                    if (data.username) {
                        userData.username = data.username;
                        localStorage.setItem('username', data.username);
                        document.getElementById('username-display').textContent = data.username;
                        document.getElementById('account-username').textContent = data.username;
                    }
                    userData.joinDate = data.joinDate || '';
                    userData.onlineTime = data.onlineTime || 0;
                    userData.lastLogin = data.lastLogin || '';
                    userData.serverStatus = data.serverStatus || 'offline';
                    userData.points = data.points || 0;

                    // 更新页面显示
                    document.getElementById('user-points').textContent = userData.points;
                    document.getElementById('join-date').textContent = data.joinDate || '未知';
                    document.getElementById('last-login-date').textContent = data.lastLogin || '从未登录';
                    document.getElementById('online-time-total').textContent = formatTime(data.onlineTime || 0);

                    // 更新服务器状态显示
                    updateServerStatus(userData.serverStatus);

                    // 更新头像
                    if (data.avatarPath) {
                        document.getElementById('avatar-preview').src = data.avatarPath;
                        document.getElementById('user-avatar-img').src = data.avatarPath;
                        userData.avatar = data.avatarPath;
                    }

                    // 获取签到历史
                    try {
                    const signResponse = await fetch(`/api/signHistory?t=${Date.now()}`, {
                        cache: 'no-store',
                        headers: { 'X-Web-Session': session }
                    });
                        const signDataResponse = await signResponse.json();

                        if (signResponse.ok && signDataResponse.success) {
                            // 更新全局签到数据
                            applySignData(signDataResponse);

                            // 强制更新UI
                            updateSignButton();
                            if (currentPage === 'sign-page') {
                                generateCalendar();
                            }
                        }
                    } catch (error) {
                        console.error('获取签到历史失败:', error);
                    }
                    updateUserCenterSummary();
                } catch (error) {
                    console.error('获取用户信息失败:', error);
                }
            }

            function updateUserCenterSummary() {
                const points = document.getElementById('center-user-points');
                const streak = document.getElementById('center-sign-streak');
                const total = document.getElementById('center-sign-total');
                if (points) points.textContent = Number(signData.points ?? userData.points) || 0;
                if (streak) streak.textContent = Number(signData.consecutiveDays) || 0;
                if (total) total.textContent = Number(signData.totalDays) || 0;
            }

            async function refreshUserCenter() {
                const button = document.getElementById('user-center-refresh');
                if (button) button.disabled = true;
                try {
                    {
                        await fetchUserInfo();
                    }
                    showNotification('用户中心已刷新');
                } catch (error) {
                    console.error('刷新用户中心失败:', error);
                    showNotification('刷新用户中心失败', 'error');
                } finally {
                    if (button) button.disabled = false;
                }
            }
            // 根据网页授权和插件在线租约分别显示状态。
            function updateServerStatus(status) {
                const icon = document.getElementById('server-status-icon');
                const text = document.getElementById('server-status-text');
                const container = document.getElementById('server-status-container');

                // 清除所有可能的状态类
                container.className = 'server-status';

                const isOnline = status === 'online' ||
                    status === '已登录' ||
                    status === '登录成功' ||
                    status === 'online';

                if (isOnline) {
                    icon.className = 'fas fa-check-circle';
                    icon.style.color = 'var(--success)'; // 跟随当前主题的在线状态色。
                    container.classList.add('status-online');
                    text.textContent = '已登录';
                    text.style.color = 'var(--success)';

                    // 更新按钮状态
                    document.getElementById('server-login-btn').style.display = 'none';
                    document.getElementById('server-logout-btn').style.display = 'block';
                } else if (status === 'authorized' || status === '已授权') {
                    icon.className = 'fas fa-hourglass-half';
                    icon.style.color = 'var(--warning, #d97706)';
                    container.classList.add('status-authorized');
                    text.textContent = '已授权，等待进入服务器';
                    text.style.color = 'var(--warning, #d97706)';
                    document.getElementById('server-login-btn').style.display = 'block';
                    document.getElementById('server-logout-btn').style.display = 'block';
                } else {
                    icon.className = 'fas fa-times-circle';
                    icon.style.color = 'var(--danger)'; // 跟随当前主题的离线状态色。
                    container.classList.add('status-offline');
                    text.textContent = '未登录';
                    text.style.color = 'var(--danger)';

                    // 更新按钮状态
                    document.getElementById('server-login-btn').style.display = 'block';
                    document.getElementById('server-logout-btn').style.display = 'none';
                }

                // 更新其他信息
                document.getElementById('server-account').textContent = userData.username;
                document.getElementById('online-time').textContent = formatTime(userData.onlineTime);
                document.getElementById('last-login').textContent = userData.lastLogin || '从未登录';
            }
            // 修改：格式化时间显示 - 更精确
            function formatTime(ms) {
                if (!ms) return "0分钟";

                const seconds = Math.floor(ms / 1000);
                const minutes = Math.floor(seconds / 60);
                const hours = Math.floor(minutes / 60);
                const days = Math.floor(hours / 24);

                // 处理超大时间值
                if (days > 365) {
                    const years = Math.floor(days / 365);
                    const remainingDays = days % 365;
                    return `${years}年${remainingDays}天`;
                }

                if (days > 0) {
                    const remainingHours = hours % 24;
                    return `${days}天${remainingHours}小时`;
                }
                if (hours > 0) {
                    const remainingMinutes = minutes % 60;
                    return `${hours}小时${remainingMinutes}分钟`;
                }
                if (minutes > 0) {
                    return `${minutes}分钟`;
                }
                return `${seconds}秒`;
            }
            // 修改refreshPoints函数，从排行榜获取最新积分
            async function refreshPoints() {
                try {
                    // 调用排行榜API获取最新数据
                    const response = await fetch(`/api/leaderboard?session=${encodeURIComponent(getWebSession())}&t=${new Date().getTime()}`);
                    const data = await response.json();

                    if (data.success) {
                        // 在排行榜中查找当前用户的积分
                        const currentUser = data.players.find(player => samePlayerName(player.name, userData.username));
                        if (currentUser) {
                            // 更新本地数据
                            userData.points = currentUser.points;
                            document.getElementById('user-points').textContent = currentUser.points;
                        }
                    }
                } catch (error) {
                    console.error('刷新积分错误:', error);
                }
            }
            // 服务器状态检测统一由后端 /api/serverStatus 发起，前端只展示后端结果。

            async function getPortConfig() {
                // 安全：浏览器端无法读取本地文件，端口信息从后端获取
                try {
                    const resp = await fetch('/ports', { cache: 'no-store' });
                    if (!resp.ok) throw new Error('Failed to fetch ports');
                    return await resp.json();
                } catch (error) {
                    console.warn('无法获取端口配置，使用默认值:', error);
                    return { httpPort: 80, httpsPort: 443, httpsEnabled: false };
                }
            }

            // 修改前端服务器状态刷新函数
            async function refreshServerStatus() {
                try {
                    showLoading(true);
                    const serverStatusGrid = document.getElementById('server-status-grid');
                    serverStatusGrid.innerHTML = '';

                    // 调用后端API获取服务器状态
                    const response = await fetch('/api/serverStatus');
                    const data = await response.json();

                    if (!data.success) {
                        throw new Error('获取服务器状态失败');
                    }

                    serverStatusGrid.innerHTML = '';

                    data.servers.forEach(server => {
                        const card = document.createElement('div');
                        card.className = `server-card ${server.online ? '' : 'offline'}`;

                        const header = document.createElement('div');
                        header.className = 'server-header';

                        const n = document.createElement('div');
                        n.className = 'server-name';
                        safeSetText(n, server.name);

                        const st = document.createElement('div');
                        st.className = `server-status ${server.online ? 'status-online' : 'status-offline'}`;
                        st.innerHTML = `<i class="fas ${server.online ? 'fa-check-circle' : 'fa-times-circle'}"></i>`;
                        st.appendChild(document.createTextNode(server.online ? '在线' : '离线'));

                        header.appendChild(n);
                        header.appendChild(st);

                        const info = document.createElement('div');
                        info.className = 'server-info';

                        const addr = document.createElement('div');
                        addr.className = 'server-address';
                        safeSetText(addr, `${server.host}:${server.port}`);
                        info.appendChild(addr);

                        if (server.source || server.latencyMs) {
                            const probe = document.createElement('div');
                            probe.className = 'server-address';
                            const sourceText = server.source ? `检测: ${server.source}` : '检测: 未知';
                            const latencyText = Number.isFinite(Number(server.latencyMs)) ? ` · ${Math.round(Number(server.latencyMs))}ms` : '';
                            safeSetText(probe, `${sourceText}${latencyText}`);
                            info.appendChild(probe);
                        }

                        if (server.online) {
                            const stats = document.createElement('div');
                            stats.className = 'server-stats';

                            const p1 = document.createElement('div');
                            p1.className = 'stat-item';
                            const l1 = document.createElement('span'); l1.className = 'stat-label'; safeSetText(l1, '在线玩家');
                            const v1 = document.createElement('span'); v1.className = 'stat-value';
                            safeSetText(v1, `${server.players?.online ?? 0}/${server.players?.max ?? 0}`);
                            p1.appendChild(l1); p1.appendChild(v1);

                            const p2 = document.createElement('div');
                            p2.className = 'stat-item';
                            const l2 = document.createElement('span'); l2.className = 'stat-label'; safeSetText(l2, '版本');
                            const v2 = document.createElement('span'); v2.className = 'stat-value'; safeSetText(v2, server.version || '');
                            p2.appendChild(l2); p2.appendChild(v2);

                            stats.appendChild(p1);
                            stats.appendChild(p2);
                            info.appendChild(stats);

                            if (server.motd) {
                                const motd = document.createElement('div');
                                motd.className = 'server-motd';
                                safeSetText(motd, server.motd);
                                info.appendChild(motd);
                            }
                        } else if (server.error) {
                            const stats = document.createElement('div');
                            stats.className = 'server-stats';
                            const it = document.createElement('div');
                            it.className = 'stat-item';
                            const lab = document.createElement('span'); lab.className = 'stat-label'; safeSetText(lab, '错误信息');
                            const val = document.createElement('span'); val.className = 'stat-value'; safeSetText(val, server.error);
                            it.appendChild(lab); it.appendChild(val);
                            stats.appendChild(it);
                            info.appendChild(stats);
                        }

                        card.appendChild(header);
                        card.appendChild(info);
                        serverStatusGrid.appendChild(card);
                    });

                    showNotification('服务器状态刷新完成！');
                } catch (error) {
                    console.error('刷新服务器状态错误:', error);
                    showNotification('刷新服务器状态失败', 'error');
                } finally {
                    showLoading(false);
                }
            }

            

            
            

            
            
            // 添加删除用户函数
            
            // 添加邮箱编辑相关函数
            

            

            
            function getSignDateString(date = new Date()) {
                const offsetHours = Number(signData.timezoneOffsetHours) || 8;
                const shifted = new Date(date.getTime() + offsetHours * 60 * 60 * 1000);
                return shifted.toISOString().slice(0, 10);
            }

            function getAuthoritativeSignDate() {
                return signData.today || getSignDateString();
            }

            function applySignData(data) {
                const previousOffset = Number(signData.timezoneOffsetHours) || 8;
                signData = {
                    signedDays: Object.keys(data.signHistory || {}),
                    consecutiveDays: Number(data.consecutiveDays) || 0,
                    totalDays: Number(data.totalDays) || 0,
                    lastSign: data.lastSign || "",
                    points: Number(data.points ?? data.totalPoints) || 0,
                    today: data.today || getSignDateString(),
                    timezoneOffsetHours: Number.isFinite(Number(data.timezoneOffsetHours))
                        ? Number(data.timezoneOffsetHours)
                        : (Number(signData.timezoneOffsetHours) || 8)
                };
                userData.points = signData.points;
                const points = document.getElementById('user-points');
                if (points) points.textContent = signData.points;
                updateUserCenterSummary();
                if (dailyResetInitialized && signData.timezoneOffsetHours !== previousOffset) {
                    setupDailyReset();
                }
            }

            // 以服务端返回的日期为锚点生成本周日历。
            function generateCalendar() {
                const calendar = document.getElementById('calendar');
                if (!calendar) return;
                calendar.innerHTML = '';

                // 使用周一到周日的顺序
                const weekdays = ['一', '二', '三', '四', '五', '六', '日'];
                weekdays.forEach(day => {
                    const header = document.createElement('div');
                    header.className = 'calendar-header';
                    header.textContent = day;
                    calendar.appendChild(header);
                });

                const todayStr = getAuthoritativeSignDate();
                const [year, month, day] = todayStr.split('-').map(Number);
                const todayUtc = new Date(Date.UTC(year, month - 1, day));
                const dayOfWeek = todayUtc.getUTCDay();
                const mondayTime = todayUtc.getTime() - (dayOfWeek === 0 ? 6 : dayOfWeek - 1) * 86400000;

                // 生成一周的日期（周一到周日）
                for (let i = 0; i < 7; i++) {
                    const date = new Date(mondayTime + i * 86400000);
                    const dateStr = date.toISOString().slice(0, 10);

                    const dayElement = document.createElement('div');
                    dayElement.className = 'calendar-day';

                    // 标记今天
                    if (dateStr === todayStr) {
                        dayElement.classList.add('today');
                    }

                    // 标记已签到 / 未签到
                    const isSigned = (signData.signedDays && signData.signedDays.includes(dateStr));
                    if (isSigned) {
                        dayElement.classList.add('signed');
                    } else {
                        // 标记未签到（仅今天之前的日期）
                        if (dateStr < todayStr) {
                            dayElement.classList.add('unsigned');
                        }
                    }
                    dayElement.textContent = date.getUTCDate();
                    calendar.appendChild(dayElement);
                }

                // 更新签到按钮状态
                updateSignButton();
            }
            // 更新签到按钮状态
            function updateSignButton() {
                const signButton = document.getElementById('sign-button');
                if (!signButton) return;

                const todayStr = getAuthoritativeSignDate();
                const signedToday = signData.signedDays && signData.signedDays.includes(todayStr);

                if (signRequestPending) {
                    signButton.textContent = '签到处理中…';
                    signButton.disabled = true;
                    signButton.classList.add('disabled');
                } else if (signedToday) {
                    signButton.textContent = '今日已签到';
                    signButton.disabled = true;
                    signButton.classList.add('disabled');

                    // 显示连续签到天数
                    if (signData.consecutiveDays > 1) {
                        signButton.textContent += ` (连续${signData.consecutiveDays}天)`;
                    }
                } else {
                    signButton.textContent = '立即签到';
                    signButton.disabled = false;
                    signButton.classList.remove('disabled');

                    // 显示连续签到提示
                    if (signData.consecutiveDays > 0) {
                        signButton.textContent = `签到 (保持连续${signData.consecutiveDays}天)`;
                    }
                }
            }
            // 获取用户签到数据
            async function fetchUserSignData() {
                try {
                    const session = getWebSession();
                    const response = await fetch(`/api/signHistory?t=${Date.now()}`, {
                        cache: 'no-store',
                        headers: { 'X-Web-Session': session }
                    });
                    const data = await response.json();

                    if (!response.ok || !data.success) {
                        throw new Error(data.message || `获取签到数据失败（HTTP ${response.status}）`);
                    }

                    applySignData(data);
                    generateCalendar();
                    updateSignButton();
                } catch (error) {
                    console.error('获取签到数据失败:', error);
                    showNotification('获取签到数据失败', 'error');
                    throw error;
                }
            }
            // 修改点3：修改排行榜刷新函数，存储最新数据
            async function refreshLeaderboard() {
                try {
                    showLoading(true);
                    const response = await fetch(`/api/leaderboard?session=${encodeURIComponent(getWebSession())}&t=${new Date().getTime()}`);
                    const data = await response.json();

                    if (data.success) {
                        // 存储到全局变量
                        latestLeaderboardData = data.players;
                        renderLeaderboard(latestLeaderboardData);
                    } else {
                        showNotification('获取排行榜数据失败', 'error');
                    }
                } catch (error) {
                    console.error('刷新排行榜错误:', error);
                    showNotification('刷新排行榜失败', 'error');
                } finally {
                    showLoading(false);
                }
            }

            // 新增函数：渲染排行榜
            function renderLeaderboard(players) {
                const tbody = document.getElementById('leaderboard-body');
                tbody.innerHTML = '';

                (players || []).forEach((player, index) => {
                    const row = document.createElement('tr');

                    const rankCell = document.createElement('td');
                    rankCell.className = 'rank';
                    safeSetText(rankCell, index + 1);

                    const playerCell = document.createElement('td');
                    const info = document.createElement('div');
                    info.className = 'player-info';

                    const avatar = document.createElement('img');
                    avatar.className = 'player-avatar';
                    avatar.src = safeImageSrc(player.avatar, DEFAULT_AVATAR_IMAGE);
                    avatar.alt = player.name ? String(player.name) : '';
                    avatar.referrerPolicy = 'no-referrer';
                    setImageFallback(avatar, DEFAULT_AVATAR_IMAGE);

                    const playerName = document.createElement('span');
                    safeSetText(playerName, player.name);

                    info.appendChild(avatar);
                    info.appendChild(playerName);
                    playerCell.appendChild(info);

                    const onlineCell = document.createElement('td');
                    safeSetText(onlineCell, formatTime(player.onlineTime || 0));

                    const lastLoginCell = document.createElement('td');
                    safeSetText(lastLoginCell, player.lastLogin || '从未登录');

                    const signCell = document.createElement('td');
                    safeSetText(signCell, player.consecutiveDays || 0);

                    const pointsCell = document.createElement('td');
                    safeSetText(pointsCell, player.points || 0);

                    row.appendChild(rankCell);
                    row.appendChild(playerCell);
                    row.appendChild(onlineCell);
                    row.appendChild(lastLoginCell);
                    row.appendChild(signCell);
                    row.appendChild(pointsCell);
                    tbody.appendChild(row);
                });
            }
            // 修改点4：添加账户设置刷新功能
            async function refreshAccountSettings() {
                try {
                    showLoading(true);

                    // 刷新用户信息
                    await fetchUserInfo();

                    // 刷新排行榜数据
                    await refreshLeaderboard();

                    // 从排行榜获取当前用户积分
                    const currentUser = latestLeaderboardData.find(player => samePlayerName(player.name, userData.username));
                    if (currentUser) {
                        userData.points = currentUser.points;
                        document.getElementById('user-points').textContent = currentUser.points;
                    }

                    showNotification('账户信息已刷新！');
                } catch (error) {
                    console.error('刷新账户设置失败:', error);
                    showNotification('刷新账户设置失败', 'error');
                } finally {
                    showLoading(false);
                }
            }
            // 修改签到函数
            async function signIn() {
                if (signRequestPending) return;
                signRequestPending = true;
                updateSignButton();
                try {
                    showLoading(true);

                    const response = await fetch('/api/sign', {
                        method: 'POST',
                        headers: {
                            'Content-Type': 'application/json',
                            'X-Web-Session': getWebSession()
                        },
                        body: JSON.stringify({ username: userData.username })
                    });
                    const data = await response.json();

                    if (response.ok && data.success) {
                        applySignData(data);
                        generateCalendar();

                        // 显示成功消息
                        let message = `签到成功！获得${data.pointsAwarded || 0}积分`;
                        if (data.consecutiveDays > 1) {
                            message += `，已连续签到 ${data.consecutiveDays} 天！`;
                        }
                        showNotification(message);
                    } else {
                        if (data.today) applySignData(data);
                        showNotification(data.message || `签到失败（HTTP ${response.status}）`, 'error');
                    }
                } catch (error) {
                    console.error('签到失败:', error);
                    showNotification('签到失败: ' + error.message, 'error');
                } finally {
                    signRequestPending = false;
                    updateSignButton();
                    showLoading(false);
                }
            }
            // 切换页面
            function switchPage(page) {
                if (currentPage === 'shop' && page !== 'shop') {
                    stopShopAutoRefresh();
                }

                
                
                
                
                if (page !== 'user-center' && refreshTimer) {
                    clearInterval(refreshTimer);
                    refreshTimer = null;
                }
                // 更新当前页面
                currentPage = page;
                // 特殊页面初始化
                if (page === 'server-status') {
                    refreshServerStatus();
                } else if (page === 'sign-page') {
                    // 每次进入签到页面时刷新签到数据
                    fetchUserSignData();
                } else if (page === 'shop') {
                    refreshShop();
                    startShopAutoRefresh();
                } else if (page === 'user-center') {
                    fetchUserInfo();
                    if (refreshTimer) clearInterval(refreshTimer);
                    refreshTimer = setInterval(fetchUserInfo, 60000); // 确保用户中心刷新数据
                } else if (page === 'leaderboard') {
                    refreshLeaderboard();
                }
                // 更新导航项激活状态
                document.querySelectorAll('.nav-item').forEach(item => {
                    item.classList.remove('active');
                    if (item.getAttribute('data-page') === page) {
                        item.classList.add('active');
                    }
                });

                // 更新页面标题
                document.getElementById('page-title').textContent = getPageTitle(page);

                // 隐藏所有页面内容
                document.querySelectorAll('.page-content').forEach(content => {
                    content.style.display = 'none';
                });

                // 显示当前页面内容
                document.getElementById(page).style.display = 'block';

                if (page === 'account') {  // 添加账户设置页面的自动刷新
                    refreshAccountSettings();
                }
            }

            // 获取页面标题
            function getPageTitle(page) {
                const titles = {
                    'user-center': '用户中心',
                    'sign-page': '签到系统',

                    'sign': '签到系统',
                    'leaderboard': '排行榜',
                    'shop': '积分商城',
                    'server-status': '服务器状态',
                    'account': '账户设置',
                    'support': 'LittleSkin皮肤站'
                };
                return titles[page] || '嘎嘣脆の小服';
            }

            // 退出登录
            function logout() {
                isLoggedIn = false;
                
                
                stopShopAutoRefresh();
                if (refreshTimer) clearInterval(refreshTimer);
                refreshTimer = null;
                if (dailyResetTimer) clearTimeout(dailyResetTimer);
                dailyResetTimer = null;

                // 清理会话与记住登录信息
                try {
                    localStorage.removeItem('webSession');
                    
                    localStorage.removeItem('rememberLogin');
                    localStorage.removeItem('username');
                } catch (e) { /* ignore */ }

                userData = {};
                document.getElementById('main-system').style.display = 'none';
                document.getElementById('login-page').style.display = 'flex';
                showNotification('您已退出登录');

                // 刷新页面，确保所有状态重置
                location.reload();
            }


            // 头像上传
            // 从后端读取限制，避免“前端允许但后端拒绝/或反之”的不一致
            window.__avatarMaxBytes = window.__avatarMaxBytes || (9 * 1024 * 1024); // 兜底值
            window.__itemImageMaxBytes = window.__itemImageMaxBytes || (5 * 1024 * 1024);
            window.__couponItemMaxAmount = window.__couponItemMaxAmount || 2304;
            (function syncPublicLimits() {
                fetch('/api/publicConfig')
                    .then(r => r.json())
                    .then(cfg => {
                        const avatarLimit = cfg && cfg.success && cfg.limits && cfg.limits.maxAvatarBytes;
                        const itemImageLimit = cfg && cfg.success && cfg.limits && cfg.limits.maxItemImageBytes;
                        const couponItemLimit = cfg && cfg.success && cfg.limits && cfg.limits.maxCouponItemAmount;
                        if (typeof avatarLimit === 'number' && avatarLimit > 0) window.__avatarMaxBytes = avatarLimit;
                        if (typeof itemImageLimit === 'number' && itemImageLimit > 0) window.__itemImageMaxBytes = itemImageLimit;
                        if (typeof couponItemLimit === 'number' && couponItemLimit > 0) window.__couponItemMaxAmount = couponItemLimit;
                    })
                    .catch(() => { /* ignore */ });
            })();

            document.getElementById('avatar-input').addEventListener('change', function (e) {
                const file = e.target.files[0];
                if (!file) return;

                const limit = window.__avatarMaxBytes || (9 * 1024 * 1024);
                if (file.size > limit) {
                    const mb = (limit / (1024 * 1024));
                    showNotification(`头像图片不能超过${mb.toFixed(mb >= 10 ? 0 : 1)}MB`, 'error');
                    this.value = ''; // 清空文件选择
                    return;
                }

                const reader = new FileReader();
                reader.onload = function (e) {
                    const base64Image = e.target.result;

                    // 调用后端API上传头像
                    fetch('/api/uploadAvatar', {
                        method: 'POST',
                        headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify({
                            username: userData.username,
                            avatar: base64Image,
                            session: getWebSession()
                        })
                    })
                        .then(response => response.json())
                        .then(data => {
                            if (data.success) {
                                // 更新前端显示
                                document.getElementById('avatar-preview').src = data.avatarUrl;
                                document.getElementById('user-avatar-img').src = data.avatarUrl;
                                showNotification('头像更新成功！');
                            } else {
                                showNotification(data.message || '头像上传失败', 'error');
                            }
                        })
                        .catch(() => showNotification('头像上传失败', 'error'));
                };
                reader.readAsDataURL(file);
            });

            // 初始化页面
            window.addEventListener('load', () => { initPage(); document.body.classList.remove('loading'); });


            // 登录与导航的键盘操作；主题切换统一由 theme.js 处理。
            (function () {
                document.addEventListener('DOMContentLoaded', () => {
                    // 登录表单支持回车提交。
                    const triggerLogin = (e) => {
                        if (e.key === 'Enter') {
                            e.preventDefault();
                            const btn = document.getElementById('player-login-btn');
                            if (btn) btn.click();
                        }
                    };

                    const pw = document.getElementById('login-password');
                    const un = document.getElementById('login-username');
                    const totp = document.getElementById('login-totp');

                    if (pw) pw.addEventListener('keydown', triggerLogin);
                    if (un) un.addEventListener('keydown', triggerLogin);
                    if (totp) totp.addEventListener('keydown', triggerLogin);

                    const enterActions = {
                        'register-form': '#register-form .login-button',
                        'forgot-form': '#forgot-form .login-button',
                        'verification-form': '#verify-button'
                    };
                    document.querySelectorAll('#register-form .input, #forgot-form .input, #verification-form .input').forEach(input => {
                        input.addEventListener('keydown', event => {
                            if (event.key !== 'Enter' || event.isComposing) return;
                            event.preventDefault();
                            document.querySelector(enterActions[input.closest('.auth-form')?.id])?.click();
                        });
                    });

                    document.querySelectorAll('.nav-item[onclick]').forEach((item) => {
                        if (!item.hasAttribute('role')) item.setAttribute('role', 'button');
                        if (!item.hasAttribute('tabindex')) item.setAttribute('tabindex', '0');
                        item.addEventListener('keydown', (event) => {
                            if (event.key === 'Enter' || event.key === ' ') {
                                event.preventDefault();
                                item.click();
                            }
                        });
                    });

                });
            })();

        
