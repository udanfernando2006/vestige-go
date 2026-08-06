import { useCallback, useRef, useState, type ReactNode } from "react";
import ConfirmDialog from "../components/ConfirmDialog";

interface ConfirmOptions {
    title?: string;
    confirmLabel?: string;
    cancelLabel?: string;
    destructive?: boolean;
}

interface ConfirmState extends ConfirmOptions {
    message: string;
}

/**
 * Drop-in replacement for `window.confirm()`, styled with the rest of the
 * app instead of the browser's native dialog.
 *
 * Usage:
 *   const { confirm, dialog } = useConfirm();
 *   ...
 *   async function handleDelete() {
 *     if (!(await confirm("Delete this store?", { destructive: true }))) return;
 *     ...
 *   }
 *   ...
 *   return <div>{dialog}{...rest of the page...}</div>;
 */
export function useConfirm(): { confirm: (message: string, options?: ConfirmOptions) => Promise<boolean>; dialog: ReactNode } {
    const [state, setState] = useState<ConfirmState | null>(null);
    const resolverRef = useRef<((value: boolean) => void) | undefined>(
        undefined,
    );

    const confirm = useCallback((message: string, options?: ConfirmOptions) => {
        setState({ message, ...options });
        return new Promise<boolean>((resolve) => {
            resolverRef.current = resolve;
        });
    }, []);

    function resolve(value: boolean) {
        setState(null);
        resolverRef.current?.(value);
    }

    const dialog = state ? (
        <ConfirmDialog
            title={state.title ?? "Confirm"}
            message={state.message}
            confirmLabel={state.confirmLabel ?? "Confirm"}
            cancelLabel={state.cancelLabel ?? "Cancel"}
            destructive={state.destructive}
            onConfirm={() => resolve(true)}
            onCancel={() => resolve(false)}
        />
    ) : null;

    return { confirm, dialog };
}
