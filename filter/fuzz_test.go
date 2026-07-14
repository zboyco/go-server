package filter

import "testing"

// FuzzBeginEndSplit checks arbitrary marker streams for panics and invalid advances.
func FuzzBeginEndSplit(f *testing.F) {
	f.Add([]byte("!$payload$!"))
	f.Add([]byte("garbage"))
	f.Add([]byte("!$partial"))
	split := (&BeginEndMarkReceiveFilter{Begin: []byte("!$"), End: []byte("$!")}).SplitFunc()
	f.Fuzz(func(t *testing.T, data []byte) {
		advance, _, _ := split(data, true)
		if advance < 0 || advance > len(data) {
			t.Fatalf("advance = %d for %d bytes", advance, len(data))
		}
	})
}

// FuzzFixedHeaderDecode checks arbitrary fixed-header packets for safe bounds handling.
func FuzzFixedHeaderDecode(f *testing.F) {
	f.Add([]byte{0, 0, 0, 8, 0, 0, 0, 0})
	f.Add([]byte{0, 0, 0, 4})
	split := (&FixedHeaderReceiveFilter{}).SplitFunc()
	resolve := (&FixedHeaderReceiveFilter{}).ResolveAction()
	f.Fuzz(func(t *testing.T, data []byte) {
		advance, token, _ := split(data, true)
		if advance < 0 || advance > len(data) {
			t.Fatalf("advance = %d for %d bytes", advance, len(data))
		}
		if token != nil {
			_, _, _ = resolve(token)
		}
	})
}
