// Package aimodel carries the trained candidate proposer inside the binary.
//
// Embedded rather than shipped alongside: the release is two files by design (the GUI and the Vulkan
// shim), and a model that can go missing or fall out of sync with the build is a support problem for
// a feature that is supposed to be a single toggle.
//
// What it buys, measured on the owner's bench at 1000 shapes: against a UNIFORM search given the same
// number of candidates, the network's proposals win in seven pairs out of eight, and the engine
// reaches within ~2-3% of its full-batch error on roughly a quarter of the candidates -- which is
// where the time goes, since scoring is ~96% of a run and linear in the batch. It is a TRADE, not a
// free win, so it lives behind a toggle rather than in the presets.
package aimodel

import _ "embed"

//go:embed proposer.bin
var Proposer []byte
