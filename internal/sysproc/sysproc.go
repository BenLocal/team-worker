// Package sysproc holds the few process operations that differ between Unix
// and Windows, so the CLI builds for every platform. Local agents and the
// daemon are Unix features; on Windows these calls degrade to the closest
// equivalent, which is enough for the remote HTTP client.
package sysproc
