package catalog

// SetFirstYear lets a test start the read at a year it serves, instead of
// walking from 1900.
func SetFirstYear(s *Service, year int) { s.firstYear = year }
