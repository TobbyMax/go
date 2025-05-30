// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package poll_test

import (
	"errors"
	"internal/poll"
	"internal/syscall/windows"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"unsafe"
)

func init() {
	poll.InitWSA()
}

func TestWSASocketConflict(t *testing.T) {
	t.Parallel()
	s, err := windows.WSASocket(syscall.AF_INET, syscall.SOCK_STREAM, syscall.IPPROTO_TCP, nil, 0, windows.WSA_FLAG_OVERLAPPED)
	if err != nil {
		t.Fatal(err)
	}
	fd := poll.FD{Sysfd: s, IsStream: true, ZeroReadIsEOF: true}
	if err = fd.Init("tcp", true); err != nil {
		syscall.CloseHandle(s)
		t.Fatal(err)
	}
	defer fd.Close()

	const SIO_TCP_INFO = syscall.IOC_INOUT | syscall.IOC_VENDOR | 39
	inbuf := uint32(0)
	var outbuf _TCP_INFO_v0
	cbbr := uint32(0)

	var ov syscall.Overlapped
	// Create an event so that we can efficiently wait for completion
	// of a requested overlapped I/O operation.
	ov.HEvent, _ = windows.CreateEvent(nil, 0, 0, nil)
	if ov.HEvent == 0 {
		t.Fatalf("could not create the event!")
	}
	defer syscall.CloseHandle(ov.HEvent)

	if err = fd.WSAIoctl(
		SIO_TCP_INFO,
		(*byte)(unsafe.Pointer(&inbuf)),
		uint32(unsafe.Sizeof(inbuf)),
		(*byte)(unsafe.Pointer(&outbuf)),
		uint32(unsafe.Sizeof(outbuf)),
		&cbbr,
		&ov,
		0,
	); err != nil && !errors.Is(err, syscall.ERROR_IO_PENDING) {
		t.Fatalf("could not perform the WSAIoctl: %v", err)
	}

	if err != nil && errors.Is(err, syscall.ERROR_IO_PENDING) {
		// It is possible that the overlapped I/O operation completed
		// immediately so there is no need to wait for it to complete.
		if res, err := syscall.WaitForSingleObject(ov.HEvent, syscall.INFINITE); res != 0 {
			t.Fatalf("waiting for the completion of the overlapped IO failed: %v", err)
		}
	}
}

type _TCP_INFO_v0 struct {
	State             uint32
	Mss               uint32
	ConnectionTimeMs  uint64
	TimestampsEnabled bool
	RttUs             uint32
	MinRttUs          uint32
	BytesInFlight     uint32
	Cwnd              uint32
	SndWnd            uint32
	RcvWnd            uint32
	RcvBuf            uint32
	BytesOut          uint64
	BytesIn           uint64
	BytesReordered    uint32
	BytesRetrans      uint32
	FastRetrans       uint32
	DupAcksIn         uint32
	TimeoutEpisodes   uint32
	SynRetrans        uint8
}

func newFD(t testing.TB, h syscall.Handle, kind string, overlapped bool) *poll.FD {
	fd := poll.FD{
		Sysfd:         h,
		IsStream:      true,
		ZeroReadIsEOF: true,
	}
	err := fd.Init(kind, overlapped)
	if overlapped && err != nil {
		// Overlapped file handles should not error.
		fd.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		fd.Close()
	})
	return &fd
}

func newFile(t testing.TB, name string, overlapped bool) *poll.FD {
	namep, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		t.Fatal(err)
	}
	flags := syscall.FILE_ATTRIBUTE_NORMAL
	if overlapped {
		flags |= syscall.FILE_FLAG_OVERLAPPED
	}
	h, err := syscall.CreateFile(namep,
		syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_READ,
		nil, syscall.OPEN_ALWAYS, uint32(flags), 0)
	if err != nil {
		t.Fatal(err)
	}
	typ, err := syscall.GetFileType(h)
	if err != nil {
		syscall.CloseHandle(h)
		t.Fatal(err)
	}
	kind := "file"
	if typ == syscall.FILE_TYPE_PIPE {
		kind = "pipe"
	}
	return newFD(t, h, kind, overlapped)
}

func BenchmarkReadOverlapped(b *testing.B) {
	benchmarkRead(b, true)
}

func BenchmarkReadSync(b *testing.B) {
	benchmarkRead(b, false)
}

func benchmarkRead(b *testing.B, overlapped bool) {
	name := filepath.Join(b.TempDir(), "foo")
	const content = "hello world"
	err := os.WriteFile(name, []byte(content), 0644)
	if err != nil {
		b.Fatal(err)
	}
	file := newFile(b, name, overlapped)
	var buf [len(content)]byte
	for b.Loop() {
		_, err := io.ReadFull(file, buf[:])
		if err != nil {
			b.Fatal(err)
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			b.Fatal(err)
		}
	}
}
