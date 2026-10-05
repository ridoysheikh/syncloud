// One shared WebSocket to /api/v1/stream for the whole dashboard (§5.1).
// Components subscribe by topic; the connection reconnects with backoff.
import { useEffect, useSyncExternalStore } from "react";

export interface StreamEvent<T = unknown> {
  topic: string;
  at: string;
  data: T;
}

type Listener = (e: StreamEvent) => void;
export type StreamState = "idle" | "connecting" | "open" | "closed";

const listeners = new Map<string, Set<Listener>>();
const stateListeners = new Set<() => void>();
let state: StreamState = "idle";
let ws: WebSocket | null = null;
let retry = 0;
let retryTimer: ReturnType<typeof setTimeout> | undefined;
let wanted = false;

function setState(s: StreamState) {
  state = s;
  stateListeners.forEach((l) => l());
}

function connect() {
  if (!wanted || ws) return;
  setState("connecting");
  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  const sock = new WebSocket(`${proto}//${location.host}/api/v1/stream`);
  ws = sock;
  sock.onopen = () => {
    retry = 0;
    setState("open");
  };
  sock.onmessage = (msg) => {
    let e: StreamEvent;
    try {
      e = JSON.parse(msg.data as string);
    } catch {
      return;
    }
    listeners.get(e.topic)?.forEach((l) => l(e));
    listeners.get("*")?.forEach((l) => l(e));
  };
  sock.onclose = () => {
    ws = null;
    setState("closed");
    if (!wanted) return;
    const delay = Math.min(30_000, 500 * 2 ** retry++) * (0.75 + Math.random() * 0.5);
    retryTimer = setTimeout(connect, delay);
  };
}

/** Opens the stream (after sign-in). */
export function startStream() {
  wanted = true;
  connect();
}

/** Closes the stream (on sign-out). */
export function stopStream() {
  wanted = false;
  clearTimeout(retryTimer);
  ws?.close();
  ws = null;
  setState("idle");
}

export function subscribe(topic: string, l: Listener): () => void {
  let set = listeners.get(topic);
  if (!set) listeners.set(topic, (set = new Set()));
  set.add(l);
  return () => set.delete(l);
}

/** Calls handler for each event on topic. handler should be stable (useCallback) or cheap to re-subscribe. */
export function useStreamTopic<T>(topic: string, handler: (e: StreamEvent<T>) => void) {
  useEffect(() => subscribe(topic, handler as Listener), [topic, handler]);
}

export function useStreamState(): StreamState {
  return useSyncExternalStore(
    (cb) => {
      stateListeners.add(cb);
      return () => stateListeners.delete(cb);
    },
    () => state,
  );
}
