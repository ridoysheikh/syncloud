import { Component, type ErrorInfo, type ReactNode } from "react";
import { AlertTriangle, RotateCw } from "lucide-react";
import { Button } from "./controls";

/**
 * What a view shows when it failed to render: the error and a retry, in place
 * of the view, so the rest of the page (its tabs, the nav) keeps working.
 */
export function ErrorView({
  error,
  onRetry,
}: {
  error: unknown;
  onRetry?: () => void;
}) {
  const message = error instanceof Error ? error.message : String(error);
  return (
    <div
      role="alert"
      className="flex flex-col items-center justify-center gap-1.5 px-4 py-10 text-center"
    >
      <AlertTriangle className="text-bad size-6" strokeWidth={1.5} />
      <div className="text-sm font-medium">This view failed to load</div>
      <div className="text-muted max-w-md text-xs break-words">
        Something on this page went wrong. The rest of the dashboard still
        works.
      </div>
      {message && (
        <code className="bg-raised border-line text-muted mt-1 max-w-xl rounded-sm border px-2 py-1 font-mono text-xs break-all">
          {message}
        </code>
      )}
      {onRetry && (
        <Button className="mt-2" onClick={onRetry}>
          <RotateCw className="size-3.5" /> Try again
        </Button>
      )}
    </div>
  );
}

/**
 * Catches render errors below it and shows ErrorView. A new `resetKey` (the
 * tab, the page) clears the error, so moving on never needs a reload.
 */
export class ErrorBoundary extends Component<
  { resetKey?: unknown; children: ReactNode },
  { error: unknown; key: unknown }
> {
  state = { error: null as unknown, key: this.props.resetKey };

  static getDerivedStateFromError(error: unknown) {
    return { error };
  }

  static getDerivedStateFromProps(
    props: { resetKey?: unknown },
    state: { error: unknown; key: unknown },
  ) {
    return props.resetKey !== state.key
      ? { error: null, key: props.resetKey }
      : null;
  }

  componentDidCatch(error: unknown, info: ErrorInfo) {
    console.error("View crashed:", error, info.componentStack);
  }

  render() {
    if (this.state.error != null)
      return (
        <ErrorView
          error={this.state.error}
          onRetry={() => this.setState({ error: null })}
        />
      );
    return this.props.children;
  }
}
