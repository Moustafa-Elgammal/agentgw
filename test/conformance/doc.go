// Package conformance holds the black-box suite for the gateway's MCP front
// door. It drives the real manifest (examples/city/manifest.json) with the
// official MCP client against a backend that verifies request signatures, and
// checks the safety rules in docs/SECURITY-MODEL.md as an agent would see them.
package conformance
