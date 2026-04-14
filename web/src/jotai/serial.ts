import { atom } from 'jotai';

export type SerialConfig = {
  port: string;
  baudrate: number;
  parity: string;
  flowControl: string;
  dataBits: number;
  stopBits: number;
};

export const serialConfigAtom = atom<SerialConfig>({
  port: '/dev/ttyS1',
  baudrate: 115200,
  parity: 'none',
  flowControl: 'none',
  dataBits: 8,
  stopBits: 1
});

export const serialTerminalOpenAtom = atom(false);

// Incremented to signal the terminal to reconnect with new config
export const serialConnectCountAtom = atom(0);
