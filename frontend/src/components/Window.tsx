import type { ReactNode } from "react";

interface WindowProps {
    /** Text shown in the title bar. */
    title: string;
    /** Swap the title bar to the warning (gold) treatment for attention-needed panels. */
    variant?: "default" | "warning";
    /** Optional controls rendered on the right side of the title bar. */
    actions?: ReactNode;
    children: ReactNode;
    className?: string;
}

/**
 * The repeated "retro OS window" idiom — a hard-bordered panel with an
 * offset drop shadow and a colored title bar — as a single component, so
 * every page doesn't hand-roll the same three CSS classes around its forms
 * and tables.
 */
export default function Window({
    title,
    variant = "default",
    actions,
    children,
    className = "",
}: WindowProps) {
    return (
        <section className={`vestige-window ${className}`.trim()}>
            <div
                className={`vestige-titlebar ${
                    variant === "warning" ? "vestige-titlebar-warning" : ""
                }`.trim()}>
                <span>{title}</span>
                {actions && (
                    <div className="vestige-titlebar-actions">{actions}</div>
                )}
            </div>
            <div className="vestige-window-body">{children}</div>
        </section>
    );
}
