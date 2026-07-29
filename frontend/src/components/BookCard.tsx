import AvailabilityBadge from "./AvailabilityBadge";

interface StoreStatus {
    storeName: string;
    status: string;
}

interface BookCardProps {
    bookName: string;
    seriesName?: string;
    stores: StoreStatus[];
}

export default function BookCard({
    bookName,
    seriesName,
    stores,
}: BookCardProps) {
    return (
        <div className="vestige-window">
            <div className="vestige-titlebar">
                <span>{bookName}</span>
            </div>
            <div className="vestige-window-body">
                <div className="book-card-header">
                    {seriesName && (
                        <span className="book-card-series">{seriesName}</span>
                    )}
                </div>
                <div className="book-card-stores">
                    {stores.length === 0 && (
                        <span className="muted">Not tracked anywhere yet</span>
                    )}
                    {stores.map((s) => (
                        <div key={s.storeName} className="book-card-store-row">
                            <span>{s.storeName}</span>
                            <AvailabilityBadge status={s.status} />
                        </div>
                    ))}
                </div>
            </div>
        </div>
    );
}
