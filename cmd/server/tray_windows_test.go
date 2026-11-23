//go:build windows

package main

import (
	"runtime"
	"testing"
	"unsafe"
)

func TestWindowsTrayStructSizes(t *testing.T) {
	if runtime.GOARCH != "amd64" {
		t.Skip("exact size assertions target windows/amd64")
	}
	if got := unsafe.Sizeof(wndClassExW{}); got != 80 {
		t.Fatalf("sizeof(WNDCLASSEXW) = %d, want 80", got)
	}
	if got := unsafe.Sizeof(winPoint{}); got != 8 {
		t.Fatalf("sizeof(POINT) = %d, want 8", got)
	}
	if got := unsafe.Sizeof(winMsg{}); got != 48 {
		t.Fatalf("sizeof(MSG) = %d, want 48", got)
	}
	if got := unsafe.Sizeof(notifyIconDataW{}); got != 976 {
		t.Fatalf("sizeof(NOTIFYICONDATAW) = %d, want 976", got)
	}
}
