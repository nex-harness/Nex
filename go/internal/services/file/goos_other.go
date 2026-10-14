//go:build !windows

package file

// goosWindows is a var off Windows so tests could flip it if the Windows
// path conversion ever needs a non-Windows test.
var goosWindows = false
