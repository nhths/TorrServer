//go:build gst

package gstreamer

import (
	"net/url"
	"reflect"
	"testing"
)

// HDR-capability parsing and policy decision tests.

func TestParseCaps_HDRFlags(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  []HDRFeature
	}{
		{"none", "hdr=none", []HDRFeature{HDRNone}},
		{"pq-hdr10-10bit", "hdr=pq,hdr10,10bit",
			[]HDRFeature{HDRPQ, HDRHDR10, HDR10Bit}},
		{"hlg-bt2020", "hdr=hlg,bt2020",
			[]HDRFeature{HDRHLG, HDRBT2020}},
		{"unknown dropped", "hdr=dolbyvision,pq,foo",
			[]HDRFeature{HDRPQ}},
		{"empty", "hdr=", nil},
		{"no hdr", "v=h264:hw", nil},
		{"case insensitive", "hdr=PQ,HDR10,10BIT",
			[]HDRFeature{HDRPQ, HDRHDR10, HDR10Bit}},
		{"duplicates dedup", "hdr=pq,pq,hdr10",
			[]HDRFeature{HDRPQ, HDRHDR10}},
		{"whitespace tolerated", "hdr= pq , hdr10 ",
			[]HDRFeature{HDRPQ, HDRHDR10}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q, err := url.ParseQuery(tc.query)
			if err != nil {
				t.Fatal(err)
			}
			_, _, got := ParseCaps(q)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("hdr = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestParseCaps_FullCapsWithHDR(t *testing.T) {
	q, _ := url.ParseQuery("v=h264:hw,h265:sw&a=aac,ac3&hdr=pq,hdr10,10bit")
	v, a, h := ParseCaps(q)
	if len(v) != 2 || v[0].Codec != "h264" || v[1].Codec != "h265" {
		t.Errorf("video = %+v", v)
	}
	if !reflect.DeepEqual(a, []string{"aac", "ac3"}) {
		t.Errorf("audio = %+v", a)
	}
	if !reflect.DeepEqual(h, []HDRFeature{HDRPQ, HDRHDR10, HDR10Bit}) {
		t.Errorf("hdr = %+v", h)
	}
}

func TestCapsDigest_IncludesHDR(t *testing.T) {
	v := []VideoCap{{"h265", TierHw}}
	a := []string{"aac"}
	h1 := []HDRFeature{HDRPQ, HDR10Bit}
	h2 := []HDRFeature{HDR10Bit, HDRPQ}
	if CapsDigest(v, a, h1) != CapsDigest(v, a, h2) {
		t.Errorf("hdr order should not affect digest")
	}
	// Adding hdr to a caps-only digest must change it.
	if CapsDigest(v, a, nil) == CapsDigest(v, a, h1) {
		t.Errorf("digest must change when hdr is added")
	}
}

func TestDecideHDRPolicy_SDRSourcePassthroughAlways(t *testing.T) {
	// Client declares HDR; source is SDR — keep SDR passthrough.
	if DecideHDRPolicy(SourceTransferSDR, []HDRFeature{HDRPQ}) != HDRPolicySDR {
		t.Error("SDR source must return HDRPolicySDR regardless of client")
	}
}

func TestDecideHDRPolicy_ClientNoneForcesTonemap(t *testing.T) {
	if DecideHDRPolicy(SourceTransferPQ, []HDRFeature{HDRNone}) != HDRPolicyTonemap {
		t.Error("client hdr=none must force tonemap")
	}
	if DecideHDRPolicy(SourceTransferHLG, []HDRFeature{HDRNone}) != HDRPolicyTonemap {
		t.Error("client hdr=none with HLG must force tonemap")
	}
}

func TestDecideHDRPolicy_ClientMatchesPassthrough(t *testing.T) {
	if DecideHDRPolicy(SourceTransferPQ, []HDRFeature{HDRPQ}) != HDRPolicyPassthrough {
		t.Error("PQ source + pq-capable client must passthrough")
	}
	if DecideHDRPolicy(SourceTransferPQ, []HDRFeature{HDR10Bit}) != HDRPolicyPassthrough {
		t.Error("PQ source + 10bit-capable client must passthrough")
	}
	if DecideHDRPolicy(SourceTransferHLG, []HDRFeature{HDRHLG}) != HDRPolicyPassthrough {
		t.Error("HLG source + hlg-capable client must passthrough")
	}
	if DecideHDRPolicy(SourceTransferPQ, []HDRFeature{HDRPQ, HDRHDR10}) != HDRPolicyPassthrough {
		t.Error("PQ source with both pq and hdr10 must passthrough")
	}
}

func TestDecideHDRPolicy_HDR10AloneTonemap(t *testing.T) {
	// hdr10 alone (no pq, no 10bit) is insufficient: HDR10 implies
	// PQ but the client may not be able to decode a raw PQ stream
	// without 10-bit sample depth or the PQ transfer function.
	if DecideHDRPolicy(SourceTransferPQ, []HDRFeature{HDRHDR10}) != HDRPolicyTonemap {
		t.Error("hdr10 alone (no pq, no 10bit) must tonemap")
	}
}

func TestDecideHDRPolicy_MissingRequiredForcesTonemap(t *testing.T) {
	// PQ source + hlg-capable (but not pq-capable) → tonemap.
	if DecideHDRPolicy(SourceTransferPQ, []HDRFeature{HDRHLG}) != HDRPolicyTonemap {
		t.Error("HLG client cannot decode PQ source")
	}
	// PQ source + dv-capable only → tonemap (dv != pq).
	if DecideHDRPolicy(SourceTransferPQ, []HDRFeature{HDRDV}) != HDRPolicyTonemap {
		t.Error("DV client cannot decode PQ source")
	}
}

func TestDecideHDRPolicy_EmptyClientHdrUsesConfig(t *testing.T) {
	// No client signal — caller should consult Config.HDRToSDR.
	// DecideHDRPolicy returns Passthrough as the neutral default;
	// NewTask in task.go only applies the decision when clientHdr
	// is non-empty, so empty input never overrides Config.
	if DecideHDRPolicy(SourceTransferPQ, nil) != HDRPolicyPassthrough {
		t.Error("empty clientHdr must not force tonemap (Config.HDRToSDR is the fallback)")
	}
}

func TestSourceTransfer_FromTrack(t *testing.T) {
	cases := []struct {
		transfer string
		want     SourceTransfer
	}{
		{"", SourceTransferSDR},
		{"bt709", SourceTransferSDR},
		{"smpte2084", SourceTransferPQ},
		{"PQ", SourceTransferPQ},
		{"arib-std-b67", SourceTransferHLG},
		{"HLG", SourceTransferHLG},
		{"unknown", SourceTransferSDR},
	}
	for _, tc := range cases {
		got := sourceTransfer(&TrackInfo{VideoTransfer: tc.transfer})
		if got != tc.want {
			t.Errorf("sourceTransfer(%q) = %v, want %v", tc.transfer, got, tc.want)
		}
	}
	if sourceTransfer(nil) != SourceTransferSDR {
		t.Error("sourceTransfer(nil) must return SDR")
	}
}