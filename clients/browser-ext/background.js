// Nex Browser Bridge - MV3 Service Worker
// Maintains a resilient WebSocket connection to the local Nex Runner Bridge server
// and routes tab/DOM commands across both daily Chrome and headless=new Chromium.

const DEFAULT_BRIDGE_URL = 'ws://127.0.0.1:19222/ws/bridge';
let ws = null;
let reconnectTimer = null;

async function getBridgeURL() {
  try {
    const stored = await chrome.storage.local.get(['bridgeUrl']);
    if (stored && typeof stored.bridgeUrl === 'string' && stored.bridgeUrl.trim()) {
      return stored.bridgeUrl.trim();
    }
  } catch {
    // ignore
  }
  return DEFAULT_BRIDGE_URL;
}

function scheduleReconnect() {
  if (reconnectTimer) return;
  reconnectTimer = setTimeout(() => {
    reconnectTimer = null;
    void connect();
  }, 3000);
}

async function resolveTargetTab(tabId) {
  if (tabId) {
    const num = Number(tabId);
    if (!Number.isNaN(num)) {
      try {
        return await chrome.tabs.get(num);
      } catch {
        // fallback to active tab
      }
    }
  }
  const tabs = await chrome.tabs.query({ active: true, lastFocusedWindow: true });
  if (tabs.length > 0) return tabs[0];
  const all = await chrome.tabs.query({});
  return all[0] || null;
}

async function handleCommand(cmd) {
  const { id, action, args = {} } = cmd;
  const reply = (ok, data, error) => {
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ id, ok, data, error }));
    }
  };

  try {
    if (action === 'list_tabs') {
      const tabs = await chrome.tabs.query({});
      reply(true, {
        tabs: tabs.map((t) => ({
          tab_id: String(t.id),
          url: t.url || '',
          title: t.title || '',
          active: Boolean(t.active),
        })),
      });
      return;
    }

    if (action === 'open' || action === 'new_tab') {
      const url = String(args.url || 'about:blank');
      if (action === 'open' && !args.new_tab) {
        const target = await resolveTargetTab(args.tab_id);
        if (target && target.id) {
          const updated = await chrome.tabs.update(target.id, { url, active: true });
          reply(true, { tab_id: String(updated.id), url: updated.url || url });
          return;
        }
      }
      const created = await chrome.tabs.create({ url, active: true });
      reply(true, { tab_id: String(created.id), url: created.url || url });
      return;
    }

    if (action === 'switch_tab') {
      const num = Number(args.tab_id);
      const updated = await chrome.tabs.update(num, { active: true });
      reply(true, { tab_id: String(updated.id), url: updated.url || '' });
      return;
    }

    if (action === 'close_tab') {
      const target = await resolveTargetTab(args.tab_id);
      if (target && target.id) {
        await chrome.tabs.remove(target.id);
      }
      reply(true, { closed: true });
      return;
    }

    if (action === 'eval') {
      const target = await resolveTargetTab(args.tab_id);
      if (!target || !target.id) {
        reply(false, null, 'no active tab available');
        return;
      }
      const expr = String(args.expression || '');
      const results = await chrome.scripting.executeScript({
        target: { tabId: target.id },
        world: 'MAIN',
        func: (code) => {
          // eslint-disable-next-line no-eval
          return (0, eval)(code);
        },
        args: [expr],
      });
      reply(true, { value: results?.[0]?.result ?? null });
      return;
    }

    if (action === 'screenshot') {
      const target = await resolveTargetTab(args.tab_id);
      const dataUrl = await chrome.tabs.captureVisibleTab(target?.windowId, { format: 'png' });
      reply(true, { data_url: dataUrl });
      return;
    }

    // Forward DOM actions (snapshot, click, fill, scroll) to content script
    const target = await resolveTargetTab(args.tab_id);
    if (!target || !target.id) {
      reply(false, null, 'no active tab available');
      return;
    }
    const resp = await chrome.tabs.sendMessage(target.id, {
      source: 'nex-bridge',
      action,
      args,
    });
    if (resp && resp.ok) {
      reply(true, resp.data);
    } else {
      reply(false, null, resp?.error || 'content script returned no response');
    }
  } catch (err) {
    reply(false, null, err instanceof Error ? err.message : String(err));
  }
}

async function connect() {
  if (ws && (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING)) {
    return;
  }
  const url = await getBridgeURL();
  try {
    ws = new WebSocket(url);
    ws.onopen = () => {
      ws.send(JSON.stringify({ type: 'hello', client: 'nex-browser-ext', version: '0.1.0' }));
    };
    ws.onmessage = (ev) => {
      try {
        const msg = JSON.parse(ev.data);
        if (msg && msg.id && msg.action) {
          void handleCommand(msg);
        }
      } catch {
        // ignore malformed frames
      }
    };
    ws.onclose = () => {
      ws = null;
      scheduleReconnect();
    };
    ws.onerror = () => {
      try {
        ws?.close();
      } catch {
        // ignore
      }
    };
  } catch {
    ws = null;
    scheduleReconnect();
  }
}

chrome.alarms.create('nex-bridge-keepalive', { periodInMinutes: 1 });
chrome.alarms.onAlarm.addListener((alarm) => {
  if (alarm.name === 'nex-bridge-keepalive') {
    void connect();
  }
});

void connect();
