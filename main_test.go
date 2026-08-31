package airlockspace

import "testing"

func TestFitImage(t *testing.T) {
	for _, tc := range []struct {
		imgW, imgH, boxW, boxH, wantW, wantH int
	}{
		{100, 50, 50, 50, 50, 25},    // wide image, width-bound
		{50, 100, 50, 50, 25, 50},    // tall image, height-bound
		{10, 10, 100, 100, 100, 100}, // upscale
		{0, 10, 50, 50, 0, 0},        // degenerate input
	} {
		gotW, gotH := fitImage(tc.imgW, tc.imgH, tc.boxW, tc.boxH)
		if gotW != tc.wantW || gotH != tc.wantH {
			t.Errorf("fitImage(%d,%d,%d,%d) = %d,%d; want %d,%d",
				tc.imgW, tc.imgH, tc.boxW, tc.boxH, gotW, gotH, tc.wantW, tc.wantH)
		}
	}
}
