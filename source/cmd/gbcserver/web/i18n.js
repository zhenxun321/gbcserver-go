(() => {
  'use strict';

  const storageKey = 'gbcserver-language';
  const dictionaries = [
    window.GBC_TRANSLATIONS_PLAYER || {},
    window.GBC_TRANSLATIONS_ADMIN || {}
  ];
  const translations = Object.assign({}, ...dictionaries);
  const originalText = new WeakMap();
  const renderedText = new WeakMap();
  const originalAttributes = new WeakMap();
  const detectedLanguage = (navigator.language || '').toLowerCase().startsWith('en') ? 'en' : 'zh-CN';
  let language = localStorage.getItem(storageKey) || detectedLanguage;
  if (language !== 'en' && language !== 'zh-CN') language = detectedLanguage;

  function translate(value) {
    if (language !== 'en' || typeof value !== 'string') return value;
    const trimmed = value.trim();
    if (Object.prototype.hasOwnProperty.call(translations, trimmed)) {
      return value.replace(trimmed, translations[trimmed]);
    }
    for (const [template, translated] of Object.entries(translations)) {
      if (!template.includes('${') && !/%[ds]/.test(template)) continue;
      const parts = template.split(/(\$\{[^}]+\}|%[ds])/g);
      const placeholders = parts.filter(part => part.startsWith('${') || /^%[ds]$/.test(part));
      const matcher = new RegExp('^' + parts
        .map(part => part.startsWith('${') || /^%[ds]$/.test(part) ? '([\\s\\S]*?)' : part.replace(/[.*+?^${}()|[\\]\\\\]/g, '\\$&'))
        .join('') + '$');
      const match = trimmed.match(matcher);
      if (!match) continue;
      let output = translated;
      for (let index = 0; index < placeholders.length; index++) {
        output = output.replace(/\$\{[^}]+\}|%[ds]/, match[index + 1]);
      }
      return value.replace(trimmed, output);
    }
    return value;
  }

  function isCustomBranding(node) {
    return Boolean(node.parentElement?.closest('[data-site-field], [data-site-title]'));
  }

  function translateNode(node) {
    if (node.nodeType !== Node.TEXT_NODE || !node.nodeValue.trim()) return;
    if (isCustomBranding(node) || node.parentElement?.closest('[data-user-content], #username-display, .user-name, .player-name, .username, .item-name, .server-name, .dynamic-value')) return;
    if (node.parentElement?.closest('script, style, code, pre, textarea, [contenteditable="true"]')) return;
    const current = node.nodeValue;
    if (!originalText.has(node) || (renderedText.has(node) && renderedText.get(node) !== current)) originalText.set(node, current);
    const translated = translate(originalText.get(node));
    renderedText.set(node, translated);
    if (node.nodeValue !== translated) node.nodeValue = translated;
  }

  function translateAttributes(root = document) {
    const selector = 'input[placeholder], textarea[placeholder], [title], [aria-label], img[alt]';
    const elements = [];
    if (root.nodeType === Node.ELEMENT_NODE && root.matches(selector)) elements.push(root);
    if (root.querySelectorAll) elements.push(...root.querySelectorAll(selector));
    for (const element of elements) {
      if (element.closest('[data-site-field], [data-site-title]')) continue;
      let values = originalAttributes.get(element);
      if (!values) {
        values = new Map();
        originalAttributes.set(element, values);
      }
      for (const name of ['placeholder', 'title', 'aria-label', 'alt']) {
        if (!element.hasAttribute(name)) continue;
        const current = element.getAttribute(name);
        const rendered = values.get(`${name}:rendered`);
        if (!values.has(name) || (rendered !== undefined && rendered !== current)) values.set(name, current);
        element.setAttribute(name, translate(values.get(name)));
        values.set(`${name}:rendered`, element.getAttribute(name));
      }
    }
  }

  function translateTree(root = document) {
    const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
    while (walker.nextNode()) translateNode(walker.currentNode);
    translateAttributes(root);
  }

  function refreshLanguageControls() {
    for (const button of document.querySelectorAll('[data-language-toggle]')) {
      const nextLabel = language === 'en' ? '中文' : 'EN';
      button.textContent = nextLabel;
      button.setAttribute('aria-label', language === 'en' ? '切换到简体中文' : 'Switch to English');
      button.title = language === 'en' ? '切换到简体中文' : 'Switch to English';
    }
  }

  function setLanguage(nextLanguage, persist = true) {
    if (nextLanguage !== 'en' && nextLanguage !== 'zh-CN') return;
    language = nextLanguage;
    document.documentElement.lang = language;
    if (persist) localStorage.setItem(storageKey, language);
    translateTree();
    refreshLanguageControls();
    document.dispatchEvent(new CustomEvent('gbcserver:languagechange', { detail: { language } }));
  }

  window.GBCI18N = Object.freeze({
    t: translate,
    getLanguage: () => language,
    setLanguage,
    locale: () => language === 'en' ? 'en-US' : 'zh-CN'
  });

  const nativeConfirm = window.confirm.bind(window);
  window.confirm = message => nativeConfirm(translate(message));
  const nativeAlert = window.alert.bind(window);
  window.alert = message => nativeAlert(translate(message));

  function initialize() {
    setLanguage(language, false);
    document.addEventListener('click', event => {
      if (!(event.target instanceof Element) || !event.target.closest('[data-language-toggle]')) return;
      setLanguage(language === 'en' ? 'zh-CN' : 'en');
    });
    const observer = new MutationObserver(records => {
      for (const record of records) {
        if (record.type === 'characterData') translateNode(record.target);
        for (const node of record.addedNodes) {
          if (node.nodeType === Node.TEXT_NODE) translateNode(node);
          else if (node.nodeType === Node.ELEMENT_NODE) translateTree(node);
        }
      }
    });
    observer.observe(document.body, { subtree: true, childList: true, characterData: true });
    window.addEventListener('storage', event => {
      if (event.key === storageKey) setLanguage(event.newValue || detectedLanguage, false);
    });
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', initialize, { once: true });
  else initialize();
})();
