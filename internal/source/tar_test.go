// SPDX-License-Identifier: MPL-2.0

package source

import (
	"archive/tar"
	"bytes"
	"io"
	"regexp"
	"strings"
	"testing"
)

func sampleFiles() []File {
	long := strings.Repeat("deep/", 30) + "file.txt" // past the 100-byte name field
	return []File{
		{Path: "bin/run.sh", Exec: true, Data: []byte("#!/bin/sh\necho hi\n")},
		{Path: "empty.txt", Data: nil},
		{Path: long, Data: []byte("long")},
		{Path: "src/naïve-ünïcode.py", Data: []byte("x = 1\n")},
		{Path: "task.yaml", Data: []byte("slug: t\nruntime: python\n")},
	}
}

func TestTarRoundTrip(t *testing.T) {
	files := sampleFiles()
	archive, err := Pack(files)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(bytes.NewReader(archive))
	for i := 0; ; i++ {
		hdr, err := tr.Next()
		if err == io.EOF {
			if i != len(files) {
				t.Fatalf("%d members, want %d", i, len(files))
			}
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		want := files[i]
		data, _ := io.ReadAll(tr)
		if hdr.Name != want.Path || !bytes.Equal(data, want.Data) || hdr.Typeflag != tar.TypeReg {
			t.Fatalf("member %d: %q %q type %c, want %q %q", i, hdr.Name, data, hdr.Typeflag, want.Path, want.Data)
		}
		wantMode := int64(modePlain)
		if want.Exec {
			wantMode = modeExec
		}
		if hdr.Mode != wantMode {
			t.Fatalf("%s: mode %o, want %o", hdr.Name, hdr.Mode, wantMode)
		}
		if strings.Contains(hdr.Name, `\`) || strings.HasPrefix(hdr.Name, "/") {
			t.Fatalf("%s: not a POSIX relative path", hdr.Name)
		}
	}
}

func TestTarIsDeterministic(t *testing.T) {
	a, err := Pack(sampleFiles())
	if err != nil {
		t.Fatal(err)
	}
	b, err := Pack(sampleFiles())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("two packs of the same files differ")
	}
	digest, err := Digest(sampleFiles())
	if err != nil {
		t.Fatal(err)
	}
	if digest != ArchiveDigest(a) {
		t.Fatal("the streamed digest differs from the archive's digest")
	}
}

func TestDigestChangesWithContentPathAndExecBit(t *testing.T) {
	base := []File{{Path: "a.sh", Data: []byte("x")}}
	variants := [][]File{
		{{Path: "a.sh", Data: []byte("y")}},
		{{Path: "b.sh", Data: []byte("x")}},
		{{Path: "a.sh", Exec: true, Data: []byte("x")}},
	}
	want, _ := Digest(base)
	for _, v := range variants {
		if got, _ := Digest(v); got == want {
			t.Errorf("%+v: same digest as the base", v)
		}
	}
}

// octalSize is a tar header size field the server reads: octal digits only.
var octalSize = regexp.MustCompile(`^[0-7]*$`)

// TestTarFormatTheServerAccepts checks the raw headers: every member is a
// regular file or a PAX record, never a GNU long-name member, and every size
// field is octal.
func TestTarFormatTheServerAccepts(t *testing.T) {
	archive, err := Pack(sampleFiles())
	if err != nil {
		t.Fatal(err)
	}
	const block = 512
	sawPAX := false
	for off := 0; off+block <= len(archive); {
		hdr := archive[off : off+block]
		off += block
		if bytes.Count(hdr, []byte{0}) == block {
			break
		}
		size := strings.TrimSpace(strings.TrimRight(string(hdr[124:136]), "\x00"))
		if !octalSize.MatchString(size) {
			t.Fatalf("size field %q is not octal", size)
		}
		switch flag := hdr[156]; flag {
		case tar.TypeReg:
		case tar.TypeXHeader:
			sawPAX = true
		default:
			t.Fatalf("member type %q is not a regular file or a PAX record", flag)
		}
		var n int64
		for _, c := range size {
			n = n*8 + int64(c-'0')
		}
		off += int((n + block - 1) / block * block)
	}
	if !sawPAX {
		t.Fatal("the non-ASCII name must be written as a PAX record")
	}
}

// TestTarHeaders checks how a name is written: a long ASCII name that splits
// at a "/" is a USTAR header with a prefix; a non-ASCII name is a PAX record
// before its member.
func TestTarHeaders(t *testing.T) {
	long := strings.Repeat("dir/", 30) + "file.txt"
	archive, err := Pack([]File{{Path: long, Data: []byte("x")}})
	if err != nil {
		t.Fatal(err)
	}
	hdr := archive[:tarBlock]
	if hdr[156] != tar.TypeReg || string(hdr[257:262]) != "ustar" {
		t.Fatalf("long name: type %q magic %q, want a USTAR regular file", hdr[156], hdr[257:262])
	}
	prefix := strings.TrimRight(string(hdr[345:500]), "\x00")
	name := strings.TrimRight(string(hdr[0:100]), "\x00")
	if prefix == "" || prefix+"/"+name != long {
		t.Fatalf("long name: prefix %q name %q", prefix, name)
	}

	archive, err = Pack([]File{{Path: "naïve.py", Data: []byte("x")}})
	if err != nil {
		t.Fatal(err)
	}
	if archive[156] != tar.TypeXHeader || !bytes.Contains(archive[tarBlock:2*tarBlock], []byte("path=naïve.py\n")) {
		t.Fatal("a non-ASCII name must be a PAX path record before its member")
	}
	if archive[2*tarBlock+156] != tar.TypeReg {
		t.Fatal("the PAX record must be followed by the regular file member")
	}
}

func TestArchivedSizeCountsHeadersAndPadding(t *testing.T) {
	files := []File{{Path: "a", Data: make([]byte, 1)}, {Path: "b", Data: make([]byte, 512)}, {Path: "c"}}
	archive, err := Pack(files)
	if err != nil {
		t.Fatal(err)
	}
	want := int64(archiveTrailerBytes)
	for _, f := range files {
		want += archivedSize(int64(len(f.Data)))
	}
	// The archive is exactly the counted members plus the two end blocks.
	if int64(len(archive)) != want || want != 1024+1024+1024+512 {
		t.Fatalf("archive %d bytes, counted %d", len(archive), want)
	}
}
