package application

// GetRequest identifies an owner-scoped record.
type GetRequest struct {
	OwnerID int64
	ID      string
}

// ListRequest describes an owner-scoped page query.
type ListRequest struct {
	OwnerID int64
	Page    int
	Size    int
}

// CreateRequest describes a new owner-scoped record.
type CreateRequest struct {
	OwnerID int64
	Title   string
}

// UpdateRequest describes an owner-scoped record update.
type UpdateRequest struct {
	OwnerID int64
	ID      string
	Title   string
}

// DeleteRequest identifies an owner-scoped record to delete.
type DeleteRequest struct {
	OwnerID int64
	ID      string
}

// DeleteManyRequest identifies owner-scoped records to delete atomically.
type DeleteManyRequest struct {
	OwnerID int64
	IDs     []string
}
