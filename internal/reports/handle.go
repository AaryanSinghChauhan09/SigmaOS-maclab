package reports

// HandleShaped reports whether text has the shape of a GitHub handle as the
// lab keeps one: lowercase letters, digits and single hyphens, a hyphen at
// neither end, at most 39 characters.
func HandleShaped(text string) bool {
	if len(text) == 0 || len(text) > 39 {
		return false
	}
	alnum := func(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') }
	for i := 0; i < len(text); i++ {
		switch {
		case alnum(text[i]):
		case text[i] == '-' && i > 0 && i+1 < len(text) && alnum(text[i+1]):
		default:
			return false
		}
	}
	return true
}
