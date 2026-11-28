// Nex Browser Bridge - Content Script (injected into all frames, including nested iframes)
// Provides fast in-page DOM snapshot, element ref resolution, and batch actions without CDP round-trips.

(() => {
  if (globalThis.__nexBridgeInstalled) return;
  globalThis.__nexBridgeInstalled = true;

  const refMap = new Map();

  function isVisible(el) {
    if (!el || !(el instanceof Element)) return false;
    const style = window.getComputedStyle(el);
    if (style.display === 'none' || style.visibility === 'hidden' || style.opacity === '0') {
      return false;
    }
    const rect = el.getBoundingClientRect();
    return rect.width > 0 && rect.height > 0;
  }

  function collectInteractive(root, out, maxElements, includeHidden) {
    const selector = [
      'a[href]',
      'button',
      'input:not([type="hidden"])',
      'textarea',
      'select',
      '[role="button"]',
      '[role="link"]',
      '[role="textbox"]',
      '[role="checkbox"]',
      '[role="radio"]',
      '[role="tab"]',
      '[role="menuitem"]',
      '[contenteditable="true"]',
    ].join(',');

    const nodes = root.querySelectorAll(selector);
    for (const el of nodes) {
      if (out.length >= maxElements) break;
      if (!includeHidden && !isVisible(el)) continue;

      const ref = `e${out.length + 1}`;
      refMap.set(ref, el);

      const tag = el.tagName.toLowerCase();
      const role = el.getAttribute('role') || (tag === 'a' ? 'link' : tag === 'button' ? 'button' : tag);
      const name = (
        el.getAttribute('aria-label') ||
        el.getAttribute('placeholder') ||
        el.getAttribute('title') ||
        (el.innerText || el.textContent || '')
      )
        .replace(/\s+/g, ' ')
        .trim()
        .slice(0, 120);

      const item = { ref, tag, role, name };
      if (tag === 'a' && el.getAttribute('href')) {
        item.href = el.getAttribute('href');
      }
      if ('value' in el && el.type !== 'password' && typeof el.value === 'string' && el.value) {
        item.value = el.value.slice(0, 120);
      }
      out.push(item);
    }

    // Traverse open Shadow DOMs
    const all = root.querySelectorAll('*');
    for (const el of all) {
      if (out.length >= maxElements) break;
      if (el.shadowRoot) {
        collectInteractive(el.shadowRoot, out, maxElements, includeHidden);
      }
    }
  }

  function resolveElement(args) {
    if (args.ref && refMap.has(args.ref)) {
      const el = refMap.get(args.ref);
      if (el && el.isConnected) return el;
    }
    if (args.selector) {
      return document.querySelector(args.selector);
    }
    return null;
  }

  chrome.runtime.onMessage.addListener((msg, _sender, sendResponse) => {
    if (!msg || msg.source !== 'nex-bridge') return false;
    const { action, args = {} } = msg;

    try {
      if (action === 'snapshot') {
        refMap.clear();
        const maxElements = Math.min(Math.max(Number(args.max_elements) || 80, 10), 300);
        const includeHidden = Boolean(args.include_hidden);
        const elements = [];
        collectInteractive(document, elements, maxElements, includeHidden);
        const textPreview = (document.body?.innerText || '')
          .replace(/\s+/g, ' ')
          .trim()
          .slice(0, 4000);
        sendResponse({
          ok: true,
          data: {
            url: location.href,
            title: document.title,
            is_top_frame: window.top === window,
            elements,
            text_preview: textPreview,
          },
        });
        return false;
      }

      if (action === 'click') {
        const el = resolveElement(args);
        if (!el) {
          sendResponse({ ok: false, error: `element not found (${args.ref || args.selector})` });
          return false;
        }
        el.scrollIntoView({ block: 'center', inline: 'center' });
        el.click();
        sendResponse({ ok: true, data: { clicked: args.ref || args.selector } });
        return false;
      }

      if (action === 'fill') {
        const el = resolveElement(args);
        if (!el) {
          sendResponse({ ok: false, error: `element not found (${args.ref || args.selector})` });
          return false;
        }
        el.focus();
        const val = String(args.value ?? '');
        if ('value' in el) {
          el.value = val;
        } else if (el.isContentEditable) {
          el.textContent = val;
        }
        el.dispatchEvent(new Event('input', { bubbles: true }));
        el.dispatchEvent(new Event('change', { bubbles: true }));
        sendResponse({ ok: true, data: { filled: args.ref || args.selector } });
        return false;
      }

      if (action === 'scroll') {
        const dir = args.direction || 'down';
        const dx = Number(args.delta_x) || (dir === 'left' ? -500 : dir === 'right' ? 500 : 0);
        const dy = Number(args.delta_y) || (dir === 'up' ? -500 : dir === 'down' ? 500 : 0);
        window.scrollBy({ left: dx, top: dy, behavior: 'instant' });
        sendResponse({ ok: true, data: { scrollX: window.scrollX, scrollY: window.scrollY } });
        return false;
      }

      sendResponse({ ok: false, error: `unsupported content action: ${action}` });
    } catch (err) {
      sendResponse({ ok: false, error: String(err) });
    }
    return false;
  });
})();
