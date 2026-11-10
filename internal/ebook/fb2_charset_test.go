package ebook

import (
	"testing"

	"golang.org/x/text/encoding/charmap"
)

// Most Russian FB2 libraries ship windows-1251 files; the title must come out as UTF-8.
func TestParseFB2_Windows1251(t *testing.T) {
	utf8Doc := `<?xml version="1.0" encoding="windows-1251"?>
<FictionBook xmlns="http://www.gribuser.ru/xml/fictionbook/2.0">
  <description><title-info>
    <author><first-name>Лев</first-name><last-name>Толстой</last-name></author>
    <book-title>Война и мир</book-title>
  </title-info></description>
  <body><section><p>Текст</p></section></body>
</FictionBook>`
	data, err := charmap.Windows1251.NewEncoder().Bytes([]byte(utf8Doc))
	if err != nil {
		t.Fatal(err)
	}
	m, err := parseFB2Bytes(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.Title != "Война и мир" {
		t.Errorf("title = %q, want %q", m.Title, "Война и мир")
	}
	if len(m.Authors) != 1 || m.Authors[0].Name != "Лев Толстой" {
		t.Errorf("authors = %+v, want [Лев Толстой]", m.Authors)
	}
}
