import { useEffect, useRef, useState } from "react";
import { Terminal as XTerm } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";

const css = (name: string) => getComputedStyle(document.documentElement).getPropertyValue(name).trim();

/**
 * An interactive shell in a task (§10, kubectl exec), over the exec WebSocket:
 * binary frames carry terminal I/O, text frames carry resize and exit.
 */
export function Terminal({ taskId, command = ["sh"] }: { taskId: string; command?: string[] }) {
  const el = useRef<HTMLDivElement>(null);
  const [state, setState] = useState<"connecting" | "open" | "closed">("connecting");

  useEffect(() => {
    const term = new XTerm({
      fontFamily: css("--font-mono") || "monospace",
      fontSize: 12,
      cursorBlink: true,
      theme: { background: css("--color-bg"), foreground: css("--color-fg"), cursor: css("--color-accent") },
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(el.current!);
    fit.fit();

    const q = new URLSearchParams({ tty: "1", cols: String(term.cols), rows: String(term.rows) });
    command.forEach((c) => q.append("command", c));
    const proto = location.protocol === "https:" ? "wss" : "ws";
    const ws = new WebSocket(`${proto}://${location.host}/api/v1/tasks/${taskId}/exec?${q}`);
    ws.binaryType = "arraybuffer";
    const enc = new TextEncoder();
    ws.onopen = () => {
      setState("open");
      term.focus();
    };
    ws.onmessage = (e) => {
      if (typeof e.data !== "string") {
        term.write(new Uint8Array(e.data as ArrayBuffer));
        return;
      }
      const m = JSON.parse(e.data) as { type: string; code?: number; message?: string };
      if (m.type === "exit") term.write(`\r\n\x1b[2m[process exited with code ${m.code ?? 0}]\x1b[0m\r\n`);
      if (m.type === "error") term.write(`\r\n\x1b[31m${m.message}\x1b[0m\r\n`);
    };
    ws.onclose = () => setState("closed");
    const input = term.onData((d) => ws.readyState === WebSocket.OPEN && ws.send(enc.encode(d)));
    const resize = term.onResize(({ cols, rows }) => ws.readyState === WebSocket.OPEN && ws.send(JSON.stringify({ type: "resize", cols, rows })));
    const ro = new ResizeObserver(() => fit.fit());
    ro.observe(el.current!);
    return () => {
      ro.disconnect();
      input.dispose();
      resize.dispose();
      ws.close();
      term.dispose();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [taskId]);

  return (
    <div className="flex flex-col gap-1">
      <div ref={el} className="bg-bg border-line h-[60vh] rounded-sm border p-1" />
      <span className="text-faint text-xs">{state === "connecting" ? "Connecting…" : state === "open" ? "Connected" : "Session ended"}</span>
    </div>
  );
}
