package main

import (
	"strings"
	"testing"
)

const sampleAMFI = `Scheme Code;ISIN Div Payout/ ISIN Growth;ISIN Div Reinvestment;Scheme Name;Net Asset Value;Date

Open Ended Schemes(Equity Scheme - ELSS)

Mirae Asset Mutual Fund

135781;INF769K01DM9;INF769K01DN7;Mirae Asset ELSS Tax Saver Fund - Direct Plan - Growth;58.761;05-Aug-2026
135782;INF769K01DP2;-;Mirae Asset Large Cap Fund - Direct Plan - Growth;112.45;05-Aug-2026
120716;INF789F01XA0;INF789F01XB8;UTI Nifty 50 Index Fund - Growth Option- Direct;173.3618;04-Aug-2026
`

func TestParseNAVs(t *testing.T) {
	navs, err := parseNAVs(strings.NewReader(sampleAMFI))
	if err != nil {
		t.Fatalf("parseNAVs returned an error: %v", err)
	}

	if len(navs) != 5 {
		t.Errorf("len(navs) = %d, want 5", len(navs))
	}

	nav, ok := navs["INF769K01DM9"]
	if !ok {
		t.Fatal("expected INF769K01DM9 to be present")
	}
	if nav.Value != "58.761" {
		t.Errorf("Value = %q, want %q", nav.Value, "58.761")
	}

	if _, ok := navs["-"]; ok {
		t.Error(`"-" should never be a key`)
	}
	if _, ok := navs["ISIN Div Payout/ ISIN Growth"]; ok {
		t.Error("header row was parsed as data")
	}
}
