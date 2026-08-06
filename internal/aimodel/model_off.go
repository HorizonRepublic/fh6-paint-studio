//go:build !aimodel

package aimodel

// No model in this build. Build with -tags aimodel to embed it.
var Proposer []byte
