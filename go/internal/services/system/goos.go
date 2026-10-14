package system

// runtimeGOOS mirrors the TS process.platform checks; a var so tests can
// exercise the Windows resolver logic on any platform.
var runtimeGOOS = goos

const goos = goosValue
