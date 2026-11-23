package main

import (
	"bytes"
	"image"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"sync"
)

type rollingLogWriter struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	file     *os.File
	size     int64
}

func newRollingLogWriter(path string, maxBytes int64) (*rollingLogWriter, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	var size int64
	if info, sErr := f.Stat(); sErr == nil {
		size = info.Size()
	}
	return &rollingLogWriter{path: path, maxBytes: maxBytes, file: f, size: size}, nil
}

func (w *rollingLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return len(p), nil
	}
	if w.maxBytes > 0 && w.size+int64(len(p)) > w.maxBytes {
		_ = w.file.Close()
		_ = os.Remove(w.path + ".1")
		_ = os.Rename(w.path, w.path+".1")
		f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			w.file = nil
			return 0, err
		}
		w.file = f
		w.size = 0
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *rollingLogWriter) Close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file != nil {
		_ = w.file.Close()
		w.file = nil
	}
}

func resizePNGForIcon(raw []byte, target int) []byte {
	src, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return raw
	}
	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w <= 0 || h <= 0 || target <= 0 {
		return raw
	}
	if w <= target && h <= target {
		return raw
	}
	srcRGBA := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(srcRGBA, srcRGBA.Bounds(), src, bounds.Min, draw.Src)

	dst := image.NewRGBA(image.Rect(0, 0, target, target))
	for y := 0; y < target; y++ {
		sy0 := y * h / target
		sy1 := (y + 1) * h / target
		if sy1 <= sy0 {
			sy1 = sy0 + 1
		}
		if sy1 > h {
			sy1 = h
		}
		for x := 0; x < target; x++ {
			sx0 := x * w / target
			sx1 := (x + 1) * w / target
			if sx1 <= sx0 {
				sx1 = sx0 + 1
			}
			if sx1 > w {
				sx1 = w
			}
			var rSum, gSum, bSum, aSum, count uint32
			for sy := sy0; sy < sy1; sy++ {
				rowOff := sy * srcRGBA.Stride
				for sx := sx0; sx < sx1; sx++ {
					off := rowOff + sx*4
					rSum += uint32(srcRGBA.Pix[off])
					gSum += uint32(srcRGBA.Pix[off+1])
					bSum += uint32(srcRGBA.Pix[off+2])
					aSum += uint32(srcRGBA.Pix[off+3])
					count++
				}
			}
			if count > 0 {
				doff := y*dst.Stride + x*4
				dst.Pix[doff] = uint8(rSum / count)
				dst.Pix[doff+1] = uint8(gSum / count)
				dst.Pix[doff+2] = uint8(bSum / count)
				dst.Pix[doff+3] = uint8(aSum / count)
			}
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, dst); err != nil {
		return raw
	}
	return out.Bytes()
}
