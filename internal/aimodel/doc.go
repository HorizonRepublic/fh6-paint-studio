// Package aimodel carries the trained candidate proposer.
//
// It is behind the `aimodel` build tag and is NOT in the shipped release. The measurement never
// justified it as a default -- it trades accuracy for a smaller candidate batch rather than winning
// outright -- and 2.8 MB of high-entropy weights under a private header is, to anything scanning the
// binary, an unidentifiable encrypted payload. Build with -tags aimodel to get it back.
package aimodel

// Present reports whether this build carries the model. The proposer is behind the `aimodel` build
// tag and OUT of the shipped release: it was measured as a trade rather than a win, and 2.8 MB of
// high-entropy weights with a private header is exactly what a static classifier cannot identify
// and therefore treats as an encrypted payload. Callers must check this rather than assume the
// toggle does something -- switching it on without the model would shrink the candidate batch with
// nothing steering what is left, which is strictly worse output.
func Present() bool { return len(Proposer) > 0 }
