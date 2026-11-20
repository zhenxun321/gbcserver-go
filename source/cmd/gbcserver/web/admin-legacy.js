
            // 页面状态管理
            let isLoggedIn = false;
            let isAdmin = false;
            let currentEditingItem = null;
            let refreshTimer = null;
            let shopRefreshTimer = null;
            let shopRefreshRequestId = 0;
            let shopLastRefreshAt = 0;
            let latestShopItems = [];
            let latestShopCoupons = [];
            let latestMinecraftItems = [];
            let minecraftItemsPromise = null;
            let currentPage = 'user-center';
            let itemImageUploadPromise = null;
            
            const dialogTriggers = new Map();
            let latestLeaderboardData = [];
            
            // 每日0点刷新签到状态：避免重复初始化多个定时器
            
            
            
            
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

            // 获取当前有效的 Web Session（玩家）或 Admin Session（管理员）
            
            function getAdminSession() {
                return localStorage.getItem('adminSession') || '';
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
            function formatSize(bytes) {
                const mb = Number(bytes || 0) / (1024 * 1024);
                return `${mb.toFixed(mb >= 10 ? 0 : 1)}MB`;
            }
            function updateItemImagePreview(src = '') {
                const preview = document.getElementById('item-image-preview');
                if (!preview) return;
                preview.dataset.fallbackApplied = 'false';
                preview.onerror = () => {
                    if (preview.dataset.fallbackApplied === 'true') return;
                    preview.dataset.fallbackApplied = 'true';
                    preview.src = DEFAULT_ITEM_IMAGE;
                };
                preview.src = safeImageSrc(src, DEFAULT_ITEM_IMAGE);
            }
            function setItemImageUploadStatus(message, isError = false) {
                const status = document.getElementById('item-image-upload-status');
                if (!status) return;
                status.textContent = message;
                status.classList.toggle('error', isError);
            }
            async function uploadItemImage(file) {
                if (!file) return;
                const session = getAdminSession();
                if (!session) {
                    showNotification('管理员会话已过期，请重新登录', 'error');
                    return;
                }

                const limit = window.__itemImageMaxBytes || (5 * 1024 * 1024);
                if (file.size > limit) {
                    showNotification(`商品图片不能超过${formatSize(limit)}`, 'error');
                    return;
                }

                const allowedTypes = new Set(['image/png', 'image/jpeg', 'image/jpg']);
                if (file.type && !allowedTypes.has(file.type)) {
                    showNotification('仅支持 PNG/JPG/JPEG 图片', 'error');
                    return;
                }

                try {
                    showLoading(true);
                    const form = new FormData();
                    form.append('file', file, file.name);
                    const response = await fetch('/api/uploadItemImage', {
                        method: 'POST',
                        headers: { 'X-Admin-Session': session },
                        body: form
                    });
                    const data = await response.json();
                    if (!data.success) {
                        showNotification(data.message || '商品图片上传失败', 'error');
                        return;
                    }

                    const imageInput = document.getElementById('item-image');
                    if (imageInput) imageInput.value = data.imageUrl || '';
                    updateItemImagePreview(data.imageUrl || '');
                    setItemImageUploadStatus(`已上传：${file.name}`);
                    showNotification('商品图片上传成功');
                    return data.imageUrl || '';
                } catch (error) {
                    console.error('商品图片上传失败:', error);
                    setItemImageUploadStatus('图片上传失败，请重新选择', true);
                    showNotification('商品图片上传失败', 'error');
                } finally {
                    showLoading(false);
                }
            }
            function appendIcon(button, iconClass, label = '') {
                const icon = document.createElement('i');
                icon.className = iconClass;
                icon.setAttribute('aria-hidden', 'true');
                button.appendChild(icon);
                if (label) {
                    button.appendChild(document.createTextNode(` ${label}`));
                }
            }
            function emailNodeId(username) {
                return `email-${String(username || '').replace(/[^a-zA-Z0-9_-]/g, '_')}`;
            }
            function getEmailSpan(username) {
                return document.getElementById(emailNodeId(username));
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
            function openItemModal(item = null) {
                currentEditingItem = item;
                const modal = document.getElementById('item-modal');
                const title = document.getElementById('item-modal-title');
                const imageUpload = document.getElementById('item-image-upload');
                if (imageUpload) imageUpload.value = '';
                itemImageUploadPromise = null;

                if (item) {
                    title.textContent = '编辑商品';
                    document.getElementById('item-name').value = item.name || '';
                    document.getElementById('item-id').value = item.itemId || '';
                    document.getElementById('item-amount').value = (item.amount ?? 1);
                    document.getElementById('item-points').value = item.points ?? '';
                    document.getElementById('item-stock').value = (item.stock === null ? '' : (item.stock ?? ''));
                    document.getElementById('item-image').value = item.image || '';
                    document.getElementById('item-description').value = item.description || '';
                } else {
                    title.textContent = '添加商品';
                    document.getElementById('item-name').value = '';
                    document.getElementById('item-id').value = '';
                    document.getElementById('item-amount').value = 1;
                    document.getElementById('item-points').value = '';
                    document.getElementById('item-stock').value = '';
                    document.getElementById('item-image').value = '';
                    document.getElementById('item-description').value = '';
                }
                updateItemImagePreview(document.getElementById('item-image').value);
                setItemImageUploadStatus(item ? '当前图片已保存，可选择新文件替换' : '未选择图片，保存时使用默认图片');

                openDialog('item-modal', '#item-name');
            }

            // 关闭商品弹窗
            function closeItemModal() {
                closeDialog('item-modal');
                currentEditingItem = null;
            }

            // 保存商品
            // 保存商品
            async function saveShopItem() {
                const name = document.getElementById('item-name').value.trim();
                const itemId = document.getElementById('item-id').value.trim();
                const amount = parseInt(document.getElementById('item-amount').value, 10) || 1;
                const points = parseInt(document.getElementById('item-points').value, 10);
                const stockRaw = document.getElementById('item-stock').value;
                const stock = (stockRaw === '' ? null : parseInt(stockRaw, 10));
                let image = document.getElementById('item-image').value.trim();
                const description = document.getElementById('item-description').value.trim();

                if (itemImageUploadPromise) {
                    await itemImageUploadPromise;
                    itemImageUploadPromise = null;
                    image = document.getElementById('item-image').value.trim();
                }

                if (!name || !itemId || !Number.isFinite(points)) {
                    showNotification('请填写必填字段（名称/物品ID/积分）', 'error');
                    return;
                }
                if (!Number.isFinite(amount) || amount <= 0) {
                    showNotification('物品数量不合法', 'error');
                    return;
                }
                if (stock !== null && (!Number.isFinite(stock) || stock < 0)) {
                    showNotification('库存不合法（留空代表无限库存）', 'error');
                    return;
                }

                const session = getAdminSession();
                if (!session) {
                    showNotification('管理员会话已过期，请重新登录', 'error');
                    return;
                }

                const method = currentEditingItem ? 'updateShopItem' : 'addShopItem';
                const body = currentEditingItem
                    ? { id: currentEditingItem.id, name, itemId, amount, points, stock, image, description }
                    : { name, itemId, amount, points, stock, image, description };

                try {
                    showLoading(true);
                    const response = await fetch(apiUrl({ method }), {
                        method: 'POST',
                        headers: {
                            'Content-Type': 'application/json',
                            'X-Admin-Session': session
                        },
                        body: JSON.stringify(body)
                    });

                    const data = await response.json();
                    if (data.success) {
                        showNotification(`商品${currentEditingItem ? '更新' : '添加'}成功`);
                        closeItemModal();
                        await refreshShop({ silent: true });
                    } else {
                        showNotification(`操作失败: ${data.message || '未知错误'}`, 'error');
                    }
                } catch (error) {
                    console.error('保存商品失败:', error);
                    showNotification('保存失败', 'error');
                } finally {
                    showLoading(false);
                }
            }

            // 删除商品

            async function deleteItem(itemId) {
                if (!confirm('确定要删除这个商品吗？')) return;

                const session = getAdminSession();
                if (!session) {
                    showNotification('管理员会话已过期，请重新登录', 'error');
                    return;
                }

                try {
                    showLoading(true);
                    const response = await fetch(apiUrl({ method: 'deleteShopItem', itemId }), {
                        headers: { 'X-Admin-Session': session }
                    });
                    const data = await response.json();

                    if (data.success) {
                        showNotification('商品已删除');
                        await refreshShop({ silent: true });
                    } else {
                        showNotification(data.message || '删除失败', 'error');
                    }
                } catch (error) {
                    console.error('删除商品失败:', error);
                    showNotification('删除失败', 'error');
                } finally {
                    showLoading(false);
                }
            }

            
            function normalizeCouponItemId(value) {
                const raw = String(value || '').trim().toLowerCase().replace(/\s+/g, '_');
                if (!raw) return '';
                if ((raw.match(/:/g) || []).length > 1) return '';
                if (!/^[a-z0-9_.-]+(?::[a-z0-9_.-]+)?$/.test(raw)) return '';
                return raw.includes(':') ? raw : `minecraft:${raw}`;
            }

            function normalizeCouponAmount(value) {
                const amount = Number(value);
                const maxAmount = window.__couponItemMaxAmount || 2304;
                if (!Number.isInteger(amount) || amount < 1 || amount > maxAmount) return null;
                return amount;
            }

            function getMinecraftItemLabel(itemId) {
                const normalized = normalizeCouponItemId(itemId) || String(itemId || '').trim();
                if (!normalized) return '';
                const shortName = normalized.replace(/^minecraft:/, '').replace(/_/g, ' ');
                return normalized.startsWith('minecraft:') ? `${shortName} (${normalized})` : normalized;
            }

            function populateMinecraftItemOptions() {
                const datalist = document.getElementById('minecraft-item-options');
                if (!datalist) return;
                datalist.innerHTML = '';
                latestMinecraftItems.forEach(itemId => {
                    const option = document.createElement('option');
                    option.value = itemId;
                    option.label = getMinecraftItemLabel(itemId);
                    datalist.appendChild(option);
                });
            }

            async function loadMinecraftItems() {
                if (latestMinecraftItems.length) return latestMinecraftItems;
                if (minecraftItemsPromise) return minecraftItemsPromise;

                minecraftItemsPromise = fetch('/minecraft-items.json', { cache: 'force-cache' })
                    .then(response => {
                        if (!response.ok) throw new Error(`HTTP ${response.status}`);
                        return response.json();
                    })
                    .then(items => {
                        const seen = new Set();
                        latestMinecraftItems = (Array.isArray(items) ? items : [])
                            .map(normalizeCouponItemId)
                            .filter(itemId => {
                                if (!itemId || seen.has(itemId)) return false;
                                seen.add(itemId);
                                return true;
                            })
                            .sort((a, b) => a.localeCompare(b));
                        populateMinecraftItemOptions();
                        return latestMinecraftItems;
                    })
                    .catch(error => {
                        console.warn('加载MC物品列表失败:', error);
                        latestMinecraftItems = [];
                        populateMinecraftItemOptions();
                        return [];
                    })
                    .finally(() => {
                        minecraftItemsPromise = null;
                    });

                return minecraftItemsPromise;
            }

            function createMinecraftItemInput(initialValue = '') {
                const input = document.createElement('input');
                input.type = 'text';
                input.className = 'bundle-item-id input';
                input.title = '输入或选择MC物品ID';
                input.placeholder = ' ';
                input.autocomplete = 'off';
                input.setAttribute('list', 'minecraft-item-options');
                input.value = normalizeCouponItemId(initialValue) || String(initialValue || '').trim();
                input.addEventListener('change', () => {
                    const normalized = normalizeCouponItemId(input.value);
                    if (normalized) input.value = normalized;
                });
                return input;
            }

            function createBundleItemRow(initialValue = '', initialAmount = 1) {
                const row = document.createElement('div');
                row.className = 'bundle-item';

                const itemGroup = document.createElement('div');
                itemGroup.className = 'group bundle-field select-field';
                const itemSelect = createMinecraftItemInput(initialValue);
                const itemLabel = document.createElement('label');
                itemLabel.textContent = 'MC物品ID';
                itemGroup.appendChild(itemSelect);
                itemGroup.insertAdjacentHTML('beforeend', '<span class="highlight"></span><span class="bar"></span>');
                itemGroup.appendChild(itemLabel);

                const amountGroup = document.createElement('div');
                amountGroup.className = 'group bundle-field';
                amountGroup.innerHTML = '<input class="bundle-item-amount input" min="1" placeholder=" " title="数量" type="number" value="1"><span class="highlight"></span><span class="bar"></span><label>数量</label>';
                const amountInput = amountGroup.querySelector('.bundle-item-amount');
                amountInput.value = Math.max(1, parseInt(initialAmount, 10) || 1);

                const removeButton = document.createElement('button');
                removeButton.type = 'button';
                removeButton.className = 'bundle-remove';
                appendIcon(removeButton, 'fas fa-trash-can');
                removeButton.title = '移除此物品';
                removeButton.setAttribute('aria-label', '移除此物品');
                removeButton.addEventListener('click', () => removeBundleItem(removeButton));

                row.appendChild(itemGroup);
                row.appendChild(amountGroup);
                row.appendChild(removeButton);
                return row;
            }

            // 添加礼包物品行
            function addBundleItem() {
                const container = document.getElementById('bundle-items-container');
                if (container) container.appendChild(createBundleItemRow());
            }

            // 移除礼包物品行，至少保留一行方便继续填写。
            function removeBundleItem(button) {
                const row = button?.closest('.bundle-item');
                const container = document.getElementById('bundle-items-container');
                if (!row || !container) return;
                if (container.children.length <= 1) {
                    const select = row.querySelector('.bundle-item-id');
                    const amount = row.querySelector('.bundle-item-amount');
                    if (select) select.value = '';
                    if (amount) amount.value = 1;
                    return;
                }
                row.remove();
            }

            function updateShopSummary(items = latestShopItems, coupons = latestShopCoupons) {
                const total = Array.isArray(items) ? items.length : 0;
                const available = (items || []).filter(item => {
                    const unlimited = item.stock === null || item.stock === undefined || item.stock === '';
                    return unlimited || Number(item.stock) > 0;
                }).length;
                const couponTotal = Array.isArray(coupons) ? coupons.length : 0;
                const totalNode = document.getElementById('shop-total-count');
                const availableNode = document.getElementById('shop-available-count');
                const couponNode = document.getElementById('shop-coupon-count');
                if (totalNode) totalNode.textContent = String(total);
                if (availableNode) availableNode.textContent = String(available);
                if (couponNode) couponNode.textContent = String(couponTotal);
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

                    if (isAdmin) {
                        requests.push(fetch(apiUrl({ method: 'getCoupons', t: Date.now() }), {
                            headers: { 'X-Admin-Session': getAdminSession() },
                            cache: 'no-store'
                        }));
                    }

                    const responses = await Promise.all(requests);
                    const data = await responses[0].json();
                    const couponData = isAdmin && responses[1] ? await responses[1].json() : null;

                    if (requestId !== shopRefreshRequestId) return;
                    if (!data.success) throw new Error(data.message || '获取商品列表失败');

                    latestShopItems = Array.isArray(data.items) ? data.items : [];
                    renderShopItems(latestShopItems);
                    populateMinecraftItemOptions();

                    if (isAdmin) {
                        if (!couponData?.success) {
                            throw new Error(couponData?.message || '获取兑换码列表失败');
                        }
                        latestShopCoupons = Array.isArray(couponData.coupons) ? couponData.coupons : [];
                        renderCoupons(latestShopCoupons);
                        const management = document.getElementById('coupon-management');
                        if (management) management.style.display = 'block';
                    }

                    updateShopSummary(latestShopItems, latestShopCoupons);
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

                    if (isAdmin) {
                        const editBtn = document.createElement('button');
                        editBtn.className = 'server-button small';
                        editBtn.type = 'button';
                        editBtn.title = '编辑商品';
                        editBtn.setAttribute('aria-label', '编辑商品');
                        appendIcon(editBtn, 'fas fa-edit', '编辑');
                        editBtn.addEventListener('click', () => openItemModal(item));

                        const delBtn = document.createElement('button');
                        delBtn.className = 'server-button danger small';
                        delBtn.type = 'button';
                        delBtn.title = '删除商品';
                        delBtn.setAttribute('aria-label', '删除商品');
                        appendIcon(delBtn, 'fas fa-trash', '删除');
                        delBtn.addEventListener('click', () => deleteItem(String(item.id)));

                        actions.appendChild(editBtn);
                        actions.appendChild(delBtn);
                    } else {
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

            function getCouponStatus(coupon) {
                const expired = coupon?.expiresAt && new Date(coupon.expiresAt).getTime() < Date.now();
                if (expired) return { key: 'expired', text: '已过期', className: 'is-expired' };
                if (coupon?.oneTimeUse) {
                    return coupon.used
                        ? { key: 'used', text: '已使用', className: 'is-used' }
                        : { key: 'available', text: '未使用', className: 'is-available' };
                }
                const usedCount = Array.isArray(coupon?.usedBy) ? coupon.usedBy.length : 0;
                return usedCount > 0
                    ? { key: 'used', text: `已使用 (${usedCount}次)`, className: 'is-used' }
                    : { key: 'available', text: '未使用', className: 'is-available' };
            }

            function applyCouponFilters() {
                renderCoupons(latestShopCoupons);
            }

            function renderCoupons(coupons = latestShopCoupons) {
                const tbody = document.getElementById('coupons-body');
                if (!tbody) return;
                tbody.innerHTML = '';

                const search = String(document.getElementById('coupon-search')?.value || '').trim().toLowerCase();
                const statusFilter = document.getElementById('coupon-status-filter')?.value || 'all';
                const sourceCoupons = Array.isArray(coupons) ? coupons : [];
                const visibleCoupons = sourceCoupons.filter(coupon => {
                    const status = getCouponStatus(coupon);
                    const itemText = (coupon.items || []).map(item => getMinecraftItemLabel(item.itemId)).join(' ');
                    const playerText = coupon.designatedPlayer || '未指定';
                    const searchable = `${coupon.code || ''} ${itemText} ${playerText}`.toLowerCase();
                    return (!search || searchable.includes(search))
                        && (statusFilter === 'all' || status.key === statusFilter);
                });

                const countNode = document.getElementById('coupon-visible-count');
                if (countNode) {
                    countNode.textContent = visibleCoupons.length === sourceCoupons.length
                        ? `${sourceCoupons.length} 条记录`
                        : `显示 ${visibleCoupons.length} / ${sourceCoupons.length} 条`;
                }

                visibleCoupons.forEach(coupon => {
                    const status = getCouponStatus(coupon);
                    const playerUsed = coupon.designatedPlayer ? coupon.designatedPlayer : '未指定';
                    const itemInfo = (coupon.items || []).map(item => {
                        const label = getMinecraftItemLabel(item.itemId) || String(item.itemId || '');
                        return `${label} x${item.amount || 1}`;
                    }).join(', ');
                    const row = document.createElement('tr');

                    const tdCode = document.createElement('td');
                    safeSetText(tdCode, coupon.code);

                    const tdItems = document.createElement('td');
                    safeSetText(tdItems, itemInfo);

                    const tdPlayer = document.createElement('td');
                    safeSetText(tdPlayer, playerUsed);

                    const tdExp = document.createElement('td');
                    try {
                        safeSetText(tdExp, new Date(coupon.expiresAt).toLocaleDateString());
                    } catch (_) {
                        safeSetText(tdExp, '');
                    }

                    const tdStatus = document.createElement('td');
                    tdStatus.className = `coupon-status ${status.className}`;
                    safeSetText(tdStatus, status.text);

                    const tdAct = document.createElement('td');
                    const actionWrap = document.createElement('div');
                    actionWrap.className = 'coupon-actions';

                    const copyBtn = document.createElement('button');
                    copyBtn.className = 'server-button small';
                    copyBtn.type = 'button';
                    copyBtn.title = '复制兑换码';
                    copyBtn.setAttribute('aria-label', '复制兑换码');
                    appendIcon(copyBtn, 'fas fa-copy', '复制');
                    copyBtn.addEventListener('click', async () => {
                        try {
                            await copyText(String(coupon.code || ''));
                            showNotification('兑换码已复制');
                        } catch (_) {
                            showNotification('复制失败，请手动复制', 'error');
                        }
                    });

                    const deleteBtn = document.createElement('button');
                    deleteBtn.className = 'server-button danger small';
                    deleteBtn.type = 'button';
                    deleteBtn.title = '删除兑换码';
                    deleteBtn.setAttribute('aria-label', '删除兑换码');
                    appendIcon(deleteBtn, 'fas fa-trash', '删除');
                    deleteBtn.addEventListener('click', () => deleteCoupon(String(coupon.code)));

                    actionWrap.appendChild(copyBtn);
                    actionWrap.appendChild(deleteBtn);
                    tdAct.appendChild(actionWrap);

                    row.appendChild(tdCode);
                    row.appendChild(tdItems);
                    row.appendChild(tdPlayer);
                    row.appendChild(tdExp);
                    row.appendChild(tdStatus);
                    row.appendChild(tdAct);

                    tbody.appendChild(row);
                });
            }
            // 打开兑换码生成弹窗

            function getDateInputValue(daysFromToday = 0) {
                const date = new Date();
                date.setHours(12, 0, 0, 0);
                date.setDate(date.getDate() + daysFromToday);
                const year = date.getFullYear();
                const month = String(date.getMonth() + 1).padStart(2, '0');
                const day = String(date.getDate()).padStart(2, '0');
                return `${year}-${month}-${day}`;
            }

            function openCouponModal() {
                if (!getAdminSession()) {
                    showNotification('管理员会话已过期，请重新登录', 'error');
                    return;
                }

                document.getElementById('coupon-type').value = 'item';
                document.getElementById('coupon-expires').min = getDateInputValue(0);
                document.getElementById('coupon-expires').value = getDateInputValue(3);
                document.getElementById('coupon-designated').value = '';
                document.getElementById('coupon-one-time').value = 'true';
                document.getElementById('coupon-item-id').value = '';
                document.getElementById('coupon-item-amount').value = 1;
                loadMinecraftItems();

                const bundleContainer = document.getElementById('bundle-items-container');
                if (bundleContainer) {
                    bundleContainer.innerHTML = '';
                    bundleContainer.appendChild(createBundleItemRow());
                }
                toggleCouponType();
                openDialog('generate-coupon-modal', '#coupon-item-id');
            }

            // 关闭兑换码生成弹窗
            function closeGenerateCouponModal() {
                closeDialog('generate-coupon-modal');
            }

            // 关闭兑换码显示弹窗
            function closeCouponModal() {
                closeDialog('coupon-modal');
            }
            // 切换兑换码类型显示
            function toggleCouponType() {
                const type = document.getElementById('coupon-type').value;
                const itemGroup = document.getElementById('coupon-item-group');
                const bundleGroup = document.getElementById('coupon-bundle-group');

                // 重置所有组
                itemGroup.style.display = 'none';
                bundleGroup.style.display = 'none';

                // 根据类型显示对应组
                if (type === 'item') {
                    itemGroup.style.display = 'grid';
                } else if (type === 'bundle') {
                    bundleGroup.style.display = 'grid';
                }
            }
            // 修复前端生成兑换码函数
            async function generateCoupon() {
                const type = document.getElementById('coupon-type').value;
                const expires = document.getElementById('coupon-expires').value;
                const designatedPlayer = document.getElementById('coupon-designated').value || '';
                const oneTimeUse = document.getElementById('coupon-one-time').value === 'true';

                let items = [];

                if (type === 'item') {
                    const itemId = normalizeCouponItemId(document.getElementById('coupon-item-id').value);
                    const amount = normalizeCouponAmount(document.getElementById('coupon-item-amount').value);
                    if (!itemId) {
                        showNotification('请输入有效的MC物品ID', 'error');
                        return;
                    }
                    if (amount === null) {
                        showNotification(`物品数量需在 1-${window.__couponItemMaxAmount || 2304} 之间`, 'error');
                        return;
                    }
                    items.push({ itemId, amount });
                } else if (type === 'bundle') {
                    const itemElements = document.querySelectorAll('#bundle-items-container .bundle-item');
                    let hasInvalidBundleItem = false;
                    itemElements.forEach(item => {
                        const rawItemId = String(item.querySelector('.bundle-item-id').value || '').trim();
                        const itemId = normalizeCouponItemId(rawItemId);
                        const amount = normalizeCouponAmount(item.querySelector('.bundle-item-amount').value);
                        if (!rawItemId) return;
                        if (!itemId || amount === null) {
                            hasInvalidBundleItem = true;
                            return;
                        }
                        items.push({ itemId, amount });
                    });

                    if (hasInvalidBundleItem) {
                        showNotification(`礼包内有不合法的MC物品ID或数量（1-${window.__couponItemMaxAmount || 2304}）`, 'error');
                        return;
                    }

                    if (items.length === 0) {
                        showNotification('请添加至少一个有效的MC物品到礼包', 'error');
                        return;
                    }
                }

                if (!expires) {
                    showNotification('请选择过期时间', 'error');
                    return;
                }

                try {
                    showLoading(true);
                    const session = localStorage.getItem('adminSession');
                    if (!session) {
                        showNotification('管理员会话已过期，请重新登录', 'error');
                        return;
                    }

                    const response = await fetch(apiUrl({ method: 'generateCoupon' }), {
                        method: 'POST',
                        headers: {
                            'Content-Type': 'application/json',
                            'X-Admin-Session': session
                        },
                        body: JSON.stringify({
                            type, items, designatedPlayer, expiresAt: expires, oneTimeUse
                        })
                    });

                    const data = await response.json();
                    if (data.success) {
                        showNotification(`兑换码生成成功: ${data.couponCode}`);
                        closeGenerateCouponModal();

                        // 显示兑换码弹窗
                        document.getElementById('coupon-item-name').textContent =
                            type === 'item' ? `${getMinecraftItemLabel(items[0].itemId)} x ${items[0].amount}` :
                                `礼包 (${items.length} 个物品)`;
                        document.getElementById('coupon-code').textContent = data.couponCode;
                        openDialog('coupon-modal');

                        // 刷新兑换码列表
                        await refreshShop({ silent: true });
                    } else {
                        showNotification(data.message, 'error');
                    }
                } catch (error) {
                    console.error('生成兑换码失败:', error);
                    showNotification('生成兑换码失败', 'error');
                } finally {
                    showLoading(false);
                }
            }
            // 删除兑换码
            async function deleteCoupon(code) {
                if (!confirm('确定要删除这个兑换码吗？')) return;

                try {
                    showLoading(true);
                    // 获取管理员会话
                    const session = localStorage.getItem('adminSession');
                    // 添加session参数到请求
                    const response = await fetch(apiUrl({ method: 'deleteCoupon', code }), { headers: { 'X-Admin-Session': session } });
                    const data = await response.json();

                    if (data.success) {
                        showNotification('兑换码已删除');
                        await refreshShop({ silent: true });
                    } else {
                        showNotification(data.message, 'error');
                    }
                } catch (error) {
                    console.error('删除兑换码失败:', error);
                    showNotification('删除失败', 'error');
                } finally {
                    showLoading(false);
                }
            }
            document.getElementById('save-item-btn').addEventListener('click', saveShopItem);
            document.getElementById('item-image-upload')?.addEventListener('change', function (event) {
                const file = event.target.files && event.target.files[0];
                itemImageUploadPromise = uploadItemImage(file).finally(() => {
                    this.value = '';
                    itemImageUploadPromise = null;
                });
            });
            document.addEventListener('change', event => {
                const input = event.target?.closest?.('.bundle-item-id');
                if (!input) return;
                const normalized = normalizeCouponItemId(input.value);
                if (normalized) input.value = normalized;
            });
            // 更新validateInput函数
            
            // 在脚本部分添加密码修改功能
            

            // 清空密码字段
            
            // 每日0点自动刷新签到状态
            
            // 初始化页面
            function initPage() {
    document.querySelectorAll('.nav-item[onclick]').forEach(item => {
        if (!item.hasAttribute('role')) item.setAttribute('role', 'button');
        if (!item.hasAttribute('tabindex')) item.setAttribute('tabindex', '0');
        item.addEventListener('keydown', event => { if (event.key === 'Enter' || event.key === ' ') { event.preventDefault(); item.click(); } });
    });
}
            // 监听ESC键关闭弹窗
            
            // 密码显示/隐藏切换
            

            // 切换管理员登录模式
            
            // 登录功能（完整版）
            async function login() {
    const username = document.getElementById('login-username')?.value.trim() || '';
    const password = document.getElementById('login-password')?.value || '';
    const totp = document.getElementById('login-totp')?.value.trim() || '';
    const button = document.getElementById('admin-login-button');
    if (!username || !password) { showNotification('请填写管理员账户和密码', 'error'); return; }
    button.disabled = true;
    showLoading(true);
    try {
        const statusResponse = await fetch('/api/admin/status', { cache: 'no-store' });
        const adminStatus = await statusResponse.json();
        if (!statusResponse.ok || adminStatus.success === false) throw new Error(adminStatus.message || '无法读取管理员状态');
        const totpInput = document.getElementById('login-totp');
        if (totpInput) totpInput.closest('.group').hidden = !adminStatus.totpEnabled;
        if (adminStatus.totpEnabled && !totp) throw new Error('请输入 TOTP 验证码');
        const response = await fetch('/api/adminLogin', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name: username, passwd: password, totp }) });
        const result = await response.json();
        if (!response.ok || !result.success || !result.session) throw new Error(result.message || '管理员登录失败');
        localStorage.setItem('adminSession', result.session);
        isAdmin = true;
        isLoggedIn = true;
        userData.username = username;
        document.body.classList.add('admin-session');
        document.getElementById('login-page').style.display = 'none';
        document.getElementById('main-system').style.display = 'flex';
        document.getElementById('username-display').textContent = username;
        document.getElementById('user-status').textContent = '管理员';
        document.getElementById('user-center-kicker').textContent = 'ADMIN DIRECTORY';
        document.getElementById('user-center-title').textContent = '用户管理';
        document.getElementById('user-center-description').textContent = '查看账户状态并管理用户';
        document.getElementById('admin-user-management').style.display = 'block';
        document.querySelectorAll('.admin-only').forEach(element => { element.style.display = ''; });
        await loadAdminUserData();
        switchPage('user-center');
        showNotification('管理员登录成功');
    } catch (error) {
        showNotification(error.message || '管理员登录失败', 'error');
    } finally {
        button.disabled = false;
        showLoading(false);
    }
}

['login-username', 'login-password', 'login-totp'].forEach(id => {
    document.getElementById(id)?.addEventListener('keydown', event => {
        if (event.key === 'Enter') {
            event.preventDefault();
            login();
        }
    });
});

            // 打开注册/忘记密码弹窗
            

            // 关闭弹窗
            

            // 切换认证标签页
            

            

            // 注册功能（添加防重复点击）
            

            // 验证注册
            

            // 忘记密码功能（添加防重复点击）
            

            // 验证密码重置
            

            // 开始验证码倒计时
            
            

            

            // 修改：服务器登录功能 - 立即更新状态
            

            // 修改：服务器登出功能 - 立即更新状态
            
            // 修改 fetchUserInfo 函数
            

            

            async function refreshUserCenter() {
                const button = document.getElementById('user-center-refresh');
                if (button) button.disabled = true;
                try {
                    if (isAdmin) {
                        await loadAdminUserData();
                    } else {
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
            // 修改：状态更新函数 - 确保UI立即刷新
            
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
            
            // 服务器状态检测统一由后端 /api/serverStatus 发起，前端只展示后端结果。

            

            // 修改前端服务器状态刷新函数
            

            function renderEmailDisplay(username, email, target = null) {
                const emailSpan = target || getEmailSpan(username);
                if (!emailSpan) return;

                emailSpan.dataset.email = email || '';
                emailSpan.textContent = '';

                const value = document.createElement('span');
                value.className = 'email-text';
                safeSetText(value, email || '');

                const editBtn = document.createElement('button');
                editBtn.type = 'button';
                editBtn.className = 'server-button primary small';
                editBtn.title = '编辑邮箱';
                editBtn.setAttribute('aria-label', '编辑邮箱');
                appendIcon(editBtn, 'fas fa-edit');
                editBtn.addEventListener('click', () => showEditEmail(username));

                emailSpan.appendChild(value);
                emailSpan.appendChild(document.createTextNode(' '));
                emailSpan.appendChild(editBtn);
            }

            // 加载管理员用户数据
            async function loadAdminUserData() {
                try {
                    showLoading(true);
                    const session = localStorage.getItem('adminSession');
                    // 调用获取所有用户API
                    const response = await fetch(`${API_BASE_URL}?method=getAllUsers`, { headers: { 'X-Admin-Session': session } });
                    const data = await response.json();

                    if (!data.success) {
                        showNotification('获取用户数据失败: ' + data.message, 'error');
                        return;
                    }

                    const users = data.users;
                    const tableBody = document.getElementById('user-table-body');
                    tableBody.innerHTML = '';

                    const statusCounts = users.reduce((counts, user) => {
                        if (Object.prototype.hasOwnProperty.call(counts, user.status)) {
                            counts[user.status] += 1;
                        }
                        return counts;
                    }, { active: 0, inactive: 0, banned: 0 });
                    document.getElementById('admin-user-total').textContent = users.length;
                    document.getElementById('admin-user-active').textContent = statusCounts.active;
                    document.getElementById('admin-user-inactive').textContent = statusCounts.inactive;
                    document.getElementById('admin-user-banned').textContent = statusCounts.banned;

                    users.forEach(user => {
                        const row = document.createElement('tr');

                        let statusClass = '';
                        let statusText = '';

                        switch (user.status) {
                            case 'active':
                                statusClass = 'status-active';
                                statusText = '已激活';
                                break;
                            case 'inactive':
                                statusClass = 'status-inactive';
                                statusText = '未激活';
                                break;
                            case 'banned':
                                statusClass = 'status-banned';
                                statusText = '已封禁';
                                break;
                        }

                        // 安全：避免 innerHTML 拼接用户可控字段，防止DOM XSS
                        const nameCell = document.createElement('td');
                        nameCell.className = 'dynamic-value';
                        nameCell.textContent = user.name;
                        row.appendChild(nameCell);

                        const emailCell = document.createElement('td');
                        const emailSpan = document.createElement('span');
                        emailSpan.id = emailNodeId(user.name);
                        renderEmailDisplay(user.name, user.email || '', emailSpan);
                        emailCell.appendChild(emailSpan);
                        row.appendChild(emailCell);

                        const statusCell = document.createElement('td');
                        statusCell.className = statusClass;
                        statusCell.textContent = statusText;
                        row.appendChild(statusCell);

                        const joinCell = document.createElement('td');
                        joinCell.textContent = user.joinDate || '未知';
                        row.appendChild(joinCell);

                        const lastLoginCell = document.createElement('td');
                        lastLoginCell.textContent = user.lastLogin || '从未登录';
                        row.appendChild(lastLoginCell);

                        const onlineCell = document.createElement('td');
                        onlineCell.textContent = formatTime(user.onlineTime || 0);
                        row.appendChild(onlineCell);

                        const actionsCell = document.createElement('td');
                        const actionsDiv = document.createElement('div');
                        actionsDiv.className = 'action-buttons';

                        const activateBtn = document.createElement('button');
                        activateBtn.type = 'button';
                        activateBtn.className = `server-button ${user.status !== 'active' ? '' : 'disabled'}`.trim();
                        activateBtn.textContent = '激活';
                        activateBtn.disabled = user.status === 'active';
                        activateBtn.addEventListener('click', () => adminAction('activate', user.name));
                        actionsDiv.appendChild(activateBtn);

                        const banBtn = document.createElement('button');
                        banBtn.type = 'button';
                        banBtn.className = `server-button danger ${user.status !== 'banned' ? '' : 'disabled'}`.trim();
                        banBtn.textContent = '封禁';
                        banBtn.disabled = user.status === 'banned';
                        banBtn.addEventListener('click', () => adminAction('ban', user.name));
                        actionsDiv.appendChild(banBtn);

                        const unbanBtn = document.createElement('button');
                        unbanBtn.type = 'button';
                        unbanBtn.className = `server-button primary ${user.status === 'banned' ? '' : 'disabled'}`.trim();
                        unbanBtn.textContent = '解封';
                        unbanBtn.disabled = user.status !== 'banned';
                        unbanBtn.addEventListener('click', () => adminAction('unban', user.name));
                        actionsDiv.appendChild(unbanBtn);

                        const deleteBtn = document.createElement('button');
                        deleteBtn.className = 'server-button danger';
                        deleteBtn.type = 'button';
                        deleteBtn.title = '删除用户';
                        deleteBtn.setAttribute('aria-label', '删除用户');
                        appendIcon(deleteBtn, 'fas fa-trash-alt');
                        deleteBtn.addEventListener('click', () => deleteUser(user.name));
                        actionsDiv.appendChild(deleteBtn);

                        actionsCell.appendChild(actionsDiv);
                        row.appendChild(actionsCell);

                        tableBody.appendChild(row);
                    });

                } catch (error) {
                    console.error('加载用户数据错误:', error);
                    showNotification('加载用户数据失败', 'error');
                } finally {
                    showLoading(false);
                }
            }

            // 管理员操作
            async function adminAction(action, username) {
                let actionText = '';
                let method = '';

                switch (action) {
                    case 'activate':
                        actionText = '激活';
                        method = 'activateUser';
                        break;
                    case 'ban':
                        actionText = '封禁';
                        method = 'banUser';
                        break;
                    case 'unban':
                        actionText = '解封';
                        method = 'unbanUser';
                        break;
                }

                if (!confirm(`确定要${actionText}用户 ${username} 吗？`)) {
                    return;
                }

                try {
                    showLoading(true);
                    const session = localStorage.getItem('adminSession');
                    if (!session) {
                        showNotification('管理员会话已过期，请重新登录', 'error');
                        return;
                    }
                    // 调用管理员操作API
                    const response = await fetch(API_BASE_URL, {
                        method: 'POST',
                        headers: {
                            'Content-Type': 'application/json',
                            'X-Admin-Session': session
                        },
                        body: JSON.stringify({ method, name: username })
                    });
                    const data = await response.json();

                    if (data.success) {
                        showNotification(`用户 ${username} 已${actionText}`);
                        loadAdminUserData();
                    } else {
                        showNotification(`${actionText}用户失败: ${data.message}`, 'error');
                    }
                } catch (error) {
                    console.error(`${actionText}用户错误:`, error);
                    showNotification(`${actionText}用户失败`, 'error');
                } finally {
                    showLoading(false);
                }
            }
            // 添加删除用户函数
            async function deleteUser(username) {
                if (!confirm(`确定要永久删除用户 ${username} 吗？此操作不可撤销！`)) {
                    return;
                }

                try {
                    showLoading(true);
                    const session = localStorage.getItem('adminSession');
                    if (!session) {
                        showNotification('管理员会话已过期，请重新登录', 'error');
                        return;
                    }
                    const response = await fetch(API_BASE_URL, {
                        method: 'POST',
                        headers: {
                            'Content-Type': 'application/json',
                            'X-Admin-Session': session
                        },
                        body: JSON.stringify({ method: 'deleteUser', name: username })
                    });
                    const data = await response.json();

                    if (data.success) {
                        showNotification(`用户 ${username} 已永久删除`);
                        loadAdminUserData();
                    } else {
                        showNotification(`删除用户失败: ${data.message}`, 'error');
                    }
                } catch (error) {
                    console.error(`删除用户错误:`, error);
                    showNotification(`删除用户失败`, 'error');
                } finally {
                    showLoading(false);
                }
            }
            // 添加邮箱编辑相关函数
            function showEditEmail(username) {
                const emailSpan = getEmailSpan(username);
                if (!emailSpan) return;
                const currentEmail = emailSpan.dataset.email || '';

                // 创建编辑表单
                emailSpan.textContent = '';
                const wrap = document.createElement('span');
                wrap.className = 'email-edit';

                const input = document.createElement('input');
                input.type = 'email';
                input.className = 'email-edit-input';
                input.id = `edit-${emailNodeId(username)}`;
                input.value = currentEmail;

                const saveBtn = document.createElement('button');
                saveBtn.type = 'button';
                saveBtn.className = 'server-button success small';
                saveBtn.title = '保存邮箱';
                saveBtn.setAttribute('aria-label', '保存邮箱');
                appendIcon(saveBtn, 'fas fa-check');
                saveBtn.addEventListener('click', () => saveEmail(username));

                const cancelBtn = document.createElement('button');
                cancelBtn.type = 'button';
                cancelBtn.className = 'server-button danger small';
                cancelBtn.title = '取消编辑';
                cancelBtn.setAttribute('aria-label', '取消编辑');
                appendIcon(cancelBtn, 'fas fa-times');
                cancelBtn.addEventListener('click', () => cancelEditEmail(username, currentEmail));

                wrap.appendChild(input);
                wrap.appendChild(saveBtn);
                wrap.appendChild(cancelBtn);
                emailSpan.appendChild(wrap);
                input.focus();
            }

            function cancelEditEmail(username, originalEmail) {
                renderEmailDisplay(username, originalEmail);
            }

            async function saveEmail(username) {
                const emailSpan = getEmailSpan(username);
                const input = emailSpan ? emailSpan.querySelector('.email-edit-input') : null;
                const newEmail = input ? input.value.trim() : '';
                if (!newEmail) {
                    showNotification("邮箱不能为空", "error");
                    return;
                }

                try {
                    showLoading(true);
                    const session = localStorage.getItem('adminSession');
                    if (!session) {
                        showNotification('管理员会话已过期，请重新登录', 'error');
                        return;
                    }
                    const response = await fetch(API_BASE_URL, {
                        method: 'POST',
                        headers: {
                            'Content-Type': 'application/json',
                            'X-Admin-Session': session
                        },
                        body: JSON.stringify({
                            method: 'updateEmail',
                            name: username,
                            newEmail
                        })
                    });
                    const data = await response.json();

                    if (data.success) {
                        showNotification("邮箱更新成功");
                        // 更新显示
                        renderEmailDisplay(username, newEmail);
                    } else {
                        showNotification(`邮箱更新失败: ${data.message}`, "error");
                        if (input) input.focus();
                    }
                } catch (error) {
                    console.error("保存邮箱错误:", error);
                    showNotification("保存邮箱失败", "error");
                } finally {
                    showLoading(false);
                }
            }
            

            

            

            // 以服务端返回的日期为锚点生成本周日历。
            
            // 更新签到按钮状态
            
            // 获取用户签到数据
            
            // 修改点3：修改排行榜刷新函数，存储最新数据
            

            // 新增函数：渲染排行榜
            
            // 修改点4：添加账户设置刷新功能
            
            // 修改签到函数
            
            // 切换页面
            function switchPage(page) {
    if (currentPage === 'shop' && page !== 'shop') stopShopAutoRefresh();
    if (!['user-center', 'shop', 'admin-config', 'plugin-tokens'].includes(page)) page = 'user-center';
    currentPage = page;
    if (page === 'shop') { refreshShop(); startShopAutoRefresh(); }
    if (page === 'user-center') loadAdminUserData();
    if (page === 'admin-config') window.loadAdminConfig?.();
    if (page === 'plugin-tokens') window.loadPluginTokens?.();
    document.querySelectorAll('.nav-item').forEach(item => item.classList.toggle('active', item.dataset.page === page));
    document.getElementById('page-title').textContent = getPageTitle(page);
    document.querySelectorAll('.page-content').forEach(content => { content.style.display = content.id === page ? 'block' : 'none'; });
}

            // 获取页面标题
            function getPageTitle(page) {
    if (page === 'admin-config' && document.body.dataset.githubRelease === 'true') return '管理系统';
    return ({ 'user-center': '用户管理', shop: '积分商城', 'admin-config': '运行配置', 'plugin-tokens': '插件接入' })[page] || '管理控制台';
}

            // 退出登录
            function logout() {
    isLoggedIn = false;
    isAdmin = false;
    stopShopAutoRefresh();
    localStorage.removeItem('adminSession');
    document.body.classList.remove('admin-session');
    document.getElementById('main-system').style.display = 'none';
    document.getElementById('login-page').style.display = 'flex';
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

            

            // 初始化页面
            


            // 登录与导航的键盘操作；主题切换统一由 theme.js 处理。
            

        
window.addEventListener('load', () => { initPage(); document.body.classList.remove('loading'); });
window.addEventListener('load', async () => {
    try {
        const response = await fetch('/api/admin/status', { cache: 'no-store' });
        const result = await response.json();
        const input = document.getElementById('login-totp');
        if (input) input.closest('.group').hidden = !result.totpEnabled;
    } catch (_) {
        // 登录时仍会重新读取服务端状态。 / The login flow still checks the server state.
    }
});
