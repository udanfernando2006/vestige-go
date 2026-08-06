import { useEffect, useRef, type ReactNode } from "react";

interface DialogProps {
    title: string;
    onClose: () => void;
    children: ReactNode;
    footer?: ReactNode;
}

const FOCUSABLE_SELECTOR =
    'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])';

/**
 * Bespoke modal dialog, styled with the same window/titlebar chrome used
 * everywhere else in this app rather than reaching for a third-party
 * component library. Handles the three accessibility basics by hand:
 *
 * - Escape closes the dialog
 * - Tab/Shift+Tab is trapped to the dialog's own focusable elements
 * - focus returns to whatever triggered the dialog once it closes
 *
 * If this grows more states (nested dialogs, async content, etc.) it may be
 * worth swapping the trap/return logic below for `@radix-ui/react-dialog`'s
 * *unstyled* primitive — that gives the same accessibility guarantees for
 * free while keeping full control over the visual styling here.
 */
export default function Dialog({ title, onClose, children, footer }: DialogProps) {
    const panelRef = useRef<HTMLDivElement>(null);
    const lastFocused = useRef<HTMLElement | null>(null);

    useEffect(() => {
        lastFocused.current = document.activeElement as HTMLElement | null;

        const panel = panelRef.current;
        const focusable = panel
            ? Array.from(panel.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR))
            : [];
        focusable[0]?.focus();

        function handleKeyDown(e: KeyboardEvent) {
            if (e.key === "Escape") {
                e.preventDefault();
                onClose();
                return;
            }
            if (e.key === "Tab" && focusable.length > 0) {
                const first = focusable[0];
                const last = focusable[focusable.length - 1];
                if (e.shiftKey && document.activeElement === first) {
                    e.preventDefault();
                    last.focus();
                } else if (!e.shiftKey && document.activeElement === last) {
                    e.preventDefault();
                    first.focus();
                }
            }
        }

        document.addEventListener("keydown", handleKeyDown);
        return () => {
            document.removeEventListener("keydown", handleKeyDown);
            lastFocused.current?.focus();
        };
        // onClose is stable enough in every call site (state setters / refs);
        // re-running this effect on every render would re-grab focus.
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, []);

    return (
        <div
            className="vestige-overlay"
            role="presentation"
            onMouseDown={(e) => {
                if (e.target === e.currentTarget) onClose();
            }}>
            <div
                className="vestige-window vestige-dialog"
                role="dialog"
                aria-modal="true"
                aria-label={title}
                ref={panelRef}>
                <div className="vestige-titlebar">
                    <span>{title}</span>
                    <button
                        type="button"
                        className="vestige-titlebar-close"
                        onClick={onClose}
                        aria-label="Close">
                        ×
                    </button>
                </div>
                <div className="vestige-dialog-body">{children}</div>
                {footer && <div className="vestige-dialog-footer">{footer}</div>}
            </div>
        </div>
    );
}
