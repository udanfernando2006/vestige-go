import { useEffect, useRef, useState } from "react";

export interface KebabMenuItem {
    label: string;
    onClick: () => void;
    destructive?: boolean;
    disabled?: boolean;
}

interface KebabMenuProps {
    items: KebabMenuItem[];
    /** Accessible label for the trigger button, e.g. "Actions for The Hobbit" */
    label?: string;
}

/**
 * A minimal "⋮" trigger that opens a small anchored menu of actions.
 * Used wherever a row/header has more actions than there's room to show
 * as explicit buttons (Books table rows, Series headers).
 */
export default function KebabMenu({ items, label = "Actions" }: KebabMenuProps) {
    const [open, setOpen] = useState(false);
    const ref = useRef<HTMLDivElement>(null);

    useEffect(() => {
        function handleClickOutside(e: MouseEvent) {
            if (ref.current && !ref.current.contains(e.target as Node)) {
                setOpen(false);
            }
        }
        function handleEscape(e: KeyboardEvent) {
            if (e.key === "Escape") setOpen(false);
        }
        if (open) {
            document.addEventListener("mousedown", handleClickOutside);
            document.addEventListener("keydown", handleEscape);
        }
        return () => {
            document.removeEventListener("mousedown", handleClickOutside);
            document.removeEventListener("keydown", handleEscape);
        };
    }, [open]);

    return (
        <div className="vestige-kebab" ref={ref}>
            <button
                type="button"
                className="vestige-kebab-trigger"
                aria-label={label}
                aria-haspopup="menu"
                aria-expanded={open}
                onClick={() => setOpen((v) => !v)}
            >
                ⋮
            </button>
            {open && (
                <div className="vestige-kebab-menu" role="menu">
                    {items.map((item, i) => (
                        <button
                            key={i}
                            type="button"
                            role="menuitem"
                            disabled={item.disabled}
                            className={
                                item.destructive
                                    ? "vestige-kebab-item vestige-kebab-item-danger"
                                    : "vestige-kebab-item"
                            }
                            onClick={() => {
                                setOpen(false);
                                item.onClick();
                            }}
                        >
                            {item.label}
                        </button>
                    ))}
                </div>
            )}
        </div>
    );
}
