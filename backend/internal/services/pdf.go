package services

import (
	"fmt"
	"strings"

	"github.com/ledongthuc/pdf"
)

// pageBreak separates pages extracted from a paginated source (PDF). Chunking
// counts these to attribute each chunk to its source page (see ChunkText).
const pageBreak = "\f"

// parsePDF extracts plain text from a PDF, inserting a pageBreak marker between
// pages so downstream chunking can record page numbers for citations.
func parsePDF(path string) (string, error) {
	f, rdr, err := pdf.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	num := rdr.NumPage()
	if num <= 0 {
		return "", fmt.Errorf("pdf has no pages")
	}

	var sb strings.Builder
	for i := 1; i <= num; i++ {
		page := rdr.Page(i)
		text, err := page.GetPlainText(map[string]*pdf.Font{})
		if err != nil {
			text = ""
		}
		if i > 1 {
			sb.WriteString(pageBreak)
		}
		sb.WriteString(text)
	}
	return sb.String(), nil
}
