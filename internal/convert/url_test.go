package convert

import "testing"

func TestExtractPresentationID(t *testing.T) {
	const id = "1xJ5l_tVuJ9blIAgRBFJwLGe0Ojx2OzNj9QBRKYo9TpA"
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"edit url", "https://docs.google.com/presentation/d/" + id + "/edit", id, false},
		{"url with fragment", "https://docs.google.com/presentation/d/" + id + "/edit#slide=id.p1", id, false},
		{"multi-account url", "https://docs.google.com/presentation/u/1/d/" + id + "/edit", id, false},
		{"bare url no suffix", "https://docs.google.com/presentation/d/" + id, id, false},
		{"bare id", id, id, false},
		{"surrounding spaces", "  " + id + "  ", id, false},
		{"empty", "", "", true},
		{"garbage", "not a presentation", "", true},
		{"too-short id", "abc123", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExtractPresentationID(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ExtractPresentationID(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("ExtractPresentationID(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
