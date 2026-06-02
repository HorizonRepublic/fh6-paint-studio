package model

import "math"

// LinearLight, when true, makes the engine treat its working float colours as LINEAR light — the
// space the livery editor composites in (gamma ≈2.2, straight alpha). Default false keeps the
// plain sRGB/byte behaviour unchanged; the CLI -linear flag flips it once at startup. The
// conversions live only at the byte<->float colour boundaries (EncByte / DecChan), so the
// backend float maths is untouched.
var LinearLight bool

// SRGBToLinear maps an sRGB-encoded channel in 0..1 to linear light (standard sRGB EOTF, piecewise).
func SRGBToLinear(c float32) float32 {
	if c <= 0.04045 {
		return c / 12.92
	}
	return float32(math.Pow((float64(c)+0.055)/1.055, 2.4))
}

// LinearToSRGB maps a linear-light channel in 0..1 to sRGB encoding (standard sRGB OETF, piecewise).
func LinearToSRGB(c float32) float32 {
	if c <= 0.0031308 {
		return c * 12.92
	}
	return float32(1.055*math.Pow(float64(c), 1.0/2.4) - 0.055)
}

// EncByte converts a working-space colour channel (0..1 float) to its stored sRGB byte. In linear
// mode the working value is linear and is sRGB-encoded first; otherwise it is already sRGB. Alpha
// must NOT go through here — it is stored straight (use F2B).
func EncByte(v float32) int {
	if LinearLight {
		v = LinearToSRGB(v)
	}
	return F2B(v)
}

// DecChan converts a stored sRGB byte colour channel to a working-space float. In linear mode the
// stored byte is sRGB and is decoded to linear; otherwise it stays sRGB. Inverse of EncByte. Alpha
// is straight (divide by 255 directly).
func DecChan(b int) float32 {
	v := float32(b) / 255
	if LinearLight {
		v = SRGBToLinear(v)
	}
	return v
}
