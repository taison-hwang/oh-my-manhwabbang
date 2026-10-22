package httpapi

import (
	"testing"

	"shelf/internal/index"
)

// Ruling E-56: `SeriesDetail.encoding` is the most suspect of the books'
// labels, by kenc's one order; a book without a verdict is left out, and a
// series with no verdict at all is null.
func TestSeriesEncoding_mostSuspectWinsAndNoVerdictIsNull(t *testing.T) {
	t.Parallel()
	book := func(enc string) index.BookRow { return index.BookRow{Book: index.Book{NameEncoding: enc}} }
	cases := []struct {
		name  string
		books []index.BookRow
		want  string // "" means null
	}{
		{"no books", nil, ""},
		{"no book has a verdict", []index.BookRow{book(""), book("")}, ""},
		{"one label", []index.BookRow{book("utf-8")}, "utf-8"},
		{"a book without a verdict is not counted", []index.BookRow{book(""), book("cp949")}, "cp949"},
		{"cp949 beats utf-8", []index.BookRow{book("utf-8"), book("cp949"), book("utf-8")}, "cp949"},
		{"cp932 beats cp949", []index.BookRow{book("cp949"), book("cp932")}, "cp932"},
		{"utf-8-invalid beats the legacy readings", []index.BookRow{book("cp932"), book("utf-8-invalid")}, "utf-8-invalid"},
		{"unknown beats everything", []index.BookRow{book("utf-8-invalid"), book("unknown"), book("cp949")}, "unknown"},
		{"a value that is not a label is no verdict", []index.BookRow{book("mixed")}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := seriesEncoding(tc.books)
			switch {
			case tc.want == "" && got != nil:
				t.Errorf("seriesEncoding = %q, want null", *got)
			case tc.want != "" && (got == nil || *got != tc.want):
				t.Errorf("seriesEncoding = %v, want %q", got, tc.want)
			}
		})
	}
}
