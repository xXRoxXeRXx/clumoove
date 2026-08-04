package db

// Shared test UUID constants used across the db package test suite.
// All values are valid RFC-4122 UUIDs so they can be stored in UUID columns
// without an explicit ::uuid cast in every INSERT statement.
const (
	testUserUUID    = "00000000-0000-0000-0000-000000000001"
	testMigrationID = "00000000-0000-0000-0000-000000000002"
	testTaskID      = "00000000-0000-0000-0000-000000000003"
)
