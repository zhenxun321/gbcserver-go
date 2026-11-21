/* 在样式加载前应用主题；管理员页默认深色，其他页面默认跟随系统。 */
(function () {
    'use strict';
    const root = document.documentElement;
    const media = window.matchMedia('(prefers-color-scheme: dark)');
    const storageKey = root.dataset.themeStorageKey || 'theme';
    let preference = readPreference();

    function readPreference() {
        try {
            const value = localStorage.getItem(storageKey);
            return value === 'dark' || value === 'light' ? value : null;
        } catch (error) {
            return null;
        }
    }

    function defaultTheme() {
        const pageDefault = root.dataset.themeDefault;
        if (pageDefault === 'dark' || pageDefault === 'light') return pageDefault;
        return media.matches ? 'dark' : 'light';
    }

    function applyTheme() {
        const theme = preference || defaultTheme();
        const dark = theme === 'dark';
        root.dataset.theme = theme;
        const meta = document.querySelector('meta[name="theme-color"]');
        if (meta) meta.content = dark ? '#111318' : '#f9f9ff';
        if (!document.body) return;
        document.body.classList.toggle('theme-dark', dark);
        document.body.classList.toggle('theme-light', !dark);
        document.querySelectorAll('[data-theme-toggle]').forEach(button => {
            const label = dark ? '切换到浅色模式' : '切换到深色模式';
            button.setAttribute('aria-label', label);
            button.setAttribute('title', label);
            const text = button.querySelector('.nav-text');
            if (text) text.textContent = dark ? '浅色模式' : '深色模式';
        });
    }

    window.toggleTheme = function () {
        preference = root.dataset.theme === 'dark' ? 'light' : 'dark';
        try { localStorage.setItem(storageKey, preference); } catch (error) { /* 禁用存储时仍允许当前页面切换。 */ }
        applyTheme();
    };

    applyTheme();
    document.addEventListener('DOMContentLoaded', () => {
        applyTheme();
        document.querySelectorAll('[data-theme-toggle]').forEach(button => {
            button.addEventListener('click', window.toggleTheme);
            if (button.tagName !== 'BUTTON') {
                button.addEventListener('keydown', event => {
                    if (event.key === 'Enter' || event.key === ' ') {
                        event.preventDefault();
                        window.toggleTheme();
                    }
                });
            }
        });
    }, { once: true });
    media.addEventListener('change', () => { if (!preference) applyTheme(); });
    window.addEventListener('storage', event => {
        if (event.key === storageKey || event.key === null) {
            preference = readPreference();
            applyTheme();
        }
    });
})();
