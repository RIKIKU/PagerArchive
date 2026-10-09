package decoder

import "testing"

func TestMultimon(t *testing.T) {
	tests := []struct {
		line string
		ok   bool
		want Record
	}{
		{
			line: "POCSAG512: Address:  100001  Function: 0  Alpha:   @@EX0000001 HSF1 SMOKE",
			ok:   true,
			want: Record{Capcode: "0100001", Function: 0, Baud: 512, Text: "@@EX0000001 HSF1 SMOKE"},
		},
		{
			line: "POCSAG1200: Address:      42  Function: 3  Alpha:      leading spaces kept\r\n",
			ok:   true,
			want: Record{Capcode: "0000042", Function: 3, Baud: 1200, Text: "   leading spaces kept"},
		},
		{
			line: "POCSAG2400: Address: 1234567  Function: 1  Numeric: 0123 456",
			ok:   true,
			want: Record{Capcode: "1234567", Function: 1, Baud: 2400, Numeric: true, Text: "0123 456"},
		},
		{
			line: "POCSAG512: Address:  100001  Function: 0  Certainty:    12  Alpha:   x",
			ok:   true,
			want: Record{Capcode: "0100001", Baud: 512, Text: "x"},
		},
		{line: "POCSAG512: Address:  100001  Function: 0 ", ok: false},           // tone-only
		{line: "POCSAG512: Address:  100001  Function: 0  Alpha:   ", ok: false}, // empty
		{line: "POCSAG512: Address:       -  Function: -  Alpha:   x", ok: false},
		{line: "POCSAG512: Address:  100001  Function: 0  Skyper:  x", ok: false},
		{line: "FLEX: something else", ok: false},
		{line: "", ok: false},
	}
	for _, tt := range tests {
		got, ok := Multimon{}.Decode(tt.line)
		if ok != tt.ok || got != tt.want {
			t.Errorf("Decode(%q) = %+v, %v; want %+v, %v", tt.line, got, ok, tt.want, tt.ok)
		}
	}
}
