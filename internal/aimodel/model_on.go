//go:build aimodel

package aimodel

import _ "embed"

// What it buys, measured on the owner's bench at 1000 shapes: against a UNIFORM search given the
// same number of candidates, the network's proposals win in seven pairs out of eight, and the engine
// reaches within ~2-3% of its full-batch error on roughly a quarter of the candidates. It is a
// TRADE, not a free win -- which is why it is not in the release.
//
//go:embed proposer.bin
var Proposer []byte
