package billingmonth

import "regexp"

var re = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)

// Valid reports whether s is a well-formed YYYY-MM billing month.
func Valid(s string) bool { return re.MatchString(s) }
