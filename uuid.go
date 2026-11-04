package spi

// UUIDGenerator produces identifiers for stored records.
//
// The return type is [16]byte, so an implementation need not import a UUID
// package. Callers that want the github.com/google/uuid type perform a
// zero-cost type conversion: uuid.UUID(gen.NewTimeUUID()).
//
// Implementations should produce monotonic, time-ordered IDs (v1 UUIDs
// or equivalent) so that sorted IDs correspond to insertion order.
type UUIDGenerator interface {
	NewTimeUUID() [16]byte
}
