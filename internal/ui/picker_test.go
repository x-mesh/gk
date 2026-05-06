package ui

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestFallbackPicker_Pick verifies that a numeric selection returns the correct item.
func TestFallbackPicker_Pick(t *testing.T) {
	items := []PickerItem{
		{Display: "alpha", Key: "a"},
		{Display: "beta", Key: "b"},
		{Display: "gamma", Key: "c"},
	}
	in := strings.NewReader("2\n")
	var out strings.Builder
	p := &FallbackPicker{In: in, Out: &out}

	got, err := p.Pick(context.Background(), "choose", items)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Key != "b" {
		t.Fatalf("expected key=%q, got %q", "b", got.Key)
	}
	if got.Display != "beta" {
		t.Fatalf("expected display=%q, got %q", "beta", got.Display)
	}
}

// TestFallbackPicker_EmptyInput verifies that EOF/empty input returns ErrPickerAborted.
func TestFallbackPicker_EmptyInput(t *testing.T) {
	items := []PickerItem{{Display: "only", Key: "only"}}
	in := strings.NewReader("") // EOF immediately
	var out strings.Builder
	p := &FallbackPicker{In: in, Out: &out}

	_, err := p.Pick(context.Background(), "choose", items)
	if !errors.Is(err, ErrPickerAborted) {
		t.Fatalf("expected ErrPickerAborted, got %v", err)
	}
}

// TestFallbackPicker_Quit verifies that "q" returns ErrPickerAborted.
func TestFallbackPicker_Quit(t *testing.T) {
	items := []PickerItem{{Display: "only", Key: "only"}}
	in := strings.NewReader("q\n")
	var out strings.Builder
	p := &FallbackPicker{In: in, Out: &out}

	_, err := p.Pick(context.Background(), "choose", items)
	if !errors.Is(err, ErrPickerAborted) {
		t.Fatalf("expected ErrPickerAborted, got %v", err)
	}
}

// TestFallbackPicker_InvalidNumber verifies that out-of-range input returns an error
// containing "invalid selection".
func TestFallbackPicker_InvalidNumber(t *testing.T) {
	items := []PickerItem{
		{Display: "alpha", Key: "a"},
		{Display: "beta", Key: "b"},
	}
	in := strings.NewReader("99\n")
	var out strings.Builder
	p := &FallbackPicker{In: in, Out: &out}

	_, err := p.Pick(context.Background(), "choose", items)
	if err == nil {
		t.Fatal("expected error for invalid selection, got nil")
	}
	if !strings.Contains(err.Error(), "invalid selection") {
		t.Fatalf("expected 'invalid selection' in error, got %q", err.Error())
	}
}

// TestFallbackPicker_NoItems verifies that an empty item slice returns an error.
func TestFallbackPicker_NoItems(t *testing.T) {
	var out strings.Builder
	p := &FallbackPicker{In: strings.NewReader(""), Out: &out}

	_, err := p.Pick(context.Background(), "choose", nil)
	if err == nil {
		t.Fatal("expected error for no items, got nil")
	}
	if !strings.Contains(err.Error(), "no items to pick") {
		t.Fatalf("expected 'no items to pick' in error, got %q", err.Error())
	}
}
