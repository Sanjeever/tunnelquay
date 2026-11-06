package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestArchiveContentsAndMode(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "program")
	if err := os.WriteFile(source, []byte("真实归档内容"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, isZip := range []bool{true, false} {
		name := "tunnelquay-client"
		dest := filepath.Join(dir, "package.tar.gz")
		if isZip {
			dest = filepath.Join(dir, "package.zip")
			name += ".exe"
		}
		if err := archive(source, name, dest, isZip); err != nil {
			t.Fatal(err)
		}
		if isZip {
			z, err := zip.OpenReader(dest)
			if err != nil {
				t.Fatal(err)
			}
			if len(z.File) != 1 || z.File[0].Name != name || z.File[0].Mode().Perm() != 0755 {
				t.Fatal("ZIP 文件名或权限错误")
			}
			r, err := z.File[0].Open()
			if err != nil {
				t.Fatal(err)
			}
			b, err := io.ReadAll(r)
			r.Close()
			z.Close()
			if err != nil || string(b) != "真实归档内容" {
				t.Fatal("ZIP 内容错误", err)
			}
		} else {
			f, err := os.Open(dest)
			if err != nil {
				t.Fatal(err)
			}
			gz, err := gzip.NewReader(f)
			if err != nil {
				t.Fatal(err)
			}
			tr := tar.NewReader(gz)
			h, err := tr.Next()
			if err != nil || h.Name != name || h.Mode != 0755 {
				t.Fatal("TAR 文件名或权限错误", err)
			}
			b, err := io.ReadAll(tr)
			if err != nil || string(b) != "真实归档内容" {
				t.Fatal("TAR 内容错误", err)
			}
			if _, err = tr.Next(); err != io.EOF {
				t.Fatal("TAR 存在多余内容", err)
			}
			gz.Close()
			f.Close()
		}
	}
}
