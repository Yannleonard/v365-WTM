// Minimal ambient typings for guacamole-common-js (Apache Guacamole JS client).
//
// The published package ships no .d.ts. We declare only the surface the console
// panel consumes: WebSocketTunnel, Client, Mouse, Keyboard, plus the display +
// the status object passed to onstatechange/onerror. This is intentionally a
// partial model — enough to keep tsc strict happy without pretending to type the
// whole library.

declare module "guacamole-common-js" {
  /** A transport tunnel (we use the websocket implementation). */
  export class Tunnel {
    connect(data?: string): void;
    disconnect(): void;
    sendMessage(...elements: unknown[]): void;
  }

  /** WebSocket-backed tunnel. Pass the full ws:// or wss:// URL. */
  export class WebSocketTunnel extends Tunnel {
    constructor(tunnelURL: string);
  }

  /** A Guacamole status code/message (delivered to onerror / onstatechange). */
  export class Status {
    code: number;
    message?: string;
    isError(): boolean;
  }

  /** The remote display surface. */
  export class Display {
    /** The root element to attach into the DOM. */
    getElement(): HTMLDivElement;
    getWidth(): number;
    getHeight(): number;
    /** Scale the rendered display (1 = native). */
    scale(scale: number): void;
    getScale(): number;
    onresize: ((width: number, height: number) => void) | null;
  }

  /** Mouse state delivered to Mouse event handlers. */
  export interface MouseState {
    x: number;
    y: number;
    left: boolean;
    middle: boolean;
    right: boolean;
    up: boolean;
    down: boolean;
  }

  /** Captures mouse events over an element and exposes them as MouseState. */
  export class Mouse {
    constructor(element: HTMLElement);
    onmousedown: ((state: MouseState) => void) | null;
    onmouseup: ((state: MouseState) => void) | null;
    onmousemove: ((state: MouseState) => void) | null;
    onEachEvent?: (handler: (state: MouseState) => void) => void;
  }

  /** Captures keyboard events on an element/document. */
  export class Keyboard {
    constructor(element: HTMLElement | Document);
    onkeydown: ((keysym: number) => boolean | void) | null;
    onkeyup: ((keysym: number) => void) | null;
    /** Detach all listeners this keyboard installed. */
    reset(): void;
  }

  /** The remote-desktop client; drives a Display over a Tunnel. */
  export class Client {
    constructor(tunnel: Tunnel);
    getDisplay(): Display;
    connect(data?: string): void;
    disconnect(): void;
    sendMouseState(state: MouseState): void;
    sendKeyEvent(pressed: number, keysym: number): void;
    sendSize(width: number, height: number): void;
    onstatechange: ((state: number) => void) | null;
    onerror: ((status: Status) => void) | null;
    onname: ((name: string) => void) | null;
  }

  const Guacamole: {
    Tunnel: typeof Tunnel;
    WebSocketTunnel: typeof WebSocketTunnel;
    Status: typeof Status;
    Display: typeof Display;
    Mouse: typeof Mouse;
    Keyboard: typeof Keyboard;
    Client: typeof Client;
  };

  export default Guacamole;
}
