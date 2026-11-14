package oob

import (
	"fmt"
	"os"
	"strings"
	"syscall"
)

// ReadToken reads the Out-of-band controller's token from a file only the
// account running this process can read: root for the controller, the lab
// account for labd (decision S1). A file anyone else could read, a link, or
// a token too short to be one made by the set-up is refused.
func ReadToken(path string) (string, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return "", fmt.Errorf("the Out-of-band controller's token is not at %s: the mini's set-up makes it", path)
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a plain file", path)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return "", fmt.Errorf("%s can be read by others (mode %04o): it must be its owner's alone (0600)", path, perm)
	}
	if st, ok := info.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Geteuid() {
		return "", fmt.Errorf("%s does not belong to the account this runs as (uid %d)", path, os.Geteuid())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(b))
	if len(token) < 32 || strings.ContainsAny(token, " \t\r\n") {
		return "", fmt.Errorf("%s does not hold a token: one line of 32 characters or more", path)
	}
	return token, nil
}
