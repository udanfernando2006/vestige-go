import Dialog from "./Dialog";

interface ConfirmDialogProps {
    title: string;
    message: string;
    confirmLabel?: string;
    cancelLabel?: string;
    /** Styles the confirm button with the burgundy "danger" treatment instead of gold. */
    destructive?: boolean;
    onConfirm: () => void;
    onCancel: () => void;
}

export default function ConfirmDialog({
    title,
    message,
    confirmLabel = "Confirm",
    cancelLabel = "Cancel",
    destructive,
    onConfirm,
    onCancel,
}: ConfirmDialogProps) {
    return (
        <Dialog
            title={title}
            onClose={onCancel}
            footer={
                <>
                    <button
                        type="button"
                        className="vestige-btn-secondary"
                        onClick={onCancel}>
                        {cancelLabel}
                    </button>
                    <button
                        type="button"
                        className={destructive ? "vestige-btn-danger" : ""}
                        onClick={onConfirm}>
                        {confirmLabel}
                    </button>
                </>
            }>
            <p>{message}</p>
        </Dialog>
    );
}
