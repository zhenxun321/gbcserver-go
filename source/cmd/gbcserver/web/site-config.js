(() => {
  async function applySiteBranding() {
    try {
      const response = await fetch('/api/publicConfig', { cache: 'no-store' });
      if (!response.ok) return;
      const data = await response.json();
      const branding = data?.branding;
      if (!branding || typeof branding !== 'object') return;

      document.querySelectorAll('[data-site-field]').forEach(element => {
        const value = branding[element.dataset.siteField];
        if (typeof value === 'string') element.textContent = value;
      });

      const titleKey = document.querySelector('title')?.dataset.siteTitle;
      if (titleKey && typeof branding[titleKey] === 'string') {
        document.title = branding[titleKey];
      }
    } catch (_) {
    }
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', applySiteBranding, { once: true });
  } else {
    applySiteBranding();
  }
})();
