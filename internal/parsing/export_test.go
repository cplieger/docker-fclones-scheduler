package parsing

// Test-only re-exports. parseHumanBytes has no production caller outside this
// package (it is used only by ParseActionSummary's metric extraction), so it
// is unexported in the shipped package. This file is compiled only under
// `go test`, so it re-exposes the helper to the black-box parsing_test
// package and its fuzz target without widening the package's public API.
var ParseHumanBytes = parseHumanBytes
