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

// srgbLUT holds SRGBToLinear for every byte value. Byte-input callers hit this decode per pixel
// per channel (target load, RenderFH6, the colour solves), and a math.Pow there is ~200x the cost
// of the lookup — measured 220ms vs 1ms over a 2.6Mpx decode. Filled from the exact formula, so
// results are bit-identical.
var srgbLUT = func() (t [256]float32) {
	for i := range t {
		t[i] = SRGBToLinear(float32(i) / 255)
	}
	return
}()

// SRGBToLinearByte is SRGBToLinear for a stored byte channel, via the table.
func SRGBToLinearByte(b uint8) float32 { return srgbLUT[b] }

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
	if LinearLight && b >= 0 && b < 256 {
		return srgbLUT[b]
	}
	v := float32(b) / 255
	if LinearLight {
		v = SRGBToLinear(v)
	}
	return v
}
