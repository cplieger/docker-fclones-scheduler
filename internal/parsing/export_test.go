package parsing

// Test-only re-exports. These parse helpers have no production caller outside
// this package (each is used only by its single higher-level parser:
// ParseStats, ParseDuplicateGroups, ParseActionSummary), so they are
// unexported in the shipped package. This file is compiled only under
// `go test`, so it re-exposes them to the black-box parsing_test package and
// the per-primitive fuzz targets without widening the package's public API.
var (
	ParseRedundantSize = parseRedundantSize
	IsGroupHeader      = isGroupHeader
	ExtractGroupSize   = extractGroupSize
	ParseHumanBytes    = parseHumanBytes
)
