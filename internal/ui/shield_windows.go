//go:build windows

package ui

import (
	"image"
	"unsafe"

	"golang.org/x/sys/windows"
)

// loadShield extracts the system UAC shield icon (premultiplied *image.RGBA) via
// SHGetStockIconInfo -> GetIconInfo -> GetDIBits. Returns nil on any failure (the admin
// button then falls back to text).
func loadShield() image.Image {
	const (
		siidShield     = 77
		shgsiIcon      = 0x000000100
		shgsiLargeIcon = 0x000000000
	)
	var sii shStockIconInfo
	sii.cbSize = uint32(unsafe.Sizeof(sii))
	if r, _, _ := procSHGetStockIconInfo.Call(uintptr(siidShield), uintptr(shgsiIcon|shgsiLargeIcon), uintptr(unsafe.Pointer(&sii))); r != 0 || sii.hIcon == 0 {
		return nil
	}
	hIcon := sii.hIcon
	defer procDestroyIcon.Call(uintptr(hIcon))

	var ii iconInfo
	if r, _, _ := procGetIconInfo.Call(uintptr(hIcon), uintptr(unsafe.Pointer(&ii))); r == 0 {
		return nil
	}
	if ii.hbmColor != 0 {
		defer procDeleteObject.Call(uintptr(ii.hbmColor))
	}
	if ii.hbmMask != 0 {
		defer procDeleteObject.Call(uintptr(ii.hbmMask))
	}
	if ii.hbmColor == 0 {
		return nil
	}

	var bm bitmapStruct
	if r, _, _ := procGetObject.Call(uintptr(ii.hbmColor), unsafe.Sizeof(bm), uintptr(unsafe.Pointer(&bm))); r == 0 {
		return nil
	}
	w, h := int(bm.bmWidth), int(bm.bmHeight)
	if w <= 0 || h <= 0 || w > 256 || h > 256 {
		return nil
	}

	hdc, _, _ := procCreateCompatibleDC.Call(0)
	if hdc == 0 {
		return nil
	}
	defer procDeleteDC.Call(hdc)

	var bih bitmapInfoHeader
	bih.Size = uint32(unsafe.Sizeof(bih))
	bih.Width = int32(w)
	bih.Height = -int32(h) // top-down
	bih.Planes = 1
	bih.BitCount = 32
	bih.Compression = 0 // BI_RGB
	buf := make([]byte, w*h*4)
	if r, _, _ := procGetDIBits.Call(hdc, uintptr(ii.hbmColor), 0, uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&bih)), 0); r == 0 {
		return nil
	}

	// HICON color bits are premultiplied BGRA -> *image.RGBA (premultiplied) with R/B swapped.
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	anyAlpha := false
	for i := 0; i < w*h; i++ {
		b, g, r, a := buf[i*4+0], buf[i*4+1], buf[i*4+2], buf[i*4+3]
		if a != 0 {
			anyAlpha = true
		}
		img.Pix[i*4+0], img.Pix[i*4+1], img.Pix[i*4+2], img.Pix[i*4+3] = r, g, b, a
	}
	if !anyAlpha { // icons without an alpha channel: make colored pixels opaque
		for i := 0; i < w*h; i++ {
			if img.Pix[i*4]|img.Pix[i*4+1]|img.Pix[i*4+2] != 0 {
				img.Pix[i*4+3] = 255
			}
		}
	}
	return img
}

// --- Win32 structs + procs ---

type shStockIconInfo struct {
	cbSize         uint32
	hIcon          windows.Handle
	iSysImageIndex int32
	iIcon          int32
	szPath         [windows.MAX_PATH]uint16
}

type iconInfo struct {
	fIcon    int32
	xHotspot uint32
	yHotspot uint32
	hbmMask  windows.Handle
	hbmColor windows.Handle
}

type bitmapStruct struct {
	bmType       int32
	bmWidth      int32
	bmHeight     int32
	bmWidthBytes int32
	bmPlanes     uint16
	bmBitsPixel  uint16
	bmBits       uintptr
}

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

var (
	shell32 = windows.NewLazySystemDLL("shell32.dll")
	user32  = windows.NewLazySystemDLL("user32.dll")
	gdi32   = windows.NewLazySystemDLL("gdi32.dll")

	procSHGetStockIconInfo = shell32.NewProc("SHGetStockIconInfo")
	procGetIconInfo        = user32.NewProc("GetIconInfo")
	procDestroyIcon        = user32.NewProc("DestroyIcon")
	procGetObject          = gdi32.NewProc("GetObjectW")
	procCreateCompatibleDC = gdi32.NewProc("CreateCompatibleDC")
	procGetDIBits          = gdi32.NewProc("GetDIBits")
	procDeleteObject       = gdi32.NewProc("DeleteObject")
	procDeleteDC           = gdi32.NewProc("DeleteDC")
)
