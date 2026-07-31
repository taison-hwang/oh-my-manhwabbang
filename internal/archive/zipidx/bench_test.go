package zipidx_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"

	"shelf/internal/archive"
	"shelf/internal/archive/zipidx"
	"shelf/internal/testutil"
)

// Benchmarks have no *testing.T, so these two thin wrappers keep the call
// sites identical to the tests'.
func readCD(r io.ReaderAt, size int64) (*archive.Index, error) {
	return zipidx.ReadCentralDirectory(context.Background(), r, size)
}

func openEntry(r io.ReaderAt, ref archive.EntryRef) (io.ReadCloser, error) {
	return zipidx.OpenEntry(context.Background(), r, ref)
}

// impl-plan §6.4 names BenchmarkCentralDir and BenchmarkOpenEntry.
//
// BenchmarkOpenEntry is the one that matters: it exists to prove AC-008 and
// FR-SRV-002 as a *scaling* property. Extracting one page must cost the same
// whether the archive holds 10 pages or 1 540 — that is the difference between
// "seek to the stored offset" and "walk the archive", and it is the difference
// the whole zipidx deviation (E-2) was granted for.

func BenchmarkCentralDir(b *testing.B) {
	for _, pages := range []int{10, 100, 1000} {
		data, _ := bigArchive(b, pages, 512, testutil.MethodStore)
		r := bytes.NewReader(data)
		size := int64(len(data))
		b.Run(fmt.Sprintf("pages=%d", pages), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(size)
			for b.Loop() {
				if _, err := readCD(r, size); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkOpenEntry_scaling reads the *last* page of archives of increasing
// size. Flat ns/op across the sizes is the claim; a linear curve would mean
// something is walking the container.
func BenchmarkOpenEntry_scaling(b *testing.B) {
	for _, pages := range []int{10, 100, 1000, 4000} {
		data, _ := bigArchive(b, pages, 4096, testutil.MethodStore)
		r := bytes.NewReader(data)
		ix, err := readCD(r, int64(len(data)))
		if err != nil {
			b.Fatal(err)
		}
		ref := ix.Entries[len(ix.Entries)-1].Ref()

		b.Run(fmt.Sprintf("pages=%d/bytes=%d", pages, len(data)), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				rc, err := openEntry(r, ref)
				if err != nil {
					b.Fatal(err)
				}
				if _, err := io.Copy(io.Discard, rc); err != nil {
					b.Fatal(err)
				}
				if err := rc.Close(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkOpenEntry_deflate is the same read through the inflate path, so a
// regression in stream setup shows up separately from the seek itself.
func BenchmarkOpenEntry_deflate(b *testing.B) {
	data, _ := bigArchive(b, 500, 4096, testutil.MethodDeflate)
	r := bytes.NewReader(data)
	ix, err := readCD(r, int64(len(data)))
	if err != nil {
		b.Fatal(err)
	}
	ref := ix.Entries[len(ix.Entries)/2].Ref()

	b.ReportAllocs()
	b.SetBytes(ref.Size)
	for b.Loop() {
		rc, err := openEntry(r, ref)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := io.Copy(io.Discard, rc); err != nil {
			b.Fatal(err)
		}
		if err := rc.Close(); err != nil {
			b.Fatal(err)
		}
	}
}
