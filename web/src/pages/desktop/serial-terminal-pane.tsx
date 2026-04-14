import { useCallback, useEffect, useRef } from 'react';
import { AttachAddon } from '@xterm/addon-attach';
import { FitAddon } from '@xterm/addon-fit';
import { Terminal as XtermTerminal } from '@xterm/xterm';
import { useAtomValue, useSetAtom } from 'jotai';
import { XIcon } from 'lucide-react';

import '@xterm/xterm/css/xterm.css';

import { getBaseUrl } from '@/lib/service.ts';
import {
  serialConfigAtom,
  serialConnectCountAtom,
  serialTerminalOpenAtom
} from '@/jotai/serial.ts';
import { validatePicocomParameters } from '@/pages/terminal/validater.ts';

export const SerialTerminalPane = () => {
  const config = useAtomValue(serialConfigAtom);
  const connectCount = useAtomValue(serialConnectCountAtom);
  const setOpen = useSetAtom(serialTerminalOpenAtom);

  const termRef = useRef<HTMLDivElement>(null);
  const xtermRef = useRef<XtermTerminal | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const picocomRunningRef = useRef(false);

  const cleanup = useCallback(() => {
    if (wsRef.current && picocomRunningRef.current) {
      wsRef.current.send('\x01\x18');
      picocomRunningRef.current = false;
    }
    setTimeout(() => {
      if (wsRef.current?.readyState === WebSocket.OPEN) {
        wsRef.current.close();
      }
      wsRef.current = null;
    }, 100);
    if (xtermRef.current) {
      xtermRef.current.dispose();
      xtermRef.current = null;
    }
  }, []);

  useEffect(() => {
    if (!termRef.current || connectCount === 0) return;

    cleanup();

    const terminal = new XtermTerminal({ cursorBlink: true });
    const fitAddon = new FitAddon();
    terminal.loadAddon(fitAddon);
    terminal.open(termRef.current);
    fitAddon.fit();
    xtermRef.current = terminal;

    const url = `${getBaseUrl('ws')}/api/vm/terminal`;
    const ws = new WebSocket(url);
    wsRef.current = ws;

    ws.onopen = () => {
      const attachAddon = new AttachAddon(ws);
      terminal.loadAddon(attachAddon);

      const windowSize = { rows: terminal.rows, cols: terminal.cols };
      ws.send(new Blob([JSON.stringify(windowSize)], { type: 'application/json' }));

      setTimeout(() => {
        const params = {
          port: config.port,
          baud: String(config.baudrate),
          parity: config.parity,
          flowControl: config.flowControl,
          dataBits: String(config.dataBits),
          stopBits: String(config.stopBits)
        };

        if (!validatePicocomParameters(params)) return;

        ws.send(
          `picocom ${config.port} --baud ${config.baudrate} --parity ${config.parity} --flow ${config.flowControl} --databits ${config.dataBits} --stopbits ${config.stopBits}\r`
        );
        picocomRunningRef.current = true;
      }, 300);
    };

    const resizeHandler = () => {
      fitAddon.fit();
      const windowSize = { rows: terminal.rows, cols: terminal.cols };
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(new Blob([JSON.stringify(windowSize)], { type: 'application/json' }));
      }
    };

    window.addEventListener('resize', resizeHandler);

    return () => {
      window.removeEventListener('resize', resizeHandler);
      cleanup();
    };
  }, [connectCount]);

  return (
    <div className="flex h-full flex-col bg-neutral-950">
      <div className="flex items-center justify-between border-b border-neutral-800 px-3 py-1.5">
        <span className="text-sm text-neutral-300">
          Serial: {config.port} @ {config.baudrate}
        </span>
        <button
          className="rounded p-1 text-neutral-400 hover:bg-neutral-800 hover:text-neutral-200"
          onClick={() => setOpen(false)}
        >
          <XIcon size={14} />
        </button>
      </div>
      <div ref={termRef} className="min-h-0 flex-1 p-2" />
    </div>
  );
};
