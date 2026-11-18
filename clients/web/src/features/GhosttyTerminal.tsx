import { useEffect, useRef, useState } from 'react';
import { wsClient } from '../core/wsClient';

let ghosttyInitPromise: Promise<typeof import('ghostty-web')> | null = null;

function ensureGhosttyLoaded() {
  if (!ghosttyInitPromise) {
    ghosttyInitPromise = import('ghostty-web').then(async (mod) => {
      await mod.init();
      return mod;
    });
  }
  return ghosttyInitPromise;
}

export function GhosttyTerminal({
  ptyId,
  runnerId,
}: {
  ptyId: string;
  runnerId: string;
}) {
  const containerRef = useRef<HTMLDivElement | null>(null);
  const [fallbackMode, setFallbackMode] = useState<string | null>(null);

  useEffect(() => {
    const el = containerRef.current;
    if (!el || !ptyId) return;

    let disposed = false;
    let unsubOutput: (() => void) | null = null;
    let resizeObs: ResizeObserver | null = null;
    let termInstance: { dispose: () => void } | null = null;

    ensureGhosttyLoaded()
      .then((ghostty) => {
        if (disposed || !containerRef.current) return;
        const term = new ghostty.Terminal({
          fontSize: 13,
          fontFamily: 'JetBrains Mono, Menlo, Consolas, monospace',
          cursorBlink: true,
          theme: {
            background: '#09090b',
            foreground: '#e4e4e7',
            cursor: '#3b82f6',
          },
        });
        const fitAddon = new ghostty.FitAddon();
        term.loadAddon(fitAddon);
        term.open(containerRef.current);
        fitAddon.fit();
        termInstance = term;

        term.onData((data: string) => {
          wsClient.sendPTYStdin(ptyId, data);
        });

        term.onResize(({ cols, rows }: { cols: number; rows: number }) => {
          wsClient.sendEnvelope('pty.resize', { pty_id: ptyId, cols, rows }, { runner_id: runnerId });
        });

        unsubOutput = wsClient.onPTYOutput(ptyId, (bytes: Uint8Array) => {
          term.write(bytes);
        });

        // Re-attach to trigger server RingBuffer history replay if reconnecting
        wsClient
          .requestRPC<{ pty_id: string; cols: number; rows: number }, { history?: string }>(
            'pty.open',
            { pty_id: ptyId, cols: 100, rows: 24 },
            { runner_id: runnerId },
          )
          .then((resp) => {
            if (resp.payload?.history) {
              const raw = Uint8Array.from(atob(resp.payload.history), (c) => c.charCodeAt(0));
              term.write(raw);
            }
          })
          .catch(() => {});

        if (typeof ResizeObserver !== 'undefined') {
          resizeObs = new ResizeObserver(() => {
            try {
              fitAddon.fit();
            } catch {
              // ignore transient layout resize
            }
          });
          resizeObs.observe(containerRef.current);
        }
      })
      .catch((err) => {
        if (!disposed) {
          setFallbackMode(`libghostty WASM unavailable in current environment (${(err as Error).message})`);
        }
      });

    return () => {
      disposed = true;
      unsubOutput?.();
      resizeObs?.disconnect();
      termInstance?.dispose();
    };
  }, [ptyId, runnerId]);

  if (!ptyId) {
    return (
      <div className="flex h-full items-center justify-center text-xs text-zinc-500">
        Click + to launch a remote Ghostty PTY session on the selected Runner
      </div>
    );
  }

  if (fallbackMode) {
    return <div className="p-3 font-mono text-xs text-zinc-400">{fallbackMode}</div>;
  }

  return <div ref={containerRef} className="h-full w-full overflow-hidden bg-zinc-950 p-1" />;
}
