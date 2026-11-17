(function () {
    'use strict';

    const root = document.documentElement;
    const reducedMotion = window.matchMedia
        && window.matchMedia('(prefers-reduced-motion: reduce)').matches;

    root.classList.add('effects-loading');

    function finishPageEntrance() {
        window.setTimeout(() => {
            root.classList.add('effects-ready');
            window.setTimeout(() => {
                root.classList.remove('effects-loading');
            }, reducedMotion ? 0 : 300);
        }, reducedMotion ? 0 : 360);
    }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', finishPageEntrance, { once: true });
    } else {
        finishPageEntrance();
    }

    function getEffectTarget(node) {
        return node instanceof Element
            ? node.closest('button, a, .nav-item, .modal-tab, .close-button, [role="button"]')
            : null;
    }

    function createClickEffect(event, keyboardTriggered) {
        if (reducedMotion) return;

        const target = getEffectTarget(event.target);
        if (!target || target.matches(':disabled, .disabled')) return;

        const rect = target.getBoundingClientRect();
        if (!rect.width || !rect.height) return;

        const x = keyboardTriggered ? rect.width / 2 : event.clientX - rect.left;
        const y = keyboardTriggered ? rect.height / 2 : event.clientY - rect.top;
        const size = Math.max(rect.width, rect.height) * 1.35;

        // effect-host 的 position:relative + overflow:hidden 只对 static 元素安全：
        // 绝对定位按钮（密码眼睛、弹窗关闭按钮）会被强行拉回文档流导致点击丢失、按钮错位；
        // 已定位元素（relative/absolute/fixed/sticky）本身就能作为 ripple 的包含块。
        if (window.getComputedStyle(target).position === 'static') {
            target.classList.add('effect-host');
        }
        const ripple = document.createElement('span');
        ripple.className = 'effect-ripple';
        ripple.style.width = `${size}px`;
        ripple.style.height = `${size}px`;
        ripple.style.left = `${x - size / 2}px`;
        ripple.style.top = `${y - size / 2}px`;
        ripple.addEventListener('animationend', () => ripple.remove(), { once: true });
        target.appendChild(ripple);
        // 表单切换会中断动画，兜底清除已隐藏按钮内的反馈元素。
        window.setTimeout(() => ripple.remove(), 600);

    }

    document.addEventListener('pointerdown', (event) => {
        if (event.isPrimary && event.button === 0) createClickEffect(event, false);
    }, { passive: true });

    document.addEventListener('click', (event) => {
        if (event.detail === 0) createClickEffect(event, true);
    });

    function initializeRevealEffects() {
        if (reducedMotion || !('IntersectionObserver' in window)) return;

        const selector = [
            '#introduction > .container',
            '.content-card',
            '.server-card',
            '.shop-item-card',
            '.status-board',
            '.login-card'
        ].join(',');

        const observer = new IntersectionObserver((entries) => {
            entries.forEach((entry) => {
                if (!entry.isIntersecting) return;
                entry.target.classList.add('effect-visible');
                observer.unobserve(entry.target);
            });
        }, { threshold: 0.08, rootMargin: '0px 0px -28px' });

        function enroll(container) {
            if (!(container instanceof Element) && container !== document) return;
            const nodes = container.matches?.(selector)
                ? [container]
                : Array.from(container.querySelectorAll(selector));
            nodes.forEach((node) => {
                if (node.classList.contains('effect-reveal')) return;
                node.classList.add('effect-reveal');
                observer.observe(node);
            });
        }

        enroll(document);
        const mutationObserver = new MutationObserver((mutations) => {
            mutations.forEach((mutation) => {
                mutation.addedNodes.forEach((node) => enroll(node));
            });
        });
        const observationRoot = document.body || document.documentElement;
        if (observationRoot) {
            mutationObserver.observe(observationRoot, { childList: true, subtree: true });
        }
    }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', initializeRevealEffects, { once: true });
    } else {
        initializeRevealEffects();
    }
})();
