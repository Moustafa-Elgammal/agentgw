// Package conformance holds the black-box suite for the gateway's MCP front
// door. It drives the real manifest (examples/city/manifest.yaml) with the
// official MCP client against a backend that verifies request signatures, and
// checks the safety rules in docs/SECURITY-MODEL.md as an agent would see them.
//
// external_test.go adds checks that run against a deployed gateway and a studio's own
// backend, selected by AGENTGW_CONFORMANCE_* environment variables. They skip when the
// variables are unset. See docs/tutorials/07-test-your-integration.md.
package conformance
