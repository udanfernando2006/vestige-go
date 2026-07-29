import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import KebabMenu from "./KebabMenu";

describe("KebabMenu", () => {
    it("does not show the menu until the trigger is clicked", () => {
        render(<KebabMenu items={[{ label: "Delete", onClick: vi.fn() }]} />);
        expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    });

    it("opens the menu on trigger click and shows all items", () => {
        render(
            <KebabMenu
                items={[
                    { label: "Edit details", onClick: vi.fn() },
                    { label: "Delete", onClick: vi.fn(), destructive: true },
                ]}
            />,
        );
        fireEvent.click(screen.getByRole("button", { name: "Actions" }));
        expect(screen.getByRole("menu")).toBeInTheDocument();
        expect(screen.getByText("Edit details")).toBeInTheDocument();
        expect(screen.getByText("Delete")).toBeInTheDocument();
    });

    it("calls the item's onClick and closes the menu", () => {
        const onDelete = vi.fn();
        render(<KebabMenu items={[{ label: "Delete", onClick: onDelete }]} />);
        fireEvent.click(screen.getByRole("button", { name: "Actions" }));
        fireEvent.click(screen.getByText("Delete"));
        expect(onDelete).toHaveBeenCalledTimes(1);
        expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    });

    it("closes the menu on outside click without calling any item", () => {
        const onClick = vi.fn();
        render(
            <div>
                <KebabMenu items={[{ label: "Delete", onClick }]} />
                <button>outside</button>
            </div>,
        );
        fireEvent.click(screen.getByRole("button", { name: "Actions" }));
        expect(screen.getByRole("menu")).toBeInTheDocument();

        fireEvent.mouseDown(screen.getByText("outside"));
        expect(screen.queryByRole("menu")).not.toBeInTheDocument();
        expect(onClick).not.toHaveBeenCalled();
    });

    it("closes the menu on Escape", () => {
        render(<KebabMenu items={[{ label: "Delete", onClick: vi.fn() }]} />);
        fireEvent.click(screen.getByRole("button", { name: "Actions" }));
        expect(screen.getByRole("menu")).toBeInTheDocument();

        fireEvent.keyDown(document, { key: "Escape" });
        expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    });

    it("respects a custom accessible label", () => {
        render(
            <KebabMenu
                label="Actions for The Last Wish"
                items={[{ label: "Delete", onClick: vi.fn() }]}
            />,
        );
        expect(
            screen.getByRole("button", { name: "Actions for The Last Wish" }),
        ).toBeInTheDocument();
    });
});
