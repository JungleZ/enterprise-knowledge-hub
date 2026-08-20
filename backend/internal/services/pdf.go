package services

import (
	"io"

	"github.com/ledongthuc/pdf"
)

// parsePDF extracts plain text from a PDF using the pure-Go extractor.
func parsePDF(path string) (string, error) {
	f, rdr, err := pdf.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	reader, err := rdr.GetPlainText()
	if err != nil {
		return "", err
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
