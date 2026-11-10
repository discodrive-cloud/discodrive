package db

// MigrateTo brings the schema to exactly the given version, for tests of what a migration
// does to the data that was there before it.
func MigrateTo(databaseURL string, version uint) error {
	m, err := newMigrator(databaseURL)
	if err != nil {
		return err
	}
	defer m.Close()
	return m.Migrate(version)
}
